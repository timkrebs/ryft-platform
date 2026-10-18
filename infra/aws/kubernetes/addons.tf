# AWS Load Balancer Controller: turns the Traefik Service into an NLB. Region
# and VPC are passed in because pods cannot reach IMDS (hop limit 1).
resource "helm_release" "aws_load_balancer_controller" {
  name       = "aws-load-balancer-controller"
  namespace  = "kube-system"
  repository = local.charts.aws_load_balancer_controller.repository
  chart      = local.charts.aws_load_balancer_controller.chart
  version    = local.charts.aws_load_balancer_controller.version

  values = [yamlencode({
    clusterName = var.cluster_name
    region      = var.region
    vpcId       = local.vpc_id
    serviceAccount = {
      name = "aws-load-balancer-controller"
    }
    # Only Services that ask for it (aws-load-balancer-type: external) get an
    # NLB; don't take over every LoadBalancer Service by default.
    enableServiceMutatorWebhook = false
    resources = {
      requests = { cpu = "50m", memory = "64Mi" }
      limits   = { memory = "256Mi" }
    }
  })]

  depends_on = [module.aws_load_balancer_controller_pod_identity]
}

# Traefik: the ingress controller. HTTPS only — the plain-HTTP entrypoint is
# not exposed, so a client can never send its bearer token in cleartext. It
# runs in kube-system because deploy/k8s/base/{api,ingest}/networkpolicy.yaml admit Traefik
# pods there.
resource "helm_release" "traefik" {
  name       = "traefik"
  namespace  = "kube-system"
  repository = local.charts.traefik.repository
  chart      = local.charts.traefik.chart
  version    = local.charts.traefik.version

  values = [yamlencode({
    deployment = {
      replicas = 2
    }
    podDisruptionBudget = {
      enabled      = true
      minAvailable = 1
    }
    resources = {
      requests = { cpu = "50m", memory = "64Mi" }
      limits   = { memory = "256Mi" }
    }
    ingressClass = {
      enabled        = true
      isDefaultClass = true
      name           = "traefik"
    }
    ports = {
      web = {
        expose = { default = false }
      }
      websecure = {
        expose = { default = true }
      }
    }
    service = {
      annotations = {
        "service.beta.kubernetes.io/aws-load-balancer-type"            = "external"
        "service.beta.kubernetes.io/aws-load-balancer-nlb-target-type" = "ip"
        "service.beta.kubernetes.io/aws-load-balancer-scheme"          = "internet-facing"
        "service.beta.kubernetes.io/aws-load-balancer-attributes"      = "load_balancing.cross_zone.enabled=true"
      }
    }
    # Publish the NLB hostname in Ingress status so ExternalDNS can point
    # api.<domain> at it.
    providers = {
      kubernetesIngress = {
        publishedService = { enabled = true }
      }
    }
  })]

  # The controller must exist to build the NLB, and on destroy it must still be
  # running to delete it (otherwise the NLB is orphaned and blocks VPC deletion).
  depends_on = [helm_release.aws_load_balancer_controller]
}

# cert-manager: Let's Encrypt certificates for the Ingress hosts, solved via
# DNS-01 in Route 53 (no solver pods, works before anything is public).
resource "helm_release" "cert_manager" {
  name             = "cert-manager"
  namespace        = "cert-manager"
  create_namespace = true
  repository       = local.charts.cert_manager.repository
  chart            = local.charts.cert_manager.chart
  version          = local.charts.cert_manager.version

  values = [yamlencode({
    crds = {
      enabled = true
    }
    serviceAccount = {
      name = "cert-manager"
    }
    # Check DNS-01 propagation against public resolvers, not the VPC resolver: after
    # a delegation change it can cache the old nameservers for up to a day (seen when
    # ryftapp.de moved from GoDaddy to Route 53), and Let's Encrypt validates from the
    # internet anyway.
    extraArgs = [
      "--dns01-recursive-nameservers-only",
      "--dns01-recursive-nameservers=8.8.8.8:53,9.9.9.9:53",
    ]
    resources = {
      requests = { cpu = "10m", memory = "64Mi" }
      limits   = { memory = "256Mi" }
    }
  })]

  depends_on = [module.cert_manager_pod_identity]
}

# ClusterIssuers letsencrypt-staging and letsencrypt-prod, from a local chart
# because kubernetes_manifest would need the cert-manager CRDs at plan time.
resource "helm_release" "cluster_issuers" {
  name      = "cluster-issuers"
  namespace = "cert-manager"
  chart     = "${path.module}/charts/cluster-issuers"

  values = [yamlencode({
    email        = var.acme_email
    region       = var.region
    hostedZoneID = data.aws_route53_zone.this.zone_id
    dnsZone      = var.domain
  })]

  depends_on = [helm_release.cert_manager]
}

# ExternalDNS: keeps Route 53 records in sync with Ingress hosts (api.<domain>
# → the NLB). policy=sync with a TXT ownership registry: it only ever deletes
# records it created itself.
resource "helm_release" "external_dns" {
  name             = "external-dns"
  namespace        = "external-dns"
  create_namespace = true
  repository       = local.charts.external_dns.repository
  chart            = local.charts.external_dns.chart
  version          = local.charts.external_dns.version

  values = [yamlencode({
    provider = {
      name = "aws"
    }
    domainFilters = [var.domain]
    policy        = "sync"
    registry      = "txt"
    txtOwnerId    = var.cluster_name
    sources       = ["ingress"]
    extraArgs     = ["--aws-zone-type=public"]
    env = [{
      name  = "AWS_DEFAULT_REGION"
      value = var.region
    }]
    serviceAccount = {
      name = "external-dns"
    }
    resources = {
      requests = { cpu = "10m", memory = "64Mi" }
      limits   = { memory = "128Mi" }
    }
  })]

  depends_on = [module.external_dns_pod_identity]
}

# Vault Secrets Operator: syncs each RYFT namespace's Secret ryft-secrets from that
# service's own Vault namespace. infra/vault generates the VaultConnection, VaultAuth
# and VaultStaticSecret per namespace (deploy/k8s/overlays/vault-namespaces). There is
# no default connection or auth method: a namespace without its own VaultAuth gets
# nothing, so no namespace can borrow another's Vault identity.
resource "helm_release" "vault_secrets_operator" {
  name             = "vault-secrets-operator"
  namespace        = "vault-secrets-operator-system"
  create_namespace = true
  repository       = local.charts.vault_secrets_operator.repository
  chart            = local.charts.vault_secrets_operator.chart
  version          = local.charts.vault_secrets_operator.version

  values = [yamlencode({
    defaultVaultConnection = { enabled = false }
    defaultAuthMethod      = { enabled = false }
    controller = {
      manager = {
        # No `_raw` key (the whole secret as JSON) in any synced Secret.
        globalTransformationOptions = { excludeRaw = true }
        resources = {
          requests = { cpu = "10m", memory = "64Mi" }
          limits   = { memory = "256Mi" }
        }
      }
    }
  })]
}

