package main

import (
	"fmt"
	"time"
)

func runArgo(c *chart) {
	fmt.Println("== P7, P10, carve-outs and cost")
	app := func(name string) map[string]any { return find("Application", "argocd", name) }
	proj := func(name string) map[string]any { return find("AppProject", "argocd", name) }
	type x struct {
		name string
		req  Req
		want []string
	}
	var ts []x
	upd := func(name, user string, obj map[string]any, want ...string) {
		ts = append(ts, x{name, Req{User: user, Op: "UPDATE", Obj: obj, Old: deepCopy(obj)}, want})
	}
	updTo := func(name, user string, obj, old map[string]any, want ...string) {
		ts = append(ts, x{name, Req{User: user, Op: "UPDATE", Obj: obj, Old: old}, want})
	}
	del7 := func(name, user string, old map[string]any, want ...string) {
		ts = append(ts, x{name, Req{User: user, Op: "DELETE", Old: old}, want})
	}
	crt := func(name, user string, obj map[string]any, want ...string) {
		ts = append(ts, x{name, Req{User: user, Op: "CREATE", Obj: obj}, want})
	}
	crt("P7 app other project", argoSrv, set(app("vault-api"), "spec/project", "ryft-root"), "P7")
	crt("P7 app other path", argoSrv, set(app("vault-api"), "spec/source/path", "deploy/k8s/overlays/vault-namespaces/notify"), "P7")
	crt("P7 app path traversal", argoSrv, set(app("vault-api"), "spec/source/path", "deploy/k8s/overlays/vault-namespaces/api/.."), "P7")
	crt("P7 app other repo", argoSrv, set(app("vault-api"), "spec/source/repoURL", "https://github.com/evil/x.git"), "P7")
	crt("P7 app targetRevision", argoSrv, set(app("vault-api"), "spec/source/targetRevision", "feature"), "P7")
	crt("P7 app kustomize image", argoSrv, set(app("vault-api"), "spec/source/kustomize", jv(`{"images":["x=y"]}`)), "P7")
	crt("P7 app helm", argoSrv, set(app("vault-api"), "spec/source/helm", jv(`{"valueFiles":["x"]}`)), "P7")
	crt("P7 app plugin", argoSrv, set(app("vault-api"), "spec/source/plugin", jv(`{"name":"x"}`)), "P7")
	crt("P7 app directory", argoSrv, set(app("vault-api"), "spec/source/directory", jv(`{"recurse":true}`)), "P7")
	crt("P7 app sources", argoSrv, set(app("vault-api"), "spec/sources", jv(`[{"repoURL":"x"}]`)), "P7")
	crt("P7 app sourceHydrator", argoSrv, set(app("vault-api"), "spec/sourceHydrator", jv(`{}`)), "P7")
	crt("P7 app destination name", argoSrv, set(app("vault-api"), "spec/destination/name", "other-cluster"), "P7")
	crt("P7 app destination server", argoSrv, set(app("vault-api"), "spec/destination/server", "https://evil.example"), "P7")
	crt("P7 app destination namespace", argoSrv, set(app("vault-api"), "spec/destination/namespace", "kube-system"), "P7")
	crt("P7 app unknown name", argoSrv, set(app("vault-api"), "metadata/name", "evil"), "P7")
	crt("P7 vault-x pattern ok", argoSrv, set(set(set(app("vault-api"), "metadata/name", "vault-future"), "spec/source/path", "deploy/k8s/overlays/vault-namespaces/future"), "spec/destination/namespace", "ryft-future"))
	crt("P7 vault-x wrong namespace", argoSrv, set(set(app("vault-api"), "metadata/name", "vault-future"), "spec/source/path", "deploy/k8s/overlays/vault-namespaces/future"), "P7")
	crt("P7 vault-.. invalid", argoSrv, set(set(set(app("vault-api"), "metadata/name", "vault-.."), "spec/source/path", "deploy/k8s/overlays/vault-namespaces/.."), "spec/destination/namespace", "ryft-.."), "P7")
	crt("P7 app extra spec key", argoSrv, set(app("vault-api"), "spec/revisionHistoryLimit", float64(3)), "P7")
	crt("P7 app syncPolicy is free", argoSrv, set(app("vault-api"), "spec/syncPolicy", jv(`{"automated":{"prune":false}}`)))
	crt("P7 root app moved", argoSrv, set(app("ryft-root"), "spec/source/path", "deploy"), "P7")
	crt("P7 appset", argoSrv, fx("applicationset-x"), "P7")
	crt("P7 project widened", argoSrv, set(proj("ryft"), "spec/clusterResourceWhitelist", jv(`[{"group":"*","kind":"*"}]`)), "P7")
	crt("P7 project unknown", argoSrv, set(proj("ryft"), "metadata/name", "evil"), "P7")
	crt("P7 project as committed", admin, proj("ryft"))
	crt("P7 default project loosened", admin, set(proj("default"), "spec/sourceRepos", jv(`["*"]`)), "P7")
	del7("P7 delete default (admin)", admin, proj("default"), "P7")
	del7("P7 delete ryft by argocd-server", argoSrv, proj("ryft"), "P7")
	del7("P7 delete ryft by the controller", argoCtl, proj("ryft"), "P7")
	del7("P7 delete ryft by an admin", admin, proj("ryft"))
	del7("P7 delete an application (argocd-server)", argoSrv, app("vault-api"), "P7")
	del7("P7 delete an application (controller)", argoCtl, app("vault-api"))
	// controller carve-out: status and operation writes with an unchanged spec
	bad := set(app("vault-api"), "spec/destination/namespace", "kube-system")
	upd("P7 controller status write with an unchanged (bad) spec is not judged", argoCtl, bad)
	good := app("vault-api")
	updTo("P7 controller spec change is judged", argoCtl, bad, good, "P7")
	upd("P7 argocd-server unchanged bad spec is judged", argoSrv, bad, "P7")
	// secrets
	sec := func(labels map[string]any) map[string]any {
		md := map[string]any{"name": "repo-1", "namespace": "argocd"}
		if labels != nil {
			md["labels"] = labels
		}
		return map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": md, "data": map[string]any{"url": "eA=="}}
	}
	crt("P7 server writes a repository secret", argoSrv, sec(map[string]any{"argocd.argoproj.io/secret-type": "repository"}), "P7")
	crt("P7 controller writes a labelled secret", argoCtl, sec(map[string]any{"argocd.argoproj.io/secret-type": "repository"}))
	crt("P7 server writes an unlabelled secret", argoSrv, sec(nil))
	del7("P7 server deletes a cluster secret", argoSrv, sec(map[string]any{"argocd.argoproj.io/secret-type": "cluster"}), "P7")
	// P10
	synced := func() map[string]any {
		a := app("ryft-platform")
		set(a, "status/sync/revision", "abc1234def")
		return a
	}
	withOp := func(op string) map[string]any {
		a := synced()
		a["operation"] = jv(op)
		return a
	}
	updTo("P10 server sync revision empty", argoSrv, withOp(`{"sync":{"revision":""}}`), synced())
	updTo("P10 server sync revision main", argoSrv, withOp(`{"sync":{"revision":"main"}}`), synced())
	updTo("P10 server sync no revision", argoSrv, withOp(`{"sync":{"prune":true}}`), synced())
	updTo("P10 server sync revision of the synced state", argoSrv, withOp(`{"sync":{"revision":"abc1234def"}}`), synced())
	updTo("P10 server sync other revision", argoSrv, withOp(`{"sync":{"revision":"deadbeef"}}`), synced(), "P10")
	updTo("P10 server sync manifests (--local)", argoSrv, withOp(`{"sync":{"manifests":["{}"]}}`), synced(), "P10")
	updTo("P10 server sync source (rollback)", argoSrv, withOp(`{"sync":{"source":{"repoURL":"x"},"revision":"abc1234def"}}`), synced(), "P10")
	updTo("P10 server sync sources", argoSrv, withOp(`{"sync":{"sources":[{"repoURL":"x"}]}}`), synced(), "P10")
	updTo("P10 admin sync manifests", admin, withOp(`{"sync":{"manifests":["{}"]}}`), synced(), "P10")
	updTo("P10 admin other revision is not judged", admin, withOp(`{"sync":{"revision":"deadbeef"}}`), synced())
	updTo("P10 controller automated sync with the commit", argoCtl, withOp(`{"initiatedBy":{"automated":true},"sync":{"revision":"cafe1234"}}`), synced())
	inflight := withOp(`{"sync":{"manifests":["{}"]}}`)
	updTo("P10 controller status write carries the unchanged operation", argoCtl, inflight, deepCopy(inflight))
	terminate := synced()
	updTo("P10 terminate-op (operation removed)", argoSrv, terminate, withOp(`{"sync":{"manifests":["{}"]}}`))
	crt("P10 create with manifests", argoSrv, func() map[string]any {
		a := app("vault-api")
		a["operation"] = jv(`{"sync":{"manifests":["x"]}}`)
		return a
	}(), "P10")
	updTo("P10 pause (syncPolicy none, no operation)", argoSrv, set(synced(), "spec/syncPolicy", jv(`{}`)), synced())
	updTo("P10 hard refresh annotation", argoSrv, set(synced(), "metadata/annotations/argocd.argoproj.io~refresh", "hard"), synced())

	// finalizer carve-out (VSO removes its finalizer from a VaultStaticSecret that is being deleted)
	vs := defaults(find("VaultStaticSecret", "ryft-api", "ryft-secrets"))
	vsOld := deepCopy(vs)
	set(vsOld, "metadata/deletionTimestamp", "2026-10-23T10:00:00Z")
	set(vsOld, "metadata/finalizers", jv(`["secrets.hashicorp.com/finalizer"]`))
	set(vsOld, "spec/destination/transformation/excludeRaw", false) // already bad before
	vsNew := deepCopy(vsOld)
	set(vsNew, "metadata/finalizers", jv(`[]`))
	vsonly := "system:serviceaccount:vault-secrets-operator-system:vault-secrets-operator-controller-manager"
	updTo("carve-out: VSO finalizer removal on a deleting object passes", vsonly, vsNew, vsOld)
	vsNew2 := deepCopy(vsNew)
	set(vsNew2, "spec/path", "other")
	updTo("carve-out: a spec change on a deleting object is judged", vsonly, vsNew2, vsOld, "P4")
	vsNew3 := deepCopy(vsOld)
	set(vsNew3, "metadata/labels/x", "y")
	updTo("carve-out: a label change on a deleting object is judged", vsonly, vsNew3, vsOld, "P4")
	vsLive := deepCopy(vsOld)
	delete(vsLive["metadata"].(map[string]any), "deletionTimestamp")
	vsLiveNew := deepCopy(vsLive)
	set(vsLiveNew, "metadata/finalizers", jv(`[]`))
	updTo("carve-out: a live object with the same bad spec is judged", vsonly, vsLiveNew, vsLive, "P4")

	for _, t := range ts {
		report(c, t.name, t.req, t.want)
	}

	// cost: a large Deployment and Pod (250 containers, 100 env each, 100 volumes)
	big := find("Deployment", "ryft-notify", "notify")
	cont := big["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	var cs []any
	for i := 0; i < 250; i++ {
		cc := deepCopy(cont)
		cc["name"] = fmt.Sprintf("c%d", i)
		var env []any
		for j := 0; j < 100; j++ {
			env = append(env, map[string]any{"name": fmt.Sprintf("E%d", j), "value": "v"})
		}
		cc["env"] = env
		cs = append(cs, cc)
	}
	var vols []any
	for i := 0; i < 100; i++ {
		vols = append(vols, map[string]any{"name": fmt.Sprintf("v%d", i), "emptyDir": map[string]any{}})
	}
	set(big, "spec/template/spec/containers", cs)
	set(big, "spec/template/spec/volumes", vols)
	t0 := time.Now()
	report(c, "cost: 250 containers x 100 env x 100 volumes, deployment", Req{User: argoCtl, Op: "CREATE", Obj: big}, nil)
	pod := podOf(big, nil)
	report(c, "cost: the same as a pod", Req{User: "system:serviceaccount:kube-system:replicaset-controller", Op: "CREATE", Obj: pod}, nil)
	fmt.Println("   elapsed", time.Since(t0))
}
