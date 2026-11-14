#!/usr/bin/env bash
# scripts/dns_compare.sh against a fake dig (make test-scripts). dig and terraform are fakes first on
# PATH, so nothing reaches a name server or HCP Terraform. It checks: the arguments (a pattern per
# provider, no swap, nothing that could pass for an option) before any query; the default Cloud DNS
# server from terraform output; every record set asked at both servers without recursion; equal sets
# pass in any order and case; exactly the two intended additions (CAA pki.goog, one
# google-site-verification string) pass and anything more or less fails; a DS at the parent, a
# missing Route 53 answer, a server that is not authoritative or does not answer; and that the
# script's list holds every label of infra/aws/dns/mail.tf and infra/gcp/dns.tf.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
mkdir -p "$t/bin"
fail() { echo "✗ dns_compare_test: $*" >&2; exit 1; }
unset FAKE_DIG_DOWN FAKE_TF_FAIL
export FAKE_DNS=$t/dns
R53=ns-1.awsdns-01.com
GCP=ns-cloud-a1.googledomains.com

# dig: answers from $FAKE_DNS/<server>/<TYPE> <name> (no file: an empty answer). Every call's argv goes
# to $FAKE_DNS/calls. It refuses a query without +short +norecurse; FAKE_DIG_DOWN=<server> times out.
cat > "$t/bin/dig" <<'D'
#!/usr/bin/env bash
server='' type='' name='' norec=0 short=0
for a in "$@"; do
  case "$a" in
    @*) server=${a#@} ;;
    +norecurse) norec=1 ;;
    +short) short=1 ;;
    +*) ;;
    *) if [ -z "$type" ]; then type=$a; else name=$a; fi ;;
  esac
done
printf '%s\n' "$*" >> "$FAKE_DNS/calls"
[ "$norec" = 1 ] && [ "$short" = 1 ] || { echo "fake dig: a query without +norecurse +short" >&2; exit 3; }
if [ "$server" = "${FAKE_DIG_DOWN:-}" ]; then echo ";; connection timed out; no servers could be reached"; exit 9; fi
f="$FAKE_DNS/$server/$type $name"
if [ -f "$f" ]; then cat "$f"; fi
D
# terraform: only `output -json name_servers` in infra/gcp; FAKE_TF_FAIL makes it fail.
cat > "$t/bin/terraform" <<'T'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_DNS/terraform"
[ -z "${FAKE_TF_FAIL:-}" ] || { echo "Error: No outputs found" >&2; exit 1; }
echo '["ns-cloud-a1.googledomains.com.","ns-cloud-a2.googledomains.com.","ns-cloud-a3.googledomains.com.","ns-cloud-a4.googledomains.com."]'
T
chmod +x "$t/bin/dig" "$t/bin/terraform"

# put <server> <type> <name> [<line>...]: that answer (no line: none).
put() { local s=$1 ty=$2 n=$3; shift 3; mkdir -p "$FAKE_DNS/$s"; if [ $# -gt 0 ]; then printf '%s\n' "$@" > "$FAKE_DNS/$s/$ty $n"; else rm -f "$FAKE_DNS/$s/$ty $n"; fi; }
# Both zones as they should be: Route 53's records (mail.tf, main.tf's CAA), and Cloud DNS with the
# same in another order plus the two intended additions.
reset() {
  rm -rf "$FAKE_DNS"; mkdir -p "$FAKE_DNS"; : > "$FAKE_DNS/calls"
  put "$R53" SOA ryftlab.de "ns-1.awsdns-01.com. awsdns-hostmaster.amazon.com. 1 7200 900 1209600 86400"
  put "$GCP" SOA ryftlab.de "ns-cloud-a1.googledomains.com. cloud-dns-hostmaster.google.com. 1 21600 3600 259200 300"
  put "$R53" CAA ryftlab.de '0 issue "letsencrypt.org"' '0 issuewild "letsencrypt.org"'
  put "$GCP" CAA ryftlab.de '0 issuewild "letsencrypt.org"' '0 issue "pki.goog"' '0 issue "letsencrypt.org"'
  put "$R53" TXT ryftlab.de '"NETORGFT21311797.onmicrosoft.com"' '"v=spf1 include:secureserver.net -all"'
  put "$GCP" TXT ryftlab.de '"google-site-verification=example-only"' '"v=spf1 include:secureserver.net -all"' '"NETORGFT21311797.onmicrosoft.com"'
  local s
  for s in "$R53" "$GCP"; do
    put "$s" MX ryftlab.de "0 ryftlab-de.mail.protection.outlook.com."
    put "$s" TXT _dmarc.ryftlab.de '"v=DMARC1; p=quarantine; adkim=r; aspf=r; rua=mailto:dmarc_rua@onsecureserver.net;"'
    put "$s" CNAME selector2._domainkey.ryftlab.de "selector2-ryftlab-de._domainkey.netorgft21311797.d-v1.dkim.mail.microsoft."
    put "$s" CNAME autodiscover.ryftlab.de "autodiscover.outlook.com."
    put "$s" CNAME email.ryftlab.de "email.secureserver.net."
    put "$s" CNAME lyncdiscover.ryftlab.de "webdir.online.lync.com."
    put "$s" CNAME sip.ryftlab.de "sipdir.online.lync.com."
    put "$s" CNAME msoid.ryftlab.de "clientconfig.microsoftonline-p.net."
    put "$s" SRV _sip._tls.ryftlab.de "100 1 443 sipdir.online.lync.com."
    put "$s" SRV _sipfederationtls._tcp.ryftlab.de "100 1 5061 sipfed.online.lync.com."
  done
}
run() { # dns_compare.sh <args> with the fakes first on PATH: $rc, $t/out, $t/err
  : > "$FAKE_DNS/calls"; rm -f "$FAKE_DNS/terraform"
  rc=0; PATH="$t/bin:$PATH" bash "$ROOT/scripts/dns_compare.sh" "$@" >"$t/out" 2>"$t/err" || rc=$?
}
said() { echo "rc=$rc; stdout: $(cat "$t/out"); stderr: $(cat "$t/err")"; }
no_queries() { [ ! -s "$FAKE_DNS/calls" ] || fail "$1: dig ran: $(cat "$FAKE_DNS/calls")"; }
differs() { # <what> <grep -F pattern in stdout>: exit 1, the record named, the stop said
  [ "$rc" = 1 ] && grep -qF -- "$2" "$t/out" && grep -q 'do not switch the name servers' "$t/err" || fail "$1: $(said)"
}

# 1. --help; the arguments are checked before any query.
reset; run --help
[ "$rc" = 0 ] && grep -q '^  scripts/dns_compare.sh <route53-ns> \[<cloud-dns-ns>\]' "$t/out" || fail "--help: $(said)"
if grep -q '^set -euo' "$t/out"; then fail "--help prints code: $(cat "$t/out")"; fi
for a in "" "$R53 $GCP extra" "$GCP $R53" "$R53 $R53" "-x $GCP" "$R53 @evil" "ns-1.awsdns-01.com;id $GCP" "$R53 ns-cloud-a1.googledomains.com.evil.example"; do
  read -ra words <<< "$a"
  run "${words[@]+"${words[@]}"}"
  [ "$rc" = 2 ] || fail "'$a' must exit 2: $(said)"
  no_queries "'$a'"
done

# 2. Equal zones with the two intended additions pass; every record set is asked at both servers,
# plus each SOA and the DS at the parent; terraform is not asked when both servers are named.
reset; run "$R53." "$GCP."
[ "$rc" = 0 ] && grep -q '✓ dns_compare: every record matches (12 record sets' "$t/out" || fail "equal zones: $(said)"
grep -qx '✓ CAA ryftlab.de (plus 0 issue "pki.goog", intended)' "$t/out" && grep -qx '✓ TXT ryftlab.de (plus the Search Console string, intended)' "$t/out" \
  && grep -qx '✓ no DS for ryftlab.de at a.nic.de (DNSSEC off)' "$t/out" || fail "the intended additions and the DS are not reported: $(cat "$t/out")"
[ "$(grep -c '^✓ ' "$t/out")" = 14 ] || fail "want 12 records, the DS and the summary: $(cat "$t/out")"
for q in "CAA ryftlab.de" "MX ryftlab.de" "TXT ryftlab.de" "TXT _dmarc.ryftlab.de" "CNAME selector2._domainkey.ryftlab.de" \
         "CNAME autodiscover.ryftlab.de" "CNAME email.ryftlab.de" "CNAME lyncdiscover.ryftlab.de" "CNAME sip.ryftlab.de" \
         "CNAME msoid.ryftlab.de" "SRV _sip._tls.ryftlab.de" "SRV _sipfederationtls._tcp.ryftlab.de" "SOA ryftlab.de"; do
  for s in "$R53" "$GCP"; do
    [ "$(grep -c -- " $q @$s\$" "$FAKE_DNS/calls")" = 1 ] || fail "'$q' not asked once at $s: $(cat "$FAKE_DNS/calls")"
  done
done
[ "$(grep -c -- ' DS ryftlab.de @a.nic.de$' "$FAKE_DNS/calls")" = 1 ] || fail "the DS is not asked at a.nic.de: $(cat "$FAKE_DNS/calls")"
[ "$(wc -l < "$FAKE_DNS/calls" | tr -d ' ')" = 27 ] || fail "want 27 queries (13 at each server, the DS): $(cat "$FAKE_DNS/calls")"
[ ! -e "$FAKE_DNS/terraform" ] || fail "terraform ran although both servers were named: $(cat "$FAKE_DNS/terraform")"

# 3. Without the second argument the Cloud DNS server is the first of terraform output name_servers.
reset; run "$R53"
[ "$rc" = 0 ] && grep -qx "Cloud DNS: $GCP" "$t/out" || fail "the server from terraform output: $(said)"
[ "$(cat "$FAKE_DNS/terraform")" = "-chdir=$ROOT/infra/gcp output -json name_servers" ] || fail "terraform's arguments: $(cat "$FAKE_DNS/terraform")"
FAKE_TF_FAIL=1 run "$R53"
[ "$rc" = 2 ] && grep -q 'terraform output name_servers failed' "$t/err" || fail "a failing terraform output: $(said)"
no_queries "a failing terraform output"

# 4. Case and order do not matter for host names.
reset; put "$GCP" CNAME autodiscover.ryftlab.de "AutoDiscover.Outlook.com."
run "$R53" "$GCP"; [ "$rc" = 0 ] || fail "a CNAME in another case: $(said)"

# 5. Any other difference fails and names the record.
reset; put "$GCP" MX ryftlab.de "10 ryftlab-de.mail.protection.outlook.com."
run "$R53" "$GCP"; differs "a different MX" "✗ MX ryftlab.de: Route 53 '0 ryftlab-de.mail.protection.outlook.com.', Cloud DNS '10 ryftlab-de.mail.protection.outlook.com.'"
reset; put "$GCP" CNAME msoid.ryftlab.de
run "$R53" "$GCP"; differs "a missing CNAME" "✗ CNAME msoid.ryftlab.de"
reset; put "$GCP" TXT _dmarc.ryftlab.de '"v=DMARC1; p=none;"'
run "$R53" "$GCP"; differs "a different DMARC" "✗ TXT _dmarc.ryftlab.de"
reset; put "$GCP" SRV _sip._tls.ryftlab.de "100 1 443 sipdir.online.lync.com." "100 1 443 other.example."
run "$R53" "$GCP"; differs "an extra SRV" "✗ SRV _sip._tls.ryftlab.de"

# 6. CAA: exactly pki.goog added; without it, or with another CA or tag as well, it fails.
reset; put "$GCP" CAA ryftlab.de '0 issue "letsencrypt.org"' '0 issuewild "letsencrypt.org"'
run "$R53" "$GCP"; differs "CAA without pki.goog" "✗ CAA ryftlab.de: Cloud DNS must hold Route 53's set plus exactly 0 issue \"pki.goog\""
reset; put "$GCP" CAA ryftlab.de '0 issue "letsencrypt.org"' '0 issuewild "letsencrypt.org"' '0 issue "pki.goog"' '0 issue "sectigo.com"'
run "$R53" "$GCP"; differs "CAA with another CA" "added: '0 issue \"pki.goog\" | 0 issue \"sectigo.com\"'"
reset; put "$GCP" CAA ryftlab.de '0 issue "letsencrypt.org"' '0 issue "pki.goog"'
run "$R53" "$GCP"; differs "CAA without issuewild" "missing: '0 issuewild \"letsencrypt.org\"'"
reset; put "$GCP" CAA ryftlab.de '0 issue "letsencrypt.org"' '0 issuewild "letsencrypt.org"' '0 issuewild "pki.goog"'
run "$R53" "$GCP"; differs "issuewild pki.goog instead of issue" "✗ CAA ryftlab.de"

# 7. The apex TXT: exactly one google-site-verification string added; none, two, another string, or a
# Route 53 string missing fails.
reset; put "$GCP" TXT ryftlab.de '"NETORGFT21311797.onmicrosoft.com"' '"v=spf1 include:secureserver.net -all"'
run "$R53" "$GCP"; differs "TXT without the verification" "✗ TXT ryftlab.de: Cloud DNS must hold Route 53's strings plus exactly one google-site-verification= string"
reset; put "$GCP" TXT ryftlab.de '"NETORGFT21311797.onmicrosoft.com"' '"v=spf1 include:secureserver.net -all"' '"google-site-verification=example-only"' '"google-site-verification=example-two"'
run "$R53" "$GCP"; differs "two verification strings" "✗ TXT ryftlab.de"
reset; put "$GCP" TXT ryftlab.de '"NETORGFT21311797.onmicrosoft.com"' '"v=spf1 include:secureserver.net -all"' '"hosting-site=example"'
run "$R53" "$GCP"; differs "another added string" "added: '\"hosting-site=example\"'"
reset; put "$GCP" TXT ryftlab.de '"NETORGFT21311797.onmicrosoft.com"' '"google-site-verification=example-only"'
run "$R53" "$GCP"; differs "the SPF missing" "missing: '\"v=spf1 include:secureserver.net -all\"'"
reset; put "$GCP" TXT ryftlab.de '"NETORGFT21311797.onmicrosoft.com" "v=spf1 include:secureserver.net -all"' '"google-site-verification=example-only"'
run "$R53" "$GCP"; differs "two values in one record" "✗ TXT ryftlab.de"

# 8. A DS at the parent fails; so does a record Route 53 does not answer (the list or the server is wrong).
reset; put a.nic.de DS ryftlab.de "1 13 2 00AA"
run "$R53" "$GCP"; differs "a DS at the parent" "✗ DS ryftlab.de at a.nic.de: '1 13 2 00AA'"
reset; put "$R53" CNAME sip.ryftlab.de; put "$GCP" CNAME sip.ryftlab.de
run "$R53" "$GCP"; differs "a record Route 53 does not answer" "✗ CNAME sip.ryftlab.de: Route 53 answers nothing"

# 9. A server that is not authoritative (no SOA, or another provider's) or does not answer: exit 2.
reset; put "$GCP" SOA ryftlab.de
run "$R53" "$GCP"; [ "$rc" = 2 ] && grep -q "$GCP is not authoritative for ryftlab.de" "$t/err" || fail "a Cloud DNS server without the zone: $(said)"
reset; put "$R53" SOA ryftlab.de "ns-cloud-a1.googledomains.com. cloud-dns-hostmaster.google.com. 1 21600 3600 259200 300"
run "$R53" "$GCP"; [ "$rc" = 2 ] && grep -q "$R53 is not authoritative" "$t/err" || fail "Route 53's name with Google's SOA: $(said)"
reset; FAKE_DIG_DOWN=$GCP run "$R53" "$GCP"
[ "$rc" = 2 ] && grep -q "no answer from $GCP" "$t/err" || fail "a server that times out: $(said)"
reset; FAKE_DIG_DOWN=a.nic.de run "$R53" "$GCP"
[ "$rc" = 2 ] && grep -q 'no answer from a.nic.de for DS ryftlab.de' "$t/err" || fail "the parent times out: $(said)"

# 10. The script's list holds every label of the zones' mail records (infra/gcp/dns.tf, and
# infra/aws/dns/mail.tf while it exists).
for f in "$ROOT/infra/gcp/dns.tf" "$ROOT/infra/aws/dns/mail.tf"; do
  [ -f "$f" ] || continue
  labels=$(sed -nE 's/^[[:space:]]+"([A-Za-z0-9_.-]+)"[[:space:]]+=[[:space:]]+"[^"]*"$/\1/p' "$f")
  [ "$(printf '%s\n' "$labels" | grep -c .)" = 8 ] || fail "$f: want 8 mail labels (6 CNAMEs, 2 SRVs), got: $labels"
  for l in $labels; do
    grep -Eq "^  \"(CNAME|SRV) $l\\.ryftlab\\.de\"\$" "$ROOT/scripts/dns_compare.sh" || fail "$f's $l is not in dns_compare.sh's list"
  done
done

echo "✓ dns_compare_test: arguments before any query, the server from terraform output, every record at both servers without recursion, case and order, exactly the two intended additions, any other difference, a DS, a missing or non-authoritative or silent server, the list against both zones"
