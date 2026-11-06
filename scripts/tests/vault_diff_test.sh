#!/usr/bin/env bash
# scripts/vault_diff.sh against two fake Vaults (HTTPS servers on 127.0.0.1 with a CA made here) and a
# fake kubectl (make test-scripts). Nothing here reaches a cluster or a real Vault: KUBECONFIG=/dev/null,
# the fakes first on PATH, every address a loopback port of this run, and every run has a dead proxy in
# HTTPS_PROXY, https_proxy and ALL_PROXY (curl must not use it). It checks:
# - the comparison (equal, differs, missing on either side, a whole secret missing, shared secrets per
#   service, the broker's amqp-user-* and operator/demo, a Secret against either Vault) and the exit codes;
# - the refusals before any request: no --context or an unknown one, an HCP address other than the one
#   the generated VaultConnections pin (or, once none does, one outside hashicorp.cloud), generated
#   addresses that disagree, an in-cluster address that is not https loopback, http, no CA, a token
#   curl's config could not carry;
# - TLS: a server signed by another CA (given as either --cacert), the system trust for HCP, and
#   CURL_CA_BUNDLE / SSL_CERT_FILE / SSL_CERT_DIR in the environment ignored; no redirect followed;
# - the prompt, in a pseudo-terminal: no echo, each token to its own Vault, echo back after Ctrl-C,
#   Ctrl-\, SIGHUP and SIGTERM (with every bash found: 3.2 on macOS, 5 in CI);
# - no core file: every process the script starts has a core limit of 0;
# - no leak: a canary value, the base64 of every fixture Secret value, both tokens, a VAULT_TOKEN and a
#   ~/.vault-token are looked for in every stdout and stderr, in the argv and environment of every
#   process the script starts (each one is wrapped and recorded), in `ps` as the fake servers see it
#   during each request, and in every file the run could write (TMPDIR, HOME, the working directory);
#   also under the caller's `bash -x`, SHELLOPTS=xtrace:allexport, a verbose ~/.curlrc, SSLKEYLOGFILE
#   and a CDPATH that would move the script's own `cd`;
# - lib/vault_diff.py directly: the comparer's exit codes and the keyed, salted hash.
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
mkdir -p "$t/bin" "$t/srv" "$t/tmp" "$t/home" "$t/work" "$t/out" "$t/overlays" "$t/secrets" "$t/cdp/scripts/lib"
PY=$(command -v python3)
export KUBECONFIG=/dev/null FAKE_DIR=$t
unset KUBE_CONTEXT VAULT_TOKEN HCP_VAULT_TOKEN CE_VAULT_TOKEN VAULT_ADDR_HCP VAULT_ADDR_K8S VAULT_CACERT VAULT_CACERT_HCP \
  HCP_VAULT_NAMESPACE_ROOT VAULT_DIFF_OVERLAYS NO_PROXY no_proxy CURL_CA_BUNDLE SSL_CERT_FILE SSL_CERT_DIR SSLKEYLOGFILE CDPATH
# No core files from the test's own processes (only run() raises the soft limit, for the script).
ulimit -S -c 0

# Made up at run time; a hit anywhere below is a leak.
CANARY=canary-$RANDOM$RANDOM-do-not-print-$RANDOM$RANDOM
HCP_TOK=hvs.HCPtok$RANDOM$RANDOM$RANDOM
CE_TOK=hvs.CEtok$RANDOM$RANDOM$RANDOM
VT_ENV=hvs.VTenv$RANDOM$RANDOM$RANDOM    # a VAULT_TOKEN the caller happens to have
VT_FILE=hvs.VTfile$RANDOM$RANDOM$RANDOM  # and a ~/.vault-token
printf '%s' "$VT_FILE" > "$t/home/.vault-token"
: > "$t/needles64"  # the base64 of every fixture Secret value (secret() adds them)
sha() { printf '%s' "$1" | "$PY" -c 'import hashlib, sys; print(hashlib.sha256(sys.stdin.buffer.read()).hexdigest())'; }

# Every external program the script may start is a recorder in front of the real one: its argv and
# its whole environment go to spawn.log, and its core-file limit (the script sets 0; run() raises it
# first where the hard limit allows). The fakes record themselves the same way.
for tool in curl python3 od tr sed grep awk dirname rm cat stty; do
  real=$(command -v "$tool")
  cat > "$t/bin/$tool" <<W
#!/bin/sh
{ printf 'argv: %s' "\$0"; for a in "\$@"; do printf ' %s' "\$a"; done; printf '\n'; printf 'core: %s\n' "\$(ulimit -c)"; printf "env:\n"; /usr/bin/env; } >> "\$FAKE_DIR/spawn.log"
exec "$real" "\$@"
W
done
# kubectl: three contexts; `get secret` answers from $t/secrets/<ns>/<name>.json (none: nothing, as
# with --ignore-not-found); anything else is refused.
cat > "$t/bin/kubectl" <<'K'
#!/bin/sh
{ printf 'argv: kubectl'; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; printf 'core: %s\n' "$(ulimit -c)"; printf "env:\n"; /usr/bin/env; } >> "$FAKE_DIR/spawn.log"
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
# What a CDPATH could make the script's own `cd scripts` find instead: never run.
printf 'import os\nopen(os.path.join(os.environ["FAKE_DIR"], "cdpath-used"), "w").write("x")\n' > "$t/cdp/scripts/lib/vault_diff.py"

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
class S(http.server.ThreadingHTTPServer):
    def handle_error(self, request, client_address):  # one line: the TLS refusals here are expected
        sys.stderr.write("%s: %s\n" % (name, sys.exc_info()[0].__name__))
s = S(("127.0.0.1", 0), H)
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(cert, key)
s.socket = ctx.wrap_socket(s.socket, server_side=True, do_handshake_on_connect=False)  # the handshake in the handler thread
open(os.path.join(d, "port-%s.tmp" % name), "w").write(str(s.server_address[1]))
os.rename(os.path.join(d, "port-%s.tmp" % name), os.path.join(d, "port-%s" % name))
s.serve_forever()
PY
# The servers' CA, and a second one that signed nothing here.
for c in ca ca2; do
  openssl req -x509 -newkey rsa:2048 -nodes -keyout "$t/srv/$c-key.pem" -out "$t/srv/$c.pem" -days 1 -subj /CN=127.0.0.1 \
    -addext subjectAltName=IP:127.0.0.1 -addext basicConstraints=critical,CA:TRUE 2>/dev/null
done
CA=$t/srv/ca.pem
CA2=$t/srv/ca2.pem
start() { # <name> <token>
  "$PY" -I "$t/srv/vault_fake.py" "$1" "$(sha "$2")" - "$t/srv" "$CA" "$t/srv/ca-key.pem" 2>>"$t/srv/server.err" &
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
IN_CLUSTER=https://vault.vault.svc:8200

# A fixture of the generated VSO objects (the generator's shape): api and the broker still on HCP (the
# fake HCP's address in their VaultConnection), ingest already in the in-cluster shape.
vso() { # <dir> <svc> <ns> <address> <mount> <path>:<secret>...
  local dir=$1 svc=$2 ns=$3 addr=$4 mount=$5 p; shift 5
  mkdir -p "$dir/$svc"
  {
    echo "# Generated by infra/vault — do not edit by hand (terraform apply rewrites it)."
    printf 'apiVersion: secrets.hashicorp.com/v1beta1\nkind: VaultConnection\nmetadata:\n  name: vault\n  namespace: %s\nspec:\n  address: %s\n  skipTLSVerify: false\n' "$ns" "$addr"
    printf -- '---\napiVersion: secrets.hashicorp.com/v1beta1\nkind: VaultAuth\nmetadata:\n  name: %s\n  namespace: %s\nspec:\n  vaultConnectionRef: vault\n  method: kubernetes\n  mount: kubernetes\n' "$svc" "$ns"
    for p in "$@"; do
      printf -- '---\napiVersion: secrets.hashicorp.com/v1beta1\nkind: VaultStaticSecret\nmetadata:\n  name: %s\n  namespace: %s\n  annotations: { argocd.argoproj.io/sync-wave: "1" }\nspec:\n  vaultAuthRef: %s\n  mount: %s\n  type: kv-v2\n  path: %s\n  refreshAfter: 1h\n  destination:\n    name: %s\n    create: true\n    labels:\n      rabbitmq.com/topology-operator: "true"\n    transformation:\n      excludeRaw: true\n' \
        "${p#*:}" "$ns" "$svc" "$mount" "${p%%:*}" "${p#*:}"
    done
  } > "$dir/$svc/vault.yaml"
}
vso "$t/overlays" api ryft-api "$HCP_URL" kv config:ryft-secrets
vso "$t/overlays" ingest ryft-ingest "$IN_CLUSTER" kv-ingest config:ryft-secrets
vso "$t/overlays" rabbitmq ryft-rabbitmq "$HCP_URL" kv amqp-user-api:amqp-user-api amqp-user-ingest:amqp-user-ingest
export VAULT_DIFF_OVERLAYS=$t/overlays
# Two HCP addresses in the generated objects; and every namespace already in the cluster.
vso "$t/ov-disagree" api ryft-api "$HCP_URL" kv config:ryft-secrets
vso "$t/ov-disagree" ingest ryft-ingest "https://other.hashicorp.cloud:8200" kv config:ryft-secrets
vso "$t/ov-ce" api ryft-api "$IN_CLUSTER" kv-api config:ryft-secrets

# The data: one JSON per side; HCP keys carry the namespace, the in-cluster ones none.
data() { # <hcp|k8s> <python dict literal over the variables C (canary)>
  C=$CANARY "$PY" -c 'import json, os, sys; C = os.environ["C"]; json.dump(eval(sys.argv[2]), open(sys.argv[1], "w"))' "$t/srv/data-$1.json" "$2"
}
API='{"DATABASE_URL": "postgresql://u:" + C + "@db/x", "ADMIN_API_KEY": C + "-admin", "SUPABASE_PROJECT_REF": "abcdefghij12", "BETA_GATE": "on"}'
INGEST='{"DATABASE_URL": "postgresql://i:" + C + "@db/x", "SUPABASE_PROJECT_REF": "abcdefghij12", "BETA_GATE": "on"}'
both() { # the same everywhere (dual-write)
  data hcp "{'admin/ryft/api|kv|config': $API, 'admin/ryft/ingest|kv|config': $INGEST,
    'admin/ryft/rabbitmq|kv|amqp-user-api': {'username': 'api', 'password': C + 'a'},
    'admin/ryft/rabbitmq|kv|amqp-user-ingest': {'username': 'ingest', 'password': C + 'i'},
    'admin/ryft|operator|demo': {'email': 'review@example.com', 'password': C + 'd'}}"
  data k8s "{'|kv-api|config': $API, '|kv-ingest|config': $INGEST,
    '|kv-rabbitmq|amqp-user-api': {'username': 'api', 'password': C + 'a'},
    '|kv-rabbitmq|amqp-user-ingest': {'username': 'ingest', 'password': C + 'i'},
    '|kv-operator|demo': {'email': 'review@example.com', 'password': C + 'd'}}"
}
# k8s_api <key> <python expression>: the in-cluster api config with one change (None: the key removed).
k8s_api() {
  data k8s "{'|kv-api|config': {k: v for k, v in dict($API, **{'$1': $2}).items() if v is not None}}"
}
# secret <ns> <name> <python dict literal>: the Secret kubectl returns (base64 data, as the API does).
# The base64 of each value of 6 bytes or more becomes a needle for the leak scan: the canary's own
# base64 depends on where it sits in a value, so the canary alone would catch a leaked Secret by luck.
# (Shorter values, like "on", encode to 4 characters that could turn up anywhere by chance.)
secret() {
  mkdir -p "$t/secrets/$1"
  C=$CANARY "$PY" -c 'import base64, json, os, sys; C = os.environ["C"]
d = eval(sys.argv[2]); b = {k: base64.b64encode(v.encode()).decode() for k, v in d.items()}
json.dump({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "x"}, "type": "Opaque", "data": b}, open(sys.argv[1], "w"))
with open(sys.argv[3], "a") as f:
    f.writelines(x + "\n" for k, x in b.items() if len(d[k]) >= 6)' "$t/secrets/$1/$2.json" "$3" "$t/needles64"
}

# run <want rc> <args...>: the script as an operator runs it, the tokens in its environment only
# (RUN_HCP_TOK / RUN_CE_TOK replace them; RUN_ENV="<name>=<value> ..." adds variables; RUN_BASH="bash -x"
# traces; RUN_REL=1 starts it as scripts/vault_diff.sh from the tree's root). Every run also has a
# VAULT_TOKEN, a dead proxy and SSLKEYLOGFILE. Every run's stdout and stderr are kept for the leak scan.
runs=0
DEAD_PROXY=http://127.0.0.1:9
run() {
  local want=$1 rc=0; shift
  runs=$((runs + 1))
  : > "$t/kubectl.calls"
  rm -f "$t/srv/requests-"*.log
  (
    cd "$t/work"
    # Core files allowed in the caller (where the hard limit permits): the script must lower it.
    ulimit -S -c unlimited 2>/dev/null || true
    # shellcheck disable=SC2030 # the run's own environment, on purpose in this subshell
    export HOME="$t/home" TMPDIR="$t/tmp" PATH="$t/bin:$PATH" VAULT_TOKEN="$VT_ENV" SSLKEYLOGFILE="$t/home/tls-keys.log" \
      HTTPS_PROXY=$DEAD_PROXY https_proxy=$DEAD_PROXY ALL_PROXY=$DEAD_PROXY all_proxy=$DEAD_PROXY
    export HCP_VAULT_TOKEN="${RUN_HCP_TOK:-$HCP_TOK}" CE_VAULT_TOKEN="${RUN_CE_TOK:-$CE_TOK}"
    # A prefix assignment on a function call is exported: only the two variables above carry a token.
    unset RUN_HCP_TOK RUN_CE_TOK
    s=$SCRIPT
    if [ -n "${RUN_REL:-}" ]; then cd "$ROOT"; s=scripts/vault_diff.sh; fi
    # env(1) for the extra variables: SHELLOPTS is read-only inside bash. Only they are in its argv.
    # shellcheck disable=SC2086 # RUN_ENV is a list of assignments, RUN_BASH a command with its options
    exec /usr/bin/env ${RUN_ENV:-} ${RUN_BASH:-bash} "$s" "$@"
  ) > "$t/out/$runs.out" 2> "$t/out/$runs.err" < /dev/null || rc=$?
  cp "$t/out/$runs.out" "$t/stdout"; cp "$t/out/$runs.err" "$t/stderr"
  [ "$rc" = "$want" ] || fail "'$*' exited $rc, want $want: $(cat "$t/stdout" "$t/stderr")"
}
COMMON=(--context test-ctx --hcp-addr "$HCP_URL" --k8s-addr "$K8S_URL" --hcp-cacert "$CA" --k8s-cacert "$CA")
# The service goes before --secret below (the script takes services anywhere): a secret scanner reads
# `--secret <word>` on a command line as a credential.
has() { grep -Fqx -- "$1" "$t/stdout" || fail "no line '$1' in: $(cat "$t/stdout" "$t/stderr")"; }
says() { grep -Fq -- "$1" "$t/stderr" || fail "stderr does not say '$1': $(cat "$t/stderr")"; }
requests() { cat "$t/srv/requests-$1.log" 2>/dev/null || true; }
no_requests() { [ -z "$(requests hcp)$(requests k8s)" ] || fail "$1: a Vault was asked: $(requests hcp) $(requests k8s)"; }

# 1. The cluster is named, every time. Not by KUBE_CONTEXT, not by kubectl's current context.
both
RUN_ENV=KUBE_CONTEXT=test-ctx run 2 --kv api --hcp-addr "$HCP_URL" --k8s-cacert "$CA"
says '--context <ctx> is required'
[ ! -s "$t/kubectl.calls" ] || fail "no --context still ran kubectl: $(cat "$t/kubectl.calls")"
no_requests "no --context"
run 2 --context ryft-prod-eu --kv api --hcp-addr "$HCP_URL" --k8s-cacert "$CA"
[ "$(cat "$t/kubectl.calls")" = "config get-contexts -o name" ] || fail "an unknown context: $(cat "$t/kubectl.calls")"
no_requests "an unknown context"
run 2 --context 'test-ctx;id' --kv api --hcp-addr "$HCP_URL" --k8s-cacert "$CA"
no_requests "a context that is not a name"

# 2. Refusals before any request: http, an in-cluster address that is not your port-forward, no CA
#    for the in-cluster Vault, a token curl's config could not carry (not echoed), an unknown service,
#    the removed --save and --against.
run 2 --context test-ctx --kv api --hcp-addr "http://127.0.0.1:1" --k8s-cacert "$CA"
for a in "http://127.0.0.1:8200" "https://10.0.0.1:8200" "$IN_CLUSTER" "https://127.0.0.1.example.com:8200" "https://localhost:8200/x"; do
  run 2 "${COMMON[@]}" --k8s-addr "$a" --kv api
  says '--k8s-addr must be your port-forward'
done
run 2 --context test-ctx --kv api --hcp-addr "$HCP_URL"
says 'no CA for the in-cluster Vault'
BADTOK='hvs.x" -o /tmp/x "'$RANDOM
RUN_HCP_TOK=$BADTOK run 2 "${COMMON[@]}" --kv api
says "HCP_VAULT_TOKEN has characters"
! grep -Fq "$BADTOK" "$t/stdout" "$t/stderr" || fail "a refused token was echoed"
run 2 "${COMMON[@]}" --kv no-such-svc
run 2 "${COMMON[@]}" --kv 'api;id'
run 2 "${COMMON[@]}" api --secret --save "$t/work/fp"
run 2 "${COMMON[@]}" api --secret --against "$t/work/fp"
run 2 "${COMMON[@]}" api --secret --kv
no_requests "the refusals"

# 3. The HCP token's destination is pinned: the generated VaultConnections' HCP address (the default,
#    and the only one accepted), one shared value, and after the last cutover only hashicorp.cloud.
#    Both destinations are shown before any token is used.
run 2 "${COMMON[@]}" --hcp-addr "https://127.0.0.2:${HCP_URL##*:}" --kv api
says "is not HCP's address in the generated VaultConnections ($HCP_URL)"
run 2 "${COMMON[@]}" --hcp-addr "https://localhost:${HCP_URL##*:}" --kv api
VAULT_DIFF_OVERLAYS=$REPO/deploy/k8s/overlays/vault-namespaces run 2 "${COMMON[@]}" --kv api
says "is not HCP's address in the generated VaultConnections (https://"
VAULT_DIFF_OVERLAYS=$t/ov-disagree run 2 "${COMMON[@]}" --kv api
says 'do not agree on one HCP address'
VAULT_DIFF_OVERLAYS=$t/ov-ce run 2 "${COMMON[@]}" --kv api
says 'must be https://<name>.hashicorp.cloud[:8200]'
VAULT_DIFF_OVERLAYS=$t/ov-ce run 2 --context test-ctx --k8s-addr "$K8S_URL" --k8s-cacert "$CA" --kv api
says 'no HCP address'
no_requests "a foreign HCP address"
run 0 --context test-ctx --k8s-addr "$K8S_URL" --hcp-cacert "$CA" --k8s-cacert "$CA" --kv api
says "-> HCP: ${HCP_URL#https://}"
says "-> in-cluster: ${K8S_URL#https://}"
says "HCP_VAULT_TOKEN: taken from the environment"
says "CE_VAULT_TOKEN: taken from the environment"

# 4. All equal: one line per key, exit 0. Each Vault got its own token only, HCP the service's
#    namespace, the in-cluster Vault none and the mount kv-<svc>; the dead proxy was not used.
run 0 "${COMMON[@]}" --kv api
for k in ADMIN_API_KEY BETA_GATE DATABASE_URL SUPABASE_PROJECT_REF; do has "api  config  $k  equal"; done
[ "$(wc -l < "$t/stdout" | tr -d ' ')" = 4 ] || fail "equal: more than the four keys: $(cat "$t/stdout")"
says '4 keys, all equal'
[ "$(requests hcp)" = '{"path": "/v1/kv/data/config", "ns": "admin/ryft/api", "token": "ok", "auth": false}' ] || fail "HCP request: $(requests hcp)"
[ "$(requests k8s)" = '{"path": "/v1/kv-api/data/config", "ns": null, "token": "ok", "auth": false}' ] || fail "in-cluster request: $(requests k8s)"
[ ! -s "$t/kubectl.calls" ] || [ "$(cat "$t/kubectl.calls")" = "config get-contexts -o name" ] || fail "--kv asked the cluster: $(cat "$t/kubectl.calls")"
# Started by a relative path with a CDPATH that has a `scripts` of its own: the script's `cd` stays home.
RUN_REL=1 RUN_ENV="CDPATH=$t/cdp" run 0 "${COMMON[@]}" --kv api
[ ! -e "$t/cdpath-used" ] || fail "CDPATH moved the script's cd"

# 5. One value differs (the canary, changed): that key says so, the others are equal, exit 1.
k8s_api ADMIN_API_KEY "C + '-other'"
run 1 "${COMMON[@]}" --kv api
has "api  config  ADMIN_API_KEY  differs"
has "api  config  DATABASE_URL  equal"
says '4 keys, 1 not equal'
data k8s "{'|kv-api|config': dict($API, **{'ADMIN_API_KEY': 'on'})}"
run 1 "${COMMON[@]}" --kv api
has "api  config  ADMIN_API_KEY  differs"

# 6. A key missing on either side, and a whole secret missing on one side.
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

# 7. --all: every service with its paths (the broker's amqp-user-*), plus operator/demo. The shared
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

# 8. A Vault that cannot be read is an error (2), never a difference or "equal": a wrong token; a
#    server signed by another CA, given as either --cacert; HCP on the system's trust (a CA in
#    CURL_CA_BUNDLE, SSL_CERT_FILE or SSL_CERT_DIR does not count); nothing listening; a redirect.
both
RUN_HCP_TOK=$CE_TOK run 2 "${COMMON[@]}" --kv api
says 'refused the read (HTTP 403'
[ ! -s "$t/stdout" ] || fail "a 403 still printed a comparison: $(cat "$t/stdout")"
run 2 "${COMMON[@]}" --k8s-cacert "$CA2" --kv api
says 'the in-cluster Vault gave no answer'
[ -z "$(requests k8s)" ] || fail "a server the given CA did not sign was asked: $(requests k8s)"
run 2 "${COMMON[@]}" --hcp-cacert "$CA2" --kv api
says 'HCP Vault gave no answer'
no_requests "a server the given HCP CA did not sign"
run 2 --context test-ctx --hcp-addr "$HCP_URL" --k8s-addr "$K8S_URL" --k8s-cacert "$CA" --kv api
no_requests "HCP on the system's trust"
RUN_ENV="CURL_CA_BUNDLE=$CA SSL_CERT_FILE=$CA SSL_CERT_DIR=$t/srv" run 2 --context test-ctx --hcp-addr "$HCP_URL" --k8s-addr "$K8S_URL" --k8s-cacert "$CA" --kv api
no_requests "a CA from the environment"
run 2 "${COMMON[@]}" --k8s-addr "https://127.0.0.1:1" --kv api
says 'the in-cluster Vault gave no answer'
data hcp "{'admin/ryft/api|kv|config': {'__redirect__': '$K8S_URL/v1/kv-api/data/config'}}"
run 2 "${COMMON[@]}" --kv api
says 'HCP Vault answered HTTP 307'
[ -z "$(requests k8s)" ] || fail "a redirect was followed: $(requests k8s)"

# 9. --secret: the synced Secret against the in-cluster Vault (or HCP), through the named context.
#    Before a cutover `--kv` and `--secret --vault hcp` are equal, after it `--secret` (the chain in --help).
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
secret ryft-api ryft-secrets "$API"

# 10. The caller's tracing and a verbose ~/.curlrc change nothing (the leak scan below covers these runs).
printf 'verbose\ntrace-ascii = %s\n' "$t/home/curl-trace" > "$t/home/.curlrc"
both
k8s_api ADMIN_API_KEY "C + '-other'"
RUN_BASH="bash -x" run 1 "${COMMON[@]}" --kv api
has "api  config  ADMIN_API_KEY  differs"
RUN_ENV=SHELLOPTS=xtrace:allexport run 1 "${COMMON[@]}" --kv api
has "api  config  ADMIN_API_KEY  differs"
both
RUN_BASH="bash -x" run 0 "${COMMON[@]}" api --secret
[ ! -e "$t/home/curl-trace" ] || fail "curl read ~/.curlrc"
rm -f "$t/home/.curlrc"

# 11. The prompt, in a pseudo-terminal, with every bash there is: nothing echoed, each token to its own
#     Vault; Ctrl-C, Ctrl-\, SIGHUP and SIGTERM at the prompt exit 130, 131, 129 and 143 and leave
#     the terminal echoing. The tokens reach the driver on its stdin, never in its argv.
cat > "$t/srv/pty_drive.py" <<'PY'
import os, pty, select, signal, sys, termios, time
mode, out, argv = sys.argv[1], sys.argv[2], sys.argv[3:]
def echo_off(fd, deadline=10):
    # bash prints read's prompt before it turns echo off: type only once the terminal stopped echoing
    # (or after the deadline, so a read without -s still shows the token and fails the test).
    stop = time.time() + deadline
    while time.time() < stop and termios.tcgetattr(fd)[3] & termios.ECHO:
        time.sleep(0.02)
toks = [l.rstrip("\n").encode() for l in sys.stdin.readlines()]
if mode != "prompt":
    # mode is INT (Ctrl-C), QUIT (Ctrl-\), or HUP or TERM (sent to the terminal's process group). The
    # shell around the script traps HUP, QUIT and TERM so it lives to print the script's rc and the
    # terminal's state; a trapped signal is back to its default in the script.
    argv = [argv[0], "-c", 'trap : HUP QUIT TERM; "$0" "$@"; echo "rc=$?"; stty -a'] + argv
pid, fd = pty.fork()
if pid == 0:
    os.execv(argv[0], argv)
buf, sent, end = b"", 0, time.time() + 60
while time.time() < end:
    r, _, _ = select.select([fd], [], [], 0.2)
    if r:
        try:
            chunk = os.read(fd, 4096)
        except OSError:
            break
        if not chunk:
            break
        buf += chunk
    if sent < 2 and buf.count(b"_VAULT_TOKEN: ") > sent:
        echo_off(fd)
        if mode == "prompt":
            os.write(fd, toks[sent] + b"\n"); sent += 1
        else:
            if mode in ("HUP", "TERM"):
                os.killpg(pid, getattr(signal, "SIG" + mode))  # pty.fork's child leads its process group
            else:
                os.write(fd, b"\x03" if mode == "INT" else b"\x1c")
            sent = 2
else:
    os.kill(pid, 9)
open(out, "wb").write(buf)
_, st = os.waitpid(pid, 0)
sys.exit(os.waitstatus_to_exitcode(st) if time.time() < end else 99)
PY
bashes=()
for b in /bin/bash /usr/bin/bash /usr/local/bin/bash /opt/homebrew/bin/bash "$(command -v bash)"; do
  [ -x "$b" ] || continue
  # shellcheck disable=SC2016 # expanded by that bash
  v=$("$b" -c 'echo "${BASH_VERSINFO[0]}"')
  case " ${bashes[*]+${bashes[*]}} " in *" $v:"*) ;; *) bashes+=("$v:$b") ;; esac
done
[ ${#bashes[@]} -gt 0 ] || fail "no bash found for the pty runs"
both
for vb in "${bashes[@]}"; do
  b=${vb#*:}
  rm -f "$t/srv/requests-"*.log
  runs=$((runs + 1)); rc=0
  # shellcheck disable=SC2031 # PATH as the test has it, plus the recorders
  printf '%s\n%s\n' "$HCP_TOK" "$CE_TOK" | env -u HCP_VAULT_TOKEN -u CE_VAULT_TOKEN HOME="$t/home" TMPDIR="$t/tmp" PATH="$t/bin:$PATH" \
    "$PY" -I "$t/srv/pty_drive.py" prompt "$t/out/$runs.pty" "$b" "$SCRIPT" "${COMMON[@]}" --kv api || rc=$?
  [ "$rc" = 0 ] || fail "bash ${vb%%:*}: the prompted run exited $rc: $(cat "$t/out/$runs.pty")"
  { grep -Fq 'HCP_VAULT_TOKEN: ' "$t/out/$runs.pty" && grep -Fq 'CE_VAULT_TOKEN: ' "$t/out/$runs.pty"; } || fail "bash ${vb%%:*}: no prompts: $(cat "$t/out/$runs.pty")"
  ! grep -Fq -e "$HCP_TOK" -e "$CE_TOK" "$t/out/$runs.pty" || fail "bash ${vb%%:*}: a token was echoed"
  ! grep -Fq 'taken from the environment' "$t/out/$runs.pty" || fail "bash ${vb%%:*}: the prompt run says the environment"
  [ "$(requests hcp)" = '{"path": "/v1/kv/data/config", "ns": "admin/ryft/api", "token": "ok", "auth": false}' ] || fail "bash ${vb%%:*}: HCP got $(requests hcp)"
  [ "$(requests k8s)" = '{"path": "/v1/kv-api/data/config", "ns": null, "token": "ok", "auth": false}' ] || fail "bash ${vb%%:*}: the in-cluster Vault got $(requests k8s)"
  # A signal at the (silent) prompt: the script's exit code, and the terminal echoes again (the
  # cleanup's stty echo; a signal without a trap would kill the script with echo still off).
  for sr in INT:130 QUIT:131 HUP:129 TERM:143; do
    sig=${sr%%:*}
    runs=$((runs + 1)); rc=0
    # shellcheck disable=SC2031 # as above
    : | env -u HCP_VAULT_TOKEN -u CE_VAULT_TOKEN HOME="$t/home" TMPDIR="$t/tmp" PATH="$t/bin:$PATH" \
      "$PY" -I "$t/srv/pty_drive.py" "$sig" "$t/out/$runs.pty" "$b" "$SCRIPT" "${COMMON[@]}" --kv api || rc=$?
    [ "$rc" = 0 ] || fail "bash ${vb%%:*}: SIG$sig: the shell around the script exited $rc: $(cat "$t/out/$runs.pty")"
    grep -Fq "rc=${sr#*:}" "$t/out/$runs.pty" || fail "bash ${vb%%:*}: SIG$sig at the prompt did not exit ${sr#*:}: $(cat "$t/out/$runs.pty")"
    { grep -Eq '(^|[[:space:]])echo([[:space:]]|$)' "$t/out/$runs.pty" && ! grep -Eq '(^|[[:space:]])-echo([[:space:]]|$)' "$t/out/$runs.pty"; } \
      || fail "bash ${vb%%:*}: the terminal does not echo after SIG$sig: $(cat "$t/out/$runs.pty")"
  done
done

# 12. --list over the real generated objects (no cluster, no Vault, no context needed).
VAULT_DIFF_OVERLAYS=$REPO/deploy/k8s/overlays/vault-namespaces run 0 --list
has "api  ryft-api  ryft-api/ryft-secrets  kv-api/config"
for u in api ingest notify scoring; do has "rabbitmq  ryft-rabbitmq  ryft-rabbitmq/amqp-user-$u  kv-rabbitmq/amqp-user-$u"; done
run 0 --help
grep -Fq -- '--context <ctx> --kv <svc>' "$t/stdout" || fail "--help"
! grep -q -e '--save' -e '--against' "$t/stdout" || fail "--help still names --save or --against"

# 13. lib/vault_diff.py on its own: the comparer's exit codes (the script also counts the lines, so
#     only this catches a comparer that says "equal" in its exit code), and the hash: keyed, salted,
#     the whole digest compared.
lib() { "$PY" -I "$ROOT/scripts/lib/vault_diff.py" "$@"; }
H1=$(printf 'a%.0s' $(seq 64)); H2=$(printf 'a%.0s' $(seq 63))b
cmp_rc() { # <want rc> <A lines> <B lines>
  local rc=0
  printf '%s\n#--\n%s\n' "$2" "$3" | lib compare s o A B > "$t/cmp.out" 2>&1 || rc=$?
  [ "$rc" = "$1" ] || fail "compare '$2' vs '$3' exited $rc, want $1: $(cat "$t/cmp.out")"
}
cmp_rc 0 "K	$H1" "K	$H1"
grep -Fqx 's  o  K  equal' "$t/cmp.out" || fail "compare: $(cat "$t/cmp.out")"
cmp_rc 1 "K	$H1" "K	$H2"
grep -Fqx 's  o  K  differs' "$t/cmp.out" || fail "a digest that differs in its last digit: $(cat "$t/cmp.out")"
cmp_rc 1 "K	$H1" ""
cmp_rc 1 "" "K	$H1"
cmp_rc 1 "#absent" "#absent"
cmp_rc 3 "K	short" "K	$H1"
SALT1=$(printf '1%.0s' $(seq 64)); SALT2=$(printf '2%.0s' $(seq 64))
vh() { printf '%s\n{"data": {"data": %s}}\n200' "$1" "$2" | lib vault k8s; }
two=$(vh "$SALT1" '{"A": "on", "B": "on"}')
[ "$(printf '%s\n' "$two" | cut -f2 | sort -u | wc -l | tr -d ' ')" = 2 ] || fail "two keys with one value share a hash: $two"
[ "$(vh "$SALT1" '{"A": "on"}')" = "$(vh "$SALT1" '{"A": "on"}')" ] || fail "the hash is not deterministic"
[ "$(vh "$SALT1" '{"A": "on"}')" != "$(vh "$SALT2" '{"A": "on"}')" ] || fail "the salt does not change the hash"
for plain in "$(sha on)" "$(printf 'A\0on' | "$PY" -c 'import hashlib, sys; print(hashlib.sha256(sys.stdin.buffer.read()).hexdigest())')"; do
  [ "$(vh "$SALT1" '{"A": "on"}' | cut -f2)" != "$plain" ] || fail "the hash is unsalted"
done
sh_=$(printf '%s\n{"kind": "Secret", "data": {"A": "b24="}}' "$SALT1" | lib secret)
[ "$sh_" = "$(vh "$SALT1" '{"A": "on"}')" ] || fail "a Secret's bytes and Vault's string hash differently"

# The leak scan, over every run above.
[ -s "$t/spawn.log" ] || fail "no process was recorded (the wrappers are not on PATH)"
[ -s "$t/srv/ps.log" ] || fail "the fake Vaults recorded no ps"
# The live view must have seen most requests (a few stalled snapshots are tolerated, not a silent zero).
ps_ok=$(grep -c . "$t/srv/ps.ok" 2>/dev/null || true); ps_stalled=$(grep -c . "$t/srv/ps.stalled" 2>/dev/null || true)
[ "${ps_ok:-0}" -ge 20 ] && [ "${ps_stalled:-0}" -le $(( ${ps_ok:-0} / 4 )) ] || fail "ps snapshots: ${ps_ok:-0} taken, ${ps_stalled:-0} stalled"
grep -Fq -- "--config -" "$t/srv/ps.log" || fail "no ps snapshot caught the script's curl"
grep -q '^argv: .*curl -q --config -' "$t/spawn.log" || fail "curl was not recorded"
[ "$(grep -c . "$t/needles64")" -ge 8 ] || fail "too few base64 needles: $(grep -c . "$t/needles64")"
# The needles go to grep in a file (-f), never in its argv: the fake servers' ps snapshots would see
# them, and so would those of another run of this test at the same time.
printf '%s\n' "$CANARY" "$HCP_TOK" "$CE_TOK" "$VT_ENV" "$VT_FILE" > "$t/needles"
grep -v '^$' "$t/needles64" >> "$t/needles"
for f in "$t"/out/*; do ! grep -Fq -f "$t/needles" "$f" || fail "a secret in the output of $f: $(cat "$f")"; done
! grep -Fq -f "$t/needles" "$t/spawn.log" || fail "a secret in the argv or environment of a process: $(grep -F -f "$t/needles" "$t/spawn.log" | cut -c1-200)"
! grep -Fq -f "$t/needles" "$t/srv/ps.log" || fail "a secret in ps: $(grep -F -f "$t/needles" "$t/srv/ps.log" | cut -c1-200)"
hits=$(grep -rlF -f "$t/needles" "$t/tmp" "$t/home" "$t/work" | grep -vx "$t/home/.vault-token" || true)
[ -z "$hits" ] || fail "a secret on disk: $hits"
[ -z "$(ls -A "$t/tmp")" ] || fail "files left in TMPDIR: $(ls -A "$t/tmp")"
[ -z "$(ls -A "$t/work")" ] || fail "files in the working directory: $(ls -A "$t/work")"
[ ! -e "$t/home/tls-keys.log" ] || fail "curl wrote TLS keys to SSLKEYLOGFILE"
# No core file with a token in it: every process the script started had a core limit of 0, although
# run() allowed core files (checked only where the hard limit lets the caller raise it).
if [ "$( (ulimit -S -c unlimited 2>/dev/null && ulimit -S -c) || echo 0)" != 0 ]; then
  grep -q '^core: 0$' "$t/spawn.log" || fail "no core limit was recorded"
  # One grep: in a pipe, grep -q's early exit gives the first grep SIGPIPE, and pipefail reports that.
  ! grep -Eq '^core: ([^0]|0.)' "$t/spawn.log" || fail "a process of the script may dump core: $(grep '^core: ' "$t/spawn.log" | sort | uniq -c)"
else
  echo "vault_diff_test: the hard core limit is 0 here: the script's ulimit -c 0 is not checked" >&2
fi
! grep -q '"auth": true' "$t/srv/all-requests.log" || fail "an Authorization header was sent"
# No hash is printed either (a fingerprint is for comparing, not for showing).
! grep -Eq '[0-9a-f]{64}' "$t"/out/* || fail "a hash in the output: $(grep -El '[0-9a-f]{64}' "$t"/out/*)"
echo "✓ vault_diff_test: $runs runs, pty runs with bash ${bashes[*]%%:*}"
