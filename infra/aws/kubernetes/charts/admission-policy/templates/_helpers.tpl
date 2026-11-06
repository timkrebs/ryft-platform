{{- /*
Helpers of the admission-policy chart. CEL is written into YAML block scalars; values are
spliced in with toJson, which gives CEL string, list and map literals.
*/ -}}

{{- /* Fails the render on a value the chart cannot honour. */ -}}
{{- define "admission.validate" -}}
{{- $known := list "vso" "operators-cluster" "broker" "namespaces" "workloads-templates" "netpol" "auth-env" "traefik" "workloads-pods" "operator-reach" "argocd" "argocd-operation" "vault-trust" "namespaces-vault" "workloads-templates-vault" "workloads-pods-vault" "netpol-vault" -}}
{{- if hasKey .Values "vaultAddress" -}}{{- fail "vaultAddress is now vaultAddresses, a list (admission-values.yaml)" -}}{{- end -}}
{{- if not .Values.vaultAddresses -}}{{- fail "vaultAddresses is required (admission-values.yaml)" -}}{{- end -}}
{{- if not (kindIs "slice" .Values.vaultAddresses) -}}{{- fail "vaultAddresses must be a list" -}}{{- end -}}
{{- range .Values.vaultAddresses -}}
{{- if not (and (kindIs "string" .) (hasPrefix "https://" .) (not (hasSuffix "/" .))) -}}{{- fail "every vaultAddresses entry must be an https:// URL without a trailing slash" -}}{{- end -}}
{{- end -}}
{{- /* Namespace vault: a name of its own (the ryft-* and operator branches would shadow it), and
image patterns that are one literal reference pinned by digest: ^, letters, digits, / : _ - and
[.] for a dot, then @sha256:<64 hex>$. No alternation, group, repetition, escape or bare dot, so
^.*@sha256:...$ or a pattern without a full digest cannot pass (an empty pattern would match
every image; P2 leaves an empty one out instead). */ -}}
{{- $vns := .Values.vault.namespace -}}
{{- if or (not $vns) (hasPrefix "ryft-" $vns) (eq $vns .Values.operators.namespace) (eq $vns .Values.argocd.namespace) -}}{{- fail "vault.namespace must be set and must not be a ryft-* namespace, the operators' or Argo CD's" -}}{{- end -}}
{{- if not .Values.images.vault -}}{{- fail "images.vault is required (the Vault server's pinned image)" -}}{{- end -}}
{{- range $k := list "vault" "awsCli" -}}
{{- $re := index $.Values.images $k -}}
{{- if and $re (not (regexMatch "^\\^([A-Za-z0-9/:_-]|\\[[.]\\])+@sha256:[0-9a-f]{64}\\$$" $re)) -}}{{- fail (printf "images.%s must be one literal image reference pinned by digest: ^<registry/repository:tag, a dot written [.]>@sha256:<64 hex>$" $k) -}}{{- end -}}
{{- end -}}
{{- if not (and .Values.vault.issuer .Values.vault.serverCertificate .Values.vault.caCertificate .Values.vault.caCommonNamePrefix .Values.vault.certManagerUser .Values.vault.serverDNSNames .Values.vault.rootCASecret .Values.vault.bootstrapIssuer .Values.vault.clusterResourceNamespace) -}}{{- fail "vault.issuer, serverCertificate, serverDNSNames, caCertificate, caCommonNamePrefix, certManagerUser, rootCASecret, bootstrapIssuer and clusterResourceNamespace are required (P13)" -}}{{- end -}}
{{- if not .Values.vso.kvMountPrefix -}}{{- fail "vso.kvMountPrefix is required (P4: the in-cluster Vault's kv-<service> mounts)" -}}{{- end -}}
{{- /* Defence in depth only: a leaf with usages but no server auth has no EKU at all, and Go's
verifier accepts a leaf without EKU for any purpose. What keeps a vault-ca leaf from serving as
Vault is that it bears no SAN (P13 allows no dnsNames, ipAddresses, uris or emailAddresses, and Go
never matches a host name against the commonName). */ -}}
{{- if has "server auth" .Values.vault.caUsages -}}{{- fail "vault.caUsages must not hold server auth (P13: a vault-ca leaf is no server certificate)" -}}{{- end -}}
{{- if not .Values.vso.reservedServices -}}{{- fail "vso.reservedServices is required (P4: the kv-<name> mounts no namespace may read)" -}}{{- end -}}
{{- if not .Values.netpol.podIdentityAgentCIDR -}}{{- fail "netpol.podIdentityAgentCIDR is required (P3, namespace vault)" -}}{{- end -}}
{{- range $k, $v := .Values.actions -}}
{{- if not (has $k $known) -}}{{- fail (printf "actions.%s is not a binding of this chart" $k) -}}{{- end -}}
{{- if not (has $v (list "audit" "deny")) -}}{{- fail (printf "actions.%s must be audit or deny" $k) -}}{{- end -}}
{{- end -}}
{{- range $known -}}
{{- if not (hasKey $.Values.actions .) -}}{{- fail (printf "actions.%s is missing" .) -}}{{- end -}}
{{- end -}}
{{- range .Values.projects -}}
{{- if not (and .metadata .metadata.name .spec) -}}{{- fail "projects must be AppProject documents with metadata.name and spec" -}}{{- end -}}
{{- end -}}
{{- /* Hard guards: a deny that would refuse every request of its kind, or an object the cluster has today. */ -}}
{{- if and (eq (include "admission.mode" (dict "root" . "key" "argocd")) "deny") (not .Values.projects) -}}
{{- fail "actions.argocd is deny but projects is empty: P7 would refuse every AppProject write (T12-b wires projects)" -}}
{{- end -}}
{{- if and (eq (include "admission.mode" (dict "root" . "key" "netpol")) "deny") .Values.netpol.brokerEgressPublic -}}
{{- fail "actions.netpol is deny while netpol.brokerEgressPublic is true: ryft-rabbitmq's open 443 egress rule would be refused (T12-k narrows it first)" -}}
{{- end -}}
{{- end -}}

{{- /* The effective mode of a binding: break-glass forces audit. args: root, key */ -}}
{{- define "admission.mode" -}}
{{- if .root.Values.breakGlass -}}audit{{- else -}}{{- index .root.Values.actions .key -}}{{- end -}}
{{- end -}}

{{- /*
The binding. args: root, key, policy, id, step, and optionally suffix (of the name), rules (a
YAML list of resourceRules that narrows the policy's matchConstraints), and namespaces or
excludeNamespaces (a list: a namespaceSelector on kubernetes.io/metadata.name, In or NotIn). The
API server sets that label on every Namespace before admission; a Namespace object is matched on
its own labels, and a namespaceSelector never excludes another cluster-scoped object, so only
policies that judge namespaced objects and Namespaces are split this way. The vault bindings
(namespaces [vault]) and the general ones (excludeNamespaces [vault]) give one policy two
stagings: vault's can go to deny before the ryft-* rollout reaches its step.
The intended-action label is the committed action (audit or deny), whatever break-glass
does to validationActions; break-glass adds the label break-glass: "true", which T12-e
alerts on when Terraform's break-glass is left on. The drift alert and the Terraform check
compare the live validationActions with the effective ones (by key).
*/ -}}
{{- define "admission.binding" -}}
{{- $mode := include "admission.mode" . -}}
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: {{ .policy }}{{ if .suffix }}-{{ .suffix }}{{ end }}
  labels:
    app.kubernetes.io/part-of: admission-policy
    app.kubernetes.io/managed-by: {{ .root.Release.Service }}
    admission.ryft.dev/policy: {{ .id | quote }}
    admission.ryft.dev/step: {{ .step | quote }}
    admission.ryft.dev/key: {{ .key | quote }}
    admission.ryft.dev/intended-action: {{ index .root.Values.actions .key | quote }}
    {{- if .root.Values.breakGlass }}
    admission.ryft.dev/break-glass: "true"
    {{- end }}
spec:
  policyName: {{ .policy }}
  validationActions:
  {{- if eq $mode "deny" }}
    - Deny
    - Audit
  {{- else }}
    - Warn
    - Audit
  {{- end }}
  {{- if or .rules .namespaces .excludeNamespaces }}
  matchResources:
    {{- if or .namespaces .excludeNamespaces }}
    namespaceSelector:
      matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: {{ if .namespaces }}In{{ else }}NotIn{{ end }}
          values: {{ (.namespaces | default .excludeNamespaces) | toJson }}
    {{- end }}
    {{- if .rules }}
    resourceRules:
    {{- .rules | nindent 4 }}
    {{- end }}
  {{- end }}
{{- end -}}

{{- /* The policy's common head. args: root, name, id, summary */ -}}
{{- define "admission.policyHead" -}}
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: {{ .name }}
  labels:
    app.kubernetes.io/part-of: admission-policy
    app.kubernetes.io/managed-by: {{ .root.Release.Service }}
    admission.ryft.dev/policy: {{ .id | quote }}
  annotations:
    admission.ryft.dev/summary: {{ .summary | quote }}
{{- end -}}

{{- /* system:serviceaccount:<ns>:<name>. args: list ns name */ -}}
{{- define "admission.sa" -}}
{{- printf "system:serviceaccount:%s:%s" (index . 0) (index . 1) -}}
{{- end -}}

{{- define "admission.user.argoController" -}}{{- include "admission.sa" (list .Values.argocd.namespace .Values.argocd.controllerServiceAccount) -}}{{- end -}}
{{- define "admission.user.argoServer" -}}{{- include "admission.sa" (list .Values.argocd.namespace .Values.argocd.serverServiceAccount) -}}{{- end -}}
{{- define "admission.user.clusterOperator" -}}{{- include "admission.sa" (list .Values.operators.namespace .Values.operators.clusterOperatorServiceAccount) -}}{{- end -}}
{{- define "admission.user.topologyOperator" -}}{{- include "admission.sa" (list .Values.operators.namespace .Values.operators.topologyOperatorServiceAccount) -}}{{- end -}}

{{- /*
A CEL literal of any value. The CEL checker types a literal strictly (a map of lists and
strings is an error), so every map value and list element goes through dyn().
*/ -}}
{{- define "admission.cel.literal" -}}
{{- if kindIs "map" . -}}
{{- $parts := list -}}
{{- range $k, $v := . -}}
{{- $parts = append $parts (printf "%s: dyn(%s)" ($k | toJson) (include "admission.cel.literal" $v)) -}}
{{- end -}}
{{- printf "{%s}" (join ", " $parts) -}}
{{- else if kindIs "slice" . -}}
{{- $parts := list -}}
{{- range $v := . -}}
{{- $parts = append $parts (printf "dyn(%s)" (include "admission.cel.literal" $v)) -}}
{{- end -}}
{{- printf "[%s]" (join ", " $parts) -}}
{{- else -}}
{{- . | toJson -}}
{{- end -}}
{{- end -}}

{{- /* CEL list literal of both Argo CD identities */ -}}
{{- define "admission.cel.argoUsers" -}}
{{- list (include "admission.user.argoController" .) (include "admission.user.argoServer" .) | toJson -}}
{{- end -}}

{{- /* CEL: c.image matches one of the patterns namespace vault may run (constant patterns, one
matches() each; an empty one is left out, because an empty pattern matches anything). */ -}}
{{- define "admission.cel.vaultImages" -}}
{{- $out := list -}}
{{- range $k := list "vault" "awsCli" -}}{{- with index $.Values.images $k -}}{{- $out = append $out (printf "c.image.matches(%s)" (. | toJson)) -}}{{- end -}}{{- end -}}
{{- join " || " $out -}}
{{- end -}}

{{- /* CEL list literal of both operator identities */ -}}
{{- define "admission.cel.operatorUsers" -}}
{{- list (include "admission.user.clusterOperator" .) (include "admission.user.topologyOperator" .) | toJson -}}
{{- end -}}

{{- /*
A matchCondition for every policy that judges objects: an UPDATE of an object that is
being deleted and changes nothing but finalizers and status (a finalizer removal, VSO's for
example) is not judged. object[k] == oldObject[k] compares whole values, so a changed
spec, data, rules or webhooks is judged as usual, and so is a changed label, annotation or
owner reference (those carry policy: a PSA label, an aggregate-to label, secret-type).
*/ -}}
{{- define "admission.cond.finalizerRemoval" -}}
- name: not-a-finalizer-removal
  expression: |-
    !(request.operation == 'UPDATE' && has(oldObject.metadata.deletionTimestamp) &&
      dyn(object).all(k, k == 'metadata' || k == 'status' || (k in dyn(oldObject) && dyn(object)[k] == dyn(oldObject)[k])) &&
      dyn(oldObject).all(k, k == 'metadata' || k == 'status' || k in dyn(object)) &&
      (has(dyn(object).metadata.labels) ? dyn(object).metadata.labels : {}) == (has(dyn(oldObject).metadata.labels) ? dyn(oldObject).metadata.labels : {}) &&
      (has(dyn(object).metadata.annotations) ? dyn(object).metadata.annotations : {}) == (has(dyn(oldObject).metadata.annotations) ? dyn(oldObject).metadata.annotations : {}) &&
      (has(dyn(object).metadata.ownerReferences) ? dyn(object).metadata.ownerReferences : []) == (has(dyn(oldObject).metadata.ownerReferences) ? dyn(oldObject).metadata.ownerReferences : []))
{{- end -}}

{{- /*
Variables for the policies that look into pod specs (P2, P5): podSpec of a Pod, a CronJob
or any other pod template owner, and the containers and volumes with absent lists as [].
*/ -}}
{{- define "admission.podVariables" -}}
- name: reqKind
  expression: request.kind.kind
- name: ns
  expression: request.namespace
- name: o
  expression: dyn(object)
- name: podSpec
  expression: |-
    variables.reqKind == 'Pod' ? variables.o.spec :
    variables.reqKind == 'CronJob' ? variables.o.spec.jobTemplate.spec.template.spec :
    variables.o.spec.template.spec
- name: containers
  expression: |-
    (has(variables.podSpec.containers) ? variables.podSpec.containers : []) +
    (has(variables.podSpec.initContainers) ? variables.podSpec.initContainers : [])
- name: volumes
  expression: |-
    has(variables.podSpec.volumes) ? variables.podSpec.volumes : []
{{- end -}}

{{- /* resourceRules of the pod template owners (everything P2 and P5 judge but Pod). args: ops (list) */ -}}
{{- define "admission.rules.templates" -}}
- apiGroups: ["apps"]
  apiVersions: ["*"]
  operations: {{ .ops | toJson }}
  resources: ["deployments", "statefulsets", "daemonsets", "replicasets"]
- apiGroups: ["batch"]
  apiVersions: ["*"]
  operations: {{ .ops | toJson }}
  resources: ["jobs", "cronjobs"]
- apiGroups: [""]
  apiVersions: ["*"]
  operations: {{ .ops | toJson }}
  resources: ["replicationcontrollers"]
{{- end -}}

{{- /* Pods, and the ephemeralcontainers subresource (an UPDATE only: it adds containers to a running Pod). */ -}}
{{- define "admission.rules.pods" -}}
- apiGroups: [""]
  apiVersions: ["*"]
  operations: {{ .ops | toJson }}
  resources: ["pods"]
- apiGroups: [""]
  apiVersions: ["*"]
  operations: ["UPDATE"]
  resources: ["pods/ephemeralcontainers"]
{{- end -}}
