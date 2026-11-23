# Reviews: direct connections, phase 1

## Design review (security-privacy-reviewer, 2026-11-23)

| # | Sev | Section | Finding | Fix | State |
|---|---|---|---|---|---|
| B1 | blocker | §6.2, §4.2, §5.1 | A reconnect sets the live row `pending` and overwrites its tokens; an unconfirmed reconnect then deregisters a working link; the unique index on `status <> 'revoked'` covers `pending`, so a relink fails at callback | pending grant in its own table/columns; index only `connected`/`needs_reauth`; swap at confirm; a different provider user id = disconnect of the old one | resolved in design (amended 2026-11-23) |
| B2 | blocker | §6.2, §6.3 | attacker's own `authorize_url` sent to a connected victim expires → `Deregister` can end the victim's live grant → §8 deletes the victim's data | before any `Deregister` on expiry/cap/permission, if a live connection holds (provider, provider user id), delete tokens only; test | resolved in design (amended 2026-11-23) |
| S1 | should | §5.1, §7.2 | inbox unique on (provider, kind, record_id) across users with `DO UPDATE`: an id collision writes B's data into A's row | `user_id` in the key, or `DO UPDATE … WHERE user_id = EXCLUDED.user_id` | resolved in design (amended 2026-11-23) |
| S2 | should | §6.4 | `deleteDirectData` omits the direct `user_sources` row §8 promises to delete | add it | resolved in design (amended 2026-11-23) |
| S3 | should | §4.4, §5.1 | repeated confirm "answers 200" but `confirm_hash` is NULL after success | keep the hash until `confirm_expires_at` | resolved in design (amended 2026-11-23) |
| S4 | should | §6.2, logs | LB logs keep preview matches with the full URL 14 days (`infra/gcp/logging.tf:23`); Cloud Run 5xx entries keep the URL; `code` may trip sqli | scope-exclude the callback now, or a logging exclusion for `/v1/connect/` | resolved: design §6.2, §7.5; infra logging.tf + edge.tf |
| S5 | should | §7.2 | weak verification → forged pushes inject data; a forged deregistration deletes it | per-user cap on pending inbox rows; phase 2: a cryptographic push signature is a precondition | resolved in design (amended 2026-11-23) |
| S6 | should | §5.1 | `revoked` rows keep `provider_user_id`/`permissions` 90 days | null them | resolved in design (amended 2026-11-23) |
| S7 | should | §5.5 | Art. 15: export lacks `provider_user_id`, `permissions` | include them | resolved in design (amended 2026-11-23) |

§8 (delete on disconnect) judged sound (Art. 17(1)(b)); disclose backup retention and the 30-day deregistration queue.
Consent Q1: the reviewer reads consent as purpose-bound, not version-bound, and suggests `CONNECT_MIN_POLICY_VERSION`;
left to the user (design §17 Q1). The brief's "drop without consent in force" stays the default.

## PR #234 review (code-reviewer, 2026-11-23): no blocker

| # | Sev | Finding | Resolution |
|---|---|---|---|
| 1 | should | disconnect blocked by App Attest / invite gate | exempt from both (API.md, design §4.5) |
| 2 | should | the cap counts the caller's own reservation | own open states and pending grant never count |
| 3 | should | flag scope: confirm gated or not | gated; confirm while off → 404, pending grant handled like `confirm_expired` (B2 applies) |
| 4 | should | callback logging exclusion only with the edge built | unconditional (`infra/gcp/logging.tf`) |
| 5 | should | this file showed blockers open | rows marked resolved |
| nits | — | "3 open" scope, `deregistered` in two cases, a two-body example, `ConnectCallback` path parsing, `rmssd` ≠ direct, reserve `unknown` | fixed in API.md / design |
