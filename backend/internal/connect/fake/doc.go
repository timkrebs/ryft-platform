// Package fake is the one provider adapter of phase 1 (D257), a provider with no network, for tests, compose and make smoke.
//
// It runs under CONNECT_FAKE=on only; config refuses that setting, and "fake" in CONNECT_PROVIDERS, on Cloud Run.
//
// Driving it over HTTP (what the smoke does, against api on :8080 and ingest on :8081 in compose):
//
//  1. POST /v1/me/connections/fake/start answers an authorize_url. The fake "authorises" at once, so that URL is the
//     callback itself: <CONNECT_CALLBACK_BASE>/v1/connect/fake/callback?state=<state>&code=<code>.
//
//  2. GET that URL without following redirects: api answers 302 ryft://connect/fake?status=ok&ticket=<ticket>.
//
//  3. POST /v1/me/connections/fake/confirm {"ticket": "<ticket>"}.
//
//  4. The provider's user id is the first 16 hex characters of SHA-256(state), where state is the authorize URL's
//     "state" parameter (the smoke computes the same, or reads provider_user_id from GET /v1/me/connections).
//
//  5. Push to ingest: POST /connect/fake/push with the header X-Ryft-Fake-Signature = hex(HMAC-SHA256(
//     CONNECT_FAKE_PUSH_SECRET, body)) and a body of this form (an item's "user" is the provider's user id):
//
//     {"items":[{"kind":"sleep","record_id":"n-1","user":"<puid>","version":1,
//     "night":{"state":"final","segments":[{"start":"2026-11-23T22:30:00Z","end":"2026-11-24T00:30:00Z","stage":"light"}]}},
//     {"kind":"hrv","record_id":"h-1","user":"<puid>","version":1,
//     "hrv":{"start":"2026-11-24T02:00:00Z","end":"2026-11-24T02:05:00Z","rmssd_ms":48.5}}]}
//
//     Stages are light (RYFT's core), deep, rem and awake. A "deregistration" item (kind, record_id, user, version)
//     is the provider's notice that the user removed RYFT; a "permissions" item carries "permissions": [...].
//     The same body again is a duplicate; a changed one with version >= the stored one replaces the record.
//
// PKCE is checked for real: the code the fake hands out is the code challenge, and Exchange accepts it only with
// the verifier whose S256 it is.
package fake
