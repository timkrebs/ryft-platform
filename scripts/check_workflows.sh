#!/usr/bin/env bash
# The workflow rules that actionlint cannot know (CI's `manifests` job). The workflows are PARSED
# (yq, mikefarah v4, the pinned release in CI), not grepped, so flow style, quoted keys,
# `environment : deploy`, YAML escapes in names and anchors read as GitHub reads them:
#   1. no workflow triggers on pull_request_target or workflow_run: they run with the base
#      repository's secrets and token on code a pull request influences;
#   2. the environment `deploy` (where the deploy App's key lives, only main may use it) is named
#      by exactly one job, `promote` in ci.yml, and RYFT_DEPLOY_APP_KEY is read only there (any
#      case: secret names are case-insensitive);
#   3. a job-level `uses:` is a workflow of this repository (./.github/workflows/<file>.yml): an
#      external reusable workflow could name the environment, or run with our secrets;
#   4. no `secrets: inherit`, and `secrets` is only ever read as `secrets.<NAME>`: not
#      `secrets[format(...)]`, not `toJSON(secrets)`, not the bare context;
#   5. none of the test hooks of the scripts (CHECK_REGISTRY, CHECK_REGISTRY_OFFLINE, CHECK_RENDER,
#      RENDER_CMD, PIN_QUIET, RYFT_HOOKS_NODE_DIRS) is named anywhere, and NOW or TODAY is no
#      environment variable and no assignment in a script: a workflow must not switch a check off.
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

HOOKS = ["CHECK_REGISTRY_OFFLINE", "CHECK_REGISTRY", "CHECK_RENDER", "RENDER_CMD", "PIN_QUIET", "RYFT_HOOKS_NODE_DIRS"]
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
    if KEY_RE.search(s) and not (is_ci and job == "promote"):
        fail(f"{where}: RYFT_DEPLOY_APP_KEY outside ci.yml's promote job (job: {job or 'workflow level'})")
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
        if not (is_ci and jid == "promote"):
            fail(f"job '{jid}' names an environment ({name!r}): only ci.yml's promote may (the deploy App's key is scoped to it)")
        elif name != "deploy":
            fail(f"promote must run in the environment 'deploy', not {name!r}")
if is_ci:
    p = jobs.get("promote")
    e = p.get("environment") if isinstance(p, dict) else None
    name = e if isinstance(e, str) else e.get("name") if isinstance(e, dict) else None
    if name != "deploy":
        fail("the promote job does not run in `environment: deploy`")

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
[ "$rc" = 0 ] && echo "workflows: no pull_request_target/workflow_run, no external reusable workflow, no secrets: inherit or dynamic secrets, no test hooks; only promote names the environment deploy and reads the App key"
exit "$rc"
