package main

import "fmt"

const (
	stsController = "system:serviceaccount:kube-system:statefulset-controller"
	gcController  = "system:serviceaccount:kube-system:generic-garbage-collector"
	rsController  = "system:serviceaccount:kube-system:replicaset-controller"
)

// grantsWhatT12gRemoved reports whether a ClusterRole of a render still holds Secrets,
// pods/exec or a Pod write (the release's cluster-wide roles of the Cluster and Topology
// Operator before T12-g). P6 refuses those from Argo CD; the harness expects exactly that, so it
// is green on a render from before and after T12-g.
func grantsWhatT12gRemoved(d map[string]any) bool {
	rules, _ := d["rules"].([]any)
	for _, r := range rules {
		rm, _ := r.(map[string]any)
		res, _ := rm["resources"].([]any)
		for _, x := range res {
			if x == "secrets" || x == "pods/exec" {
				return true
			}
		}
	}
	return false
}

func runHardening(c *chart) {
	type n struct {
		name string
		req  Req
		want []string
	}
	var ts []n
	add := func(name, user, op string, obj map[string]any, want ...string) {
		r := Req{User: user, Op: op, Obj: obj}
		if op == "UPDATE" {
			r.Old = deepCopy(obj)
		}
		ts = append(ts, n{name, r, want})
	}
	upd := func(name, user string, obj, old map[string]any, want ...string) {
		ts = append(ts, n{name, Req{User: user, Op: "UPDATE", Obj: obj, Old: old}, want})
	}
	sub := func(name, user, subres string, obj, old map[string]any, want ...string) {
		ts = append(ts, n{name, Req{User: user, Op: "UPDATE", Sub: subres, Obj: obj, Old: old}, want})
	}

	fmt.Println("== P6: wildcard and dangerous subresources (a Role in the broker's namespace and a ClusterRole)")
	role := func() map[string]any { return fx("role-cluster-operator-ryft-rabbitmq") }
	cr := func() map[string]any {
		o := fx("clusterrole-rabbitmq-cluster-operator-role")
		o["rules"] = jv(`[{"apiGroups":["rabbitmq.com"],"resources":["rabbitmqclusters"],"verbs":["get","list","watch"]}]`)
		return o
	}
	bad := []struct{ name, rule string }{
		{"star token", `{"apiGroups":[""],"resources":["*/token"],"verbs":["create"]}`},
		{"star proxy", `{"apiGroups":[""],"resources":["*/proxy"],"verbs":["get"]}`},
		{"star star", `{"apiGroups":[""],"resources":["*/*"],"verbs":["get"]}`},
		{"star group", `{"apiGroups":["*"],"resources":["pods"],"verbs":["get"]}`},
		{"pods attach", `{"apiGroups":[""],"resources":["pods/attach"],"verbs":["create"]}`},
		{"pods ephemeralcontainers", `{"apiGroups":[""],"resources":["pods/ephemeralcontainers"],"verbs":["update"]}`},
		{"serviceaccounts token", `{"apiGroups":[""],"resources":["serviceaccounts/token"],"verbs":["create"]}`},
		{"csr approval", `{"apiGroups":["certificates.k8s.io"],"resources":["certificatesigningrequests/approval"],"verbs":["update"]}`},
		{"signers approve", `{"apiGroups":["certificates.k8s.io"],"resources":["signers"],"verbs":["approve"]}`},
		{"nodes proxy", `{"apiGroups":[""],"resources":["nodes/proxy"],"verbs":["get"]}`},
		{"resource without group", `{"resources":["secrets"],"verbs":["get"]}`},
	}
	for _, b := range bad {
		r := role()
		set(r, "rules/0", jv(b.rule))
		add("P6 role in ryft-rabbitmq: "+b.name, argoCtl, "CREATE", r, "P6: a Role or ClusterRole")
		k := cr()
		set(k, "rules/0", jv(b.rule))
		add("P6 clusterrole: "+b.name, argoCtl, "CREATE", k, "P6: a Role or ClusterRole")
	}
	topo := role()
	set(topo, "metadata/name", "messaging-topology-manager-role")
	add("P6 pods/exec in the topology role", argoCtl, "CREATE", topo, "P6: a Role or ClusterRole")
	add("P6 pods/exec in the cluster operator's role is its own", argoCtl, "CREATE", role())

	fmt.Println("== P6 with T12-g: the four Argo-written objects in the broker's namespace")
	for _, name := range []string{"role-cluster-operator-ryft-rabbitmq", "rolebinding-cluster-operator-ryft-rabbitmq", "role-topology-manager-ryft-rabbitmq", "rolebinding-topology-manager-ryft-rabbitmq"} {
		add("P6 "+name+" create", argoCtl, "CREATE", fx(name))
		add("P6 "+name+" update", argoCtl, "UPDATE", fx(name))
	}
	r := role()
	set(r, "metadata/name", "rabbitmq-peer-discovery")
	add("P6 argo writes the peer-discovery role", argoCtl, "CREATE", r, "P6: Roles and RoleBindings are allowed")
	r = role()
	set(r, "metadata/name", "rabbitmq-cluster-operator-role-2")
	add("P6 argo writes another operator role in ryft-rabbitmq", argoCtl, "CREATE", r, "P6: Roles and RoleBindings are allowed")
	r = role()
	set(r, "metadata/namespace", "ryft-api")
	add("P6 scoped role moved to ryft-api", argoCtl, "CREATE", r, "P6: Roles and RoleBindings are allowed")
	rb := func() map[string]any { return fx("rolebinding-cluster-operator-ryft-rabbitmq") }
	b := rb()
	set(b, "metadata/name", "rabbitmq-server")
	add("P6 argo writes the server rolebinding", argoCtl, "CREATE", b, "P6: Roles and RoleBindings are allowed")
	b = rb()
	set(b, "roleRef/kind", "ClusterRole")
	add("P6 rolebinding to a clusterrole", argoCtl, "CREATE", b, "P6: Roles and RoleBindings are allowed")
	b = rb()
	set(b, "roleRef/name", "messaging-topology-manager-role-evil")
	add("P6 rolebinding to another role", argoCtl, "CREATE", b, "P6: Roles and RoleBindings are allowed")
	b = rb()
	set(b, "subjects/0/name", "default")
	add("P6 rolebinding for another subject", argoCtl, "CREATE", b, "P6: Roles and RoleBindings are allowed")
	b = rb()
	set(b, "subjects/0/namespace", "ryft-rabbitmq")
	add("P6 rolebinding for a subject in ryft-rabbitmq", argoCtl, "CREATE", b, "P6: Roles and RoleBindings are allowed")
	r = role()
	set(r, "rules/0/verbs", jv(`["escalate"]`))
	add("P6 the scoped role with escalate", argoCtl, "CREATE", r, "P6: a Role or ClusterRole")
	add("P6 the release's clusterrole with secrets (before T12-g)", argoCtl, "CREATE", fx("clusterrole-rabbitmq-cluster-operator-role"), "P6: a ClusterRole of the operators must not grant Secrets")
	add("P6 the topology clusterrole with secrets (before T12-g)", argoCtl, "CREATE", fx("clusterrole-messaging-topology-manager-role"), "P6: a ClusterRole of the operators must not grant Secrets")
	k := cr()
	set(k, "rules/0", jv(`{"apiGroups":[""],"resources":["secrets"],"verbs":["get"]}`))
	add("P6 clusterrole with secrets", argoCtl, "CREATE", k, "P6: a ClusterRole of the operators must not grant Secrets")
	k = cr()
	set(k, "rules/0", jv(`{"apiGroups":[""],"resources":["pods"],"verbs":["update"]}`))
	add("P6 clusterrole with pod writes", argoCtl, "CREATE", k, "P6: a ClusterRole of the operators must not grant Secrets")
	k = cr()
	set(k, "rules/0", jv(`{"apiGroups":[""],"resources":["pods/exec"],"verbs":["create"]}`))
	add("P6 clusterrole with pods/exec", argoCtl, "CREATE", k, "P6: a ClusterRole of the operators must not grant Secrets")
	k = cr()
	set(k, "rules/0", jv(`{"apiGroups":[""],"resources":["pods"],"verbs":["get","list","watch"]}`))
	add("P6 clusterrole may read pods", argoCtl, "CREATE", k)

	fmt.Println("== P8: the Cluster Operator's RBAC writes")
	roleP8 := func(name string) map[string]any {
		o := role()
		set(o, "metadata/name", name)
		return o
	}
	rbP8 := func(name string) map[string]any {
		o := rb()
		set(o, "metadata/name", name)
		return o
	}
	add("P8 operator writes the peer-discovery role", cop, "CREATE", fx("role-peer-discovery"))
	add("P8 operator writes the server rolebinding", cop, "CREATE", fx("rolebinding-server"))
	x := fx("role-peer-discovery")
	set(x, "rules", jv(`[{"apiGroups":[""],"resources":["endpoints"],"verbs":["get"]},{"apiGroups":[""],"resources":["events"],"verbs":["create"]},{"apiGroups":[""],"resources":["secrets"],"verbs":["get"]}]`))
	add("P8 the peer-discovery role with a third rule", cop, "CREATE", x, "P8: the peer-discovery Role may hold only")
	x = fx("role-peer-discovery")
	set(x, "rules/0/verbs", jv(`["get","list"]`))
	add("P8 the peer-discovery role with more verbs", cop, "CREATE", x, "P8: the peer-discovery Role may hold only")
	x = fx("rolebinding-server")
	set(x, "roleRef/name", "rabbitmq-cluster-operator-role")
	add("P8 the server rolebinding to the operator's own role", cop, "CREATE", x, "P8: the server RoleBinding must bind")
	x = fx("rolebinding-server")
	set(x, "subjects/0/name", "rabbitmq-cluster-operator")
	add("P8 the server rolebinding for another account", cop, "CREATE", x, "P8: the server RoleBinding must bind")
	x = fx("rolebinding-server")
	set(x, "subjects/0/namespace", "rabbitmq-system")
	add("P8 the server rolebinding for an account elsewhere", cop, "CREATE", x, "P8: the server RoleBinding must bind")
	x = fx("rolebinding-server")
	set(x, "roleRef/kind", "ClusterRole")
	add("P8 the server rolebinding to a clusterrole", cop, "CREATE", x, "P8: the server RoleBinding must bind")
	add("P8 operator edits its own role", cop, "UPDATE", roleP8("rabbitmq-cluster-operator-role"), "P8: the Cluster Operator may write only")
	add("P8 operator edits the topology operator's role", cop, "UPDATE", roleP8("messaging-topology-manager-role"), "P8: the Cluster Operator may write only")
	add("P8 operator edits its own rolebinding", cop, "UPDATE", rbP8("rabbitmq-cluster-operator-rolebinding"), "P8: the Cluster Operator may write only")
	add("P8 topology operator writes a role", top, "CREATE", fx("role-peer-discovery"), "P8: this operator may not write this resource")

	fmt.Println("== P2: Pod UPDATE (orphaning, adoption), ephemeral containers, replication controllers")
	owned := brokerPod()
	orphan := deepCopy(owned)
	del(orphan, "metadata/ownerReferences")
	upd("P2 GC orphans a broker pod", gcController, orphan, owned)
	upd("P2 the statefulset-controller adopts it again", stsController, owned, orphan)
	upd("P2 an admin relabels a broker pod", admin, set(deepCopy(owned), "metadata/labels/x", "y"), owned)
	notify := func() map[string]any { return podOf(find("Deployment", "ryft-notify", "notify"), nil) }
	img := set(notify(), "spec/containers/0/image", "docker.io/evil/x:1")
	upd("P2 a pod update that swaps the image", admin, img, notify(), "P2: a container image")
	ec := func(c string) map[string]any {
		p := notify()
		set(p, "spec/ephemeralContainers", jv(c))
		return p
	}
	sub("P2 ephemeral container with the namespace's image", admin, "ephemeralcontainers",
		ec(`[{"name":"dbg","image":"ghcr.io/timkrebs/ryft/notify:7b4886f"}]`), notify())
	sub("P2 ephemeral container with a debug image", admin, "ephemeralcontainers",
		ec(`[{"name":"dbg","image":"busybox:1.36"}]`), notify(), "P2: a container image")
	tokenVol := `[{"name":"kube-api-access-abc","projected":{"sources":[{"serviceAccountToken":{"path":"token"}}]}}]`
	p := ec(`[{"name":"dbg","image":"ghcr.io/timkrebs/ryft/notify:7b4886f","volumeMounts":[{"name":"kube-api-access-abc","mountPath":"/t"}]}]`)
	set(p, "spec/volumes", jv(tokenVol))
	sub("P2 ephemeral container mounting the token volume", admin, "ephemeralcontainers", p, notify(), "P2: an ephemeral container")
	p = ec(`[{"name":"dbg","image":"ghcr.io/timkrebs/ryft/notify:7b4886f","volumeMounts":[{"name":"tok","mountPath":"/t"}]}]`)
	set(p, "spec/volumes", jv(`[{"name":"tok","projected":{"sources":[{"serviceAccountToken":{"path":"token"}}]}}]`))
	sub("P2 ephemeral container mounting a projected token under another name", admin, "ephemeralcontainers", p, notify(), "P2: an ephemeral container")
	p = ec(`[{"name":"dbg","image":"ghcr.io/timkrebs/ryft/notify:7b4886f","volumeMounts":[{"name":"v","mountPath":"/t"}]}]`)
	set(p, "spec/volumes", jv(`[{"name":"v","secret":{"secretName":"vault-auth-token"}}]`))
	sub("P2 ephemeral container mounting vault-auth-token", admin, "ephemeralcontainers", p, notify(), "P2: an ephemeral container")
	p = ec(`[{"name":"dbg","image":"ghcr.io/timkrebs/ryft/notify:7b4886f","envFrom":[{"secretRef":{"name":"vault-auth-token"}}]}]`)
	sub("P2 ephemeral container with envFrom vault-auth-token", admin, "ephemeralcontainers", p, notify(), "P2: an ephemeral container")
	bp := brokerPod()
	set(bp, "spec/ephemeralContainers", jv(`[{"name":"dbg","image":"ghcr.io/rabbitmq/x:1"}]`))
	sub("P2 ephemeral container in the broker with another image", admin, "ephemeralcontainers", bp, brokerPod(), "P2: a container image")
	rc := jv(`{"apiVersion":"v1","kind":"ReplicationController","metadata":{"name":"rc","namespace":"ryft-api"},"spec":{"template":{"spec":{"automountServiceAccountToken":false,"containers":[{"name":"c","image":"busybox"}]}}}}`).(map[string]any)
	add("P2 replicationcontroller with a foreign image", admin, "CREATE", rc, "P2: a container image")

	fmt.Println("== P3: endPort")
	np := func() map[string]any { return find("NetworkPolicy", "ryft-rabbitmq", "rabbitmq") }
	add("P3 the broker's own policy keeps its endPort", argoCtl, "CREATE", np())
	x = np()
	set(x, "spec/egress/0/to", jv(`[{"ipBlock":{"cidr":"10.0.0.0/16"}}]`))
	add("P3 endPort next to an ipBlock peer (broker, vpc cidr)", argoCtl, "CREATE", x, "P3: endPort is allowed only")
	x = np()
	set(x, "spec/egress/0/to", jv(`[{"ipBlock":{"cidr":"0.0.0.0/0","except":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","100.64.0.0/10","169.254.0.0/16"]}}]`))
	set(x, "spec/egress/0/ports", jv(`[{"port":35672,"endPort":35682,"protocol":"TCP"}]`))
	add("P3 endPort next to the public-egress ipBlock", argoCtl, "CREATE", x, "P3: endPort is allowed only")
	x = np()
	set(x, "spec/egress/0/ports/2/endPort", float64(40000))
	add("P3 another range in the broker", argoCtl, "CREATE", x, "P3: endPort is allowed only")
	x = np()
	set(x, "spec/egress/0/to", jv(`[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"ryft-api"}}}]`))
	add("P3 endPort next to a namespaceSelector peer", argoCtl, "CREATE", x, "P3: endPort is allowed only")
	x = np()
	set(x, "spec/ingress/3/from/0/ipBlock", jv(`{"cidr":"10.0.0.0/16"}`))
	add("P3 endPort in an ingress rule with an ipBlock peer", argoCtl, "CREATE", x, "P3: endPort is allowed only")

	fmt.Println("== the finalizer carve-out judges labels, annotations and owner references")
	deleting := func(o map[string]any) map[string]any {
		set(o, "metadata/deletionTimestamp", "2026-10-23T10:00:00Z")
		return o
	}
	sec := func(labels string) map[string]any {
		o := jv(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"repo-1","namespace":"argocd","finalizers":["x/y"]},"data":{"url":"eA=="}}`).(map[string]any)
		if labels != "" {
			set(o, "metadata/labels", jv(labels))
		}
		return deleting(o)
	}
	upd("carve-out: P7 secret-type label added to a deleting Secret", argoSrv,
		sec(`{"argocd.argoproj.io/secret-type":"repository"}`), sec(""), "P7: argocd-server must not write Secrets")
	upd("carve-out: a deleting Secret loses only its finalizer", argoSrv, set(sec(""), "metadata/finalizers", jv(`[]`)), sec(""))
	k = cr()
	oldK := deleting(deepCopy(k))
	newK := deepCopy(oldK)
	set(newK, "metadata/labels/rbac.authorization.k8s.io~aggregate-to-admin", "true")
	upd("carve-out: P6 aggregate label added to a deleting ClusterRole", argoCtl, newK, oldK, "P6: a ClusterRole must be named")
	oldK2 := deleting(deepCopy(k))
	newK2 := deepCopy(oldK2)
	set(newK2, "metadata/finalizers", jv(`[]`))
	upd("carve-out: a deleting ClusterRole loses only its finalizer", argoCtl, newK2, oldK2)
	ns := fx("namespace-as-infra-vault")
	oldN := deleting(deepCopy(ns))
	newN := deepCopy(oldN)
	set(newN, "metadata/labels/pod-security.kubernetes.io~enforce", "baseline")
	upd("carve-out: P1 PSA label lowered on a deleting Namespace", admin, newN, oldN, "P1: a RYFT")
	vs := defaults(find("VaultStaticSecret", "ryft-api", "ryft-secrets"))
	oldV := deleting(deepCopy(vs))
	newV := deepCopy(oldV)
	set(newV, "metadata/ownerReferences", jv(`[{"apiVersion":"v1","kind":"Pod","name":"p","uid":"u"}]`))
	upd("carve-out: an ownerReference added to a deleting VaultStaticSecret passes (no policy reads it)", "system:serviceaccount:vault-secrets-operator-system:vault-secrets-operator-controller-manager", newV, oldV)

	fmt.Println("== P7: argocd-server and status.sync, Application deletion")
	app := func() map[string]any { return find("Application", "argocd", "ryft-platform") }
	synced := func(rev string) map[string]any {
		a := app()
		set(a, "status/sync/revision", rev)
		return a
	}
	upd("P7 server leaves status.sync unchanged", argoSrv, synced("abc1234"), synced("abc1234"))
	upd("P7 server rewrites status.sync.revision", argoSrv, synced("deadbeef"), synced("abc1234"), "P7: argocd-server must not write status.sync")
	upd("P7 server adds status.sync", argoSrv, synced("deadbeef"), app(), "P7: argocd-server must not write status.sync")
	upd("P7 controller writes status.sync", argoCtl, synced("deadbeef"), synced("abc1234"))
	add("P7 server creates an Application with a status.sync", argoSrv, "CREATE", synced("deadbeef"), "P7: argocd-server must not write status.sync")
	ts = append(ts, n{"P7 server deletes an Application", Req{User: argoSrv, Op: "DELETE", Old: app()}, []string{"P7: argocd-server must not delete Applications"}})
	ts = append(ts, n{"P7 controller deletes an Application", Req{User: argoCtl, Op: "DELETE", Old: app()}, nil})

	fmt.Println("== P11: plugins, configuration, Service")
	rcl := func() map[string]any { return find("RabbitmqCluster", "ryft-rabbitmq", "rabbitmq") }
	x = rcl()
	set(x, "spec/rabbitmq/additionalPlugins", jv(`["rabbitmq_shovel"]`))
	add("P11 additional plugin", argoCtl, "CREATE", x, "P11: the broker's plugins")
	x = rcl()
	set(x, "spec/rabbitmq/advancedConfig", "[{rabbit, []}].")
	add("P11 advancedConfig", argoCtl, "CREATE", x, "P11: the broker's plugins")
	x = rcl()
	set(x, "spec/rabbitmq/envConfig", "X=1")
	add("P11 envConfig", argoCtl, "CREATE", x, "P11: the broker's plugins")
	x = rcl()
	set(x, "spec/rabbitmq/additionalConfig", "vm_memory_high_watermark.relative = 0.6\nlisteners.tcp.default = 5672\n")
	add("P11 additionalConfig with a new line", argoCtl, "CREATE", x, "P11: the broker's plugins")
	x = rcl()
	set(x, "spec/rabbitmq/additionalConfig", "# only comments\n\nlog.console.formatter = json\n")
	add("P11 additionalConfig with fewer lines", argoCtl, "CREATE", x)
	x = rcl()
	set(x, "spec/service/type", "LoadBalancer")
	add("P11 service LoadBalancer", argoCtl, "CREATE", x, "P11: the broker's Service")
	x = rcl()
	set(x, "spec/service/type", "ClusterIP")
	add("P11 service ClusterIP", argoCtl, "CREATE", x)

	fmt.Println("== P4, P5, P9 details")
	va := func() map[string]any { return defaults(find("VaultAuth", "ryft-api", "api")) }
	add("P4 vault namespace outside admin/ryft/", admin, "CREATE", set(va(), "spec/namespace", "admin"), "P4: a VaultAuth")
	add("P4 vault namespace under admin/ryft/", admin, "CREATE", set(va(), "spec/namespace", "admin/ryft/other"))
	cmBin := jv(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c","namespace":"ryft-api"},"binaryData":{"API_KEY":"eA=="}}`).(map[string]any)
	add("P5 binaryData API_KEY", argoCtl, "CREATE", cmBin, "P5: a ConfigMap or Secret")
	cmBin2 := jv(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c","namespace":"ryft-api"},"binaryData":{"AUTH_MODE":"eA=="}}`).(map[string]any)
	add("P5 binaryData AUTH_MODE", argoCtl, "CREATE", cmBin2, "P5: AUTH_MODE")
	ig := func() map[string]any { return find("Ingress", "ryft-api", "api") }
	add("P9 own namespace but a name outside the list", argoCtl, "CREATE", set(ig(), "metadata/annotations/traefik.ingress.kubernetes.io~router.middlewares", "ryft-api-other@kubernetescrd"), "P9: an Ingress")
	add("P9 a longer namespace sharing the prefix", argoCtl, "CREATE", set(ig(), "metadata/annotations/traefik.ingress.kubernetes.io~router.middlewares", "ryft-api-x-ratelimit@kubernetescrd"), "P9: an Ingress")

	fmt.Println("== N9: Namespaces as infra/vault and argocd.tf write them")
	add("N9 ryft-api as infra/vault creates it", admin, "CREATE", fx("namespace-as-infra-vault"))
	add("N9 ryft-api as infra/vault updates it", admin, "UPDATE", fx("namespace-as-infra-vault"))
	add("N9 argocd as argocd.tf creates it", admin, "CREATE", fx("namespace-as-argocd-tf"))
	add("N9 monitoring (privileged, D51) stays outside", admin, "UPDATE", fx("namespace-as-infra-vault-monitoring"))

	fmt.Println("== P6: a ClusterRole is read-only except rabbitmq.com, events and the review creates")
	clusterRule := func(rule string) map[string]any {
		k := cr()
		set(k, "rules/0", jv(rule))
		return k
	}
	readOnly := "P6: a ClusterRole of the operators is read-only"
	for _, res := range []struct{ group, name string }{
		{"", "configmaps"}, {"", "services"}, {"", "serviceaccounts"}, {"", "endpoints"},
		{"discovery.k8s.io", "endpointslices"}, {"", "persistentvolumeclaims"}, {"apps", "statefulsets"}, {"coordination.k8s.io", "leases"},
		{"rbac.authorization.k8s.io", "roles"},
	} {
		read := fmt.Sprintf(`{"apiGroups":[%q],"resources":[%q],"verbs":["get","list","watch"]}`, res.group, res.name)
		add("P6 clusterrole reads "+res.name, argoCtl, "CREATE", clusterRule(read))
		for _, verb := range []string{"create", "update", "patch", "delete"} {
			write := fmt.Sprintf(`{"apiGroups":[%q],"resources":[%q],"verbs":[%q]}`, res.group, res.name, verb)
			add("P6 clusterrole "+verb+"s "+res.name, argoCtl, "CREATE", clusterRule(write), readOnly)
		}
	}
	add("P6 clusterrole creates events", argoCtl, "CREATE", clusterRule(`{"apiGroups":[""],"resources":["events"],"verbs":["create","patch"]}`))
	add("P6 clusterrole creates events.k8s.io events", argoCtl, "CREATE", clusterRule(`{"apiGroups":["events.k8s.io"],"resources":["events"],"verbs":["create","patch"]}`))
	add("P6 clusterrole creates tokenreviews", argoCtl, "CREATE", clusterRule(`{"apiGroups":["authentication.k8s.io"],"resources":["tokenreviews"],"verbs":["create"]}`))
	add("P6 clusterrole creates subjectaccessreviews", argoCtl, "CREATE", clusterRule(`{"apiGroups":["authorization.k8s.io"],"resources":["subjectaccessreviews"],"verbs":["create"]}`))
	add("P6 clusterrole writes rabbitmqclusters", argoCtl, "CREATE", clusterRule(`{"apiGroups":["rabbitmq.com"],"resources":["rabbitmqclusters"],"verbs":["create","update","delete"]}`))
	add("P6 clusterrole writes queues status", argoCtl, "CREATE", clusterRule(`{"apiGroups":["rabbitmq.com"],"resources":["queues/status"],"verbs":["patch","update"]}`))
	add("P6 clusterrole updates bindings finalizers", argoCtl, "CREATE", clusterRule(`{"apiGroups":["rabbitmq.com"],"resources":["bindings/finalizers"],"verbs":["update"]}`))
	add("P6 clusterrole deletes tokenreviews (not a create)", argoCtl, "CREATE", clusterRule(`{"apiGroups":["authentication.k8s.io"],"resources":["tokenreviews"],"verbs":["delete"]}`), readOnly)

	fmt.Println("== P6: Roles in rabbitmq-system hold no Secrets and no pods/exec")
	le := func() map[string]any { return find("Role", "rabbitmq-system", "rabbitmq-cluster-leader-election-role") }
	add("P6 leader election role as released", argoCtl, "CREATE", le())
	x = le()
	set(x, "rules/2", jv(`{"apiGroups":[""],"resources":["secrets"],"verbs":["get"]}`))
	add("P6 leader election role with secrets", argoCtl, "CREATE", x, "P6: a Role in rabbitmq-system")
	x = le()
	set(x, "rules/2", jv(`{"apiGroups":[""],"resources":["pods/exec"],"verbs":["create"]}`))
	add("P6 leader election role with pods/exec", argoCtl, "CREATE", x, "P6: a Role")
	x = le()
	set(x, "metadata/name", "rabbitmq-cluster-operator-role")
	set(x, "rules/2", jv(`{"apiGroups":[""],"resources":["pods/exec"],"verbs":["create"]}`))
	add("P6 the operator's role name in rabbitmq-system with pods/exec", argoCtl, "CREATE", x, "P6: a Role")

	fmt.Println("== P11: erlangInetConfig, secretBackend")
	x = rcl()
	set(x, "spec/rabbitmq/erlangInetConfig", "[{inet6, true}].")
	add("P11 erlangInetConfig", argoCtl, "CREATE", x, "P11: the broker's plugins")
	x = rcl()
	set(x, "spec/secretBackend", jv(`{"vault":{"role":"x"}}`))
	add("P11 secretBackend", argoCtl, "CREATE", x, "P11: the broker takes no secretBackend")

	for _, t := range ts {
		report(c, t.name, t.req, t.want)
	}
}
