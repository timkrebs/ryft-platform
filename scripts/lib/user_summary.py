"""A GET /v1/admin/users/{id} answer (stdin) in a few lines (trace.sh; user.sh --short)."""
import json
import sys

d = json.load(sys.stdin)
a, data = d["account"], d["data"]
ids = ", ".join(i["provider"] + (" (relay)" if i["private_relay"] else "") for i in (a["identities"] or []))
ids = ids or a.get("identities_note") or "-"
c = a["consent"]
consent = "%s since %s" % (c["policy_version"], c["granted_at"]) if c else "none"
print("user %s (%s; signs in with %s)" % (a["short_id"], a["provider"] or "-", ids))
print("  beta access %s, consent %s" % (a["beta_access_at"] or "no", consent))
print("  last sync %s: %s; %d device(s)" % (data["last_sync_at"] or "never",
                                           json.dumps(data["last_sync"]) if data["last_sync"] else "-", len(d["devices"])))
for x in d["possible_duplicates"]:
    print("  possible duplicate: %s (%s), last sync %s, %d days scored"
          % (x["short_id"], x["provider"] or "-", x["last_sync_at"], x["days_scored"]))
