// Package gate owns the decision and the answer of the beta invite gate and the consent gate, for a user the caller has already loaded.
//
// It must not read the database: the user row, consent included, arrives with
// the request, loaded fresh every time, so a withdrawal counts at once. It also
// knows no routes; each service decides where to run a gate.
//
// Invariants:
//   - Sync (ingest) and the training import (api) share one consent rule (D129).
//     Run it before the body is read, so a refused upload costs no parse.
//   - Off passes, warn serves the request and names the code in X-RYFT-Gate, on
//     answers 403 with a code (D87). Every warning and refusal is counted. The
//     import route raises the consent gate from warn to on (docs/API.md → Train).
//   - Consent counts only for the privacy policy version in force (D78).
//
// See docs/API.md → Errors; D78, D87, D129.
package gate
