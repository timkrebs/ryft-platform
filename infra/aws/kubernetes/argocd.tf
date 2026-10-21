# Argo CD: deploys the RYFT workloads on this cluster from git (deploy/argocd, D94).
# It manages workloads only; every add-on in this root stays a Terraform Helm release,
# so no object has two owners. Argo CD itself is an add-on, installed here.
#
# Non-HA: one replica per component, Redis without persistence (a cache Argo CD
# rebuilds). On three nodes that is enough; the HA option is redis-ha plus 2+
# replicas of server and repo-server and a sharded controller (chart docs).
# The UI and API are public at https://argocd.<domain> (D103): Traefik ends TLS with a
# Let's Encrypt certificate, limits requests per client address and adds HSTS; the
# login is Argo CD's own (it slows down repeated failures). argocd-server itself speaks
# plain HTTP behind Traefik, like the RYFT services. Without the Ingress:
#   kubectl -n argocd port-forward svc/argocd-server 8080:80
# The local `admin` account stays; its password is generated here and is read with
#   terraform -chdir=infra/aws/kubernetes output -raw argocd_admin_password
# so no argocd-initial-admin-secret is ever created. The repository is public, so
# Argo CD reads it over HTTPS without credentials.

resource "kubernetes_namespace_v1" "argocd" {
  metadata {
    name = "argocd"
    labels = {
      # Every Argo CD container runs as non-root, read-only, without capabilities.
      "pod-security.kubernetes.io/enforce" = "restricted"
      "pod-security.kubernetes.io/warn"    = "restricted"
      "pod-security.kubernetes.io/audit"   = "restricted"
    }
  }
}

# Rotate with: terraform apply -replace=random_password.argocd_admin
# (and change argocd_admin_password_mtime, which logs existing sessions out).
resource "random_password" "argocd_admin" {
  length  = 32
  special = false
}

locals {
  argocd_admin_password_mtime = "2026-10-21T00:00:00Z"
  argocd_host                 = "argocd.${var.domain}"

  # RabbitMQ (D97): the cluster is healthy when all its replicas are ready and the
  # operator reconciled it; a topology object (vhost, queue, user, ...) when the
  # Topology Operator reports it Ready. The broker's sync waves wait on these. One key
  # per kind: a ConfigMap key cannot hold the wildcard rabbitmq.com_*.
  argocd_rabbitmq_health = { for kind in ["RabbitmqCluster", "Vhost", "Exchange", "Queue", "Binding", "Policy", "User", "Permission", "TopicPermission"] :
    "resource.customizations.health.rabbitmq.com_${kind}" => <<-LUA
      hs = { status = "Progressing", message = "waiting for the RabbitMQ operators" }
      if obj.status == nil or obj.status.conditions == nil then
        return hs
      end
      local want = { Ready = true }
      if obj.kind == "RabbitmqCluster" then
        want = { AllReplicasReady = true, ReconcileSuccess = true }
      end
      local ok = 0
      local total = 0
      for _ in pairs(want) do total = total + 1 end
      for _, c in ipairs(obj.status.conditions) do
        if want[c.type] then
          if c.status == "True" then
            ok = ok + 1
          elseif c.status == "False" and c.reason ~= nil and string.find(c.reason, "Fail") then
            hs.status = "Degraded"
            hs.message = c.type .. ": " .. (c.message or c.reason)
            return hs
          end
        end
      end
      if ok == total then
        hs.status = "Healthy"
        hs.message = ""
      end
      return hs
    LUA
  }

  # Scraped by the Grafana Cloud collectors through pod annotations, like the RYFT pods.
  argocd_scrape = { for component, port in { controller = 8082, server = 8083, repoServer = 8084 } : component => {
    "prometheus.io/scrape" = "true"
    "prometheus.io/port"   = tostring(port)
    "prometheus.io/path"   = "/metrics"
  } }

  argocd_values = {
    global = {
      # The public host; also argocd-cm's url (the links Argo CD builds).
      domain  = local.argocd_host
      image   = { tag = "v3.5.6" } # chart 10.10.5 ships v3.5.5; v3.5.6 is the newest patch
      logging = { format = "json" }
      # The chart's own policies (default on): controller, repo-server and Redis
      # admit only the components that talk to them. extraObjects adds the scrape.
      networkPolicy = { create = true }
    }

    # No SSO (dex) and no Argo CD notifications: alerts come from Grafana Cloud.
    dex           = { enabled = false }
    notifications = { enabled = false }

    configs = {
      # TLS ends at Traefik; argocd-server serves HTTP on its service's port 80.
      params = { "server.insecure" = true }
      cm = merge(local.argocd_rabbitmq_health, {
        "admin.enabled" = true
        # Restores the health of Application resources (removed in Argo CD 1.8), so
        # the app-of-apps waits in each sync wave until the previous wave's
        # applications are healthy: Vault secrets, then the broker, then the services.
        "resource.customizations.health.argoproj.io_Application" = <<-LUA
          hs = {}
          hs.status = "Progressing"
          hs.message = ""
          if obj.status ~= nil and obj.status.health ~= nil then
            hs.status = obj.status.health.status
            if obj.status.health.message ~= nil then
              hs.message = obj.status.health.message
            end
          end
          return hs
        LUA
        # An autoscaler is healthy unless it cannot scale. Argo CD's own check calls
        # "no metrics yet" Degraded, which the api's and ingest's HPAs report for about a
        # minute after every rollout (the metrics server has no samples for the new
        # pods), and a Degraded object fails the sync (2026-10-21, D105).
        "resource.customizations.health.autoscaling_HorizontalPodAutoscaler" = <<-LUA
          hs = { status = "Healthy", message = "" }
          if obj.status == nil or obj.status.conditions == nil then
            return hs
          end
          for _, c in ipairs(obj.status.conditions) do
            if c.type == "AbleToScale" and c.status == "False" then
              hs.status = "Degraded"
              hs.message = c.message or c.reason
              return hs
            end
            if c.type == "ScalingActive" and c.status == "False" then
              if c.reason == "FailedGetResourceMetric" or c.reason == "FailedComputeMetricsReplicas" or c.reason == "ScalingDisabled" then
                hs.message = (c.reason or "") .. ": " .. (c.message or "")
              else
                hs.status = "Degraded"
                hs.message = c.message or c.reason
                return hs
              end
            end
          end
          return hs
        LUA
        # A namespace's Secret from Vault is ready when the operator says it synced
        # (wave 0 must be healthy before the broker and the services start).
        "resource.customizations.health.secrets.hashicorp.com_VaultStaticSecret" = <<-LUA
          hs = { status = "Progressing", message = "waiting for the Vault Secrets Operator" }
          if obj.status ~= nil and obj.status.conditions ~= nil then
            for _, c in ipairs(obj.status.conditions) do
              if c.type == "SecretSynced" then
                if c.status == "True" and (c.observedGeneration == nil or c.observedGeneration == obj.metadata.generation) then
                  hs.status = "Healthy"
                  hs.message = c.message
                elseif c.status == "False" then
                  hs.status = "Degraded"
                  hs.message = c.message
                end
              end
            end
          end
          return hs
        LUA
      })
      secret = {
        argocdServerAdminPassword      = random_password.argocd_admin.bcrypt_hash
        argocdServerAdminPasswordMtime = local.argocd_admin_password_mtime
      }
    }

    controller = {
      replicas       = 1
      podAnnotations = local.argocd_scrape.controller
      resources = {
        requests = { cpu = "50m", memory = "256Mi" }
        limits   = { memory = "768Mi" }
      }
    }
    server = {
      replicas       = 1
      podAnnotations = local.argocd_scrape.server
      resources = {
        requests = { cpu = "10m", memory = "64Mi" }
        limits   = { memory = "256Mi" }
      }
      # https://argocd.<domain>: the certificate (Secret argocd-server-tls) from
      # cert-manager, the DNS record from ExternalDNS, as for api.<domain>. The CLI
      # goes through it with --grpc-web (Traefik does not pass native gRPC here).
      ingress = {
        enabled          = true
        ingressClassName = "traefik"
        tls              = true
        annotations = {
          "cert-manager.io/cluster-issuer"                   = "letsencrypt-prod"
          "traefik.ingress.kubernetes.io/router.entrypoints" = "websecure"
          "traefik.ingress.kubernetes.io/router.tls"         = "true"
          "traefik.ingress.kubernetes.io/router.middlewares" = "argocd-ratelimit@kubernetescrd,argocd-hsts@kubernetescrd"
        }
      }
    }
    repoServer = {
      replicas       = 1
      podAnnotations = local.argocd_scrape.repoServer
      resources = {
        requests = { cpu = "20m", memory = "128Mi" }
        limits   = { memory = "512Mi" }
      }
    }
    applicationSet = {
      replicas = 1
      resources = {
        requests = { cpu = "10m", memory = "32Mi" }
        limits   = { memory = "128Mi" }
      }
    }
    redis = {
      resources = {
        requests = { cpu = "10m", memory = "32Mi" }
        limits   = { memory = "128Mi" }
      }
    }

    # The collectors run on the nodes' network, so their scrapes come from node
    # addresses, which the chart's policies (pods only) do not admit. Only the
    # metrics ports, from inside the VPC (as deploy/k8s/overlays/eks/metrics-from-nodes.yaml).
    # Then the Ingress's Traefik middlewares: a per-IP limit (the UI polls and streams,
    # so more than the API's 20/s) and HSTS (argocd-server sends none without TLS).
    extraObjects = [{
      apiVersion = "networking.k8s.io/v1"
      kind       = "NetworkPolicy"
      metadata   = { name = "allow-metrics-from-nodes", namespace = "argocd" }
      spec = {
        podSelector = {
          matchExpressions = [{
            key      = "app.kubernetes.io/name"
            operator = "In"
            values   = ["argocd-application-controller", "argocd-server", "argocd-repo-server"]
          }]
        }
        policyTypes = ["Ingress"]
        ingress = [{
          from  = [{ ipBlock = { cidr = var.vpc_cidr } }]
          ports = [for p in [8082, 8083, 8084] : { protocol = "TCP", port = p }]
        }]
      }
      }, {
      apiVersion = "traefik.io/v1alpha1"
      kind       = "Middleware"
      metadata   = { name = "ratelimit", namespace = "argocd" }
      spec = {
        rateLimit = { average = 50, period = "1s", burst = 200, sourceCriterion = { ipStrategy = { depth = 0 } } }
      }
      }, {
      apiVersion = "traefik.io/v1alpha1"
      kind       = "Middleware"
      metadata   = { name = "hsts", namespace = "argocd" }
      spec = {
        headers = { stsSeconds = 31536000, stsIncludeSubdomains = false }
      }
    }]
  }
}

resource "helm_release" "argocd" {
  name       = "argocd"
  namespace  = kubernetes_namespace_v1.argocd.metadata[0].name
  repository = local.charts.argocd.repository
  chart      = local.charts.argocd.chart
  version    = local.charts.argocd.version

  values = [yamlencode(local.argocd_values)]

  # The middlewares are Traefik's CRDs; the Ingress's certificate is cert-manager's.
  depends_on = [helm_release.traefik, helm_release.cert_manager]
}
