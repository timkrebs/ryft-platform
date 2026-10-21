# Handoff — what the other two folders need from web/

The site in `web/` replaces the ConfigMap site in `plattform/deploy/k8s/base/web/`. This file
has three parts: what **you** decide or click, the exact change for the **platform session**,
and what the **app session** needs. Nothing outside `web/` was edited; the platform change is
a patch in this repository (`docs/handoff/platform-web-image.patch`) for the platform session
to apply.

As of 2026-10-21.

---

## 1. For you

### Done on 2026-10-21

- **The repository is public** (your choice). Before that: no personal details in the
  tree, history or PRs; gitleaks clean; the fonts are never committed.
- **`main` is protected:** ruleset `main` (id 25564428): PRs only, squash only, linear
  history, no force push or deletion, the four checks required and up to date, nobody
  bypasses it. Merge commits and rebase merges are off; branches go on merge.
- **`SUPPORT_EMAIL` = `support@ryftapp.de`**, from the next image on (support page, the beta
  page's "Ask for an invite"). `TESTFLIGHT_URL` stays unset: the beta is invite-only, so the
  "Open in TestFlight" buttons stay hidden.
- **The images** `ghcr.io/timkrebs/ryft/web:abfbe26` (the first green `main`) and `:f437747`
  (with the access-log fix, #9) are pushed. The package was still private at 16:30 (ghcr.io
  refused an anonymous pull token), until you make it public (below).

### Mail for ryftapp.de (later, by your choice)

`ryftapp.de` has no MX record today, so `support@ryftapp.de` receives nothing yet, and mail
to it bounces. The image from `f437747` on prints that address (support page, the beta
page's "Ask for an invite").

1. **A mail service** for the domain. It should receive mail for `support@ryftapp.de`,
   forward it to your private inbox, and let you **reply as** `support@ryftapp.de`: replies
   from your private address would show it to everyone who writes. It needs a data
   processing agreement, and ideally servers in the EU. It is named in privacy §5 (the
   marker is in ryft-web PR #10).
2. **The forwarding target** goes only into that service's settings, never into DNS text
   records, a repository or a variable. Avoid services that keep the target in a public
   TXT record.
3. **DNS:** give the platform session the records the service asks for (MX, SPF, DKIM,
   DMARC, a verification TXT). `ryftapp.de` is a Route 53 zone in `plattform/infra/aws/dns`
   (section 2, item 7).

### Grafana Cloud: exclude RYFT's logs (your choice)

Grafana Cloud → Fleet Management → the pod-log pipeline: exclude namespaces matching
`ryft-.*`. Then Explore → `{namespace=~"ryft-.*"}` over the last hour must return nothing.
The privacy text now says Grafana receives service metrics only (PR #10), which is true only
from then on.

### Make the image public (your decision, 2026-10-21)

github.com → your profile → Packages → `ryft/web` → Package settings
(`https://github.com/users/timkrebs/packages/container/ryft%2Fweb/settings`) → Danger Zone →
Change visibility → **Public**. Then the platform needs no pull secret (section 2, the quick
switch).

The image contains the three Satoshi WOFF2 files. The ITF Free Font License allows
self-hosting them on the site but not making them available "through … publicly accessible
servers", which a public image does: anyone can pull it and take the files out. You chose
speed over that; D14 records it. To undo it later: set the package back to private, and the
platform takes the pull-secret variant (section 2, after the quick switch).

### New web releases reach the cluster

This repository's CI opens the pin PR in `ryft-platform` with the platform's GitHub App
`ryft-deploy` (your choice; the job is ryft-web PR #11). It stays skipped until you set the
App here, **after** the platform has switched and changed `scripts/promote.sh` (section 2,
items 1–6):

```bash
gh variable set RYFT_DEPLOY_APP_CLIENT_ID --repo timkrebs/ryft-web --body "Iv23lidXNXq0jv463BaX"   # the same App as in ryft-platform
gh secret set   RYFT_DEPLOY_APP_KEY       --repo timkrebs/ryft-web < <the App's private key>.pem
```

Until then a web release is a one-line pin PR in `ryft-platform` (section 2 → 3).

### Legal pages

`docs/LEGAL_CHECKLIST.md`: what is answered, what is still open, and the facts with their
sources. Your answers of 2026-10-21 are in ryft-web PR #10; the pages stay drafts until every
`TODO(user)` is resolved and you say "publish".

### Brand findings in app/ and plattform/

`docs/BRAND_CHECK.md`: **nothing user-facing** carries the denylisted name; one user-facing
warning (a planned feature name in `app/docs/APP_STORE.md:64`); 48 internal hits in
comments and docs, which are your call. Both repositories are public, so the most visible
is `plattform/README.md:3`, the platform's front page on GitHub.

---

## 2. Paste into the platform session (plattform/)

> **Switch `ryft-web` to the image built in `timkrebs/ryft-web` (the quick switch, decided
> 2026-10-21).** The package `ghcr.io/timkrebs/ryft/web` is **public**, like your service
> images, so there is no pull secret and nothing changes in `infra/vault`, Argo CD's
> project, the CI or `check_registry.sh`. The image holds the site, its nginx config, the
> legal texts and the AASA; it serves on 8080 as uid 101 with a read-only root filesystem
> (writable `/tmp` and `/var/cache/nginx`, as today) and answers `/healthz`. Service,
> NetworkPolicy, Ingress, the `www` → apex redirect and the rate limit stay exactly as they
> are.
>
> 1. On a branch from `main`, apply this diff (from the `plattform/` root; the same file is
>    `../web/docs/handoff/platform-web-image-public.patch`, checked with `git apply --check`
>    against `003a1a9`; the rendered overlay passes `kubeconform -strict`, 54 resources), then
>    delete what nothing references any more:
>    `git rm -r deploy/k8s/base/web/site deploy/k8s/base/web/default.conf`.
>
> ```diff
> diff --git a/deploy/k8s/base/web/deployment.yaml b/deploy/k8s/base/web/deployment.yaml
> index 18d23d2..0a1bb9d 100644
> --- a/deploy/k8s/base/web/deployment.yaml
> +++ b/deploy/k8s/base/web/deployment.yaml
> @@ -24,13 +24,11 @@ spec:
>            labelSelector: { matchLabels: { app: web } }
>        containers:
>          - name: nginx
> -          image: nginxinc/nginx-unprivileged:1.27-alpine
> +          # The whole site (pages, nginx config, AASA) is one image, built and pushed by
> +          # timkrebs/ryft-web on every green main. The tag is pinned in overlays/eks.
> +          image: ghcr.io/timkrebs/ryft/web
>            ports: [{ containerPort: 8080, name: http }]
>            volumeMounts:
> -            - { name: nginx, mountPath: /etc/nginx/conf.d, readOnly: true }
> -            - { name: site, mountPath: /usr/share/nginx/html/index.html, subPath: index.html, readOnly: true }
> -            - { name: site, mountPath: /usr/share/nginx/html/.well-known/apple-app-site-association, subPath: apple-app-site-association, readOnly: true }
> -            - { name: pages, mountPath: /usr/share/nginx/html/pages, readOnly: true }
>              - { name: tmp, mountPath: /tmp }
>              - { name: cache, mountPath: /var/cache/nginx }
>            readinessProbe:
> @@ -47,12 +45,6 @@ spec:
>              requests: { cpu: 10m, memory: 16Mi }
>              limits: { memory: 64Mi }
>        volumes:
> -        - name: nginx
> -          configMap: { name: web-nginx }
> -        - name: site
> -          configMap: { name: web-site }
> -        - name: pages
> -          configMap: { name: web-pages }
>          - name: tmp
>            emptyDir: {}
>          - name: cache
> diff --git a/deploy/k8s/base/web/kustomization.yaml b/deploy/k8s/base/web/kustomization.yaml
> index a910fba..bce66b7 100644
> --- a/deploy/k8s/base/web/kustomization.yaml
> +++ b/deploy/k8s/base/web/kustomization.yaml
> @@ -1,27 +1,9 @@
>  apiVersion: kustomize.config.k8s.io/v1beta1
>  kind: Kustomization
> -# The apex site ryftapp.de: a static page and /.well-known/apple-app-site-association.
> -# Namespace-neutral; services/web puts it into ryft-web.
> +# The apex site ryftapp.de: one image from timkrebs/ryft-web (the static pages, the legal
> +# texts, nginx's config and /.well-known/apple-app-site-association). Namespace-neutral;
> +# services/web puts it into ryft-web.
>  resources:
>    - deployment.yaml
>    - service.yaml
>    - networkpolicy.yaml
> -configMapGenerator:
> -  # The hash suffix rolls the Deployment when the content changes.
> -  - name: web-site
> -    files:
> -      - site/index.html
> -      - apple-app-site-association=site/.well-known/apple-app-site-association
> -  - name: web-pages
> -    files:
> -      - site/pages/privacy.en.html
> -      - site/pages/privacy.de.html
> -      - site/pages/terms.en.html
> -      - site/pages/terms.de.html
> -      - site/pages/support.en.html
> -      - site/pages/support.de.html
> -      - site/pages/impressum.en.html
> -      - site/pages/impressum.de.html
> -      - site/pages/open.html
> -  - name: web-nginx
> -    files: [default.conf]
> diff --git a/deploy/k8s/overlays/eks/kustomization.yaml b/deploy/k8s/overlays/eks/kustomization.yaml
> index 991ea73..e7bd1e1 100644
> --- a/deploy/k8s/overlays/eks/kustomization.yaml
> +++ b/deploy/k8s/overlays/eks/kustomization.yaml
> @@ -52,5 +52,6 @@ images:
>      newTag: "0fd4256"
>    - name: ghcr.io/timkrebs/ryft/notify
>      newTag: "0fd4256"
> +  # web: the short SHA of a green main run in timkrebs/ryft-web, not of this repository.
>    - name: ghcr.io/timkrebs/ryft/web
> -    newTag: "1015bc7"
> +    newTag: "f437747"
> ```
>
> 2. `make k8s-validate` and `./scripts/check_registry.sh` (anonymous, as for the others:
>    `ghcr.io/timkrebs/ryft/web:f437747` must answer ✓).
> 3. PR, merge. Argo CD rolls `web` in wave 3 (the old pods serve until the new ones are
>    ready).
> 4. Check (all 200; the AASA `application/json` without a redirect; the CSP carries the
>    site's script hashes, the ConfigMap site's had none):
>
> ```bash
> kubectl -n ryft-web get pods -l app=web -o jsonpath='{.items[*].spec.containers[0].image}'; echo
> for p in "" privacy terms support impressum de/privacy de/impressum privacy/2026-10-20 day/2026-10-19 auth/callback invite/TEST; do
>   curl -s -o /dev/null -w "%{http_code} %{content_type} /$p\n" https://ryftapp.de/$p; done
> curl -sI https://ryftapp.de/.well-known/apple-app-site-association | grep -iE "^HTTP|content-type|location"
> curl -sI https://ryftapp.de/privacy | grep -i content-security-policy | grep -c "sha256-"   # 1
> curl -sI https://www.ryftapp.de/ | grep -i location                 # https://ryftapp.de/
> ```
>
> 5. Docs: OPERATIONS → "Apex site and apple-app-site-association": the site is the
>    `ryft-web` image (legal texts, AASA and nginx config in `timkrebs/ryft-web`,
>    `content/legal/`; the AASA also covers `/invite/*`); a change there is a PR in that
>    repository plus a tag bump here. "A new policy text": `PRIVACY_POLICY_VERSION` in both
>    ConfigMaps **and** `content/legal/{en,de}/privacy/<version>.mdx` in ryft-web.
>    `overlays/eks/kustomization.yaml`'s comment "The packages are public" stays true.
>
> Later web releases: a one-line PR that moves web's `newTag` (item 3 below), until the
> promote job from ryft-web (item 6) is switched on.
>
> ---
>
> **Only if the web package goes private again:** the rest of this section (a Vault-synced
> pull secret, `docs/handoff/platform-web-image.patch`). Not needed for the quick switch.
>
> **Switch `ryft-web` from ConfigMaps to the image built in `timkrebs/ryft-web`, pulled with
> a secret from Vault.** The site, its nginx config, the legal texts and the AASA now live in
> that repository; the image is `ghcr.io/timkrebs/ryft/web:<short-sha>`, built and pushed by
> its CI on every green `main` (multi-arch, provenance, Trivy-clean). It serves on 8080 as
> uid 101 with a read-only root filesystem (writable `/tmp` and `/var/cache/nginx`, as today)
> and answers `/healthz`. The package is **private** (the Satoshi licence forbids offering
> the font files for download), so the pods pull with a `kubernetes.io/dockerconfigjson`
> Secret that the Vault Secrets Operator syncs, like every other credential. Keep the
> Service, NetworkPolicy, Ingress, the `www` → apex redirect and the rate limit
> (`overlays/eks/web.yaml`) exactly as they are.
>
> **The change is a patch:** `git apply ../web/docs/handoff/platform-web-image.patch` (from
> the `plattform/` root, on a branch from `main`), then
> `git rm -r deploy/k8s/base/web/site deploy/k8s/base/web/default.conf`. The ryft-web session
> checked it on a copy of this tree at `003a1a9`: `git apply --check` clean; `terraform test`
> in `infra/vault` 22 passed (2 new runs); `kubeconform -strict` on the rendered EKS overlay,
> the generated web operator objects, the new golden file and the Argo CD files: 65 valid;
> `scripts/check_argocd.sh`: 12 applications. Re-run all of it here (`make k8s-validate`).
>
> **1. What the patch does**
>
> - `deploy/k8s/base/web`: the Deployment runs `ghcr.io/timkrebs/ryft/web` (no tag in base;
>   `overlays/eks` pins it) with `imagePullSecrets: [{ name: ghcr-pull }]`; the three ConfigMap
>   mounts and volumes and the `configMapGenerator` go.
> - `infra/vault`: a service `web = { registry_pull = true, manage_namespace = false,
>   workload_kind = "" }`. `registry_pull` writes `kv/ghcr-pull` in `admin/ryft/web` with
>   Docker's config JSON for `ghcr.io` (user `ghcr_pull_user`, default `timkrebs`; token
>   `ghcr_pull_token`, sensitive, validated as a classic `ghp_…` token), lets only web's
>   policy read it, and generates a `VaultStaticSecret` `ghcr-pull` with
>   `destination.type: kubernetes.io/dockerconfigjson`. Web gets no `kv/config` and no
>   `ryft-secrets`; nothing restarts (the kubelet reads the Secret on the next pull). A
>   `check` warns while the token is empty. Golden file `tests/golden/web.vault.yaml`.
> - **Why `manage_namespace = false`:** `ryft-web` is the one namespace kustomize makes
>   (`deploy/k8s/services/web/namespace.yaml`), and `ryft-platform` syncs with
>   `prune: true`. Moving it into `infra/vault` would mean removing it from git, and Argo CD
>   would prune the namespace, and the site with it, unless every step went right. So the
>   namespace stays Argo CD's; `infra/vault` only creates its objects in it (service accounts
>   `vault-auth` and `web`, the reviewer token, the auth-delegator binding) and no
>   `kubernetes_namespace_v1` for web. The four namespace references in
>   `kubernetes-auth.tf` go through `local.k8s_ns`, which resolves to the same names for
>   every existing service, so their plan doesn't change.
> - `deploy/argocd/apps/vault-web.yaml` (wave 0, like the other `vault-*` apps) applies
>   `deploy/k8s/overlays/vault-namespaces/web`; `project.yaml` ignores the orphaned
>   `Secret ghcr-pull` (VSO makes it).
> - `scripts/check_registry.sh` authenticates when `GHCR_TOKEN` is set; the `manifests` job
>   passes `GITHUB_TOKEN` with `packages: read` (the package grants this repository Read: the
>   ryft-web handoff, section 1).
> - `Makefile` break-glass: the web namespace and its pull secret before the services.
>
> **2. Order**
>
> 1. The user has confirmed the package is private, given `ryft-platform` Read access and
>    put `ghcr_pull_token` into `infra/vault/terraform.tfvars` (ryft-web handoff, section 1);
>    mail for `support@ryftapp.de` works (item 7).
> 2. Branch, apply the patch, `git rm` as above.
> 3. `cd infra/vault && terraform test && terraform plan -out=tfplan`. Expect only
>    additions, all for web: the Vault namespace `ryft/web`, its Kubernetes auth mount,
>    config and role, `kv/` with `ghcr-pull`, the policy, the service accounts `vault-auth`
>    and `web` plus the token Secret in `ryft-web`, the ClusterRoleBinding
>    `ryft-web-vault-auth-delegator`, and the two generated files. **No change to any other
>    service** and no `kubernetes_namespace_v1`. Apply; restart the operator as usual; commit
>    `deploy/k8s/overlays/vault-namespaces/web`.
> 4. Pin the web tag in `overlays/eks/kustomization.yaml` (quoted; `"abfbe26"` on
>    2026-10-21, or newer):
>    `gh run list -R timkrebs/ryft-web --workflow ci --branch main --status success --limit 1 --json headSha --jq '.[0].headSha[:7]'`.
>    The current `newTag: "1015bc7"` for web pointed at nothing; no web image existed.
> 5. `make k8s-validate`, and the registry by hand:
>    `GHCR_TOKEN=$(gh auth token) ./scripts/check_registry.sh` (after
>    `gh auth refresh -h github.com -s read:packages`).
> 6. PR, merge. Argo CD: wave 0 `vault-web` syncs `ghcr-pull`, wave 3 rolls `web` (the old
>    pods serve until the new ones are ready). Then, by hand as always for that file:
>    `kubectl apply -f deploy/argocd/project.yaml`.
> 7. Check (all must stay 200; the AASA `application/json` without a redirect; the CSP now
>    carries the site's script hashes, the ConfigMap site's had none):
>
> ```bash
> kubectl -n ryft-web get secret ghcr-pull -o jsonpath='{.type}'; echo     # kubernetes.io/dockerconfigjson
> kubectl -n ryft-web get pods -l app=web -o jsonpath='{.items[*].spec.containers[0].image}'; echo
> for p in "" privacy terms support impressum de/privacy de/impressum privacy/2026-10-20 day/2026-10-19 auth/callback invite/TEST; do
>   curl -s -o /dev/null -w "%{http_code} %{content_type} /$p\n" https://ryftapp.de/$p; done
> curl -sI https://ryftapp.de/.well-known/apple-app-site-association | grep -iE "^HTTP|content-type|location"
> curl -sI https://ryftapp.de/privacy | grep -i content-security-policy | grep -c "sha256-"   # 1
> curl -sI https://ryftapp.de/privacy/ | grep -iE "^HTTP|location"    # 301 → /privacy
> curl -sI https://www.ryftapp.de/ | grep -i location                 # https://ryftapp.de/
> ```
>
> **3. A web release, until section 6 is in place:** a PR that changes only web's `newTag`
> (the short SHA of a green `main` run in ryft-web, quoted). `manifests` checks the tag
> exists; the merge deploys it. Rollback: revert that commit.
>
> **4. Logs out of Grafana Cloud (the user's decision, 2026-10-21):** the user excludes
> `ryft-.*` from pod-log collection in Fleet Management. The privacy text now names Grafana
> Labs for service metrics only. Update OPERATIONS → "Data protection (logs)" (no longer "the
> pod logs also reach Grafana Cloud") and STATUS (step 7's "by choice" line); D51's original
> intent stands. The minimum age stays 13 (`MinAge`); nothing changes there.
>
> **5. Docs:** OPERATIONS → "Apex site and apple-app-site-association" now says the site is
> the `ryft-web` image (legal texts, AASA and nginx config in `timkrebs/ryft-web`,
> `content/legal/`; the AASA also covers `/invite/*` since 2026-10-21), a change there is a
> PR in that repository plus a tag bump here, and the pods pull with `ghcr-pull`. OPERATIONS →
> "Namespaces and identities": a row for `ryft-web` (Deployment `web`, `admin/ryft/web`,
> holds the pull secret only; namespace from kustomize). "A new policy text": change
> `PRIVACY_POLICY_VERSION` in both ConfigMaps **and** add
> `content/legal/{en,de}/privacy/<version>.mdx` in ryft-web (its CI warns while they differ).
> ARCHITECTURE line ~300 and STATUS's apex row: the same pointer. A decision: the web image is
> private and pulled with a Vault-synced secret; `infra/vault` doesn't own `ryft-web`.
> Nothing changes in `docs/API.md`: `privacy_policy_url` stays
> `https://ryftapp.de/privacy`, `privacy_policy_version` stays `2026-10-20` until the user
> decides on a new version.
>
> **6. How new web tags reach the cluster: ryft-web's CI opens the pin PR (decided; the job
> is ryft-web PR #11).** After its image job has pushed and Trivy has passed, a `promote` job in ryft-web takes a
> token from the `ryft-deploy` App for `ryft-platform` only, sets web's `newTag` on a branch
> `deploy/web-<sha>`, opens `deploy: web <sha>` with the ryft-web commits since the running
> pin, closes an older `deploy/web-*` PR, and turns auto-merge on. Your checks run on it
> (`branches-ignore: deploy/**` already keeps them to one run), Argo CD deploys the merge, a
> revert rolls back. Why this way round and not `promote.sh` reading the newest tag from
> ghcr.io:
>
> - `promote` runs only on pushes to this repository's `main`, and only when `backend/`
>   changed, so a web-only release would wait for an unrelated backend change, or this
>   repository would need a schedule to poll;
> - "the newest tag" is not well defined: tags are short SHAs, so it means listing package
>   versions by date through the Packages API (with `read:packages` on a private package),
>   and ryft-web pushes the image **before** its Trivy step, so a poll could pick an image
>   that is about to fail;
> - one PR per web release keeps your loop: auto-merge on green, close to stop, revert to
>   roll back, and the commits listed.
>
> What it needs here: one line in `scripts/promote.sh`, so a backend promote doesn't close a
> web pin PR as "superseded" (they edit different lines and don't conflict):
>
> ```diff
> -for n in $(gh pr list --state open --json number,headRefName --jq '.[] | select(.headRefName | startswith("deploy/")) | .number'); do
> +for n in $(gh pr list --state open --json number,headRefName --jq '.[] | select((.headRefName | startswith("deploy/")) and (.headRefName | startswith("deploy/web-") | not)) | .number'); do
> ```
>
> and the user sets the App's client ID and key in ryft-web (its handoff, section 1), after
> the switch and this line. The App needs no installation on ryft-web; its token is for
> `ryft-platform`, where it already has contents and pull-request write.
>
> **7. Mail for ryftapp.de:** the site publishes `support@ryftapp.de` (support, privacy
> contact, Impressum), and the zone has no MX record today. Once the user has chosen a mail
> service, add the records it asks for (MX, SPF, DKIM, DMARC, its verification TXT) to the
> `ryftapp.de` zone in `infra/aws/dns`. The forwarding target is the user's private address:
> it must not appear in a DNS record, this repository or a variable. This should be live
> before the switch, since the new image prints the address.
>
> Optional, not needed for the switch:
>
> - **Brotli**: the image serves precompressed gzip (no brotli module in the official nginx
>   image). Traefik's `compress` middleware on the `web` Ingress could add brotli.
> - **A public JSON for the web's version check**: `GET https://api.ryftapp.de/v1/legal`
>   without auth → `{"privacy_policy_version": "2026-10-20", "privacy_policy_url":
>   "https://ryftapp.de/privacy"}`. ryft-web reads it when `PLATFORM_LEGAL_URL` is set; until
>   then its CI reads your ConfigMaps from a checkout, which works.

---

## 3. Paste into the app session (app/)

> **ryftapp.de moves to a new build; every URL the app uses stays the same.** What the app
> links to (checked against `ryft-app` `main`, 2026-10-21):
>
> | URL | Where in the app | On the new site |
> | --- | --- | --- |
> | `https://ryftapp.de/privacy` | `Design/SafariView.swift`, `Features/Auth/WelcomeView.swift`, `CreateAccountView.swift`, `Localizable.xcstrings` (en, de) | 200, the privacy policy (version 2026-10-20) |
> | `https://ryftapp.de/terms` | `Design/SafariView.swift`, `CreateAccountView.swift`, `Localizable.xcstrings` | 200 |
> | `https://ryftapp.de/auth/callback` | `App/SupabaseConfig.swift` (`universalRedirectURL`) | 200, "Open this in RYFT", `no-store`, no script reads the URL |
> | `https://ryftapp.de/<path>` universal links | `Navigation/AppRoute.swift` | 200 "Open this in RYFT" for `/day/*`, `/sleep/*`, `/insights/*`, `/train/*`, `/auth/*`, `/invite/*` |
> | `https://ryftapp.de/.well-known/apple-app-site-association` | (Associated Domains, not added yet) | `application/json`, no redirect; every path `AppRoute.url` creates, **now including `/invite/*`** (your `API_REQUESTS.md` → Universal links, 2026-10-21) |
> | `https://ryftapp.de/support`, `https://ryftapp.de` | `docs/APP_STORE.md`, `docs/TESTFLIGHT.md` (App Store Connect fields) | 200 |
>
> Once the platform has switched to the new image (it serves the AASA then), add
> `applinks:ryftapp.de` under Signing & Capabilities → Associated Domains, as your
> `API_REQUESTS.md` plans; ryft-web's test checks the AASA against `AppRoute` on every PR.
>
> **Requests from the legal review:**
>
> - **Minimum age:** the privacy policy and terms say 13, which is what the app accepts
>   today (`latestBirthYear`, `− 13`). Nothing to change.
> - **Saving workouts to Apple Health** (your beta feedback brief, section 3): the privacy
>   policy already says, in English and German: "If you turn on saving workouts, it also
>   writes your completed strength workouts to Apple Health (the type, start, end and energy,
>   if known; no heart rate)." If what ships differs (more fields, heart rate, a default other
>   than off), tell ryft-web before the release.
> - **"Profile → Report a problem"** is named in `docs/TESTFLIGHT.md:85`, but the app has no
>   such screen yet (`POST /v1/feedback` is in `APIClient` and `Models.swift`, nothing calls
>   it). The support page will name whatever exists when it is published: build the screen,
>   or change the doc to TestFlight's screenshot feedback.
>
> Optional:
>
> - New captures for the website, in dark **and** light, as 1206 × 2622 simulator
>   screenshots in `docs/screenshots/`: `today-{dark,light}.png` (the current Preview
>   captures show the readiness number missing or overlapping mid-animation, so the site
>   draws its Today hero natively); `journal-{dark,light}.png`; `profile-data-{dark,light}.png`
>   (Profile → Data with "Export my data"). The website picks them up with
>   `npm run screenshots`.
> - The in-app Licences screen and `ryftapp.de/licenses` list the same Swift packages
>   (ryft-web's CI fails when `Package.resolved` changes without its snapshot; synced for the
>   bump in ryft-app #7). If a package is added, run `npm run licenses -- --sync` in ryft-web.
