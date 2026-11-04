// Offline evaluation of the rendered admission-policy chart with the Kubernetes CEL
// libraries (the same compiler and cost budget the API server uses).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	v1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/matchconditions"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/cel/environment"
)

type kindInfo struct{ group, version, resource string }

var kinds = map[string]kindInfo{
	"Namespace": {"", "v1", "namespaces"}, "Pod": {"", "v1", "pods"}, "ConfigMap": {"", "v1", "configmaps"},
	"Secret": {"", "v1", "secrets"}, "ReplicationController": {"", "v1", "replicationcontrollers"}, "Service": {"", "v1", "services"}, "ServiceAccount": {"", "v1", "serviceaccounts"},
	"Deployment": {"apps", "v1", "deployments"}, "StatefulSet": {"apps", "v1", "statefulsets"},
	"DaemonSet": {"apps", "v1", "daemonsets"}, "ReplicaSet": {"apps", "v1", "replicasets"},
	"Job": {"batch", "v1", "jobs"}, "CronJob": {"batch", "v1", "cronjobs"},
	"NetworkPolicy": {"networking.k8s.io", "v1", "networkpolicies"}, "Ingress": {"networking.k8s.io", "v1", "ingresses"},
	"Middleware": {"traefik.io", "v1alpha1", "middlewares"}, "ServersTransport": {"traefik.io", "v1alpha1", "serverstransports"},
	"VaultConnection":          {"secrets.hashicorp.com", "v1beta1", "vaultconnections"},
	"VaultAuth":                {"secrets.hashicorp.com", "v1beta1", "vaultauths"},
	"VaultStaticSecret":        {"secrets.hashicorp.com", "v1beta1", "vaultstaticsecrets"},
	"VaultDynamicSecret":       {"secrets.hashicorp.com", "v1beta1", "vaultdynamicsecrets"},
	"CustomResourceDefinition": {"apiextensions.k8s.io", "v1", "customresourcedefinitions"},
	"ClusterRole":              {"rbac.authorization.k8s.io", "v1", "clusterroles"}, "ClusterRoleBinding": {"rbac.authorization.k8s.io", "v1", "clusterrolebindings"},
	"Role": {"rbac.authorization.k8s.io", "v1", "roles"}, "RoleBinding": {"rbac.authorization.k8s.io", "v1", "rolebindings"},
	"MutatingWebhookConfiguration":   {"admissionregistration.k8s.io", "v1", "mutatingwebhookconfigurations"},
	"ValidatingWebhookConfiguration": {"admissionregistration.k8s.io", "v1", "validatingwebhookconfigurations"},
	"AppProject":                     {"argoproj.io", "v1alpha1", "appprojects"}, "Application": {"argoproj.io", "v1alpha1", "applications"},
	"ApplicationSet":  {"argoproj.io", "v1alpha1", "applicationsets"},
	"RabbitmqCluster": {"rabbitmq.com", "v1beta1", "rabbitmqclusters"}, "Shovel": {"rabbitmq.com", "v1beta1", "shovels"},
	"Federation": {"rabbitmq.com", "v1beta1", "federations"}, "Queue": {"rabbitmq.com", "v1beta1", "queues"},
}

type compiled struct {
	pol *v1.ValidatingAdmissionPolicy
	val validating.Validator
}

var env *environment.EnvSet

func compile(p *v1.ValidatingAdmissionPolicy) validating.Validator {
	opt := cel.OptionalVariableDeclarations{HasParams: false, HasAuthorizer: true}
	eopt := cel.OptionalVariableDeclarations{HasParams: false, HasAuthorizer: false}
	fc, err := cel.NewCompositedCompiler(env)
	must(err)
	vars := make([]cel.NamedExpressionAccessor, len(p.Spec.Variables))
	for i, v := range p.Spec.Variables {
		vars[i] = &validating.Variable{Name: v.Name, Expression: v.Expression}
	}
	fc.CompileAndStoreVariables(vars, opt, environment.NewExpressions)
	var matcher matchconditions.Matcher
	if len(p.Spec.MatchConditions) > 0 {
		acc := make([]cel.ExpressionAccessor, len(p.Spec.MatchConditions))
		for i := range p.Spec.MatchConditions {
			acc[i] = (*matchconditions.MatchCondition)(&p.Spec.MatchConditions[i])
		}
		matcher = matchconditions.NewMatcher(fc.CompileCondition(acc, opt, environment.NewExpressions), p.Spec.FailurePolicy, "policy", "validate", p.Name)
	}
	va := make([]cel.ExpressionAccessor, len(p.Spec.Validations))
	for i, v := range p.Spec.Validations {
		va[i] = &validating.ValidationCondition{Expression: v.Expression, Message: v.Message, Reason: v.Reason}
	}
	return validating.NewValidator(
		fc.CompileCondition(va, opt, environment.NewExpressions), matcher,
		fc.CompileCondition(nil, opt, environment.NewExpressions),
		fc.CompileCondition(make([]cel.ExpressionAccessor, len(p.Spec.Validations)), eopt, environment.NewExpressions),
		p.Spec.FailurePolicy, nil)
}

type chart struct {
	policies map[string]*compiled
	bindings []*v1.ValidatingAdmissionPolicyBinding
}

func decodeDocs(b []byte) []map[string]any {
	var out []map[string]any
	d := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	for {
		var m map[string]any
		if err := d.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			must(err)
		}
		if m != nil {
			out = append(out, m)
		}
	}
	return out
}

func loadChart(path string) *chart {
	b, err := os.ReadFile(path)
	must(err)
	c := &chart{policies: map[string]*compiled{}}
	for _, m := range decodeDocs(b) {
		raw, _ := json.Marshal(m)
		switch m["kind"] {
		case "ValidatingAdmissionPolicy":
			p := &v1.ValidatingAdmissionPolicy{}
			must(json.Unmarshal(raw, p))
			c.policies[p.Name] = &compiled{pol: p, val: compile(p)}
		case "ValidatingAdmissionPolicyBinding":
			bnd := &v1.ValidatingAdmissionPolicyBinding{}
			must(json.Unmarshal(raw, bnd))
			c.bindings = append(c.bindings, bnd)
		}
	}
	return c
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// ruleMatches follows the apiserver's matching of a rule: "*" matches every resource but no
// subresource, "*/*" every subresource, "pods/*" and "*/status" the obvious; apiVersions are
// honoured ("*" matches any).
func ruleMatches(rules []v1.NamedRuleWithOperations, info kindInfo, sub string, op string) bool {
	has := func(list []string, v string) bool {
		for _, x := range list {
			if x == v || x == "*" {
				return true
			}
		}
		return false
	}
	resOK := func(list []string) bool {
		for _, x := range list {
			if sub == "" {
				if x == info.resource || x == "*" {
					return true
				}
				continue
			}
			if x == info.resource+"/"+sub || x == info.resource+"/*" || x == "*/"+sub || x == "*/*" {
				return true
			}
		}
		return false
	}
	for _, r := range rules {
		ops := make([]string, len(r.Operations))
		for i, o := range r.Operations {
			ops[i] = string(o)
		}
		if has(ops, op) && has(r.APIGroups, info.group) && has(r.APIVersions, info.version) && resOK(r.Resources) {
			return true
		}
	}
	return false
}

type Req struct {
	User  string
	Op    string // CREATE UPDATE DELETE CONNECT
	Sub   string
	Obj   map[string]any
	Old   map[string]any
	NsObj map[string]any // namespace object (labels, deletionTimestamp)
}

type Result struct {
	Binding  string
	Action   string // admit deny error
	Messages []string
}

func (c *chart) eval(r Req) []Result {
	src := r.Obj
	if src == nil {
		src = r.Old
	}
	kind, _ := src["kind"].(string)
	info, ok := kinds[kind]
	if !ok {
		panic("unknown kind " + kind)
	}
	md := src["metadata"].(map[string]any)
	name, _ := md["name"].(string)
	ns, _ := md["namespace"].(string)
	if kind == "Namespace" {
		ns = name
	}
	var out []Result
	for _, b := range c.bindings {
		cp := c.policies[b.Spec.PolicyName]
		if cp.pol.Spec.MatchConstraints == nil || !ruleMatches(cp.pol.Spec.MatchConstraints.ResourceRules, info, r.Sub, r.Op) {
			continue
		}
		if b.Spec.MatchResources != nil && len(b.Spec.MatchResources.ResourceRules) > 0 &&
			!ruleMatches(b.Spec.MatchResources.ResourceRules, info, r.Sub, r.Op) {
			continue
		}
		var obj, old *unstructured.Unstructured
		if r.Obj != nil {
			obj = &unstructured.Unstructured{Object: deepCopy(r.Obj)}
		}
		if r.Old != nil {
			old = &unstructured.Unstructured{Object: deepCopy(r.Old)}
		}
		gvk := schema.GroupVersionKind{Group: info.group, Version: info.version, Kind: kind}
		gvr := schema.GroupVersionResource{Group: info.group, Version: info.version, Resource: info.resource}
		var attrs admission.Attributes
		ui := &user.DefaultInfo{Name: r.User}
		switch {
		case obj != nil && old != nil:
			attrs = admission.NewAttributesRecord(obj, old, gvk, ns, name, gvr, r.Sub, admission.Operation(r.Op), nil, false, ui)
		case obj != nil:
			attrs = admission.NewAttributesRecord(obj, nil, gvk, ns, name, gvr, r.Sub, admission.Operation(r.Op), nil, false, ui)
		default:
			attrs = admission.NewAttributesRecord(nil, old, gvk, ns, name, gvr, r.Sub, admission.Operation(r.Op), nil, false, ui)
		}
		va := &admission.VersionedAttributes{Attributes: attrs, VersionedKind: gvk}
		if obj != nil {
			va.VersionedObject = obj
		}
		if old != nil {
			va.VersionedOldObject = old
		}
		nso := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if r.NsObj != nil {
			raw, _ := json.Marshal(r.NsObj)
			must(json.Unmarshal(raw, nso))
		}
		res := cp.val.Validate(context.Background(), gvr, va, nil, nso, celconfig.RuntimeCELCostBudget, nil)
		for _, d := range res.Decisions {
			switch d.Action {
			case validating.ActionDeny:
				act := "deny"
				if d.Evaluation == validating.EvalError {
					act = "error"
				}
				out = append(out, Result{Binding: b.Name, Action: act, Messages: []string{d.Message}})
			}
		}
	}
	return out
}

func deepCopy(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var o map[string]any
	must(json.Unmarshal(b, &o))
	return o
}

// ---- helpers to mutate fixtures by path ----

func walk(root any, parts []string) (parent any, last string) {
	cur := root
	for _, p := range parts[:len(parts)-1] {
		cur = step(cur, p)
	}
	return cur, parts[len(parts)-1]
}

func step(cur any, p string) any {
	switch c := cur.(type) {
	case map[string]any:
		if _, ok := c[p]; !ok {
			c[p] = map[string]any{}
		}
		return c[p]
	case []any:
		i, _ := strconv.Atoi(p)
		return c[i]
	}
	panic("bad path " + p)
}

func set(o map[string]any, path string, v any) map[string]any {
	parts := splitPath(path)
	par, last := walk(o, parts)
	switch c := par.(type) {
	case map[string]any:
		c[last] = v
	case []any:
		i, _ := strconv.Atoi(last)
		c[i] = v
	}
	return o
}

func del(o map[string]any, path string) map[string]any {
	parts := splitPath(path)
	par, last := walk(o, parts)
	if c, ok := par.(map[string]any); ok {
		delete(c, last)
	}
	return o
}

func jv(s string) any {
	var v any
	must(json.Unmarshal([]byte(s), &v))
	return v
}

// ---- fixtures ----

var docs []map[string]any

func load(path string) {
	b, err := os.ReadFile(path)
	must(err)
	docs = append(docs, decodeDocs(b)...)
}

func find(kind, ns, name string) map[string]any {
	for _, d := range docs {
		if d["kind"] != kind {
			continue
		}
		md := d["metadata"].(map[string]any)
		if md["name"] == name && (ns == "" || md["namespace"] == ns) {
			return deepCopy(d)
		}
	}
	panic(fmt.Sprintf("fixture %s %s/%s not found", kind, ns, name))
}

func defaults(d map[string]any) map[string]any {
	switch d["kind"] {
	case "VaultAuth":
		if k, ok := d["spec"].(map[string]any)["kubernetes"].(map[string]any); ok {
			if _, has := k["tokenExpirationSeconds"]; !has {
				k["tokenExpirationSeconds"] = float64(600)
			}
		}
	case "VaultStaticSecret":
		s := d["spec"].(map[string]any)
		if _, has := s["hmacSecretData"]; !has {
			s["hmacSecretData"] = true
		}
	case "CustomResourceDefinition":
		s := d["spec"].(map[string]any)
		if _, has := s["conversion"]; !has {
			s["conversion"] = map[string]any{"strategy": "None"}
		}
	}
	return d
}

func podOf(d map[string]any, extraOwner map[string]any) map[string]any {
	spec := d["spec"].(map[string]any)
	var tpl map[string]any
	if d["kind"] == "CronJob" {
		tpl = spec["jobTemplate"].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)
	} else {
		tpl = spec["template"].(map[string]any)
	}
	md := d["metadata"].(map[string]any)
	pmd := map[string]any{"name": md["name"].(string) + "-abc12", "namespace": md["namespace"]}
	if l, ok := tpl["metadata"].(map[string]any); ok {
		pmd["labels"] = l["labels"]
	}
	if extraOwner != nil {
		pmd["ownerReferences"] = []any{extraOwner}
	}
	return map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": pmd, "spec": deepCopy(tpl["spec"].(map[string]any))}
}

var failures int

// results maps each case to the messages it was refused with ("" cases are absent); it is
// compared with testdata/messages.golden, so a negative that is refused by the wrong rule
// (or for a new reason) shows up as a diff. UPDATE_GOLDEN=1 rewrites the file.
var results = map[string][]string{}

func report(c *chart, name string, r Req, want []string) {
	if _, dup := results[name]; dup {
		panic("duplicate case name: " + name)
	}
	res := c.eval(r)
	maybeEmit(name, r, res)
	var msgs, plain []string
	errored := false
	for _, x := range res {
		msgs = append(msgs, x.Action+":"+x.Messages[0])
		plain = append(plain, x.Binding+": "+x.Messages[0])
		if x.Action == "error" {
			errored = true
		}
	}
	sort.Strings(plain)
	results[name] = plain
	ok := false
	if len(want) == 0 {
		ok = len(res) == 0
	} else {
		ok = len(res) > 0
		for _, w := range want {
			found := false
			for _, m := range msgs {
				if strings.Contains(m, w) {
					found = true
				}
			}
			ok = ok && found
		}
	}
	if errored {
		ok = false // a CEL error is never an acceptable refusal
	}
	status := "ok  "
	if !ok {
		status = "FAIL"
		failures++
	}
	fmt.Printf("%s %-62s want=%v got=%v\n", status, name, want, trim(msgs))
}

// checkGolden compares the refusals of this run with testdata/messages.golden.
func checkGolden() {
	var names []string
	for n, m := range results {
		// cases of the release's own ClusterRoles exist only in a render from before T12-g; they
		// are asserted by their wanted message, not pinned here, so the run is green on both
		if len(m) > 0 && !strings.Contains(n, "(before T12-g)") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + "\n")
		for _, m := range results[n] {
			b.WriteString("    " + m + "\n")
		}
	}
	path := "testdata/messages.golden"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		must(os.WriteFile(path, []byte(b.String()), 0o644))
		fmt.Println("golden rewritten:", path)
		return
	}
	want, err := os.ReadFile(path)
	must(err)
	if string(want) != b.String() {
		// Diff per case: a case whose refusals changed is named once, with both versions.
		parse := func(text string) map[string][]string {
			out := map[string][]string{}
			cur := ""
			for _, l := range strings.Split(text, "\n") {
				switch {
				case l == "":
				case strings.HasPrefix(l, "    "):
					out[cur] = append(out[cur], strings.TrimPrefix(l, "    "))
				default:
					cur = l
					out[cur] = nil
				}
			}
			return out
		}
		wm, gm := parse(string(want)), parse(b.String())
		var changed []string
		for n, w := range wm {
			g, ok := gm[n]
			switch {
			case !ok:
				changed = append(changed, n+"\n    was refused with: "+strings.Join(w, " | ")+"\n    now: admitted")
			case strings.Join(w, "\n") != strings.Join(g, "\n"):
				changed = append(changed, n+"\n    was: "+strings.Join(w, " | ")+"\n    now: "+strings.Join(g, " | "))
			}
		}
		for n, g := range gm {
			if _, ok := wm[n]; !ok {
				changed = append(changed, n+"\n    new refusal: "+strings.Join(g, " | "))
			}
		}
		sort.Strings(changed)
		fmt.Printf("FAIL %d case(s) are refused differently from %s (UPDATE_GOLDEN=1 rewrites it after a review):\n", len(changed), path)
		for i, c := range changed {
			if i == 25 {
				fmt.Printf("  ... and %d more\n", len(changed)-i)
				break
			}
			fmt.Println("  " + c)
		}
		if len(changed) == 0 {
			fmt.Println("  (the file differs in layout only: regenerate it)")
		}
		failures++
	}
}

func trim(m []string) []string {
	out := make([]string, len(m))
	for i, s := range m {
		if len(s) > 90 && os.Getenv("FULL") == "" {
			s = s[:90]
		}
		out[i] = s
	}
	sort.Strings(out)
	return out
}

const (
	argoCtl = "system:serviceaccount:argocd:argocd-application-controller"
	argoSrv = "system:serviceaccount:argocd:argocd-server"
	cop     = "system:serviceaccount:rabbitmq-system:rabbitmq-cluster-operator"
	top     = "system:serviceaccount:rabbitmq-system:messaging-topology-operator"
	admin   = "kubernetes-admin"
)

func main() {
	env = environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion())
	c := loadChart(os.Args[1])
	for _, f := range os.Args[2:] {
		load(f)
	}
	runPositive(c)
	printUnmodelled()
	runNegative(c)
	runArgo(c)
	runHardening(c)
	checkGolden()
	writeFixtures()
	fmt.Println("cases:", len(results), "failures:", failures)
	if failures > 0 {
		os.Exit(1)
	}
}

var unmodelled = map[string]int{}

func printUnmodelled() {
	var ks []string
	for k, n := range unmodelled {
		ks = append(ks, fmt.Sprintf("%s(%d)", k, n))
	}
	sort.Strings(ks)
	fmt.Println("== kinds of the renders that no policy can match, not evaluated:", strings.Join(ks, " "))
}

func runPositive(c *chart) {
	fmt.Println("== every render of the repository passes (create and update, as Argo CD's controller)")
	for _, d := range docs {
		d = defaults(deepCopy(d))
		kind := d["kind"].(string)
		if _, ok := kinds[kind]; !ok {
			unmodelled[kind]++
			continue
		}
		md := d["metadata"].(map[string]any)
		user := argoCtl
		label := fmt.Sprintf("%s %v/%v", kind, md["namespace"], md["name"])
		var want []string
		if kind == "ClusterRole" && grantsWhatT12gRemoved(d) {
			want = []string{"P6: a ClusterRole of the operators must not grant Secrets"} // the release's roles, before T12-g
			label += " (before T12-g)"
		}
		report(c, "create "+label, Req{User: user, Op: "CREATE", Obj: d}, want)
		report(c, "update "+label, Req{User: user, Op: "UPDATE", Obj: d, Old: d}, want)
		switch kind {
		case "Deployment", "StatefulSet", "Job", "CronJob", "DaemonSet":
			p := podOf(d, nil)
			report(c, "pod of "+label, Req{User: "system:serviceaccount:kube-system:replicaset-controller", Op: "CREATE", Obj: p}, nil)
		}
	}
}

func brokerSts() map[string]any {
	return fx("statefulset-rabbitmq-server")
}

func brokerPod() map[string]any {
	s := brokerSts()
	p := podOf(s, map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "rabbitmq-server", "controller": true, "uid": "u"})
	p["metadata"].(map[string]any)["name"] = "rabbitmq-server-0"
	return p
}

func runNegative(c *chart) {
	fmt.Println("== the broker, the operator-written objects and exemptions")
	report(c, "broker statefulset by the cluster operator", Req{User: cop, Op: "CREATE", Obj: brokerSts()}, nil)
	report(c, "broker pod owned by the statefulset", Req{User: "system:serviceaccount:kube-system:statefulset-controller", Op: "CREATE", Obj: brokerPod()}, nil)
	report(c, "broker statefulset by someone else (commands, token)", Req{User: admin, Op: "CREATE", Obj: brokerSts()}, []string{"P2"})
	bp := brokerPod()
	delete(bp["metadata"].(map[string]any), "ownerReferences")
	report(c, "broker pod without the owner (commands, token)", Req{User: admin, Op: "CREATE", Obj: bp}, []string{"P2"})
	// A forged ownerReference: the owner is written by the requester, so the creator counts.
	report(c, "broker pod with a forged ownerRef, created by an admin", Req{User: admin, Op: "CREATE", Obj: brokerPod()}, []string{"P2"})
	report(c, "broker pod with a forged ownerRef, created by the cluster operator", Req{User: cop, Op: "CREATE", Obj: brokerPod()}, []string{"P2"})
	report(c, "broker pod created by the real statefulset-controller", Req{User: "system:serviceaccount:kube-system:statefulset-controller", Op: "CREATE", Obj: brokerPod()}, nil)
	plain := brokerPod()
	delete(plain["metadata"].(map[string]any), "ownerReferences")
	forgedUpdate := brokerPod()
	report(c, "ownerRef added to a plain pod by an update (judged on images only)", Req{User: admin, Op: "UPDATE", Obj: forgedUpdate, Old: plain}, nil)
	report(c, "update of the owned pod that keeps the ownerRef", Req{User: cop, Op: "UPDATE", Obj: brokerPod(), Old: brokerPod()}, nil)
	// restartedAt-only update by any writer
	oldS := brokerSts()
	newS := deepCopy(oldS)
	set(newS, "spec/template/metadata/annotations/kubectl.kubernetes.io~restartedAt", "x")
	newS2 := brokerSts()
	newS2["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)["kubectl.kubernetes.io/restartedAt"] = "2026-10-23T10:00:00Z"
	report(c, "kubectl rollout restart (restartedAt only) by an admin", Req{User: admin, Op: "UPDATE", Obj: newS2, Old: oldS}, nil)
	other := deepCopy(newS2)
	set(other, "spec/replicas", float64(5))
	report(c, "restartedAt plus replicas change by an admin", Req{User: admin, Op: "UPDATE", Obj: other, Old: oldS}, []string{"P2"})
	other2 := deepCopy(newS2)
	set(other2, "spec/template/spec/containers/0/command", jv(`["sh"]`))
	report(c, "restartedAt plus a command by an admin", Req{User: admin, Op: "UPDATE", Obj: other2, Old: oldS}, []string{"P2"})
	other3 := deepCopy(newS2)
	set(other3, "spec/template/metadata/annotations/foo", "bar")
	report(c, "restartedAt plus another annotation by an admin", Req{User: admin, Op: "UPDATE", Obj: other3, Old: oldS}, []string{"P2"})
	bs := brokerSts()
	set(bs, "spec/template/spec/containers/0/image", "docker.io/library/rabbitmq:4.3.6")
	report(c, "broker image without digest", Req{User: cop, Op: "CREATE", Obj: bs}, []string{"P2"})
	bs2 := brokerSts()
	set(bs2, "spec/template/spec/containers/0/env", jv(`[{"name":"X","valueFrom":{"secretKeyRef":{"name":"vault-auth-token","key":"token"}}}]`))
	report(c, "broker env from vault-auth-token", Req{User: cop, Op: "CREATE", Obj: bs2}, []string{"P2"})

	p2 := func() map[string]any { return find("Deployment", "ryft-notify", "notify") }
	type n struct {
		name string
		req  Req
		want []string
	}
	var ns []n
	add := func(name string, user, op string, obj map[string]any, want ...string) {
		r := Req{User: user, Op: op, Obj: obj}
		if op == "UPDATE" {
			r.Old = deepCopy(obj)
		}
		ns = append(ns, n{name, r, want})
	}
	fmt.Println("== P2 negatives")
	add("P2 other image", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/image", "ghcr.io/timkrebs/ryft/api:7b4886f"), "P2")
	add("P2 image tag latest", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/image", "ghcr.io/timkrebs/ryft/notify:latest"), "P2")
	add("P2 image from another registry", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/image", "docker.io/ryft/notify:7b4886f"), "P2")
	add("P2 command", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/command", jv(`["sh"]`)), "P2")
	add("P2 exec probe", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/livenessProbe", jv(`{"exec":{"command":["true"]}}`)), "P2")
	add("P2 exec preStop", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/lifecycle", jv(`{"preStop":{"exec":{"command":["true"]}}}`)), "P2")
	add("P2 http preStop is fine", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/lifecycle", jv(`{"preStop":{"sleep":{"seconds":5}}}`)))
	add("P2 args", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/args", jv(`["x"]`)), "P2")
	add("P2 token automount true", argoCtl, "CREATE", set(p2(), "spec/template/spec/automountServiceAccountToken", true), "P2")
	add("P2 token automount absent", argoCtl, "CREATE", del(p2(), "spec/template/spec/automountServiceAccountToken"), "P2")
	add("P2 projected token", argoCtl, "CREATE", set(p2(), "spec/template/spec/volumes", jv(`[{"name":"t","projected":{"sources":[{"serviceAccountToken":{"path":"t"}}]}}]`)), "P2")
	add("P2 vault-auth-token volume", argoCtl, "CREATE", set(p2(), "spec/template/spec/volumes", jv(`[{"name":"t","secret":{"secretName":"vault-auth-token"}}]`)), "P2")
	add("P2 vault-auth-token envFrom", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/envFrom", jv(`[{"secretRef":{"name":"vault-auth-token"}}]`)), "P2")
	add("P2 vault-auth-token env", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/env", jv(`[{"name":"X","valueFrom":{"secretKeyRef":{"name":"vault-auth-token","key":"k"}}}]`)), "P2")
	add("P2 image volume", argoCtl, "CREATE", set(p2(), "spec/template/spec/volumes", jv(`[{"name":"i","image":{"reference":"x"}}]`)), "P2")
	add("P2 init container with another image", argoCtl, "CREATE", set(p2(), "spec/template/spec/initContainers", jv(`[{"name":"i","image":"busybox"}]`)), "P2")
	add("P2 init container command", argoCtl, "CREATE", set(p2(), "spec/template/spec/initContainers", jv(`[{"name":"i","image":"ghcr.io/timkrebs/ryft/notify:7b4886f","command":["x"]}]`)), "P2")
	cron := fx("cronjob-c")
	add("P2 cronjob with another image", argoCtl, "CREATE", cron, "P2")
	smoke := find("Job", "ryft-smoke", "smoke")
	add("P2 smoke with other args", argoCtl, "CREATE", set(smoke, "spec/template/spec/containers/0/args", jv(`["serve"]`)), "P2")
	mig := find("Job", "ryft-migrate", "migrate")
	add("P2 migrate with args smoke", argoCtl, "CREATE", set(mig, "spec/template/spec/containers/0/args", jv(`["smoke"]`)), "P2")
	op := find("Deployment", "rabbitmq-system", "rabbitmq-cluster-operator")
	add("P2 operator image tag moved to :latest", argoCtl, "CREATE", set(op, "spec/template/spec/containers/0/image", "ghcr.io/rabbitmq/cluster-operator:latest"), "P2")
	op2 := find("Deployment", "rabbitmq-system", "rabbitmq-cluster-operator")
	add("P2 operator with other command", argoCtl, "CREATE", set(op2, "spec/template/spec/containers/0/command", jv(`["sh"]`)), "P2")
	op3 := find("Deployment", "rabbitmq-system", "rabbitmq-cluster-operator")
	add("P2 operator with another image", argoCtl, "CREATE", set(op3, "spec/template/spec/containers/0/image", "docker.io/evil/op:1.0.0"), "P2")
	pod := podOf(p2(), nil)
	set(pod, "spec/volumes", jv(`[{"name":"kube-api-access-abc","projected":{"sources":[{"serviceAccountToken":{"path":"token"}}]}}]`))
	add("P2 pod with kube-api-access", "system:serviceaccount:kube-system:replicaset-controller", "CREATE", pod, "P2")
	pod2 := podOf(p2(), nil)
	set(pod2, "spec/containers/0/image", "ghcr.io/timkrebs/ryft/notify:abc")
	add("P2 pod short sha", "system:serviceaccount:kube-system:replicaset-controller", "CREATE", pod2, "P2")
	// pods binding vs templates: both matched through one policy
	// finalizer carve-out
	fmt.Println("== P1, P3, P4, P5, P9, P11 negatives")
	nsw := find("Namespace", "", "ryft-web")
	add("P1 label missing", admin, "CREATE", del(nsw, "metadata/labels/pod-security.kubernetes.io~enforce"), "P1")
	nsw1 := find("Namespace", "", "ryft-web")
	nsw1["metadata"].(map[string]any)["labels"].(map[string]any)["pod-security.kubernetes.io/enforce"] = "baseline"
	add("P1 enforce baseline", admin, "UPDATE", nsw1, "P1")
	nsw2 := find("Namespace", "", "ryft-web")
	nsw2["metadata"].(map[string]any)["labels"].(map[string]any)["pod-security.kubernetes.io/enforce-version"] = "v1.30"
	add("P1 version label pinned", admin, "UPDATE", nsw2, "P1")
	nsw3 := find("Namespace", "", "ryft-web")
	nsw3["metadata"].(map[string]any)["labels"].(map[string]any)["pod-security.kubernetes.io/enforce-version"] = "latest"
	add("P1 version label latest is fine", admin, "UPDATE", nsw3)
	nsx := fx("namespace-ryft-evil")
	add("P1 argo creates another namespace", argoCtl, "CREATE", nsx, "P1")
	nsm := fx("namespace-monitoring")
	add("P1 monitoring stays outside (privileged)", admin, "UPDATE", nsm)
	nsa := fx("namespace-argocd")
	add("P1 argocd namespace privileged", admin, "UPDATE", nsa, "P1")
	nsg := fx("namespace-ryft-")
	add("P1 generateName ryft- without labels", admin, "CREATE", nsg, "P1")

	np := func(ns, name string) map[string]any { return find("NetworkPolicy", ns, name) }
	add("P3 rule without ports", argoCtl, "CREATE", set(np("ryft-api", "api"), "spec/ingress/0/ports", jv(`[]`)), "P3")
	add("P3 allow-all ingress rule", argoCtl, "CREATE", set(np("ryft-api", "api"), "spec/ingress", jv(`[{}]`)), "P3")
	add("P3 port entry without port", argoCtl, "CREATE", set(np("ryft-api", "api"), "spec/ingress/0/ports", jv(`[{"protocol":"TCP"}]`)), "P3")
	add("P3 empty namespaceSelector", argoCtl, "CREATE", set(np("ryft-api", "api"), "spec/ingress/0/from/0/namespaceSelector", jv(`{}`)), "P3")
	add("P3 namespaceSelector expressions", argoCtl, "CREATE", set(np("ryft-api", "api"), "spec/ingress/0/from/0/namespaceSelector", jv(`{"matchExpressions":[{"key":"a","operator":"Exists"}]}`)), "P3")
	add("P3 ingress ipBlock world", argoCtl, "CREATE", set(np("ryft-api", "allow-metrics-from-nodes"), "spec/ingress/0/from/0/ipBlock/cidr", "0.0.0.0/0"), "P3")
	add("P3 ingress ipBlock vpc other port", argoCtl, "CREATE", set(np("ryft-api", "allow-metrics-from-nodes"), "spec/ingress/0/ports/0/port", float64(8080)), "P3")
	add("P3 egress world on 22", argoCtl, "CREATE", set(np("ryft-api", "api"), "spec/egress/1/ports", jv(`[{"port":22,"protocol":"TCP"}]`)), "P3")
	add("P3 egress world without except", argoCtl, "CREATE", del(np("ryft-api", "api"), "spec/egress/1/to/0/ipBlock/except"), "P3")
	add("P3 egress world with 4 excepts", argoCtl, "CREATE", set(np("ryft-api", "api"), "spec/egress/1/to/0/ipBlock/except", jv(`["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","100.64.0.0/10"]`)), "P3")
	add("P3 endPort outside the broker", argoCtl, "CREATE", set(np("ryft-api", "api"), "spec/egress/0/ports/0/endPort", float64(5680)), "P3")
	add("P3 default-deny with a rule", argoCtl, "CREATE", set(np("ryft-api", "default-deny"), "spec/ingress", jv(`[{"from":[{"podSelector":{}}],"ports":[{"port":1}]}]`)), "P3")
	add("P3 default-deny narrowed", argoCtl, "CREATE", set(np("ryft-api", "default-deny"), "spec/podSelector", jv(`{"matchLabels":{"a":"b"}}`)), "P3")
	add("P3 default-deny with only Ingress", argoCtl, "CREATE", set(np("ryft-api", "default-deny"), "spec/policyTypes", jv(`["Ingress"]`)), "P3")
	add("P3 named port ok", argoCtl, "CREATE", set(np("ryft-api", "allow-metrics-from-nodes"), "spec/ingress/0/ports/0/port", "metrics"), "P3")

	vc := func() map[string]any { return find("VaultConnection", "ryft-api", "vault") }
	add("P4 other vault address", admin, "CREATE", set(vc(), "spec/address", "https://evil.example:8200"), "P4")
	add("P4 skipTLSVerify", admin, "CREATE", set(vc(), "spec/skipTLSVerify", true), "P4")
	add("P4 caCertPath", admin, "CREATE", set(vc(), "spec/caCertPath", "/x"), "P4")
	va := func() map[string]any { return defaults(find("VaultAuth", "ryft-api", "api")) }
	add("P4 allowedNamespaces", admin, "CREATE", set(va(), "spec/allowedNamespaces", jv(`["*"]`)), "P4")
	add("P4 other audience", admin, "CREATE", set(va(), "spec/kubernetes/audiences", jv(`["x"]`)), "P4")
	add("P4 other connection", admin, "CREATE", set(va(), "spec/vaultConnectionRef", "other/vault"), "P4")
	add("P4 method jwt", admin, "CREATE", set(va(), "spec/method", "jwt"), "P4")
	vs := func() map[string]any { return defaults(find("VaultStaticSecret", "ryft-api", "ryft-secrets")) }
	add("P4 cross-namespace vaultAuthRef", admin, "CREATE", set(vs(), "spec/vaultAuthRef", "ryft-notify/notify"), "P4")
	add("P4 destination to another secret", admin, "CREATE", set(vs(), "spec/destination/name", "vault-auth-token"), "P4")
	add("P4 transformation templates", admin, "CREATE", set(vs(), "spec/destination/transformation/templates", jv(`{"x":{"text":"y"}}`)), "P4")
	add("P4 excludeRaw false", admin, "CREATE", set(vs(), "spec/destination/transformation/excludeRaw", false), "P4")
	add("P4 vault namespace override", admin, "CREATE", set(vs(), "spec/namespace", "admin"), "P4")
	add("P4 other kind", admin, "CREATE", fx("vaultdynamicsecret-x"), "P4")

	cm := fx("configmap-c")
	add("P5 configmap AUTH_MODE apikey", argoCtl, "CREATE", cm, "P5")
	cm2 := fx("configmap-c-1")
	add("P5 configmap API_KEY", argoCtl, "CREATE", cm2, "P5")
	sc := fx("secret-ryft-secrets")
	add("P5 secret USER_ID (a Vault key synced by VSO)", "system:serviceaccount:vault-secrets-operator-system:vault-secrets-operator-controller-manager", "UPDATE", sc, "P5")
	sc2 := fx("secret-ryft-secrets-2")
	add("P5 secret AUTH_MODE", "system:serviceaccount:vault-secrets-operator-system:vault-secrets-operator-controller-manager", "UPDATE", sc2, "P5")
	sc3 := fx("secret-ryft-secrets-3")
	add("P5 secret with ordinary keys", "system:serviceaccount:vault-secrets-operator-system:vault-secrets-operator-controller-manager", "UPDATE", sc3)
	add("P5 env AUTH_MODE apikey", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/env", jv(`[{"name":"AUTH_MODE","value":"apikey"}]`)), "P5")
	add("P5 env AUTH_MODE supabase literal", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/env", jv(`[{"name":"AUTH_MODE","value":"supabase"}]`)))
	add("P5 env AUTH_MODE valueFrom", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/env", jv(`[{"name":"AUTH_MODE","valueFrom":{"configMapKeyRef":{"name":"x","key":"y"}}}]`)), "P5")
	add("P5 env API_KEY", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/env", jv(`[{"name":"API_KEY","valueFrom":{"secretKeyRef":{"name":"x","key":"y"}}}]`)), "P5")
	add("P5 envFrom prefix", argoCtl, "CREATE", set(p2(), "spec/template/spec/containers/0/envFrom/0/prefix", "API_"), "P5")

	mw := func() map[string]any { return find("Middleware", "ryft-api", "ratelimit") }
	add("P9 middleware plugin", argoCtl, "CREATE", set(mw(), "spec", jv(`{"plugin":{"x":{}}}`)), "P9")
	add("P9 middleware forwardAuth", argoCtl, "CREATE", set(mw(), "spec", jv(`{"forwardAuth":{"address":"http://x"}}`)), "P9")
	st := func() map[string]any { return find("ServersTransport", "ryft-rabbitmq", "rabbitmq-management") }
	add("P9 serverstransport insecure", argoCtl, "CREATE", set(st(), "spec/insecureSkipVerify", true), "P9")
	ig := func() map[string]any { return find("Ingress", "ryft-api", "api") }
	add("P9 ingress cross-namespace middleware", argoCtl, "CREATE", set(ig(), "metadata/annotations/traefik.ingress.kubernetes.io~router.middlewares", "ryft-ingest-ratelimit@kubernetescrd"), "P9")
	add("P9 ingress file middleware", argoCtl, "CREATE", set(ig(), "metadata/annotations/traefik.ingress.kubernetes.io~router.middlewares", "ryft-api-ratelimit@file"), "P9")
	add("P9 ingress two own middlewares", argoCtl, "CREATE", set(ig(), "metadata/annotations/traefik.ingress.kubernetes.io~router.middlewares", "ryft-api-ratelimit@kubernetescrd, ryft-api-hsts@kubernetescrd"))

	rc := func() map[string]any { return find("RabbitmqCluster", "ryft-rabbitmq", "rabbitmq") }
	add("P11 image tag only", argoCtl, "CREATE", set(rc(), "spec/image", "docker.io/library/rabbitmq:4.3.6-management"), "P11")
	add("P11 other registry", argoCtl, "CREATE", set(rc(), "spec/image", "evil.io/rabbitmq:1@sha256:54cb013d76cd1fd6c0a0708f34062ad61c452bb6f016b434c6c33f1a765d2b58"), "P11")
	add("P11 override with containers", argoCtl, "CREATE", set(rc(), "spec/override/statefulSet/spec/template/spec", jv(`{"containers":[{"name":"rabbitmq","command":["x"]}]}`)), "P11")
	add("P11 override service", argoCtl, "CREATE", set(rc(), "spec/override/service", jv(`{"spec":{"type":"LoadBalancer"}}`)), "P11")
	add("P11 no override is fine", argoCtl, "CREATE", del(rc(), "spec/override"))
	add("P11 shovel", argoCtl, "CREATE", fx("shovel-s"), "P11")
	add("P11 rabbitmqcluster elsewhere", argoCtl, "CREATE", set(rc(), "metadata/namespace", "ryft-api"), "P11")

	for _, x := range ns {
		report(c, x.name, x.req, x.want)
	}

	fmt.Println("== P6 negatives")
	// A clean ClusterRole of the operators (T12-g left reads of rabbitmqclusters and the
	// tokenreview roles); the mutations below each add one forbidden thing.
	cr := func() map[string]any {
		o := fx("clusterrole-rabbitmq-cluster-operator-role")
		o["rules"] = jv(`[{"apiGroups":["rabbitmq.com"],"resources":["rabbitmqclusters"],"verbs":["get","list","watch"]},{"apiGroups":[""],"resources":["endpoints"],"verbs":["get","list","watch"]}]`)
		return o
	}
	var ops6 []n
	add6 := func(name string, obj map[string]any, want ...string) {
		ops6 = append(ops6, n{name, Req{User: argoCtl, Op: "CREATE", Obj: obj}, want})
	}
	add6("P6 clusterrole misnamed", set(cr(), "metadata/name", "admin-role"), "P6")
	add6("P6 clusterrole wildcard", set(cr(), "rules/0/verbs", jv(`["*"]`)), "P6")
	add6("P6 clusterrole escalate", set(cr(), "rules/0/verbs", jv(`["escalate"]`)), "P6")
	add6("P6 clusterrole admission", set(cr(), "rules/0", jv(`{"apiGroups":["admissionregistration.k8s.io"],"resources":["validatingadmissionpolicies"],"verbs":["update"]}`)), "P6")
	add6("P6 clusterrole clusterrolebindings", set(cr(), "rules/0", jv(`{"apiGroups":["rbac.authorization.k8s.io"],"resources":["clusterrolebindings"],"verbs":["create"]}`)), "P6")
	add6("P6 clusterrole nodes", set(cr(), "rules/0", jv(`{"apiGroups":[""],"resources":["nodes"],"verbs":["get"]}`)), "P6")
	add6("P6 clusterrole serviceaccounts/token", set(cr(), "rules/0", jv(`{"apiGroups":[""],"resources":["serviceaccounts/token"],"verbs":["create"]}`)), "P6")
	add6("P6 clusterrole nonResourceURL", set(cr(), "rules/0", jv(`{"nonResourceURLs":["/healthz"],"verbs":["get"]}`)), "P6")
	add6("P6 clusterrole metrics URL is fine", set(cr(), "rules/0", jv(`{"nonResourceURLs":["/metrics"],"verbs":["get"]}`)))
	add6("P6 clusterrole aggregation label", set(cr(), "metadata/labels/rbac.authorization.k8s.io~aggregate-to-admin", "true"), "P6")
	add6("P6 clusterrole aggregationRule", set(cr(), "aggregationRule", jv(`{"clusterRoleSelectors":[]}`)), "P6")
	crb := func() map[string]any { return fx("clusterrolebinding-rabbitmq-cluster-operator-rolebinding") }
	add6("P6 binding to cluster-admin", set(crb(), "roleRef/name", "cluster-admin"), "P6")
	add6("P6 binding to another subject", set(crb(), "subjects/0/name", "default"), "P6")
	add6("P6 binding subject outside rabbitmq-system", set(crb(), "subjects/0/namespace", "argocd"), "P6")
	add6("P6 binding to a group", set(crb(), "subjects/0", jv(`{"kind":"Group","name":"system:authenticated"}`)), "P6")
	crd := func() map[string]any { return defaults(find("CustomResourceDefinition", "", "queues.rabbitmq.com")) }
	add6("P6 crd of another group", set(crd(), "spec/group", "evil.io"), "P6")
	add6("P6 crd conversion webhook", set(crd(), "spec/conversion", jv(`{"strategy":"Webhook"}`)), "P6")
	wh := func() map[string]any {
		return find("ValidatingWebhookConfiguration", "", "cluster-operator-validating-webhook-configuration")
	}
	add6("P6 webhook other name", set(wh(), "metadata/name", "evil"), "P6")
	add6("P6 webhook url", set(wh(), "webhooks/0/clientConfig", jv(`{"url":"https://evil.example/x"}`)), "P6")
	add6("P6 webhook service elsewhere", set(wh(), "webhooks/0/clientConfig/service/namespace", "ryft-api"), "P6")
	add6("P6 webhook on pods", set(wh(), "webhooks/0/rules/0/apiGroups", jv(`[""]`)), "P6")
	role := func() map[string]any { return find("Role", "rabbitmq-system", "rabbitmq-cluster-leader-election-role") }
	add6("P6 role in ryft-api", set(role(), "metadata/namespace", "ryft-api"), "P6")
	rb := func() map[string]any {
		return find("RoleBinding", "rabbitmq-system", "rabbitmq-cluster-leader-election-rolebinding")
	}
	add6("P6 rolebinding to clusterrole", set(rb(), "roleRef/kind", "ClusterRole"), "P6")
	add6("P6 rolebinding other subject", set(rb(), "subjects/0/name", "default"), "P6")
	add6("P6 rolebinding in ryft-api", set(rb(), "metadata/namespace", "ryft-api"), "P6")
	add6("P6 same role by someone else is not judged", set(cr(), "rules/0/verbs", jv(`["*"]`)))
	for i, x := range ops6 {
		if strings.Contains(x.name, "someone else") {
			ops6[i].req.User = admin
		}
	}
	for _, x := range ops6 {
		report(c, x.name, x.req, x.want)
	}

	fmt.Println("== P8 operator reach")
	sec := func(ns string) map[string]any {
		return jv(fmt.Sprintf(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"s","namespace":"%s"},"data":{"a":"Yg=="}}`, ns)).(map[string]any)
	}
	report(c, "cluster operator writes a secret in ryft-rabbitmq", Req{User: cop, Op: "CREATE", Obj: sec("ryft-rabbitmq")}, nil)
	report(c, "cluster operator writes a secret in ryft-api", Req{User: cop, Op: "CREATE", Obj: sec("ryft-api")}, []string{"P8"})
	report(c, "cluster operator writes a secret in kube-system", Req{User: cop, Op: "UPDATE", Obj: sec("kube-system"), Old: sec("kube-system")}, []string{"P8"})
	report(c, "topology operator writes a secret in ryft-rabbitmq", Req{User: top, Op: "CREATE", Obj: sec("ryft-rabbitmq")}, nil)
	report(c, "topology operator writes a secret in ryft-api", Req{User: top, Op: "CREATE", Obj: sec("ryft-api")}, []string{"P8"})
	svc := fx("service-s")
	report(c, "topology operator writes a service", Req{User: top, Op: "CREATE", Obj: svc}, []string{"P8"})
	report(c, "cluster operator writes a service in ryft-rabbitmq", Req{User: cop, Op: "CREATE", Obj: svc}, nil)
	pod8 := brokerPod()
	report(c, "cluster operator updates a broker pod", Req{User: cop, Op: "UPDATE", Obj: pod8, Old: pod8}, nil)
	report(c, "cluster operator creates a pod", Req{User: cop, Op: "CREATE", Obj: pod8}, []string{"P8"})
	report(c, "cluster operator execs into a broker pod", Req{User: cop, Op: "CONNECT", Sub: "exec", Obj: pod8}, nil)
	pod8b := deepCopy(pod8)
	set(pod8b, "metadata/namespace", "ryft-api")
	report(c, "cluster operator execs into a pod elsewhere", Req{User: cop, Op: "CONNECT", Sub: "exec", Obj: pod8b}, []string{"P8"})
	report(c, "topology operator execs", Req{User: top, Op: "CONNECT", Sub: "exec", Obj: pod8}, []string{"P8"})
	report(c, "someone else writes a secret in ryft-api", Req{User: admin, Op: "CREATE", Obj: sec("ryft-api")}, nil)
}
