"""GET /v1/admin/users/{id} (stdin) as a readable page (user.sh)."""
import json
import sys

d = json.load(sys.stdin)
a, data = d["account"], d["data"]


def t(v):
    return (v or "-").replace("T", " ")[:16]


print("account   %s  (%s)" % (a["id"], a["provider"] or "-"))
print("  created %s, email %s%s" % (t(a["created_at"]), a["email"] or "-", " (Apple relay)" if a["private_relay"] else ""))
if a["identities"] is None:
    print("  sign-in %s" % a.get("identities_note", "-"))
for i in a["identities"] or []:
    print("  sign-in %-6s %s%s, last %s" % (i["provider"], i["email"] or "-", " (relay)" if i["private_relay"] else "",
                                          t(i["last_sign_in_at"])))
c = a["consent"]
print("  beta access %s | consent %s | max HR %d (%s) | %s" % (
    t(a["beta_access_at"]) if a["beta_access_at"] else "no",
    "%s (%s, app %s)" % (c["policy_version"], t(c["granted_at"]), c["app_version"] or "-") if c else "none",
    a["max_hr"], a["max_hr_source"], a["tz"]))
print("devices")
for x in d["devices"] or []:
    print("  %s %-14s app %-12s %-10s first %s, last %s" % (x["id"][:8], x["name"] or "-", x["app_version"] or "-",
                                                          x["os_version"] or "-", t(x["first_sync_at"]), t(x["last_sync_at"])))
    if x["last_sync"]:
        print("           last sync: %s" % json.dumps(x["last_sync"]))
    if x["push_env"]:
        print("           push %s, last delivered %s" % (x["push_env"], t(x["push_last_success_at"])))
print("data (14 days)  scored %d, with stages %d, with HRV %d; open feedback %d" % (
    data["days_scored_14d"], data["nights_with_stages_14d"], data["days_with_hrv_14d"], data["open_feedback"]))
latest = data.get("latest") or {}
print("  newest: " + (", ".join("%s %s" % (k, t(v)) for k, v in sorted(latest.items())) or "nothing"))
for x in d["possible_duplicates"]:
    print("possible duplicate  %s (%s): last sync %s, %d days scored, newest record %s" % (
        x["short_id"], x["provider"] or "-", t(x["last_sync_at"]), x["days_scored"], t(x["newest_record"])))
