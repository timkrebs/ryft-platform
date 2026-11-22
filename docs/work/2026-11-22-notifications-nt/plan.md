# The app's notification requests NT1–NT7 (2026-11-22)

Source: the app's `docs/API_REQUESTS.md` → Notifications on `feat/reminder-scheduler` (`538ea17`), copied into
the head-architect's brief. Priorities: NT7, then NT5, then NT1/NT3/NT4. Two PRs from `origin/main`.

## PR 1: `push/device-token-follows-account`

### T1 (NT7): a device's push token follows its newest account (backend-engineer, database-engineer)
**Design (D251).** Each account keeps the installs it was the last account on: `users.push_devices uuid[]`
(newest last, at most 8). The auth step already reads the users row on every request (`EnsureUser`), so the hit
path costs nothing: when the request's `X-RYFT-Device-ID: D` is in the caller's list, nothing happens. On a miss
one statement (data-modifying CTEs, one round trip) **claims** D: it deletes every `push_tokens` row for D of
another account, removes D from every other account's list, and appends D to the caller's. So the claim runs on
an account's first request from an install, and again only after another account used that install.
Best effort: a failed claim is logged and the request goes on (the next request retries, nothing was recorded).
`POST /v1/devices/{id}/push` claims `{id}` in its transaction too (a client that sends no header).
The middleware runs after the bearer, the user load and App Attest, and before the beta gate. So the invite gate's
`403` still claims, and under `APP_ATTEST=on` a request without an app token doesn't.

Acceptance:
- Shared iPhone: A registers a token for D, A's session expires (no DELETE), B's first `GET /v1/me` with D drops
  A's row. No POST by B is needed. A's next request from D drops B's (A comes back).
- Failed DELETE: same as above with A signed out but its row left. B's first request ends it.
- Invite gate on, B without beta access: B's `POST /v1/devices/D/push` answers `403 invite_required` and A's row
  for D is gone anyway.
- Idempotent: a second request from (B, D) runs no claim (`push_devices` already holds D), and the counters prove it.
  A repeated claim changes nothing.
- No header, or a malformed one: nothing happens. Another account's token on another device is untouched.
- A `POST` for the same `{id}` by the same account replaces its token (one row per account and install).
- At most 8 devices per account. The ninth claim drops the oldest from the list, never a token.

### T2 (NT5): the flag `notifications.reminders` (database-engineer)
A migration, as `train.import` (0028) and `analytics.enabled` (0036): `enabled: true, rollout_pct: 0`, empty
allow-list, `ON CONFLICT DO NOTHING`. `TestRemoteConfig`'s seeded list gains it. Applied by the deploy's migration
step, so the user needs no command to create it. Raising it is the operator's existing `PUT /v1/admin/flags/{key}`.

### T3 (NT3, NT4, NT5 and NT7 in the contract): API.md (api-contract-owner)
- `POST /v1/devices/{id}/push`: the replace rule and the device-follows-account rule.
- `GET /v1/config`: `notifications.reminders` in the defaults table, with a paragraph on what it gates.
- Push notifications: NT4 (`SESSION_SCORED` and `PR` are **dropped** during quiet hours, as the app reads it;
  `MORNING_SCORE` waits). NT3 (an activity recorded in RYFT is not eligible for anything today; its HealthKit copy,
  when NT1 lands, is excluded by the rule in PR 2).
- The changelog entry.

### Docs (lead): DECISIONS D251–D252, STATUS, OPERATIONS (Feature flags; Push: the claim).

## PR 2: `push/activity-scored` (NT1; NT2, NT6 answered)
Contract first (api-contract-owner): the category row, keys, payload, `ryft.account` on every alert,
`categories.ACTIVITY_SCORED`, collapse id and expiry, the changelog. NT2 and NT6 answered under a "planned" note.
Then the sender (platform-architect design note, then backend-engineer). If the session runs short, PR 2 opens as
a draft with the contract only, and the report says so.

## Risks
- The claim is a write in the auth path: the CTE locks the other account's users row. Two accounts claiming the same
  install at the same moment can deadlock, so one claim fails. That is logged, the request still succeeds, and the
  claim is retried on the next request.
- Abuse: an account that knows another install's device id can drop that install's token until its owner's next
  request reclaims it and the app re-registers. Device ids are random per install and never shown to other
  accounts. The privacy review weighs this.

## Rollback
Revert the PR. The column and the flag stay. Both are additive, and an old api ignores them.

## Needs the user
Nothing for PR 1 beyond merging (the migrations run with the deploy). Raising `notifications.reminders` is the
operator's call, later, once the app's N4 has shipped.
