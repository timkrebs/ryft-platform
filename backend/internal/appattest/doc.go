// Package appattest verifies Apple App Attest attestations and assertions and mints and checks the HMAC app tokens.
//
// It is pure: the standard library's crypto and encoding, time and uuid, and no
// database, network, log or metric. The handlers (cmd/api) and the token check
// (api and ingest) call it; the store keeps keys and challenges.
//
// Invariants:
//   - The checks run in docs/API.md's order (Conventions → App Attest: seven for
//     an attestation, then the assertion's), and only production attestations
//     pass (D247).
//   - Apple's App Attestation Root CA is embedded (apple_root.pem) and pinned by
//     its SHA-256 fingerprint in a test; nothing calls Apple.
//   - CBOR is decoded by a small strict decoder: definite lengths, text map
//     keys, no tags or floats, a depth and a size limit, no trailing bytes.
//   - Every refusal is ErrInvalid with a fixed step name (Step); no error holds
//     a byte of the input, a key id, a challenge or a token (AA4).
//   - A token is v1.<kid>.<body>.<mac> under the ring's first key, checked under
//     any of its keys without a database read, TokenTTL >= MinTokenTTL (AA3).
//
// See docs/API.md → Conventions → App Attest; D245 (the design and the token), D247.
package appattest
