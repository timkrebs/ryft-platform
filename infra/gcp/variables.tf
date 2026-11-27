variable "project_id" {
  description = "The GCP project (D216): no organisation, billing on"
  type        = string
  default     = "ryftlab-prod"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.project_id))
    error_message = "project_id must be a GCP project ID (6 to 30 characters: lowercase letters, digits, hyphens)."
  }
}

variable "region" {
  description = "Everything regional (D217). A Cloud Run domain mapping exists only in some regions, europe-west1 among them"
  type        = string
  default     = "europe-west1"

  validation {
    # The regions with Cloud Run domain mappings (design section 1); without one, api.ryftlab.de needs a load balancer.
    condition = contains([
      "asia-east1", "asia-northeast1", "asia-southeast1", "europe-north1", "europe-west1", "europe-west4",
      "us-central1", "us-east1", "us-east4", "us-west1",
    ], var.region)
    error_message = "region must be one with Cloud Run domain mappings (europe-west1 is the decision, D217)."
  }
}

# The GitHub IDs the WIF condition pins (wif.tf). IDs, not names: a renamed or re-created repository
# or owner gets new ones. Read them with
#   gh api repos/timkrebs/ryft-platform --jq '.id, .owner.id'
# and set them as workspace variables (runbook step 2e). No default on purpose.
variable "github_repository_id" {
  description = "The numeric ID of timkrebs/ryft-platform (gh api repos/timkrebs/ryft-platform --jq .id)"
  type        = string

  validation {
    condition     = can(regex("^[1-9][0-9]*$", var.github_repository_id))
    error_message = "github_repository_id must be the repository's numeric ID (digits only)."
  }
}

variable "github_repository_owner_id" {
  description = "The numeric ID of the repository's owner (gh api repos/timkrebs/ryft-platform --jq .owner.id)"
  type        = string

  validation {
    condition     = can(regex("^[1-9][0-9]*$", var.github_repository_owner_id))
    error_message = "github_repository_owner_id must be the owner's numeric ID (digits only)."
  }
}

variable "google_site_verification" {
  description = "Search Console's TXT value for the domain property ryftlab.de (runbook step 1; public, not a secret)"
  type        = string

  validation {
    condition     = can(regex("^google-site-verification=[A-Za-z0-9_-]+$", var.google_site_verification))
    error_message = "google_site_verification must be the whole TXT value Search Console shows: google-site-verification=<token>."
  }
}

# ---- the two-phase gates: set in gates.auto.tfvars (reviewed), never as workspace variables -------

variable "serve" {
  description = "false: apply 1. true: apply 2, ryft-push invokes the four services, api. and ingest. are mapped and in DNS, the tick runs"
  type        = bool
  default     = false
}

variable "public_invokers" {
  description = "The 20 EUR stop line (D226): false keeps api and ingest without allUsers and the tick paused"
  type        = bool
  default     = true
}

# The edge (D242, edge.tf, runbook step 16), one phase a PR, in this order and back the same way:
#   off     nothing of the edge exists (the APIs, the sink, the dashboard and DNS as before D242);
#   built   the load balancer with Cloud Armor, its certificates, the sink's exclusions, the metrics and
#           tiles; nothing points at it, so DNS still names the domain mappings;
#   live    api. and ingest. are A records at the edge's address; the mappings stay as the way back;
#   locked  the two domain mappings are gone (only after deploy/run's ingress is the LB's alone).
variable "edge" {
  description = "The edge (D242): off, built (the load balancer and Cloud Armor, nothing pointing at it), live (api. and ingest. at its address), locked (the domain mappings gone)"
  type        = string
  default     = "off"

  validation {
    condition     = contains(["off", "built", "live", "locked"], var.edge)
    error_message = "edge must be off, built, live or locked (one step a PR, runbook GCP step 16)."
  }

  # The backends are the Cloud Run services api and ingest, which exist only from runbook step 8, and
  # the hosts only from apply 2.
  validation {
    condition     = var.edge == "off" || var.serve
    error_message = "edge must stay off while serve is false: its backends are api and ingest, and its hosts exist only from apply 2 (runbook GCP step 9)."
  }
}

# ---- Cloud Monitoring's alerting (alerts.tf, runbook step 11): the gate -----------------------------

# The Google Cloud mobile app's notification channel, by its full resource name: the device can only
# be picked in the console, so the user attaches it to one policy by hand and reads the name there
# (runbook step 11.1). An id, not a display name: a lookup by name found the email channel (D237).
# A workspace variable of ryft-gcp only (category terraform), never in a committed file: it is per
# device. Not sensitive: an opaque id that grants nothing, so the plan shows it and count needs no
# nonsensitive(). Null (the default) is unset: no alert policy exists and no channel is read
# (local.alerting). An empty string is refused, not taken for unset.
variable "alert_app_channel" {
  description = "The Google Cloud app's notification channel, projects/<project>/notificationChannels/<number> (runbook step 11.1): a workspace variable only. Null: no alert policy, no channel read"
  type        = string
  default     = null

  validation {
    condition     = var.alert_app_channel == null || can(regex("^projects/[a-z0-9-]+/notificationChannels/[0-9]+$", var.alert_app_channel))
    error_message = "alert_app_channel must be the channel's full resource name, projects/<project>/notificationChannels/<number> (the entry of the policy's notificationChannels that isn't the email channel's, runbook GCP 11.1), not a display name. Leave it unset, not empty, to keep alerting off."
  }
}

# ---- the production smoke's token minters (iam.tf, D286) --------------------------------------------

# Who, besides ryft-deployer, may mint an ID token of ryft-smoke for `make smoke-prod`: IAM members of
# people and groups only. A service account is refused on purpose (CI's is ryft-deployer, bound in
# iam.tf), and so are allUsers and allAuthenticatedUsers: a token of ryft-smoke acts as the smoke
# account in api and ingest. Operators' addresses are personal: a workspace variable of ryft-gcp
# (HCL on), never in a committed file. Empty (the default) leaves minting to CI alone.
variable "smoke_token_creators" {
  description = "IAM members (user:<email>, group:<email>) that may mint ryft-smoke's ID token with roles/iam.serviceAccountOpenIdTokenCreator. A workspace variable only"
  type        = list(string)
  default     = []

  validation {
    condition = alltrue([
      for m in var.smoke_token_creators : can(regex("^(user|group):[^@:[:space:]]+@[^@:[:space:]]+\\.[^@:[:space:]]+$", m))
    ])
    error_message = "smoke_token_creators takes IAM members written user:<email> or group:<email>, nothing else (no serviceAccount:, domain:, allUsers or allAuthenticatedUsers)."
  }

  validation {
    condition     = length(distinct(var.smoke_token_creators)) == length(var.smoke_token_creators)
    error_message = "smoke_token_creators names each member once."
  }
}

# ---- the Pub/Sub topology: pubsub.auto.tfvars.json, written by cmd/pubsub-topology (design 2.1) ------

variable "pubsub_topics" {
  description = "Topic => the services that may publish to it. The service agents' bindings are added here, not in the file"
  type = map(object({
    publishers = list(string)
  }))

  validation {
    condition     = alltrue([for t in keys(var.pubsub_topics) : can(regex("^ryft-[a-z0-9-]+$", t))])
    error_message = "every topic is named ryft-<name>."
  }

  validation {
    condition = alltrue(flatten([
      for t in values(var.pubsub_topics) : [for p in t.publishers : contains(["api", "ingest", "scoring", "notify"], p)]
    ]))
    error_message = "a publisher must be one of the services api, ingest, scoring, notify."
  }

  validation {
    condition     = alltrue([for t in values(var.pubsub_topics) : length(distinct(t.publishers)) == length(t.publishers)])
    error_message = "a topic names each publisher once."
  }

  validation {
    condition     = contains(keys(var.pubsub_topics), "ryft-tick") && try(length(var.pubsub_topics["ryft-tick"].publishers), 1) == 0
    error_message = "ryft-tick exists, and only the Cloud Scheduler service agent publishes to it (no service)."
  }
}

variable "pubsub_push_subscriptions" {
  description = "Push subscription => topic, the service and path it pushes to, ordering, ack deadline, retention, dead-letter topic (\"\" for none)"
  type = map(object({
    topic                     = string
    service                   = string
    path                      = string
    ordered                   = bool
    ack_deadline_seconds      = number
    message_retention_seconds = number
    dead_letter_topic         = string
  }))

  validation {
    condition     = alltrue([for k in keys(var.pubsub_push_subscriptions) : can(regex("^[a-z][a-z0-9-]{2,62}$", k))])
    error_message = "a subscription name is lowercase letters, digits and hyphens."
  }

  validation {
    condition     = alltrue([for s in values(var.pubsub_push_subscriptions) : contains(["api", "ingest", "scoring", "notify"], s.service)])
    error_message = "a push subscription's service must be api, ingest, scoring or notify."
  }

  validation {
    condition = alltrue([
      for s in values(var.pubsub_push_subscriptions) :
      s.topic == "ryft-tick" ? s.path == "/internal/tick" : can(regex("^/push/[a-z][a-z.]*[a-z]$", s.path))
    ])
    error_message = "a tick subscription pushes to /internal/tick, an event subscription to /push/<key>."
  }

  validation {
    condition     = alltrue([for s in values(var.pubsub_push_subscriptions) : contains(keys(var.pubsub_topics), s.topic)])
    error_message = "a push subscription's topic must be in pubsub_topics."
  }

  validation {
    condition = alltrue([
      for s in values(var.pubsub_push_subscriptions) :
      s.dead_letter_topic == "" || (contains(keys(var.pubsub_topics), s.dead_letter_topic) && s.dead_letter_topic != s.topic)
    ])
    error_message = "a dead-letter topic must be in pubsub_topics and differ from the subscription's own topic."
  }

  validation {
    condition     = alltrue([for s in values(var.pubsub_push_subscriptions) : s.topic == "ryft-tick" || s.dead_letter_topic != ""])
    error_message = "every event subscription has a dead-letter topic (only ticks are dropped on failure)."
  }

  validation {
    # A DLQ topic is read by its hold subscription only, never pushed anywhere.
    condition = length(setintersection(
      toset([for s in values(var.pubsub_push_subscriptions) : s.topic]),
      toset([for s in values(var.pubsub_push_subscriptions) : s.dead_letter_topic]),
    )) == 0
    error_message = "a dead-letter topic must not be the topic of a push subscription."
  }

  validation {
    condition = alltrue([
      for s in values(var.pubsub_push_subscriptions) :
      s.ack_deadline_seconds >= 10 && s.ack_deadline_seconds <= 600 && s.message_retention_seconds >= 600 && s.message_retention_seconds <= 2678400
    ])
    error_message = "ack_deadline_seconds must be 10 to 600, message_retention_seconds 600 to 2678400 (Pub/Sub's limits)."
  }
}

variable "pubsub_hold_subscriptions" {
  description = "Pull subscription on a dead-letter topic => topic, retention (the operator reads it with scripts/dlq.sh)"
  type = map(object({
    topic                     = string
    message_retention_seconds = number
  }))

  validation {
    condition     = length(setintersection(toset(keys(var.pubsub_hold_subscriptions)), toset(keys(var.pubsub_push_subscriptions)))) == 0
    error_message = "a hold subscription's name must not be a push subscription's."
  }

  validation {
    condition = alltrue([
      for h in values(var.pubsub_hold_subscriptions) :
      contains([for s in values(var.pubsub_push_subscriptions) : s.dead_letter_topic], h.topic)
    ])
    error_message = "a hold subscription must read a dead-letter topic (one a push subscription names)."
  }

  validation {
    # A message published to a topic without a subscription is gone: every DLQ needs its hold.
    condition = alltrue([
      for s in values(var.pubsub_push_subscriptions) :
      s.dead_letter_topic == "" || contains([for h in values(var.pubsub_hold_subscriptions) : h.topic], s.dead_letter_topic)
    ])
    error_message = "every dead-letter topic needs a hold subscription, or its letters are lost."
  }

  validation {
    condition = alltrue([
      for h in values(var.pubsub_hold_subscriptions) : h.message_retention_seconds >= 600 && h.message_retention_seconds <= 2678400
    ])
    error_message = "message_retention_seconds must be 600 to 2678400 (Pub/Sub's limits)."
  }
}
