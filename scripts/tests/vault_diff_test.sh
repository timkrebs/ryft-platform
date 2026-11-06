#!/usr/bin/env bash
# scripts/vault_diff.sh against two fake Vaults (HTTPS servers on 127.0.0.1 with a CA made here) and a
# fake kubectl (make test-scripts). Nothing here reaches a cluster or a real Vault: KUBECONFIG=/dev/null,
# the fakes first on PATH, every address a loopback port of this run. It checks the comparison (equal,
# differs, missing on either side, a whole secret missing, shared secrets per service, the broker's
# amqp-user-* and operator/demo, a Secret against Vault and against a saved fingerprint), the refusals
# (no --context, an unknown one, http, no CA, an unverifiable server, a token curl's config could not
# carry) and the exit codes, and that no value and no token leaks: a canary value and both tokens are
# looked for in every stdout and stderr, in the argv and environment of every process the script starts
# (each one is wrapped and recorded), in `ps` as the fake servers see it during each request, and in
# every file the run could write (TMPDIR, HOME, the working directory). Also with the caller's
# `bash -x`, SHELLOPTS=xtrace:allexport and a ~/.curlrc that asks for verbose output.
#   VAULT_DIFF_ROOT=<dir>  the tree whose scripts/vault_diff.sh is tested (default this checkout; mutation runs)
set -euo pipefail
REPO=$(cd "$(dirname "$0")/../.." && pwd)
ROOT=${VAULT_DIFF_ROOT:-$REPO}
SCRIPT=$ROOT/scripts/vault_diff.sh
t=$(mktemp -d)
pids=()
cleanup() {
  for p in ${pids[@]+"${pids[@]}"}; do kill "$p" 2>/dev/null || true; done
  rm -rf "$t"
}
trap cleanup EXIT
fail() { echo "✗ vault_diff_test: $*" >&2; [ ! -s "$t/srv/server.err" ] || { echo "the fake Vaults said:" >&2; cat "$t/srv/server.err" >&2; }; exit 1; }
mkdir -p "$t/bin" "$t/srv" "$t/tmp" "$t/home" "$t/work" "$t/out" "$t/overlays" "$t/secrets"
PY=$(command -v python3)
export KUBECONFIG=/dev/null FAKE_DIR=$t
unset KUBE_CONTEXT VAULT_TOKEN HCP_VAULT_TOKEN CE_VAULT_TOKEN VAULT_ADDR_HCP VAULT_ADDR_K8S VAULT_CACERT VAULT_CACERT_HCP \
  HCP_VAULT_NAMESPACE_ROOT VAULT_DIFF_OVERLAYS

# Made up at run time; a hit anywhere below is a leak.
CANARY=canary-$RANDOM$RANDOM-do-not-print-$RANDOM$RANDOM
CANARY64=$(printf '%s' "$CANARY" | base64 | tr -d '\n')
HCP_TOK=hvs.HCPtok$RANDOM$RANDOM$RANDOM
CE_TOK=hvs.CEtok$RANDOM$RANDOM$RANDOM
sha() { printf '%s' "$1" | "$PY" -c 'import hashlib, sys; print(hashlib.sha256(sys.stdin.buffer.read()).hexdigest())'; }

# Every external program the script may start is a recorder in front of the real one: its argv and
# its whole environment go to spawn.log. The fakes record themselves the same way.
for tool in curl python3 od tr sed grep awk dirname rm cat; do
  real=$(command -v "$tool")
  cat > "$t/bin/$tool" <<W
#!/bin/sh
{ printf 'argv: %s' "\$0"; for a in "\$@"; do printf ' %s' "\$a"; done; printf '\n'; printf "env:\n"; /usr/bin/env; } >> "\$FAKE_DIR/spawn.log"
exec "$real" "\$@"
W
done
# kubectl: three contexts; `get secret` answers from $t/secrets/<ns>/<name>.json (none: nothing, as
# with --ignore-not-found); anything else is refused.
cat > "$t/bin/kubectl" <<'K'
#!/bin/sh
{ printf 'argv: kubectl'; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; printf "env:\n"; /usr/bin/env; } >> "$FAKE_DIR/spawn.log"
echo "$*" >> "$FAKE_DIR/kubectl.calls"
case "$*" in
  "config get-contexts -o name") printf '%s\n' kind-ryft ryft-prod-euc1 test-ctx ;;
  "--context test-ctx -n "*" get secret "*" --ignore-not-found -o json")
    f="$FAKE_DIR/secrets/$4/$7.json"
    [ ! -f "$f" ] || /bin/cat "$f" ;;
  *) echo "fake kubectl: unexpected: $*" >&2; exit 3 ;;
esac
K
chmod +x "$t/bin/"*

# The fake Vaults: kv-v2 reads only. Answers from data-<name>.json ("<namespace>|<mount>|<path>" →
# a map, or absent: 404), reread on every request. The token must match the sha256 given in argv
# (else 403); each request is recorded (path, namespace header, token ok/wrong/missing, never the
# header itself), and so is `ps` (the argv of this user's processes) at that moment, while the
# script's curl runs.
cat > "$t/srv/vault_fake.py" <<'PY'
import hashlib, http.server, json, os, ssl, subprocess, sys
name, want, data, d, cert, key = sys.argv[1:7]
class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def do_GET(self):
        tok = self.headers.get("X-Vault-Token")
        ns = self.headers.get("X-Vault-Namespace")
        ok = "missing" if tok is None else ("ok" if hashlib.sha256(tok.encode()).hexdigest() == want else "wrong")
        rec = {"path": self.path, "ns": ns, "token": ok, "auth": self.headers.get("Authorization") is not None}
        for log in ("requests-%s.log" % name, "all-requests.log"):
            with open(os.path.join(d, log), "a") as f:
                f.write(json.dumps(rec) + "\n")
        # This user's processes (the script's are among them). macOS's ps can stall for many seconds
        # on a process in exit or exec under load: a stalled snapshot is skipped and counted, never
        # waited for (the recorders on PATH are the complete record; this is the live view).
        try:
            ps = subprocess.run(["ps", "-ww", "-U", str(os.getuid()), "-o", "command="],
                                capture_output=True, text=True, timeout=3).stdout
            mark = "ps.ok"
        except subprocess.TimeoutExpired:
            ps, mark = "", "ps.stalled"
        with open(os.path.join(d, "ps.log"), "a") as f:
            f.write(ps)
        with open(os.path.join(d, mark), "a") as f:
            f.write("x\n")
        if ok != "ok":
            return self.send(403, {"errors": ["permission denied"]})
        parts = self.path.split("/")  # /v1/<mount>/data/<path>
        if len(parts) != 5 or parts[1] != "v1" or parts[3] != "data":
            return self.send(404, {"errors": []})
        store = json.load(open(os.path.join(d, "data-%s.json" % name)))
        k = "%s|%s|%s" % (ns or "", parts[2], parts[4])
        if k not in store:
            return self.send(404, {"errors": []})
        if "__redirect__" in store[k]:  # a standby's or an attacker's redirect: elsewhere
            return self.send(307, {}, store[k]["__redirect__"])
        self.send(200, {"data": {"data": store[k], "metadata": {"version": 3}}})
    def send(self, code, doc, location=None):
        b = json.dumps(doc).encode()
        self.send_response(code)
        if location:
            self.send_header("Location", location)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
    def log_message(self, *a):
        pass
s = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(cert, key)
s.socket = ctx.wrap_socket(s.socket, server_side=True, do_handshake_on_connect=False)  # the handshake in the handler thread
open(os.path.join(d, "port-%s.tmp" % name), "w").write(str(s.server_address[1]))
os.rename(os.path.join(d, "port-%s.tmp" % name), os.path.join(d, "port-%s" % name))
s.serve_forever()
PY
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$t/srv/key.pem" -out "$t/srv/ca.pem" -days 1 -subj /CN=127.0.0.1 \
  -addext subjectAltName=IP:127.0.0.1 -addext basicConstraints=critical,CA:TRUE 2>/dev/null
CA=$t/srv/ca.pem
start() { # <name> <token>
  "$PY" -I "$t/srv/vault_fake.py" "$1" "$(sha "$2")" - "$t/srv" "$CA" "$t/srv/key.pem" 2>>"$t/srv/server.err" &
  pids+=($!)
  disown "$!"  # killed by the trap; no job notice
  local i=0
  while [ ! -s "$t/srv/port-$1" ]; do
    i=$((i + 1)); [ "$i" -lt 200 ] || fail "the fake $1 Vault did not start: $(cat "$t/srv/server.err")"
    sleep 0.05
  done
}
: > "$t/srv/data-hcp.json"; : > "$t/srv/data-k8s.json"
start hcp "$HCP_TOK"
start k8s "$CE_TOK"
HCP_URL=https://127.0.0.1:$(cat "$t/srv/port-hcp")
K8S_URL=https://127.0.0.1:$(cat "$t/srv/port-k8s")

# A fixture of the generated VSO objects (the generator's shape): api and ingest with ryft-secrets
# from config, the broker with two amqp-user-*; ingest in the in-cluster shape (sync waves, mount kv-<svc>).
vso() { # <svc> <ns> <mount> <path>:<secret>...
  local svc=$1 ns=$2 mount=$3 p; shift 3
  mkdir -p "$t/overlays/$svc"
  {
    echo "# Generated by infra/vault — do not edit by hand (terraform apply rewrites it)."
    printf 'apiVersion: secrets.hashicorp.com/v1beta1\nkind: VaultAuth\nmetadata:\n  name: %s\n  namespace: %s\nspec:\n  vaultConnectionRef: vault\n  method: kubernetes\n  mount: kubernetes\n' "$svc" "$ns"
    for p in "$@"; do
      printf -- '---\napiVersion: secrets.hashicorp.com/v1beta1\nkind: VaultStaticSecret\nmetadata:\n  name: %s\n  namespace: %s\n  annotations: { argocd.argoproj.io/sync-wave: "1" }\nspec:\n  vaultAuthRef: %s\n  mount: %s\n  type: kv-v2\n  path: %s\n  refreshAfter: 1h\n  destination:\n    name: %s\n    create: true\n    labels:\n      rabbitmq.com/topology-operator: "true"\n    transformation:\n      excludeRaw: true\n' \
        "${p#*:}" "$ns" "$svc" "$mount" "${p%%:*}" "${p#*:}"
    done
  } > "$t/overlays/$svc/vault.yaml"
}
vso api ryft-api kv config:ryft-secrets
vso ingest ryft-ingest kv-ingest config:ryft-secrets
vso rabbitmq ryft-rabbitmq kv amqp-user-api:amqp-user-api amqp-user-ingest:amqp-user-ingest
export VAULT_DIFF_OVERLAYS=$t/overlays

# The data: one JSON per side; HCP keys carry the namespace, the in-cluster ones none.
data() { # <hcp|k8s> <python dict literal over the variables C (canary)>
  C=$CANARY "$PY" -c 'import json, os, sys; C = os.environ["C"]; json.dump(eval(sys.argv[2]), open(sys.argv[1], "w"))' "$t/srv/data-$1.json" "$2"
}
API='{"DATABASE_URL": "postgresql://u:" + C + "@db/x", "ADMIN_API_KEY": C + "-admin", "SUPABASE_PROJECT_REF": "abcdefghij12", "BETA_GATE": "on"}'
INGEST='{"DATABASE_URL": "postgresql://i:" + C + "@db/x", "SUPABASE_PROJECT_REF": "abcdefghij12", "BETA_GATE": "on"}'
both() { # <python expressions for the shared maps>: the same everywhere (dual-write)
  data hcp "{'admin/ryft/api|kv|config': $API, 'admin/ryft/ingest|kv|config': $INGEST,
    'admin/ryft/rabbitmq|kv|amqp-user-api': {'username': 'api', 'password': C + 'a'},
    'admin/ryft/rabbitmq|kv|amqp-user-ingest': {'username': 'ingest', 'password': C + 'i'},
    'admin/ryft|operator|demo': {'email': 'review@example.com', 'password': C + 'd'}}"
  data k8s "{'|kv-api|config': $API, '|kv-ingest|config': $INGEST,
    '|kv-rabbitmq|amqp-user-api': {'username': 'api', 'password': C + 'a'},
    '|kv-rabbitmq|amqp-user-ingest': {'username': 'ingest', 'password': C + 'i'},
    '|kv-operator|demo': {'email': 'review@example.com', 'password': C + 'd'}}"
}
# k8s_with <key> <python expression>: the in-cluster api config with one change (None: the key removed).
k8s_api() {
  data k8s "{'|kv-api|config': {k: v for k, v in dict($API, **{'$1': $2}).items() if v is not None}}"
}
secret() { # <ns> <name> <python dict literal>: the Secret kubectl returns (base64 data, as the API does)
  mkdir -p "$t/secrets/$1"
  C=$CANARY "$PY" -c 'import base64, json, os, sys; C = os.environ["C"]
d = eval(sys.argv[2]); json.dump({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "x"}, "type": "Opaque",
  "data": {k: base64.b64encode(v.encode()).decode() for k, v in d.items()}}, open(sys.argv[1], "w"))' "$t/secrets/$1/$2.json" "$3"
}

# run <want rc> <args...>: the script as an operator runs it, the tokens in its environment only
# (RUN_HCP_TOK / RUN_CE_TOK replace them, RUN_NOTOK=1 gives none; RUN_ENV=<name>=<value> adds one
# variable; RUN_BASH="bash -x" traces). Every run's stdout and stderr are kept for the leak scan.
runs=0
run() {
  local want=$1 rc=0; shift
  runs=$((runs + 1))
  : > "$t/kubectl.calls"
  rm -f "$t/srv/requests-"*.log
  (
    cd "$t/work"
    export HOME="$t/home" TMPDIR="$t/tmp" PATH="$t/bin:$PATH"
    if [ -z "${RUN_NOTOK:-}" ]; then
      export HCP_VAULT_TOKEN="${RUN_HCP_TOK:-$HCP_TOK}" CE_VAULT_TOKEN="${RUN_CE_TOK:-$CE_TOK}"
    fi
    # A prefix assignment on a function call is exported: only the two variables above carry a token.
    unset RUN_HCP_TOK RUN_CE_TOK
    # env(1) for the extra variable: SHELLOPTS is read-only inside bash. Only that variable is in its argv.
    # shellcheck disable=SC2086 # RUN_BASH is a command with its options
    exec /usr/bin/env ${RUN_ENV:+"$RUN_ENV"} ${RUN_BASH:-bash} "$SCRIPT" "$@"
  ) > "$t/out/$runs.out" 2> "$t/out/$runs.err" < /dev/null || rc=$?
  cp "$t/out/$runs.out" "$t/stdout"; cp "$t/out/$runs.err" "$t/stderr"
  [ "$rc" = "$want" ] || fail "'$*' exited $rc, want $want: $(cat "$t/stdout" "$t/stderr")"
}
COMMON=(--context test-ctx --hcp-addr "$HCP_URL" --k8s-addr "$K8S_URL" --hcp-cacert "$CA" --k8s-cacert "$CA")
# The service goes before --secret below (the script takes services anywhere): a secret scanner reads
# `--secret <word>` on a command line as a credential.
has() { grep -Fqx -- "$1" "$t/stdout" || fail "no line '$1' in: $(cat "$t/stdout" "$t/stderr")"; }
requests() { cat "$t/srv/requests-$1.log" 2>/dev/null || true; }
no_requests() { [ -z "$(requests hcp)$(requests k8s)" ] || fail "$1: a Vault was asked: $(requests hcp) $(requests k8s)"; }

# 1. The cluster is named, every time. Not by KUBE_CONTEXT, not by kubectl's current context.
both
RUN_ENV=KUBE_CONTEXT=test-ctx run 2 --kv api --hcp-addr "$HCP_URL" --k8s-cacert "$CA"
grep -Fq -- '--context <ctx> is required' "$t/stderr" || fail "the refusal does not say what is missing: $(cat "$t/stderr")"
[ ! -s "$t/kubectl.calls" ] || fail "no --context still ran kubectl: $(cat "$t/kubectl.calls")"
no_requests "no --context"
run 2 --context ryft-prod-eu --kv api --hcp-addr "$HCP_URL" --k8s-cacert "$CA"
[ "$(cat "$t/kubectl.calls")" = "config get-contexts -o name" ] || fail "an unknown context: $(cat "$t/kubectl.calls")"
no_requests "an unknown context"
run 2 --context 'test-ctx;id' --kv api --hcp-addr "$HCP_URL" --k8s-cacert "$CA"
no_requests "a context that is not a name"

# 2. Refusals before any request: http, no CA for the in-cluster Vault, a token curl's config could not
#    carry (and it is not echoed), an unknown service, --save on --kv, an existing fingerprint file.
run 2 --context test-ctx --kv api --hcp-addr "http://127.0.0.1:1" --k8s-cacert "$CA"
run 2 --context test-ctx --kv api --hcp-addr "$HCP_URL" --k8s-addr "http://127.0.0.1:8200" --k8s-cacert "$CA"
run 2 --context test-ctx --kv api --hcp-addr "$HCP_URL"
grep -Fq 'no CA for the in-cluster Vault' "$t/stderr" || fail "no CA: $(cat "$t/stderr")"
BADTOK='hvs.x" -o /tmp/x "'$RANDOM
RUN_HCP_TOK=$BADTOK run 2 "${COMMON[@]}" --kv api
grep -Fq "HCP_VAULT_TOKEN has characters" "$t/stderr" || fail "a bad token: $(cat "$t/stderr")"
! grep -Fq "$BADTOK" "$t/stdout" "$t/stderr" || fail "a refused token was echoed"
run 2 "${COMMON[@]}" --kv no-such-svc
run 2 "${COMMON[@]}" --kv 'api;id'
run 2 "${COMMON[@]}" --kv api --save "$t/work/fp"
run 2 "${COMMON[@]}" api --secret --kv
no_requests "the refusals"
[ ! -e "$t/work/fp" ] || fail "a refused --save still wrote a file"

# 3. All equal: one line per key, exit 0. Each Vault got its own token only, HCP the service's
#    namespace, the in-cluster Vault none and the mount kv-<svc>.
run 0 "${COMMON[@]}" --kv api
for k in ADMIN_API_KEY BETA_GATE DATABASE_URL SUPABASE_PROJECT_REF; do has "api  config  $k  equal"; done
[ "$(wc -l < "$t/stdout" | tr -d ' ')" = 4 ] || fail "equal: more than the four keys: $(cat "$t/stdout")"
grep -Fq '4 keys, all equal' "$t/stderr" || fail "no summary: $(cat "$t/stderr")"
[ "$(requests hcp)" = '{"path": "/v1/kv/data/config", "ns": "admin/ryft/api", "token": "ok", "auth": false}' ] || fail "HCP request: $(requests hcp)"
[ "$(requests k8s)" = '{"path": "/v1/kv-api/data/config", "ns": null, "token": "ok", "auth": false}' ] || fail "in-cluster request: $(requests k8s)"
[ ! -s "$t/kubectl.calls" ] || [ "$(cat "$t/kubectl.calls")" = "config get-contexts -o name" ] || fail "--kv asked the cluster: $(cat "$t/kubectl.calls")"

# 4. One value differs (the canary, changed): that key says so, the others are equal, exit 1.
k8s_api ADMIN_API_KEY "C + '-other'"
run 1 "${COMMON[@]}" --kv api
has "api  config  ADMIN_API_KEY  differs"
has "api  config  DATABASE_URL  equal"
grep -Fq '4 keys, 1 not equal' "$t/stderr" || fail "summary: $(cat "$t/stderr")"
# The same value under another key is not equal either (the key is in the hash).
data k8s "{'|kv-api|config': dict($API, **{'ADMIN_API_KEY': 'on'})}"
run 1 "${COMMON[@]}" --kv api
has "api  config  ADMIN_API_KEY  differs"

# 5. A key missing on either side, and a whole secret missing on one side.
k8s_api BETA_GATE None
run 1 "${COMMON[@]}" --kv api
has "api  config  BETA_GATE  missing in k8s"
has "api  config  DATABASE_URL  equal"
k8s_api NEW_KEY "C + '-new'"
run 1 "${COMMON[@]}" --kv api
has "api  config  NEW_KEY  missing in HCP"
data k8s "{}"
run 1 "${COMMON[@]}" --kv api
for k in ADMIN_API_KEY BETA_GATE DATABASE_URL SUPABASE_PROJECT_REF; do has "api  config  $k  missing in k8s"; done
data k8s "{'|kv-api|config': {}}"
run 1 "${COMMON[@]}" --kv api
has "api  config  ADMIN_API_KEY  missing in k8s"
data hcp "{}"; data k8s "{}"
run 1 "${COMMON[@]}" --kv api
has "api  config  -  missing in HCP and k8s"

# 6. --all: every service with its paths (the broker's amqp-user-*), plus operator/demo. The shared
#    SUPABASE_PROJECT_REF is compared per service: only ingest's copy differs.
both
run 0 "${COMMON[@]}" --all
for l in "api  config  SUPABASE_PROJECT_REF  equal" "ingest  config  SUPABASE_PROJECT_REF  equal" \
  "rabbitmq  amqp-user-api  password  equal" "rabbitmq  amqp-user-ingest  username  equal" "operator  demo  password  equal"; do has "$l"; done
grep -Fq '"path": "/v1/operator/data/demo", "ns": "admin/ryft"' "$t/srv/requests-hcp.log" || fail "operator/demo on HCP: $(requests hcp)"
grep -Fq '"path": "/v1/kv-operator/data/demo", "ns": null' "$t/srv/requests-k8s.log" || fail "kv-operator/demo: $(requests k8s)"
data k8s "{'|kv-api|config': $API, '|kv-ingest|config': dict($INGEST, SUPABASE_PROJECT_REF='zzzzzzzzzz99'),
  '|kv-rabbitmq|amqp-user-api': {'username': 'api', 'password': C + 'a'},
  '|kv-rabbitmq|amqp-user-ingest': {'username': 'ingest', 'password': C + 'i'},
  '|kv-operator|demo': {'email': 'review@example.com', 'password': C + 'd'}}"
run 1 "${COMMON[@]}" --all
has "api  config  SUPABASE_PROJECT_REF  equal"
has "ingest  config  SUPABASE_PROJECT_REF  differs"
has "ingest  config  BETA_GATE  equal"

# 7. A Vault that cannot be read is an error (2), never a difference or "equal": wrong token, a server
#    whose certificate is not trusted (no CA given for HCP: the system's trust), nothing listening.
both
RUN_HCP_TOK=$CE_TOK run 2 "${COMMON[@]}" --kv api
grep -Fq 'refused the read (HTTP 403' "$t/stderr" || fail "403: $(cat "$t/stderr")"
[ ! -s "$t/stdout" ] || fail "a 403 still printed a comparison: $(cat "$t/stdout")"
run 2 --context test-ctx --hcp-addr "$HCP_URL" --k8s-addr "$K8S_URL" --k8s-cacert "$CA" --kv api
[ -z "$(requests k8s)" ] || fail "after HCP failed, the in-cluster Vault was still asked"
run 2 "${COMMON[@]}" --k8s-addr "https://127.0.0.1:1" --kv api
grep -Fq 'the in-cluster Vault gave no answer' "$t/stderr" || fail "nothing listening: $(cat "$t/stderr")"
# A redirect is not followed: the HCP token never goes where a Location header points.
data hcp "{'admin/ryft/api|kv|config': {'__redirect__': '$K8S_URL/v1/kv-api/data/config'}}"
run 2 "${COMMON[@]}" --kv api
grep -Fq 'HCP Vault answered HTTP 307' "$t/stderr" || fail "a redirect: $(cat "$t/stderr")"
[ -z "$(requests k8s)" ] || fail "a redirect was followed: $(requests k8s)"

# 8. --secret: the synced Secret against the in-cluster Vault (or HCP), through the named context.
both
secret ryft-api ryft-secrets "$API"
run 0 "${COMMON[@]}" api --secret
has "api  ryft-secrets  DATABASE_URL  equal"
[ "$(cat "$t/kubectl.calls")" = "config get-contexts -o name
--context test-ctx -n ryft-api get secret ryft-secrets --ignore-not-found -o json" ] || fail "kubectl: $(cat "$t/kubectl.calls")"
[ -z "$(requests hcp)" ] || fail "--secret (k8s) asked HCP: $(requests hcp)"
run 0 "${COMMON[@]}" api --secret --vault hcp
[ -z "$(requests k8s)" ] || fail "--secret --vault hcp asked the in-cluster Vault: $(requests k8s)"
secret ryft-api ryft-secrets "dict($API, ADMIN_API_KEY=C + '-rotated', EXTRA='x')"
run 1 "${COMMON[@]}" api --secret
has "api  ryft-secrets  ADMIN_API_KEY  differs"
has "api  ryft-secrets  EXTRA  missing in Vault"
rm -f "$t/secrets/ryft-api/ryft-secrets.json"
run 1 "${COMMON[@]}" api --secret
has "api  ryft-secrets  DATABASE_URL  missing in Secret"
secret ryft-rabbitmq amqp-user-api "{'username': 'api', 'password': C + 'a'}"
secret ryft-rabbitmq amqp-user-ingest "{'username': 'ingest', 'password': C + 'i'}"
run 0 "${COMMON[@]}" rabbitmq --secret
has "rabbitmq  amqp-user-ingest  password  equal"
run 2 "${COMMON[@]}" operator --secret

# 9. --save, then --against: no Vault and no token needed; the file is 0600, holds no value and no
#    unsalted hash, is never overwritten, and its salt is new on every save.
secret ryft-api ryft-secrets "$API"
RUN_NOTOK=1 run 0 --context test-ctx api --secret --save "$t/work/fp"
no_requests "--save"
# shellcheck disable=SC2012 # the mode string, from a name this test made
[ "$(ls -l "$t/work/fp" | cut -c1-10)" = "-rw-------" ] || fail "the fingerprint's mode: $(ls -l "$t/work/fp")"
[ "$(grep -c "^ryft-secrets"$'\t' "$t/work/fp")" = 4 ] || fail "the fingerprint: $(cat "$t/work/fp")"
for v in "$CANARY-admin" "abcdefghij12"; do
  ! grep -Fq "$(sha "$v")" "$t/work/fp" || fail "the fingerprint holds an unsalted hash"
done
run 2 "${COMMON[@]}" api --secret --save "$t/work/fp"
grep -Fq 'never overwritten' "$t/stderr" || fail "overwrite: $(cat "$t/stderr")"
run 0 --context test-ctx api --secret --save "$t/work/fp2"
[ "$(grep '^salt ' "$t/work/fp")" != "$(grep '^salt ' "$t/work/fp2")" ] || fail "two saves share a salt"
RUN_NOTOK=1 run 0 --context test-ctx api --secret --against "$t/work/fp"
has "api  ryft-secrets  ADMIN_API_KEY  equal"
no_requests "--against"
secret ryft-api ryft-secrets "dict($API, BETA_GATE='off')"
run 1 --context test-ctx api --secret --against "$t/work/fp"
has "api  ryft-secrets  BETA_GATE  differs"
# The key is in the hash: two keys with the same value get different fingerprints (the file does not
# tell which values are equal).
secret ryft-api ryft-secrets "{'BETA_GATE': 'on', 'SAME_AS_BETA': 'on'}"
run 0 --context test-ctx api --secret --save "$t/work/fp3"
[ "$(cut -f3 "$t/work/fp3" | grep -E '^[0-9a-f]{64}$' | sort -u | wc -l | tr -d ' ')" = 2 ] || fail "equal values share a fingerprint: $(cat "$t/work/fp3")"
run 2 --context test-ctx ingest --secret --against "$t/work/fp"
grep -Fq 'not a fingerprint of ingest' "$t/stderr" || fail "a foreign fingerprint: $(cat "$t/stderr")"

# 10. The caller's tracing and a verbose ~/.curlrc change nothing (the leak scan below covers these runs).
printf 'verbose\ntrace-ascii = %s\n' "$t/home/curl-trace" > "$t/home/.curlrc"
both
k8s_api ADMIN_API_KEY "C + '-other'"
RUN_BASH="bash -x" run 1 "${COMMON[@]}" --kv api
has "api  config  ADMIN_API_KEY  differs"
RUN_ENV=SHELLOPTS=xtrace:allexport run 1 "${COMMON[@]}" --kv api
has "api  config  ADMIN_API_KEY  differs"
[ ! -e "$t/home/curl-trace" ] || fail "curl read ~/.curlrc"

# 11. --list over the real generated objects (no cluster, no Vault, no context needed).
VAULT_DIFF_OVERLAYS=$REPO/deploy/k8s/overlays/vault-namespaces run 0 --list
has "api  ryft-api  ryft-api/ryft-secrets  kv-api/config"
for u in api ingest notify scoring; do has "rabbitmq  ryft-rabbitmq  ryft-rabbitmq/amqp-user-$u  kv-rabbitmq/amqp-user-$u"; done
run 0 --help
grep -Fq -- '--context <ctx> --kv <svc>' "$t/stdout" || fail "--help"

# The leak scan, over every run above.
[ -s "$t/spawn.log" ] || fail "no process was recorded (the wrappers are not on PATH)"
[ -s "$t/srv/ps.log" ] || fail "the fake Vaults recorded no ps"
# The live view must have seen most requests (a few stalled snapshots are tolerated, not a silent zero).
ps_ok=$(grep -c . "$t/srv/ps.ok" 2>/dev/null || true); ps_stalled=$(grep -c . "$t/srv/ps.stalled" 2>/dev/null || true)
[ "${ps_ok:-0}" -ge 20 ] && [ "${ps_stalled:-0}" -le $(( ${ps_ok:-0} / 4 )) ] || fail "ps snapshots: ${ps_ok:-0} taken, ${ps_stalled:-0} stalled"
grep -Fq -- "--config -" "$t/srv/ps.log" || fail "no ps snapshot caught the script's curl"
grep -q '^argv: .*curl -q --config -' "$t/spawn.log" || fail "curl was not recorded"
for needle in "$CANARY" "$CANARY64" "$HCP_TOK" "$CE_TOK"; do
  for f in "$t"/out/*; do ! grep -Fq -- "$needle" "$f" || fail "a secret in the output of $f: $(cat "$f")"; done
  ! grep -Fq -- "$needle" "$t/spawn.log" || fail "a secret in the argv or environment of a process: $(grep -F -- "$needle" "$t/spawn.log" | cut -c1-200)"
  ! grep -Fq -- "$needle" "$t/srv/ps.log" || fail "a secret in ps: $(grep -F -- "$needle" "$t/srv/ps.log" | cut -c1-200)"
  ! grep -rFq -- "$needle" "$t/tmp" "$t/home" "$t/work" || fail "a secret on disk: $(grep -rlF -- "$needle" "$t/tmp" "$t/home" "$t/work")"
done
[ -z "$(ls -A "$t/tmp")" ] || fail "files left in TMPDIR: $(ls -A "$t/tmp")"
# shellcheck disable=SC2012 # names this test made
[ "$(ls -A "$t/work" | sort | tr '\n' ' ')" = "fp fp2 fp3 " ] || fail "files in the working directory: $(ls -A "$t/work")"
! grep -q '"auth": true' "$t/srv/all-requests.log" || fail "an Authorization header was sent"
# No hash is printed either (a fingerprint is for comparing, not for showing).
! grep -Eq '[0-9a-f]{64}' "$t"/out/* || fail "a hash in the output: $(grep -El '[0-9a-f]{64}' "$t"/out/*)"
echo "✓ vault_diff_test: $runs runs"
