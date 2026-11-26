// Package config owns the services' environment configuration: what each variable means, its default and which combinations are refused.
//
// It is a leaf but for internal/appattest, itself a leaf, whose ParseRing reads
// APP_ATTEST_TOKEN_KEYS (so a malformed ring stops the start), and every package
// may import it. Configuration is environment only (no flags, no files), and a
// service reads it once at start-up.
//
// Invariants:
//
//   - A bad or half-set configuration fails the start instead of running
//     half-configured: a Supabase project reference that is none, apikey mode
//     without ALLOW_INSECURE_APIKEY_AUTH, some but not all of a credential set
//     (APNs, Sign in with Apple), a push identity (PUSH_AUDIENCE, PUSH_SA_EMAIL,
//     PUSH_SA_ID) missing or not of its form. A Cloud Run job (CLOUD_RUN_JOB
//     without K_SERVICE) publishes nothing and serves no push, so it may go without
//     the push identity and GCP_PROJECT; what it sets is checked all the same.
//
//   - Nothing insecure or emulated starts on Cloud Run: ALLOW_INSECURE_PUSH=true
//     and PUBSUB_EMULATOR_HOST are refused while K_SERVICE or CLOUD_RUN_JOB is set
//     (D218).
//
//   - The gate defaults are what production runs unless the config says
//     otherwise: consent on, the invite gate off (D87). api and ingest refuse to
//     start without PRIVACY_POLICY_VERSION (D78).
//
//   - App Attest (D245, D247): APP_ATTEST warn or on without a token ring, or a
//     malformed ring in any mode, stops api and ingest; a ring without
//     APPLE_TEAM_ID and APPLE_CLIENT_ID stops the api. No error holds a key.
//
//   - Direct connections (D257): CONNECT_FAKE=on, or fake in CONNECT_PROVIDERS without it, stops api and ingest, and
//     the fake is refused on Cloud Run; the api also refuses a served provider without its user cap (at least 1), its
//     OAuth client (a real provider), DATA_ENCRYPTION_KEY or an https CONNECT_CALLBACK_BASE. No error holds a secret.
//
//   - Smoke sign-in (D286): SMOKE_OIDC_SA_EMAIL (a service account address ending in .iam.gserviceaccount.com),
//     SMOKE_OIDC_SA_ID (its unique ID, 1 to 32 digits), SMOKE_OIDC_AUDIENCE (at most 256 printable characters, no
//     spaces) and SMOKE_USER_ID (the smoke account's UUID, not nil) are all set or all empty. All empty is off;
//     any other mix, a malformed one, or any of them with AUTH_MODE=apikey stops api and ingest. The errors name
//     the variables, never a value. auth then accepts that one service account's Google ID token as that one user.
//
// It is not yet the only reader of the environment: cmd/migrate reads its own
// variables, httpx.Logger reads LOG_LEVEL, K_SERVICE, CLOUD_RUN_JOB and
// GCP_PROJECT (the log format), and the Postgres test helpers read
// RYFT_TEST_POSTGRES_URL.
//
// See docs/ARCHITECTURE.md → Services (Auth and users); D78, D87, D218.
package config
