# The site's records (site.tf, C1, runbook step 10): empty adds nothing; the wizard's values become
# exactly their record sets; www is A (+TXT) or a CNAME, never both. Plan-only, mocked, with the
# mocks of dns.tftest.hcl. rrdatas is a list, so the literals are tolist([...]).
mock_provider "google" {
  mock_data "google_logging_sink" {
    defaults = {
      disabled = true
    }
  }
}
mock_provider "google-beta" {}
mock_provider "grafana" {}

override_data {
  target = data.google_project.this
  values = {
    number = "123456789012"
  }
}

# See dns.tftest.hcl: with serve = true (gates.auto.tfvars) the mappings plan, and their status is mocked.
override_resource {
  target          = google_cloud_run_domain_mapping.host
  override_during = plan
  values = {
    status = [{
      conditions          = [{ type = "Ready", status = "True", reason = "", message = "" }]
      mapped_route_name   = ""
      observed_generation = 1
      resource_records    = [{ name = "", type = "CNAME", rrdata = "ghs.googlehosted.com." }]
    }]
  }
}

variables {
  github_repository_id       = "100000001"
  github_repository_owner_id = "200000002"
  google_site_verification   = "google-site-verification=example-only"
  site_apex_a                = []
  site_apex_txt              = ""
  site_www_a                 = []
  site_www_txt               = ""
  site_www_cname             = ""
}

run "empty_adds_nothing" {
  command = plan

  assert {
    condition = (
      length(google_dns_record_set.site_apex_a) == 0 &&
      length(google_dns_record_set.site_www_a) == 0 &&
      length(google_dns_record_set.site_www_txt) == 0 &&
      length(google_dns_record_set.site_www_cname) == 0 &&
      length(google_dns_record_set.apex_txt.rrdatas) == 3
    )
    error_message = "with the variables empty, the site adds no record and the apex TXT set is the three of dns.tf"
  }
}

run "apex_and_www_by_a_records" {
  command = plan

  variables {
    site_apex_a   = ["199.36.158.100"]
    site_apex_txt = "hosting-site=ryftlab-site"
    site_www_a    = ["199.36.158.100"]
    site_www_txt  = "hosting-site=ryftlab-site"
  }

  assert {
    condition = (
      google_dns_record_set.site_apex_a[0].name == "ryftlab.de." &&
      google_dns_record_set.site_apex_a[0].type == "A" &&
      google_dns_record_set.site_apex_a[0].ttl == 300 &&
      google_dns_record_set.site_apex_a[0].rrdatas == tolist(["199.36.158.100"])
    )
    error_message = "the apex A set holds exactly the wizard's addresses"
  }

  assert {
    condition = (
      toset(google_dns_record_set.apex_txt.rrdatas) == toset([
        "\"NETORGFT21311797.onmicrosoft.com\"",
        "\"v=spf1 include:secureserver.net -all\"",
        "\"google-site-verification=example-only\"",
        "\"hosting-site=ryftlab-site\"",
      ]) &&
      length(google_dns_record_set.apex_txt.rrdatas) == 4
    )
    error_message = "the apex hosting-site= value joins the one apex TXT set, quoted like the others"
  }

  assert {
    condition = (
      google_dns_record_set.site_www_a[0].name == "www.ryftlab.de." &&
      google_dns_record_set.site_www_a[0].type == "A" &&
      google_dns_record_set.site_www_a[0].rrdatas == tolist(["199.36.158.100"]) &&
      google_dns_record_set.site_www_txt[0].name == "www.ryftlab.de." &&
      google_dns_record_set.site_www_txt[0].type == "TXT" &&
      google_dns_record_set.site_www_txt[0].rrdatas == tolist(["\"hosting-site=ryftlab-site\""]) &&
      length(google_dns_record_set.site_www_cname) == 0
    )
    error_message = "www gets the wizard's A set and its own TXT, and no CNAME"
  }
}

run "www_by_cname" {
  command = plan

  variables {
    site_www_cname = "ryftlab-site.web.app"
  }

  assert {
    condition = (
      google_dns_record_set.site_www_cname[0].name == "www.ryftlab.de." &&
      google_dns_record_set.site_www_cname[0].type == "CNAME" &&
      google_dns_record_set.site_www_cname[0].rrdatas == tolist(["ryftlab-site.web.app."]) &&
      length(google_dns_record_set.site_www_a) == 0 &&
      length(google_dns_record_set.site_www_txt) == 0
    )
    error_message = "www's CNAME is fully qualified, and nothing else is at www"
  }
}

# The committed file sets the five site variables once each and nothing else: no gate, no ID and no
# other value can ride along in the site's DNS PR.
run "site_file_sets_only_the_site_variables" {
  command = plan

  assert {
    condition = (
      length(regexall("(?m)^[a-z_]+\\s*=", file("${path.module}/site.auto.tfvars"))) == 5 &&
      alltrue([
        for k in ["site_apex_a", "site_apex_txt", "site_www_a", "site_www_txt", "site_www_cname"] :
        length(regexall("(?m)^${k}\\s*=", file("${path.module}/site.auto.tfvars"))) == 1
      ])
    )
    error_message = "site.auto.tfvars sets site_apex_a, site_apex_txt, site_www_a, site_www_txt and site_www_cname, once each, and nothing else"
  }
}

run "rejects_a_cname_next_to_a_records" {
  command = plan

  variables {
    site_www_a     = ["199.36.158.100"]
    site_www_cname = "ryftlab-site.web.app"
  }

  expect_failures = [var.site_www_cname]
}

run "rejects_an_address_that_is_not_ipv4" {
  command = plan

  variables {
    site_apex_a = ["199.36.158.1000"]
  }

  expect_failures = [var.site_apex_a]
}

run "rejects_a_txt_value_without_its_prefix" {
  command = plan

  variables {
    site_apex_txt = "ryftlab-site"
  }

  expect_failures = [var.site_apex_txt]
}

run "rejects_a_cname_to_another_host" {
  command = plan

  variables {
    site_www_cname = "evil.example."
  }

  expect_failures = [var.site_www_cname]
}
