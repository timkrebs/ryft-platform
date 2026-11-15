// Package config owns the services' environment configuration: what each variable means, its default and which combinations are refused.
//
// It is a leaf: it imports nothing internal, so every package may import it.
// Configuration is environment only (no flags, no files), and a service reads it
// once at start-up.
//
// Invariants:
//   - A bad or half-set configuration fails the start instead of running
//     half-configured: a Supabase project reference that is none, apikey mode
//     without ALLOW_INSECURE_APIKEY_AUTH, some but not all of a credential set
//     (APNs, Sign in with Apple), a push identity (PUSH_AUDIENCE, PUSH_SA_EMAIL,
//     PUSH_SA_ID) missing or not of its form. A Cloud Run job (CLOUD_RUN_JOB
//     without K_SERVICE) publishes nothing and serves no push, so it may go without
//     the push identity and GCP_PROJECT; what it sets is checked all the same.
//   - Nothing insecure or emulated starts on Cloud Run: ALLOW_INSECURE_PUSH=true
//     and PUBSUB_EMULATOR_HOST are refused while K_SERVICE or CLOUD_RUN_JOB is set
//     (D218).
//   - The gate defaults are what production runs unless the config says
//     otherwise: consent on, the invite gate off (D87). api and ingest refuse to
//     start without PRIVACY_POLICY_VERSION (D78).
//
// It is not yet the only reader of the environment: cmd/migrate reads its own
// variables, httpx.Logger reads LOG_LEVEL, K_SERVICE, CLOUD_RUN_JOB and
// GCP_PROJECT (the log format), and the Postgres test helpers read
// RYFT_TEST_POSTGRES_URL.
//
// See docs/ARCHITECTURE.md → Services (Auth and users); D78, D87, D218.
package config
