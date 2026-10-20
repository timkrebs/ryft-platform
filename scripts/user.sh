#!/usr/bin/env bash
# One account (docs/OPERATIONS.md → Beta): GET /v1/admin/users/{id} as a readable
# page, and the operator's actions on it. The account: its full id, its first 8
# digits (as the logs and beta_report.sh show them), or its email.
#
#   scripts/user.sh <id|short id|email> [--emails]
#   scripts/user.sh <id> --grant | --revoke          # beta access without a code
#   scripts/user.sh <id> --merge-into <id> [--dry-run] [--force] [--yes]
#                                                    # move everything to the other account, delete this one
#   scripts/user.sh <id> --json                      # the raw answer
#
# The admin key: ADMIN_API_KEY, else infra/vault's terraform output, else Vault
# (scripts/lib/admin.sh). Every call is audited.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib/admin.sh"
who="" action=show emails="" json="" into="" dry="" force="" yes=""
while [ $# -gt 0 ]; do
  case "$1" in
    --emails) emails=1; shift ;;
    --grant) action=grant; shift ;;
    --revoke) action=revoke; shift ;;
    --json) json=1; shift ;;
    --merge-into) action=merge; into=$2; shift 2 ;;
    --dry-run) dry=1; shift ;;
    --force) force=1; shift ;;
    --yes) yes=1; shift ;;
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
  merge)
    plan() {   # the answer of a merge as a table
      python3 -c '
import json, sys
d = json.load(sys.stdin)
print("%s merge %s (%d scored days) into %s (%d)" % ("plan:" if d["dry_run"] else "done:", d["from"]["short_id"],
      d["from"]["days_scored"], d["to"]["short_id"], d["to"]["days_scored"]))
for t in d["tables"]:
    if t["moved"] or t["dropped"]:
        print("  %-18s moved %6d  dropped %6d   %s" % (t["table"], t["moved"], t["dropped"], t["rule"]))
print("  consent %s, beta access %s, rescore %s" % (d["consent"], d["beta_access"],
      "%s..%s" % (d["rescore"]["from"], d["rescore"]["to"]) if d["rescore"] else "none"))
if d.get("deleted"):
    print("  %s deleted (Supabase sign-in %s, Apple grant %s)" % (d["from"]["short_id"],
          "deleted" if d["deleted"]["auth_user_deleted"] else "NOT deleted", d["deleted"].get("apple_token_revoked")))'
    }
    q="dry_run=1${force:+&force=1}"
    ryft_admin POST "/v1/admin/users/$id/merge-into/$into?$q" | plan
    [ -z "$dry" ] || exit 0
    if [ -z "$yes" ]; then
      printf 'This moves the data and deletes %s for good. Type MERGE to go on: ' "$id"
      read -r answer
      [ "$answer" = MERGE ] || { echo "not merged"; exit 1; }
    fi
    ryft_admin POST "/v1/admin/users/$id/merge-into/$into${force:+?force=1}" | plan ;;
  show)
    out=$(ryft_admin GET "/v1/admin/users/$id${emails:+?emails=1}")
    if [ -n "$json" ]; then echo "$out"; else echo "$out" | python3 "$HERE/lib/user_detail.py"; fi ;;
esac
