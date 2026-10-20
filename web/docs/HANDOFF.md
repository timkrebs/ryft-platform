# Handoff — what the other two folders need from web/

The site in `web/` replaces the ConfigMap site in `plattform/deploy/k8s/base/web/`. This file
has three parts: what **you** decide or click, the exact change for the **platform session**,
and what the **app session** needs (nothing for the URLs; a few optional items).
Nothing outside `web/` was edited.

---

## 1. For you

### Create the repository (needs your OK)

`web/` is a git repository with eight local commits on `main`, not yet on GitHub.

```bash
cd web
gh repo create timkrebs/ryft-web --private --source . --remote origin --push
```

`--private` as the brief says. `ryft-app` and `ryft-platform` are public; a private
repository costs Linux Actions minutes only (2,000 free a month on a personal plan). One run
should use roughly 10–15 minutes across its jobs — an estimate until the first run, which
the job timings in the Actions tab will confirm.

### Branch protection (needs your OK — run it yourself)

The same rules as the app: PRs only, the four checks required, linear history, no force
push, admins included.

```bash
gh api -X POST repos/timkrebs/ryft-web/rulesets --input - <<'JSON'
{
  "name": "main",
  "target": "branch",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] } },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "required_linear_history" },
    { "type": "pull_request", "parameters": {
        "required_approving_review_count": 0, "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": false, "require_last_push_approval": false,
        "required_review_thread_resolution": false, "allowed_merge_methods": ["squash"] } },
    { "type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": true,
        "required_status_checks": [
          { "context": "Checks" },
          { "context": "E2E, accessibility, links, Lighthouse" },
          { "context": "Secrets (gitleaks)" },
          { "context": "PR title" } ] } }
  ],
  "bypass_actors": []
}
JSON
gh repo edit timkrebs/ryft-web --enable-squash-merge --disable-merge-commit --disable-rebase-merge --delete-branch-on-merge
```

### Repository variables (public values, baked into the HTML)

```bash
gh variable set TESTFLIGHT_URL --repo timkrebs/ryft-web --body "https://testflight.apple.com/join/XXXXXXXX"
gh variable set SUPPORT_EMAIL  --repo timkrebs/ryft-web --body "support@ryftapp.de"   # the address you read
```

While unset, the TestFlight buttons are hidden and the invite and contact spots show a
`TODO(user)` marker, like the legal drafts.

### The image's visibility: decide before the first deploy

The first green `main` pushes `ghcr.io/timkrebs/ryft/web:<short-sha>`. That image contains
the three Satoshi WOFF2 files. The ITF Free Font License allows self-hosting them on your own
site but not making them available "through … repository, download service … publicly
accessible servers". A **public** package lets anyone pull the image and take the files out.

| Option | Cost |
| --- | --- |
| **A — private package (recommended)** | The cluster needs a pull secret in `ryft-web` (today the namespace holds none). A read-only token with `read:packages`, synced like the other secrets or created once by hand. Section 2 has the extra lines. |
| B — public package, like the platform's images | No secret. The licence risk above stays. |

Check what GitHub chose after the first push (Packages → `ryft/web` → Package settings).

### Before publishing the legal pages

Every legal page is a **draft**: a banner, `noindex`, and the platform's `TODO(user)`s. To
publish one (`content/legal/{en,de}/…`):

1. Fill every `TODO(user)` in **both** languages (controller, addresses, DPAs, the
   competent authority, minimum age, liability, law, support address, menu path, response
   time). Have it reviewed; the drafts say what the system does, they are not legal advice.
2. Delete the `portedFrom:` line (the test that compares the text with the platform's
   draft stops for that file) and set `status: published` and `effective: 'YYYY-MM-DD'`.
3. For a **new** privacy text: add `content/legal/{en,de}/privacy/<new-version>.mdx` (the old
   version stays published at `/privacy/<old-version>`), and set the platform's
   `PRIVACY_POLICY_VERSION` to the same date (platform session; everyone consents again).
   CI warns while the two differ (`npm run check:policy`).

### Brand findings in app/ and plattform/

`docs/BRAND_CHECK.md`: **nothing user-facing** carries the denylisted name; one user-facing
warning (a planned feature name in `app/docs/APP_STORE.md:64`); 48 internal hits in
comments and docs, which are your call. Both repositories are public, so the most visible
is `plattform/README.md:3`, the platform's front page on GitHub.

---

## 2. Paste into the platform session (plattform/)

> **Switch `ryft-web` from ConfigMaps to the image built in `timkrebs/ryft-web`.** The
> site, its nginx config, the legal texts and the AASA now live in that repository; the
> image is `ghcr.io/timkrebs/ryft/web:<short-sha>`, built and pushed by its CI on every
> green `main` (multi-arch, provenance, Trivy-clean). It serves on 8080 as uid 101, runs
> with a read-only root filesystem (writable `/tmp` and `/var/cache/nginx`, as today), and
> answers `/healthz`. Keep the Service, NetworkPolicy, Ingress, the `www` → apex redirect
> and the rate limit exactly as they are.
>
> 1. Apply this diff (from the `plattform/` root):
>
> ```diff
> diff -ru a/deploy/k8s/base/web/deployment.yaml b/deploy/k8s/base/web/deployment.yaml
> --- a/deploy/k8s/base/web/deployment.yaml
> +++ b/deploy/k8s/base/web/deployment.yaml
> @@ -24,13 +24,11 @@
>            labelSelector: { matchLabels: { app: web } }
>        containers:
>          - name: nginx
> -          image: nginxinc/nginx-unprivileged:1.27-alpine
> +          # The whole site (pages, nginx config, AASA) is one image, built and pushed by
> +          # timkrebs/ryft-web on every green main. The tag is pinned in overlays/eks.
> +          image: ghcr.io/timkrebs/ryft/web:latest
>            ports: [{ containerPort: 8080, name: http }]
>            volumeMounts:
> -            - { name: nginx, mountPath: /etc/nginx/conf.d, readOnly: true }
> -            - { name: site, mountPath: /usr/share/nginx/html/index.html, subPath: index.html, readOnly: true }
> -            - { name: site, mountPath: /usr/share/nginx/html/.well-known/apple-app-site-association, subPath: apple-app-site-association, readOnly: true }
> -            - { name: pages, mountPath: /usr/share/nginx/html/pages, readOnly: true }
>              - { name: tmp, mountPath: /tmp }
>              - { name: cache, mountPath: /var/cache/nginx }
>            readinessProbe:
> @@ -47,12 +45,6 @@
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
> diff -ru a/deploy/k8s/base/web/kustomization.yaml b/deploy/k8s/base/web/kustomization.yaml
> --- a/deploy/k8s/base/web/kustomization.yaml
> +++ b/deploy/k8s/base/web/kustomization.yaml
> @@ -1,27 +1,9 @@
>  apiVersion: kustomize.config.k8s.io/v1beta1
>  kind: Kustomization
> -# The apex site ryftapp.de: a static page and /.well-known/apple-app-site-association.
> -# Namespace-neutral; services/web puts it into ryft-web.
> +# The apex site ryftapp.de: one image from timkrebs/ryft-web (static pages, nginx config,
> +# /.well-known/apple-app-site-association). Namespace-neutral; services/web puts it into
> +# ryft-web. The legal texts live in that repository now (content/legal/).
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
> diff -ru a/deploy/k8s/overlays/eks/kustomization.yaml b/deploy/k8s/overlays/eks/kustomization.yaml
> --- a/deploy/k8s/overlays/eks/kustomization.yaml
> +++ b/deploy/k8s/overlays/eks/kustomization.yaml
> @@ -52,5 +52,6 @@
>      newTag: "21eb0fa"
>    - name: ghcr.io/timkrebs/ryft/notify
>      newTag: "21eb0fa"
> +  # web: the short SHA of a green main run in timkrebs/ryft-web, not of this repository.
>    - name: ghcr.io/timkrebs/ryft/web
> -    newTag: "21eb0fa"
> +    newTag: "<ryft-web short sha>"
> ```
>
>    Then delete what nothing references any more:
>    `git rm -r deploy/k8s/base/web/site deploy/k8s/base/web/default.conf`.
>    The tag: `gh run list -R timkrebs/ryft-web --workflow ci --branch main --status success --limit 1 --json headSha --jq ".[0].headSha[:7]"`
>    (quote it; the existing `newTag: "21eb0fa"` for web pointed at nothing, since no web
>    image existed).
>
> 2. **Only if the package stays private** (option A): a pull secret. Add to the
>    Deployment's pod spec `imagePullSecrets: [{ name: ghcr-pull }]`, and create the secret
>    in `ryft-web` from a token with only `read:packages`
>    (`kubectl -n ryft-web create secret docker-registry ghcr-pull --docker-server=ghcr.io --docker-username=timkrebs --docker-password=<token>`),
>    or sync it with VSO like the other namespaces. Note it in `docs/DECISIONS.md`: the
>    namespace now holds one secret.
> 3. `make k8s-validate && make k8s-check-images`, then `make k8s-apply-eks`, then the
>    checks from OPERATIONS → "Apex site" (all must stay 200, the AASA
>    `application/json` without a redirect):
>
> ```bash
> for p in "" privacy terms support impressum de/privacy de/impressum privacy/2026-10-20 day/2026-10-19 auth/callback; do
>   curl -s -o /dev/null -w "%{http_code} %{content_type} /$p\n" https://ryftapp.de/$p; done
> curl -sI https://ryftapp.de/.well-known/apple-app-site-association | grep -iE "^HTTP|content-type|location"
> curl -sI https://ryftapp.de/privacy/ | grep -iE "^HTTP|location"    # 301 → /privacy
> ```
>
> 4. Docs: OPERATIONS → "Apex site and apple-app-site-association" now says the site is the
>    `ryft-web` image (legal texts, AASA and nginx config in `timkrebs/ryft-web`;
>    `content/legal/`), a change there is a PR in that repository plus a tag bump here;
>    and "A new policy text" becomes: change `PRIVACY_POLICY_VERSION` in both ConfigMaps
>    **and** add `content/legal/{en,de}/privacy/<version>.mdx` in ryft-web (its CI warns
>    while they differ). ARCHITECTURE line ~300 and STATUS's apex row: same pointer.
>    OPERATIONS also says the app's entitlements list `applinks:ryftapp.de`; today
>    `app/Ryft/Ryft.entitlements` has no associated-domains key — please reword it as
>    "will list, once the app adds the capability".
> 5. Nothing changes in `docs/API.md`: `privacy_policy_url` stays
>    `https://ryftapp.de/privacy`, `privacy_policy_version` stays `2026-10-20`.
>
> Optional, not needed for the switch:
> - **A public JSON for the web's version check** (today ryft-web's CI reads your
>   ConfigMaps from a checkout of this public repository, which works):
>   `GET https://api.ryftapp.de/v1/legal` without auth →
>   `{"privacy_policy_version": "2026-10-20", "privacy_policy_url": "https://ryftapp.de/privacy"}`.
>   The web reads it when `PLATFORM_LEGAL_URL` is set.
> - **Brotli**: the image serves precompressed gzip (no brotli module in the official
>   nginx image). If wanted, Traefik's `compress` middleware on the `web` Ingress adds
>   brotli for responses that aren't already compressed.
> - **Visitor numbers without client-side tracking**: if the user ever wants page counts,
>   count them from the nginx access log of `ryft-web` (Grafana/Loki already scrape logs):
>   requests per path and status, no IPs stored past the existing log retention. Nothing
>   is added to the pages.

---

## 3. Paste into the app session (app/)

> **ryftapp.de moves to a new build; every URL the app uses stays the same.** No code
> change is needed. What the app links to (checked with `grep -rn ryftapp.de` on
> 2026-10-20):
>
> | URL | Where in the app | On the new site |
> | --- | --- | --- |
> | `https://ryftapp.de/privacy` | `Design/SafariView.swift:12`, `Features/Auth/WelcomeView.swift:78`, `CreateAccountView.swift:31`, `Localizable.xcstrings` (en, de) | 200, the draft privacy policy (version 2026-10-20) |
> | `https://ryftapp.de/terms` | `Design/SafariView.swift:13`, `CreateAccountView.swift:31`, `Localizable.xcstrings` | 200 |
> | `https://ryftapp.de/auth/callback` | `App/SupabaseConfig.swift:34` (`universalRedirectURL`) | 200, "Open this in RYFT", `no-store`, no script reads the URL |
> | `https://ryftapp.de/<path>` universal links | `Navigation/AppRoute.swift` (host `ryftapp.de`, paths below) | 200 "Open this in RYFT" for `/day/*`, `/sleep/*`, `/insights/*`, `/train/*`, `/auth/*` |
> | `https://ryftapp.de/.well-known/apple-app-site-association` | (Associated Domains, not added yet) | byte-identical to the platform's file, `application/json`, no redirect |
> | `https://ryftapp.de/support`, `https://ryftapp.de` (marketing) | `docs/APP_STORE.md`, `docs/TESTFLIGHT.md` (App Store Connect fields) | 200 |
>
> `/impressum` and `/de/impressum` are served too, though nothing in the app links them.
>
> Optional:
> - `AppRoute` also accepts `/train/exercise/<id>` and `/train/plans/<id>`, which the AASA
>   doesn't list (only `/train/exercises/*` and `/train/plan/*`). Nothing generates those
>   forms today, so they would open in Safari, not the app. The AASA lives in
>   `timkrebs/ryft-web` now (`public/.well-known/apple-app-site-association`); ask for them
>   there. Its test already checks every form `AppRoute.url` creates.
> - New captures for the website, in dark **and** light, as 1206 × 2622 simulator
>   screenshots in `docs/screenshots/` (`make screenshots` or as you capture them):
>   `today-{dark,light}.png` (the current Preview captures show the readiness number
>   missing or overlapping mid-animation, and a canvas artefact top right, so the site
>   draws its Today hero natively instead); `journal-{dark,light}.png` (the morning sheet
>   or the Behaviors section); `profile-data-{dark,light}.png` (Profile → Data with
>   "Export my data"). The website picks them up with `npm run screenshots`.
> - The in-app Licences screen and `ryftapp.de/licenses` list the same Swift packages
>   (ryft-web's CI fails if `Package.resolved` changes without its snapshot). If a
>   package is added, run `npm run licenses -- --sync` in ryft-web.
