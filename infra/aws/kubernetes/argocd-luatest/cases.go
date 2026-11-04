package main

// A case: the object Argo CD hands to the script as obj, and the health it must get back.
// message is a substring ("" on a Healthy case means the message must be empty).
type tcase struct {
	name    string
	obj     map[string]any
	status  string
	message string
}

type m = map[string]any

func cond(typ, status, reason, message string) m {
	c := m{"type": typ, "status": status}
	if reason != "" {
		c["reason"] = reason
	}
	if message != "" {
		c["message"] = message
	}
	return c
}

func withConds(kind string, conds ...any) m {
	return m{"kind": kind, "metadata": m{"name": "x", "generation": float64(3)}, "status": m{"conditions": conds, "observedGeneration": float64(3)}}
}

// stale is an object whose conditions are those of an older generation.
func stale(o m, generation, observed any) m {
	o["metadata"].(m)["generation"] = generation
	if observed == nil {
		delete(o["status"].(m), "observedGeneration")
	} else {
		o["status"].(m)["observedGeneration"] = observed
	}
	return o
}

func bare(kind string) m { return m{"kind": kind, "metadata": m{"name": "x"}} }

const hp = prefix

func allCases() map[string][]tcase {
	out := map[string][]tcase{}

	// RabbitmqCluster (Cluster Operator v2.23.1): healthy with AllReplicasReady and
	// ReconcileSuccess both True; the reasons are the operator's own.
	rc := func(c ...any) m { return withConds("RabbitmqCluster", c...) }
	arrTrue := cond("AllReplicasReady", "True", "AllPodsAreReady", "")
	rsTrue := cond("ReconcileSuccess", "True", "Success", "Finish reconciling")
	cluster := []tcase{
		{"no status", bare("RabbitmqCluster"), "Progressing", "waiting for the RabbitMQ operators"},
		{"status without conditions", m{"kind": "RabbitmqCluster", "status": m{}}, "Progressing", "waiting"},
		{"empty conditions", rc(), "Progressing", ""},
		{"all True", rc(arrTrue, rsTrue, cond("ClusterAvailable", "True", "AtLeastOneEndpointAvailable", "")), "Healthy", ""},
		{"all True, an unwanted condition False", rc(arrTrue, rsTrue, cond("NoWarnings", "False", "FailedSomething", "x")), "Healthy", ""},
		{"node roll: AllReplicasReady False NotAllPodsReady", rc(cond("AllReplicasReady", "False", "NotAllPodsReady", "2/3 Pods ready"), rsTrue), "Progressing", "AllReplicasReady: 2/3 Pods ready"},
		{"scaled to zero", rc(cond("AllReplicasReady", "False", "ScaledToZero", ""), rsTrue), "Progressing", "AllReplicasReady: ScaledToZero"},
		{"AllReplicasReady Unknown", rc(cond("AllReplicasReady", "Unknown", "MissingStatefulSet", "Could not find StatefulSet"), rsTrue), "Progressing", "Could not find StatefulSet"},
		{"ReconcileSuccess False without a reason", rc(arrTrue, cond("ReconcileSuccess", "False", "", "")), "Degraded", "ReconcileSuccess: not ready"},
		{"ReconcileSuccess False, generic Error", rc(arrTrue, cond("ReconcileSuccess", "False", "Error", "invalid additionalConfig")), "Degraded", "ReconcileSuccess: invalid additionalConfig"},
		{"empty message falls back to the reason", rc(arrTrue, m{"type": "ReconcileSuccess", "status": "False", "reason": "Error", "message": ""}), "Degraded", "ReconcileSuccess: Error"},
		{"empty message on a pending condition falls back to the reason", rc(m{"type": "AllReplicasReady", "status": "False", "reason": "ScaledToZero", "message": ""}, rsTrue), "Progressing", "AllReplicasReady: ScaledToZero"},
		{"a condition type twice counts once", rc(arrTrue, arrTrue), "Progressing", ""},
		{"spec changed: conditions True, generation 4 observed 3", stale(rc(arrTrue, rsTrue), float64(4), float64(3)), "Progressing", "reconcile generation 4"},
		{"spec changed: no observedGeneration yet", stale(rc(arrTrue, rsTrue), float64(1), nil), "Progressing", "reconcile generation 1"},
		{"observed generation caught up", stale(rc(arrTrue, rsTrue), float64(4), float64(4)), "Healthy", ""},
		{"no generation in the object", m{"kind": "RabbitmqCluster", "status": m{"conditions": []any{arrTrue, rsTrue}}}, "Healthy", ""},
		{"failure wins over a stale generation", stale(rc(arrTrue, cond("ReconcileSuccess", "False", "InvalidConfiguration", "bad")), float64(4), float64(3)), "Degraded", "ReconcileSuccess: bad"},
		{"only AllReplicasReady", rc(arrTrue), "Progressing", ""},
		{"only ReconcileSuccess", rc(rsTrue), "Progressing", ""},
		{"neither wanted condition", rc(cond("ClusterAvailable", "True", "AtLeastOneEndpointAvailable", "")), "Progressing", ""},
		{"failure reported while a node rolls", rc(cond("AllReplicasReady", "False", "NotAllPodsReady", "2/3 Pods ready"), cond("ReconcileSuccess", "False", "FailedCLICommand", "boom")), "Degraded", "ReconcileSuccess: boom"},
		{"failure listed before the progressing condition", rc(cond("ReconcileSuccess", "False", "FailedCLICommand", "boom"), cond("AllReplicasReady", "False", "NotAllPodsReady", "")), "Degraded", "boom"},
		{"failure reason on a True condition is ignored", rc(arrTrue, cond("ReconcileSuccess", "True", "FailedCLICommand", "")), "Healthy", ""},
	}
	// Every reason the Cluster Operator v2.23.1 writes with ReconcileSuccess=False (see argocd.tf).
	for _, r := range []string{
		"Error", "InvalidStatefulSetSelectorOverride", "FailedReconcilePVC", "InvalidConfiguration",
		"FailedVersionAnnotation", "FailedCLICommand", "TLSError", "UnsupportedOperation", "TransformErrorOperation",
	} {
		cluster = append(cluster, tcase{"ReconcileSuccess False " + r, rc(arrTrue, cond("ReconcileSuccess", "False", r, "it broke")), "Degraded", "ReconcileSuccess: it broke"})
	}
	out[hp+"rabbitmq.com_RabbitmqCluster"] = cluster

	// The topology kinds (Messaging Topology Operator v1.20.4): Ready.
	for _, kind := range []string{"Vhost", "Exchange", "Queue", "Binding", "Policy", "User", "Permission", "TopicPermission"} {
		tc := func(c ...any) m { return withConds(kind, c...) }
		out[hp+"rabbitmq.com_"+kind] = []tcase{
			{"no status", bare(kind), "Progressing", "waiting for the RabbitMQ operators"},
			{"empty conditions", tc(), "Progressing", ""},
			{"Ready True", tc(cond("Ready", "True", "SuccessfulCreateOrUpdate", "")), "Healthy", ""},
			{"Ready True next to PasswordSynced", tc(cond("PasswordSynced", "True", "SuccessfulPasswordSync", ""), cond("Ready", "True", "SuccessfulCreateOrUpdate", "")), "Healthy", ""},
			{"Ready False FailedCreateOrUpdate", tc(cond("Ready", "False", "FailedCreateOrUpdate", "vhost not found")), "Degraded", "Ready: vhost not found"},
			{"Ready False FailedDelete", tc(cond("Ready", "False", "FailedDelete", "cannot delete")), "Degraded", "Ready: cannot delete"},
			// v1.20.4 writes FailedCreateOrUpdate for every Ready=False, so any other reason is
			// outside the operator's contract; the default is still Degraded.
			{"Ready False, other reason (defensive default)", tc(cond("Ready", "False", "Waiting", "the cluster is not there yet")), "Degraded", "Ready: the cluster is not there yet"},
			{"Ready False without reason or message", tc(cond("Ready", "False", "", "")), "Degraded", "Ready: not ready"},
			{"Ready False, empty message falls back to the reason", tc(m{"type": "Ready", "status": "False", "reason": "FailedCreateOrUpdate", "message": ""}), "Degraded", "Ready: FailedCreateOrUpdate"},
			{"spec changed: Ready True, generation 4 observed 3", stale(tc(cond("Ready", "True", "SuccessfulCreateOrUpdate", "")), float64(4), float64(3)), "Progressing", "reconcile generation 4"},
			{"spec changed: no observedGeneration yet", stale(tc(cond("Ready", "True", "SuccessfulCreateOrUpdate", "")), float64(1), nil), "Progressing", "reconcile generation 1"},
			{"observed generation caught up", stale(tc(cond("Ready", "True", "SuccessfulCreateOrUpdate", "")), float64(4), float64(4)), "Healthy", ""},
			{"failure wins over a stale generation", stale(tc(cond("Ready", "False", "FailedCreateOrUpdate", "no")), float64(4), float64(3)), "Degraded", "Ready: no"},
			{"Ready Unknown", tc(cond("Ready", "Unknown", "", "")), "Progressing", "Ready: not ready"},
			{"only another condition", tc(cond("PasswordSynced", "True", "SuccessfulPasswordSync", "")), "Progressing", ""},
		}
	}

	// D105: an autoscaler is healthy unless it cannot scale.
	hpa := func(c ...any) m { return withConds("HorizontalPodAutoscaler", c...) }
	out[hp+"autoscaling_HorizontalPodAutoscaler"] = []tcase{
		{"no status", bare("HorizontalPodAutoscaler"), "Healthy", ""},
		{"no metrics yet after a rollout", hpa(cond("AbleToScale", "True", "ReadyForNewScale", ""), cond("ScalingActive", "False", "FailedGetResourceMetric", "no metrics yet")), "Healthy", "FailedGetResourceMetric: no metrics yet"},
		{"metrics not computable", hpa(cond("ScalingActive", "False", "FailedComputeMetricsReplicas", "x")), "Healthy", "FailedComputeMetricsReplicas"},
		{"scaling disabled", hpa(cond("ScalingActive", "False", "ScalingDisabled", "")), "Healthy", "ScalingDisabled"},
		{"scaling inactive for another reason", hpa(cond("ScalingActive", "False", "InvalidSelector", "bad selector")), "Degraded", "bad selector"},
		{"cannot scale", hpa(cond("AbleToScale", "False", "FailedGetScale", "no such target")), "Degraded", "no such target"},
		{"able and active", hpa(cond("AbleToScale", "True", "ReadyForNewScale", ""), cond("ScalingActive", "True", "ValidMetricFound", "")), "Healthy", ""},
	}

	// The Application health the app of apps waits on.
	app := func(health m) m { return m{"kind": "Application", "status": health} }
	out[hp+"argoproj.io_Application"] = []tcase{
		{"no status", bare("Application"), "Progressing", ""},
		{"no health yet", m{"kind": "Application", "status": m{}}, "Progressing", ""},
		{"Healthy", app(m{"health": m{"status": "Healthy"}}), "Healthy", ""},
		{"Degraded with message", app(m{"health": m{"status": "Degraded", "message": "a wave failed"}}), "Degraded", "a wave failed"},
		{"Progressing", app(m{"health": m{"status": "Progressing"}}), "Progressing", ""},
	}

	// A namespace's Secret from Vault.
	vss := func(c ...any) m { return withConds("VaultStaticSecret", c...) }
	synced := func(status string, gen any, msg string) m {
		c := cond("SecretSynced", status, "", msg)
		if gen != nil {
			c["observedGeneration"] = gen
		}
		return c
	}
	out[hp+"secrets.hashicorp.com_VaultStaticSecret"] = []tcase{
		{"no status", bare("VaultStaticSecret"), "Progressing", "waiting for the Vault Secrets Operator"},
		{"no conditions", m{"kind": "VaultStaticSecret", "metadata": m{"generation": float64(3)}, "status": m{}}, "Progressing", "waiting"},
		{"synced, current generation", vss(synced("True", float64(3), "Secret synced")), "Healthy", "Secret synced"},
		{"synced, no observedGeneration", vss(synced("True", nil, "Secret synced")), "Healthy", "Secret synced"},
		{"synced, old generation", vss(synced("True", float64(2), "Secret synced")), "Progressing", "waiting"},
		{"sync failed", vss(synced("False", float64(3), "permission denied")), "Degraded", "permission denied"},
		{"another condition only", vss(cond("Other", "True", "", "")), "Progressing", "waiting"},
	}
	return out
}
