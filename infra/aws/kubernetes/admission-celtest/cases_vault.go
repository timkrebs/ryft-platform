package main

import "fmt"

// hcpAddress is the deleted HCP Vault's address, as infra/vault generated it before the rebuild. The
// committed overlays carry it until the follow-up PR commits infra/vault's regenerated ones, which
// name the in-cluster Vault (D207); the cases carry it as a constant, so they still mean HCP's
// address after that.
const hcpAddress = "https://ryft-prod-euc1-vault-public-vault-d9cda245.5ea5b8ce.z1.hashicorp.cloud:8200"

// Namespace vault: the in-cluster Vault (docs/work/2026-11-05-vault-ce/design.md sections 5 and 6),
// a Terraform add-on whose chart is not in the tree yet. The fixtures (testdata/objects.json,
// "vault-*") are the design's shapes: the Namespace, the server StatefulSet, the snapshot CronJob,
// the NetworkPolicies, the Certificates and the chart's RBAC, plus the VSO objects infra/vault
// generates for the in-cluster target. Terraform writes the add-on (the admin here); Argo CD
// writes the VSO objects and the per-namespace vault-ca Certificates. Every case name starts with
// "vault: ", so the kind fixtures the earlier cases emit keep their names.
func runVault(c *chart) {
	const (
		cmUser   = "system:serviceaccount:cert-manager:cert-manager"
		jobUser  = "system:serviceaccount:kube-system:job-controller"
		vaultImg = "docker.io/hashicorp/vault:2.2.1@sha256:962a92876e168d68eaaa24b0b8b49bbce1cb63024060a67bba95cd158a366fc2"
	)
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
		ts = append(ts, n{"vault: " + name, r, want})
	}
	upd := func(name, user string, obj, old map[string]any, want ...string) {
		ts = append(ts, n{"vault: " + name, Req{User: user, Op: "UPDATE", Obj: obj, Old: old}, want})
	}
	remove := func(name, user string, old map[string]any, want ...string) {
		ts = append(ts, n{"vault: " + name, Req{User: user, Op: "DELETE", Old: old}, want})
	}
	sub := func(name, user, subres string, obj, old map[string]any, want ...string) {
		ts = append(ts, n{"vault: " + name, Req{User: user, Op: "UPDATE", Sub: subres, Obj: obj, Old: old}, want})
	}

	fmt.Println("== namespace vault: P1")
	ns := func() map[string]any { return fx("vault-namespace") }
	add("P1 the namespace as infra/aws/kubernetes creates it", admin, "CREATE", ns())
	add("P1 the namespace as infra/aws/kubernetes updates it", admin, "UPDATE", ns())
	add("P1 the namespace without enforce", admin, "CREATE", del(ns(), "metadata/labels/pod-security.kubernetes.io~enforce"), "P1: a RYFT, rabbitmq-system, argocd or vault Namespace")
	add("P1 the namespace at baseline", admin, "UPDATE", set(ns(), "metadata/labels/pod-security.kubernetes.io~enforce", "baseline"), "P1: a RYFT, rabbitmq-system, argocd or vault Namespace")
	add("P1 the namespace with privileged audit", admin, "UPDATE", set(ns(), "metadata/labels/pod-security.kubernetes.io~audit", "privileged"), "P1: a RYFT, rabbitmq-system, argocd or vault Namespace")
	add("P1 the namespace with a pinned version", admin, "UPDATE", set(ns(), "metadata/labels/pod-security.kubernetes.io~enforce-version", "v1.36"), "P1: Pod Security version labels")
	add("P1 Argo CD creates the namespace", argoCtl, "CREATE", ns(), "P1: Argo CD may write only")
	add("P1 argocd-server updates the namespace", argoSrv, "UPDATE", ns(), "P1: Argo CD may write only")
	remove("P1 Argo CD deletes the namespace", argoCtl, ns(), "P1: Argo CD may write only")
	remove("P1 the admin deletes the namespace (Terraform's destroy)", admin, ns())
	add("P1 vault-x is not vault (the scope is the exact name)", admin, "CREATE", set(jm(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"vault-x"}}`), "metadata/labels", jv(`{}`)))

	fmt.Println("== namespace vault: P2 (the two pinned images, the chart's commands and tokens)")
	sts := func() map[string]any { return fx("vault-statefulset") }
	image := func(img string) map[string]any { return set(sts(), "spec/template/spec/containers/0/image", img) }
	add("P2 the server StatefulSet as the chart renders it", admin, "CREATE", sts())
	add("P2 the server StatefulSet updated", admin, "UPDATE", sts())
	owner := map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "vault", "controller": true, "uid": "u"}
	pod := func() map[string]any {
		p := podOf(sts(), owner)
		p["metadata"].(map[string]any)["name"] = "vault-0"
		// What the EKS Pod Identity webhook and the service account admission add.
		vols := p["spec"].(map[string]any)["volumes"].([]any)
		vols = append(vols,
			jv(`{"name":"eks-pod-identity-token","projected":{"sources":[{"serviceAccountToken":{"audience":"pods.eks.amazonaws.com","expirationSeconds":86400,"path":"eks-pod-identity-token"}}]}}`),
			jv(`{"name":"kube-api-access-abcde","projected":{"sources":[{"serviceAccountToken":{"expirationSeconds":3607,"path":"token"}}]}}`))
		p["spec"].(map[string]any)["volumes"] = vols
		return p
	}
	add("P2 a server pod with the pod identity token", stsController, "CREATE", pod())
	add("P2 the server image by tag only", admin, "CREATE", image("docker.io/hashicorp/vault:2.2.1"), "P2: a container image")
	add("P2 the server image without docker.io/ (the chart's default repository)", admin, "CREATE", image("hashicorp/vault:2.2.1@sha256:962a92876e168d68eaaa24b0b8b49bbce1cb63024060a67bba95cd158a366fc2"), "P2: a container image")
	add("P2 another Vault version with the same digest", admin, "CREATE", image("docker.io/hashicorp/vault:2.2.0@sha256:962a92876e168d68eaaa24b0b8b49bbce1cb63024060a67bba95cd158a366fc2"), "P2: a container image")
	add("P2 the right tag with another digest", admin, "CREATE", image("docker.io/hashicorp/vault:2.2.1@sha256:2194d86d9ddbb17a6d017c3b2e555e1d4431195f18c3cb2827e3f61320fb6855"), "P2: a container image")
	add("P2 the Enterprise image", admin, "CREATE", image("docker.io/hashicorp/vault-enterprise:2.2.1-ent@sha256:962a92876e168d68eaaa24b0b8b49bbce1cb63024060a67bba95cd158a366fc2"), "P2: a container image")
	add("P2 the pin with a suffix", admin, "CREATE", image(vaultImg+"x"), "P2: a container image")
	add("P2 a RYFT image in vault", admin, "CREATE", image("ghcr.io/timkrebs/ryft/api:7b4886f"), "P2: a container image")
	x := sts()
	set(x, "spec/template/spec/containers", append(x["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any), jv(`{"name":"reloader","image":"docker.io/library/busybox:1.36"}`)))
	add("P2 a sidecar with another image", admin, "CREATE", x, "P2: a container image")
	x = sts()
	set(x, "spec/template/spec/initContainers", jv(`[{"name":"init","image":"docker.io/library/busybox:1.36"}]`))
	add("P2 an init container with another image", admin, "CREATE", x, "P2: a container image")
	x = sts()
	vols := x["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["volumes"].([]any)
	set(x, "spec/template/spec/volumes", append(vols, jv(`{"name":"i","image":{"reference":"docker.io/library/busybox:1.36"}}`)))
	add("P2 an image volume", admin, "CREATE", x, "P2: image volumes are not allowed")
	podImg := set(pod(), "spec/containers/0/image", "docker.io/library/busybox:1.36")
	upd("P2 a pod update that swaps the image", admin, podImg, pod(), "P2: a container image")
	ec := set(pod(), "spec/ephemeralContainers", jv(`[{"name":"dbg","image":"docker.io/library/busybox:1.36"}]`))
	sub("P2 a debug container in a server pod", admin, "ephemeralcontainers", ec, pod(), "P2: a container image")
	ec2 := set(pod(), "spec/ephemeralContainers", jv(fmt.Sprintf(`[{"name":"dbg","image":%q}]`, vaultImg)))
	sub("P2 an ephemeral container with the server image", admin, "ephemeralcontainers", ec2, pod())
	// The fixture is charts/vault-extras' render (templates/snapshot.yaml) with the images vault.tf pins.
	cj := func() map[string]any { return fx("vault-cronjob-snapshot") }
	upload := func(img string) map[string]any {
		return set(cj(), "spec/jobTemplate/spec/template/spec/containers/0/image", img)
	}
	add("P2 the snapshot CronJob as vault-extras renders it (the pinned AWS CLI)", admin, "CREATE", cj())
	add("P2 the snapshot CronJob updated", admin, "UPDATE", cj())
	add("P2 the snapshot CronJob with the server image only", admin, "CREATE", upload(vaultImg))
	add("P2 the AWS CLI by tag only", admin, "CREATE", upload("public.ecr.aws/aws-cli/aws-cli:2.39.5"), "P2: a container image")
	add("P2 the AWS CLI's tag with another digest", admin, "CREATE", upload("public.ecr.aws/aws-cli/aws-cli:2.39.5@sha256:c6a538a5b1f1c0e5b9f4cbf0b6d1a5e9a0f7d2e3c4b5a6978877665544332211"), "P2: a container image")
	add("P2 another AWS CLI version with the pinned digest", admin, "CREATE", upload("public.ecr.aws/aws-cli/aws-cli:2.39.4@sha256:0cea63506f28774b70b1a3dd1934f49bc202d9784dee23370acc81438e8d0292"), "P2: a container image")
	add("P2 the AWS CLI from Docker Hub", admin, "CREATE", upload("docker.io/amazon/aws-cli:2.39.5@sha256:0cea63506f28774b70b1a3dd1934f49bc202d9784dee23370acc81438e8d0292"), "P2: a container image")
	cli := "public.ecr.aws/aws-cli/aws-cli:2.39.5@sha256:0cea63506f28774b70b1a3dd1934f49bc202d9784dee23370acc81438e8d0292"
	// The AWS CLI only under the snapshot job's service account (the Pod Identity association's).
	add("P2 the AWS CLI in the server StatefulSet (service account vault)", admin, "CREATE", image(cli), "P2: a container image")
	add("P2 the snapshot CronJob under another service account", admin, "CREATE", set(cj(), "spec/jobTemplate/spec/template/spec/serviceAccountName", "vault"), "P2: a container image")
	add("P2 the snapshot CronJob without a service account (default)", admin, "CREATE", del(cj(), "spec/jobTemplate/spec/template/spec/serviceAccountName"), "P2: a container image")
	add("P2 the server image only, under another service account", admin, "CREATE", set(upload(vaultImg), "spec/jobTemplate/spec/template/spec/serviceAccountName", "default"))
	snapPod := func() map[string]any {
		p := podOf(cj(), nil)
		// What the EKS Pod Identity webhook adds for the association vault/vault-snapshot.
		vols := p["spec"].(map[string]any)["volumes"].([]any)
		p["spec"].(map[string]any)["volumes"] = append(vols,
			jv(`{"name":"eks-pod-identity-token","projected":{"sources":[{"serviceAccountToken":{"audience":"pods.eks.amazonaws.com","expirationSeconds":86400,"path":"eks-pod-identity-token"}}]}}`))
		return p
	}
	add("P2 a snapshot pod with its projected vault token and the pod identity token", jobUser, "CREATE", snapPod())
	add("P2 a snapshot pod whose upload image is not the pin", jobUser, "CREATE", set(snapPod(), "spec/containers/0/image", "public.ecr.aws/aws-cli/aws-cli:latest"), "P2: a container image")
	add("P2 a pod of another service account with the AWS CLI", jobUser, "CREATE", set(snapPod(), "spec/serviceAccountName", "vault"), "P2: a container image")
	add("P2 the AWS CLI in a RYFT namespace", argoCtl, "CREATE", set(find("Deployment", "ryft-notify", "notify"), "spec/template/spec/containers/0/image", "public.ecr.aws/aws-cli/aws-cli:2.39.5@sha256:0cea63506f28774b70b1a3dd1934f49bc202d9784dee23370acc81438e8d0292"), "P2: a container image")
	add("P2 the server image in a RYFT namespace", argoCtl, "CREATE", set(find("Deployment", "ryft-notify", "notify"), "spec/template/spec/containers/0/image", vaultImg), "P2: a container image")

	fmt.Println("== namespace vault: P3 (default-deny, the server's and the job's flows)")
	np := func(name string) map[string]any { return fx("vault-netpol-" + name) }
	for _, name := range []string{"default-deny", "allow-dns", "server", "snapshot"} {
		add("P3 "+name+" as designed", admin, "CREATE", np(name))
		add("P3 "+name+" updated", admin, "UPDATE", np(name))
	}
	add("P3 default-deny with a rule", admin, "CREATE", set(np("default-deny"), "spec/egress", jv(`[{"to":[{"podSelector":{}}],"ports":[{"port":8200}]}]`)), "P3: default-deny must select every pod")
	add("P3 default-deny narrowed", admin, "UPDATE", set(np("default-deny"), "spec/podSelector", jv(`{"matchLabels":{"app.kubernetes.io/name":"vault-snapshot"}}`)), "P3: default-deny must select every pod")
	remove("P3 default-deny deleted in a live namespace", admin, np("default-deny"), "P3: default-deny can be deleted only")
	ts = append(ts, n{"vault: P3 default-deny deleted with its namespace", Req{User: "system:serviceaccount:kube-system:namespace-controller", Op: "DELETE", Old: np("default-deny"),
		NsObj: jm(`{"metadata":{"name":"vault","deletionTimestamp":"2026-11-05T10:00:00Z"}}`)}, nil})
	srv := func() map[string]any { return np("server") }
	add("P3 8200 from the VPC range", admin, "CREATE", set(srv(), "spec/ingress/0/from", jv(`[{"ipBlock":{"cidr":"10.0.0.0/16"}}]`)), "P3: an ingress ipBlock")
	add("P3 node metrics 9090 from the VPC (a ryft-* shape, not in vault)", admin, "CREATE", set(srv(), "spec/ingress/0", jv(`{"from":[{"ipBlock":{"cidr":"10.0.0.0/16"}}],"ports":[{"port":9090,"protocol":"TCP"}]}`)), "P3: an ingress ipBlock")
	add("P3 8200 from every namespace", admin, "CREATE", set(srv(), "spec/ingress/0/from", jv(`[{"namespaceSelector":{}}]`)), "P3: a namespaceSelector")
	add("P3 a rule without ports", admin, "CREATE", del(srv(), "spec/ingress/0/ports"), "P3: every NetworkPolicy rule")
	add("P3 the VPC on 8200 (egress)", admin, "CREATE", set(srv(), "spec/egress/1/ports", jv(`[{"port":8200,"protocol":"TCP"}]`)), "P3: an egress ipBlock")
	add("P3 the VPC on 443 with an except", admin, "CREATE", set(srv(), "spec/egress/1/to/0/ipBlock/except", jv(`["10.0.1.0/24"]`)), "P3: an egress ipBlock")
	add("P3 another private range on 443", admin, "CREATE", set(srv(), "spec/egress/1/to/0/ipBlock/cidr", "10.0.0.0/8"), "P3: an egress ipBlock")
	add("P3 the Pod Identity agent on 443", admin, "CREATE", set(srv(), "spec/egress/3/ports", jv(`[{"port":443,"protocol":"TCP"}]`)), "P3: an egress ipBlock")
	add("P3 the instance metadata service on 80", admin, "CREATE", set(srv(), "spec/egress/3/to/0/ipBlock/cidr", "169.254.169.254/32"), "P3: an egress ipBlock")
	add("P3 the link-local range on 80", admin, "CREATE", set(srv(), "spec/egress/3/to/0/ipBlock/cidr", "169.254.0.0/16"), "P3: an egress ipBlock")
	add("P3 the agent and the VPC in one rule on 80 and 443", admin, "CREATE", set(srv(), "spec/egress/1", jv(`{"to":[{"ipBlock":{"cidr":"10.0.0.0/16"}},{"ipBlock":{"cidr":"169.254.170.23/32"}}],"ports":[{"port":443,"protocol":"TCP"},{"port":80,"protocol":"TCP"}]}`)), "P3: an egress ipBlock")
	add("P3 public egress on 5432 (a ryft-* shape, not in vault)", admin, "CREATE", set(srv(), "spec/egress/2/ports", jv(`[{"port":5432,"protocol":"TCP"}]`)), "P3: an egress ipBlock")
	add("P3 public egress on 80", admin, "CREATE", set(srv(), "spec/egress/2/ports", jv(`[{"port":80,"protocol":"TCP"}]`)), "P3: an egress ipBlock")
	add("P3 public egress without the private ranges excepted", admin, "CREATE", del(srv(), "spec/egress/2/to/0/ipBlock/except"), "P3: an egress ipBlock")
	add("P3 an endPort in vault", admin, "CREATE", set(srv(), "spec/egress/0/ports/1/endPort", float64(8210)), "P3: endPort is allowed only")
	api := func() map[string]any { return find("NetworkPolicy", "ryft-api", "api") }
	add("P3 the VPC on 443 in a RYFT namespace (vault's shape only)", argoCtl, "CREATE", set(set(api(), "spec/egress/1/to/0/ipBlock", jv(`{"cidr":"10.0.0.0/16"}`)), "spec/egress/1/ports", jv(`[{"port":443,"protocol":"TCP"}]`)), "P3: an egress ipBlock")
	add("P3 the Pod Identity agent in a RYFT namespace (vault's shape only)", argoCtl, "CREATE", set(set(api(), "spec/egress/1/to/0/ipBlock", jv(`{"cidr":"169.254.170.23/32"}`)), "spec/egress/1/ports", jv(`[{"port":80,"protocol":"TCP"}]`)), "P3: an egress ipBlock")

	fmt.Println("== P4: the in-cluster shape of the VSO objects beside HCP's")
	vc := func() map[string]any { return fx("vault-vso-connection-api") }
	va := func() map[string]any { return defaults(fx("vault-vso-auth-api")) }
	vs := func() map[string]any { return defaults(fx("vault-vso-static-api")) }
	for _, o := range []struct {
		label string
		obj   func() map[string]any
	}{{"VaultConnection", vc}, {"VaultAuth", va}, {"VaultStaticSecret", vs}} {
		add("P4 the in-cluster "+o.label+" as infra/vault generates it", argoCtl, "CREATE", o.obj())
		add("P4 the in-cluster "+o.label+" updated", argoCtl, "UPDATE", o.obj())
	}
	add("P4 the in-cluster address without a CA (VSO's own trust store)", argoCtl, "CREATE", del(vc(), "spec/caCertSecretRef"))
	// Admitted while admission-values.yaml lists HCP's address; the follow-up PR drops it and makes this a
	// refusal (docs/work/2026-11-13-rebuild/followup-overlays.md).
	add("P4 the HCP address with caCertSecretRef vault-ca (admitted until the follow-up)", argoCtl, "CREATE", set(vc(), "spec/address", hcpAddress))
	add("P4 caCertSecretRef to another Secret", argoCtl, "CREATE", set(vc(), "spec/caCertSecretRef", "amqp-client"), "P4: a VaultConnection")
	add("P4 the in-cluster address over http", argoCtl, "CREATE", set(vc(), "spec/address", "http://vault.vault.svc:8200"), "P4: a VaultConnection")
	add("P4 the in-cluster address spelt with cluster.local", argoCtl, "CREATE", set(vc(), "spec/address", "https://vault.vault.svc.cluster.local:8200"), "P4: a VaultConnection")
	add("P4 the active pod's address", argoCtl, "CREATE", set(vc(), "spec/address", "https://vault-active.vault.svc:8200"), "P4: a VaultConnection")
	add("P4 a VaultConnection with headers", argoCtl, "CREATE", set(vc(), "spec/headers", jv(`{"X-Vault-Namespace":"admin"}`)), "P4: a VaultConnection")
	add("P4 a VaultConnection with skipTLSVerify", argoCtl, "CREATE", set(vc(), "spec/skipTLSVerify", true), "P4: a VaultConnection")
	add("P4 a VaultConnection with a timeout", argoCtl, "CREATE", set(vc(), "spec/timeout", "30s"), "P4: a VaultConnection")
	add("P4 an in-cluster VaultAuth with an HCP namespace (still admitted until the decommission)", argoCtl, "CREATE", set(va(), "spec/namespace", "admin/ryft/api"))
	add("P4 an in-cluster VaultAuth with the namespace root", argoCtl, "CREATE", set(va(), "spec/namespace", "root"), "P4: a VaultAuth")
	add("P4 an in-cluster VaultAuth with another mount", argoCtl, "CREATE", set(va(), "spec/mount", "kubernetes-other"), "P4: a VaultAuth")
	add("P4 another service's mount (kv-notify in ryft-api)", argoCtl, "CREATE", set(vs(), "spec/mount", "kv-notify"), "P4: a VaultStaticSecret")
	add("P4 the operator's mount (kv-operator)", argoCtl, "CREATE", set(vs(), "spec/mount", "kv-operator"), "P4: a VaultStaticSecret")
	add("P4 the mount spelt with the namespace (kv-ryft-api)", argoCtl, "CREATE", set(vs(), "spec/mount", "kv-ryft-api"), "P4: a VaultStaticSecret")
	add("P4 the bare prefix (kv-)", argoCtl, "CREATE", set(vs(), "spec/mount", "kv-"), "P4: a VaultStaticSecret")
	add("P4 the service's mount with a path in it", argoCtl, "CREATE", set(vs(), "spec/mount", "kv-api/data"), "P4: a VaultStaticSecret")
	moved := set(set(set(vs(), "metadata/namespace", "ryft-notify"), "spec/vaultAuthRef", "notify"), "spec/rolloutRestartTargets/0/name", "notify")
	add("P4 kv-api read from ryft-notify", argoCtl, "CREATE", moved, "P4: a VaultStaticSecret")
	add("P4 kv-notify read from ryft-notify", argoCtl, "CREATE", set(deepCopy(moved), "spec/mount", "kv-notify"))
	mon := set(set(set(vs(), "metadata/namespace", "monitoring"), "spec/vaultAuthRef", "monitoring"), "spec/mount", "kv-monitoring")
	del(mon, "spec/rolloutRestartTargets")
	add("P4 kv-monitoring read from monitoring (no ryft- prefix)", argoCtl, "CREATE", mon)
	add("P4 kv-monitoring read from ryft-monitoring passes P4 (both map to monitoring; the Vault role binds the namespace)", argoCtl, "CREATE", set(deepCopy(mon), "metadata/namespace", "ryft-monitoring"))
	amqp := set(set(set(set(vs(), "metadata/namespace", "ryft-rabbitmq"), "metadata/name", "amqp-user-api"), "spec/vaultAuthRef", "rabbitmq"), "spec/mount", "kv-rabbitmq")
	set(amqp, "spec/destination/name", "amqp-user-api")
	del(amqp, "spec/rolloutRestartTargets")
	add("P4 a broker user from kv-rabbitmq", argoCtl, "CREATE", amqp)
	add("P4 an HCP VaultStaticSecret is still admitted (mount kv)", argoCtl, "CREATE", set(vs(), "spec/mount", "kv"))
	// The reserved names (infra/vault refuses them as services): no namespace that maps to one may
	// read that mount, so kv-operator (the demo password) and the rest stay unread.
	in := func(ns, mount string) map[string]any {
		o := set(set(set(vs(), "metadata/namespace", ns), "spec/vaultAuthRef", "vault"), "spec/mount", mount)
		return del(o, "spec/rolloutRestartTargets")
	}
	for _, r := range []struct{ ns, mount string }{
		{"ryft-operator", "kv-operator"}, {"ryft-root", "kv-root"}, {"ryft-admin", "kv-admin"},
		{"ryft-terraform", "kv-terraform"}, {"default", "kv-default"}, {"ryft-vault-snapshot", "kv-vault-snapshot"},
		{"vault-snapshot", "kv-vault-snapshot"}, {"ryft-snapshot", "kv-snapshot"},
	} {
		add(fmt.Sprintf("P4 the reserved mount %s read from %s", r.mount, r.ns), argoCtl, "CREATE", in(r.ns, r.mount), "P4: a VaultStaticSecret")
	}
	add("P4 kv-operators read from ryft-operators (not a reserved name)", argoCtl, "CREATE", in("ryft-operators", "kv-operators"))
	add("P4 the mount kv still admitted in ryft-operator (HCP's, until the decommission)", argoCtl, "CREATE", in("ryft-operator", "kv"))

	fmt.Println("== P13: what ryft-vault may issue")
	tls := func() map[string]any { return fx("vault-certificate-tls") }
	ca := func() map[string]any { return fx("vault-certificate-ca") }
	cr := func() map[string]any { return fx("vault-certificaterequest") }
	server := "P13: vault/vault-tls may carry only"
	caMsg := "P13: a vault-ca Certificate must be"
	only := "P13: the issuer ryft-vault issues only"
	ref := "P13: a Certificate for ryft-vault must name"
	add("P13 vault/vault-tls as designed", admin, "CREATE", tls())
	add("P13 vault/vault-tls updated", admin, "UPDATE", tls())
	add("P13 vault/vault-tls with fewer names", admin, "CREATE", set(tls(), "spec/dnsNames", jv(`["vault.vault.svc"]`)))
	add("P13 ryft-api/vault-ca as designed", argoCtl, "CREATE", ca())
	add("P13 ryft-api/vault-ca updated", argoCtl, "UPDATE", ca())
	add("P13 vault/vault-ca for the operator's CLI", admin, "CREATE", set(set(ca(), "metadata/namespace", "vault"), "spec/commonName", "vso.vault"))
	add("P13 monitoring/vault-ca", argoCtl, "CREATE", set(set(ca(), "metadata/namespace", "monitoring"), "spec/commonName", "vso.monitoring"))
	add("P13 vault-ca without usages (cert-manager's default)", argoCtl, "CREATE", del(ca(), "spec/usages"))
	add("P13 a CertificateRequest by cert-manager", cmUser, "CREATE", cr())
	add("P13 a CertificateRequest updated by cert-manager", cmUser, "UPDATE", cr())
	add("P13 a CertificateRequest by the admin", admin, "CREATE", cr(), "P13: only cert-manager's controller")
	add("P13 a CertificateRequest from Argo CD", argoCtl, "CREATE", cr(), "P13: only cert-manager's controller")
	add("P13 a CertificateRequest from cert-manager's cainjector", "system:serviceaccount:cert-manager:cert-manager-cainjector", "CREATE", cr(), "P13: only cert-manager's controller")
	add("P13 a CertificateRequest updated by the admin", admin, "UPDATE", cr(), "P13: only cert-manager's controller")
	add("P13 a CertificateRequest of another issuer by the admin is not judged", admin, "CREATE", set(cr(), "spec/issuerRef/name", "ryft-internal"))
	add("P13 vault-tls in ryft-api", admin, "CREATE", set(tls(), "metadata/namespace", "ryft-api"), only)
	add("P13 another Certificate in vault", admin, "CREATE", set(set(tls(), "metadata/name", "vault-tls-2"), "spec/secretName", "vault-tls-2"), only)
	add("P13 a Certificate with a generated name", admin, "CREATE", set(del(tls(), "metadata/name"), "metadata/generateName", "vault-tls-"), only)
	ingressShim := set(set(set(tls(), "metadata/namespace", "ryft-api"), "metadata/name", "api-tls"), "spec/secretName", "api-tls")
	set(ingressShim, "spec/dnsNames", jv(`["api.ryftlab.de"]`))
	del(ingressShim, "spec/ipAddresses")
	add("P13 ingress-shim's Certificate for an Ingress annotated with ryft-vault", "system:serviceaccount:cert-manager:cert-manager", "CREATE", ingressShim, only)
	add("P13 vault-tls with another name", admin, "CREATE", set(tls(), "spec/dnsNames/0", "api.ryftlab.de"), server)
	add("P13 vault-tls with a wildcard", admin, "CREATE", set(tls(), "spec/dnsNames/0", "*.vault.svc"), server)
	add("P13 vault-tls with a pod IP", admin, "CREATE", set(tls(), "spec/ipAddresses", jv(`["127.0.0.1","10.0.3.7"]`)), server)
	add("P13 vault-tls with client auth", admin, "CREATE", set(tls(), "spec/usages", jv(`["server auth","client auth","digital signature"]`)), server)
	add("P13 vault-tls with cert sign", admin, "CREATE", set(tls(), "spec/usages", jv(`["server auth","digital signature","cert sign"]`)), server)
	add("P13 vault-tls as a CA", admin, "CREATE", set(tls(), "spec/isCA", true), server)
	add("P13 vault-tls with isCA false (not in the shape)", admin, "CREATE", set(tls(), "spec/isCA", false), server)
	add("P13 vault-tls into another Secret", admin, "CREATE", set(tls(), "spec/secretName", "vault-ca"), server)
	add("P13 vault-tls with a secretTemplate", admin, "CREATE", set(tls(), "spec/secretTemplate", jv(`{"annotations":{"reflector.v1.k8s.emberstack.com/reflection-allowed":"true"}}`)), server)
	add("P13 vault-tls with uris", admin, "CREATE", set(tls(), "spec/uris", jv(`["spiffe://ryft/vault"]`)), server)
	add("P13 vault-tls with a commonName", admin, "CREATE", set(tls(), "spec/commonName", "vault.vault.svc"), server)
	add("P13 vault-ca with the server's name", argoCtl, "CREATE", set(ca(), "spec/dnsNames", jv(`["vault.vault.svc"]`)), caMsg)
	add("P13 vault-ca with an IP address", argoCtl, "CREATE", set(ca(), "spec/ipAddresses", jv(`["127.0.0.1"]`)), caMsg)
	add("P13 vault-ca with server auth", argoCtl, "CREATE", set(ca(), "spec/usages", jv(`["server auth","digital signature"]`)), caMsg)
	add("P13 vault-ca with client auth", argoCtl, "CREATE", set(ca(), "spec/usages", jv(`["client auth","digital signature"]`)), caMsg)
	add("P13 vault-ca as a CA", argoCtl, "CREATE", set(ca(), "spec/isCA", true), caMsg)
	add("P13 vault-ca with another namespace's commonName", argoCtl, "CREATE", set(ca(), "spec/commonName", "vso.ryft-notify"), caMsg)
	add("P13 vault-ca with the server's commonName", argoCtl, "CREATE", set(ca(), "spec/commonName", "vault.vault.svc"), caMsg)
	add("P13 vault-ca without a commonName", argoCtl, "CREATE", del(ca(), "spec/commonName"), caMsg)
	add("P13 vault-ca with a literalSubject", argoCtl, "CREATE", set(del(ca(), "spec/commonName"), "spec/literalSubject", "CN=vault.vault.svc"), caMsg)
	add("P13 vault-ca with a subject", argoCtl, "CREATE", set(ca(), "spec/subject", jv(`{"organizations":["RYFT"]}`)), caMsg)
	add("P13 vault-ca with uris", argoCtl, "CREATE", set(ca(), "spec/uris", jv(`["spiffe://ryft/api"]`)), caMsg)
	add("P13 vault-ca with an e-mail address", argoCtl, "CREATE", set(ca(), "spec/emailAddresses", jv(`["ops@example.com"]`)), caMsg)
	add("P13 vault-ca into ryft-secrets", argoCtl, "CREATE", set(ca(), "spec/secretName", "ryft-secrets"), caMsg)
	add("P13 vault-ca with keystores", argoCtl, "CREATE", set(ca(), "spec/keystores", jv(`{"pkcs12":{"create":true}}`)), caMsg)
	add("P13 vault-ca naming a namespaced Issuer of that name", argoCtl, "CREATE", set(ca(), "spec/issuerRef/kind", "Issuer"), ref)
	add("P13 vault-ca naming the issuer without a kind (an Issuer)", argoCtl, "CREATE", del(ca(), "spec/issuerRef/kind"), ref)
	add("P13 vault-ca naming an external issuer group", argoCtl, "CREATE", set(ca(), "spec/issuerRef/group", "awspca.cert-manager.io"), ref)
	add("P13 vault-ca without a group (cert-manager's default)", argoCtl, "CREATE", del(ca(), "spec/issuerRef/group"))
	upd("P13 an update that adds the server's name to vault-ca", argoCtl, set(ca(), "spec/dnsNames", jv(`["vault.vault.svc"]`)), ca(), caMsg)
	upd("P13 an update that moves vault-ca to ryft-vault from another issuer", argoCtl, set(ca(), "spec/dnsNames", jv(`["vault.vault.svc"]`)), set(ca(), "spec/issuerRef/name", "ryft-internal"), caMsg)
	add("P13 a Certificate of ryft-internal for vault.vault.svc in another Secret is not P13's (VSO does not trust that CA)", admin, "CREATE", set(set(set(tls(), "metadata/name", "vault-internal-tls"), "spec/secretName", "vault-internal-tls"), "spec/issuerRef/name", "ryft-internal"))
	deleting := set(set(set(ca(), "spec/dnsNames", jv(`["vault.vault.svc"]`)), "metadata/deletionTimestamp", "2026-11-05T10:00:00Z"), "metadata/finalizers", jv(`["x/y"]`))
	upd("P13 a deleting bad Certificate loses its finalizer", cmUser, set(deepCopy(deleting), "metadata/finalizers", jv(`[]`)), deleting)
	// has() guards: an absent list is no list to check.
	add("P13 vault-tls with ipAddresses only (no dnsNames)", admin, "CREATE", del(tls(), "spec/dnsNames"))
	add("P13 vault-tls with dnsNames only (no ipAddresses)", admin, "CREATE", del(tls(), "spec/ipAddresses"))
	add("P13 vault-tls without usages (cert-manager's default)", admin, "CREATE", del(tls(), "spec/usages"))
	add("P13 vault-tls without a secretName", admin, "CREATE", del(tls(), "spec/secretName"), server)
	// The shape is keyed by the name: a vault-ca-shaped Certificate under another name is refused.
	add("P13 a vault-ca-shaped Certificate under another name", argoCtl, "CREATE", set(ca(), "metadata/name", "vault-ca-2"), only)

	fmt.Println("== P13: the reverse (the Secrets of the chain only from their issuer)")
	rev := "P13: the Secrets vault-ca and vault/vault-tls may be written only"
	rootMsg := "P13: the Secret ryft-vault-ca may be written only"
	add("P13 vault-ca from ryft-internal", argoCtl, "CREATE", set(ca(), "spec/issuerRef/name", "ryft-internal"), rev)
	add("P13 vault-ca from a selfSigned Issuer of its namespace", argoCtl, "CREATE", set(ca(), "spec/issuerRef", jv(`{"name":"selfsigned","kind":"Issuer"}`)), rev)
	add("P13 another Certificate writing the Secret vault-ca", argoCtl, "CREATE", set(set(set(ca(), "metadata/name", "api-ca"), "spec/issuerRef/name", "ryft-internal"), "spec/commonName", "api-ca"), rev)
	add("P13 vault/vault-tls from ryft-internal", admin, "CREATE", set(tls(), "spec/issuerRef/name", "ryft-internal"), rev)
	add("P13 the Secret vault-tls from letsencrypt in vault, another Certificate", admin, "CREATE", set(set(tls(), "metadata/name", "vault-public"), "spec/issuerRef/name", "letsencrypt-prod"), rev)
	add("P13 the Secret vault-tls outside vault is not P13's", argoCtl, "CREATE", set(set(set(set(tls(), "metadata/namespace", "ryft-api"), "metadata/name", "api-vault-tls"), "spec/issuerRef/name", "ryft-internal"), "spec/dnsNames", jv(`["api.ryft-api.svc"]`)))
	root := func() map[string]any { return fx("vault-certificate-root") }
	add("P13 the root CA as charts/cluster-issuers renders it", admin, "CREATE", root())
	add("P13 the root CA updated", admin, "UPDATE", root())
	add("P13 the root CA from another bootstrap issuer", admin, "CREATE", set(root(), "spec/issuerRef/name", "ryft-internal-bootstrap"), rootMsg)
	add("P13 the root CA Secret written by another Certificate", admin, "CREATE", set(root(), "metadata/name", "ryft-vault-ca-2"), rootMsg)
	add("P13 the root CA Secret in another namespace", admin, "CREATE", set(root(), "metadata/namespace", "ryft-api"), rootMsg)
	add("P13 the root CA from a namespaced Issuer of the bootstrap's name", admin, "CREATE", set(root(), "spec/issuerRef/kind", "Issuer"), rootMsg)
	add("P13 the root CA signed by ryft-vault itself", admin, "CREATE", set(root(), "spec/issuerRef/name", "ryft-vault"), rootMsg, only)
	add("P13 the root CA without an issuerRef name (has() guard)", admin, "CREATE", del(root(), "spec/issuerRef/name"), rootMsg)

	fmt.Println("== P13: Issuers and ClusterIssuers")
	ci := func() map[string]any { return fx("vault-clusterissuer") }
	boot := func() map[string]any { return fx("vault-clusterissuer-bootstrap") }
	issuer := func(ns, name, spec string) map[string]any {
		return jm(fmt.Sprintf(`{"apiVersion":"cert-manager.io/v1","kind":"Issuer","metadata":{"name":%q,"namespace":%q},"spec":%s}`, name, ns, spec))
	}
	clusterIssuer := func(name, spec string) map[string]any {
		return jm(fmt.Sprintf(`{"apiVersion":"cert-manager.io/v1","kind":"ClusterIssuer","metadata":{"name":%q},"spec":%s}`, name, spec))
	}
	exact := "P13: the ClusterIssuer ryft-vault must be exactly"
	onlySigner := "P13: only the ClusterIssuer ryft-vault may sign"
	bootMsg := "P13: the ClusterIssuer ryft-vault-bootstrap must be exactly"
	argoMsg := "P13: Argo CD may write only Issuers that are exactly selfSigned"
	add("P13 ClusterIssuer ryft-vault as charts/cluster-issuers renders it", admin, "CREATE", ci())
	add("P13 ClusterIssuer ryft-vault updated", admin, "UPDATE", ci())
	add("P13 ClusterIssuer ryft-vault-bootstrap as rendered", admin, "CREATE", boot())
	add("P13 ClusterIssuer ryft-vault-bootstrap updated", admin, "UPDATE", boot())
	add("P13 ClusterIssuer ryft-vault as selfSigned", admin, "CREATE", set(ci(), "spec", jv(`{"selfSigned":{}}`)), exact)
	add("P13 ClusterIssuer ryft-vault with another Secret", admin, "UPDATE", set(ci(), "spec/ca/secretName", "ryft-internal-ca"), exact)
	add("P13 ClusterIssuer ryft-vault with OCSP servers", admin, "CREATE", set(ci(), "spec/ca/ocspServers", jv(`["http://ocsp.example.com"]`)), exact)
	add("P13 ClusterIssuer ryft-vault with ca and acme", admin, "CREATE", set(ci(), "spec/acme", jv(`{"server":"https://acme.example.com","privateKeySecretRef":{"name":"k"},"solvers":[]}`)), exact)
	add("P13 ClusterIssuer ryft-vault with an empty ca (has() guard)", admin, "CREATE", set(ci(), "spec/ca", jv(`{}`)), exact)
	add("P13 another ClusterIssuer signing with ryft-vault-ca", admin, "CREATE", clusterIssuer("ryft-vault-2", `{"ca":{"secretName":"ryft-vault-ca"}}`), onlySigner)
	add("P13 an Issuer in cert-manager signing with ryft-vault-ca", admin, "CREATE", issuer("cert-manager", "vault-ca-copy", `{"ca":{"secretName":"ryft-vault-ca"}}`), onlySigner)
	add("P13 a namespaced Issuer named ryft-vault signing with ryft-vault-ca", admin, "CREATE", issuer("vault", "ryft-vault", `{"ca":{"secretName":"ryft-vault-ca"}}`), onlySigner)
	add("P13 a CA ClusterIssuer of another Secret is not P13's", admin, "CREATE", clusterIssuer("ryft-internal", `{"ca":{"secretName":"ryft-internal-ca"}}`))
	add("P13 an ACME ClusterIssuer by Terraform is not P13's (no ca: has() guard)", admin, "CREATE", clusterIssuer("letsencrypt-prod", `{"acme":{"server":"https://acme-v02.api.letsencrypt.org/directory","privateKeySecretRef":{"name":"letsencrypt-prod-account-key"},"solvers":[]}}`))
	add("P13 an Issuer with a ca but no secretName is not P13's (has() guard)", admin, "CREATE", issuer("ryft-api", "x", `{"ca":{}}`))
	add("P13 ClusterIssuer ryft-vault-bootstrap as a CA issuer", admin, "CREATE", set(boot(), "spec", jv(`{"ca":{"secretName":"ryft-internal-ca"}}`)), bootMsg)
	add("P13 ClusterIssuer ryft-vault-bootstrap with CRL points", admin, "UPDATE", set(boot(), "spec/selfSigned/crlDistributionPoints", jv(`["http://crl.example.com"]`)), bootMsg)
	add("P13 ClusterIssuer ryft-vault-bootstrap with an empty spec (has() guard)", admin, "CREATE", set(boot(), "spec", jv(`{}`)), bootMsg)
	opIssuer := func() map[string]any { return issuer("rabbitmq-system", "selfsigned-issuer", `{"selfSigned":{}}`) }
	add("P13 Argo CD writes the operators' selfSigned Issuer", argoCtl, "CREATE", opIssuer())
	add("P13 argocd-server updates the operators' selfSigned Issuer", argoSrv, "UPDATE", opIssuer())
	add("P13 Argo CD writes a CA Issuer", argoCtl, "CREATE", issuer("ryft-api", "api-ca", `{"ca":{"secretName":"amqp-client"}}`), argoMsg)
	add("P13 Argo CD writes a Vault Issuer that sends ryft-secrets to another server", argoCtl, "CREATE",
		issuer("rabbitmq-system", "selfsigned-issuer", `{"vault":{"server":"https://vault.example.com","path":"pki/sign/x","auth":{"tokenSecretRef":{"name":"ryft-secrets","key":"token"}}}}`), argoMsg)
	add("P13 Argo CD writes an ACME Issuer", argoCtl, "CREATE", issuer("ryft-api", "acme", `{"acme":{"server":"https://acme.example.com","privateKeySecretRef":{"name":"ryft-secrets"},"solvers":[]}}`), argoMsg)
	add("P13 Argo CD writes a selfSigned Issuer with CRL points", argoSrv, "CREATE", issuer("rabbitmq-system", "selfsigned-issuer", `{"selfSigned":{"crlDistributionPoints":["http://crl.example.com"]}}`), argoMsg)
	add("P13 Argo CD writes an Issuer with an empty spec (has() guard)", argoCtl, "CREATE", issuer("rabbitmq-system", "selfsigned-issuer", `{}`), argoMsg)
	add("P13 Argo CD writes a selfSigned ClusterIssuer", argoCtl, "CREATE", clusterIssuer("argo-selfsigned", `{"selfSigned":{}}`), argoMsg)
	add("P13 Argo CD writes ClusterIssuer ryft-vault as rendered", argoCtl, "UPDATE", ci(), argoMsg)

	fmt.Println("== P13: CertificateRequest status and Kubernetes CSRs")
	sub("P13 cert-manager approves and signs a CertificateRequest (status)", cmUser, "status", cr(), cr())
	sub("P13 the admin approves a CertificateRequest (status)", admin, "status", set(cr(), "status", jv(`{"conditions":[{"type":"Approved","status":"True","reason":"x","message":"y"}]}`)), cr(), "P13: only cert-manager's controller")
	sub("P13 Argo CD writes a CertificateRequest's status", argoCtl, "status", cr(), cr(), "P13: only cert-manager's controller")
	sub("P13 the status of another issuer's CertificateRequest is not judged", admin, "status", set(cr(), "spec/issuerRef/name", "ryft-internal"), set(cr(), "spec/issuerRef/name", "ryft-internal"))
	csr := func() map[string]any { return fx("vault-csr-k8s") }
	csrMsg := "P13: a Kubernetes CertificateSigningRequest for the signer"
	add("P13 a Kubernetes CSR for ryft-vault by the admin", admin, "CREATE", csr(), csrMsg)
	add("P13 a Kubernetes CSR for ryft-vault by cert-manager", cmUser, "CREATE", csr(), csrMsg)
	add("P13 a Kubernetes CSR for ryft-vault-bootstrap", admin, "CREATE", set(csr(), "spec/signerName", "clusterissuers.cert-manager.io/ryft-vault-bootstrap"), csrMsg)
	add("P13 a Kubernetes CSR for the API server's client signer is not P13's", admin, "CREATE", set(set(csr(), "spec/signerName", "kubernetes.io/kube-apiserver-client"), "spec/usages", jv(`["client auth"]`)))
	add("P13 a Kubernetes CSR for ryft-internal is not P13's", admin, "CREATE", set(csr(), "spec/signerName", "clusterissuers.cert-manager.io/ryft-internal"))
	add("P13 a Kubernetes CSR without a signerName is not judged (has() guard)", admin, "CREATE", del(csr(), "spec/signerName"))

	fmt.Println("== the chart's RBAC and Argo CD in vault (P6 today; P12, #121, for the rest)")
	add("P6 Terraform writes the discovery Role", admin, "CREATE", fx("vault-role-discovery"))
	add("P6 Terraform writes the discovery RoleBinding", admin, "CREATE", fx("vault-rolebinding-discovery"))
	add("P6 Terraform writes the auth-delegator ClusterRoleBinding", admin, "CREATE", fx("vault-clusterrolebinding-server"))
	add("P6 Terraform writes the service account", admin, "CREATE", fx("vault-serviceaccount"))
	add("P6 Argo CD writes the discovery Role", argoCtl, "CREATE", fx("vault-role-discovery"), "P6")
	add("P6 Argo CD writes the discovery RoleBinding", argoCtl, "CREATE", fx("vault-rolebinding-discovery"), "P6")
	add("P6 Argo CD writes the auth-delegator ClusterRoleBinding", argoCtl, "CREATE", fx("vault-clusterrolebinding-server"), "P6")
	// Admitted here: the writer is not judged by P1-P11 or P13 for these kinds. P12 (#121) refuses
	// every Argo CD write in vault (its namespace list does not hold vault); these cases then expect P12.
	add("P12's: Argo CD writes the server StatefulSet (admitted until #121)", argoCtl, "CREATE", sts())
	add("P12's: Argo CD writes vault/vault-tls (admitted until #121)", argoCtl, "CREATE", tls())
	add("P12's: Argo CD writes a NetworkPolicy in vault (admitted until #121)", argoCtl, "CREATE", np("server"))

	for _, t := range ts {
		report(c, t.name, t.req, t.want)
	}
}

// runDecom judges P4 as the decommission renders it (run.sh: vso.kvMount and
// vso.vaultNamespacePrefix "", vaultAddresses only the in-cluster address): the in-cluster shape
// passes, every HCP field is refused. Case names start with "decom: " (no kind fixture: the kind
// run installs the committed values).
func runDecom(c *chart) {
	fmt.Println("== P4 after the decommission (the decommission's values)")
	add := func(name string, obj map[string]any, want ...string) {
		report(c, "decom: "+name, Req{User: argoCtl, Op: "CREATE", Obj: obj}, want)
	}
	vc := func() map[string]any { return fx("vault-vso-connection-api") }
	va := func() map[string]any { return defaults(fx("vault-vso-auth-api")) }
	vs := func() map[string]any { return defaults(fx("vault-vso-static-api")) }
	add("the in-cluster VaultConnection", vc())
	add("the in-cluster VaultAuth", va())
	add("the in-cluster VaultStaticSecret", vs())
	add("the HCP address", set(vc(), "spec/address", hcpAddress), "P4: a VaultConnection")
	// HCP's shape as infra/vault generated it: its address and no caCertSecretRef (a public CA).
	add("the HCP VaultConnection as infra/vault generated it", del(set(vc(), "spec/address", hcpAddress), "spec/caCertSecretRef"), "P4: a VaultConnection")
	add("an HCP Vault namespace", set(va(), "spec/namespace", "admin/ryft/api"), "P4: a VaultAuth must use the kubernetes method and mount, audience vault, the local VaultConnection, no Vault namespace")
	add("an empty Vault namespace", set(va(), "spec/namespace", ""), "P4: a VaultAuth")
	add("the mount kv", set(vs(), "spec/mount", "kv"), "P4: a VaultStaticSecret must use a local VaultAuth, type kv-v2 on the mount kv-<its service> (never a reserved one)")
	add("an empty mount", set(vs(), "spec/mount", ""), "P4: a VaultStaticSecret")
	add("another service's mount", set(vs(), "spec/mount", "kv-notify"), "P4: a VaultStaticSecret")
	add("the in-cluster address without a CA (admitted: VSO fails TLS)", del(vc(), "spec/caCertSecretRef"))
}
