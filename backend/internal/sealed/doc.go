// Package sealed encrypts the few secrets the platform stores (Apple refresh tokens, direct connections' tokens and PKCE verifiers) with AES-256-GCM.
//
// It must not be imported outside cmd/api: the key (DATA_ENCRYPTION_KEY, the
// Secret Manager secret data-key) reaches only the api: deploy/run/api.yaml pins
// it, and ryft-api is its only accessor (infra/gcp/secrets.tf).
//
// Invariants:
//   - The associated data, such as the user id, binds a ciphertext to its row; a
//     value copied to another row does not open.
//   - Every ciphertext starts with a version byte so the key can be rotated.
//   - A direct connection's tokens (access, refresh, pending or live) and the PKCE verifier of an open OAuth flow
//     are sealed here too, each under its own associated data (connect:access:<provider>:<user>, connect:state:<hash>,
//     ...; D257). Still api only: ingest's hook and consumer hold the push half of a provider and never a token
//     (R10 in internal/arch_test.go).
//
// See docs/ARCHITECTURE.md → Identities and trust; D48.
package sealed
