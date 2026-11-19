# The two-phase gates (docs/OPERATIONS.md -> GCP). Committed on purpose: each flip is a reviewed
# one-line PR. HCP Terraform loads *.auto.tfvars in remote runs. Never set serve or public_invokers
# as workspace variables: this file must stay the only place.

# false: apply 1 (runbook step 4). true: apply 2 (step 9): ryft-push may invoke the four services,
# api.ryftlab.de and ingest.ryftlab.de are mapped and in DNS, and the tick runs.
serve = true

# The 20 EUR stop line (OPERATIONS -> GCP -> The €20 stop line): false keeps api and ingest
# without allUsers and the tick paused, as the stop line's gcloud commands leave them.
public_invokers = true

# The edge (D242, OPERATIONS -> GCP -> 16): off, built, live, locked, one step a PR, and back the
# same way. built: the load balancer and Cloud Armor, nothing pointing at it. live: api. and ingest.
# at its address. locked: the domain mappings gone (only after the deploy PR's ingress).
edge = "built"
