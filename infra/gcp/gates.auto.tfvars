# The two-phase gates (docs/OPERATIONS.md -> GCP). Committed on purpose: each flip is a reviewed
# one-line PR. HCP Terraform loads *.auto.tfvars in remote runs. Never set serve or public_invokers
# as workspace variables: this file must stay the only place.

# false: apply 1 (runbook step 4). true: apply 2 (step 9): ryft-push may invoke the four services,
# api.ryftlab.de and ingest.ryftlab.de are mapped and in DNS, and the tick runs.
serve = false

# The 20 EUR stop line (OPERATIONS -> GCP -> The €20 stop line): false keeps api and ingest
# without allUsers and the tick paused, as the stop line's gcloud commands leave them.
public_invokers = true
