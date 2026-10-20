#!/usr/bin/env bash
# The beta at a glance (docs/OPERATIONS.md → Beta): one line per tester from
# GET /v1/admin/users, so whose data is broken shows; or the latest feedback.
# No emails unless --emails; the api logs every call with the admin key's
# fingerprint. Needs curl and jq.
#
#   ADMIN_API_KEY=$(terraform -chdir=infra/vault output -raw admin_api_key) scripts/beta_report.sh [--emails]
#   ADMIN_API_KEY=... scripts/beta_report.sh feedback [since]     # since: YYYY-MM-DD or RFC 3339, default 7 days
#   API_URL=http://localhost:8082 ADMIN_API_KEY=dev-admin-key-change-me-0123456789abcdef scripts/beta_report.sh
set -euo pipefail
API_URL=${API_URL:-https://api.ryftapp.de}
: "${ADMIN_API_KEY:?set ADMIN_API_KEY (terraform -chdir=infra/vault output -raw admin_api_key)}"
get() { curl -fsS -H "Authorization: Bearer $ADMIN_API_KEY" "$API_URL$1"; }
tab=$(printf '\t')

if [ "${1:-}" = feedback ]; then
  get "/v1/admin/feedback${2:+?since=$2}" | jq -r '
    (["id", "when", "user", "category", "app", "open", "message"]),
    (.[] | [(.id | tostring), (.created_at[:16] | sub("T"; " ")), .user_id[:8], .category,
            ((.app_version // "-") + (if .build then " (" + .build + ")" else "" end)),
            (if .resolved_at then "no" else "yes" end), (.message | gsub("[\t\n]"; " ") | .[:100])])
    | @tsv' | column -t -s "$tab"
  exit 0
fi

q=""
[ "${1:-}" = --emails ] && q="?emails=1"
get "/v1/admin/users$q" | jq -r --arg emails "$q" '
  (["user", "created", "provider", "beta", "consent", "app", "last sync", "sources", "scored/14", "staged/14",
    "hrv/14", "calibrating", "max hr", "open fb"] + (if $emails != "" then ["email"] else [] end)),
  (.[] | [.short_id, .created_at[:10], (.provider // "-"), (if .beta_access then "yes" else "no" end),
          (.consent_version // "-"), (.app_version // "-"), ((.last_sync_at // "-")[:16] | sub("T"; " ")),
          (.sources | map(.name + ":" + (.kinds | join(","))) | join(" ") | if . == "" then "-" else . end),
          (.days_scored_14d | tostring), (.nights_with_stages_14d | tostring), (.days_with_hrv_14d | tostring),
          (if .calibrating == null then "-" elif .calibrating then "yes" else "no" end), .max_hr_source,
          (.open_feedback | tostring)] + (if $emails != "" then [.email // "-"] else [] end))
  | @tsv' | column -t -s "$tab"
