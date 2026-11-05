# API.md: the invite part of the admin-web contract (for PR2)

> Status: the text as cut out of #125. Where `docs/API.md` differs, API.md wins (PR2 fix round 1 added the 14-day
> default and 90-day maximum expiry of a bound code, the `422` while bound invites are unavailable and the retention
> of the note).

Source: PR #125 (`docs/admin-web-contract`, d6284d7), the hunks that PR1 (`feat/admin-web-auth`) left out because
PR1 implements none of them. PR2 (`api/db: invites for a person, revoke, who redeemed`, migration 0033) re-adds them
to `docs/API.md` **verbatim**, at the places named below. Every JSON example is typed against the models PR2 adds
(`model.BetaInvite` gains `email`, `created_by` (`model.Actor`, which PR1 adds), `revoked_at`, `status`, `link`,
`uses_by` (`model.InviteUse`); `model.BetaInviteInput` gains `email`), so `TestAPIDocExamples` is green only together
with PR2's model change.

What PR1's `docs/API.md` holds instead, which PR2 replaces or amends:

- the section `### Invite codes (existing shapes)`: replaced by the section in A below (the heading and everything up to
  `### Users, feedback, flags (existing shapes)`);
- the route-table row for the invites routes lists `GET /v1/admin/invites` (no `?emails=1`): replaced by B;
- the audit example's first row has `"details": {}`: PR2 makes the invite create and revoke write the code into
  `details` (design §6.3) and so takes the example in G;
- the changelog row of 2026-11-05 in PR1 is about the admin sign-in only: PR2 adds the row in H as a new row above it
  (same date, the invite part), and does not edit PR1's row.

Anything not listed here is as PR1 has it.

---

## A. Replace `### Invite codes (existing shapes)` with this section

### Invites for a person

An invite code (`RYFT-7K3M-QX9P`; case, spaces and dashes never matter where a code is read) is either
**unbound** (anyone with the code, today's behaviour) or **bound to an email address**. The tester opens the
universal link `https://ryftapp.de/invite/RYFT-7K3M-QX9P` (the `link` field) or types the code in the app
(`POST /v1/me/invite`).

#### `POST /v1/admin/invites` — create a code

| Field | Type | Rule |
| --- | --- | --- |
| `note` | string | optional, at most 200 characters (`422 "note has at most 200 characters"`); may name the person; never shown to the tester |
| `max_uses` | int? | 1 to 1000, default 1 (`422 "max_uses must be 1-1000"`) |
| `expires_at` | RFC 3339? | must be in the future (`422 "expires_at must be in the future"`); **omitted means it never expires** (the page sends 14 days) |
| `email` | string? (**new**) | the address this code is for. Trimmed and lower-cased; ASCII printable, at most 254 characters, one `@`, a dot in the domain, else `422 "email is not a valid address"`. A bound code has `max_uses` 1: a larger value is `422 "a code for one email has max_uses 1"`. `null` or omitted: an unbound code. The api never looks the address up |

<!-- type: model.BetaInviteInput -->
```json
{ "note": "Anna", "email": "Anna@Example.com", "max_uses": 1, "expires_at": "2026-11-19T10:00:00Z" }
```

`201` with the new `BetaInvite`. In this answer `email` is the address **as stored** (lower case, not masked:
the caller just sent it), `uses_by` is `[]` and `created_by` is the caller's actor.

<!-- type: model.BetaInvite -->
```json
{ "code": "RYFT-7K3M-QX9P", "note": "Anna", "max_uses": 1, "uses": 0,
  "expires_at": "2026-11-19T10:00:00Z", "created_at": "2026-11-05T10:00:00Z",
  "email": "anna@example.com", "created_by": { "kind": "user", "id": "1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081" },
  "revoked_at": null, "status": "open", "link": "https://ryftapp.de/invite/RYFT-7K3M-QX9P",
  "uses_by": [] }
```

#### `GET /v1/admin/invites[?emails=1]` — the list

Still a JSON **array** of `BetaInvite`, newest first, without pagination (beta size). Every code is listed,
revoked and used-up ones too. The new fields are all additive:

| Field | Type | Meaning |
| --- | --- | --- |
| `code`, `note`, `max_uses`, `uses`, `expires_at`, `created_at` | as before | unchanged |
| `email` | string? | the bound address; `null` for an unbound code. **Masked** (`a***@e***.com`) unless `?emails=1`. The api clears it 30 days after the code could no longer be redeemed (revoked, expired or used up), so `null` on a code that is not `open` does **not** mean "anyone with the code" |
| `created_by` | `Actor` | who created it; `{"kind": "key", "id": null}` for codes from before 2026-11-05 |
| `revoked_at` | RFC 3339? | set by `DELETE`; `null` otherwise |
| `status` | string | **closed** enum, the first match wins: `revoked` (`revoked_at` set), `used` (`uses` ≥ `max_uses`), `expired` (`expires_at` not in the future), `open` |
| `link` | string | `<PUBLIC_WEB_URL>/invite/<code with dashes>`, i.e. `https://ryftapp.de/invite/RYFT-7K3M-QX9P`. Always present |
| `uses_by` | `[InviteUse]` | the accounts that redeemed the code, oldest first |

`InviteUse`: `user_id` (uuid), `short_id` (its first 8 hex digits), `email` (string?, the account's email, masked unless
`?emails=1`; `null` when the account has none), `private_relay` (bool, an Apple "Hide My Email" address) and `at`
(RFC 3339). `uses_by` lists only accounts that still exist: `uses` stays the count, so `uses > len(uses_by)` means
"redeemed by an account deleted since". Codes redeemed before 2026-11-05 are listed with `at` = the account's
`beta_access_at`. The call is audited with `emails` true or false. The page passes `?emails=1`:

<!-- type: []model.BetaInvite -->
```json
[
  { "code": "RYFT-7K3M-QX9P", "note": "Anna", "max_uses": 1, "uses": 1,
    "expires_at": "2026-11-19T10:00:00Z", "created_at": "2026-11-05T10:00:00Z",
    "email": "anna@example.com", "created_by": { "kind": "user", "id": "1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081" },
    "revoked_at": null, "status": "used", "link": "https://ryftapp.de/invite/RYFT-7K3M-QX9P",
    "uses_by": [ { "user_id": "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", "short_id": "9f8e7d6c", "email": "anna@example.com",
                   "private_relay": false, "at": "2026-11-05T18:02:11Z" } ] },
  { "code": "RYFT-H2NP-4WCX", "note": "Ben", "max_uses": 1, "uses": 0,
    "expires_at": "2026-10-30T12:00:00Z", "created_at": "2026-10-21T09:00:00Z",
    "email": "ben@example.com", "created_by": { "kind": "key", "id": null },
    "revoked_at": "2026-10-30T12:00:00Z", "status": "revoked", "link": "https://ryftapp.de/invite/RYFT-H2NP-4WCX",
    "uses_by": [] },
  { "code": "RYFT-A9KT-3DMF", "note": "TestFlight wave 2", "max_uses": 20, "uses": 3,
    "expires_at": null, "created_at": "2026-10-20T08:00:00Z",
    "email": null, "created_by": { "kind": "key", "id": "3fa1c2d9" },
    "revoked_at": null, "status": "open", "link": "https://ryftapp.de/invite/RYFT-A9KT-3DMF",
    "uses_by": [ { "user_id": "0a1b2c3d-4e5f-4061-8283-949a5b6c7d8e", "short_id": "0a1b2c3d", "email": "mia@icloud.com",
                   "private_relay": false, "at": "2026-10-22T07:41:00Z" },
                 { "user_id": "7c6b5a49-3827-4160-8f0e-1d2c3b4a5968", "short_id": "7c6b5a49", "email": "x7k2m9q4@privaterelay.appleid.com",
                   "private_relay": true, "at": "2026-10-23T19:05:30Z" } ] }
]
```

Without `?emails=1` the same first entry reads:

<!-- type: []model.BetaInvite -->
```json
[
  { "code": "RYFT-7K3M-QX9P", "note": "Anna", "max_uses": 1, "uses": 1,
    "expires_at": "2026-11-19T10:00:00Z", "created_at": "2026-11-05T10:00:00Z",
    "email": "a***@e***.com", "created_by": { "kind": "user", "id": "1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081" },
    "revoked_at": null, "status": "used", "link": "https://ryftapp.de/invite/RYFT-7K3M-QX9P",
    "uses_by": [ { "user_id": "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", "short_id": "9f8e7d6c", "email": "a***@e***.com",
                   "private_relay": false, "at": "2026-11-05T18:02:11Z" } ] }
]
```

#### `DELETE /v1/admin/invites/{code}` — revoke

**The meaning of this route changes.** `DELETE` no longer removes the row: it **revokes** the code. The row stays
in the list with `status: "revoked"`; a hard delete is not offered. A revoked code redeems nothing, and accounts that
already redeemed it keep their access. `204` with no body, also when the code was already revoked (idempotent; the
first revoke time is kept); `404 "no such code"` for a code that does not exist. `{code}` may be written with or
without dashes. A revoke also sets `expires_at` to the revoke time (an earlier value is kept), so the listed
`expires_at` of a revoked code is not the one it was created with: read `status`.

#### Redeeming: `POST /v1/me/invite`

The request and the answers are unchanged: `{"code": "…"}` → `204`, and **one** `422 "invalid or used code"` for
every refusal (unknown, expired, used up, and now also **revoked**, **bound to another email**, and bound while the
account's email is not confirmed). The binding compares the email address Supabase holds for the account, and only
when Supabase has confirmed it (not the token's `email` claim, not the app's stored copy), trimmed and lower-cased,
with no plus-address or dot folding. A Sign in with Apple "Hide My Email" address matches only a code made for exactly
that relay address: when the tester used it and the operator does not have it, the operator leaves `email` empty.
A bound code has one use, so an account cannot redeem it twice (a second attempt is the `422`; an account that already
has access still gets `204` without using the code). New for **bound** codes only: when Supabase's account lookup is
unavailable the answer is `503` with `Retry-After: 5` and no `code` (`"the invite could not be checked right now"`);
send the same request again. Unbound codes never depend on that lookup. The 5 attempts an hour apply as before. The
app's `InvitePost` and its handling of this route do not change.

---

## B. The route-table row of the invites routes (`### Routes and who may call them`)

| `POST /v1/admin/invites`, `GET /v1/admin/invites[?emails=1]`, `DELETE /v1/admin/invites/{code}` | yes | yes | Invites |

---

## C. `### POST /v1/me/invite — redeem an invite code`: in the paragraph, replace the two lines that start with `used-up code (one message for all three).` (PR1 has them as on `main`; the paragraph's first lines up to `... for an unknown, expired or` stay) with these six lines:

used-up code (one message for all three), and, since 2026-11-05, for a **revoked** code and for a code
**bound to an email address** that is not the account's confirmed email: one message and one status for every
refusal, so the answer reveals nothing about a code. At most 5 attempts per user and hour (`429`).
An account that already has access gets `204` without using the code. Only for a bound code, `503` with
`Retry-After: 5` (and no `code`) when the account's email cannot be checked right now: send the same request
again. The request and the app's handling do not change (Admin routes → Invites for a person has the rules).

---

## D. The status table in Errors, the `503` row: insert, after the clause on Apple (`POST /v1/me/apple-authorization`; 5)

`; the account lookup for a code bound to an email is unavailable (`POST /v1/me/invite`; 5)`

(The whole `503` row of PR #125, for context:)

| `503` | — | The server cannot answer now, always with `Retry-After` except for `GET /readyz`: the token or the account could not be loaded at the start of the request (Supabase keys or the database unreachable — **not** a sign-in problem; `Retry-After: 5`); the request ran out of time (`"request timed out"`; 5); `POST /v1/sync` could not apply its page (the database, or a concurrent sync of the same account; 5) or `POST /v1/train/import` could not finish (5); the queue is down (`POST /v1/recompute`, or the completion or edit of a session, which is already stored; 30); Apple is unreachable (`POST /v1/me/apple-authorization`; 5); the account lookup for a code bound to an email is unavailable (`POST /v1/me/invite`; 5); on `/v1/admin/*` the signing keys or the database for an admin token (5). `GET /readyz` answers `503` without `Retry-After` when a dependency is down. | Wait `Retry-After` seconds and send the same request again (a sync and an import are idempotent; after a `POST /v1/recompute` `503` the retry can get `429` with `Retry-After: 60`, since the minute is already used); keep the session. The `503` of `GET /readyz` is for probes, not the app. |

---

## E. The first table at the top of API.md (the operator page row, with the invite wording)

| Operator web page `https://ryftapp.de/admin` (not an app screen; Supabase sign-in with TOTP) | invites for a person (create, copy and share the link, list, revoke, who redeemed), users (beta access), feedback, flags (read-only) | **Admin routes** below: `POST`/`GET /v1/admin/invites`, `DELETE /v1/admin/invites/{code}`, `GET /v1/admin/users`, `POST`/`DELETE /v1/admin/users/{id}/access`, `GET /v1/admin/feedback`, `POST /v1/admin/feedback/{id}/resolve`, `GET /v1/admin/flags` |

---

## F. `### The actor: who did it`: replace the two paragraphs after the table (the one starting `` `kind` is a **closed** enum`` and the one starting `` `AuditRecord.actor` is new``) with

`kind` is a **closed** enum. `id` is `string?`: it is `null` only in the `created_by` of an invite created
before 2026-11-05 by the key (the fingerprint was not kept: `{"kind": "key", "id": null}`). An attempt on a
key-only route is audited too, with status `403`. `GET /v1/admin/audit?since=` takes RFC 3339 or `YYYY-MM-DD`
(default 7 days), newest first, at most 1 000 rows; another format is `422`.

`AuditRecord.actor` is new. `key_fingerprint` stays (a string; never removed) and is `""` on rows whose actor is a
user. `details` is an open object (the route adds a few values, never an email): a create or revoke of an invite holds
its `code`, a list of users or invites holds `emails` (whether the addresses were asked for).

---

## G. The audit example (`<!-- type: []model.AuditRecord -->`): the first row's `details` is `{ "code": "RYFT-7K3M-QX9P" }`

(The whole block of PR #125:)

<!-- type: []model.AuditRecord -->
```json
[
  { "id": 9142, "at": "2026-11-05T18:02:44Z", "key_fingerprint": "",
    "actor": { "kind": "user", "id": "1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081" },
    "action": "POST /v1/admin/invites", "target_user": null, "status": 201,
    "request_id": "01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c", "details": { "code": "RYFT-7K3M-QX9P" } },
  { "id": 9141, "at": "2026-11-05T17:58:03Z", "key_fingerprint": "",
    "actor": { "kind": "user", "id": "1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081" },
    "action": "PUT /v1/admin/flags/{key}", "target_user": null, "status": 403,
    "request_id": "01a14ff9-91c0-7a2e-b6d3-0f4e5a6b7c8d", "details": {} },
  { "id": 9140, "at": "2026-11-05T09:12:31Z", "key_fingerprint": "3fa1c2d9",
    "actor": { "kind": "key", "id": "3fa1c2d9" },
    "action": "GET /v1/admin/users", "target_user": null, "status": 200,
    "request_id": "01a14ff1-0b7a-7d21-a3c4-5e6f708192a3", "details": { "emails": false, "n": 12 } }
]
```

---

## H. A new changelog row for PR2 (the invite part of PR #125's row, which is reproduced whole below for reference)

| 2026-11-05 | **Invites for a person** (the operator's page; the app is not involved): `POST /v1/admin/invites` takes an optional `email` (a bound code has `max_uses` 1; `422` otherwise); `BetaInvite` gains `email` (masked unless `?emails=1`), `created_by` (`Actor`), `revoked_at`, `status` (`open` \| `used` \| `expired` \| `revoked`, closed), `link` and `uses_by`; `GET /v1/admin/invites` is still an array; `DELETE /v1/admin/invites/{code}` now **revokes** (`204`, idempotent; the row stays listed) instead of deleting; `POST /v1/me/invite` refuses a revoked code and a bound code whose email is not the account's confirmed one, with the same single `422 "invalid or used code"`, and answers `503` with `Retry-After: 5` for a bound code when the account's email cannot be checked. **Behaviour:** nothing changes for the app, except that `DELETE /v1/admin/invites/{code}` no longer removes the row; codes created before this entry are unbound and list as created by `{"kind": "key", "id": null}`. Old clients ignore every new field. **Swift:** none (`InvitePost` is unchanged and the admin types are not mirrored). |

PR #125's own row, whole:

| 2026-11-05 | **Admin web** (the operator's page at `https://ryftapp.de/admin`; the app is not involved, so no `API_REQUESTS.md` entry answers to this): a new section [Admin routes (operator only)](#admin-routes-operator-only) documents `/v1/admin/*`, which API.md did not cover before. **Auth:** `/v1/admin/*` accepts the admin key (unchanged) **or** a Supabase access token with `app_metadata.role` `admin`, an allow-list row and a TOTP (`aal2`, `amr` `totp`) at most 12 h old; every other bearer gets one identical `401`; a valid, allow-listed admin without a fresh second factor gets `403` with the new `code` **`mfa_required`** (Errors → `code` table and the `403` row); merge, flag writes, user detail and resync-hint stay key-only (`403` for a token, no `code`); `/v1/admin/*` has a per-address limit (10/s, bursts of 20, `429` with `Retry-After`). **CORS:** only `/v1/admin/*`, only for the origins in `ADMIN_WEB_ORIGINS` (empty = none), `GET, POST, DELETE`, `Authorization`, `Content-Type`, preflight answered without auth, `Max-Age` 600, no credentials, `X-Request-ID` and `Retry-After` exposed. **Invites for a person:** `POST /v1/admin/invites` takes an optional `email` (a bound code has `max_uses` 1; `422` otherwise); `BetaInvite` gains `email` (masked unless `?emails=1`), `created_by`, `revoked_at`, `status` (`open` \| `used` \| `expired` \| `revoked`, closed), `link` and `uses_by`; `GET /v1/admin/invites` is still an array; `DELETE /v1/admin/invites/{code}` now **revokes** (`204`, idempotent; the row stays listed) instead of deleting; `POST /v1/me/invite` refuses a revoked code and a bound code whose email is not the account's confirmed one, with the same single `422 "invalid or used code"`, and answers `503` with `Retry-After: 5` for a bound code when the account's email cannot be checked. **Audit:** `AuditRecord.actor` (`{kind: key \| user, id}`, `Actor`, closed `kind`); `key_fingerprint` stays and is `""` for a user actor. **Behaviour:** nothing changes for the app or for key-based scripts, except that `DELETE /v1/admin/invites/{code}` no longer removes the row; codes created before this entry are unbound and list as created by `{"kind": "key", "id": null}`. Old clients ignore every new field. **Swift:** the mirror gains `APIErrorCode.mfaRequired` for completeness; the app needs no change, it never calls `/v1/admin/*` (`InvitePost` and `APIErrorBody` are unchanged, and the admin types are not mirrored). |
