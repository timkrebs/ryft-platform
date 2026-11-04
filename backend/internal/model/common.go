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
)
