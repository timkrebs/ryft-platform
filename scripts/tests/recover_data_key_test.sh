#!/usr/bin/env bash
# shellcheck disable=SC2016
# scripts/recover_data_key.sh against a fake HCP Terraform API (make test-scripts). curl is a fake
# on PATH (the script has no switch for the API host), HOME and TMPDIR are throwaway, and the token
# and every value are made up at run time: nothing reaches app.terraform.io. It checks: arguments
# and the token before any request; the token and every URL (a signed download URL is a
# credential) only in curl's config on stdin, never in any argv of any run, the token only for
# app.terraform.io, curl started with -q; the state versions paged and sorted, only finalized
# ones, the newest that holds the root module's random_bytes.data_key["api"] (not another index key,
# a module's or a deposed instance); the value in a 0600 file exactly as in the state, never
# printed, never overwritten; the folder excluded from Time Machine before the write, a failing
# tmutil only a warning; --demo, --dry-run, a named version; the errors (no token, 401, 404 that
# names both causes, none holds it, a key that is not 32 bytes, an expired download URL, an
# unknown state format); and no raw state left behind.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
mkdir -p "$t/bin" "$t/tmp" "$t/fx/obj" "$t/fx/sv"
fail() { echo "✗ recover_data_key_test: $*" >&2; exit 1; }
unset TF_TOKEN_app_terraform_io FAKE_LIST FAKE_WS_GONE FAKE_DL_CODE FAKE_TMUTIL_FAIL
TOKEN=tok$RANDOM$RANDOM.atlasv1.$RANDOM$RANDOM$RANDOM
# The object store's signed token, in the path of every download URL there.
SIG=dmF1bHQ6djE6$RANDOM$RANDOM$RANDOM$RANDOM
export FAKE_DIR=$t/fx FAKE_TOKEN=$TOKEN FAKE_SIG=$SIG TMPDIR=$t/tmp

# curl: the API and the state downloads from $FAKE_DIR. Records each call (argv, URL and where it
# came from, the headers from the config on stdin) in calls (one run), and every argv in argv.log
# (all runs). app.terraform.io answers 401 without the right bearer; the object store answers 400
# to a request that carries one and 403 to a path without the signed token. FAKE_LIST picks the
# list pages (<list>-<page>.json), FAKE_WS_GONE answers 404 for the workspace, FAKE_DL_CODE answers
# the object store with that status. Any other host: "could not resolve" (no network in this test).
cat > "$t/bin/curl" <<'C'
#!/usr/bin/env python3
import json, os, re, sys
from urllib.parse import urlsplit, parse_qs
d = os.environ["FAKE_DIR"]
args, opts, url, i = sys.argv[1:], {}, None, 0
with open(os.path.join(d, "argv.log"), "a") as f:
    f.write(json.dumps(args) + "\n")
valued = {"--config", "--max-redirs", "--proto", "--proto-redir", "--connect-timeout", "--max-time", "--output", "--write-out"}
while i < len(args):
    if args[i] in valued:
        opts[args[i]] = args[i + 1]; i += 2
    elif args[i].startswith("-"):
        opts[args[i]] = True; i += 1
    else:
        url = args[i]; i += 1
headers, url_from = {}, ("argv" if url else None)
if opts.get("--config") == "-":
    for line in sys.stdin.read().splitlines():
        m = re.fullmatch(r'header = "([^:"]+): ([^"]*)"', line.strip())
        n = re.fullmatch(r'url = "([^"\\]+)"', line.strip())
        if m:
            headers[m.group(1).lower()] = m.group(2)
        elif n and url_from != "config":
            url, url_from = n.group(1), "config"
        elif line.strip():
            sys.stderr.write("fake curl: a config line that is not a header or the one url\n"); sys.exit(2)
with open(os.path.join(d, "calls"), "a") as f:
    f.write(json.dumps({"argv": args, "url": url, "url_from": url_from, "headers": headers}) + "\n")
if url is None:
    sys.stderr.write("curl: no URL specified\n"); sys.exit(2)
u = urlsplit(url)
code, body = 404, '{"errors":[{"status":"404","title":"not found"}]}'
def from_file(p):
    global code, body
    if os.path.isfile(p):
        code, body = 200, open(p).read()
if u.scheme != "https" or u.hostname not in ("app.terraform.io", "archivist.terraform.io"):
    sys.stderr.write("curl: (6) Could not resolve host\n"); sys.stdout.write("000"); sys.exit(6)
if u.hostname == "app.terraform.io":
    if headers.get("authorization") != "Bearer " + os.environ["FAKE_TOKEN"]:
        code, body = 401, '{"errors":[{"status":"401","title":"unauthorized"}]}'
    elif u.path.startswith("/api/v2/organizations/"):
        m = re.fullmatch(r"/api/v2/organizations/([^/]+)/workspaces/([^/]+)", u.path)
        if m and m.groups() == ("tim-krebs-org", "ryft-prod-euc1-vault") and not os.environ.get("FAKE_WS_GONE"):
            code, body = 200, json.dumps({"data": {"id": "ws-test", "type": "workspaces", "attributes": {"name": m.group(2)}}})
    elif u.path == "/api/v2/state-versions":
        q = parse_qs(u.query)
        if q.get("filter[workspace][name]") == ["ryft-prod-euc1-vault"] and q.get("filter[organization][name]") == ["tim-krebs-org"]:
            from_file(os.path.join(d, "%s-%s.json" % (os.environ.get("FAKE_LIST") or "main", q.get("page[number]", ["1"])[0])))
    elif re.fullmatch(r"/api/v2/state-versions/sv-[A-Za-z0-9]+", u.path):
        from_file(os.path.join(d, "sv", u.path.rsplit("/", 1)[1] + ".json"))
    elif re.fullmatch(r"/api/state-versions/sv-[A-Za-z0-9]+/hosted_state", u.path):
        from_file(os.path.join(d, "obj", u.path.split("/")[3]))
elif "authorization" in headers:
    code, body = 400, "a bearer sent to the object store"
elif not u.path.startswith("/v1/object/" + os.environ["FAKE_SIG"] + "/"):
    code, body = 403, "a download URL without its signed token"
else:
    from_file(os.path.join(d, "obj", u.path.rsplit("/", 1)[1]))
    if code == 200 and os.environ.get("FAKE_DL_CODE"):
        code, body = int(os.environ["FAKE_DL_CODE"]), "expired"
if opts.get("--output"):
    open(opts["--output"], "w").write(body)
else:
    sys.stdout.write(body)
sys.stdout.write(opts.get("--write-out", "").replace("%{http_code}", str(code)))
C
chmod +x "$t/bin/curl"

# tmutil: records its arguments and what the folder held at that moment (in $FAKE_DIR/tmutil, one
# run); FAKE_TMUTIL_FAIL makes it fail. First on PATH, so the real one never runs.
cat > "$t/bin/tmutil" <<'T'
#!/usr/bin/env bash
printf '%s|%s\n' "$*" "$(ls -A "${2:-}" 2>/dev/null | tr '\n' ' ')" >> "$FAKE_DIR/tmutil"
if [ -n "${FAKE_TMUTIL_FAIL:-}" ]; then echo "tmutil: the operation could not be completed (fake)" >&2; exit 1; fi
T
chmod +x "$t/bin/tmutil"

# The workspace's history: two state versions after the destroy (the newest empty, the one before
# with only another index key, a deposed instance and a module's resource of the same name), a
# discarded one that is newer still (a decoy value: it must not be read), the last one before the
# destroy (served through app.terraform.io, so it needs the bearer) and an older one with an
# earlier key. Out of order across two pages. Plus single versions for the error cases.
python3 - "$t/fx" <<'PY'
import base64, json, os, secrets, string, sys
d = sys.argv[1]
key = lambda n=32: base64.b64encode(os.urandom(n)).decode()
pw = lambda: "".join(secrets.choice(string.ascii_letters + string.digits) for _ in range(24))
vals = {"key": key(), "older": key(), "ingest": key(), "module": key(), "deposed": key(), "decoy": key(),
        "demo": pw(), "demo_older": pw(), "demo_decoy": pw(), "short": key(16)}
json.dump(vals, open(os.path.join(d, "values.json"), "w"))
P = 'provider["registry.terraform.io/hashicorp/random"]'
def inst(attrs, **kw):
    return dict({"schema_version": 0, "attributes": attrs, "sensitive_attributes": []}, **kw)
def b(k, v, **kw):
    raw = base64.b64decode(v)
    return inst({"base64": v, "hex": raw.hex(), "id": "none", "keepers": None, "length": len(raw)}, index_key=k, **kw)
def data_key(*instances, module=None):
    r = {"mode": "managed", "type": "random_bytes", "name": "data_key", "provider": P, "instances": list(instances)}
    if module:
        r["module"] = module
    return r
def demo(v):
    return {"mode": "managed", "type": "random_password", "name": "demo", "provider": P,
            "instances": [inst({"result": v, "length": 24, "special": False, "id": "none"})]}
db = {"mode": "managed", "type": "random_password", "name": "db", "provider": P, "instances": [inst({"result": "x" * 40}, index_key="api")]}
def state(name, serial, *resources, version=4):
    json.dump({"version": version, "terraform_version": "1.9.8", "serial": serial, "lineage": "test", "outputs": {},
               "resources": list(resources), "check_results": None}, open(os.path.join(d, "obj", name), "w"))
state("obj-post2", 41)
state("obj-post1", 40, data_key(b("ingest", vals["ingest"]), b("api", vals["deposed"], deposed="0a1b2c3d")),
      data_key(b("api", vals["module"]), module="module.old"))
state("obj-disc", 42, data_key(b("api", vals["decoy"])), demo(vals["demo_decoy"]))
state("sv-good", 38, db, data_key(b("api", vals["key"]), b("ingest", vals["ingest"])), demo(vals["demo"]))
state("obj-older", 12, data_key(b("api", vals["older"])), demo(vals["demo_older"]))
state("obj-short", 5, data_key(b("api", vals["short"])))
state("obj-v3", 3, data_key(b("api", vals["key"])), version=3)
A = "https://archivist.terraform.io/v1/object/" + os.environ["FAKE_SIG"] + "/"
def sv(id, created, serial, url, status="finalized", ws="ws-test"):
    v = {"id": id, "type": "state-versions",
         "attributes": {"created-at": created, "serial": serial, "status": status, "hosted-state-download-url": url,
                        "hosted-json-state-download-url": None, "resources-processed": True},
         "relationships": {"workspace": {"data": {"id": ws, "type": "workspaces"}}}}
    json.dump({"data": v}, open(os.path.join(d, "sv", id + ".json"), "w"))
    return v
V = {
    "post2": sv("sv-post2", "2026-11-10T12:05:31.000Z", 41, A + "obj-post2"),
    "post1": sv("sv-post1", "2026-11-10T12:04:02.000Z", 40, A + "obj-post1"),
    "disc": sv("sv-disc", "2026-11-10T12:06:00.000Z", 42, A + "obj-disc", status="discarded"),
    "good": sv("sv-good", "2026-11-09T08:00:00.000Z", 38, "https://app.terraform.io/api/state-versions/sv-good/hosted_state"),
    "older": sv("sv-older", "2026-10-01T10:00:00.000Z", 12, A + "obj-older"),
    "foreign": sv("sv-foreign", "2026-10-02T10:00:00.000Z", 3, A + "obj-older", ws="ws-other"),
    "short": sv("sv-short", "2026-11-01T10:00:00.000Z", 5, A + "obj-short"),
    "v3": sv("sv-v3", "2026-11-01T10:00:00.000Z", 3, A + "obj-v3"),
    "nourl": sv("sv-nourl", "2026-11-10T13:00:00.000Z", 43, None),
}
def pages(name, *groups):
    for n, g in enumerate(groups, 1):
        nxt = n + 1 if n < len(groups) else None
        json.dump({"data": [V[k] for k in g], "links": {},
                   "meta": {"pagination": {"current-page": n, "next-page": nxt, "prev-page": n - 1 or None,
                                           "total-pages": len(groups), "total-count": sum(map(len, groups))}}},
                  open(os.path.join(d, "%s-%d.json" % (name, n)), "w"))
pages("main", ["older", "post2", "disc"], ["good", "post1"])
pages("none", ["post2", "post1"])
pages("short", ["post2", "short"])
pages("v3", ["v3"])
pages("nourl", ["nourl", "good"])
PY
val() { python3 -c 'import json, sys; sys.stdout.write(json.load(open(sys.argv[1]))[sys.argv[2]])' "$t/fx/values.json" "$1"; }
KEY=$(val key) OLDER=$(val older) DEMO=$(val demo)
fp() { printf '%s' "$1" | shasum -a 256 | cut -c1-16; }
mode() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
home() { export HOME=$t/home.$1; mkdir -p "$HOME"; }
run() { # recover_data_key.sh <args> with the fakes first on PATH: $rc, $t/out, $t/err, $t/fx/calls, $t/fx/tmutil
  : > "$t/fx/calls"; : > "$t/fx/tmutil"
  rc=0; PATH="$t/bin:$PATH" bash "$ROOT/scripts/recover_data_key.sh" "$@" >"$t/out" 2>"$t/err" || rc=$?
}
said() { echo "rc=$rc; stdout: $(cat "$t/out"); stderr: $(cat "$t/err")"; }
no_calls() { [ ! -s "$t/fx/calls" ] || fail "$1: curl ran: $(cat "$t/fx/calls")"; }
no_tmutil() { [ ! -s "$t/fx/tmutil" ] || fail "$1: tmutil ran: $(cat "$t/fx/tmutil")"; }
excluded() { # tmutil addexclusion (the sticky form: no -p) on the folder, once, while it was still empty
  [ "$(cat "$t/fx/tmutil")" = "addexclusion $HOME/.config/ryft|" ] || fail "$1: tmutil: $(cat "$t/fx/tmutil")"
}
calls() { # <python over calls (each argv, url, url_from, headers), tok and sig>
  python3 -c '
import json, os, sys
calls = [json.loads(l) for l in open(sys.argv[1])]
tok, sig = os.environ["FAKE_TOKEN"], os.environ["FAKE_SIG"]
exec(sys.argv[2])' "$t/fx/calls" "$1"
}
silent() { # no value of the fixtures, no token and no signed URL in what the script printed
  local name
  for name in key older ingest module deposed decoy demo demo_older demo_decoy short; do
    if grep -qF -- "$(val "$name")" "$t/out" "$t/err"; then fail "$1: the value '$name' was printed: $(said)"; fi
  done
  if grep -qF -- "$TOKEN" "$t/out" "$t/err"; then fail "$1: the token was printed"; fi
  if grep -qF -- "$SIG" "$t/out" "$t/err"; then fail "$1: a signed download URL was printed"; fi
}

# 1. --help, and every argument checked before the token and before any request.
home help; run --help
[ "$rc" = 0 ] && grep -q '^  scripts/recover_data_key.sh \[--demo\] \[--dry-run\]' "$t/out" || fail "--help: $(said)"
if grep -q '^#' "$t/out" || grep -q '^set -euo' "$t/out"; then fail "--help prints code or # prefixes: $(cat "$t/out")"; fi
grep -q 'tmutil addexclusion ~/.config/ryft' "$t/out" && grep -q 'local snapshot' "$t/out" || fail "--help does not name the Time Machine step: $(cat "$t/out")"
export TF_TOKEN_app_terraform_io=$TOKEN
for a in "--frob" "sv-a;b" "sv-a sv-b" "sv-" "--org" "--org a.b" "--workspace ../x" "--demo extra"; do
  read -ra words <<< "$a"
  run "${words[@]}"
  [ "$rc" = 2 ] || fail "'$a' must exit 2: $(said)"
  no_calls "'$a'"
done

# 2. The token: none at all, one with characters a token does not have, a wrong one (401), one
# from terraform login's file, and the environment before that file.
unset TF_TOKEN_app_terraform_io
home notoken; run --dry-run
[ "$rc" = 2 ] && grep -q 'no HCP Terraform token: set TF_TOKEN_app_terraform_io, or run terraform login' "$t/err" || fail "no token: $(said)"
no_calls "no token"
mkdir -p "$HOME/.terraform.d"; echo '{"credentials":{"other.example":{"token":"x"}}}' > "$HOME/.terraform.d/credentials.tfrc.json"
run --dry-run; [ "$rc" = 2 ] || fail "a credentials file without app.terraform.io: $(said)"; no_calls "no app.terraform.io token"
TF_TOKEN_app_terraform_io='abc" header = "X: y' run --dry-run
[ "$rc" = 2 ] && grep -q 'characters a token does not have' "$t/err" || fail "a token with quotes: $(said)"; no_calls "a token with quotes"
TF_TOKEN_app_terraform_io=wrong.atlasv1.token run --dry-run
[ "$rc" = 1 ] && grep -q 'refused the token (401)' "$t/err" || fail "a wrong token: $(said)"
printf '{"credentials":{"app.terraform.io":{"token":"%s"}}}\n' "$TOKEN" > "$HOME/.terraform.d/credentials.tfrc.json"
run --dry-run; [ "$rc" = 0 ] || fail "the token from credentials.tfrc.json: $(said)"
TF_TOKEN_app_terraform_io=wrong.atlasv1.token run --dry-run
[ "$rc" = 1 ] && grep -q '(401)' "$t/err" || fail "TF_TOKEN_app_terraform_io must come before the credentials file: $(said)"
export TF_TOKEN_app_terraform_io=$TOKEN

# 3. The workspace is gone (or hidden from the token): a 404 that names both causes, sends the user
# to the UI first and names the fallback as final; nothing else asked, nothing written.
home gone; FAKE_WS_GONE=1 run
[ "$rc" = 1 ] && grep -q 'workspace ryft-prod-euc1-vault in tim-krebs-org: not found (404' "$t/err" \
  && grep -q 'clears the unreadable Apple tokens' "$t/err" || fail "workspace gone: $(said)"
grep -q 'deleted, or this token cannot see it' "$t/err" \
  && grep -qF 'HCP Terraform UI (https://app.terraform.io/app/tim-krebs-org/workspaces/ryft-prod-euc1-vault)' "$t/err" \
  && grep -qF 'Before you treat random_bytes.data_key["api"] as lost' "$t/err" && grep -q 'which cannot be undone' "$t/err" \
  || fail "the 404 must name both causes, the UI check and the fallback as final: $(said)"
no_tmutil "workspace gone"
[ "$(wc -l < "$t/fx/calls" | tr -d ' ')" = 1 ] || fail "after a 404 for the workspace nothing more is asked: $(cat "$t/fx/calls")"
[ ! -e "$HOME/.config/ryft/data-key-api" ] || fail "workspace gone: a file was written"

# 4. The newest finalized version that holds the key, from two pages out of order; the discarded
# version is never read, the older one never downloaded; the value as in the state, 0600, no newline.
home main; run
[ "$rc" = 0 ] || fail "the newest version holding the key: $(said)"
cat > "$t/want" <<W
state version: sv-good
created-at:    2026-11-09T08:00:00.000Z
serial:        38
sha256:        $(fp "$KEY") (first 16 hex digits, of the value as written, no newline)
decoded:       32 bytes
written:       $HOME/.config/ryft/data-key-api (0600)
W
diff "$t/want" "$t/out" >/dev/null || fail "the report: $(diff "$t/want" "$t/out")"
grep -q '1 of 5 state versions are not finalized' "$t/err" && grep -q '2 newer state version(s) did not hold it' "$t/err" \
  || fail "the skipped versions are not named: $(cat "$t/err")"
f=$HOME/.config/ryft/data-key-api
[ "$(cat "$f")" = "$KEY" ] && [ "$(wc -c < "$f" | tr -d ' ')" = 44 ] || fail "the file does not hold exactly the key"
[ "$(mode "$f")" = 600 ] && [ "$(mode "$HOME/.config/ryft")" = 700 ] || fail "modes: file $(mode "$f"), dir $(mode "$HOME/.config/ryft")"
excluded "the main run"
grep -qF "$HOME/.config/ryft: excluded from Time Machine backups" "$t/err" || fail "the exclusion is not reported: $(cat "$t/err")"
silent "the main run"
calls '
api = "https://app.terraform.io/"
obj = "https://archivist.terraform.io/v1/object/" + sig + "/"
urls = [c["url"] for c in calls]
assert urls[0] == api + "api/v2/organizations/tim-krebs-org/workspaces/ryft-prod-euc1-vault", urls
assert [u.split("page%5Bnumber%5D=")[1] for u in urls[1:3]] == ["1", "2"], urls
assert all("filter%5Bworkspace%5D%5Bname%5D=ryft-prod-euc1-vault" in u and "filter%5Borganization%5D%5Bname%5D=tim-krebs-org" in u for u in urls[1:3]), urls
assert urls[3:] == [obj + "obj-post2", obj + "obj-post1", api + "api/state-versions/sv-good/hosted_state"], urls
for c in calls:
    assert c["argv"][0] == "-q", c["argv"]  # no ~/.curlrc
    assert not [a for a in c["argv"] if tok in a], "the token in argv"
    assert c["url_from"] == "config", ("the URL not from the config on stdin", c)
    assert not [a for a in c["argv"] if "://" in a or sig in a or "bearer" in a.lower()], ("a URL or a bearer in argv", c["argv"])
    assert c["argv"][c["argv"].index("--proto") + 1] == "=https" and c["argv"][c["argv"].index("--proto-redir") + 1] == "=https", c["argv"]
    if c["url"].startswith(api):
        assert c["headers"].get("authorization") == "Bearer " + tok, c
    else:
        assert c["headers"] == {}, c
' || fail "the requests (above)"

# 5. Never overwritten: a second run refuses before any request, a dangling symlink too; --dry-run
# next to the file is fine and leaves it.
run; [ "$rc" = 1 ] && grep -q 'exists: refusing to overwrite it' "$t/err" || fail "a second run: $(said)"
no_calls "a second run"; no_tmutil "a second run"; [ "$(cat "$f")" = "$KEY" ] || fail "the second run changed the file"
run --dry-run; [ "$rc" = 0 ] && grep -qx 'written:       nothing (--dry-run)' "$t/out" && [ "$(cat "$f")" = "$KEY" ] || fail "--dry-run next to the file: $(said)"
no_tmutil "--dry-run next to the file"
home sym; mkdir -p "$HOME/.config/ryft"; ln -s "$t/nowhere" "$HOME/.config/ryft/data-key-api"
run; [ "$rc" = 1 ] && [ ! -e "$t/nowhere" ] || fail "a dangling symlink: $(said)"; no_calls "a dangling symlink"
home dry; run --dry-run
[ "$rc" = 0 ] && [ ! -e "$HOME/.config/ryft/data-key-api" ] && grep -q "^sha256:        $(fp "$KEY") " "$t/out" || fail "--dry-run: $(said)"
no_tmutil "--dry-run"
silent "--dry-run"

# 6. A named version: that one, its own workspace's, without the list.
home named; run sv-older
[ "$rc" = 0 ] && grep -qx 'state version: sv-older' "$t/out" && grep -qx 'serial:        12' "$t/out" \
  && grep -q "^sha256:        $(fp "$OLDER") " "$t/out" && [ "$(cat "$HOME/.config/ryft/data-key-api")" = "$OLDER" ] || fail "sv-older: $(said)"
calls 'assert [c["url"].rsplit("/", 1)[1] for c in calls] == ["ryft-prod-euc1-vault", "sv-older", "obj-older"], [c["url"] for c in calls]' \
  || fail "a named version must not list the versions (above)"
silent "sv-older"
home named2; run sv-post2
[ "$rc" = 1 ] && grep -q 'sv-post2 (2026-11-10T12:05:31.000Z, serial 41) does not hold random_bytes.data_key\["api"\]' "$t/err" \
  && [ ! -e "$HOME/.config/ryft/data-key-api" ] || fail "a named version without the key: $(said)"
run sv-foreign; [ "$rc" = 1 ] && grep -q 'sv-foreign belongs to workspace ws-other, not to ryft-prod-euc1-vault' "$t/err" || fail "another workspace's version: $(said)"
run sv-nope; [ "$rc" = 1 ] && grep -q 'state version sv-nope: not found (404' "$t/err" || fail "an unknown version: $(said)"

# 7. --demo: random_password.demo from the same version, into its own file, no decoded length.
home demo; run --demo
[ "$rc" = 0 ] && grep -qx 'state version: sv-good' "$t/out" && grep -q "^sha256:        $(fp "$DEMO") " "$t/out" || fail "--demo: $(said)"
if grep -q '^decoded:' "$t/out"; then fail "--demo prints a decoded length"; fi
d=$HOME/.config/ryft/demo-password
[ "$(cat "$d")" = "$DEMO" ] && [ "$(wc -c < "$d" | tr -d ' ')" = 24 ] && [ "$(mode "$d")" = 600 ] || fail "the demo password file"
[ ! -e "$HOME/.config/ryft/data-key-api" ] || fail "--demo wrote the key"
excluded "--demo"
silent "--demo"

# 8. Time Machine: a tmutil that fails is a warning that says what to do, and the key is still
# written (0600, the same report on stdout).
home tmfail; FAKE_TMUTIL_FAIL=1 run
[ "$rc" = 0 ] && grep -qF "warning: tmutil addexclusion $HOME/.config/ryft failed (tmutil: the operation could not be completed (fake)" "$t/err" \
  && grep -q 'exclude the folder by hand' "$t/err" || fail "a failing tmutil: $(said)"
excluded "a failing tmutil"
f=$HOME/.config/ryft/data-key-api
[ "$(cat "$f")" = "$KEY" ] && [ "$(mode "$f")" = 600 ] && grep -qxF "written:       $f (0600)" "$t/out" || fail "a failing tmutil stopped the write: $(said)"
if grep -qF 'excluded from Time Machine backups' "$t/err"; then fail "a failing tmutil reported as done: $(cat "$t/err")"; fi
silent "a failing tmutil"

# 9. The errors: no version holds it, a key that is not 32 bytes, an expired download URL, a state
# format this script does not read, a version without a download URL.
home none; FAKE_LIST=none run
[ "$rc" = 1 ] && grep -q 'none of the 2 state versions of ryft-prod-euc1-vault holds random_bytes.data_key\["api"\]' "$t/err" \
  && grep -q 'clears the unreadable Apple tokens' "$t/err" && [ ! -e "$HOME/.config/ryft/data-key-api" ] || fail "none holds it: $(said)"
FAKE_LIST=none run --demo
[ "$rc" = 1 ] && grep -q 'holds random_password.demo. without it: scripts/seed_demo.sh' "$t/err" || fail "none holds the demo password: $(said)"
home short; FAKE_LIST=short run
[ "$rc" = 1 ] && grep -q 'is not base64 of 32 bytes (24 characters, 16 bytes decoded): not written' "$t/err" \
  && [ ! -e "$HOME/.config/ryft/data-key-api" ] || fail "a 16-byte key: $(said)"
silent "a 16-byte key"
home expired; FAKE_DL_CODE=403 run
[ "$rc" = 1 ] && grep -q 'the state of sv-post2: HTTP 403 (the URL expires after a while: run again)' "$t/err" || fail "an expired URL: $(said)"
home v3; FAKE_LIST=v3 run
[ "$rc" = 1 ] && grep -q 'not a Terraform state this script reads: .*state format 3, want 4' "$t/err" || fail "state format 3: $(said)"
home nourl; FAKE_LIST=nourl run
[ "$rc" = 1 ] && grep -q 'sv-nourl (2026-11-10T13:00:00.000Z) has no download URL' "$t/err" || fail "no download URL: $(said)"
silent "the error cases"

# 10. Across every run above: no URL (so no signed token) and no bearer in any argv curl was
# started with (ps shows argv to every user); and no raw state anywhere: every run removed its
# temporary directory.
python3 - "$t/fx/argv.log" <<'PY' || fail "curl's argv (above)"
import json, os, sys
runs = [json.loads(l) for l in open(sys.argv[1])]
assert len(runs) > 20, "the fake curl hardly ran: %d" % len(runs)
hidden = [os.environ["FAKE_TOKEN"], os.environ["FAKE_SIG"], "wrong.atlasv1.token"]
for argv in runs:
    bad = [a for a in argv if "://" in a or "bearer" in a.lower() or "authorization" in a.lower() or any(h in a for h in hidden)]
    assert not bad, ("in argv", bad)
PY
[ -z "$(ls -A "$t/tmp")" ] || fail "left behind in TMPDIR: $(ls -A "$t/tmp")"
echo "✓ recover_data_key_test: arguments and the token before any request, the token and every URL only in curl's config (never in argv), the token only for app.terraform.io, the newest finalized version holding the root key, 0600 and never overwritten, excluded from Time Machine first (a failing tmutil only warns), --demo, --dry-run, a named version, the errors, nothing left behind"
