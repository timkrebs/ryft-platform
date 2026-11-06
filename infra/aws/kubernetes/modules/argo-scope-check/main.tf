# Where P12 lets Argo CD's controller write (argoScope of the admission-policy chart), judged before a plan
# (D206, security review S3). Pure: no provider, no resource. admission.tf passes the committed scope and the
# namespaces of its root; the precondition of helm_release.admission_policy and check
# "admission_argo_scope_never" read the outputs, and tests/admission.tftest.hcl runs this module with scopes
# the committed file does not have.

variable "namespaces" {
  description = "argoScope.namespaces: the namespaces the controller writes namespaced objects in."
  type        = list(string)
}

variable "namespace_objects" {
  description = "argoScope.namespaceObjects: the Namespace objects the controller may create and update."
  type        = list(string)
}

variable "never" {
  description = "Namespaces Argo CD never writes in: vault's, cert-manager's, argocd, kube-system, every add-on's, the chart's neverNamespaces."
  type        = list(string)
}

variable "addon_namespaces" {
  description = "The namespaces the Helm releases of infra/aws/kubernetes install into."
  type        = list(string)
}

variable "never_chart" {
  description = "The chart's own never-list (its fixed part and argoScope.neverNamespaces): what a render outside Terraform refuses."
  type        = list(string)
}

variable "operators_namespace" {
  description = "operators.namespace (rabbitmq-system): the one namespace outside ryft-* the scope may hold."
  type        = string
}

output "never_listed" {
  description = "Namespaces of the scope (namespaces or namespaceObjects) that are on the never-list; empty when clean."
  value = sort(distinct([
    for n in concat(var.namespaces, var.namespace_objects) : n if contains(var.never, n)
  ]))
}

output "off_shape" {
  description = "Entries of argoScope.namespaces that are neither ryft-* nor the operators' namespace; empty when clean."
  value       = sort([for n in var.namespaces : n if !(startswith(n, "ryft-") || n == var.operators_namespace)])
}

output "never_uncovered" {
  description = "Add-on namespaces the chart's never-list misses (so a render outside Terraform would admit them); empty when clean."
  value       = sort([for n in var.addon_namespaces : n if !contains(var.never_chart, n)])
}
