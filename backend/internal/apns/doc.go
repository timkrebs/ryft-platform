// Package apns sends one APNs request per device token with token-based
// authentication: an ES256 JWT from the team's .p8 key over HTTP/2 (net/http
// negotiates it).
//
// It must not decide what to send or to whom (notify does) and uses no
// certificates. Result says whether a token is gone for good so the caller can
// delete it; this package keeps no token state.
//
// See D73.
package apns
