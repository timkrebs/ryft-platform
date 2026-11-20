// Package appattesttest builds App Attest test vectors: a generated CA chain, attestations and assertions in CBOR, signed with real P-256 keys.
//
// It is for tests only (depguard refuses it elsewhere) and imports no package of
// this module, so internal/appattest's own tests can use it as well as the
// handler tests of cmd/api. Nothing here is Apple's: the root is generated per
// test, so a verifier must be built with appattest.NewVerifierWithRoot and the
// App's Root.
//
// See docs/API.md → Conventions → App Attest; D245, D247.
package appattesttest
