// Package gate owns the decision and the answer of the beta invite gate, the consent gate and the App Attest token check, for a caller already authenticated.
//
// It must not read the database: the user row, consent included, arrives with
// the request, loaded fresh every time, so a withdrawal counts at once; an app
// token is checked against the ring alone (internal/appattest). It also knows no
// routes; each service decides where to run a gate and which routes App Attest
// exempts.
//
// Invariants:
//   - Sync (ingest) and the training import (api) share one consent rule (D129).
//     Run it before the body is read, so a refused upload costs no parse.
//   - Off passes, warn serves the request and names the code in X-RYFT-Gate, on
//     answers 403 with a code (D87). Every warning and refusal is counted. The
//     import route raises the consent gate from warn to on (docs/API.md → Train).
//   - Consent counts only for the privacy policy version in force (D78).
//   - App Attest (D245, D247): off never reads X-RYFT-App-Token; warn serves a
//     request without a valid token unchanged (no X-RYFT-Gate) and logs it; on
//     answers 403 app_attest_required. It runs after the bearer and the per-user
//     limit, before the beta and consent gates. Its warn line "app attest" is
//     written without the request's log fields, so it names no account.
//
// See docs/API.md → Errors, Conventions → App Attest; D78, D87, D129, D245, D247.
package gate
