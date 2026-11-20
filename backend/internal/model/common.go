package model

// Error codes (the error body's code), closed in v1.
const (
	CodeInviteRequired  = "invite_required"
	CodeConsentRequired = "consent_required"
	// CodeImportInProgress: POST /v1/train/import while another import of the account
	// runs (429, Retry-After: 10; D129).
	CodeImportInProgress = "import_in_progress"
	// CodeImportNotEnabled: POST /v1/train/import for an account the flag train.import is off
	// for (403, S4). The app hides the import then; the server enforces it as well.
	CodeImportNotEnabled = "import_not_enabled"

	// App Attest (D245, D247; docs/API.md → Conventions → App Attest).

	// CodeAppAttestRequired: 403 under APP_ATTEST=on on every route the check covers, for a
	// request without a valid app token. The app gets a new token and sends the request again
	// once; a refused write stays queued.
	CodeAppAttestRequired = "app_attest_required"
	// CodeAppAttestChallengeInvalid: 422 on POST /v1/app-attest/keys and /token: the challenge
	// is unknown, used up, expired or another account's.
	CodeAppAttestChallengeInvalid = "app_attest_challenge_invalid"
	// CodeAppAttestInvalid: 422 on POST /v1/app-attest/keys (the attestation fails a check, a
	// development attestation included) and /token (the assertion fails a check).
	CodeAppAttestInvalid = "app_attest_invalid"
	// CodeAppAttestKeyUnknown: 404 on POST /v1/app-attest/token: key_id is not one of the
	// account's keys.
	CodeAppAttestKeyUnknown = "app_attest_key_unknown"
)
