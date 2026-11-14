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

# ---- Grafana Cloud: the Grafana-managed rules on Cloud Monitoring (alerts.tf, runbook step 11) ------

variable "grafana_url" {
  description = "The Grafana Cloud stack's URL, https://<stack>.grafana.net"
  type        = string
  default     = ""

  validation {
    condition     = var.grafana_url == "" || can(regex("^https://[a-z0-9.-]+$", var.grafana_url))
    error_message = "grafana_url must be the stack's https URL without a path, or empty."
  }
}

variable "grafana_datasource_uid" {
  description = "The UID of the Google Cloud Monitoring data source made at runbook step 11. Empty: no rules, no Grafana token needed"
  type        = string
  default     = ""

  validation {
    condition     = can(regex("^[A-Za-z0-9_-]{0,40}$", var.grafana_datasource_uid))
    error_message = "grafana_datasource_uid must be a Grafana UID (letters, digits, - and _, at most 40)."
  }

  validation {
    condition     = var.grafana_datasource_uid == "" || var.grafana_url != ""
    error_message = "grafana_datasource_uid needs grafana_url as well."
  }
}

variable "grafana_auth" {
  description = "A Grafana service account token (alert rules write). Set in the workspace UI only, as sensitive; needed from step 11"
  type        = string
  default     = null
  sensitive   = true
  ephemeral   = true
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
