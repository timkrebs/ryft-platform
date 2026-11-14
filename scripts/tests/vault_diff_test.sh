#!/usr/bin/env bash
# scripts/vault_diff.sh against a fake in-cluster Vault (an HTTPS server on 127.0.0.1 with a CA made
# here) and a fake kubectl (make test-scripts). Nothing here reaches a cluster or a real Vault:
# KUBECONFIG=/dev/null, the fakes first on PATH, the address a loopback port of this run, and every
# run has a dead proxy in HTTPS_PROXY, https_proxy and ALL_PROXY (curl must not use it). It checks:
# - the comparison of each synced Secret with the kv-v2 secret it comes from (equal, differs, missing
#   on either side, a whole Secret or secret missing, ryft-secrets per service and the broker's
#   amqp-user-*, --all) and the exit codes;
# - HCP Vault is gone (D207): --kv, --vault, --hcp-addr and --hcp-cacert are refused before anything
#   runs, operator/demo is no service, and a stale HCP_VAULT_TOKEN, VAULT_ADDR_HCP, VAULT_NAMESPACE or
#   VAULT_ADDR in the caller's environment changes nothing (and the token reaches no process);
# - the refusals before any request: no --context or an unknown one, an address that is not https
#   loopback (an HCP or in-cluster address included), no CA, a token curl's config could not carry, a
#   name that is no service;
# - TLS: a server signed by another CA, and CURL_CA_BUNDLE / SSL_CERT_FILE / SSL_CERT_DIR in the
#   environment ignored; no redirect followed; a Secret kubectl cannot give is an error;
# - the prompt, in a pseudo-terminal: no echo, the token to the in-cluster Vault, echo back after
#   Ctrl-C, Ctrl-\, SIGHUP and SIGTERM (with every bash found: 3.2 on macOS, 5 in CI);
# - no core file: every process the script starts has a core limit of 0;
# - no leak: a canary value, the base64 of every fixture Secret value, the token, a stale
#   HCP_VAULT_TOKEN, a VAULT_TOKEN and a ~/.vault-token are looked for in every stdout and stderr, in
#   the argv and environment of every process the script starts (each one is wrapped and recorded),
#   in `ps` as the fake server sees it during each request, and in every file the run could write
#   (TMPDIR, HOME, the working directory); also under the caller's `bash -x`,
#   SHELLOPTS=xtrace:allexport, a verbose ~/.curlrc, SSLKEYLOGFILE and a CDPATH that would move the
#   script's own `cd`;
# - lib/vault_diff.py directly: the comparer's exit codes, the keyed, salted hash, the answers it
#   turns into errors, and the removed modes.
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
fail() { echo "✗ vault_diff_test: $*" >&2; [ ! -s "$t/srv/server.err" ] || { echo "the fake Vault said:" >&2; cat "$t/srv/server.err" >&2; }; exit 1; }
mkdir -p "$t/bin" "$t/srv" "$t/tmp" "$t/home" "$t/work" "$t/out" "$t/overlays" "$t/secrets" "$t/cdp/scripts/lib"
PY=$(command -v python3)
export KUBECONFIG=/dev/null FAKE_DIR=$t
unset KUBE_CONTEXT VAULT_TOKEN HCP_VAULT_TOKEN CE_VAULT_TOKEN VAULT_ADDR VAULT_NAMESPACE VAULT_ADDR_HCP VAULT_ADDR_K8S \
  VAULT_CACERT VAULT_CACERT_HCP HCP_VAULT_NAMESPACE_ROOT VAULT_DIFF_OVERLAYS NO_PROXY no_proxy CURL_CA_BUNDLE SSL_CERT_FILE \
  SSL_CERT_DIR SSLKEYLOGFILE CDPATH
# No core files from the test's own processes (only run() raises the soft limit, for the script).
ulimit -S -c 0

# Made up at run time; a hit anywhere below is a leak.
CANARY=canary-$RANDOM$RANDOM-do-not-print-$RANDOM$RANDOM
CE_TOK=hvs.CEtok$RANDOM$RANDOM$RANDOM
HCP_TOK=hvs.HCPtok$RANDOM$RANDOM$RANDOM  # left in the caller's shell from before the rebuild
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

# The fake Vault: kv-v2 reads only. Answers from data-<name>.json ("<namespace>|<mount>|<path>" →
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
# The server's CA, and a second one that signed nothing here.
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
    i=$((i + 1)); [ "$i" -lt 200 ] || fail "the fake Vault did not start: $(cat "$t/srv/server.err")"
    sleep 0.05
  done
}
: > "$t/srv/data-k8s.json"
start k8s "$CE_TOK"
K8S_URL=https://127.0.0.1:$(cat "$t/srv/port-k8s")
IN_CLUSTER=https://vault.vault.svc:8200

# A fixture of the generated VSO objects in the generator's in-cluster shape
# (infra/vault/templates/vso.yaml.tftpl): the Certificate vault-ca, the VaultConnection and the
# VaultAuth (no Vault namespace), then the VaultStaticSecrets on kv-<svc>. web has a directory but
# no generated objects.
vso() { # <svc> <ns> <path>:<secret>...
  local svc=$1 ns=$2 p; shift 2
  mkdir -p "$t/overlays/$svc"
  {
    echo "# Generated by infra/vault — do not edit by hand (terraform apply rewrites it)."
    printf 'apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata:\n  name: vault-ca\n  namespace: %s\nspec:\n  secretName: vault-ca\n  commonName: vso.%s\n  issuerRef:\n    name: ryft-vault\n    kind: ClusterIssuer\n    group: cert-manager.io\n' "$ns" "$ns"
    printf -- '---\napiVersion: secrets.hashicorp.com/v1beta1\nkind: VaultConnection\nmetadata:\n  name: vault\n  namespace: %s\n  annotations:\n    argocd.argoproj.io/sync-wave: "0"\nspec:\n  address: %s\n  skipTLSVerify: false\n  caCertSecretRef: vault-ca\n' "$ns" "$IN_CLUSTER"
    printf -- '---\napiVersion: secrets.hashicorp.com/v1beta1\nkind: VaultAuth\nmetadata:\n  name: %s\n  namespace: %s\n  annotations:\n    argocd.argoproj.io/sync-wave: "0"\nspec:\n  vaultConnectionRef: vault\n  method: kubernetes\n  mount: kubernetes\n  kubernetes:\n    role: %s\n    serviceAccount: %s\n    audiences: ["vault"]\n' "$svc" "$ns" "$svc" "$svc"
    for p in "$@"; do
      printf -- '---\napiVersion: secrets.hashicorp.com/v1beta1\nkind: VaultStaticSecret\nmetadata:\n  name: %s\n  namespace: %s\n  annotations:\n    argocd.argoproj.io/sync-wave: "1"\nspec:\n  vaultAuthRef: %s\n  mount: kv-%s\n  type: kv-v2\n  path: %s\n  refreshAfter: 1h\n  destination:\n    name: %s\n    create: true\n    overwrite: true\n    labels:\n      rabbitmq.com/topology-operator: "true"\n    transformation:\n      excludeRaw: true\n' \
        "${p#*:}" "$ns" "$svc" "$svc" "${p%%:*}" "${p#*:}"
    done
  } > "$t/overlays/$svc/vault.yaml"
}
vso api ryft-api config:ryft-secrets
vso ingest ryft-ingest config:ryft-secrets
vso rabbitmq ryft-rabbitmq amqp-user-api:amqp-user-api amqp-user-ingest:amqp-user-ingest
mkdir -p "$t/overlays/web"
export VAULT_DIFF_OVERLAYS=$t/overlays

# The in-cluster Vault's data: "|<mount>|<path>" (no namespace on Community Edition).
data() { # <python dict literal over the variable C (canary)>
  C=$CANARY "$PY" -c 'import json, os, sys; C = os.environ["C"]; json.dump(eval(sys.argv[2]), open(sys.argv[1], "w"))' "$t/srv/data-k8s.json" "$1"
}
API='{"DATABASE_URL": "postgresql://u:" + C + "@db/x", "ADMIN_API_KEY": C + "-admin", "SUPABASE_PROJECT_REF": "abcdefghij12", "BETA_GATE": "on"}'
INGEST='{"DATABASE_URL": "postgresql://i:" + C + "@db/x", "SUPABASE_PROJECT_REF": "abcdefghij12", "BETA_GATE": "on"}'
AMQP_API="{'username': 'api', 'password': C + 'a'}"
AMQP_INGEST="{'username': 'ingest', 'password': C + 'i'}"
vault_all() { # kv-operator/demo is there too: no run may read it
  data "{'|kv-api|config': $API, '|kv-ingest|config': $INGEST,
    '|kv-rabbitmq|amqp-user-api': $AMQP_API, '|kv-rabbitmq|amqp-user-ingest': $AMQP_INGEST,
    '|kv-operator|demo': {'email': 'review@example.com', 'password': C + 'd'}}"
}
# vault_api <key> <python expression>: only the api's config, with one change (None: the key removed).
vault_api() {
  data "{'|kv-api|config': {k: v for k, v in dict($API, **{'$1': $2}).items() if v is not None}}"
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
# What a healthy sync leaves: every Secret holds the bytes of its Vault secret.
synced() {
  vault_all
  secret ryft-api ryft-secrets "$API"
  secret ryft-ingest ryft-secrets "$INGEST"
  secret ryft-rabbitmq amqp-user-api "$AMQP_API"
  secret ryft-rabbitmq amqp-user-ingest "$AMQP_INGEST"
}

# run <want rc> <args...>: the script as an operator runs it, the token in its environment only
# (RUN_CE_TOK replaces it; RUN_ENV="<name>=<value> ..." adds variables; RUN_BASH="bash -x" traces;
# RUN_REL=1 starts it as scripts/vault_diff.sh from the tree's root). Every run also has a stale
# HCP_VAULT_TOKEN, a VAULT_TOKEN, a dead proxy and SSLKEYLOGFILE. Every run's stdout and stderr are
# kept for the leak scan.
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
    export CE_VAULT_TOKEN="${RUN_CE_TOK:-$CE_TOK}" HCP_VAULT_TOKEN="$HCP_TOK"
    # A prefix assignment on a function call is exported: only the two variables above carry a token.
    unset RUN_CE_TOK
    s=$SCRIPT
    if [ -n "${RUN_REL:-}" ]; then cd "$ROOT"; s=scripts/vault_diff.sh; fi
    # env(1) for the extra variables: SHELLOPTS is read-only inside bash. Only they are in its argv.
    # shellcheck disable=SC2086 # RUN_ENV is a list of assignments, RUN_BASH a command with its options
    exec /usr/bin/env ${RUN_ENV:-} ${RUN_BASH:-bash} "$s" "$@"
  ) > "$t/out/$runs.out" 2> "$t/out/$runs.err" < /dev/null || rc=$?
  cp "$t/out/$runs.out" "$t/stdout"; cp "$t/out/$runs.err" "$t/stderr"
  [ "$rc" = "$want" ] || fail "'$*' exited $rc, want $want: $(cat "$t/stdout" "$t/stderr")"
}
COMMON=(--context test-ctx --k8s-addr "$K8S_URL" --k8s-cacert "$CA")
has() { grep -Fqx -- "$1" "$t/stdout" || fail "no line '$1' in: $(cat "$t/stdout" "$t/stderr")"; }
says() { grep -Fq -- "$1" "$t/stderr" || fail "stderr does not say '$1': $(cat "$t/stderr")"; }
requests() { cat "$t/srv/requests-k8s.log" 2>/dev/null || true; }
no_requests() { [ -z "$(requests)" ] || fail "$1: the Vault was asked: $(requests)"; }
no_kubectl() { [ ! -s "$t/kubectl.calls" ] || fail "$1: kubectl ran: $(cat "$t/kubectl.calls")"; }
API_REQ='{"path": "/v1/kv-api/data/config", "ns": null, "token": "ok", "auth": false}'
API_KUBECTL="config get-contexts -o name
--context test-ctx -n ryft-api get secret ryft-secrets --ignore-not-found -o json"

# 1. The cluster is named, every time. Not by KUBE_CONTEXT, not by kubectl's current context.
synced
RUN_ENV=KUBE_CONTEXT=test-ctx run 2 --k8s-addr "$K8S_URL" --k8s-cacert "$CA" api
says '--context <ctx> is required'
no_kubectl "no --context"
no_requests "no --context"
run 2 --context ryft-prod-eu --k8s-addr "$K8S_URL" --k8s-cacert "$CA" api
[ "$(cat "$t/kubectl.calls")" = "config get-contexts -o name" ] || fail "an unknown context: $(cat "$t/kubectl.calls")"
no_requests "an unknown context"
run 2 --context 'test-ctx;id' --k8s-addr "$K8S_URL" --k8s-cacert "$CA" api
no_requests "a context that is not a name"

# 2. HCP Vault is gone (D207): its mode and flags are refused before kubectl or a Vault is asked;
#    operator/demo is no service (it has no synced Secret); HCP's variables in the caller's
#    environment change nothing.
for a in --kv "--vault hcp" --vault=k8s "--hcp-addr https://x.hashicorp.cloud:8200" --hcp-addr=https://x.hashicorp.cloud \
  "--hcp-cacert $CA" "--hcp-cacert=$CA"; do
  # shellcheck disable=SC2086 # a flag and its value, split on purpose
  run 2 "${COMMON[@]}" $a api
  says 'is gone with HCP Vault (D207)'
  no_kubectl "$a"
  no_requests "$a"
done
run 2 "${COMMON[@]}" --kv --all
says 'is gone with HCP Vault (D207)'
run 2 "${COMMON[@]}" operator
says 'operator: no generated VSO objects'
no_requests "operator"
RUN_ENV="VAULT_ADDR_HCP=https://127.0.0.1:1 VAULT_CACERT_HCP=$CA2 HCP_VAULT_NAMESPACE_ROOT=admin/ryft VAULT_NAMESPACE=admin/ryft/api VAULT_ADDR=https://127.0.0.1:1" \
  run 0 "${COMMON[@]}" api
[ "$(requests)" = "$API_REQ" ] || fail "a stale HCP environment changed the request: $(requests)"
! grep -Fq HCP "$t/stderr" || fail "a stale HCP environment is mentioned: $(cat "$t/stderr")"

# 3. Refusals before any request: http, an address that is not your port-forward (HCP's and the
#    in-cluster Service's included), no CA or a missing file, a token curl's config could not carry
#    (not echoed), a name that is no service, a bad combination, the removed --save and --against.
for a in "http://127.0.0.1:8200" "https://10.0.0.1:8200" "$IN_CLUSTER" "https://127.0.0.1.example.com:8200" "https://localhost:8200/x" \
  "https://ryft-prod-euc1-vault-public-vault-0000.z1.hashicorp.cloud:8200"; do
  run 2 "${COMMON[@]}" --k8s-addr "$a" api
  says '--k8s-addr must be your port-forward'
done
RUN_ENV=VAULT_ADDR_K8S=https://vault.example.com:8200 run 2 --context test-ctx --k8s-cacert "$CA" api
says '--k8s-addr must be your port-forward'
run 2 --context test-ctx --k8s-addr "$K8S_URL" api
says 'no CA for the in-cluster Vault'
run 2 "${COMMON[@]}" --k8s-cacert "$t/srv/no-such-ca.pem" api
says 'no such file'
BADTOK='hvs.x" -o /tmp/x "'$RANDOM
RUN_CE_TOK=$BADTOK run 2 "${COMMON[@]}" api
says "CE_VAULT_TOKEN has characters"
! grep -Fq "$BADTOK" "$t/stdout" "$t/stderr" || fail "a refused token was echoed"
run 2 "${COMMON[@]}" no-such-svc
run 2 "${COMMON[@]}" web
says 'web: no generated VSO objects'
run 2 "${COMMON[@]}" 'api;id'
run 2 "${COMMON[@]}"
run 2 "${COMMON[@]}" --all api
run 2 --list api
run 2 "${COMMON[@]}" api --save "$t/work/fp"
run 2 "${COMMON[@]}" api --against "$t/work/fp"
no_requests "the refusals"

# 4. All equal: one line per key, exit 0. The Secret through the named context, the secret from
#    kv-api with the token only (no namespace header, no Authorization), the address shown before
#    the token is used; the dead proxy was not used.
synced
run 0 "${COMMON[@]}" api
for k in ADMIN_API_KEY BETA_GATE DATABASE_URL SUPABASE_PROJECT_REF; do has "api  ryft-secrets  $k  equal"; done
[ "$(wc -l < "$t/stdout" | tr -d ' ')" = 4 ] || fail "equal: more than the four keys: $(cat "$t/stdout")"
says '4 keys, all equal'
says "-> in-cluster: ${K8S_URL#https://}"
says "CE_VAULT_TOKEN: taken from the environment"
[ "$(requests)" = "$API_REQ" ] || fail "the request: $(requests)"
[ "$(cat "$t/kubectl.calls")" = "$API_KUBECTL" ] || fail "kubectl: $(cat "$t/kubectl.calls")"
# The mode's old flag is accepted and changes nothing; the address and the CA from the environment.
run 0 "${COMMON[@]}" api --secret
[ "$(requests)" = "$API_REQ" ] && [ "$(cat "$t/kubectl.calls")" = "$API_KUBECTL" ] || fail "--secret: $(requests) $(cat "$t/kubectl.calls")"
RUN_ENV="VAULT_ADDR_K8S=$K8S_URL/ VAULT_CACERT=$CA" run 0 --context test-ctx api
[ "$(requests)" = "$API_REQ" ] || fail "VAULT_ADDR_K8S and VAULT_CACERT: $(requests)"
# Started by a relative path with a CDPATH that has a `scripts` of its own: the script's `cd` stays home.
RUN_REL=1 RUN_ENV="CDPATH=$t/cdp" run 0 "${COMMON[@]}" api
[ ! -e "$t/cdpath-used" ] || fail "CDPATH moved the script's cd"

# 5. A difference, exit 1 every time: a rotated value and a key only in the Secret; a value that
#    differs; a key only in Vault; a key gone from Vault; the whole Secret missing; the whole secret
#    missing in Vault; both missing.
secret ryft-api ryft-secrets "dict($API, ADMIN_API_KEY=C + '-rotated', EXTRA=C + '-extra')"
run 1 "${COMMON[@]}" api
has "api  ryft-secrets  ADMIN_API_KEY  differs"
has "api  ryft-secrets  EXTRA  missing in Vault"
has "api  ryft-secrets  DATABASE_URL  equal"
says '5 keys, 2 not equal'
secret ryft-api ryft-secrets "$API"
vault_api ADMIN_API_KEY "'on'"
run 1 "${COMMON[@]}" api
has "api  ryft-secrets  ADMIN_API_KEY  differs"
vault_api NEW_KEY "C + '-new'"
run 1 "${COMMON[@]}" api
has "api  ryft-secrets  NEW_KEY  missing in Secret"
has "api  ryft-secrets  BETA_GATE  equal"
vault_api BETA_GATE None
run 1 "${COMMON[@]}" api
has "api  ryft-secrets  BETA_GATE  missing in Vault"
vault_all
rm -f "$t/secrets/ryft-api/ryft-secrets.json"
run 1 "${COMMON[@]}" api
for k in ADMIN_API_KEY BETA_GATE DATABASE_URL SUPABASE_PROJECT_REF; do has "api  ryft-secrets  $k  missing in Secret"; done
secret ryft-api ryft-secrets "$API"
data "{}"
run 1 "${COMMON[@]}" api
has "api  ryft-secrets  DATABASE_URL  missing in Vault"
rm -f "$t/secrets/ryft-api/ryft-secrets.json"
run 1 "${COMMON[@]}" api
has "api  ryft-secrets  -  missing in Vault and Secret"
says '1 keys, 1 not equal'

# 6. --all: every service with generated objects (api, ingest, the broker's two users), one read per
#    object, never kv-operator; a value only ingest's copy differs in is reported for ingest only.
synced
run 0 "${COMMON[@]}" --all
for l in "api  ryft-secrets  SUPABASE_PROJECT_REF  equal" "ingest  ryft-secrets  SUPABASE_PROJECT_REF  equal" \
  "rabbitmq  amqp-user-api  password  equal" "rabbitmq  amqp-user-ingest  username  equal"; do has "$l"; done
! grep -q '^operator ' "$t/stdout" || fail "--all compared operator/demo: $(cat "$t/stdout")"
! grep -Fq kv-operator "$t/srv/requests-k8s.log" || fail "--all read kv-operator: $(requests)"
[ "$(grep -c . "$t/srv/requests-k8s.log")" = 4 ] || fail "--all: not one read per object: $(requests)"
data "{'|kv-api|config': $API, '|kv-ingest|config': dict($INGEST, SUPABASE_PROJECT_REF='zzzzzzzzzz99'),
  '|kv-rabbitmq|amqp-user-api': $AMQP_API, '|kv-rabbitmq|amqp-user-ingest': $AMQP_INGEST}"
run 1 "${COMMON[@]}" --all
has "api  ryft-secrets  SUPABASE_PROJECT_REF  equal"
has "ingest  ryft-secrets  SUPABASE_PROJECT_REF  differs"
has "ingest  ryft-secrets  BETA_GATE  equal"
run 0 "${COMMON[@]}" rabbitmq
has "rabbitmq  amqp-user-ingest  password  equal"
[ "$(cat "$t/kubectl.calls")" = "config get-contexts -o name
--context test-ctx -n ryft-rabbitmq get secret amqp-user-api --ignore-not-found -o json
--context test-ctx -n ryft-rabbitmq get secret amqp-user-ingest --ignore-not-found -o json" ] || fail "rabbitmq, kubectl: $(cat "$t/kubectl.calls")"

# 7. A side that cannot be read is an error (2), never a difference or "equal": a wrong token; a
#    server signed by another CA (a CA in CURL_CA_BUNDLE, SSL_CERT_FILE or SSL_CERT_DIR does not
#    count); nothing listening; a redirect (not followed); a Secret kubectl does not give as one.
synced
RUN_CE_TOK=hvs.wrong$RANDOM run 2 "${COMMON[@]}" api
says 'refused the read (HTTP 403'
[ ! -s "$t/stdout" ] || fail "a 403 still printed a comparison: $(cat "$t/stdout")"
run 2 "${COMMON[@]}" --k8s-cacert "$CA2" api
says 'the in-cluster Vault gave no answer'
no_requests "a server the given CA did not sign"
RUN_ENV="CURL_CA_BUNDLE=$CA SSL_CERT_FILE=$CA SSL_CERT_DIR=$t/srv" run 2 "${COMMON[@]}" --k8s-cacert "$CA2" api
no_requests "a CA from the environment"
run 2 "${COMMON[@]}" --k8s-addr "https://127.0.0.1:1" api
says 'the in-cluster Vault gave no answer'
data "{'|kv-api|config': {'__redirect__': '$K8S_URL/v1/kv-ingest/data/config'}}"
run 2 "${COMMON[@]}" api
says 'the in-cluster Vault answered HTTP 307'
[ "$(requests)" = "$API_REQ" ] || fail "a redirect was followed: $(requests)"
synced
printf '{"kind": "ConfigMap", "data": {}}' > "$t/secrets/ryft-api/ryft-secrets.json"
run 2 "${COMMON[@]}" api
says 'ryft-api/ryft-secrets: the Secret could not be read'
[ ! -s "$t/stdout" ] || fail "an unreadable Secret still printed a comparison: $(cat "$t/stdout")"
no_requests "a Secret that is not one"
secret ryft-api ryft-secrets "$API"

# 8. The caller's tracing and a verbose ~/.curlrc change nothing (the leak scan below covers these runs).
printf 'verbose\ntrace-ascii = %s\n' "$t/home/curl-trace" > "$t/home/.curlrc"
synced
vault_api ADMIN_API_KEY "C + '-other'"
RUN_BASH="bash -x" run 1 "${COMMON[@]}" api
has "api  ryft-secrets  ADMIN_API_KEY  differs"
RUN_ENV=SHELLOPTS=xtrace:allexport run 1 "${COMMON[@]}" api
has "api  ryft-secrets  ADMIN_API_KEY  differs"
synced
RUN_BASH="bash -x" run 0 "${COMMON[@]}" --all
[ ! -e "$t/home/curl-trace" ] || fail "curl read ~/.curlrc"
rm -f "$t/home/.curlrc"

# 9. The prompt, in a pseudo-terminal, with every bash there is: nothing echoed, the token to the
#    in-cluster Vault, no other prompt; Ctrl-C, Ctrl-\, SIGHUP and SIGTERM at the prompt exit 130,
#    131, 129 and 143 and leave the terminal echoing. The token reaches the driver on its stdin,
#    never in its argv.
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
# One prompt per token given; a signal is sent at the first prompt.
want = len(toks) if mode == "prompt" else 1
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
    if sent < want and buf.count(b"_VAULT_TOKEN: ") > sent:
        echo_off(fd)
        if mode == "prompt":
            os.write(fd, toks[sent] + b"\n"); sent += 1
        else:
            if mode in ("HUP", "TERM"):
                os.killpg(pid, getattr(signal, "SIG" + mode))  # pty.fork's child leads its process group
            else:
                os.write(fd, b"\x03" if mode == "INT" else b"\x1c")
            sent = want
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
synced
for vb in "${bashes[@]}"; do
  b=${vb#*:}
  rm -f "$t/srv/requests-"*.log
  runs=$((runs + 1)); rc=0
  # shellcheck disable=SC2031 # PATH as the test has it, plus the recorders
  printf '%s\n' "$CE_TOK" | env -u HCP_VAULT_TOKEN -u CE_VAULT_TOKEN HOME="$t/home" TMPDIR="$t/tmp" PATH="$t/bin:$PATH" \
    "$PY" -I "$t/srv/pty_drive.py" prompt "$t/out/$runs.pty" "$b" "$SCRIPT" "${COMMON[@]}" api || rc=$?
  [ "$rc" = 0 ] || fail "bash ${vb%%:*}: the prompted run exited $rc: $(cat "$t/out/$runs.pty")"
  grep -Fq 'CE_VAULT_TOKEN: ' "$t/out/$runs.pty" || fail "bash ${vb%%:*}: no prompt: $(cat "$t/out/$runs.pty")"
  [ "$(grep -o '_VAULT_TOKEN: ' "$t/out/$runs.pty" | wc -l | tr -d ' ')" = 1 ] || fail "bash ${vb%%:*}: more than one prompt: $(cat "$t/out/$runs.pty")"
  ! grep -Fq 'HCP_VAULT_TOKEN' "$t/out/$runs.pty" || fail "bash ${vb%%:*}: asked for an HCP token"
  ! grep -Fq -e "$CE_TOK" "$t/out/$runs.pty" || fail "bash ${vb%%:*}: the token was echoed"
  ! grep -Fq 'taken from the environment' "$t/out/$runs.pty" || fail "bash ${vb%%:*}: the prompt run says the environment"
  grep -Fq 'vault_diff: 4 keys, all equal' "$t/out/$runs.pty" || fail "bash ${vb%%:*}: the prompted run compared nothing: $(cat "$t/out/$runs.pty")"
  [ "$(requests)" = "$API_REQ" ] || fail "bash ${vb%%:*}: the in-cluster Vault got $(requests)"
  # A signal at the (silent) prompt: the script's exit code, and the terminal echoes again (the
  # cleanup's stty echo; a signal without a trap would kill the script with echo still off).
  for sr in INT:130 QUIT:131 HUP:129 TERM:143; do
    sig=${sr%%:*}
    runs=$((runs + 1)); rc=0
    # shellcheck disable=SC2031 # as above
    : | env -u HCP_VAULT_TOKEN -u CE_VAULT_TOKEN HOME="$t/home" TMPDIR="$t/tmp" PATH="$t/bin:$PATH" \
      "$PY" -I "$t/srv/pty_drive.py" "$sig" "$t/out/$runs.pty" "$b" "$SCRIPT" "${COMMON[@]}" api || rc=$?
    [ "$rc" = 0 ] || fail "bash ${vb%%:*}: SIG$sig: the shell around the script exited $rc: $(cat "$t/out/$runs.pty")"
    grep -Fq "rc=${sr#*:}" "$t/out/$runs.pty" || fail "bash ${vb%%:*}: SIG$sig at the prompt did not exit ${sr#*:}: $(cat "$t/out/$runs.pty")"
    { grep -Eq '(^|[[:space:]])echo([[:space:]]|$)' "$t/out/$runs.pty" && ! grep -Eq '(^|[[:space:]])-echo([[:space:]]|$)' "$t/out/$runs.pty"; } \
      || fail "bash ${vb%%:*}: the terminal does not echo after SIG$sig: $(cat "$t/out/$runs.pty")"
  done
done

# 10. --list over the real generated objects and over the fixture (no cluster, no Vault, no context
#     needed), and --help.
VAULT_DIFF_OVERLAYS=$REPO/deploy/k8s/overlays/vault-namespaces run 0 --list
has "api  ryft-api  ryft-api/ryft-secrets  kv-api/config"
for u in api ingest notify scoring; do has "rabbitmq  ryft-rabbitmq  ryft-rabbitmq/amqp-user-$u  kv-rabbitmq/amqp-user-$u"; done
no_kubectl "--list"
no_requests "--list"
run 0 --list
has "rabbitmq  ryft-rabbitmq  ryft-rabbitmq/amqp-user-ingest  kv-rabbitmq/amqp-user-ingest"
[ "$(wc -l < "$t/stdout" | tr -d ' ')" = 4 ] || fail "--list over the fixture: $(cat "$t/stdout")"
run 0 --help
grep -Fq -- '--context <ctx> <svc>...' "$t/stdout" || fail "--help: $(cat "$t/stdout")"
! grep -q -e '--kv' -e '--hcp' -e 'HCP_VAULT_TOKEN' -e 'hashicorp.cloud' -e '--save' -e '--against' "$t/stdout" \
  || fail "--help still names a removed mode or flag: $(grep -e '--kv' -e '--hcp' -e 'HCP_VAULT_TOKEN' -e 'hashicorp.cloud' -e '--save' -e '--against' "$t/stdout")"

# 11. lib/vault_diff.py on its own: the comparer's exit codes (the script also counts the lines, so
#     only this catches a comparer that says "equal" in its exit code), the hash (keyed, salted, the
#     whole digest compared), the answers that are errors, and the modes that went with HCP.
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
vh() { printf '%s\n{"data": {"data": %s}}\n200' "$1" "$2" | lib vault; }
two=$(vh "$SALT1" '{"A": "on", "B": "on"}')
[ "$(printf '%s\n' "$two" | cut -f2 | sort -u | wc -l | tr -d ' ')" = 2 ] || fail "two keys with one value share a hash: $two"
[ "$(vh "$SALT1" '{"A": "on"}')" = "$(vh "$SALT1" '{"A": "on"}')" ] || fail "the hash is not deterministic"
[ "$(vh "$SALT1" '{"A": "on"}')" != "$(vh "$SALT2" '{"A": "on"}')" ] || fail "the salt does not change the hash"
for plain in "$(sha on)" "$(printf 'A\0on' | "$PY" -c 'import hashlib, sys; print(hashlib.sha256(sys.stdin.buffer.read()).hexdigest())')"; do
  [ "$(vh "$SALT1" '{"A": "on"}' | cut -f2)" != "$plain" ] || fail "the hash is unsalted"
done
sh_=$(printf '%s\n{"kind": "Secret", "data": {"A": "b24="}}' "$SALT1" | lib secret)
[ "$sh_" = "$(vh "$SALT1" '{"A": "on"}')" ] || fail "a Secret's bytes and Vault's string hash differently"
[ "$(printf '%s\n{}\n404' "$SALT1" | lib vault)" = "#absent" ] || fail "a 404 is not absent"
[ "$(printf '%s\n{"data": {"data": null}}\n200' "$SALT1" | lib vault)" = "#absent" ] || fail "a deleted version is not absent"
for code in 000 401 403 500; do
  rc=0; printf '%s\n{}\n%s' "$SALT1" "$code" | lib vault > "$t/cmp.out" 2>&1 || rc=$?
  { [ "$rc" = 3 ] && grep -Fq 'the in-cluster Vault' "$t/cmp.out" && ! grep -Fq HCP "$t/cmp.out"; } || fail "HTTP $code (exit $rc): $(cat "$t/cmp.out")"
done
rc=0; lib hcp-address "$t/overlays" > "$t/cmp.out" 2>&1 || rc=$?
[ "$rc" = 3 ] || fail "lib hcp-address is still a mode (exit $rc): $(cat "$t/cmp.out")"
rc=0; printf '%s\n{"data": {"data": {}}}\n200' "$SALT1" | lib vault hcp > "$t/cmp.out" 2>&1 || rc=$?
[ "$rc" = 3 ] || fail "lib vault <side> is still accepted (exit $rc): $(cat "$t/cmp.out")"

# The leak scan, over every run above.
[ -s "$t/spawn.log" ] || fail "no process was recorded (the wrappers are not on PATH)"
[ -s "$t/srv/ps.log" ] || fail "the fake Vault recorded no ps"
# The live view must have seen most requests (a few stalled snapshots are tolerated, not a silent zero).
ps_ok=$(grep -c . "$t/srv/ps.ok" 2>/dev/null || true); ps_stalled=$(grep -c . "$t/srv/ps.stalled" 2>/dev/null || true)
[ "${ps_ok:-0}" -ge 15 ] && [ "${ps_stalled:-0}" -le $(( ${ps_ok:-0} / 4 )) ] || fail "ps snapshots: ${ps_ok:-0} taken, ${ps_stalled:-0} stalled"
grep -Fq -- "--config -" "$t/srv/ps.log" || fail "no ps snapshot caught the script's curl"
grep -q '^argv: .*curl -q --config -' "$t/spawn.log" || fail "curl was not recorded"
[ "$(grep -c . "$t/needles64")" -ge 8 ] || fail "too few base64 needles: $(grep -c . "$t/needles64")"
# The needles go to grep in a file (-f), never in its argv: the fake server's ps snapshots would see
# them, and so would those of another run of this test at the same time.
printf '%s\n' "$CANARY" "$CE_TOK" "$HCP_TOK" "$VT_ENV" "$VT_FILE" > "$t/needles"
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
! grep -q '"ns": "' "$t/srv/all-requests.log" || fail "a Vault namespace header was sent: $(grep '"ns": "' "$t/srv/all-requests.log" | head -3)"
# No hash is printed either (a fingerprint is for comparing, not for showing).
! grep -Eq '[0-9a-f]{64}' "$t"/out/* || fail "a hash in the output: $(grep -El '[0-9a-f]{64}' "$t"/out/*)"
echo "✓ vault_diff_test: $runs runs, pty runs with bash ${bashes[*]%%:*}"
