# Review: the domain move to ryftlab.de (PR #161, `a0aeefd..82623e8`)

security-privacy-reviewer, 2026-11-14. The review was read-only and scoped to the domain diff: the DNS and mail records, the IAM zone scoping, CORS, the two check changes of D215, and privacy. No live DNS was queried (dig needs approval in the session), so nothing is verified about what GoDaddy or Route 53 serve today.

**Result:** no blocker. There are three SHOULDs and four NITs. All three SHOULDs and two NITs are fixed in the follow-up commit; two NITs are left as they are, with reasons.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | SHOULD | `infra/aws/dns/main.tf`: the `*.ryftlab.de` CAA set held only `issuewild`. DNS answers it for every name that does not exist, so a CA stops there instead of climbing to the apex's `issue`. A CA ignores `issuewild` for a normal certificate (RFC 8659 §4.3), so any CA could issue for those names. The set does nothing for wildcard certificates either, because a CA checks those at the base name (§3). The same set existed in the old zone. | **Fixed.** `caa_wildcard` is gone. The apex `caa` now holds `0 issue` and `0 issuewild "letsencrypt.org"`. The test asserts both and that no `*.` record name remains. The plan is now **13/0/7** (D214, REBUILD 1b, STATUS). OPERATIONS → Mail checks a name that does not exist instead of `*.`. |
| 2 | SHOULD | The old domain is no longer protected by anything in the repository. If `ryftapp.de` were still delegated to the old zone's awsdns servers when 1b destroys the zone, the delegation would dangle and could be claimed: someone gets a certificate for the old API host, and old builds would send it Supabase JWTs and health data. Separately, without SPF and DMARC its support address can be spoofed. | **Fixed in the runbook.** REBUILD step 1's old-domain note: before 1b, `dig +norecurse NS ryftapp.de @a.nic.de` must list `domaincontrol.com`; **stop** if it lists awsdns. At GoDaddy: auto-renew, transfer lock, and no-mail records (`MX 0 .`, `v=spf1 -all`, `_dmarc` `p=reject`). Recorded in D214. These are the user's steps; the session could not run the dig. |
| 3 | SHOULD (privacy) | D215: `privacy_policy_url` and the Impressum point at a host that does not resolve. Consent to a text nobody can open is shaky (Art. 7, 13), and §5 DDG wants the Impressum always available. | **Fixed in the runbook.** REBUILD step 12 has a stop rule: nobody but the owner signs in (no testers, no App Review) until `/privacy`, `/de/privacy` and `/impressum` return 200. D215's consequences say so too. |
| 4 | NIT | `scripts/check_policy_in_force.sh`: the pin fallback's awk differs from `deploy_targets.sh` `tag_of`. It also matches a comment line, and then takes the next `newTag:` anywhere. Not a bypass, because `check_registry.sh` ties every rendered image to its pin. | **Fixed.** The fallback uses `tag_of`'s rule exactly: the `newTag` line right after `- name: ghcr.io/timkrebs/ryft/web`. A new test case covers a comment naming web followed by another image's pin. |
| 5 | NIT | `scripts/check_registry.sh`: the web reference it adds from the pin skips the shape rule that every rendered reference gets. | **Fixed.** The same `<tag>` shape rule now applies before the reference is added. A new test case: a pin `x/y` fails and asks the registry nothing. |
| 6 | NIT | `infra/aws/kubernetes/variables.tf`: `domain` has no validation. A stale tfvars with the old domain would scope the IAM roles to the old zone. That is still one zone, so nothing widens. | **Left as it is.** No tfvars sets `domain` (key names checked), and the variable exists so that a test or another cluster can differ. If a tfvars sets it to the old domain, the plan's data lookup fails once the old zone is destroyed. |
| 7 | NIT (unverified) | `mail.tf`: `include:secureserver.net` authorises GoDaddy's shared outbound servers, so with `aspf=r` DMARC passes for anything they relay with an `@ryftlab.de` sender, if GoDaddy allows that. | **Left as it is.** It is GoDaddy's published set, by the user's decision (D214). Moving to `p=reject` once both DKIM selectors resolve is noted for later. |

**Privacy, for the owner's new text (D214):**
- GoDaddy is the contracting processor (its DPA, with Microsoft as sub-processor).
- Where the tenant stores mail, and on what transfer basis, is unverified. Support mail can carry health data (Art. 9).
- Mail to the old support address is no longer received. Deletion and export in the app still cover Art. 17 and 20.
- Supabase sending as the domain through Microsoft would be a new flow, if it is ever built.

The rest of this note is recorded in D214.

**Checked and fine, by the reviewer:**
- The mail values match GoDaddy's set exactly. `selector1` is one commented line. DMARC sets no `sp`, so subdomains are quarantined too.
- Every CNAME and SRV target is a provider-owned host, so none can be taken over.
- Removing the old zone's block, with `prevent_destroy` on the new zone, does what D213 says. Nothing points at `Z08455293REDHBC9QW5MZ` except REBUILD's pre-check.
- ExternalDNS and cert-manager are scoped to one zone ARN each, and `domainFilters`, `dnsZone`, `hostedZoneID` and the txtOwner match.
- CORS is one exact origin. The admin page's `connect-src` comes from `admin.json`.
- The legal pages change only the domain and the contact.
- No live `ryftapp` is left outside the accepted list.

**Re-verified after the fixes:** by the platform lead, by reading the code; CI runs the terraform test and the script tests.
