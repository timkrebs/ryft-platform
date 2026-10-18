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

```bash
cp deploy/k8s/secrets.example.yaml deploy/k8s/secrets.yaml
# set POSTGRES_PASSWORD, the same password inside DATABASE_URL, and API_KEY (openssl rand -hex 32)
kubectl apply -k deploy/k8s
kubectl -n ryft rollout status deploy/api deploy/ingest deploy/scoring
```

## Deploy with Vault (Vault Secrets Operator)

Prerequisites: the [Vault Secrets Operator](https://developer.hashicorp.com/vault/docs/platform/k8s/vso)
installed in the cluster, a Vault with a KV v2 engine at `secret/` and the Kubernetes
auth method at `kubernetes/`. If Vault is not at
`http://vault.vault.svc.cluster.local:8200`, edit `deploy/k8s/vault/vault-auth.yaml`
(`VaultConnection.spec.address`, and `caCertSecretRef` for TLS).

1. Write the secret (same keys as `secrets.example.yaml`):

   ```bash
   PG_PW=$(openssl rand -hex 24)
   vault kv put secret/ryft \
     API_KEY="$(openssl rand -hex 32)" \
     POSTGRES_PASSWORD="$PG_PW" \
     DATABASE_URL="postgres://ryft:${PG_PW}@postgres.ryft.svc.cluster.local:5432/ryft?sslmode=disable"
   ```

2. A read-only policy and a Kubernetes auth role bound to the `ryft-vault` service account:

   ```bash
   vault policy write ryft - <<'EOF'
   path "secret/data/ryft" { capabilities = ["read"] }
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

**Rotation.** The operator re-reads `secret/ryft` hourly and rollout-restarts
api, ingest and scoring when it changes. Rotating `API_KEY` means updating the app's
Settings too. `POSTGRES_PASSWORD` only takes effect on an empty data directory — to
rotate it, `ALTER USER ryft PASSWORD '…'` in Postgres first, then update both
`POSTGRES_PASSWORD` and `DATABASE_URL` in Vault.

## Network policy

`base/networkpolicy.yaml` denies all ingress in the namespace, then allows:

| To | From | Port |
|---|---|---|
| ingest, api | the Ingress controller: Traefik in `kube-system` (k3s) or ingress-nginx in `ingress-nginx` | 8080 |
| postgres | pods labelled `part-of: ryft` (the three services) and `app: pg-backup` (backup/restore) | 5432 |
| nats | pods labelled `part-of: ryft` | 4222 |
| ingest, scoring, api | namespace `monitoring` (Prometheus) | 8080 |

Adjust `ryft-allow-ingress-controller` for another controller, or uncomment the
`cloudflared` peer if a Cloudflare tunnel targets the services directly. Kubelet
probes come from the node and are allowed by the common CNIs. Egress is open.
Debugging from inside the namespace (e.g. `kubectl run psql …`) needs the label
`app: pg-backup` to reach Postgres.

## Scaling and disruptions

api and ingest run 2–4 replicas under a CPU HPA (70 % of the 50m request; needs
metrics-server) with a `PodDisruptionBudget` of `minAvailable: 1`, spread across nodes
when there are several. Their Deployments carry no `replicas`, so re-applying never
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
existing one both reproduce every row.

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
        - { name: PGHOST, value: postgres }
        - { name: PGUSER, value: ryft }
        - { name: PGDATABASE, value: ryft }
        - name: PGPASSWORD
          valueFrom: { secretKeyRef: { name: ryft-secrets, key: POSTGRES_PASSWORD } }
      volumeMounts: [{ name: backups, mountPath: /backups }]
  volumes:
    - name: backups
      persistentVolumeClaim: { claimName: pg-backups }
EOF
kubectl -n ryft wait --for=condition=Ready pod/pg-restore
kubectl -n ryft exec pg-restore -- ls -lh /backups

# 3. restore (drops and recreates every object in the dump)
kubectl -n ryft exec pg-restore -- pg_restore --clean --if-exists --no-owner -d ryft /backups/ryft-20261018T013000Z.dump

# 4. clean up and restart; the HPAs take api/ingest back to 2
kubectl -n ryft delete pod pg-restore
kubectl -n ryft scale deploy/api deploy/ingest deploy/scoring --replicas=1
```

To restore a dump from your machine instead, `kubectl -n ryft cp ./ryft-….dump
pg-restore:/backups/` before step 3 (the PVC must have room).

## Upgrading

Migrations run on boot (advisory lock, so several pods starting at once is safe).
New columns on `daily_scores` (readiness, sleep detail, strain detail, baselines) are
filled when a day is scored; rescore history once after upgrading to this version:

```bash
curl -fsS -X POST https://<host>/v1/recompute -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -d "{\"from\":\"$(date -v-90d +%F 2>/dev/null || date -d '-90 days' +%F)\",\"to\":\"$(date +%F)\"}"
```
