# RYFT — operations

How to deploy, keep secrets in Vault, what the network policy allows, and how to
back up and restore. Manifests are kustomize-only and applied by hand.

```
deploy/k8s/
  base/                everything except the Secret: namespace, config, Postgres, NATS,
                       the three services, Ingress, NetworkPolicies, PDBs, HPAs, backup CronJob
  kustomization.yaml   default overlay: base + secrets.yaml (git-ignored, from secrets.example.yaml)
  vault/               alternative overlay: base + VaultStaticSecret (Vault Secrets Operator)
```

Render without applying: `kubectl kustomize deploy/k8s/vault`. Validate:
`make k8s-validate` (needs `kubeconform`).

## Deploy with a plain Secret

The datastore and identity provider is Supabase ([`SUPABASE.md`](SUPABASE.md) has the
project checklist). Set the project ref in `deploy/k8s/base/configmap.yaml`, then:

```bash
cp deploy/k8s/secrets.example.yaml deploy/k8s/secrets.yaml
# DATABASE_URL = Supabase session pooler string; SUPABASE_SERVICE_ROLE_KEY = secret key (api only)
kubectl apply -k deploy/k8s
kubectl -n ryft rollout status deploy/api deploy/ingest deploy/scoring
```

**Fully self-hosted** (in-cluster Postgres, shared API key, nothing exposed):
`deploy/k8s/overlays/local-postgres` with its own `secrets.yaml` using the
`local-postgres` keys from the example. If an install from before this change ran
Postgres from `base/`, `kubectl apply` does not prune the old StatefulSet: delete it
(`kubectl -n ryft delete statefulset postgres`, after a backup) once the data lives in
Supabase.

## Deploy with Vault (Vault Secrets Operator)

Prerequisites: the [Vault Secrets Operator](https://developer.hashicorp.com/vault/docs/platform/k8s/vso)
installed in the cluster, a Vault with a KV v2 engine at `secret/` and the Kubernetes
auth method at `kubernetes/`. If Vault is not at
`http://vault.vault.svc.cluster.local:8200`, edit `deploy/k8s/vault/vault-auth.yaml`
(`VaultConnection.spec.address`, and `caCertSecretRef` for TLS).

1. Write the secrets (same keys as `secrets.example.yaml`). The admin key lives at its
   own path so only the api pods receive it:

   ```bash
   vault kv put secret/ryft \
     DATABASE_URL='postgresql://postgres.<ref>:<db-password>@aws-<n>-eu-central-1.pooler.supabase.com:5432/postgres?sslmode=require'
   vault kv put secret/ryft-admin SUPABASE_SERVICE_ROLE_KEY='sb_secret_…'
   ```

2. A read-only policy and a Kubernetes auth role bound to the `ryft-vault` service account:

   ```bash
   vault policy write ryft - <<'EOF'
   path "secret/data/ryft"       { capabilities = ["read"] }
   path "secret/data/ryft-admin" { capabilities = ["read"] }
   EOF
   vault write auth/kubernetes/role/ryft \
     bound_service_account_names=ryft-vault \
     bound_service_account_namespaces=ryft \
     audience=vault policies=ryft ttl=1h
   ```

3. Apply the Vault overlay instead of the default one:

   ```bash
   kubectl apply -k deploy/k8s/vault
   kubectl -n ryft get vaultstaticsecret ryft-secrets     # events show sync status
   kubectl -n ryft get secret ryft-secrets
   ```

   Pods wait in `CreateContainerConfigError` until the operator has created
   `ryft-secrets`, then start on their own.

Switching an existing install from `secrets.yaml` to Vault: the operator will not adopt
a Secret it did not create, so `kubectl -n ryft delete secret ryft-secrets` first
(running pods keep their env), then apply the overlay.

**Rotation.** The operator re-reads both paths hourly and rollout-restarts the pods
that use them. To rotate the database password, reset it in Supabase (Database →
Settings), then update `DATABASE_URL`; for the secret key, create a new one in
Supabase, update `secret/ryft-admin`, then revoke the old one.

## EKS (api.ryftapp.de)

The AWS side is Terraform in three roots, each applied by hand in order; the details,
cost and teardown are in [`infra/aws/README.md`](../infra/aws/README.md).

| Step | Command | Done when |
| --- | --- | --- |
| 1. Zone | `cd infra/aws/dns && terraform init && terraform apply` | `terraform output name_servers` lists four awsdns servers |
| 2. Delegate | GoDaddy → ryftapp.de → Nameservers → *I'll use my own* → those four | `dig NS ryftapp.de @a.nic.de +short` shows them |
| 3. Cluster | `cd infra/aws && terraform apply` | `kubectl get nodes` Ready; `kubectl get sc` has no unencrypted default |
| 4. Edge | `cd infra/aws/kubernetes && terraform apply` (`acme_email` in tfvars) | `kubectl get clusterissuer` Ready; Traefik Service has an NLB hostname; `kubectl get sc` shows `gp3-encrypted (default)` |
| 5. App | see below | `curl -sI https://api.ryftapp.de/v1/me` → `401`, certificate from Let's Encrypt |

Step 5 needs the Supabase project ([`SUPABASE.md`](SUPABASE.md)) and images on GHCR
(first CI run on GitHub). Then:

```bash
cp deploy/k8s/secrets.example.yaml deploy/k8s/overlays/eks/secrets.yaml   # real values; git-ignored
# set newTag in deploy/k8s/overlays/eks/kustomization.yaml to the CI commit (short SHA)
make k8s-validate
make k8s-apply-eks                                  # kubectl apply -k deploy/k8s/overlays/eks
kubectl -n ryft rollout status deploy/api deploy/ingest deploy/scoring
kubectl -n ryft get ingress,certificate             # certificate READY True (DNS-01, ~1–2 min)
dig +short api.ryftapp.de                           # NLB addresses (ExternalDNS)
curl -sI https://api.ryftapp.de/v1/me               # 401 without a token
```

The public host only routes `/v1/*`; `/healthz`, `/readyz` and `/metrics` stay internal.
Port 80 is not exposed at all, so clients must use `https://`. Don't run `make smoke`
against the public host: it writes synthetic days into the one real user. Use a
port-forward to a fresh install instead.

Certificates: cert-manager renews 30 days before expiry. To try issuance without
touching Let's Encrypt's production rate limits, point the Ingress annotation at
`letsencrypt-staging` first.

## Network policy

`base/networkpolicy.yaml` denies all ingress in the namespace, then allows:

| To | From | Port |
| --- | --- | --- |
| ingest, api | the Ingress controller: Traefik in `kube-system` (k3s default, and on EKS via `infra/aws/kubernetes`) or ingress-nginx in `ingress-nginx` | 8080 |
| postgres (`overlays/local-postgres`) | pods labelled `part-of: ryft` (the three services) and `app: pg-backup` (backup/restore) | 5432 |
| nats | pods labelled `part-of: ryft` | 4222 |
| ingest, scoring, api | namespace `monitoring` (Prometheus) | 8080 |

Adjust `ryft-allow-ingress-controller` for another controller, or uncomment the
`cloudflared` peer if a Cloudflare tunnel targets the services directly.

**Egress** (`ryft-egress`): the three services may reach cluster DNS (UDP/TCP 53), NATS
(4222) and TCP 443 / 5432 anywhere — Supabase Auth and the pooler. NetworkPolicy cannot
name hosts, so the ports are the restriction; the backup job is not restricted. Kubelet
probes come from the node and are allowed by the common CNIs. Egress is open.
Debugging from inside the namespace (e.g. `kubectl run psql …`) needs the label
`app: pg-backup` to reach Postgres.

## Scaling and disruptions

api and ingest run 2–4 replicas under a CPU HPA (70 % of the 50m request; needs
metrics-server) with a `PodDisruptionBudget` of `minAvailable: 1`, spread across nodes
when there are several. On a **single-node** cluster a drain blocks on the second pod
(the first has nowhere to reschedule): use `kubectl drain --disable-eviction`, or
`kubectl -n ryft delete pdb api ingest` before maintenance and re-apply afterwards. Their Deployments carry no `replicas`, so re-applying never
fights the HPA. scoring stays at one replica: it owns the 15-minute timer and processes
recompute ranges in order. Postgres and NATS are single-replica StatefulSets.

## Backups

`base/backup.yaml` runs `pg_dump --format=custom` nightly at 03:30 Europe/Berlin into
the `pg-backups` PVC (10 Gi) and deletes dumps older than 14 days. Files are named
`ryft-YYYYMMDDTHHMMSSZ.dump`; a dump is written as `.partial` and renamed when complete.

```bash
kubectl -n ryft create job --from=cronjob/pg-backup pg-backup-manual   # back up now
kubectl -n ryft logs job/pg-backup-manual                              # lists /backups
```

The PVC lives in the cluster, often on the same disk as Postgres. Copy dumps off it
(see the restore pod below: `kubectl -n ryft cp pg-restore:/backups/<file> ./<file>`).

### Restore

Verified against the compose stack: restoring into an empty database and over an
existing one both reproduce every row. Dumps hold the `public` schema only (no Supabase
`auth`/`storage`), so they restore into Supabase or plain Postgres alike; restoring a
plain-Postgres database into Supabase needs the RLS step from `SUPABASE.md`. Supabase
keeps its own daily backups as well.

Accounts deleted after the dump was taken come back with it. So keep the current
tombstones across the restore (steps 2b and 3b below). The api pods then erase those
accounts again when they start (their deletion reconciler runs at start-up).

```bash
# 1. stop writers (HPA does not scale a Deployment that is at 0)
kubectl -n ryft scale deploy/api deploy/ingest deploy/scoring --replicas=0

# 2. a pod with the backups mounted, allowed to reach Postgres
kubectl -n ryft apply -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata: { name: pg-restore, namespace: ryft, labels: { app: pg-backup } }
spec:
  restartPolicy: Never
  securityContext: { runAsNonRoot: true, runAsUser: 70, runAsGroup: 70, fsGroup: 70 }
  containers:
    - name: restore
      image: postgres:17-alpine
      command: ["sleep", "3600"]
      env:
        - name: DATABASE_URL
          valueFrom: { secretKeyRef: { name: ryft-secrets, key: DATABASE_URL } }
      volumeMounts: [{ name: backups, mountPath: /backups }]
  volumes:
    - name: backups
      persistentVolumeClaim: { claimName: pg-backups }
EOF
kubectl -n ryft wait --for=condition=Ready pod/pg-restore
kubectl -n ryft exec pg-restore -- ls -lh /backups

# 2b. save the current tombstones (deleted accounts)
kubectl -n ryft exec pg-restore -- sh -c 'psql "$DATABASE_URL" -c "\copy deleted_users TO /tmp/tombstones.csv CSV"'

# 3. restore (drops and recreates every object in the dump)
kubectl -n ryft exec pg-restore -- sh -c 'pg_restore --clean --if-exists --no-owner -d "$DATABASE_URL" /backups/ryft-20261018T013000Z.dump'

# 3b. put the tombstones back
kubectl -n ryft exec pg-restore -- sh -c 'psql "$DATABASE_URL" -v ON_ERROR_STOP=1 \
  -c "CREATE TEMP TABLE t (LIKE deleted_users)" -c "\copy t FROM /tmp/tombstones.csv CSV" \
  -c "INSERT INTO deleted_users SELECT * FROM t ON CONFLICT (id) DO UPDATE SET auth_deleted_at = COALESCE(deleted_users.auth_deleted_at, EXCLUDED.auth_deleted_at)"'

# 4. clean up and restart; the HPAs take api/ingest back to 2
kubectl -n ryft delete pod pg-restore
kubectl -n ryft scale deploy/api deploy/ingest deploy/scoring --replicas=1
```

To restore a dump from your machine instead, `kubectl -n ryft cp ./ryft-….dump
pg-restore:/backups/` before step 3 (the PVC must have room).

## Upgrading

From the scaffold: api and ingest no longer set `replicas` (the HPA owns it). Because the
old manifest did, the first `kubectl apply` drops the field and the Deployments briefly
fall back to 1 replica until the HPA scales them to 2 — harmless, but expect it.

Migrations run on boot (advisory lock, so several pods starting at once is safe).
New columns on `daily_scores` (readiness, sleep detail, strain detail, baselines) are
filled when a day is scored (`steps` and `active_kcal` too), and source
de-duplication (`SCORING.md`) changes strain and sleep for anyone with two devices. Rescore history once after upgrading to this
version:

```bash
curl -fsS -X POST https://<host>/v1/recompute -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"from\":\"$(date -v-90d +%F 2>/dev/null || date -d '-90 days' +%F)\",\"to\":\"$(date +%F)\"}"
```
