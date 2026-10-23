{{- /*
Helpers of the admission-policy chart. CEL is written into YAML block scalars; values are
spliced in with toJson, which gives CEL string, list and map literals.
*/ -}}

{{- /* Fails the render on a value the chart cannot honour. */ -}}
{{- define "admission.validate" -}}
{{- $known := list "vso" "operators-cluster" "broker" "namespaces" "workloads-templates" "netpol" "auth-env" "traefik" "workloads-pods" "operator-reach" "argocd" "argocd-operation" -}}
{{- if not .Values.vaultAddress -}}{{- fail "vaultAddress is required (admission-values.yaml)" -}}{{- end -}}
{{- if not (hasPrefix "https://" .Values.vaultAddress) -}}{{- fail "vaultAddress must be an https:// URL" -}}{{- end -}}
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
{{- end -}}

{{- /* The effective mode of a binding: break-glass forces audit. args: root, key */ -}}
{{- define "admission.mode" -}}
{{- if .root.Values.breakGlass -}}audit{{- else -}}{{- index .root.Values.actions .key -}}{{- end -}}
{{- end -}}

{{- /*
The binding. args: root, key, policy, id, step, suffix and rules (both optional: a name
suffix, and a YAML list of resourceRules that narrows the policy's matchConstraints).
The intended-action label is what Terraform asks for, break-glass included; the drift
alert and the Terraform check compare the live validationActions with it, and key.
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
    admission.ryft.dev/intended-action: {{ $mode | quote }}
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
  {{- if .rules }}
  matchResources:
    resourceRules:
    {{- .rules | nindent 4 }}
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

{{- /* CEL list literal of both operator identities */ -}}
{{- define "admission.cel.operatorUsers" -}}
{{- list (include "admission.user.clusterOperator" .) (include "admission.user.topologyOperator" .) | toJson -}}
{{- end -}}

{{- /*
A matchCondition for every policy that judges objects: an UPDATE of an object that is
being deleted and changes nothing but metadata and status (a finalizer removal, VSO's for
example) is not judged. object[k] == oldObject[k] compares whole values, so a changed
spec, data, rules or webhooks is judged as usual.
*/ -}}
{{- define "admission.cond.finalizerRemoval" -}}
- name: not-a-finalizer-removal
  expression: |-
    !(request.operation == 'UPDATE' && has(oldObject.metadata.deletionTimestamp) &&
      dyn(object).all(k, k == 'metadata' || k == 'status' || (k in dyn(oldObject) && dyn(object)[k] == dyn(oldObject)[k])) &&
      dyn(oldObject).all(k, k == 'metadata' || k == 'status' || k in dyn(object)))
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
{{- end -}}

{{- define "admission.rules.pods" -}}
- apiGroups: [""]
  apiVersions: ["*"]
  operations: {{ .ops | toJson }}
  resources: ["pods"]
{{- end -}}
