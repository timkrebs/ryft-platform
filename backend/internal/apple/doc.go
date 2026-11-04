// Package apple talks to Sign in with Apple's REST API: it exchanges the
// authorization code the app gets at sign-in for a refresh token, and revokes
// that token when the account is deleted. It also parses the .p8 key, which
// apns reuses.
//
// It must not log the authorization code or a token.
//
// Invariant: Apple requires revocation on deletion (App Store Review Guideline
// 5.1.1(v)); DELETE /v1/me revokes a stored token after erasing the data. A
// failed revoke never blocks the deletion; the reconciler retries (D48).
//
// See docs/API.md → POST /v1/me/apple-authorization; D48.
package apple
