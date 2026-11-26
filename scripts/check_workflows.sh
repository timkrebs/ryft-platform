#!/usr/bin/env bash
# The workflow rules that actionlint cannot know (CI's `manifests` job). The workflows are PARSED
# (yq, mikefarah v4, the pinned release in CI), not grepped, so flow style, quoted keys,
# `environment : deploy`, YAML escapes in names and anchors read as GitHub reads them:
#   1. no workflow triggers on pull_request_target or workflow_run: they run with the base
#      repository's secrets and token on code a pull request influences;
#   2. an environment is named by exactly two jobs of deploy.yml, each its own: `deploy` runs in
#      `deploy` (only main may use it, the smoke user's secrets live there, and ryft-deployer binds
#      only its WIF principal set) and `site` in `site` (main only, no secrets; only site-deployer
#      binds its principal set, D233), never the other's; and RYFT_DEPLOY_APP_KEY (the key of EKS's
#      promote, gone since C1) is named nowhere (any case: secret names are case-insensitive);
#   3. a job-level `uses:` is a workflow of this repository (./.github/workflows/<file>.yml): an
#      external reusable workflow could name the environment, or run with our secrets;
#   4. no `secrets: inherit`, and `secrets` is only ever read as `secrets.<NAME>`: not
#      `secrets[format(...)]`, not `toJSON(secrets)`, not the bare context;
#   5. no test hook of the scripts (RYFT_HOOKS_NODE_DIRS, the hooks' Node lookup; the registry, render
#      and pin hooks went with their scripts in D3, D246) is named anywhere, and NOW or TODAY is no
#      environment variable and no assignment in a script: a workflow must not switch a check off;
#   6. the golangci-lint version of ci.yml's lint job equals GOLANGCI_LINT_VERSION in the Makefile;
#   7. deploy.yml (Cloud Run, ticket B2) runs only on a push to main or by hand; every job's `if`
#      requires vars.GCP_DEPLOY == 'on' (off, it does nothing) and uses no ||; no `if` (job or step)
#      calls a status function (always(), cancelled(), failure(), success(): without one GitHub
#      wraps the condition in success(), so a failed job or step stops everything after it) and
#      nothing sets continue-on-error; the tokens are exactly: ci (the ci-ok wait) actions: read,
#      deploy and site contents: read and id-token: write, site-build contents: read, every other
#      job (site-off, held) none. The deploy job needs `ci`
#      and runs only when it found main still at this commit (needs.ci.outputs.current), in
#      concurrency group `deploy` with cancel-in-progress false (a migrate or a rollout is never cut
#      off); its only action is actions/checkout by SHA, nothing in the file mounts docker.sock,
#      it has a scan step (the pinned Trivy, `"$TRIVY" image ... --input`), and no step up to and
#      including it names gcloud, the OIDC request variables, docker login or docker push (sign-in
#      and push come after the scan); every step that runs `gcloud run` or the smoke comes after the
#      step `tip` and its `if` starts with steps.tip.outputs.current == 'true' (no || outside
#      parentheses: && binds tighter and would undo the gate). The smoke (D286): the dispatch input is
#      `auth_preflight` (boolean, default true; no `smoke` input), the preflight runs on a push or with it
#      on, the bootstrap only on a dispatch with it off, both before migrate; the post-rollout
#      `make smoke-prod` has the tip gate alone (no input skips it), mints afresh (unsets
#      SMOKE_AUTH_FILE) and rolls back inside its own step (`update-traffic` from $RUNNER_TEMP/previous,
#      api, ingest, notify, scoring, then exit 1); the step that records the serving revisions
#      (`services describe` into $RUNNER_TEMP/previous) comes before migrate and the services; no
#      smoke step takes a secret. The site (C1): site-build (npm runs
#      code there) has no id-token and no environment, and its only actions are checkout,
#      setup-node and upload-artifact by SHA; site runs in concurrency group `site` with
#      cancel-in-progress false, its only actions are checkout, setup-node (no cache key, and
#      package-manager-cache: false) and download-artifact by SHA, and no step up to and including its step `tip` signs in (gcloud,
#      firebase, the OIDC request variables, GOOGLE_APPLICATION_CREDENTIALS); both need `ci`, and
#      their `if` has the top-level conjuncts vars.SITE_DEPLOY == 'on' and
#      needs.ci.outputs.current == 'true'; every step of `site` that names `firebase ... deploy`
#      or check-hosting comes after `tip` and its `if` starts with steps.tip.outputs.current ==
#      'true' (no top-level ||), and no other job of deploy.yml names `firebase ... deploy`;
#   8. only ci.yml defines a job with the id or name `ci-ok`: deploy.yml reads the ci-ok job of
#      ci.yml's own run, and another job of that name would read as that check.
# Comments are gone after parsing. Without yq it fails (exit 2): a check that cannot read the
# files must not pass them. Usage: scripts/check_workflows.sh [workflow-dir]  (default .github/workflows)
set -euo pipefail
cd "$(dirname "$0")/.."
dir=${1:-.github/workflows}
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -q mikefarah; then
  echo "::error title=workflow rule::check_workflows: yq (mikefarah, v4) is not installed, so the workflows cannot be parsed. CI installs the pinned release binary (sha256-checked); locally: brew install yq" >&2
  exit 2
fi
rc=0
fail() { echo "::error title=workflow rule::$*" >&2; rc=1; }
shopt -s nullglob
files=("$dir"/*.yml "$dir"/*.yaml)
[ "${#files[@]}" -gt 0 ] || { echo "check_workflows: no workflows in $dir" >&2; exit 2; }

read -r -d '' PY <<'PYEOF' || true
import json, re, sys

fname, base = sys.argv[1], sys.argv[2]
is_ci = base == "ci.yml"
is_deploy = base == "deploy.yml"
# The jobs that run in an environment, and which: each environment is its own WIF principal set (D233).
ENV_JOBS = {("deploy.yml", "deploy"): "deploy", ("deploy.yml", "site"): "site"}
raw = sys.stdin.read()
dups = []
def pairs(ps):  # yq prints a duplicate key twice; a JSON reader keeps one of them, which one differs between readers
    d = {}
    for k, v in ps:
        if k in d:
            dups.append(k)
        d[k] = v
    return d
dec = json.JSONDecoder(object_pairs_hook=pairs)
docs, i = [], 0
while i < len(raw):
    if raw[i].isspace():
        i += 1
        continue
    obj, i = dec.raw_decode(raw, i)
    docs.append(obj)
errs = []
def fail(m):
    errs.append(f"{fname}: {m}")
for k in dups:
    fail(f"the key '{k}' appears twice in one mapping: GitHub and other readers disagree about which one counts")
if len(docs) != 1:
    fail(f"{len(docs)} YAML documents: a workflow is exactly one")
    docs = docs[:1] or [None]
wf = docs[0]
if not isinstance(wf, dict):
    fail("not a mapping")
    wf = {}

HOOKS = ["RYFT_HOOKS_NODE_DIRS"]
word = lambda names: re.compile(r"(?<![A-Za-z0-9_])(" + "|".join(names) + r")(?![A-Za-z0-9_])")
HOOK_RE = word(HOOKS)
TIME_KEY = re.compile(r"^(NOW|TODAY)$")
TIME_ASSIGN = re.compile(r"(?<![A-Za-z0-9_])(NOW|TODAY)=")
KEY_RE = re.compile(r"ryft_deploy_app_key", re.I)
EXPR_RE = re.compile(r"\$\{\{(.*?)\}\}", re.S)
SECRETS_RE = re.compile(r"(?<![A-Za-z0-9_.\-])secrets(?![A-Za-z0-9_\-])", re.I)
SECRETS_OK = re.compile(r"secrets\s*\.\s*[A-Za-z_][A-Za-z0-9_\-]*", re.I)

# 1. triggers
if "on" not in wf:
    fail("no `on:` trigger")
else:
    on = wf["on"]
    names = [on] if isinstance(on, str) else on if isinstance(on, list) else list(on) if isinstance(on, dict) else None
    if names is None or not all(isinstance(n, str) for n in names):
        fail("`on:` is not a name, a list of names or a mapping")
    else:
        for n in names:
            if n in ("pull_request_target", "workflow_run"):
                fail(f"triggers on {n}: not allowed (secrets and write token with a pull request's influence)")

def check_text(s, path, where):
    job = path[1] if len(path) > 1 and path[0] == "jobs" else None
    if HOOK_RE.search(s):
        fail(f"{where} names a test hook of the scripts ({HOOK_RE.search(s).group(1)}): a workflow must not switch a check off")
    if TIME_ASSIGN.search(s):
        fail(f"{where} assigns NOW or TODAY (the scripts' test clocks)")
    if KEY_RE.search(s):
        fail(f"{where}: names RYFT_DEPLOY_APP_KEY, the key of EKS's promote, which is gone (C1): no workflow may read it (job: {job or 'workflow level'})")
    exprs = EXPR_RE.findall(s)
    if path and path[-1] == "if":
        exprs.append(s)
    for e in exprs:
        for m in SECRETS_RE.finditer(e):
            if not SECRETS_OK.match(e, m.start()):
                fail(f"{where}: `secrets` used other than as secrets.<NAME> (in `{e.strip()[:80]}`)")

def walk(node, path):
    where = "/".join(path) or "(root)"
    if isinstance(node, dict):
        for k, v in node.items():
            check_text(k, path + [k], where + "/" + k + " (key)")
            if TIME_KEY.match(k) and "env" in path[-1:]:
                fail(f"{where}: sets NOW or TODAY as an environment variable")
            if k == "secrets" and v == "inherit":
                fail(f"{where}: `secrets: inherit` passes every secret on")
            walk(v, path + [k])
    elif isinstance(node, list):
        for n, v in enumerate(node):
            walk(v, path + [str(n)])
    elif isinstance(node, str):
        check_text(node, path, where)

walk(wf, [])

jobs = wf.get("jobs", {})
if not isinstance(jobs, dict):
    fail("`jobs:` is not a mapping")
    jobs = {}
for jid, job in jobs.items():
    if not isinstance(job, dict):
        fail(f"job '{jid}' is not a mapping")
        continue
    if "uses" in job:
        u = job["uses"]
        if not (isinstance(u, str) and u.startswith("./.github/workflows/") and ".." not in u and re.fullmatch(r"\./\.github/workflows/[A-Za-z0-9_.\-]+\.ya?ml", u)):
            fail(f"job '{jid}' calls `uses: {u}`: a job-level uses must be a workflow of this repository (./.github/workflows/<file>.yml), never an external reusable workflow")
    if "environment" in job:
        e = job["environment"]
        name = e if isinstance(e, str) else e.get("name") if isinstance(e, dict) else None
        if (base, jid) not in ENV_JOBS:
            fail(f"job '{jid}' names an environment ({name!r}): only deploy.yml's deploy and site may (the smoke user's secrets and the WIF principal sets are scoped to them)")
        elif name != ENV_JOBS[(base, jid)]:
            fail(f"{jid} must run in the environment {ENV_JOBS[(base, jid)]!r}, not {name!r} (each environment is its own WIF principal set, D233)")
    # 8. the job deploy.yml waits for
    if (jid == "ci-ok" or job.get("name") == "ci-ok") and not (is_ci and jid == "ci-ok"):
        fail(f"job '{jid}' is (or is named) ci-ok: only ci.yml's required check may be, deploy.yml waits for that job of ci.yml's run")

def env_name(job):
    e = job.get("environment") if isinstance(job, dict) else None
    return e if isinstance(e, str) else e.get("name") if isinstance(e, dict) else None

for (b, jid), env in sorted(ENV_JOBS.items()):
    if base == b and env_name(jobs.get(jid)) != env:
        fail(f"the {jid} job does not run in `environment: {env}`")

# 7. deploy.yml
GATE = re.compile(r"(?<![A-Za-z0-9_.])vars\.GCP_DEPLOY\s*==\s*'on'")
# Function names in expressions are case-insensitive.
STATUS_FN = re.compile(r"(?<![A-Za-z0-9_.\-])(always|cancelled|failure|success)\s*\(", re.I)
CURRENT = re.compile(r"(?<![!A-Za-z0-9_.])needs\.ci\.outputs\.current\s*==\s*'true'")
TIP_GATE = re.compile(r"steps\.tip\.outputs\.current\s*==\s*'true'")
SCAN = re.compile(r"\$\{?TRIVY\}?\"?\s+image\b[^\n]*\s--input\b")
SIGNS_IN = re.compile(r"ACTIONS_ID_TOKEN_REQUEST|(?<![A-Za-z0-9_\-])gcloud(?![A-Za-z0-9_\-])|(?<![A-Za-z0-9_\-])docker\b[^\n;&|]*?\b(?:login|push)\b")
CHANGES_RUN = re.compile(r"(?<![A-Za-z0-9_\-])gcloud\s+run\s|(?<![A-Za-z0-9_\-])make\s+smoke-prod\b")
SMOKE_AUTH = re.compile(r"scripts/smoke_prod\.sh\s+--auth-(?:preflight|bootstrap)\b")
SMOKE_PREFLIGHT = re.compile(r"scripts/smoke_prod\.sh\s+--auth-preflight\b")
SMOKE_BOOTSTRAP = re.compile(r"scripts/smoke_prod\.sh\s+--auth-bootstrap\b")
# The `if` of each smoke step, exactly (D286): the preflight on a push or when the input is true, the bootstrap
# only on a dispatch with the input off, the post-rollout smoke on the tip gate alone, no input skips it.
PREFLIGHT_IF = "steps.tip.outputs.current == 'true' && (github.event_name != 'workflow_dispatch' || inputs.auth_preflight)"
BOOTSTRAP_IF = "steps.tip.outputs.current == 'true' && github.event_name == 'workflow_dispatch' && !inputs.auth_preflight"
SMOKE_IF = "steps.tip.outputs.current == 'true'"
UPDATE_TRAFFIC = re.compile(r"\bgcloud\s+run\s+services\s+update-traffic\b")
DESCRIBE = re.compile(r"\bgcloud\s+run\s+services\s+describe\b")
PREVIOUS_FILE = re.compile(r"\$\{?RUNNER_TEMP\}?/previous\b")
SMOKE_RUN = re.compile(r"\bmake\s+smoke-prod\b")
SMOKE_ENV_NAME = re.compile(r"SMOKE_(?:EMAIL|PASSWORD|TOKEN)|SUPABASE_ANON_KEY", re.I)
SMOKE_ENV_VALUE = re.compile(r"\bsecrets\s*(?:\.\s*SMOKE_[A-Za-z0-9_]*|\[\s*['\"]SMOKE_[A-Za-z0-9_]*)|\bSUPABASE_ANON_KEY\b", re.I)
GITHUB_ENV_WRITE = re.compile(r"GITHUB_ENV\b")
SMOKE_SECRET = re.compile(r"\bsecrets\s*(?:\.\s*[A-Za-z0-9_\-*]*|\[)|\bSUPABASE_ANON_KEY\b|\bSMOKE_(?:EMAIL|PASSWORD|TOKEN)\b", re.I)
ROLLOUT = re.compile(r"\bgcloud\s+run\s+(?:jobs\s+(?:replace|execute)|services\s+replace)\b")
CHECKOUT = re.compile(r"actions/checkout@[0-9a-f]{40}")
DEPLOY_PERMS = {"ci": {"actions": "read"}, "deploy": {"contents": "read", "id-token": "write"},
                "site-build": {"contents": "read"}, "site": {"contents": "read", "id-token": "write"}}
# The site (C1): the actions each job may use, by SHA; what signs in; what deploys or checks the live site.
SITE_ACTIONS = {"site-build": re.compile(r"actions/(checkout|setup-node|upload-artifact)@[0-9a-f]{40}"),
                "site": re.compile(r"actions/(checkout|setup-node|download-artifact)@[0-9a-f]{40}")}
# The CLI by name (firebase, firebase-tools@<v>), not a file like web/firebase.json or a host like firebasehosting.
FIREBASE_CLI = r"(?<![A-Za-z0-9_\-./])firebase(?:-tools)?(?![A-Za-z0-9_.])"
SITE_SIGNS_IN = re.compile(SIGNS_IN.pattern + "|" + FIREBASE_CLI + r"|GOOGLE_APPLICATION_CREDENTIALS")
FIREBASE_DEPLOY = re.compile(FIREBASE_CLI + r"[^\n]*?(?<![A-Za-z0-9_\-])deploy(?![A-Za-z0-9_\-])")
CHECK_HOSTING = re.compile(r"check-hosting")
CURRENT_TERM = re.compile(r"needs\.ci\.outputs\.current\s*==\s*'true'")
SITE_ON_TERM = re.compile(r"vars\.SITE_DEPLOY\s*==\s*'on'")

def top_level_or(expr):
    """A || outside parentheses and quoted strings ('' is a quote inside one)."""
    depth, i, quoted = 0, 0, False
    while i < len(expr):
        c = expr[i]
        if quoted:
            if c == "'":
                if expr[i + 1:i + 2] == "'":
                    i += 1
                else:
                    quoted = False
        elif c == "'":
            quoted = True
        elif c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
        elif depth <= 0 and expr.startswith("||", i):
            return True
        i += 1
    return False

def conjuncts(expr):
    """The terms of the top-level && (outside parentheses and quoted strings), stripped."""
    terms, depth, i, quoted, start = [], 0, 0, False, 0
    while i < len(expr):
        c = expr[i]
        if quoted:
            if c == "'":
                if expr[i + 1:i + 2] == "'":
                    i += 1
                else:
                    quoted = False
        elif c == "'":
            quoted = True
        elif c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
        elif depth <= 0 and expr.startswith("&&", i):
            terms.append(expr[start:i].strip())
            i += 2
            start = i
            continue
        i += 1
    terms.append(expr[start:].strip())
    return terms

def job_steps(job):
    steps = job.get("steps") if isinstance(job, dict) else None
    return [s if isinstance(s, dict) else {} for s in steps] if isinstance(steps, list) else None

def step_label(n, s):
    return f"step {n}" + (f" ({s['name']})" if isinstance(s, dict) and isinstance(s.get("name"), str) else "")

if is_deploy:
    on = wf.get("on")
    if not isinstance(on, dict) or set(on) != {"push", "workflow_dispatch"} or on.get("push") != {"branches": ["main"]}:
        fail("deploy.yml runs on exactly `push: {branches: [main]}` and `workflow_dispatch`")
    # The dispatch's inputs: `auth_preflight` (boolean, default true) replaced `smoke`; no input skips the post-rollout smoke.
    wd = on.get("workflow_dispatch") if isinstance(on, dict) else None
    ins = wd.get("inputs") if isinstance(wd, dict) and isinstance(wd.get("inputs"), dict) else {}
    ap = ins.get("auth_preflight")
    if not (isinstance(ap, dict) and ap.get("type") == "boolean" and ap.get("default") is True):
        fail("deploy.yml's workflow_dispatch needs the input `auth_preflight` (type boolean, default true), D286")
    if "smoke" in ins:
        fail("deploy.yml's workflow_dispatch has an input `smoke`: no input skips the post-rollout smoke (D286); `auth_preflight` only picks the preflight or the bootstrap")
    # D286: nothing hands the smoke helper a token, an email or a password behind the smoke steps' backs: not the
    # workflow's `env:`, not any job's, and no run script that writes a SMOKE_ name to $GITHUB_ENV (the helper
    # would use SMOKE_TOKEN instead of minting its own).
    env_scopes = [("the workflow's `env:`", wf.get("env"))] + \
        [(f"job '{jid}' `env:`", j.get("env")) for jid, j in jobs.items() if isinstance(j, dict)]
    for where, env in env_scopes:
        if not isinstance(env, dict):
            continue
        for k, v in env.items():
            m = SMOKE_ENV_NAME.fullmatch(str(k)) or SMOKE_ENV_VALUE.search(str(v))
            if m:
                fail(f"deploy.yml, {where}: `{k}`: {m.group(0)!r} is a smoke credential; the smoke authenticates with a Google ID token "
                     "minted by the job's own sign-in and takes none (no SMOKE_TOKEN, SMOKE_EMAIL, SMOKE_PASSWORD, "
                     "SUPABASE_ANON_KEY or secrets.*), D286")
    for jid, j in jobs.items():
        for n, s in enumerate(j.get("steps") if isinstance(j, dict) and isinstance(j.get("steps"), list) else []):
            r = s.get("run") if isinstance(s, dict) else None
            if isinstance(r, str) and GITHUB_ENV_WRITE.search(r) and re.search(r"SMOKE_", r):
                fail(f"deploy.yml's job '{jid}', {step_label(n, s)}: a run script that names SMOKE_* and $GITHUB_ENV: a smoke "
                     "credential (SMOKE_TOKEN, SMOKE_EMAIL, SMOKE_PASSWORD) never reaches a later step through the environment, D286")
    for jid, job in jobs.items():
        cond = job.get("if") if isinstance(job, dict) else None
        # The gate is a top-level conjunct: `vars.GCP_DEPLOY == 'on'` first, then only `&&` terms.
        if not (isinstance(cond, str) and GATE.match(cond.strip()) and "||" not in cond):
            fail(f"deploy.yml job '{jid}': its `if` must start with vars.GCP_DEPLOY == 'on' (and use no ||), is {cond!r}")
        if not isinstance(job, dict):
            continue
        # Every `if` and continue-on-error GitHub reads: the job's and its steps'.
        steps = job.get("steps") if isinstance(job.get("steps"), list) else []
        places = [(f"job '{jid}'", job)] + [(f"job '{jid}' {step_label(n, s)}", s) for n, s in enumerate(steps) if isinstance(s, dict)]
        for where, node in places:
            m = STATUS_FN.search(str(node.get("if", "")))
            if m:
                fail(f"deploy.yml {where}: its `if` calls the status function {m.group(1)}(): it would run after a failure or past "
                     f"the gate (without one GitHub adds success()); deploy.yml has none")
            if "continue-on-error" in node:
                fail(f"deploy.yml {where}: continue-on-error lets what comes after a failure run; deploy.yml has none")
        want = DEPLOY_PERMS.get(jid, {})
        have = job.get("permissions", None if jid in DEPLOY_PERMS else {})
        if have != want:
            if jid in ("deploy", "site"):
                fail(f"deploy.yml's {jid} job has exactly contents: read and id-token: write, has {have!r}")
            elif jid == "site-build":
                fail(f"deploy.yml's site-build job has exactly contents: read, without id-token (npm runs code there, and "
                     f"every step of a job with id-token: write can mint a token), has {have!r}")
            elif jid == "ci":
                fail(f"deploy.yml's ci job (the ci-ok wait) has exactly actions: read (ci.yml's run and its jobs), has {have!r}")
            else:
                fail(f"deploy.yml's job '{jid}' has no token (permissions: {{}} or none), has {have!r}")
    if "docker.sock" in json.dumps(wf):
        fail("deploy.yml names docker.sock: nothing in it talks to the Docker daemon from a container (Trivy reads saved images)")
    d = jobs.get("deploy")
    if not isinstance(d, dict):
        fail("deploy.yml has no job `deploy`")
    else:
        needs = d.get("needs")
        needs = [needs] if isinstance(needs, str) else needs
        if not isinstance(needs, list) or "ci" not in needs:
            fail("deploy.yml's deploy job must need `ci` (the wait for ci-ok)")
        if not (isinstance(d.get("if"), str) and CURRENT.search(d["if"])):
            fail(f"deploy.yml's deploy job: its `if` must have the conjunct needs.ci.outputs.current == 'true' (a superseded run deploys nothing), is {d.get('if')!r}")
        ci_job = jobs.get("ci")
        if not (isinstance(ci_job, dict) and isinstance(ci_job.get("outputs"), dict) and "current" in ci_job["outputs"]):
            fail("deploy.yml's ci job must set outputs.current (main is still at this commit)")
        c = d.get("concurrency")
        if not (isinstance(c, dict) and c.get("group") == "deploy" and c.get("cancel-in-progress") is False):
            fail(f"deploy.yml's deploy job runs in concurrency group `deploy` with cancel-in-progress: false, has {c!r}")
        steps = d.get("steps")
        if not isinstance(steps, list):
            fail("deploy.yml's deploy job has no list of steps")
            steps = []
        steps = [s if isinstance(s, dict) else {} for s in steps]
        runs = [s["run"] if isinstance(s.get("run"), str) else "" for s in steps]
        for n, s in enumerate(steps):
            if "uses" in s and not (isinstance(s["uses"], str) and CHECKOUT.fullmatch(s["uses"])):
                fail(f"deploy.yml's deploy job, {step_label(n, s)}: `uses: {s['uses']}`: actions/checkout by SHA is its only action "
                     "(no third-party code, or container, in the job that can mint a token)")
        scan = next((n for n, r in enumerate(runs) if SCAN.search(r)), None)
        if scan is None:
            fail("deploy.yml's deploy job has no scan step (the pinned Trivy reading the saved images: \"$TRIVY\" image ... --input)")
        else:
            for n in range(scan + 1):
                rest = json.dumps({k: v for k, v in steps[n].items() if k != "run"})
                m = SIGNS_IN.search(runs[n]) or SIGNS_IN.search(rest)
                if m:
                    fail(f"deploy.yml's deploy job, {step_label(n, steps[n])}: `{m.group(0)[:60]}` at or before the scan step: "
                         "nothing signs in, mints a token or pushes before all five images passed")
        tip = next((n for n, s in enumerate(steps) if s.get("id") == "tip"), None)
        if tip is None:
            fail("deploy.yml's deploy job has no step with id `tip` (main is still at this commit, right before migrate)")
        for n, r in enumerate(runs):
            if CHANGES_RUN.search(r) or SMOKE_AUTH.search(r):
                cond = steps[n].get("if")
                if not (tip is not None and n > tip and isinstance(cond, str) and TIP_GATE.match(cond.strip()) and not top_level_or(cond)):
                    fail(f"deploy.yml's deploy job, {step_label(n, steps[n])}: it changes Cloud Run or smokes, so it comes after the "
                         f"step `tip` and its `if` starts with steps.tip.outputs.current == 'true' (no || outside parentheses), is {cond!r}")
        pre_steps = [n for n, r in enumerate(runs) if SMOKE_PREFLIGHT.search(r)]
        boot_steps = [n for n, r in enumerate(runs) if SMOKE_BOOTSTRAP.search(r)]
        auth_steps = pre_steps + boot_steps
        smoke_steps = [n for n, r in enumerate(runs) if SMOKE_RUN.search(r)]
        rollout_steps = [n for n, r in enumerate(runs) if ROLLOUT.search(r)]
        services_steps = [n for n, r in enumerate(runs) if re.search(r"\bgcloud\s+run\s+services\s+replace\b", r)]
        if not (len(pre_steps) == len(boot_steps) == len(smoke_steps) == 1 and rollout_steps and
                max(auth_steps) < min(rollout_steps) <= max(rollout_steps) < smoke_steps[0]):
            fail("deploy.yml's smoke auth preflight and bootstrap (one step each) must come before rollout, and the post-rollout smoke (one step) after it")
        else:
            if steps[pre_steps[0]].get("if") != PREFLIGHT_IF:
                fail(f"deploy.yml's smoke auth preflight: its `if` must be exactly `{PREFLIGHT_IF}` (a push, or a dispatch with auth_preflight on), is {steps[pre_steps[0]].get('if')!r}")
            if steps[boot_steps[0]].get("if") != BOOTSTRAP_IF:
                fail(f"deploy.yml's smoke auth bootstrap: its `if` must be exactly `{BOOTSTRAP_IF}` (only a dispatch with auth_preflight off), is {steps[boot_steps[0]].get('if')!r}")
            sm = steps[smoke_steps[0]]
            if sm.get("if") != SMOKE_IF:
                fail(f"deploy.yml's post-rollout smoke: its `if` must be exactly `{SMOKE_IF}` (no input skips it, D286), is {sm.get('if')!r}")
            sr = runs[smoke_steps[0]]
            # The session file of the preflight is not the smoke's: it mints afresh (no SMOKE_AUTH_FILE set for it).
            if re.search(r"SMOKE_AUTH_FILE\s*=", sr) or not re.search(r"unset\s+SMOKE_AUTH_FILE\b", sr):
                fail(f"deploy.yml's deploy job, {step_label(smoke_steps[0], sm)}: it unsets SMOKE_AUTH_FILE and never sets it: the post-rollout smoke mints a fresh token, not the preflight's session")
            # The rollback lives inside the smoke step (no status function may run a later step after its failure).
            if not (UPDATE_TRAFFIC.search(sr) and "--to-revisions" in sr and PREVIOUS_FILE.search(sr) and
                    re.search(r"\bfor\s+\w+\s+in\s+api\s+ingest\s+notify\s+scoring\s*;", sr) and re.search(r"\bexit\s+1\b", sr)):
                fail(f"deploy.yml's deploy job, {step_label(smoke_steps[0], sm)}: the smoke step rolls back itself when `make smoke-prod` fails: "
                     "`gcloud run services update-traffic <svc> --to-revisions=<previous>=100` from $RUNNER_TEMP/previous for api, ingest, notify, "
                     "scoring in that order, then `exit 1`")
            for n, r in enumerate(runs):
                if n != smoke_steps[0] and UPDATE_TRAFFIC.search(r):
                    fail(f"deploy.yml's deploy job, {step_label(n, steps[n])}: `gcloud run services update-traffic` belongs in the post-rollout smoke step only (the rollback)")
            # The serving revisions are recorded before anything rolls: before migrate, and so before the services.
            prev_steps = [n for n, r in enumerate(runs) if DESCRIBE.search(r) and PREVIOUS_FILE.search(r) and not UPDATE_TRAFFIC.search(r)]
            if len(prev_steps) != 1 or not services_steps or not prev_steps[0] < min(rollout_steps) <= min(services_steps):
                fail("deploy.yml's deploy job needs one step before migrate and the services that records the serving revisions: "
                     "`gcloud run services describe` into $RUNNER_TEMP/previous")
            else:
                pr = runs[prev_steps[0]]
                if not (re.search(r"\bfor\s+\w+\s+in\s+scoring\s+notify\s+ingest\s+api\s*;", pr) and "--format=json" in pr and
                        "traffic" in pr and "percent" in pr and "revisionName" in pr):
                    fail(f"deploy.yml's deploy job, {step_label(prev_steps[0], steps[prev_steps[0]])}: it reads status.traffic (--format=json) of scoring, notify, ingest and api, "
                         "one entry with percent 100 and its revisionName")
        # D286: the smoke's identity is a Google ID token of ryft-smoke, minted with the job's own sign-in. Neither smoke
        # step takes a secret (an expression `secrets.`, or the retired email, password, token and anon-key names).
        for n in auth_steps + smoke_steps:
            # The script itself is searched as text, not through json.dumps: a "\n" escape before a name hides its \b.
            m = SMOKE_SECRET.search(runs[n]) or SMOKE_SECRET.search(json.dumps({k: v for k, v in steps[n].items() if k != "run"}))
            if m:
                fail(f"deploy.yml's deploy job, {step_label(n, steps[n])}: `{m.group(0)}`: the smoke authenticates with a Google ID "
                     "token of ryft-smoke minted by the job's own sign-in; it takes no secret (no secrets.*, SUPABASE_ANON_KEY, "
                     "SMOKE_EMAIL, SMOKE_PASSWORD or SMOKE_TOKEN), D286")
    # The site (C1): site-build builds without a token, site deploys from main's tip only.
    for sid in ("site-build", "site"):
        sj = jobs.get(sid)
        if not isinstance(sj, dict):
            fail(f"deploy.yml has no job `{sid}`")
            continue
        needs = sj.get("needs")
        needs = [needs] if isinstance(needs, str) else needs
        if not isinstance(needs, list) or "ci" not in needs:
            fail(f"deploy.yml's {sid} job must need `ci` (the wait for ci-ok, and main still at this commit)")
        terms = conjuncts(sj["if"]) if isinstance(sj.get("if"), str) else []
        if not any(CURRENT_TERM.fullmatch(t) for t in terms):
            fail(f"deploy.yml's {sid} job: its `if` must have the top-level conjunct needs.ci.outputs.current == 'true' "
                 f"(a superseded run deploys nothing), is {sj.get('if')!r}")
        if not any(SITE_ON_TERM.fullmatch(t) for t in terms):
            fail(f"deploy.yml's {sid} job: its `if` must have the top-level conjunct vars.SITE_DEPLOY == 'on' (the site's switch), is {sj.get('if')!r}")
        steps = job_steps(sj)
        if steps is None:
            fail(f"deploy.yml's {sid} job has no list of steps")
            continue
        for n, s in enumerate(steps):
            if "uses" in s and not (isinstance(s["uses"], str) and SITE_ACTIONS[sid].fullmatch(s["uses"])):
                allowed = "checkout, setup-node and upload-artifact" if sid == "site-build" else "checkout, setup-node and download-artifact"
                fail(f"deploy.yml's {sid} job, {step_label(n, s)}: `uses: {s['uses']}`: its only actions are actions/{allowed}, by SHA")
    sb = jobs.get("site-build")
    if isinstance(sb, dict):
        perms = sb.get("permissions")
        if isinstance(perms, dict) and "id-token" in perms or isinstance(perms, str):
            fail(f"deploy.yml's site-build job may not have id-token (npm runs code there), has permissions {perms!r}")
        if "environment" in sb:
            fail("deploy.yml's site-build job runs in no environment: nothing there needs its secrets, and the WIF condition admits only that environment")
    st = jobs.get("site")
    if isinstance(st, dict):
        c = st.get("concurrency")
        if not (isinstance(c, dict) and c.get("group") == "site" and c.get("cancel-in-progress") is False):
            fail(f"deploy.yml's site job runs in concurrency group `site` with cancel-in-progress: false, has {c!r}")
        steps = job_steps(st) or []
        runs = [s["run"] if isinstance(s.get("run"), str) else "" for s in steps]
        for n, s in enumerate(steps):
            u = s.get("uses")
            if isinstance(u, str) and u.startswith("actions/setup-node@"):
                w = s.get("with", {})
                bad = [k for k in w if "cache" in str(k).lower() and not (k == "package-manager-cache" and w[k] is False)] \
                    if isinstance(w, dict) else ["(with is not a mapping)"]
                if bad:
                    fail(f"deploy.yml's site job, {step_label(n, s)}: setup-node without a cache ({', '.join(map(str, bad))}): "
                         "no cache entry, which another job may have written, is restored next to the token")
                elif not (isinstance(w, dict) and w.get("package-manager-cache") is False):
                    fail(f"deploy.yml's site job, {step_label(n, s)}: setup-node needs package-manager-cache: false "
                         "(a packageManager field in a package.json would otherwise turn its automatic cache on next to the token)")
        tip = next((n for n, s in enumerate(steps) if s.get("id") == "tip"), None)
        if tip is None:
            fail("deploy.yml's site job has no step with id `tip` (main is still at this commit, before it signs in)")
        else:
            for n in range(tip + 1):
                rest = json.dumps({k: v for k, v in steps[n].items() if k != "run"})
                m = SITE_SIGNS_IN.search(runs[n]) or SITE_SIGNS_IN.search(rest)
                if m:
                    fail(f"deploy.yml's site job, {step_label(n, steps[n])}: `{m.group(0)[:60]}` at or before the step `tip`: "
                         "nothing signs in or mints a token before the run knows main is still at this commit")
        for n, r in enumerate(runs):
            if FIREBASE_DEPLOY.search(r) or CHECK_HOSTING.search(r):
                cond = steps[n].get("if")
                if not (tip is not None and n > tip and isinstance(cond, str) and TIP_GATE.match(cond.strip()) and not top_level_or(cond)):
                    fail(f"deploy.yml's site job, {step_label(n, steps[n])}: it deploys the site or checks it live, so it comes after the "
                         f"step `tip` and its `if` starts with steps.tip.outputs.current == 'true' (no || outside parentheses), is {cond!r}")
    for jid, job in jobs.items():
        if jid == "site" or not isinstance(job, dict):
            continue
        for n, s in enumerate(job_steps(job) or []):
            if isinstance(s.get("run"), str) and FIREBASE_DEPLOY.search(s["run"]):
                fail(f"deploy.yml's job '{jid}', {step_label(n, s)}: `firebase ... deploy` outside the site job (only `site` deploys the site, from main's tip)")
    if wf.get("permissions") != {}:
        fail("deploy.yml sets `permissions: {}` at the top; each job asks for its own")

for m in errs:
    print(m)
sys.exit(1 if errs else 0)
PYEOF

errf=$(mktemp); trap 'rm -f "$errf"' EXIT
for f in "${files[@]}"; do
  # A YAML merge key (`<<: *anchor`) is read differently by yq, by GitHub and by other readers.
  if grep -Eq "(^|[{,[:space:]\"'])<<[\"']?[[:space:]]*:" "$f"; then
    fail "$f: a YAML merge key (<<:) is not allowed in a workflow: the readers disagree about what it merges. Write the keys out."; continue
  fi
  # By tag, not by text and not by yq's warning: `<<` is !!merge, a !!binary key or value is decoded
  # by other readers into something else than it looks like.
  if tags=$(yq -o=json -I0 '[... | tag] | unique' "$f" 2>/dev/null) && printf '%s' "$tags" | grep -Eq '"!!(merge|binary)"'; then
    fail "$f: a YAML merge key (<<, !!merge) or a !!binary node is not allowed in a workflow: the readers disagree about it. Write the keys and values out."; continue
  fi
  if ! json=$(yq -o=json 'explode(.)' "$f" 2>"$errf"); then
    fail "$f: yq cannot parse it: $(cat "$errf")"; continue
  fi
  if [ -s "$errf" ]; then
    fail "$f: yq warned while parsing it: $(cat "$errf")"; continue
  fi
  if ! out=$(printf '%s' "$json" | python3 -I -c "$PY" "$f" "$(basename "$f")" 2>&1); then
    while IFS= read -r l; do [ -z "$l" ] || fail "$l"; done <<EOT
$out
EOT
  fi
done
# 6. the golangci-lint release of ci.yml's lint job is the one `make lint` expects (GOLANGCI_LINT_VERSION in the Makefile).
if [ -f "$dir/ci.yml" ] && [ -f Makefile ]; then
  ci_v=$(yq '.jobs.lint.steps[] | select((.uses // "") | test("^golangci/golangci-lint-action@")) | .with.version' "$dir/ci.yml" 2>/dev/null || true)
  mk_v=$(sed -n 's/^GOLANGCI_LINT_VERSION[[:space:]]*:\{0,1\}=[[:space:]]*//p' Makefile | head -n 1)
  if [ -z "$ci_v" ] || [ -z "$mk_v" ] || [ "$ci_v" != "$mk_v" ]; then
    fail "the golangci-lint version of ci.yml's lint job (${ci_v:-not found}) must equal GOLANGCI_LINT_VERSION in the Makefile (${mk_v:-not found})"
  fi
fi
[ "$rc" = 0 ] && echo "workflows: no pull_request_target/workflow_run, no external reusable workflow, no secrets: inherit or dynamic secrets, no test hooks; only deploy.yml's deploy (in deploy) and site (in site) name an environment, nothing names the old App key; deploy.yml is gated by GCP_DEPLOY, serialized, waits for ci.yml's ci-ok, has no status function or continue-on-error, scans before it signs in, changes Cloud Run only from main's tip, builds the site without a token and deploys and checks it only from main's tip"
exit "$rc"
