package main

import (
	"fmt"
	"time"
)

// P12 ryft-argo-scope (docs/work/2026-10-23-admission/p12-argo-scope.md). T12-l1 wrote the canary
// cases below, one admitted twin for each refusal; T12-l2 extends them (about 60 named cases) and
// audits the collisions with the other policies. Each case says which of the five rules answers:
//
//	r1 no subresource and no CONNECT      r2 only the controller and the server write (+ redis init)
//	r3 the server                          r4 the controller, namespaced
//	r5 the controller, cluster-scoped
const (
	argoRedisInit = "system:serviceaccount:argocd:argocd-redis-secret-init"
	argoRepo      = "system:serviceaccount:argocd:argocd-repo-server"
	argoAppSet    = "system:serviceaccount:argocd:argocd-applicationset-controller"

	r1 = "P12: Argo CD writes no subresource"
	r2 = "P12: of Argo CD's service accounts only"
	r3 = "P12: argocd-server writes only"
	r4 = "P12: Argo CD's controller writes namespaced"
	r5 = "P12: Argo CD's controller writes cluster-scoped"
)

func runArgoScope(c *chart) {
	fmt.Println("== P12: where Argo CD's service accounts may write")
	type x struct {
		name string
		req  Req
		want []string
	}
	var ts []x
	crt := func(name, user string, obj map[string]any, want ...string) {
		ts = append(ts, x{name, Req{User: user, Op: "CREATE", Obj: obj}, want})
	}
	upd := func(name, user string, obj map[string]any, want ...string) {
		ts = append(ts, x{name, Req{User: user, Op: "UPDATE", Obj: obj, Old: deepCopy(obj)}, want})
	}
	updTo := func(name, user string, obj, old map[string]any, want ...string) {
		ts = append(ts, x{name, Req{User: user, Op: "UPDATE", Obj: obj, Old: old}, want})
	}
	del := func(name, user string, old map[string]any, want ...string) {
		ts = append(ts, x{name, Req{User: user, Op: "DELETE", Old: old}, want})
	}
	raw := func(name string, r Req, want ...string) { ts = append(ts, x{name, r, want}) }

	// small objects of kinds the renders do not hold
	mk := func(apiVersion, kind, ns, name string) map[string]any {
		md := map[string]any{"name": name}
		if ns != "" {
			md["namespace"] = ns
		}
		return map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": md}
	}
	cm := func(ns, name string) map[string]any {
		o := mk("v1", "ConfigMap", ns, name)
		o["data"] = map[string]any{"k": "v"}
		return o
	}
	deployIn := func(ns, sa string) map[string]any {
		d := set(find("Deployment", "ryft-notify", "notify"), "metadata/namespace", ns)
		return set(d, "spec/template/spec/serviceAccountName", sa)
	}
	app := func(name string) map[string]any { return find("Application", "argocd", name) }

	// ---- the controller, namespaced (rule 4): the canaries of the review
	crt("P12 controller creates a Deployment in cert-manager", argoCtl, deployIn("cert-manager", "cert-manager"), r4)
	crt("P12 controller creates a Deployment in kube-system as a controller-manager SA", argoCtl, deployIn("kube-system", "generic-garbage-collector"), r4)
	crt("P12 controller creates a Job in cert-manager", argoCtl, set(find("Job", "ryft-migrate", "migrate"), "metadata/namespace", "cert-manager"), r4)
	crt("P12 controller creates kube-system/aws-auth", argoCtl, cm("kube-system", "aws-auth"), r4)
	upd("P12 controller patches kube-system/aws-auth", argoCtl, cm("kube-system", "aws-auth"), r4)
	del("P12 controller deletes kube-system/aws-auth", argoCtl, cm("kube-system", "aws-auth"), r4)
	crt("P12 controller creates a Service in kube-system", argoCtl, mk("v1", "Service", "kube-system", "s"), r4)
	crt("P12 controller creates a ServiceAccount in default", argoCtl, mk("v1", "ServiceAccount", "default", "sa"), r4)
	upd("P12 controller patches argocd-cm", argoCtl, cm("argocd", "argocd-cm"), r4)
	upd("P12 controller patches argocd-rbac-cm", argoCtl, cm("argocd", "argocd-rbac-cm"), r4)
	upd("P12 controller patches the argocd-server Deployment", argoCtl, deployIn("argocd", "argocd-server"), r4)
	crt("P12 controller creates a Secret in argocd", argoCtl, mk("v1", "Secret", "argocd", "s"), r4)
	crt("P12 controller creates an ApplicationSet", argoCtl, fx("applicationset-x"), r4, "P7")
	crt("P12 controller creates an Application outside argocd", argoCtl, set(app("vault-api"), "metadata/namespace", "default"), r4)
	// scope edges: a prefix and a name list, not a substring
	crt("P12 controller writes in ryftx", argoCtl, cm("ryftx", "c"), r4)
	crt("P12 controller writes in xryft-api", argoCtl, cm("xryft-api", "c"), r4)
	crt("P12 controller writes in rabbitmq-system-x", argoCtl, cm("rabbitmq-system-x", "c"), r4)
	crt("P12 controller writes in argocd-x", argoCtl, cm("argocd-x", "c"), r4)
	// the scope is a list of namespaces, not the prefix ryft-: the import service's namespace (ryft-import
	// here; its own Vault credentials, no Argo CD app) and a future infra namespace are none of Argo CD's
	crt("P12 controller writes a ConfigMap in ryft-import", argoCtl, cm("ryft-import", "c"), r4)
	crt("P12 controller writes a Secret in ryft-import", argoCtl, mk("v1", "Secret", "ryft-import", "s"), r4)
	crt("P12 controller writes a ConfigMap in a future ryft-infra namespace", argoCtl, cm("ryft-infra", "c"), r4)
	crt("P12 controller writes in ryft-api-x", argoCtl, cm("ryft-api-x", "c"), r4)
	// every namespace the committed apps target or render into (deploy/argocd/apps, the eks overlay)
	for _, n := range []string{"ryft-api", "ryft-ingest", "ryft-migrate", "ryft-notify", "ryft-rabbitmq", "ryft-scoring", "ryft-smoke", "ryft-web", "rabbitmq-system"} {
		crt("P12 controller creates a ConfigMap in "+n+" (the list)", argoCtl, cm(n, "c"))
	}
	// admitted twins
	crt("P12 controller creates a ConfigMap in ryft-api", argoCtl, cm("ryft-api", "c"))
	crt("P12 controller creates a ConfigMap in rabbitmq-system", argoCtl, cm("rabbitmq-system", "c"))
	crt("P12 controller creates a Job with a generateName in ryft-migrate", argoCtl,
		func() map[string]any {
			j := find("Job", "ryft-migrate", "migrate")
			delete(j["metadata"].(map[string]any), "name")
			return set(j, "metadata/generateName", "migrate-")
		}())
	crt("P12 controller creates an Application in argocd", argoCtl, app("vault-api"))
	upd("P12 controller updates an Application in argocd", argoCtl, app("vault-api"))
	del("P12 controller deletes an Application in argocd", argoCtl, app("vault-api"))
	del("P12 controller deletes a hook Job in ryft-migrate", argoCtl, find("Job", "ryft-migrate", "migrate"))
	crt("P12 controller creates an Event in argocd", argoCtl, mk("v1", "Event", "argocd", "ev1"))
	raw("P12 controller creates an events.k8s.io Event in argocd", Req{User: argoCtl, Op: "CREATE", ResKind: "EventsK8s", Obj: mk("events.k8s.io/v1", "Event", "argocd", "ev1")})
	crt("P12 controller creates an Event in kube-system", argoCtl, mk("v1", "Event", "kube-system", "ev1"), r4)
	upd("P12 controller updates an Event in argocd (a repeated event)", argoCtl, mk("v1", "Event", "argocd", "ev1"))
	del("P12 controller deletes an Event in argocd", argoCtl, mk("v1", "Event", "argocd", "ev1"), r4)
	raw("P12 controller deletes an events.k8s.io Event in argocd", Req{User: argoCtl, Op: "DELETE", ResKind: "EventsK8s", Old: mk("events.k8s.io/v1", "Event", "argocd", "ev1")}, r4)
	upd("P12 controller updates an Event in ryft-api", argoCtl, mk("v1", "Event", "ryft-api", "ev1"))

	// ---- the controller, cluster-scoped (rule 5)
	web := func() map[string]any { return find("Namespace", "", "ryft-web") }
	opsNs := func() map[string]any { return find("Namespace", "", "rabbitmq-system") }
	crt("P12 controller creates the Namespace ryft-web", argoCtl, web())
	raw("P12 controller creates the Namespace ryft-web (no namespace in the request)", Req{User: argoCtl, Op: "CREATE", Obj: web(), NoNs: true})
	upd("P12 controller updates the Namespace ryft-web", argoCtl, web())
	crt("P12 controller creates the Namespace rabbitmq-system", argoCtl, opsNs())
	raw("P12 controller creates the Namespace rabbitmq-system (no namespace in the request)", Req{User: argoCtl, Op: "CREATE", Obj: opsNs(), NoNs: true})
	del("P12 controller deletes the Namespace ryft-web", argoCtl, web(), r5, "P1")
	del("P12 controller deletes the Namespace rabbitmq-system", argoCtl, opsNs(), r5, "P1")
	crt("P12 controller creates another Namespace", argoCtl, set(web(), "metadata/name", "ryft-evil"), r5, "P1")
	raw("P12 controller creates another Namespace (no namespace in the request)", Req{User: argoCtl, Op: "CREATE", Obj: set(web(), "metadata/name", "ryft-evil"), NoNs: true}, r5, "P1")
	upd("P12 controller updates kube-system", argoCtl, mk("v1", "Namespace", "", "kube-system"), r5, "P1")
	// ryft-api is in argoScope.namespaces (the controller writes in it) but not in namespaceObjects (it is
	// infra/vault's Namespace): both P1 and P12 refuse the Namespace object itself (P1 reads namespaceObjects).
	crt("P12 controller creates the Namespace ryft-api (in namespaces, not in namespaceObjects)", argoCtl, set(web(), "metadata/name", "ryft-api"), r5, "P1: Argo CD may write only the Namespaces")
	upd("P12 controller updates the Namespace ryft-api (in namespaces, not in namespaceObjects)", argoCtl, set(web(), "metadata/name", "ryft-api"), r5, "P1: Argo CD may write only the Namespaces")
	upd("P12 controller updates argocd", argoCtl, set(fx("namespace-argocd"), "metadata/name", "argocd"), r5, "P1")
	crt("P12 controller creates a Namespace with a generateName", argoCtl, func() map[string]any {
		n := web()
		delete(n["metadata"].(map[string]any), "name")
		return set(n, "metadata/generateName", "ryft-")
	}(), r5, "P1")
	crd := func(name string) map[string]any { return find("CustomResourceDefinition", "", name) }
	crt("P12 controller creates a rabbitmq.com CRD", argoCtl, crd("rabbitmqclusters.rabbitmq.com"))
	upd("P12 controller updates a rabbitmq.com CRD", argoCtl, crd("rabbitmqclusters.rabbitmq.com"))
	del("P12 controller deletes a rabbitmq.com CRD", argoCtl, crd("rabbitmqclusters.rabbitmq.com"), r5)
	crt("P12 controller creates a CRD of another group", argoCtl, set(crd("rabbitmqclusters.rabbitmq.com"), "metadata/name", "certificates.cert-manager.io"), r5)
	crt("P12 controller creates a CRD named like the group", argoCtl, set(crd("rabbitmqclusters.rabbitmq.com"), "metadata/name", "rabbitmq.com"), r5)
	// (a spec, so that the CRD's schema accepts it: it is emitted as the rule 5 fixture of the kind run)
	issuer := mk("cert-manager.io/v1", "ClusterIssuer", "", "x")
	issuer["spec"] = map[string]any{"selfSigned": map[string]any{}}
	crt("P12 controller creates a ClusterIssuer", argoCtl, issuer, r5)
	crt("P12 controller creates a PriorityClass", argoCtl, mk("scheduling.k8s.io/v1", "PriorityClass", "", "x"), r5)
	crt("P12 controller creates a ClusterRoleBinding without the prefix", argoCtl, set(find("ClusterRoleBinding", "", "messaging-topology-metrics-auth-rolebinding"), "metadata/name", "evil"), r5)
	crt("P12 controller creates a ClusterRoleBinding of the operators", argoCtl, find("ClusterRoleBinding", "", "messaging-topology-metrics-auth-rolebinding"))
	crt("P12 controller creates a ClusterRole of the operators", argoCtl, find("ClusterRole", "", "messaging-topology-metrics-reader"))
	upd("P12 controller updates a ClusterRole of the operators", argoCtl, find("ClusterRole", "", "messaging-topology-metrics-reader"))
	del("P12 controller deletes a ClusterRole of the operators (prune)", argoCtl, find("ClusterRole", "", "messaging-topology-metrics-reader"))
	del("P12 controller deletes cluster-admin", argoCtl, mk("rbac.authorization.k8s.io/v1", "ClusterRole", "", "cluster-admin"), r5)
	crt("P12 controller creates an operators' webhook configuration", argoCtl, find("ValidatingWebhookConfiguration", "", "cluster-operator-validating-webhook-configuration"))
	crt("P12 controller creates a webhook configuration of another name", argoCtl, set(find("ValidatingWebhookConfiguration", "", "cluster-operator-validating-webhook-configuration"), "metadata/name", "evil"), r5, "P6")
	// (SelfSubjectAccessReview, SelfSubjectRulesReview and SubjectAccessReview creates have no case: kube-apiserver's
	// exclusion list (pkg/kubeapiserver/admission/exclusion, since 1.30) exempts them from policies, claim o of
	// the kind run, so P12 neither admits nor refuses them)

	// ---- rule 1: subresources and CONNECT, whoever the account
	scale := map[string]any{"apiVersion": "autoscaling/v1", "kind": "Scale", "metadata": map[string]any{"name": "api", "namespace": "ryft-api"}, "spec": map[string]any{"replicas": float64(3)}}
	raw("P12 controller scales a Deployment in ryft-api", Req{User: argoCtl, Op: "UPDATE", Sub: "scale", ResKind: "Deployment", Obj: scale, Old: deepCopy(scale)}, r1)
	status := find("Deployment", "ryft-notify", "notify")
	raw("P12 controller writes deployments/status in ryft-api", Req{User: argoCtl, Op: "UPDATE", Sub: "status", Obj: status, Old: deepCopy(status)}, r1)
	tokenReq := map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "TokenRequest", "metadata": map[string]any{"name": "api", "namespace": "ryft-api"}, "spec": map[string]any{"audiences": []any{"x"}}}
	raw("P12 controller requests a ServiceAccount token in ryft-api", Req{User: argoCtl, Op: "CREATE", Sub: "token", ResKind: "ServiceAccount", Obj: tokenReq}, r1)
	raw("P12 controller execs into a Pod in ryft-api", Req{User: argoCtl, Op: "CONNECT", Sub: "exec", ResKind: "Pod", Ns: "ryft-api", Name: "api-abc12"}, r1)
	raw("P12 controller port-forwards a Pod in ryft-api", Req{User: argoCtl, Op: "CONNECT", Sub: "portforward", ResKind: "Pod", Ns: "ryft-api", Name: "api-abc12"}, r1)
	raw("P12 controller execs into a Pod with its options as the object", Req{User: argoCtl, Op: "CONNECT", Sub: "exec", ResKind: "Pod", Ns: "ryft-api", Name: "api-abc12",
		Obj: map[string]any{"apiVersion": "v1", "kind": "PodExecOptions", "command": []any{"sh"}}}, r1)
	raw("P12 server execs into a Pod", Req{User: argoSrv, Op: "CONNECT", Sub: "exec", ResKind: "Pod", Ns: "ryft-api", Name: "api-abc12"}, r1)
	raw("P12 the admin execs into a Pod", Req{User: admin, Op: "CONNECT", Sub: "exec", ResKind: "Pod", Ns: "ryft-api", Name: "api-abc12"})

	evict := map[string]any{"apiVersion": "policy/v1", "kind": "Eviction", "metadata": map[string]any{"name": "api-abc12", "namespace": "ryft-api"}}
	raw("P12 controller evicts a Pod in ryft-api", Req{User: argoCtl, Op: "CREATE", Sub: "eviction", ResKind: "Pod", Obj: evict}, r1)
	raw("P12 server evicts a Pod in ryft-api", Req{User: argoSrv, Op: "CREATE", Sub: "eviction", ResKind: "Pod", Obj: evict}, r1)
	raw("P12 the admin evicts a Pod in ryft-api", Req{User: admin, Op: "CREATE", Sub: "eviction", ResKind: "Pod", Obj: evict})
	raw("P12 controller proxies to a Node (nodes/proxy, CONNECT)", Req{User: argoCtl, Op: "CONNECT", Sub: "proxy", ResKind: "Node", Name: "ip-10-0-1-1"}, r1)
	raw("P12 server proxies to a Node (nodes/proxy, CONNECT)", Req{User: argoSrv, Op: "CONNECT", Sub: "proxy", ResKind: "Node", Name: "ip-10-0-1-1"}, r1)
	raw("P12 the admin proxies to a Node", Req{User: admin, Op: "CONNECT", Sub: "proxy", ResKind: "Node", Name: "ip-10-0-1-1"})
	raw("P12 controller proxies to a Pod in ryft-api (pods/proxy, CONNECT)", Req{User: argoCtl, Op: "CONNECT", Sub: "proxy", ResKind: "Pod", Ns: "ryft-api", Name: "api-abc12"}, r1)
	raw("P12 controller attaches to a Pod in ryft-api", Req{User: argoCtl, Op: "CONNECT", Sub: "attach", ResKind: "Pod", Ns: "ryft-api", Name: "api-abc12"}, r1)

	// ---- kinds Argo CD has no business with, namespaced and cluster-scoped (rules 4 and 5)
	lease := func(ns string) map[string]any {
		o := mk("coordination.k8s.io/v1", "Lease", ns, "kube-controller-manager")
		o["spec"] = map[string]any{"holderIdentity": "x"}
		return o
	}
	crt("P12 controller creates a Lease in kube-system", argoCtl, lease("kube-system"), r4)
	upd("P12 controller updates a Lease in kube-system", argoCtl, lease("kube-system"), r4)
	del("P12 controller deletes a Lease in kube-system", argoCtl, lease("kube-system"), r4)
	crt("P12 controller creates a Lease in ryft-api", argoCtl, lease("ryft-api"))
	crt("P12 server creates a Lease in kube-system", argoSrv, lease("kube-system"), r3)
	crt("P12 the admin creates a Lease in kube-system", admin, lease("kube-system"))
	pv := func() map[string]any {
		o := mk("v1", "PersistentVolume", "", "pv0")
		o["spec"] = map[string]any{"capacity": map[string]any{"storage": "1Gi"}, "accessModes": []any{"ReadWriteOnce"}, "hostPath": map[string]any{"path": "/"}}
		return o
	}
	sc := func() map[string]any {
		o := mk("storage.k8s.io/v1", "StorageClass", "", "gp3")
		o["provisioner"] = "ebs.csi.aws.com"
		return o
	}
	apisvc := func() map[string]any {
		o := mk("apiregistration.k8s.io/v1", "APIService", "", "v1beta1.metrics.k8s.io")
		o["spec"] = map[string]any{"group": "metrics.k8s.io", "version": "v1beta1", "groupPriorityMinimum": float64(100), "versionPriority": float64(100),
			"service": map[string]any{"name": "evil", "namespace": "argocd"}}
		return o
	}
	crt("P12 controller creates a PersistentVolume", argoCtl, pv(), r5)
	upd("P12 controller updates a PersistentVolume", argoCtl, pv(), r5)
	del("P12 controller deletes a PersistentVolume", argoCtl, pv(), r5)
	crt("P12 controller creates a StorageClass", argoCtl, sc(), r5)
	upd("P12 controller updates a StorageClass", argoCtl, sc(), r5)
	crt("P12 controller creates an APIService", argoCtl, apisvc(), r5)
	upd("P12 controller updates an APIService", argoCtl, apisvc(), r5)
	del("P12 controller deletes an APIService", argoCtl, apisvc(), r5)
	crt("P12 server creates a PersistentVolume", argoSrv, pv(), r3)
	crt("P12 server creates an APIService", argoSrv, apisvc(), r3)
	crt("P12 the admin creates a PersistentVolume", admin, pv())
	crt("P12 the admin creates a StorageClass", admin, sc())
	crt("P12 the admin creates an APIService", admin, apisvc())

	// ---- the server (rule 3)
	crt("P12 server creates an Application in argocd", argoSrv, app("vault-api"))
	upd("P12 server updates an Application in argocd", argoSrv, app("vault-api"))
	crt("P12 server creates an Event in argocd", argoSrv, mk("v1", "Event", "argocd", "ev1"))
	crt("P12 server creates an Event in kube-system", argoSrv, mk("v1", "Event", "kube-system", "ev1"), r3)
	upd("P12 server updates an Event in argocd", argoSrv, mk("v1", "Event", "argocd", "ev1"), r3)
	del("P12 server deletes an Event in argocd", argoSrv, mk("v1", "Event", "argocd", "ev1"), r3)
	crt("P12 server creates an Application outside argocd", argoSrv, set(app("vault-api"), "metadata/namespace", "default"), r3)
	upd("P12 server patches argocd-cm", argoSrv, cm("argocd", "argocd-cm"), r3)
	upd("P12 server patches argocd-rbac-cm", argoSrv, cm("argocd", "argocd-rbac-cm"), r3)
	upd("P12 server patches the argocd-server Deployment", argoSrv, deployIn("argocd", "argocd-server"), r3)
	crt("P12 server creates a ConfigMap in ryft-api", argoSrv, cm("ryft-api", "c"), r3)
	crt("P12 server creates an AppProject", argoSrv, find("AppProject", "argocd", "ryft"), r3)
	upd("P12 server updates an AppProject", argoSrv, find("AppProject", "argocd", "ryft"), r3)
	crt("P12 server creates the Namespace ryft-web", argoSrv, web(), r3)
	crt("P12 server creates a CRD", argoSrv, crd("rabbitmqclusters.rabbitmq.com"), r3)

	// argocd-secret: the first server.secretkey and nothing else
	sec := func(data map[string]any, labels, annotations map[string]any) map[string]any {
		s := mk("v1", "Secret", "argocd", "argocd-secret")
		if data != nil {
			s["data"] = data
		}
		if labels != nil {
			s["metadata"].(map[string]any)["labels"] = labels
		}
		if annotations != nil {
			s["metadata"].(map[string]any)["annotations"] = annotations
		}
		return s
	}
	base := func() map[string]any { return map[string]any{"admin.password": "YQ==", "admin.passwordMtime": "Yg=="} }
	with := func(m map[string]any, kv ...string) map[string]any {
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	lbl := func() map[string]any { return map[string]any{"app.kubernetes.io/part-of": "argocd"} }
	updTo("P12 server adds the first server.secretkey", argoSrv, sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil), sec(base(), lbl(), nil))
	updTo("P12 server adds the first server.secretkey (no labels at all)", argoSrv, sec(with(base(), "server.secretkey", "Yw=="), nil, nil), sec(base(), nil, nil))
	updTo("P12 server changes the existing server.secretkey", argoSrv, sec(with(base(), "server.secretkey", "ZA=="), lbl(), nil), sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil), r3)
	updTo("P12 server rewrites argocd-secret with the key present and nothing changed", argoSrv, sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil), sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil), r3)
	updTo("P12 server removes server.secretkey", argoSrv, sec(base(), lbl(), nil), sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil), r3)
	updTo("P12 server adds the key and changes admin.password", argoSrv, sec(with(with(base(), "admin.password", "ZQ=="), "server.secretkey", "Yw=="), lbl(), nil), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key and drops admin.passwordMtime", argoSrv, sec(map[string]any{"admin.password": "YQ==", "server.secretkey": "Yw=="}, lbl(), nil), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key and another key", argoSrv, sec(with(with(base(), "server.secretkey", "Yw=="), "webhook.github.secret", "ZQ=="), lbl(), nil), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key and a label", argoSrv, sec(with(base(), "server.secretkey", "Yw=="), map[string]any{"app.kubernetes.io/part-of": "argocd", "x": "y"}, nil), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key and drops the labels", argoSrv, sec(with(base(), "server.secretkey", "Yw=="), nil, nil), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key and an annotation", argoSrv, sec(with(base(), "server.secretkey", "Yw=="), lbl(), map[string]any{"x": "y"}), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key to a Secret without data", argoSrv, sec(map[string]any{"server.secretkey": "Yw=="}, lbl(), nil), sec(nil, lbl(), nil), r3)
	updTo("P12 server changes admin.password", argoSrv, sec(with(base(), "admin.password", "ZQ=="), lbl(), nil), sec(base(), lbl(), nil), r3)
	updTo("P12 controller adds the first server.secretkey", argoCtl, sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil), sec(base(), lbl(), nil), r4)
	updTo("P12 the admin adds the first server.secretkey", admin, sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil), sec(base(), lbl(), nil))
	crt("P12 server creates argocd-secret", argoSrv, sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil), r3)
	updTo("P12 server adds the first server.secretkey to a Secret elsewhere", argoSrv, set(sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil), "metadata/namespace", "ryft-api"), set(sec(base(), lbl(), nil), "metadata/namespace", "ryft-api"), r3)
	updTo("P12 server adds the first server.secretkey to another Secret", argoSrv, set(sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil), "metadata/name", "argocd-notifications-secret"), set(sec(base(), lbl(), nil), "metadata/name", "argocd-notifications-secret"), r3)
	del("P12 server deletes argocd-secret", argoSrv, sec(base(), lbl(), nil), r3)
	// the exemption compares everything else: ownerReferences, finalizers, immutable, type ... (a dangling
	// ownerReference or a finalizer on argocd-secret is a way to have it garbage-collected or held)
	withKey := func() map[string]any { return sec(with(base(), "server.secretkey", "Yw=="), lbl(), nil) }
	ownerRef := []any{map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "name": "gone", "uid": "00000000-0000-0000-0000-000000000000"}}
	updTo("P12 server adds the key and a dangling ownerReference", argoSrv, set(withKey(), "metadata/ownerReferences", ownerRef), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key and a finalizer", argoSrv, set(withKey(), "metadata/finalizers", []any{"example.com/hold"}), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key and makes the Secret immutable", argoSrv, set(withKey(), "immutable", true), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key and changes the type", argoSrv, set(withKey(), "type", "kubernetes.io/tls"), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key and renames a label key", argoSrv, sec(with(base(), "server.secretkey", "Yw=="), map[string]any{"app.kubernetes.io/name": "argocd"}, nil), sec(base(), lbl(), nil), r3)
	updTo("P12 server adds the key and drops an ownerReference", argoSrv, withKey(), set(sec(base(), lbl(), nil), "metadata/ownerReferences", ownerRef), r3)
	updTo("P12 server adds the key and drops a finalizer", argoSrv, withKey(), set(sec(base(), lbl(), nil), "metadata/finalizers", []any{"example.com/hold"}), r3)
	updTo("P12 server adds the key and sets deletionTimestamp", argoSrv, set(withKey(), "metadata/deletionTimestamp", "2026-11-05T00:00:00Z"), sec(base(), lbl(), nil), r3)
	// what the API server and the field manager move on any write is not a change
	updTo("P12 server adds the key; resourceVersion, generation and managedFields move", argoSrv,
		set(set(set(withKey(), "metadata/resourceVersion", "2"), "metadata/generation", float64(2)), "metadata/managedFields", []any{map[string]any{"manager": "argocd-server", "operation": "Update"}}),
		set(set(sec(base(), lbl(), nil), "metadata/resourceVersion", "1"), "metadata/generation", float64(1)))
	updTo("P12 server adds the key to a Secret that has an ownerReference and a finalizer, unchanged", argoSrv,
		set(set(withKey(), "metadata/ownerReferences", ownerRef), "metadata/finalizers", []any{"example.com/hold"}),
		set(set(sec(base(), lbl(), nil), "metadata/ownerReferences", ownerRef), "metadata/finalizers", []any{"example.com/hold"}))

	// DELETECOLLECTION reaches admission as a DELETE with an empty name (and no object): cluster-scoped is
	// refused by rule 5, namespaced in scope is admitted, outside scope refused; no CEL error in any case.
	// Judged on ryft-argo-scope alone (the other policies are not modelled for a request without an object).
	dcol := func(name, user, kind, ns string, want ...string) {
		raw(name, Req{User: user, Op: "DELETE", ResKind: kind, Ns: ns, Only: "ryft-argo-scope"}, want...)
	}
	dcol("P12 controller deletes a collection of ConfigMaps in ryft-api", argoCtl, "ConfigMap", "ryft-api")
	dcol("P12 controller deletes a collection of Applications in argocd", argoCtl, "Application", "argocd")
	dcol("P12 controller deletes a collection of ConfigMaps in kube-system", argoCtl, "ConfigMap", "kube-system", r4)
	dcol("P12 controller deletes a collection of Secrets in argocd", argoCtl, "Secret", "argocd", r4)
	dcol("P12 controller deletes a collection of ClusterRoles", argoCtl, "ClusterRole", "", r5)
	dcol("P12 controller deletes a collection of Namespaces", argoCtl, "Namespace", "", r5)
	dcol("P12 server deletes a collection of Applications in argocd", argoSrv, "Application", "argocd")
	dcol("P12 server deletes a collection of ConfigMaps in argocd", argoSrv, "ConfigMap", "argocd", r3)

	// ---- rule 2: the other service accounts of argocd
	crt("P12 redis-secret-init creates Secret argocd-redis", argoRedisInit, mk("v1", "Secret", "argocd", "argocd-redis"))
	upd("P12 redis-secret-init updates Secret argocd-redis", argoRedisInit, mk("v1", "Secret", "argocd", "argocd-redis"), r2)
	del("P12 redis-secret-init deletes Secret argocd-redis", argoRedisInit, mk("v1", "Secret", "argocd", "argocd-redis"), r2)
	crt("P12 redis-secret-init creates another Secret", argoRedisInit, mk("v1", "Secret", "argocd", "argocd-secret"), r2)
	crt("P12 redis-secret-init creates argocd-redis in ryft-api", argoRedisInit, mk("v1", "Secret", "ryft-api", "argocd-redis"), r2)
	crt("P12 redis-secret-init creates a ConfigMap named argocd-redis", argoRedisInit, cm("argocd", "argocd-redis"), r2)
	crt("P12 redis-secret-init creates an Application", argoRedisInit, app("vault-api"), r2)
	crt("P12 the repo-server creates a ConfigMap in ryft-api", argoRepo, cm("ryft-api", "c"), r2)
	crt("P12 the repo-server creates an Event in argocd", argoRepo, mk("v1", "Event", "argocd", "ev1"), r2)
	crt("P12 the applicationset-controller creates an Application", argoAppSet, app("vault-api"), r2)
	crt("P12 an unknown argocd service account creates an Application", "system:serviceaccount:argocd:argocd-future", app("vault-api"), r2)
	crt("P12 a lookalike of the controller creates an Application", "system:serviceaccount:argocd:argocd-application-controller-x", app("vault-api"), r2)

	// ---- not matched: the matchCondition is the requester
	crt("P12 the admin creates a Deployment in cert-manager", admin, deployIn("cert-manager", "cert-manager"))
	crt("P12 the admin creates aws-auth", admin, cm("kube-system", "aws-auth"))
	crt("P12 a service account of argocd-x creates a Deployment in cert-manager", "system:serviceaccount:argocd-x:argocd-application-controller", deployIn("cert-manager", "cert-manager"))
	crt("P12 a service account of kube-system creates a ConfigMap in cert-manager", "system:serviceaccount:kube-system:replicaset-controller", cm("cert-manager", "c"))
	crt("P12 a user named like the prefix creates a ConfigMap in cert-manager", "system:serviceaccount:argocd", cm("cert-manager", "c"))
	// An empty username is omitted from the request. P12's matchCondition reads it through has(); P8's
	// (and P1's, P6's, P7's) do not, which no real request can reach (every authenticated user has a
	// name), so the case is a kind no other policy matches.
	crt("P12 an empty username (field omitted) is not matched and is no CEL error", "", mk("scheduling.k8s.io/v1", "PriorityClass", "", "x"))
	raw("P12 an empty username deletes a ClusterRole (no namespace, no object)", Req{User: "", Op: "DELETE", Old: mk("rbac.authorization.k8s.io/v1", "ClusterRole", "", "x")})

	// ---- fields that are absent: no namespace (cluster-scoped), no name (generateName), no
	// subresource, no object (DELETE, CONNECT), no oldObject (CREATE). Every one is an admit or a
	// refusal with P12's message, never a CEL error (report fails a case on an error).
	// (named so that it sorts after "P12 controller creates a Deployment in cert-manager": the emitter
	// takes the first case by name for rule 4, and the design wants the Deployment as the fixture)
	crt("P12 controller creates a generateName ConfigMap in cert-manager", argoCtl,
		func() map[string]any {
			o := cm("cert-manager", "")
			delete(o["metadata"].(map[string]any), "name")
			return set(o, "metadata/generateName", "x-")
		}(), r4)
	crt("P12 server creates an object without metadata.name", argoSrv, func() map[string]any {
		o := mk("v1", "ConfigMap", "argocd", "")
		delete(o["metadata"].(map[string]any), "name")
		return o
	}(), r3)
	// A create with no metadata at all, of a kind rule 5 judges by name: the `name` variable must read
	// object.metadata through has() (no CEL error) and fall back to '', which is in no allowlist, so rule 5
	// refuses it. Judged on ryft-argo-scope alone: the other policies error on such an object in the harness.
	noMeta := func(kind, apiVersion string) map[string]any {
		return map[string]any{"apiVersion": apiVersion, "kind": kind}
	}
	raw("P12 controller creates a ClusterRole without any metadata", Req{User: argoCtl, Op: "CREATE", Obj: noMeta("ClusterRole", "rbac.authorization.k8s.io/v1"), NoNs: true, Only: "ryft-argo-scope"}, r5)
	raw("P12 controller creates a Namespace without any metadata", Req{User: argoCtl, Op: "CREATE", Obj: noMeta("Namespace", "v1"), NoNs: true, Only: "ryft-argo-scope"}, r5)
	raw("P12 controller deletes a cluster-scoped object with only the old object", Req{User: argoCtl, Op: "DELETE", Old: find("ClusterRole", "", "messaging-topology-metrics-reader")})

	for _, t := range ts {
		report(c, t.name, t.req, t.want)
	}

	// cost: argocd-secret with 1,000 keys, the first server.secretkey added (both walks run)
	many := base()
	for i := 0; i < 1000; i++ {
		many[fmt.Sprintf("key-%04d", i)] = "dg=="
	}
	t0 := time.Now()
	report(c, "cost: P12 server adds the first server.secretkey to a Secret of 1,000 keys", Req{User: argoSrv, Op: "UPDATE",
		Obj: sec(with(deepCopy(many), "server.secretkey", "Yw=="), lbl(), nil), Old: sec(many, lbl(), nil)}, nil)
	report(c, "cost: P12 server changes one of 1,000 keys with the key present", Req{User: argoSrv, Op: "UPDATE",
		Obj: sec(with(with(deepCopy(many), "server.secretkey", "Yw=="), "key-0999", "eg=="), lbl(), nil), Old: sec(with(deepCopy(many), "server.secretkey", "Yw=="), lbl(), nil)}, []string{r3})
	fmt.Println("   elapsed", time.Since(t0))
}
