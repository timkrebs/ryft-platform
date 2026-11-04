// Package apple talks to Sign in with Apple's REST API: it exchanges the
// authorization code the app gets at sign-in for a refresh token, and revokes
// that token when the account is deleted. It also parses the .p8 key, which
// apns reuses.
//
// It must not log the authorization code or a token.
//
// Invariant: revocation on deletion is a precondition of App Store Review
// Guideline 5.1.1(v), which is why account deletion calls it.
//
// See docs/API.md → POST /v1/me/apple-authorization; D39.
package apple
