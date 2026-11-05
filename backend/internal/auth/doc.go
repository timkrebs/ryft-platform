// Package auth owns bearer verification for /v1 (Supabase JWT, or a shared key in development), the per-user limit hook and the Supabase admin client.
//
// It must not reach storage: it imports neither store nor db, and the user row
// arrives through OnAuth, which the service supplies. The verified Principal,
// and whatever OnAuth adds, travel in the request context.
//
// Invariants:
//   - 401 means only "this token is not acceptable". Anything that stops us from
//     checking it (JWKS unreachable, a key we cannot fetch yet, the database
//     behind OnAuth) is a 503 with Retry-After. The app refreshes its token once
//     on a 401 and signs the user out if that fails, so an outage must never look
//     like a bad token (D29).
//   - The limit runs after verification and before OnAuth, so a throttled
//     request costs no database query (D36).
//   - apikey mode lets a caller act as any user (D32), so config.LoadAuth refuses
//     it without ALLOW_INSECURE_APIKEY_AUTH=true.
//   - The operator claims (app_metadata.role, aal, the newest totp entry of amr)
//     are read leniently into Principal.AppRole, AAL and MFAAt: a claim of an
//     unexpected shape reads as empty and never rejects the token, or one odd
//     shape would sign every app user out. They grant nothing here; cmd/api's
//     admin check adds the allow-list row and the freshness rule (D-A). The
//     role comes from app_metadata only, never the top-level role or user_metadata.
//   - Admin holds the Supabase secret key; only cmd/api builds one, for account
//     deletion and the operator's user view.
//   - The JWKS keyfunc library is pinned at v3.7.0 to keep the module's Go floor
//     (D30).
//
// See docs/ARCHITECTURE.md → Services (Auth and users); docs/SUPABASE.md → What
// the services check; D29, D30, D32, D36.
package auth
