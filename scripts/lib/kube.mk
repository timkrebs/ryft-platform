# Included by the Makefile: the cluster every target that talks to one uses is named,
# KUBE_CONTEXT=<context> as `kubectl config get-contexts -o name` lists it; kubectl's
# current context is never used (scripts/lib/kube.sh; on 2026-10-23 it was production
# while a script was being tried). Three ways around a guard are closed:
#   - every cluster call runs the guard in its own shell line, right before it, so a
#     failed guard prerequisite does not let `make -i` reach the call;
#   - the variables are overrides, so `make -e` or a command line cannot swap in plain
#     kubectl or helm;
#   - the context reaches a recipe only through the environment ("$$KUBE_CONTEXT"), never
#     pasted into it, so no value can break out of its quoting (the guard refuses odd
#     names anyway).
# KUBECTL_LOCAL and the local guard also refuse any context that reaches EKS.
export KUBE_CONTEXT
override KUBE_MISSING := KUBE_CONTEXT is not set: name the cluster, e.g. make $(MAKECMDGOALS) KUBE_CONTEXT=<context> (kubectl config get-contexts -o name); kubectl's current context is never used (scripts/lib/kube.sh)
override KUBE_REQUIRED = $(if $(KUBE_CONTEXT),,$(error $(KUBE_MISSING)))
override KUBE_GUARD = $(KUBE_REQUIRED). ./scripts/lib/kube.sh && KUBE_QUIET=1 kube_context "$$KUBE_CONTEXT"
override KUBE_GUARD_LOCAL = $(KUBE_REQUIRED). ./scripts/lib/kube.sh && KUBE_QUIET=1 kube_context --local "$$KUBE_CONTEXT"
override KUBECTL = $(KUBE_GUARD) && kubectl --context "$$KUBE_CONTEXT"
override KUBECTL_LOCAL = $(KUBE_GUARD_LOCAL) && kubectl --context "$$KUBE_CONTEXT"
override HELM = $(KUBE_GUARD) && helm --kube-context "$$KUBE_CONTEXT"

# The guard alone, first, so a target says which cluster it uses before it starts.
k8s-context:
	@$(KUBE_REQUIRED). ./scripts/lib/kube.sh && kube_context "$$KUBE_CONTEXT"

k8s-context-local:
	@$(KUBE_REQUIRED). ./scripts/lib/kube.sh && kube_context --local "$$KUBE_CONTEXT"
