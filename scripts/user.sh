#!/usr/bin/env bash
# One account (docs/OPERATIONS.md → Beta): GET /v1/admin/users/{id} as a readable
# page, and the operator's actions on it. The account: its full id, its first 8
# digits (as the logs and beta_report.sh show them), or its email.
#
#   scripts/user.sh <id|short id|email> [--emails]
#   scripts/user.sh <id> --grant | --revoke          # beta access without a code
#   scripts/user.sh <id> --json                      # the raw answer
#
# The admin key: ADMIN_API_KEY, else infra/vault's terraform output, else Vault
# (scripts/lib/admin.sh). Every call is audited.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib/admin.sh"
who="" action=show emails="" json=""
while [ $# -gt 0 ]; do
  case "$1" in
    --emails) emails=1; shift ;;
    --grant) action=grant; shift ;;
    --revoke) action=revoke; shift ;;
    --json) json=1; shift ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    -*) echo "unknown option $1" >&2; exit 2 ;;
    *) who=$1; shift ;;
  esac
done
[ -n "$who" ] || { echo "usage: scripts/user.sh <id|short id|email> [--emails|--grant|--revoke|--json]" >&2; exit 2; }
ryft_admin_key

# An email: find its account in the list (an audited read with emails).
id=$who
case "$who" in
  *@*)
    id=$(ryft_admin GET "/v1/admin/users?emails=1" | python3 -c '
import json, sys
want = sys.argv[1].lower()
hits = [u["id"] for u in json.load(sys.stdin) if (u.get("email") or "").lower() == want]
print(hits[0] if len(hits) == 1 else "")' "$who")
    [ -n "$id" ] || { echo "✗ no account with the email $who (Apple relay addresses differ from the real one)" >&2; exit 1; } ;;
esac

case "$action" in
  grant) ryft_admin POST "/v1/admin/users/$id/access" >/dev/null && echo "✓ beta access granted to $id" ;;
  revoke) ryft_admin DELETE "/v1/admin/users/$id/access" >/dev/null && echo "✓ beta access taken from $id" ;;
  show)
    out=$(ryft_admin GET "/v1/admin/users/$id${emails:+?emails=1}")
    if [ -n "$json" ]; then echo "$out"; else echo "$out" | python3 "$HERE/lib/user_detail.py"; fi ;;
esac
