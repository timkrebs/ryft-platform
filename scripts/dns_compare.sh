#!/usr/bin/env bash
# Compares the Route 53 zone ryftlab.de with the Cloud DNS zone that replaces it (infra/gcp, D214),
# before GoDaddy's name servers move (docs/OPERATIONS.md -> GCP, step 6). It asks one Route 53 name
# server and one Cloud DNS name server, without recursion, for every record set of the Route 53 zone
# (the list below: infra/aws/dns/mail.tf and main.tf's CAA, all of which infra/gcp/dns.tf carries),
# and compares the normalised answers. Exactly two differences are intended, and checked exactly:
#   CAA ryftlab.de  Cloud DNS holds Route 53's set plus 0 issue "pki.goog", and nothing else
#   TXT ryftlab.de  Cloud DNS holds Route 53's strings plus one "google-site-verification=..." string
# Anything else fails: another difference, an empty Route 53 answer, a server that is not
# authoritative for the zone, or a DS record for the domain at the parent (a.nic.de), since DNSSEC
# stays off.
#
#   scripts/dns_compare.sh <route53-ns> [<cloud-dns-ns>]
#     route53-ns    one of the Route 53 zone's servers (ns-<n>.awsdns-<n>.<tld>); before the switch:
#                   dig +short NS ryftlab.de @1.1.1.1
#     cloud-dns-ns  one of the Cloud DNS zone's servers (ns-cloud-<x><n>.googledomains.com); default:
#                   the first of terraform -chdir=infra/gcp output -json name_servers
#
# Exit: 0 every record matches; 1 a difference or a DS; 2 usage, a missing tool, a server that does
# not answer or is not authoritative.
set -euo pipefail

die() { local rc=$1; shift; echo "✗ dns_compare: $*" >&2; exit "$rc"; }
usage() { sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p;}' "$0"; }

zone=ryftlab.de
parent=a.nic.de
# Every (type, name) of the Route 53 zone except its own NS and SOA, which differ by design.
records=(
  "CAA ryftlab.de"
  "MX ryftlab.de"
  "TXT ryftlab.de"
  "TXT _dmarc.ryftlab.de"
  "CNAME selector2._domainkey.ryftlab.de"
  "CNAME autodiscover.ryftlab.de"
  "CNAME email.ryftlab.de"
  "CNAME lyncdiscover.ryftlab.de"
  "CNAME sip.ryftlab.de"
  "CNAME msoid.ryftlab.de"
  "SRV _sip._tls.ryftlab.de"
  "SRV _sipfederationtls._tcp.ryftlab.de"
)
caa_added='0 issue "pki.goog"'
verification_re='^"google-site-verification=[A-Za-z0-9_-]+"$'

case "${1:-}" in -h|--help) usage; exit 0 ;; esac
[ $# -ge 1 ] && [ $# -le 2 ] || die 2 "usage: scripts/dns_compare.sh <route53-ns> [<cloud-dns-ns>] (--help)"
for t in dig sort comm; do command -v "$t" >/dev/null || die 2 "$t is required"; done

route53=${1%.}
if [ $# -eq 2 ]; then
  cloud=${2%.}
else
  command -v terraform >/dev/null || die 2 "terraform is required to read the Cloud DNS name servers (or pass one as the second argument)"
  command -v jq >/dev/null || die 2 "jq is required to read the Cloud DNS name servers (or pass one as the second argument)"
  root=$(cd "$(dirname "$0")/.." && pwd)
  json=$(terraform -chdir="$root/infra/gcp" output -json name_servers) \
    || die 2 "terraform output name_servers failed (logged in to ryft-lab? applied?): pass a Cloud DNS server as the second argument"
  cloud=$(printf '%s' "$json" | jq -r 'if type == "array" then (.[0] // "") else "" end') || cloud=
  cloud=${cloud%.}
  [ -n "$cloud" ] || die 2 "terraform output name_servers holds no server"
fi
# The names reach dig as @server: a pattern each, so nothing can pass for an option, and the two
# cannot be swapped or be the same.
[[ "$route53" =~ ^ns-[0-9]+\.awsdns-[0-9]+\.(com|net|org|co\.uk)$ ]] || die 2 "'$route53' is not a Route 53 name server (ns-<n>.awsdns-<n>.<tld>)"
[[ "$cloud" =~ ^ns-cloud-[a-z][0-9]+\.googledomains\.com$ ]] || die 2 "'$cloud' is not a Cloud DNS name server (ns-cloud-<x><n>.googledomains.com)"

# ask <server> <type> <name>: the answer, one record per line, sorted and without duplicates. CNAME,
# MX and SRV targets are host names, so compared in lower case; TXT and CAA values exactly.
ask() {
  local out
  out=$(dig +short +norecurse +time=5 +tries=2 "$2" "$3" "@$1" 2>&1) || die 2 "no answer from $1 for $2 $3: $(printf '%s' "$out" | head -c 200)"
  # dig's own remarks (";; ...") instead of records: a timeout, a refusal.
  case $'\n'"$out" in *$'\n;'*) die 2 "$1 for $2 $3: $(printf '%s' "$out" | head -c 200)" ;; esac
  case "$2" in
    CNAME|MX|SRV) printf '%s\n' "$out" | sed 's/[[:space:]]*$//' | grep -v '^$' | tr '[:upper:]' '[:lower:]' | LC_ALL=C sort -u || true ;;
    *) printf '%s\n' "$out" | sed 's/[[:space:]]*$//' | grep -v '^$' | LC_ALL=C sort -u || true ;;
  esac
}
# only <a> <b>: the lines of a that b lacks.
only() { LC_ALL=C comm -23 <(printf '%s\n' "$1" | grep -v '^$' || true) <(printf '%s\n' "$2" | grep -v '^$' || true); }
flat() { printf '%s' "$1" | tr '\n' '|' | sed 's/|$//; s/|/ | /g'; }

# Both servers are authoritative for the zone, and each is its provider's (the SOA names the hostmaster).
soa53=$(ask "$route53" SOA "$zone")
soagcp=$(ask "$cloud" SOA "$zone")
[[ "$soa53" == *awsdns-hostmaster.amazon.com.* ]] || die 2 "$route53 is not authoritative for $zone (SOA: '${soa53:-none}')"
[[ "$soagcp" == *cloud-dns-hostmaster.google.com.* ]] || die 2 "$cloud is not authoritative for $zone (SOA: '${soagcp:-none}'): is apply 1 done, is this one of terraform output name_servers?"

echo "Route 53:  $route53"
echo "Cloud DNS: $cloud"
bad=0
fail() { echo "✗ $*"; bad=$((bad + 1)); }
for rec in "${records[@]}"; do
  read -r type name <<< "$rec"
  r53=$(ask "$route53" "$type" "$name")
  gcp=$(ask "$cloud" "$type" "$name")
  if [ -z "$r53" ]; then fail "$type $name: Route 53 answers nothing (a record set this list expects)"; continue; fi
  missing=$(only "$r53" "$gcp")
  added=$(only "$gcp" "$r53")
  case "$type $name" in
    "CAA $zone")
      want=$(printf '%s\n%s\n' "$r53" "$caa_added" | LC_ALL=C sort -u)
      if [ "$gcp" != "$want" ]; then
        fail "$type $name: Cloud DNS must hold Route 53's set plus exactly $caa_added; missing: '$(flat "$missing")', added: '$(flat "$added")'"
        continue
      fi
      echo "✓ $type $name (plus $caa_added, intended)" ;;
    "TXT $zone")
      if [ -n "$missing" ] || [ "$(printf '%s' "$added" | grep -c '')" != 1 ] || ! [[ "$added" =~ $verification_re ]]; then
        fail "$type $name: Cloud DNS must hold Route 53's strings plus exactly one google-site-verification= string; missing: '$(flat "$missing")', added: '$(flat "$added")'"
        continue
      fi
      echo "✓ $type $name (plus the Search Console string, intended)" ;;
    *)
      if [ -n "$missing" ] || [ -n "$added" ]; then
        fail "$type $name: Route 53 '$(flat "$r53")', Cloud DNS '$(flat "$gcp")'"
        continue
      fi
      echo "✓ $type $name" ;;
  esac
done

ds=$(ask "$parent" DS "$zone")
if [ -n "$ds" ]; then fail "DS $zone at $parent: '$(flat "$ds")'; DNSSEC must stay off, remove the DS at GoDaddy first"
else echo "✓ no DS for $zone at $parent (DNSSEC off)"; fi

[ "$bad" = 0 ] || die 1 "$bad difference(s): do not switch the name servers"
echo "✓ dns_compare: every record matches (${#records[@]} record sets, the two intended additions checked, no DS)"
