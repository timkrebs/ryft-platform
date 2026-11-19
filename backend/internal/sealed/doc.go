// Package sealed encrypts the few secrets the platform stores (Apple refresh
// tokens) with AES-256-GCM.
//
// It must not be imported outside cmd/api: the key (DATA_ENCRYPTION_KEY, the
// Secret Manager secret data-key) reaches only the api: deploy/run/api.yaml pins
// it, and ryft-api is its only accessor (infra/gcp/secrets.tf).
//
// Invariants:
//   - The associated data, such as the user id, binds a ciphertext to its row; a
//     value copied to another row does not open.
//   - Every ciphertext starts with a version byte so the key can be rotated.
//
// See docs/ARCHITECTURE.md → Identities and trust; D48.
package sealed
