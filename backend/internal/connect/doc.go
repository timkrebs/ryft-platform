// Package connect is the provider-agnostic side of direct connections: what a provider's adapter implements, the OAuth helpers and the registry (D257).
//
// The interfaces are OAuth and PushSide; the helpers are PKCE, the state and ticket secrets and the reserved
// source name; the registry holds the providers a service serves.
//
// A provider has two halves that never meet in one service. The OAuth half (authorize URL, code exchange, refresh,
// deregistration) runs in the api, which alone holds the sealed tokens. The push half (verify a delivery, split it into
// items, map one stored item to RYFT's records) runs in ingest, which never holds a token: processing a push needs
// none. Ingest's registry therefore holds PushSide values only.
//
// Invariants:
//   - It imports internal/model and the standard library, nothing else of ours: never sealed, store, config or app
//     (internal/arch_test.go). Tokens are plain strings here; the api seals them.
//   - ParseRecord is pure: no network, no token, no clock. Whatever it returns, the consumer stamps origin, vendor,
//     source and record id itself, so an adapter cannot forge another provider's rows.
//   - Direct rows carry the reserved source ryft-direct-<provider> (SourceName); a sync record with that prefix is
//     dropped at ingest (IsReservedSource).
//   - Nothing about a real provider's wire format is written here: that is each adapter's, from its programme's
//     documentation (phase 2). The fake adapter (internal/connect/fake) is the only one, for tests, compose and the smoke.
//
// See docs/ARCHITECTURE.md → Direct connections; D257.
package connect
