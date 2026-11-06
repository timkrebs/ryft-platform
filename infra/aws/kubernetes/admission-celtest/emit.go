package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// CELTEST_EMIT=<dir> writes the CREATE negatives of this run as fixtures for the kind run
// (scripts/admission_e2e.sh, infra/aws/kubernetes/admission-e2e): for every distinct refusal
// (policy binding and message) the first case by name that a single binding refuses, as one YAML
// file with a header the kind runner reads:
//
//	# fixture: <case name>
//	# op: create
//	# as: <the requester>
//	# refused-by: <binding>
//	# message: <the refusal, as in testdata/messages.golden>
//	<the object>
//
// One refused fixture per rule, from the same cases the harness pins: the kind run asks the real
// API server whether it agrees. UPDATE, DELETE and subresource cases are not emitted (the kind
// runner has hand-written fixtures for those, infra/aws/kubernetes/admission-e2e/fixtures/manual).
// Regenerate with `CELTEST_EMIT=../admission-e2e/fixtures/generated go run . ...` (run.sh does it
// when CELTEST_EMIT is set); the output is deterministic, and independent of the overlay's pins
// (the image tags of the services are written as 0000000).

type emitCase struct {
	name string
	req  Req
	res  []Result
}

var emitted []emitCase

// Cases that are no fixture for the kind run: objects the CEL harness accepts but the API server's own validation rejects before
// admission (a CRD's required fields, a Pod's missing parts, mixed peers): the kind runner has a
// hand-written, valid fixture for each rule (admission-e2e/fixtures/manual).
var notEmitted = map[string]string{
	"P7 appset": "ApplicationSet needs generators and a template",
	"P7 server creates an Application with a status.sync": "status.sync.status is required",
	"P11 shovel": "Shovel needs name, rabbitmqClusterReference and uriSecret",
	"P3 endPort in an ingress rule with an ipBlock peer": "a peer may not set ipBlock and another selector",
	"P6 crd conversion webhook":                          "a Webhook conversion needs clientConfig and conversionReviewVersions",
	"P4 other kind":                                      "VaultDynamicSecret needs mount, path and destination",
	"P2 cronjob with another image":                      "a CronJob needs schedule and restartPolicy",
	"P2 image volume":                                    "the volume mount names a volume the case replaced",
	"P2 projected token":                                 "the volume mount names a volume the case replaced",
	"P6 crd of another group":                            "a CRD's name must be its plural.group",
	// Whether the API server keeps an image volume source depends on the ImageVolume feature gate of
	// kind's release; the harness pins the rule (in ryft-* and in vault).
	"vault: P2 an image volume": "an image volume source depends on kind's ImageVolume feature gate",
	// cert-manager's webhook (after the policies) or RBAC would refuse these in kind's Audit run.
	"vault: P13 ClusterIssuer ryft-vault with an empty ca (has() guard)":             "a CA issuer needs secretName",
	"vault: P13 ClusterIssuer ryft-vault-bootstrap with an empty spec (has() guard)": "an issuer needs one type",
	"vault: P13 Argo CD writes an Issuer with an empty spec (has() guard)":           "an issuer needs one type",
	"vault: P13 the root CA without an issuerRef name (has() guard)":                 "issuerRef.name is required",
	"vault: P13 vault-tls without a secretName":                                      "secretName is required",
	"vault: P13 Argo CD writes a selfSigned ClusterIssuer":                           "Argo CD's controller has no RBAC on clusterissuers",
	"vault: P13 Argo CD writes ClusterIssuer ryft-vault as rendered":                 "Argo CD's controller has no RBAC on clusterissuers",
	"vault: P13 a Kubernetes CSR for ryft-vault by cert-manager":                     "cert-manager's RBAC on certificatesigningrequests is not the point",
	// The service account admission plugin refuses a Pod whose service account does not exist, and
	// the kind run creates no vault/vault-snapshot; the CronJob cases pin the same image rule.
	"vault: P2 a snapshot pod whose upload image is not the pin":  "the kind run has no service account vault/vault-snapshot",
	"vault: P2 a pod of another service account with the AWS CLI": "the kind run has no service account vault/vault",
	// The broker's NetworkPolicy carries the open 443 egress rule the committed values admit
	// (brokerEgressPublic); the kind run's Deny variant closes it (the chart's guard), so the
	// object would be refused for that rule first.
	"P3 another range in the broker": "carries the open 443 egress rule",
}

func maybeEmit(name string, r Req, res []Result) {
	if os.Getenv("CELTEST_EMIT") == "" {
		return
	}
	if r.Op != "CREATE" || r.Sub != "" || r.Obj == nil || len(res) == 0 {
		return
	}
	if strings.Contains(name, "(before T12-g)") || strings.HasPrefix(name, "cost:") || strings.HasPrefix(name, "decom:") {
		return
	}
	if _, bad := notEmitted[name]; bad || opensBrokerEgress(r.Obj) {
		return
	}
	// Exactly one failing validation: the API server reports the first one it finds, so an object
	// that breaks two rules would make the message to look for ambiguous.
	if len(res) != 1 || res[0].Action != "deny" {
		return
	}
	emitted = append(emitted, emitCase{name: name, req: Req{User: r.User, Op: r.Op, Obj: deepCopy(r.Obj)}, res: res})
}

// opensBrokerEgress reports a NetworkPolicy with the broker's open 443 egress rule (0.0.0.0/0
// without exceptions), which the Deny variant of the kind run refuses first (see notEmitted).
func opensBrokerEgress(obj map[string]any) bool {
	if obj["kind"] != "NetworkPolicy" {
		return false
	}
	spec, _ := obj["spec"].(map[string]any)
	egress, _ := spec["egress"].([]any)
	for _, r := range egress {
		rule, _ := r.(map[string]any)
		tos, _ := rule["to"].([]any)
		for _, t := range tos {
			peer, _ := t.(map[string]any)
			ip, _ := peer["ipBlock"].(map[string]any)
			if ip != nil && ip["cidr"] == "0.0.0.0/0" {
				if ex, _ := ip["except"].([]any); len(ex) == 0 {
					return true
				}
			}
		}
	}
	return false
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// The renders carry the EKS overlay's pins, which every promote PR moves; a fixture that followed
// them would make the committed copy stale at each deploy (the `admission` job diffs the two). The
// pinned short SHA is written as a constant that still matches P2's pattern.
var pinRE = regexp.MustCompile(`(/ryft/[a-z]+):[0-9a-f]{7}\b`)

func writeFixtures() {
	dir := os.Getenv("CELTEST_EMIT")
	if dir == "" {
		return
	}
	must(os.RemoveAll(dir))
	must(os.MkdirAll(dir, 0o755))
	sort.Slice(emitted, func(i, j int) bool { return emitted[i].name < emitted[j].name })
	seen := map[string]bool{}
	count := map[string]int{}
	for _, e := range emitted {
		binding := e.res[0].Binding
		msg := e.res[0].Messages[0]
		key := binding + "|" + msg
		if seen[key] {
			continue
		}
		seen[key] = true
		count[binding]++
		slug := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(e.name), "-"), "-")
		if len(slug) > 60 {
			slug = strings.TrimRight(slug[:60], "-")
		}
		file := filepath.Join(dir, fmt.Sprintf("%s-%02d-%s.yaml", binding, count[binding], slug))
		body, err := yaml.Marshal(e.req.Obj)
		must(err)
		body = pinRE.ReplaceAll(body, []byte("${1}:0000000"))
		var b strings.Builder
		fmt.Fprintf(&b, "# fixture: %s\n# op: create\n# as: %s\n# refused-by: %s\n# message: %s\n", e.name, e.req.User, binding, oneLine(msg))
		b.Write(body)
		must(os.WriteFile(file, []byte(b.String()), 0o644))
	}
	fmt.Printf("fixtures written: %d files in %s\n", len(seen), dir)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
