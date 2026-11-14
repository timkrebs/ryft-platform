# Vault Community Edition in the cluster (D186, D188): three Raft replicas in namespace `vault`, TLS from the
# dedicated cert-manager CA ryft-vault (D190). The seal is var.vault_seal (D208): Shamir by default (no seal stanza:
# every pod that starts is unsealed by hand with the operator's key shares), or auto-unseal by AWS KMS through EKS Pod
# Identity (iam.tf, D189) with "awskms". A Terraform add-on like VSO and cert-manager, not an Argo CD application:
# Argo CD would need statefulsets write, a binding to system:auth-delegator and an image outside its allowlist, on
# the server that holds every credential.
#
# What this creates is an uninitialised Vault: three pods Running and 0/1 ready, by design (D196). The init, the
# audit devices and the admin identity are the operator's bootstrap (docs/OPERATIONS.md -> Vault). It is the only
# Vault (D207): VSO may reach it from this apply on, and every VaultConnection infra/vault generates names it.
#
# Admin access is `kubectl port-forward` only (output vault_port_forward_command): no Ingress, no LoadBalancer.
# Upgrades are by hand, one pod at a time: updateStrategy OnDelete, so an apply that changes the pod template
# restarts nothing (delete a standby, wait for it to rejoin and unseal, then the next; the active one last).
#
# Two releases: helm_release.vault_extras (charts/vault-extras: the server Certificate vault-tls, the inert
# Certificate vault-ca whose ca.crt the operator's CLI trusts, the NetworkPolicies, the PodDisruptionBudget, the
# service-registration Role and the Raft snapshot CronJob, D192) first, so the TLS Secret and the default-deny exist
# before the first pod; then the upstream chart.

locals {
  vault_namespace       = "vault"
  vault_service_account = "vault"
  vault_replicas        = 3

  # D208: the KMS seal, its key lookup, its Pod Identity role and the server's AWS egress only with "awskms".
  vault_seal_awskms = var.vault_seal == "awskms"

  # docker.io/hashicorp/vault is Community Edition (Enterprise is hashicorp/vault-enterprise). Pinned by the
  # multi-arch index digest (versions.md section 1). The chart writes repository:tag, so the digest rides in the
  # tag. The admission chart's P2 allows exactly this reference in `vault` (D195).
  vault_image = {
    repository = "docker.io/hashicorp/vault"
    tag        = "2.2.1@sha256:962a92876e168d68eaaa24b0b8b49bbce1cb63024060a67bba95cd158a366fc2"
  }

  # Where the chart mounts the TLS Secret (a whole-directory Secret volume, no subPath: the kubelet refreshes it
  # in place, and tls_reload_interval picks the renewal up without a restart).
  vault_tls_dir = "/vault/userconfig/vault-tls"

  # Every name a client or a peer may reach this server by. retry_join's leader_api_addr names each pod through
  # the headless Service, so the list grows with the replicas (the test pins one name per pod).
  vault_dns_names = concat(
    [
      "vault", "vault.vault", "vault.vault.svc", "vault.vault.svc.cluster.local",
      "vault-active", "vault-active.vault.svc", "vault-active.vault.svc.cluster.local",
      "vault-standby", "vault-standby.vault.svc",
      "vault-internal", "vault-internal.vault.svc",
    ],
    [for i in range(local.vault_replicas) : "vault-${i}.vault-internal"],
    [for i in range(local.vault_replicas) : "vault-${i}.vault-internal.vault.svc"],
    ["localhost"],
  )

  vault_retry_join = join("\n", [for i in range(local.vault_replicas) : trimspace(<<-HCL
    retry_join {
      leader_api_addr     = "https://vault-${i}.vault-internal:8200"
      leader_ca_cert_file = "${local.vault_tls_dir}/ca.crt"
    }
  HCL
  )])

  # The server's configuration. The chart's start script copies it to /tmp and replaces the upper-case words
  # HOSTNAME, POD_IP, HOST_IP, API_ADDR, TRANSIT_ADDR and RAFT_ADDR in it (sed, everywhere); none may appear
  # (precondition below).
  #  - disable_mlock: required with Raft since 1.20, and the 2.0.2+ image has no IPC_LOCK; Pod Security restricted.
  #  - listener: TLS only; tls_reload_interval (2.2.0+) reloads a renewed certificate; /v1/sys/metrics without a
  #    token on every node (D194). The NetworkPolicy admits selected peers only (charts/vault-extras), but traffic
  #    from the node's address (host-network pods such as the privileged alloy-daemonset, the kubelet) is likely
  #    admitted by the VPC CNI regardless. The metrics are not secret, but they are not all a peer reaches without a
  #    token: before the init PUT /v1/sys/init (hence var.vault_collector_peer stays false until the operator's
  #    own init), and after it sys/health, sys/seal-status, sys/leader, starting or cancelling a generate-root or
  #    rekey attempt (completing one needs recovery shares) and the login endpoints (design section 6, G2).
  #  - retry_join: one block per pod, trusting the ryft-vault CA; no client certificate (the listener asks none).
  #  - seal: only with var.vault_seal = "awskms"; Shamir (the default) is Vault's seal when the config has none.
  #    The key's ARN, not the alias (an alias can be re-pointed silently); the region set here, so the seal
  #    does not depend on the AWS_REGION the EKS webhook injects; no credentials of any kind (the SDK's default
  #    chain finds Pod Identity's). The directives open and close mid-line and carry no strip marker (~): in an
  #    indented heredoc a ~} eats the line's newline token, and the line after it would keep its indentation.
  #    So awskms renders exactly the stanza and blank lines of before, and shamir one blank line between the
  #    storage block and service_registration (tests/addons.tftest.hcl pins both).
  #  - service_registration: labels the pods vault-active=true|false, which the vault-active and vault-standby
  #    Services select on; without it they have no endpoints.
  vault_config = <<-HCL
    ui            = true
    disable_mlock = true
    cluster_name  = "vault-integrated-storage"

    listener "tcp" {
      address             = "[::]:8200"
      cluster_address     = "[::]:8201"
      tls_cert_file       = "${local.vault_tls_dir}/tls.crt"
      tls_key_file        = "${local.vault_tls_dir}/tls.key"
      tls_min_version     = "tls12"
      tls_reload_interval = "5m"

      telemetry {
        unauthenticated_metrics_access = true
      }
    }

    storage "raft" {
      path = "/vault/data"

      ${indent(2, local.vault_retry_join)}
    }

    %{if local.vault_seal_awskms}seal "awskms" {
      region     = "${var.region}"
      kms_key_id = "${local.vault_unseal_key_arn}"
    }

    %{endif}service_registration "kubernetes" {}

    telemetry {
      prometheus_retention_time = "2m"
      disable_hostname          = true
    }
  HCL

  # /v1/sys/health answers 200 active, 429 standby (200 with standbyok), 501 uninitialised, 503 sealed, 474 a
  # standby that cannot reach the active node, 530 a node removed from the Raft cluster. Ready: only a serving
  # node. Alive: any answer at all; restarting a standby on a heartbeat gap (474) or a removed node (530) helps
  # nothing and costs a rejoin.
  vault_health_ready = "/v1/sys/health?standbyok=true"
  vault_health_alive = "/v1/sys/health?standbyok=true&sealedcode=204&uninitcode=204&haunhealthycode=204&removedcode=204"

  vault_server_selector = {
    "app.kubernetes.io/name"     = "vault"
    "app.kubernetes.io/instance" = "vault"
    "component"                  = "server"
  }

  vault_values = {
    global = {
      enabled    = true
      tlsDisable = false
    }
    # No agent injector (VSO syncs Secrets) and no CSI provider: no webhook, no DaemonSet.
    injector = { enabled = false }
    csi      = { enabled = false }

    server = {
      image = merge(local.vault_image, { pullPolicy = "IfNotPresent" })

      logLevel  = "info"
      logFormat = "json"

      # Requests sized for a few KV reads an hour (the nodes are CPU-bound, D64); no CPU limit (throttling Raft
      # heartbeats would cost leader elections). Revisit from vault_core_* once the metrics arrive.
      resources = {
        requests = { cpu = "100m", memory = "256Mi" }
        limits   = { memory = "512Mi" }
      }

      # Ready = initialised, unsealed, active or standby; the Services and the PDB count only those (D196). Alive =
      # the process answers at all, sealed and uninitialised included, so a pod waiting for its init or for KMS is
      # not killed, nor a standby on a heartbeat gap (474) or a removed node (530). The chart has no startupProbe
      # for the server; the delay covers the start. Liveness allows 6 x 5 s = 30 s of silence (the chart's 2 x 5 s
      # would kill a leader on a CPU-starved node, and cost an election).
      readinessProbe = {
        enabled = true
        path    = local.vault_health_ready
      }
      livenessProbe = {
        enabled             = true
        path                = local.vault_health_alive
        initialDelaySeconds = 60
        periodSeconds       = 5
        failureThreshold    = 6
      }

      # For the in-pod CLI (VAULT_ADDR is https://127.0.0.1:8200, set by the chart). No AWS_* variable at all.
      extraEnvironmentVars = {
        VAULT_CACERT = "${local.vault_tls_dir}/ca.crt"
      }

      volumes = [
        # 0440 (288 decimal): owner root, group fsGroup 1000, which the vault user (100) is in.
        { name = "vault-tls", secret = { secretName = "vault-tls", defaultMode = 288 } },
        # The start script writes /tmp/storageconfig.hcl; the root filesystem is read-only.
        { name = "tmp", emptyDir = { medium = "Memory", sizeLimit = "16Mi" } },
      ]
      volumeMounts = [
        { name = "vault-tls", mountPath = local.vault_tls_dir, readOnly = true },
        { name = "tmp", mountPath = "/tmp" },
      ]

      dataStorage = {
        enabled      = true
        size         = "10Gi"
        storageClass = kubernetes_storage_class_v1.gp3_encrypted.metadata[0].name
        accessMode   = "ReadWriteOnce"
      }
      auditStorage = {
        enabled      = true
        size         = "5Gi"
        storageClass = kubernetes_storage_class_v1.gp3_encrypted.metadata[0].name
        accessMode   = "ReadWriteOnce"
      }
      # The volumes outlive the StatefulSet and a scale-down (and gp3-encrypted retains the EBS volume too):
      # deleting Vault's data is a separate, manual step.
      persistentVolumeClaimRetentionPolicy = {
        whenDeleted = "Retain"
        whenScaled  = "Retain"
      }

      updateStrategyType = "OnDelete"

      # Time for the chart's preStop (sleep 5, then SIGTERM) and Vault's shutdown: an active node gives up
      # leadership and stops, a standby just stops. Neither leaves the Raft configuration (it stays a voter until
      # `vault operator raft remove-peer`). The chart's 10 s leaves little after the preStop's 5 s.
      terminationGracePeriodSeconds = 30

      # One pod per zone as well as per node (the chart's required hostname anti-affinity stays): an EBS volume is
      # zonal, so a pod's zone is fixed for good by its first PVC, and two pods in one zone would lose the quorum
      # with that zone. maxSkew 1 alone allows 2+1 when nodes exist in only two zones; minDomains = replicas
      # counts the missing zones as empty (skew 2), so the third pod stays Pending until a node in a third zone
      # exists. DoNotSchedule: Pending rather than a shared zone (minDomains requires it). The chart passes the
      # list through unchanged (toYaml).
      topologySpreadConstraints = [{
        maxSkew           = 1
        minDomains        = local.vault_replicas
        topologyKey       = "topology.kubernetes.io/zone"
        whenUnsatisfiable = "DoNotSchedule"
        labelSelector     = { matchLabels = local.vault_server_selector }
      }]

      # Pod Security restricted. A map here replaces the chart's default context entirely, so all of it is spelt out.
      statefulSet = {
        securityContext = {
          pod = {
            runAsNonRoot   = true
            runAsUser      = 100
            runAsGroup     = 1000
            fsGroup        = 1000
            seccompProfile = { type = "RuntimeDefault" }
          }
          container = {
            allowPrivilegeEscalation = false
            readOnlyRootFilesystem   = true
            capabilities             = { drop = ["ALL"] }
          }
        }
      }

      # The Services publish ready pods only (the headless vault-internal always publishes all: retry_join needs it).
      service = {
        enabled                  = true
        publishNotReadyAddresses = false
        active                   = { enabled = true }
        standby                  = { enabled = true }
      }

      ingress       = { enabled = false }
      route         = { enabled = false }
      networkPolicy = { enabled = false } # charts/vault-extras has the policies (default deny)

      # The service account the pod identity association names. No annotations: an eks.amazonaws.com/role-arn
      # would put IRSA ahead of Pod Identity in the SDK's chain.
      # The chart's discovery Role (get/list/watch/update/patch on every pod in the namespace) is off; vault-extras
      # has a Role limited to the server pods by name with what service_registration "kubernetes" calls.
      serviceAccount = {
        create           = true
        name             = local.vault_service_account
        serviceDiscovery = { enabled = false }
      }
      # ClusterRoleBinding vault-server-binding -> system:auth-delegator: Vault reviews the service account tokens
      # VSO logs in with (TokenReview), as itself; no reviewer JWT (D187).
      authDelegator = { enabled = true }

      standalone = { enabled = false }
      ha = {
        enabled  = true
        replicas = local.vault_replicas
        # The address standbys advertise: the pod's name under the headless Service, which the certificate carries
        # (the chart's default is the pod IP, which it does not). VAULT_K8S_POD_NAME, because the chart defines it
        # before VAULT_API_ADDR (HOSTNAME comes after, and $(VAR) expands only earlier variables).
        apiAddr = "https://$(VAULT_K8S_POD_NAME).vault-internal.vault.svc:8200"
        raft = {
          enabled   = true
          setNodeId = true
          config    = local.vault_config
        }
        # Off: the chart's PDB uses the default unhealthy-pod policy, so before the init (every pod unready) it
        # would block every node drain. charts/vault-extras has the PDB (minAvailable 2, AlwaysAllow, D196).
        disruptionBudget = {
          enabled = false
        }
      }
    }

    # The UI on the API port, through the port-forward only.
    ui = {
      enabled                  = true
      serviceType              = "ClusterIP"
      publishNotReadyAddresses = false
    }
  }

  # What the chart replaces in the config (its start script's sed), and the AWS settings that would come before
  # Pod Identity in the SDK's credential chain.
  vault_config_placeholders = ["HOSTNAME", "POD_IP", "HOST_IP", "API_ADDR", "TRANSIT_ADDR", "RAFT_ADDR"]
  vault_aws_credential_env  = ["AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE"]

  # The snapshot CronJob's upload image (D192): the AWS CLI v2, pinned by the multi-arch index digest of 2.39.5 (the
  # newest 2.x on 2026-11-06; Amazon Linux 2023, entrypoint /usr/local/bin/aws, no USER: the pod sets 100:1000). The
  # admission chart's P2 admits exactly this reference in `vault` (images.awsCli in charts/admission-policy); the
  # two change together (tests/addons.tftest.hcl compares them).
  vault_snapshot_aws_cli_image = "public.ecr.aws/aws-cli/aws-cli:2.39.5@sha256:0cea63506f28774b70b1a3dd1934f49bc202d9784dee23370acc81438e8d0292"

  # VSO reaches 8200 from the first apply: this is the only Vault (D207), every VaultConnection infra/vault
  # generates names it, and there is no cutover. Until the operator's init VSO's logins fail and it retries; the
  # workloads keep waiting for their Secrets. The collector (alloy-deployment, job ryft/vault in
  # deploy/monitoring/k8s-monitoring.yaml, D194) is off until var.vault_collector_peer: the NetworkPolicy admits a
  # peer to all of 8200, and before the init PUT /v1/sys/init is unauthenticated, so the peer opens only after the
  # operator's own init (OPERATIONS -> Vault metrics (once)) and closes again for a restore into a fresh cluster
  # (-> Vault -> Disaster recovery).
  # kmsSeal: the server pods' egress to public 443 (KMS, through the NAT) and to the Pod Identity agent, only for
  # the awskms seal (D208); under Shamir they reach the peers, the API server in the VPC and DNS only.
  vault_extras_values = {
    vpcCIDR  = var.vpc_cidr
    replicas = local.vault_replicas
    kmsSeal  = local.vault_seal_awskms
    tls = {
      dnsNames    = local.vault_dns_names
      ipAddresses = ["127.0.0.1"]
    }
    peers = {
      vso       = { enabled = true }
      collector = { enabled = var.vault_collector_peer }
    }
    # Every 6 hours at minute 17 (RPO 6 h; OPERATIONS -> Vault -> Snapshots). The bucket and key are infra/aws's
    # (iam.tf finds them); the job names the key by its full ARN, as the bucket policy and the role require.
    snapshot = {
      schedule = "17 */6 * * *"
      images = {
        vault  = "${local.vault_image.repository}:${local.vault_image.tag}"
        awsCli = local.vault_snapshot_aws_cli_image
      }
      bucket    = local.vault_snapshots_bucket
      keyARN    = data.aws_kms_alias.vault_snapshots.target_key_arn
      accountID = local.account_id
      region    = var.region
    }
  }

  # With awskms, the key the alias resolves to must be a key of this account in this region (a precondition below).
  # Once infra/aws's key exists its key ID can be pinned here as well, so a re-pointed alias fails the plan instead
  # of moving the seal.
  vault_unseal_key_arn_pattern = "^arn:aws[a-z-]*:kms:${var.region}:${local.account_id}:key/[0-9a-f-]+$"
}

# prevent_destroy on the namespace and both releases: deleting the namespace deletes the PVCs (the EBS volumes stay,
# Released), and destroying or replacing the extras removes default-deny and vault-server, which leaves the server
# pods without any policy, i.e. open both ways. A deliberate removal is two reviewed steps: a PR that deletes the
# three `prevent_destroy = true` lines (and nothing else), applied; then the PR that removes the resources.

# Pod Security restricted on all three modes (the admission chart's P1 is to extend to `vault`, D195).
resource "kubernetes_namespace_v1" "vault" {
  metadata {
    name = local.vault_namespace
    labels = {
      "pod-security.kubernetes.io/enforce" = "restricted"
      "pod-security.kubernetes.io/warn"    = "restricted"
      "pod-security.kubernetes.io/audit"   = "restricted"
    }
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "helm_release" "vault_extras" {
  name      = "vault-extras"
  namespace = kubernetes_namespace_v1.vault.metadata[0].name
  chart     = "${path.module}/charts/vault-extras"

  values = [yamlencode(local.vault_extras_values)]

  # The ClusterIssuer ryft-vault (charts/cluster-issuers) signs the Certificates. The snapshot role's association
  # before the CronJob's first pod (the Pod Identity webhook injects credentials at pod creation only). The admission
  # release before this one: a bump of the AWS CLI (or server) pin changes P2's images.awsCli and this chart's
  # snapshot.images in one PR, and P2 must admit the new reference before the CronJob carries it (helm_release.vault
  # follows through its own depends_on on this release). With admission off (count 0) the edge is empty.
  depends_on = [helm_release.cluster_issuers, module.vault_snapshot_pod_identity, helm_release.admission_policy]

  lifecycle {
    prevent_destroy = true

    # Both job images by digest: the admission chart's P2 admits nothing else in `vault`, and a tag can move.
    precondition {
      condition     = alltrue([for i in values(local.vault_extras_values.snapshot.images) : can(regex("^[a-z0-9./-]+:[0-9][0-9.]*@sha256:[0-9a-f]{64}$", i))])
      error_message = "The snapshot CronJob's images must be pinned as <repository>:<version>@sha256:<64 hex>."
    }
  }
}

resource "helm_release" "vault" {
  name       = "vault"
  namespace  = kubernetes_namespace_v1.vault.metadata[0].name
  repository = local.charts.vault.repository
  chart      = local.charts.vault.chart
  version    = local.charts.vault.version

  values = [yamlencode(local.vault_values)]

  # An uninitialised Vault is not ready by design, so waiting would time out every first apply (D188).
  wait = false

  # With awskms, the association before the first pod: the Pod Identity webhook injects credentials at pod creation
  # only, and a pod without them cannot unseal (delete it once the association exists). Under Shamir the module has
  # no instance and the edge is empty. The extras before the first pod: the TLS Secret it mounts and the
  # default-deny.
  depends_on = [module.vault_unseal_pod_identity, helm_release.vault_extras]

  lifecycle {
    prevent_destroy = true

    # awskms: the alias must resolve to a key of this account in this region, otherwise the seal stanza would name
    # a key Vault cannot use and every pod would stay sealed (an error of plan and apply, not a warning). Shamir
    # reads no alias.
    precondition {
      condition     = !local.vault_seal_awskms || can(regex(local.vault_unseal_key_arn_pattern, local.vault_unseal_key_arn))
      error_message = "alias/${var.cluster_name}-vault-unseal does not point at a KMS key of this account in ${var.region}: the seal stanza would name a key Vault cannot use, and every pod would stay sealed."
    }
    # The seal stanza and the server's AWS egress follow var.vault_seal together (D208): one awskms stanza and the
    # KMS egress, or neither (Shamir: no seal stanza, so Vault's own).
    precondition {
      condition     = length(regexall("(?m)^seal ", local.vault_config)) == (local.vault_seal_awskms ? 1 : 0) && local.vault_extras_values.kmsSeal == local.vault_seal_awskms
      error_message = "The Vault config's seal stanza or vault-extras' kmsSeal does not follow vault_seal = \"${var.vault_seal}\": awskms has exactly one seal \"awskms\" stanza and the KMS egress, shamir neither."
    }
    precondition {
      condition     = alltrue([for w in local.vault_config_placeholders : !strcontains(local.vault_config, w)])
      error_message = "The Vault config contains one of ${join(", ", local.vault_config_placeholders)}: the chart's start script replaces those words everywhere in it."
    }
    precondition {
      condition = (
        length([for k in keys(local.vault_values.server.extraEnvironmentVars) : k if startswith(k, "AWS_")]) == 0 &&
        !strcontains(local.vault_config, "access_key") && !strcontains(local.vault_config, "secret_key") &&
        !strcontains(local.vault_config, "role_arn") && !strcontains(local.vault_config, "shared_creds") &&
        !can(local.vault_values.server.serviceAccount.annotations) && !can(local.vault_values.server.extraSecretEnvironmentVars)
      )
      error_message = "Vault has no AWS identity under Shamir and Pod Identity's only under awskms: no AWS_* variable (${join(", ", local.vault_aws_credential_env)}), no credentials in the config, no service account annotation (IRSA) and no secret environment variables."
    }
    precondition {
      condition     = length([for n in local.vault_dns_names : n if can(regex("^vault-[0-9]+\\.vault-internal$", n))]) == local.vault_values.server.ha.replicas
      error_message = "The server certificate must name every pod (vault-N.vault-internal): retry_join reaches the peers by those names."
    }
    precondition {
      condition     = local.vault_values.ui.serviceType == "ClusterIP" && !strcontains(jsonencode(local.vault_values), "LoadBalancer") && !strcontains(jsonencode(local.vault_values), "NodePort")
      error_message = "Vault is reached through the port-forward only: no LoadBalancer or NodePort Service."
    }
  }
}
