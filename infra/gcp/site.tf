# The site's records (C1, design 8, D233): ryftlab.de and www.ryftlab.de on Firebase Hosting in the
# project ryftlab-site. Firebase owns the site and its certificates (by hand, runbook step 10); this
# zone owns the records, so the values the console's custom-domain wizard shows go into
# site.auto.tfvars (committed and reviewed, like gates.auto.tfvars) and apply 3 adds them. Public
# values. Every variable defaults to empty, and empty adds nothing, so the plan of this root doesn't
# change until site.auto.tfvars is filled in.
#
# The apex has no other A, AAAA or CNAME record (D215), as Firebase asks. The apex `hosting-site=`
# string joins the apex TXT set (dns.tf). For www the wizard shows either A records (with their own
# `hosting-site=` TXT) or one CNAME to the site's web.app name; a CNAME can't share its name with
# anything else, so the two forms are exclusive. scripts/dns_compare.sh is runbook step 6's tool
# (before the NS switch): run after step 10, it lists these records as added, which is expected.

variable "site_apex_a" {
  description = "The A records the Firebase wizard shows for ryftlab.de (runbook step 10). Empty: no apex A record"
  type        = list(string)
  default     = []

  validation {
    condition = alltrue([
      for ip in var.site_apex_a :
      can(regex("^(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])(\\.(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])){3}$", ip))
    ]) && length(distinct(var.site_apex_a)) == length(var.site_apex_a)
    error_message = "site_apex_a holds IPv4 addresses exactly as the wizard shows them (for example 199.36.158.100), each once."
  }
}

variable "site_apex_txt" {
  description = "The TXT value the Firebase wizard shows for ryftlab.de (hosting-site=<site id>). Empty: none"
  type        = string
  default     = ""

  validation {
    condition     = var.site_apex_txt == "" || can(regex("^hosting-site=[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$", var.site_apex_txt))
    error_message = "site_apex_txt is empty or the whole value the wizard shows: hosting-site=<site id>."
  }
}

variable "site_www_a" {
  description = "The A records the Firebase wizard shows for www.ryftlab.de. Empty when it shows a CNAME (site_www_cname)"
  type        = list(string)
  default     = []

  validation {
    condition = alltrue([
      for ip in var.site_www_a :
      can(regex("^(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])(\\.(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])){3}$", ip))
    ]) && length(distinct(var.site_www_a)) == length(var.site_www_a)
    error_message = "site_www_a holds IPv4 addresses exactly as the wizard shows them, each once."
  }
}

variable "site_www_txt" {
  description = "The TXT value the Firebase wizard shows for www.ryftlab.de (hosting-site=<site id>), with A records only"
  type        = string
  default     = ""

  validation {
    condition     = var.site_www_txt == "" || can(regex("^hosting-site=[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$", var.site_www_txt))
    error_message = "site_www_txt is empty or the whole value the wizard shows: hosting-site=<site id>."
  }
}

variable "site_www_cname" {
  description = "The CNAME target the Firebase wizard shows for www.ryftlab.de (<site id>.web.app), instead of A records"
  type        = string
  default     = ""

  validation {
    condition     = var.site_www_cname == "" || can(regex("^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]\\.(web\\.app|firebaseapp\\.com)\\.?$", var.site_www_cname))
    error_message = "site_www_cname is empty or the site's own name the wizard shows: <site id>.web.app (or .firebaseapp.com)."
  }

  validation {
    condition     = var.site_www_cname == "" || (length(var.site_www_a) == 0 && var.site_www_txt == "")
    error_message = "www.ryftlab.de gets either the wizard's A records (and TXT) or its CNAME, never both: a CNAME can't share its name."
  }
}

resource "google_dns_record_set" "site_apex_a" {
  count = length(var.site_apex_a) > 0 ? 1 : 0

  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = google_dns_managed_zone.ryftlab_de.dns_name
  type         = "A"
  ttl          = 300
  rrdatas      = var.site_apex_a
}

resource "google_dns_record_set" "site_www_a" {
  count = length(var.site_www_a) > 0 ? 1 : 0

  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = "www.${google_dns_managed_zone.ryftlab_de.dns_name}"
  type         = "A"
  ttl          = 300
  rrdatas      = var.site_www_a
}

resource "google_dns_record_set" "site_www_txt" {
  count = var.site_www_txt != "" ? 1 : 0

  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = "www.${google_dns_managed_zone.ryftlab_de.dns_name}"
  type         = "TXT"
  ttl          = 300
  rrdatas      = ["\"${var.site_www_txt}\""]
}

resource "google_dns_record_set" "site_www_cname" {
  count = var.site_www_cname != "" ? 1 : 0

  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = "www.${google_dns_managed_zone.ryftlab_de.dns_name}"
  type         = "CNAME"
  ttl          = 300
  rrdatas      = ["${trimsuffix(var.site_www_cname, ".")}."]
}
