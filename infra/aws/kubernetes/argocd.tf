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
  #
  # No string library (D121): Argo CD's Lua VM opens only package, base and table
  # (util/lua/lua.go, runLua), plus a safe os that exists only through require("os");
  # string.find and s:find fail with "attempt to index a non-table object(nil)" and the
  # app goes to ComparisonError. So no pattern matching, and no useOpenLibs.<group_kind>
  # (it would open the whole standard library for each kind to save an equality test).
  #
  # Degraded: ReconcileSuccess=False on a RabbitmqCluster, Ready=False on a topology
  # object, whatever the reason (Argo CD's own built-in check does the same for the
  # cluster). Cluster Operator v2.23.1 sets ReconcileSuccess=False only on a failed
  # reconcile (internal/controller/rabbitmqcluster_controller.go: 211, 252, 268, 271,
  # 297, 306; reconcile_tls.go: 24, 30; reconcile_scale_down.go: 30; reconcile_scale_zero.go:
  # 50, 60), mostly permanent problems. Messaging Topology Operator v1.20.4 writes
  # Ready=False with the one reason FailedCreateOrUpdate for every failed declare
  # (api/v1beta1/conditions.go: NotReady, 44), transient ones included; the sync's retry
  # and the ArgoAppDegraded delay absorb those. AllReplicasReady=False (NotAllPodsReady
  # during a node roll, ScaledToZero, MissingStatefulSet; internal/status/all_replicas_ready.go)
  # is never a failure: Progressing, with the condition's message.
  #
  # A spec change leaves the old conditions True until the operator has reconciled the new
  # generation: while status.observedGeneration is behind metadata.generation the object is
  # Progressing (both operators set observedGeneration on success only).
  argocd_rabbitmq_health = { for kind in ["RabbitmqCluster", "Vhost", "Exchange", "Queue", "Binding", "Policy", "User", "Permission", "TopicPermission"] :
    "resource.customizations.health.rabbitmq.com_${kind}" => <<-LUA
      hs = { status = "Progressing", message = "waiting for the RabbitMQ operators" }
      if obj.status == nil or obj.status.conditions == nil then
        return hs
      end
      local want = { Ready = true }
      local degrades = { Ready = true }
      if obj.kind == "RabbitmqCluster" then
        want = { AllReplicasReady = true, ReconcileSuccess = true }
        degrades = { ReconcileSuccess = true }
      end
      local total = 0
      for _ in pairs(want) do total = total + 1 end
      local seen = {}
      local ok = 0
      local pending = nil
      for _, c in ipairs(obj.status.conditions) do
        if want[c.type] then
          if c.status == "True" then
            if not seen[c.type] then
              seen[c.type] = true
              ok = ok + 1
            end
          else
            local detail = c.message
            if detail == nil or detail == "" then detail = c.reason end
            if detail == nil or detail == "" then detail = "not ready" end
            local msg = c.type .. ": " .. detail
            if c.status == "False" and degrades[c.type] then
              hs.status = "Degraded"
              hs.message = msg
              return hs
            end
            pending = pending or msg
          end
        end
      end
      local gen = nil
      if obj.metadata ~= nil then gen = obj.metadata.generation end
      if gen ~= nil and (obj.status.observedGeneration == nil or obj.status.observedGeneration < gen) then
        hs.message = "waiting for the operator to reconcile generation " .. gen
        return hs
      end
      if ok == total then
        hs.status = "Healthy"
        hs.message = ""
      elseif pending ~= nil then
        hs.message = pending
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

  # --- RBAC (T12-f, D120): Argo CD cannot switch off or bypass the admission layer. ---
  # The chart's defaults give the application controller `*` on everything and the
  # server delete/get/patch on everything (so secrets, and the ValidatingAdmissionPolicy
  # and -Binding objects). Both ClusterRoles are replaced by the lists below, derived
  # from `kubectl kustomize` of every path in deploy/argocd/apps and the kinds in
  # deploy/argocd/project.yaml. docs/OPERATIONS.md has the reason for each rule. A kind
  # added to a path under deploy/argocd/apps needs its rule here first.
  argocd_crud  = ["get", "list", "watch", "create", "update", "patch", "delete"]
  argocd_apply = ["get", "list", "watch", "create", "update", "patch"] # no delete

  # The operators' roles, as rendered from deploy/rabbitmq/operators.
  argocd_operator_cluster_roles = [
    "rabbitmq-cluster-operator-role",
    "rabbitmq-cluster-service-binding-role",
    "messaging-topology-manager-role",
    "messaging-topology-metrics-auth-role",
    "messaging-topology-metrics-reader",
  ]
  argocd_operator_bound_cluster_roles = [ # roleRef of the three ClusterRoleBindings
    "rabbitmq-cluster-operator-role",
    "messaging-topology-manager-role",
    "messaging-topology-metrics-auth-role",
  ]
  # Roles: the two leader-election Roles in rabbitmq-system, and (T12-g) the operators' main
  # rules as namespaced Roles in ryft-rabbitmq. The ClusterRole names above stay until T12-g
  # has landed; remove them (and their escalate/bind rules) then.
  argocd_operator_roles = [
    "rabbitmq-cluster-leader-election-role",
    "messaging-topology-leader-election-role",
    "rabbitmq-cluster-operator-role",
    "messaging-topology-manager-role",
  ]
  argocd_operator_mutating_webhooks = [
    "cluster-operator-mutating-webhook-configuration",
    "messaging-topology-mutating-webhook-configuration",
  ]
  argocd_operator_validating_webhooks = [
    "cluster-operator-validating-webhook-configuration",
    "messaging-topology-validating-webhook-configuration",
  ]
  argocd_rabbitmq_kinds = ["rabbitmqclusters", "vhosts", "exchanges", "queues", "bindings", "policies", "users", "permissions", "topicpermissions"]

  # Kinds the workloads' paths render; the controller applies, updates and prunes them.
  argocd_controller_managed = [
    { apiGroups = [""], resources = ["configmaps", "services", "serviceaccounts"], verbs = local.argocd_crud },
    { apiGroups = ["apps"], resources = ["deployments"], verbs = local.argocd_crud },
    { apiGroups = ["autoscaling"], resources = ["horizontalpodautoscalers"], verbs = local.argocd_crud },
    { apiGroups = ["batch"], resources = ["jobs"], verbs = local.argocd_crud }, # PreSync migrate, PostSync smoke
    { apiGroups = ["networking.k8s.io"], resources = ["ingresses", "networkpolicies"], verbs = local.argocd_crud },
    { apiGroups = ["policy"], resources = ["poddisruptionbudgets"], verbs = local.argocd_crud },
    { apiGroups = ["traefik.io"], resources = ["middlewares", "serverstransports"], verbs = local.argocd_crud },
    { apiGroups = ["cert-manager.io"], resources = ["certificates", "issuers"], verbs = local.argocd_crud },
    { apiGroups = ["secrets.hashicorp.com"], resources = ["vaultauths", "vaultconnections", "vaultstaticsecrets"], verbs = local.argocd_crud },
    { apiGroups = ["rabbitmq.com"], resources = local.argocd_rabbitmq_kinds, verbs = local.argocd_crud },
  ]

  # No `secrets`, no `pods/exec`, no `pods/ephemeralcontainers`, no `*`, no non-resource
  # URL, nothing on (Validating|Mutating)AdmissionPolicy or -Binding.
  argocd_controller_rules = concat(
    local.argocd_controller_managed,
    [
      # Read only, to draw the resource tree.
      { apiGroups = [""], resources = ["pods"], verbs = ["get", "list", "watch"] },
      { apiGroups = ["apps"], resources = ["replicasets", "statefulsets"], verbs = ["get", "list", "watch"] },
      # Cluster-scoped kinds of the projects. Namespaces and CRDs: no `delete`, which would
      # take every object in them along; an admin removes one on purpose.
      { apiGroups = [""], resources = ["namespaces"], verbs = local.argocd_apply },
      { apiGroups = ["apiextensions.k8s.io"], resources = ["customresourcedefinitions"], verbs = local.argocd_apply },
      # The operators' ClusterRoles, bindings and the two leader-election Roles.
      { apiGroups = ["rbac.authorization.k8s.io"], resources = ["clusterroles", "clusterrolebindings", "roles", "rolebindings"], verbs = local.argocd_crud },
      # A role may only be written or bound by someone who holds its permissions (the
      # cluster operator's has pods/exec) or may escalate/bind it: only these names.
      { apiGroups = ["rbac.authorization.k8s.io"], resources = ["clusterroles"], verbs = ["escalate"], resourceNames = local.argocd_operator_cluster_roles },
      { apiGroups = ["rbac.authorization.k8s.io"], resources = ["clusterroles"], verbs = ["bind"], resourceNames = local.argocd_operator_bound_cluster_roles },
      { apiGroups = ["rbac.authorization.k8s.io"], resources = ["roles"], verbs = ["escalate", "bind"], resourceNames = local.argocd_operator_roles },
      # The operators' four webhook configurations (ryft-operators project): get, list,
      # watch and create cannot be limited by name; changing or deleting one can.
      { apiGroups = ["admissionregistration.k8s.io"], resources = ["mutatingwebhookconfigurations", "validatingwebhookconfigurations"], verbs = ["get", "list", "watch", "create"] },
      { apiGroups = ["admissionregistration.k8s.io"], resources = ["mutatingwebhookconfigurations"], verbs = ["update", "patch", "delete"], resourceNames = local.argocd_operator_mutating_webhooks },
      { apiGroups = ["admissionregistration.k8s.io"], resources = ["validatingwebhookconfigurations"], verbs = ["update", "patch", "delete"], resourceNames = local.argocd_operator_validating_webhooks },
    ],
  )

  # The server shows live objects and logs and does a few actions; it syncs nothing.
  # Nothing on admissionregistration.k8s.io, no secrets, no pods/exec, no pods/ephemeralcontainers.
  argocd_server_rules = [
    { apiGroups = [""], resources = ["configmaps", "services", "serviceaccounts", "namespaces", "pods", "pods/log"], verbs = ["get"] },
    { apiGroups = ["apps"], resources = ["deployments", "replicasets", "statefulsets"], verbs = ["get"] },
    { apiGroups = ["autoscaling"], resources = ["horizontalpodautoscalers"], verbs = ["get"] },
    { apiGroups = ["batch"], resources = ["jobs"], verbs = ["get"] },
    { apiGroups = ["networking.k8s.io"], resources = ["ingresses", "networkpolicies"], verbs = ["get"] },
    { apiGroups = ["policy"], resources = ["poddisruptionbudgets"], verbs = ["get"] },
    { apiGroups = ["traefik.io"], resources = ["middlewares", "serverstransports"], verbs = ["get"] },
    { apiGroups = ["cert-manager.io"], resources = ["certificates", "issuers"], verbs = ["get"] },
    { apiGroups = ["secrets.hashicorp.com"], resources = ["vaultauths", "vaultconnections", "vaultstaticsecrets"], verbs = ["get"] },
    { apiGroups = ["rabbitmq.com"], resources = local.argocd_rabbitmq_kinds, verbs = ["get"] },
    { apiGroups = ["apiextensions.k8s.io"], resources = ["customresourcedefinitions"], verbs = ["get"] },
    { apiGroups = ["rbac.authorization.k8s.io"], resources = ["clusterroles", "clusterrolebindings", "roles", "rolebindings"], verbs = ["get"] },
    { apiGroups = [""], resources = ["events"], verbs = ["list", "create"] },
    { apiGroups = ["argoproj.io"], resources = ["applications"], verbs = ["get", "list", "update", "watch"] },
    # No write on workloads: the UI is public (D103), so a leaked server token must not be
    # able to rewrite a pod template. Restarts and deletes are the cluster admin's kubectl.
  ]

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
        # Strict: the controller lists and watches only the resource types its ClusterRole
        # allows, instead of failing on the rest (T12-f); the narrowed rules need it.
        "resource.respectRBAC" = "strict"
        # No terminal into pods from the UI (the chart would add pods/exec to the server's
        # role); exec is the cluster admin's job.
        "exec.enabled" = false
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
      clusterRoleRules = { enabled = true, rules = local.argocd_controller_rules }
      replicas         = 1
      podAnnotations   = local.argocd_scrape.controller
      resources = {
        requests = { cpu = "50m", memory = "256Mi" }
        limits   = { memory = "768Mi" }
      }
    }
    server = {
      clusterRoleRules = { enabled = true, rules = local.argocd_server_rules }
      replicas         = 1
      podAnnotations   = local.argocd_scrape.server
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
    # Off: this chart version has no enabled flag, so zero replicas. The applications are
    # plain files in deploy/argocd/apps; an ApplicationSet could create Applications
    # outside the fixed set the admission layer allows (D120). The CRD stays.
    applicationSet = {
      replicas = 0
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
