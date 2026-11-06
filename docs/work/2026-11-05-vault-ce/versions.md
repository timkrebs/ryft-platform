# Vault Community Edition on EKS: pinned facts

Research date: 2026-11-05. Read-only research; nothing was applied, no cluster or Terraform state was touched.
Every fact is marked `verified at <URL, date>` or `not verified` with the command a human can run.

Working copies of the sources (chart tarball, changelog, docs) were read from a scratch directory; the URLs below are the
public originals. Docs source for developer.hashicorp.com/vault lives in
`github.com/hashicorp/web-unified-docs` (path `content/vault/v2.x/content/...`); the rendered pages are at
`developer.hashicorp.com/vault/docs/...`.

## 0. Summary of recommended pins

| Item | Pin | Evidence |
| --- | --- | --- |
| Vault CE image | `hashicorp/vault:2.2.1@sha256:962a92876e168d68eaaa24b0b8b49bbce1cb63024060a67bba95cd158a366fc2` | section 1 |
| Helm chart | `vault` 0.34.1 from `https://helm.releases.hashicorp.com` (chart digest `df4c37fac32496892ff1e1d6fd7954d3a402cc7b1b2b4c2226af11b8a57564c4`) | section 2 |
| Terraform provider | `hashicorp/vault` 5.13.0 (the repo already locks 5.13.0 under `~> 5.0`) | section 10 |
| Disable mlock | `disable_mlock = true` in the config | section 4 |

Things that are surprising or need a decision (details in the sections):

1. `server.image.tag: "2.2.1"` with `repository: hashicorp/vault` IS a real Community Edition tag (released 2026-10-22). The
   "Enterprise" comment in `deploy/vault/override-values.yaml` is wrong. Enterprise images are a different repository
   (`hashicorp/vault-enterprise`, tag `2.2.1-ent`).
2. The chart (0.34.1, newest) defaults to Vault 2.0.4 and was tested with 2.0.4/1.21.9/1.20.14/1.19.20, not 2.2.x. Overriding
   the tag to 2.2.1 is outside the chart's tested matrix. It renders fine (checked with `helm template`), but no upstream
   CI covers the combination.
3. Vault 2.2.0 moved the `awskms` seal from AWS SDK v1 to v2 (go.mod evidence, no changelog line). Both SDKs support EKS
   Pod Identity, so static credentials are not needed. Set `region` explicitly: Pod Identity does not inject `AWS_REGION`.
4. Container images from 2.0.2 on no longer carry the `IPC_LOCK` capability (2.0.1 briefly required it). `disable_mlock` is
   mandatory to set for Raft anyway (Vault refuses to start without an explicit value, since 1.20.0).
5. Vault 2.2.0 added `tls_reload_interval` on the TCP listener. Certificate renewal no longer needs SIGHUP or a pod restart.
6. CE support is "latest minor only": CE patch releases for 1.21 ended at 1.21.4 (2026-03-04), 1.20 at 1.20.4. Later 1.x
   patch releases (1.21.5 and up) exist only as Enterprise. Plan to follow each CE minor (2.0 on 2026-04-14, 2.1 on
   2026-09-01, 2.2 on 2026-10-15).
7. The existing `infra/vault` Terraform root uses `vault_namespace` and the `namespace` argument on every resource. Namespaces
   are Enterprise/HCP only. Against Community Edition those resources fail. This is a structural change, not a pin. See
   section 10.
8. The override file replaces the whole `server.ha.raft.config`, so it drops `service_registration "kubernetes" {}` (the
   `vault-active` and `vault-standby` Services then have no endpoints, because they select on the pod label that this
   stanza sets) and has no `disable_mlock` (Vault 2.x refuses to start with Raft and no explicit value). `global.resources`
   is not a chart key and is ignored. `ui.serviceType: LoadBalancer` exposes the UI on a public NLB/CLB by default.
9. The Licensor is now IBM (BUSL-1.1, change date four years after each release, then MPL 2.0). Internal production use is
   expressly not a competitive offering.

## 1. Latest stable Vault Community Edition

- Latest CE release: **2.2.1**, released **2026-10-22**.
  - verified at https://api.releases.hashicorp.com/v1/releases/vault?limit=20 (entry `2.2.1`, `timestamp_created`
    2026-10-22T22:02:15Z, `is_prerelease` false), 2026-11-05. Same date on
    https://github.com/hashicorp/vault/releases/tag/v2.2.1 (published 2026-10-22T21:59:36Z).
  - Recent CE releases, same sources: 2.2.0 (2026-10-15), 2.1.2 (2026-10-07), 2.1.1 (2026-09-16), 2.1.0 (2026-09-01),
    2.0.4 (2026-08-04), 2.0.3 (2026-06-17), 2.0.2 (2026-06-05), 2.0.1 (2026-05-19), 2.0.0 (2026-04-14).
  - Versions with a `+ent` suffix in the releases API are Enterprise; the plain `2.2.1` is CE.
  - 2.2.1 changelog (security): fixes a policy-name cache-key collision that could resolve aliases to the root policy; Go
    1.27.3; Alpine CA bundle update. verified at
    https://github.com/hashicorp/vault/blob/main/CHANGELOG.md (section "## 2.2.1", 2026-10-22).
- Docker image `hashicorp/vault:2.2.1` exists.
  - verified at https://hub.docker.com/v2/repositories/hashicorp/vault/tags/2.2.1 (last_updated 2026-10-22T22:04:46Z),
    2026-11-05. The tags `2.2` and `latest` point at the same digest.
  - Multi-arch index digest (media type `application/vnd.docker.distribution.manifest.list.v2+json`):
    `sha256:962a92876e168d68eaaa24b0b8b49bbce1cb63024060a67bba95cd158a366fc2`
    - verified at https://registry-1.docker.io/v2/hashicorp/vault/manifests/2.2.1 (response header
      `docker-content-digest`, anonymous pull token), 2026-11-05.
  - Per-platform manifests (same source):
    - linux/amd64 `sha256:2194d86d9ddbb17a6d017c3b2e555e1d4431195f18c3cb2827e3f61320fb6855`
    - linux/arm64 `sha256:4e055bd22fb0f8a265573be865a4dd7fefcab977dea011c5f68ee194357bd964`
    - linux/386 `sha256:a51ffc1f940849d3c9d86033765563e2bd396199fdb9be8d34caddd6a9e4a40d` (not needed)
  - Pin the index digest (works on both amd64 and arm64 nodes). Re-check before use:
    `docker buildx imagetools inspect hashicorp/vault:2.2.1` or `crane digest hashicorp/vault:2.2.1`
    (neither tool was installed on the research machine; the digests above were read straight from the registry API:
    `T=$(curl -s 'https://auth.docker.io/token?service=registry.docker.io&scope=repository:hashicorp/vault:pull' | jq -r .token); curl -sI -H "Authorization: Bearer $T" -H 'Accept: application/vnd.docker.distribution.manifest.list.v2+json' https://registry-1.docker.io/v2/hashicorp/vault/manifests/2.2.1`).
  - The chart accepts a digest inside `server.image.tag`: `tag: "2.2.1@sha256:962a..."` renders
    `image: hashicorp/vault:2.2.1@sha256:962a...` (checked with `helm template`, chart 0.34.1).
- Is the user's `repository: hashicorp/vault`, `tag: "2.2.1"` a real CE tag? Yes. The Docker Hub description of
  `hashicorp/vault` is "Official vault docker images"; the Enterprise images are in the separate repository
  `hashicorp/vault-enterprise` (tags `2.2.1-ent`, `2.2.1-ent.hsm`, ...). verified at
  https://hub.docker.com/v2/repositories/hashicorp/vault-enterprise/tags?name=2.2.1, 2026-11-05. The comment "Use the
  Enterprise Image" in the override file is therefore wrong; `2.2.1` from `hashicorp/vault` runs without a license key.

## 2. Helm chart `hashicorp/vault`

- Latest chart: **0.34.1**, released **2026-08-13**; `appVersion` 2.0.4; `kubeVersion >= 1.20.0-0`.
  - verified at https://helm.releases.hashicorp.com/index.yaml (top entry, `created` 2026-08-13T15:16:55Z,
    digest `df4c37fac32496892ff1e1d6fd7954d3a402cc7b1b2b4c2226af11b8a57564c4`) and
    https://github.com/hashicorp/vault-helm/releases/tag/v0.34.1, 2026-11-05. I downloaded
    `https://helm.releases.hashicorp.com/vault-0.34.1.tgz` and its SHA-256 matches the index digest.
  - Previous: 0.34.0 (2026-07-02, default Vault 2.0.3), 0.33.0 (2026-06-08), 0.32.0 (2026-01-14).
  - Chart changelog 0.34.1: defaults vault 2.0.4, vault-k8s 1.7.6, csi-provider 1.7.4; "Tested with Vault v2.0.4, v1.21.9,
    v1.20.14, v1.19.20". verified at https://github.com/hashicorp/vault-helm/blob/v0.34.1/CHANGELOG.md.
  - No chart release has been made since the 2.1 and 2.2 Vault releases. Command to recheck:
    `curl -s https://api.github.com/repos/hashicorp/vault-helm/releases?per_page=3 | jq -r '.[].tag_name'`.
- Values keys, verified by reading `values.yaml` of chart 0.34.1 and rendering with `helm template` (offline, local):

| Key | Default / behaviour in 0.34.1 |
| --- | --- |
| `server.image.repository` / `.tag` / `.pullPolicy` | `hashicorp/vault` / `2.0.4` / `IfNotPresent` |
| `server.ha.enabled`, `server.ha.replicas` | `false`, `3`. Set both plus `server.ha.raft.enabled: true` |
| `server.ha.raft.enabled`, `.setNodeId`, `.config` | `false`, `false`, a TLS-off example config. `setNodeId` is a legacy flag: the chart always sets `VAULT_RAFT_NODE_ID` from the pod name (seen in the render). `config` is replaced wholesale by your value (string or HCL/JSON) |
| `server.ha.apiAddr`, `.clusterAddr` | `null`; the chart exports `VAULT_API_ADDR=https://$(POD_IP):8200` and `VAULT_CLUSTER_ADDR=https://$(HOSTNAME).<release>-internal:8201` |
| `server.ha.disruptionBudget.enabled` / `.maxUnavailable` | `true` / `null` => floor((n-1)/2): renders `maxUnavailable: 1` for 3 replicas and `2` for 5 |
| `server.dataStorage.enabled/size/storageClass/accessMode/mountPath` | `true`, `10Gi`, `null` (cluster default), `ReadWriteOnce`, `/vault/data` |
| `server.auditStorage.enabled/size/storageClass/...` | `false`, `10Gi`, `null`, mounted at `/vault/audit`. The audit device itself must still be enabled in Vault (Terraform `vault_audit`) |
| `server.persistentVolumeClaimRetentionPolicy` | `{}` (StatefulSet PVC retention) |
| `server.extraEnvironmentVars` | map of env vars. Use for `AWS_REGION` and `VAULT_CACERT`. No static AWS keys are needed with Pod Identity |
| `server.extraSecretEnvironmentVars` | list of `envName/secretName/secretKey` |
| `server.extraVolumes` | list of `{type: secret|configMap, name, path}`, mounted read-only at `/vault/userconfig/<name>`. Marked "Deprecated: please use `volumes`" |
| `server.volumes` / `server.volumeMounts` | raw pod volumes and container mounts (rendered via `toYaml`); use these for an `emptyDir` at `/tmp` |
| `server.serviceAccount.create/name/annotations/extraLabels/serviceDiscovery.enabled` | `true`, generated, `{}`, `{}`, `true` (Role for `service_registration "kubernetes"`) |
| `server.authDelegator.enabled` | `true`: creates ClusterRoleBinding `<release>-server-binding` to `system:auth-delegator` for the server SA (see section 6) |
| `server.affinity` | multi-line string; default is `requiredDuringSchedulingIgnoredDuringExecution` pod anti-affinity on `kubernetes.io/hostname`. With 5 replicas this needs 5 nodes (the repo cluster has 3) |
| `server.topologySpreadConstraints`, `server.tolerations`, `server.nodeSelector` | `[]`, `[]`, `{}` |
| `server.resources` | `{}` (no default). `global.resources` does not exist as a key |
| `server.readinessProbe.*` | `enabled: true`, `path` unset => exec `vault status -tls-skip-verify`; with `path` set => httpGet (scheme follows `global.tlsDisable`). Also `port` 8200, `failureThreshold` 2, `initialDelaySeconds` 5, `periodSeconds` 5, `successThreshold` 1, `timeoutSeconds` 3 |
| `server.livenessProbe.*` | `enabled: false`, `path: /v1/sys/health?standbyok=true`, `initialDelaySeconds: 60`, `execCommand: []`, same other keys |
| `server.startupProbe` | NOT supported by the server StatefulSet in 0.34.1 (the `startupProbe` key exists only under `injector`). Use `livenessProbe.initialDelaySeconds` or a long `failureThreshold` instead |
| `server.priorityClassName` | `""` |
| `server.terminationGracePeriodSeconds`, `server.preStopSleepSeconds`, `server.preStop` | `10`, `5`, `[]`; default preStop is `sleep 5 && kill -SIGTERM $(pidof vault)` |
| `server.updateStrategyType` | `OnDelete` (default). Pods must be deleted by hand, or set `RollingUpdate` and let Argo CD drive it |
| `server.service.active.enabled`, `.standby.enabled` | `true`, `true`: Services `vault-active` (selector `vault-active: "true"`) and `vault-standby` (`vault-active: "false"`). The labels are set by `service_registration "kubernetes"` in the config |
| `server.service.publishNotReadyAddresses` | `true` |
| `server.networkPolicy.enabled` / `.ingress` / `.egress` | `false`; default ingress allows ports 8200/8201 from any namespace (`namespaceSelector: {}`); egress `[]` (not restricted when `policyTypes` is unset, which is what the chart renders) |
| `server.statefulSet.securityContext.pod` / `.container` | `{}` / `{}`. When empty the chart renders pod `runAsNonRoot: true, runAsUser: 100, runAsGroup: 1000, fsGroup: 1000` and container `allowPrivilegeEscalation: false` only. When you set a map it replaces the default completely (render-tested), so repeat `runAsUser`, `runAsGroup` and `fsGroup` |
| `server.hostNetwork` | `false` |
| `server.shareProcessNamespace` | `false`; needed only if a sidecar must signal Vault |
| `server.includeConfigAnnotation` | `false`; adds `vaultproject.io/config-checksum` annotation |
| `server.logLevel`, `server.logFormat` | `""`; set `json` for slog-style JSON logs (`VAULT_LOG_FORMAT`) |
| `injector.enabled` | default follows `global.enabled` (true). Set `injector.enabled: false` to disable (render-tested: no injector objects) |
| `csi.enabled` | `false` (render-tested: no DaemonSet) |
| `ui.enabled`, `ui.serviceType` | `false`, `ClusterIP`. The override sets `LoadBalancer`, which creates an internet-facing or internal LB depending on controller defaults; prefer `ClusterIP` plus Traefik/Ingress |
| `global.tlsDisable` | `true` by default; `false` makes the chart use `https://127.0.0.1:8200` for `VAULT_ADDR`, HTTPS in probes and `https` in API/cluster addresses |
| `serverTelemetry.serviceMonitor.*`, `.prometheusRules.*` | disabled; ServiceMonitor wants `tlsConfig`/`authorization` settings |
| `server.enterpriseLicense.secretName` | leave empty for CE |

- Container security under Pod Security `restricted`: the chart's default container context lacks `capabilities.drop: [ALL]` and
  `seccompProfile`. Set via `server.statefulSet.securityContext` (render-tested, accepted):
  pod `{runAsNonRoot: true, runAsUser: 100, runAsGroup: 1000, fsGroup: 1000, seccompProfile: {type: RuntimeDefault}}`,
  container `{allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}, seccompProfile: {type: RuntimeDefault}}`.
  `readOnlyRootFilesystem: true` also renders, but the start script copies the config to `/tmp/storageconfig.hcl`
  (seen in the StatefulSet args), so a writable `/tmp` (`emptyDir` via `server.volumes` and `server.volumeMounts`) would be
  required. Not run-tested on a cluster.

## 3. How the `awskms` seal authenticates (EKS Pod Identity)

- AWS SDK generation: Vault **2.2.0 and later use AWS SDK for Go v2** for the seal: Vault 2.2.1 `go.mod` requires
  `github.com/hashicorp/go-kms-wrapping/wrappers/awskms/v4 v4.0.4` (imports `aws-sdk-go-v2/config`,
  `service/kms`, `go-secure-stdlib/awsutil/v2`), while 2.1.2 and everything earlier require `wrappers/awskms/v2 v2.0.11` on
  `aws-sdk-go v1.55.8`. verified at
  https://raw.githubusercontent.com/hashicorp/vault/v2.2.1/go.mod and `.../v2.1.2/go.mod`, 2026-11-05, and
  https://github.com/hashicorp/go-kms-wrapping/blob/wrappers/awskms/v4.0.4/wrappers/awskms/awskms.go. The Vault CHANGELOG
  has no line for this switch (searched "awskms", "kms-wrapping", "aws-sdk"); the evidence is the dependency change between
  v2.1.2 and v2.2.0. Re-check: `diff <(curl -s .../v2.1.2/go.mod) <(curl -s .../v2.2.0/go.mod) | grep -i kms`.
- Container credential provider / Pod Identity: yes, no static credentials needed, on both SDK generations.
  - EKS injects `AWS_CONTAINER_CREDENTIALS_FULL_URI=http://169.254.170.23/v1/credentials` and
    `AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE=/var/run/secrets/pods.eks.amazonaws.com/serviceaccount/eks-pod-identity-token`
    (projected token, audience `pods.eks.amazonaws.com`). verified at
    https://docs.aws.amazon.com/eks/latest/userguide/pod-id-how-it-works.html, 2026-11-05. Only these two variables are
    injected, so `AWS_REGION` must come from the seal stanza or `server.extraEnvironmentVars`.
  - AWS minimum SDK versions: Go v1 v1.47.11, Go v2 release-2023-11-14. verified at
    https://docs.aws.amazon.com/eks/latest/userguide/pod-id-minimum-sdk.html, 2026-11-05.
  - v2 path (Vault 2.2.0+): the wrapper builds its credentials with `config.LoadDefaultConfig`, i.e. the SDK default chain,
    which contains the container credential provider (verified in `awsutil/v2.1.2/awsutil/generate_credentials.go`: static
    keys only when `access_key`/`secret_key` are set, otherwise the default chain). Vault 2.2.1 ships
    `aws-sdk-go-v2/config v1.32.37`, far above the minimum.
  - v1 path (Vault 1.15.5 to 2.1.2): the wrapper uses `awsutil v0.3.0`, which ends the chain with
    `defaults.RemoteCredProvider`; `aws-sdk-go v1.55.8`'s implementation honours `AWS_CONTAINER_CREDENTIALS_FULL_URI`,
    `AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE` and the EKS agent address 169.254.170.23. Minimum Vault version for this path:
    1.15.5 (first tag whose go.mod has `aws-sdk-go >= v1.47.11`: v1.49.22; v1.15.4 has v1.44.331). verified at
    https://raw.githubusercontent.com/hashicorp/vault/v1.15.5/go.mod. There is no Vault changelog entry or issue that
    "added Pod Identity support"; it is inherited from the AWS SDK. Treat the minimum as derived, not announced.
  - NOT run-tested on a cluster. Test: start one pod with the association and check the log for `seal "awskms"` success,
    or `vault status` showing `Seal Type awskms`. Credential order caveat: credentials earlier in the chain (env keys,
    shared file, web identity from IRSA) win over Pod Identity (AWS doc above), so do not set `AWS_ACCESS_KEY_ID` and do not
    annotate the ServiceAccount with `eks.amazonaws.com/role-arn`.
- IAM actions on the KMS key: `kms:Encrypt`, `kms:Decrypt`, `kms:DescribeKey`. verified at
  https://github.com/hashicorp/web-unified-docs/blob/main/content/vault/v2.x/content/docs/configuration/seal/awskms.mdx
  (rendered: https://developer.hashicorp.com/vault/docs/configuration/seal/awskms), 2026-11-05. They can be granted by the
  role policy, the key policy or KMS grants. The Pod Identity role needs the trust principal `pods.eks.amazonaws.com` with
  `sts:AssumeRole` and `sts:TagSession`; the exact trust document is not re-verified here (the repo's `infra/aws` already
  has Pod Identity roles, copy that pattern).
- Seal stanza keys (same doc): `region` (default `us-east-1` if nothing else resolves it, so always set it), `kms_key_id`
  (key ID, ARN or `alias/...`), `endpoint` (e.g. VPC endpoint), `access_key`, `secret_key`, `session_token`,
  `shared_creds_filename`, `shared_creds_profile`, `role_arn`, `web_identity_token_file`, `role_session_name`, `disabled`.
  Env alternatives: `VAULT_SEAL_TYPE=awskms`, `VAULT_AWSKMS_SEAL_KEY_ID`, `AWS_REGION`, `AWS_KMS_ENDPOINT`.
  The doc page still says `access_key`/`secret_key` are `<required>`; the code path above shows they are optional.
  Minimal stanza:
  ```hcl
  seal "awskms" {
    region     = "eu-central-1"
    kms_key_id = "alias/ryft-vault-unseal"   # or key ID/ARN
  }
  ```
- Seal wrap is Enterprise only; on CE the KMS key is needed to unseal at start-up (every pod restart), not for each read.
  Key rotation (automatic or manual) is supported; never disable or delete old key versions (same doc).

## 4. `disable_mlock` and Pod Security `restricted`

- `disable_mlock` is **required** to be set explicitly for integrated storage; there is no default. CHANGELOG 1.20.0:
  "disable_mlock configuration option is now required for integrated storage and no longer has a default... Vault server
  will fail to start" ([GH-29974]). verified at https://github.com/hashicorp/vault/blob/main/CHANGELOG.md (section 1.20.0)
  and https://developer.hashicorp.com/vault/docs/configuration (`disable_mlock (bool: <required>)`), 2026-11-05.
- Recommendation for Raft: `disable_mlock = true` ("strongly recommended", BoltDB memory maps interact badly with
  `mlock`); also "strongly recommended to set disable_mlock to true, and to disable memory swapping" on the Raft
  storage page. verified at `.../docs/configuration/index.mdx` and `.../docs/configuration/storage/raft.mdx`.
- Containers: the official image no longer has the `IPC_LOCK` capability. 2.0.1 (2026-05-19) set `cap_ipc_lock` at build
  time; 2.0.2 (2026-06-05) removed it again ("Vault in containers will no longer be able to call `mlock()`. Operators
  should set `disable_mlock = true`"). The docs list this as breaking for 2.0.2+ (also backported to 1.21.7, 1.20.12,
  1.19.18, which are Enterprise-only builds). verified at CHANGELOG sections 2.0.1/2.0.2/2.2.0 and
  https://github.com/hashicorp/web-unified-docs/blob/main/content/vault/global/partials/important-changes/new-behavior/ipc_lock-removed.mdx.
- Effect on Pod Security `restricted`: `restricted` requires `capabilities.drop: [ALL]` and allows only
  `NET_BIND_SERVICE` to be added. With `disable_mlock = true` and the 2.2.1 image no `IPC_LOCK` is needed, so the pod can
  be `restricted`-compliant (with the securityContext in section 2). The chart still sets `SKIP_SETCAP=true` in the
  container env, which stops the entrypoint from calling `setcap` (non-root runs ignore it anyway; entrypoint log line
  "Container is running as non-root user, ignoring SKIP_SETCAP"). Docs advise swap-disabled or encrypted-swap nodes. EKS
  managed nodes run without swap by default (general knowledge, not verified here; confirm on a test node, not production).

## 5. TLS certificate reload

- Two mechanisms on the TCP listener (CE source and docs):
  1. **SIGHUP**: `tls_cert_file` and `tls_key_file` are marked `reloads-on-SIGHUP`; the paths used are those present at
     start-up (changing the path needs a restart). The key passphrase must stay the same. verified at
     https://github.com/hashicorp/web-unified-docs/blob/main/content/vault/v2.x/content/docs/configuration/listener/tcp/index.mdx
     (rendered: https://developer.hashicorp.com/vault/docs/configuration/listener/tcp), 2026-11-05.
  2. **`tls_reload_interval`** (new in 2.2.0, "Automatic TLS Certificate Reloading", CHANGELOG 2.2.0, 2026-10-15):
     duration string such as `"30s"`, min `1s`, max `24h`, unset/`0` = off. Vault re-reads the files at that interval and
     reloads when the content changed, without restart or SIGHUP. Applied only at start-up (changing it needs a restart).
     The docs' Kubernetes example uses `/vault/userconfig/vault-tls/tls.crt` with `tls_reload_interval = "30s"`.
     The option is in the public CE code (`internalshared/configutil/listener.go` in tag v2.2.1 defines
     `TLSReloadInterval`, min 1s, max 24h), so it is not Enterprise-only.
- A Kubernetes Secret volume (no `subPath`) is refreshed by the kubelet in place after the Secret changes (delay up to
  about a minute); `extraVolumes` mounts have no `subPath`. This is general Kubernetes behaviour, not verified in this
  session.
- The chart has no cert-reload mechanism of its own (no sidecar, no checksum rollout of the TLS secret). The only
  related hook is `server.shareProcessNamespace` for a sidecar that sends SIGHUP. On 2.2.x use `tls_reload_interval`.
  Raft peers also use the same certificate for `retry_join` (`leader_client_cert_file`): the reload covers the listener, the
  retry_join client certificate is read when joining.

## 6. Kubernetes auth method (Vault running in the cluster)

All verified at https://developer.hashicorp.com/vault/docs/auth/kubernetes and
https://developer.hashicorp.com/vault/api-docs/auth/kubernetes (sources: `.../docs/auth/kubernetes.mdx`,
`.../api-docs/auth/kubernetes.mdx` in web-unified-docs), 2026-11-05. Vault 2.2.1 bundles `vault-plugin-auth-kubernetes v0.25.0`
(`go.mod`).

- Config for in-cluster Vault: `vault write auth/kubernetes/config kubernetes_host=https://$KUBERNETES_SERVICE_HOST:$KUBERNETES_SERVICE_PORT`.
  `kubernetes_host` is the only required parameter.
- `token_reviewer_jwt` and `kubernetes_ca_cert` are optional in a pod: they default to the pod's own service account token
  (`/var/run/secrets/kubernetes.io/serviceaccount/token`, re-read periodically, so short-lived tokens work) and CA
  (`.../ca.crt`). This is the docs' recommended option (needs Vault 1.9.3+). `disable_local_ca_jwt` (default `false`)
  turns this off; with it `true` and no CA set, the system trust store is used.
- `disable_iss_validation`: **deprecated**, default **`true`** (issuer validation off); `issuer` also deprecated. `true`
  is the recommended value for all clusters. The Terraform provider still exposes both. The repo comment "(the Vault >=
  1.9 default)" is consistent with this.
- `use_annotations_as_alias_metadata` (default `false`): when `true`, annotations on the client's service account with the
  prefix `vault.hashicorp.com/alias-metadata-` (values up to 512 characters) become alias metadata, usable in templated
  policies as `identity.entity.aliases.<mount accessor>.metadata.<key>`. **Vault needs permission to read
  ServiceAccounts** from the Kubernetes API for this; `system:auth-delegator` does NOT grant `get serviceaccounts`, so an extra
  ClusterRole/Binding is needed if you enable it. Provider needs Vault 1.16+ for the attribute.
- `system:auth-delegator`: required, because the reviewer identity calls `TokenReview`. With the local-token mode the
  reviewer is Vault's own service account. The chart creates it by default: `server.authDelegator.enabled: true` renders
  ClusterRoleBinding `<release>-server-binding` -> ClusterRole `system:auth-delegator`, subject `ServiceAccount/<server sa>`
  (render-tested with chart 0.34.1, release `vault`: `vault-server-binding`). Note for Argo CD: this is a cluster-scoped object
  and the repo narrowed Argo CD's ClusterRoles (admission Step 0); allow ClusterRoleBinding creation for the vault app or create
  the binding in `infra/aws/kubernetes` / Terraform and set `server.authDelegator.enabled: false`.
- Role fields (API doc): `name`, `bound_service_account_names` (required, `*` allowed), `bound_service_account_namespaces`,
  `bound_service_account_namespace_selector` (needs namespace read permission, no `matchExpressions`), `audience` (default
  empty; "Audience claim to verify in the JWT"), `alias_name_source` (`serviceaccount_uid` default, or
  `serviceaccount_name`) plus the common token fields (`token_ttl`, `token_max_ttl`, `token_policies`, `token_period`,
  `token_bound_cidrs`, `token_explicit_max_ttl`, `token_no_default_policy`, `token_num_uses`, `token_type`).
- In Terraform the role resource uses `audience`, not `token_audiences` (`token_audiences` belongs to JWT/OIDC roles).

## 7. License and support

- Licence: Business Source License 1.1. The repo `LICENSE` at tag v2.2.1 names the **Licensor as International Business
  Machines Corporation (IBM)** ("Licensed Work: Vault Version 1.15.0 or later. The Licensed Work is (c) 2024 IBM Corp.").
  Change Date: four years after each version's publication; Change License: MPL 2.0. verified at
  https://raw.githubusercontent.com/hashicorp/vault/v2.2.1/LICENSE, 2026-11-05. (HashiCorp relicensed from MPL 2.0 to BUSL
  with 1.14.0, 2023-08; the licence text here says 1.15.0 or later; the chart repo stays MPL-2.0.)
- Additional Use Grant, summarised: production use is allowed unless it includes offering the Licensed Work to third parties
  on a hosted or embedded basis to compete with IBM's paid version(s). A "competitive offering" is a Product offered to third
  parties on a paid basis (including through paid support) that significantly overlaps with the capabilities of IBM's paid
  versions. "Hosting or using the Licensed Work(s) for internal purposes within an organization is not considered a
  competitive offering", and the organisation includes affiliates under common control. Non-paid Products are not
  competitive. HashiCorp's FAQ is declared binding (https://www.hashicorp.com/license-faq; its answer text did not render in my
  fetch, so I rely on the licence text itself).
- Practical consequence for RYFT: running CE for the company's own services (secrets for api, ingest, scoring, notify) is
  internal use and permitted in production with no fee. It would only matter if RYFT sold a product that wraps or embeds
  Vault as a secrets-management/PKI offering. Each release converts to MPL 2.0 four years after publication.
- 2025/2026 changes: the licensor and copyright moved from HashiCorp to IBM; source headers read "Copyright IBM Corp."
  and the BUSL text names IBM. Version numbering jumped from 1.21 to **2.0.0** (2026-04-14); the release notes state the
  2.x line adds native AI-agent support (Enterprise) and IBM licence integration (Enterprise-only `license_entitlement`
  stanza). The Enterprise licence page notes commercial keys issued after September 2025 terminate 60 days after expiry
  (Enterprise only; irrelevant to CE). verified at https://developer.hashicorp.com/vault/docs/updates/release-notes and
  https://developer.hashicorp.com/vault/docs/license, 2026-11-05.
- Release cadence and EOL for CE: CE gets releases, but only the **latest minor** is maintained. "Community versions end
  when the next release occurs"; extended maintenance exists only for Enterprise LTS. verified at
  https://endoflife.date/hashicorp-vault (table: 2.2 supported, 2.1 ended 2026-10-14, 2.0 ended 2026-08-31, 1.21 ended
  2026-04-13 with last patch 1.21.4 on 2026-03-04), 2026-11-05. The CHANGELOG corroborates it: sections "1.21.5 Enterprise"
  through "1.21.13 Enterprise" and "1.20.5 Enterprise" onward contain no CE builds, and Docker Hub `hashicorp/vault` has no
  1.21.x tag after 1.21.4 (2026-03-05). endoflife.date is a third-party tracker; HashiCorp itself publishes no CE support
  page (the release-notes page has an empty "Feature deprecations and EOL" table and says only "We encourage you to upgrade
  to the latest release"). Practical rule: budget a minor upgrade about every 6 to 20 weeks (2.0 on 2026-04-14, 2.1 on 2026-09-01,
  2.2 on 2026-10-15) and track patch releases for security fixes (2.1.2 and 2.2.1 both carried a root-policy alias fix).
  Release 2.0.1 CE briefly lacked some non-Linux-amd64/arm64 artifacts (docs note); container images were unaffected.

## 8. Raft snapshots

- `vault operator raft snapshot save <file>` calls `GET /v1/sys/storage/raft/snapshot` (read operation); required capability
  on `sys/storage/raft/snapshot` is **`read`**. Restore is `POST` on the same path (update operation): capability
  **`update`**. `sys/storage/raft/snapshot-force` (`update`) bypasses the seal-consistency check. None of these are in
  the sudo-protected paths list (only `storage/raft/snapshot-auto/config/*` is, and automated snapshots are Enterprise).
  verified at https://github.com/hashicorp/vault/blob/v2.2.1/vault/logical_system_raft.go (ReadOperation = snapshot save;
  UpdateOperation = restore) and `.../vault/logical_system.go` (root paths), plus
  https://developer.hashicorp.com/vault/api-docs/system/storage/raft, 2026-11-05. Policy:
  ```hcl
  path "sys/storage/raft/snapshot" { capabilities = ["read"] }          # backup
  # restore (break-glass only):  capabilities = ["update"]
  ```
  Snapshot save must run against the active node (or a standby that forwards; `VAULT_ADDR` should point at `vault-active`).
  "Vault does not support snapshots for deployments that only use raft for high-availability storage."
- Automated snapshot schedules (`sys/storage/raft/snapshot-auto/*`) are an Enterprise feature; on CE run a CronJob that
  logs in with Kubernetes auth, calls `snapshot save` and ships the file to encrypted storage.
- Restore with auto-unseal: the snapshot carries a sealed hash; restore checks it against the current seal. With an
  auto-unseal cluster a mismatch returns "possibly the snapshot is using a different autoseal key; use the snapshot-force API to
  bypass this check" (source line in `handleStorageRaftSnapshotWrite`). So restore into a cluster that uses the **same KMS
  key** (and the same key material/versions, hence never delete old KMS key versions). With a different key, only
  `snapshot-force` works; what unseal material is then needed afterwards is not verified here. The restore command returns
  once the snapshot is accepted; wait for "post-unseal setup complete" and "vault is unsealed" in the logs (docs note). Restore
  tests belong in a throw-away cluster, never against production.

## 9. Telemetry

- `telemetry` stanza keys relevant to Prometheus (docs `configuration/telemetry`): `prometheus_retention_time` (default
  `"24h"`; `0` disables Prometheus), `disable_hostname` (recommended `true`), `enable_hostname_label`, `usage_gauge_period`
  (`"10m"`), `maximum_gauge_cardinality` (500), `metrics_prefix` (`"vault"`), `filter_default`, `prefix_filter`,
  `lease_metrics_epsilon`, `num_lease_metrics_buckets`, `add_lease_metrics_namespace_labels`, `add_mount_point_rollback_metrics`.
  The chart example uses `telemetry { prometheus_retention_time = "30s", disable_hostname = true }`. verified at
  https://developer.hashicorp.com/vault/docs/configuration/telemetry and chart `values.yaml` 0.34.1, 2026-11-05.
- Endpoint `GET /v1/sys/metrics?format=prometheus` (or `Accept: prometheus/telemetry` / `application/openmetrics-text`).
  Default: needs a token with `read` and `list` on `sys/metrics`. Per the telemetry page: "The `/v1/sys/metrics` endpoint is
  only accessible on active nodes"; without unauthenticated access, community standby nodes redirect to the leader.
- Unauthenticated scraping: inside the listener, not in the global `telemetry` stanza:
  ```hcl
  listener "tcp" {
    address = "[::]:8200"
    cluster_address = "[::]:8201"
    telemetry { unauthenticated_metrics_access = true }   # bool, default false
  }
  ```
  verified at the TCP listener doc (`### telemetry parameters`), 2026-11-05. With it, each standby answers with its own
  metrics, so a per-pod scrape works. Restrict access with a NetworkPolicy on port 8200 (the metrics path shares the API port).
  A scrape token alternative: a policy `path "sys/metrics" { capabilities = ["read","list"] }` and `authorization` in the
  ServiceMonitor.
- Chart: `serverTelemetry.serviceMonitor.enabled` and `serverTelemetry.prometheusRules` exist (default off). The repo ships alert rules to Grafana Cloud; check how scraping is configured in `docs/OBSERVABILITY.md` before enabling (not read here).

## 10. Terraform `hashicorp/vault` provider

- Latest provider: **5.13.0**, released **2026-10-14**. verified at
  https://api.github.com/repos/hashicorp/terraform-provider-vault/releases and
  https://registry.terraform.io/v1/providers/hashicorp/vault/versions, 2026-11-05. Previous: 5.12.0 (2026-09-17), 5.11.0
  (2026-08-14), 5.10.1 (2026-06-26).
- Repo pin (`git show origin/main:infra/vault/terraform.tf` and `.terraform.lock.hcl`): `vault` `~> 5.0`, locked **5.13.0**;
  `kubernetes ~> 2.38` locked 2.38.0; `aws ~> 6.0` locked 6.69.1; `local ~> 2.5` locked 2.9.1; `random ~> 3.7` locked 3.9.1;
  Terraform `required_version = "1.16.6"`; state in HCP Terraform (org `tim-krebs-org`, workspace `ryft-prod-euc1-vault`).
  No provider bump is required.
- Resource arguments (provider docs at tag v5.13.0, `website/docs/r/*.html.md`, 2026-11-05):
  - `vault_auth_backend`: `type` (required), `path`, `description`, `local`, `disable_remount`, `tune{}`, `default_lease_ttl`,
    `max_lease_ttl`, `listing_visibility`, `token_type`, ... (`namespace` is Enterprise only).
  - `vault_kubernetes_auth_backend_config`: `backend`, `kubernetes_host` (required), `kubernetes_ca_cert`,
    `token_reviewer_jwt` (or write-only `token_reviewer_jwt_wo` + `token_reviewer_jwt_wo_version`), `pem_keys`, `issuer`,
    `disable_iss_validation`, `disable_local_ca_jwt`, `use_annotations_as_alias_metadata`. The provider text says "If not set
    the JWT used for login will be used to access the API"; for in-cluster Vault the server-side default is the local token
    (section 6). Set `disable_local_ca_jwt = false` explicitly and leave `token_reviewer_jwt` and `kubernetes_ca_cert` out.
    Whether the provider sends `disable_local_ca_jwt=false` when unset is not verified; run `vault read auth/kubernetes/config`
    after the first apply to confirm.
  - `vault_kubernetes_auth_backend_role`: `role_name`, `backend`, `bound_service_account_names` (required),
    `bound_service_account_namespaces`, `bound_service_account_namespace_selector`, `audience`, `alias_name_source`, plus the
    token arguments `token_ttl`, `token_max_ttl`, `token_policies`, `token_period`, `token_bound_cidrs`,
    `token_explicit_max_ttl`, `token_no_default_policy`, `token_num_uses`, `token_type`. There is no `token_audiences`.
  - `vault_mount`: `path`, `type`, `description`, `default_lease_ttl_seconds`, `max_lease_ttl_seconds`, `options`,
    `seal_wrap`, `local`, `listing_visibility`, `audit_non_hmac_*_keys`, `passthrough_request_headers`, ... For KV v2 use
    `type = "kv"` with `options = { version = "2" }` (the doc example also shows `type = "kv-v2"`). A separate
    `vault_kv_secret_backend_v2` resource (`mount`, `max_versions`, `cas_required`, `delete_version_after`) sets the engine
    config.
  - `vault_kv_secret_v2`: `mount` and `name` (required), `data_json` or write-only `data_json_wo` +
    `data_json_wo_version`, `cas`, `options`, `disable_read`, `delete_all_versions`, `custom_metadata{}`, ... Note that
    `data_json` is stored in Terraform state; the write-only variant avoids that (repo README already states the state is
    secret-bearing).
  - `vault_policy`: `name`, `policy`, `allow_overwrite` (default `true`).
- **Namespaces (blocking for the current Terraform):** `vault_namespace` ("available only with Vault Enterprise" in the
  provider docs) and the `namespace` argument on all resources, and `provider "vault" { namespace = ... }`, need Enterprise
  or HCP Vault. `/sys/namespaces` API is marked "enterprise-only" in the docs. The current `infra/vault` root
  (`namespaces.tf`, `kubernetes-auth.tf` with `namespace = vault_namespace.service[...].path_fq`) therefore cannot be applied
  to CE as written. On CE the equivalent is one Kubernetes auth mount per service at different paths (for example
  `kubernetes/api`, `kubernetes/ingest`), per-service KV mounts or path prefixes, and policies that confine each role to its
  own prefix. Whether the VSO manifests (`templates/vso.yaml.tftpl`, VaultAuth `namespace`) need the same change was not
  analysed here.
- The repo's current token reviewer is an off-cluster long-lived `vault-auth` service account token (needed for HCP). With
  in-cluster CE this can go away: one `system:auth-delegator` binding for Vault's own service account (chart default) and no
  `kubernetes_secret_v1` token.

## 11. Corrections to `deploy/vault/override-values.yaml` (observations only; the file was not changed)

- `server.image.tag: "2.2.1"` is valid for CE; fix the "Enterprise" comment. Consider a digest pin (section 1).
- Remove `global.resources` (not a chart key). Put resources under `server.resources` (already there).
- `server.extraVolumes` entry `kms-creds` is not needed with Pod Identity; drop it. `tls-ca`/`tls-server` mounts are fine
  (`/vault/userconfig/<name>`).
- `server.ha.raft.config` needs `disable_mlock = true`, `service_registration "kubernetes" {}`, a `seal "awskms"` stanza, an optional
  `telemetry` stanza and `tls_reload_interval`. `ha.replicas: 5` with required pod anti-affinity needs five schedulable
  nodes; the cluster is described as three nodes in the agent notes (not re-verified here), so use 3.
- `retry_join` lists three peers with `leader_api_addr = https://vault-N.vault-internal:8200`; with TLS the certificate must
  contain those DNS names (`*.vault-internal`, plus the Service names).
- `ui.serviceType: LoadBalancer` should be `ClusterIP`; `ui.externalPort`/`serviceNodePort` unnecessary.
- `injector.enabled: false`, `csi.enabled: false` are not in the file; the injector is on by default.
- `auditStorage.enabled: true` has no matching `vault_audit` resource yet.
- There is a trailing space after the closing `}` of `storage "raft"` (cosmetic).
