package model

import "time"

// ---- App Attest (D245, D247; docs/API.md → Conventions → App Attest) ---------------

// HeaderAppToken carries the app token of POST /v1/app-attest/token on every request to
// api and ingest. Opaque to the app; at most 512 ASCII characters.
const HeaderAppToken = "X-RYFT-App-Token" //nolint:gosec // a header's name, not a credential

// HeaderAppBuild is the app's CFBundleVersion (AA1): optional, digits and dots, at most 24
// characters. A missing or malformed value is ignored, never refused; the server writes it
// only into the App Attest warn line, never into a metric label.
const HeaderAppBuild = "X-RYFT-App-Build"

// AppAttestChallenge is the answer of POST /v1/app-attest/challenge: an opaque, single-use
// challenge for this account, valid 5 minutes.
type AppAttestChallenge struct {
	Challenge string    `json:"challenge"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AppAttestKeyPost is the body of POST /v1/app-attest/keys. KeyID is standard base64 of 32
// bytes (44 characters), Attestation standard base64 of the CBOR attestation object.
type AppAttestKeyPost struct {
	KeyID       string `json:"key_id"`
	Attestation string `json:"attestation"`
	Challenge   string `json:"challenge"`
}

// AppAttestKey is the 201 of POST /v1/app-attest/keys: the attested key's id, echoed.
type AppAttestKey struct {
	KeyID string `json:"key_id"`
}

// AppAttestTokenPost is the body of POST /v1/app-attest/token. Assertion is standard base64
// of the CBOR assertion made over SHA-256 of Challenge's UTF-8 bytes.
type AppAttestTokenPost struct {
	KeyID     string `json:"key_id"`
	Challenge string `json:"challenge"`
	Assertion string `json:"assertion"`
}

// AppAttestToken is the 200 of POST /v1/app-attest/token. TTLSec is 3600 today and never
// below 600 (AA3); the app renews with under min(300, TTLSec/2) seconds left.
type AppAttestToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	TTLSec    int       `json:"ttl_sec"`
}
