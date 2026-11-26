# D286: ryft-smoke is the subject of the production smoke's ID token, minted by CI (ryft-deployer) and,
# optionally, by operators (var.smoke_token_creators). It holds no role anywhere, has no key (the no-key
# guard is tests/guards.tftest.hcl), and its minters hold the ID-token role only. Plan-only, mocked.
mock_provider "google" {
  mock_data "google_logging_sink" {
    defaults = {
      disabled = true
    }
  }
}
mock_provider "google-beta" {}

override_data {
  target = data.google_project.this
  values = {
    number = "123456789012"
  }
}

# Cloud Run fills in a domain mapping's status only at apply (see tests/iam.tftest.hcl).
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
  edge                       = "off"
}

run "the_account_and_its_outputs" {
  command = plan

  assert {
    condition = (
      google_service_account.this["ryft-smoke"].account_id == "ryft-smoke" &&
      google_service_account.this["ryft-smoke"].project == "ryftlab-prod" &&
      google_service_account.this["ryft-smoke"].display_name == "Production smoke identity: api and ingest map its ID tokens to the smoke account (D286)" &&
      output.smoke_sa_email == "ryft-smoke@ryftlab-prod.iam.gserviceaccount.com"
    )
    error_message = "ryft-smoke exists in ryftlab-prod and its email output is ryft-smoke@ryftlab-prod.iam.gserviceaccount.com (the contract's SMOKE_OIDC_SA_EMAIL)"
  }

  # The unique ID is known only after the apply (a mocked plan leaves it unknown), so the output's wiring
  # is read from the file: the account's own unique_id, not sensitive (SMOKE_SA_ID in config.env is read
  # with terraform output -raw smoke_sa_unique_id).
  assert {
    condition = (
      can(regex("output \"smoke_sa_unique_id\" \\{[^}]*value\\s*=\\s*google_service_account\\.this\\[\"ryft-smoke\"\\]\\.unique_id", file("${path.module}/outputs.tf"))) &&
      !can(regex("output \"smoke_sa_unique_id\" \\{[^}]*sensitive", file("${path.module}/outputs.tf")))
    )
    error_message = "output smoke_sa_unique_id is ryft-smoke's unique_id and is not sensitive"
  }
}

run "no_role_anywhere" {
  command = plan

  # On the plan: ryft-smoke is the member of none of the bindings a careless edit would reach: the
  # project, the secrets, actAs, and the account bindings of the pool and Pub/Sub.
  assert {
    condition = alltrue(concat(
      [for b in google_project_iam_member.deployer_run_developer : !strcontains(b.member, "ryft-smoke")],
      [for k, b in google_secret_manager_secret_iam_binding.accessor : !anytrue([for m in b.members : strcontains(m, "ryft-smoke")])],
      [for k, b in google_service_account_iam_member.deployer_act_as : k != "ryft-smoke" && !strcontains(b.member, "ryft-smoke")],
      [for b in [google_service_account_iam_member.pubsub_agent_token_creator, google_service_account_iam_member.deployer_workload_identity] : !strcontains(b.member, "ryft-smoke")],
    ))
    error_message = "ryft-smoke is no project member, no secret accessor, no actAs target and no member of the account bindings"
  }

  # In the files, narrowly: no `member`/`members` argument line (a # comment or a description may name
  # the account) reaches ryft-smoke, and the service-account IAM resources are the five this test knows
  # (a binding or policy resource would carry members the plan assertions above don't see).
  assert {
    condition = (
      length(regexall("(?m)^[^#\\n]*\\bmembers?\\s*=[^\\n]*ryft-smoke", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0 &&
      length(regexall("(?m)^[^#\\n]*member\\[\"ryft-smoke\"\\]", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0 &&
      toset([for match in regexall("(?m)^\\s*resource\\s+\"(google_service_account_iam_[a-z]+)\"\\s+\"([^\"]+)\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")])) : "${match[0]}.${match[1]}"]) == toset([
        "google_service_account_iam_member.pubsub_agent_token_creator",
        "google_service_account_iam_member.deployer_act_as",
        "google_service_account_iam_member.deployer_workload_identity",
        "google_service_account_iam_member.smoke_deployer_id_token",
        "google_service_account_iam_member.smoke_operator_id_token",
      ])
    )
    error_message = "no member/members line names ryft-smoke, and the service-account IAM resources are the five this test knows"
  }

  # No project-level member resource besides the two tests/iam.tftest.hcl pins, so none can carry it.
  assert {
    condition     = toset([for match in regexall("(?m)^\\s*resource\\s+\"google_project_iam_member\"\\s+\"([^\"]+)\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")])) : match[0]]) == toset(["deployer_run_developer", "nutrition_cloud_sql"])
    error_message = "the project-level members are the deployer bootstrap and the Nutrition Cloud SQL client, nothing for ryft-smoke"
  }
}

run "the_deployer_mints_id_tokens_only" {
  command = plan

  assert {
    condition = (
      google_service_account_iam_member.smoke_deployer_id_token.service_account_id == "projects/ryftlab-prod/serviceAccounts/ryft-smoke@ryftlab-prod.iam.gserviceaccount.com" &&
      google_service_account_iam_member.smoke_deployer_id_token.role == "roles/iam.serviceAccountOpenIdTokenCreator" &&
      google_service_account_iam_member.smoke_deployer_id_token.member == "serviceAccount:ryft-deployer@ryftlab-prod.iam.gserviceaccount.com"
    )
    error_message = "the deployer holds roles/iam.serviceAccountOpenIdTokenCreator on ryft-smoke"
  }

  # On the plan: every binding on ryft-smoke, whatever resource it comes from, carries the OpenID role
  # and nothing else (serviceAccountTokenCreator would add access tokens, signBlob and signJwt); and
  # serviceAccountTokenCreator stays Pub/Sub's one binding, on ryft-push.
  assert {
    condition = alltrue([
      for b in [
        google_service_account_iam_member.pubsub_agent_token_creator,
        google_service_account_iam_member.deployer_workload_identity,
        google_service_account_iam_member.smoke_deployer_id_token,
      ] :
      endswith(b.service_account_id, "/ryft-smoke@ryftlab-prod.iam.gserviceaccount.com") ? b.role == "roles/iam.serviceAccountOpenIdTokenCreator" : true
    ])
    error_message = "every binding on ryft-smoke is roles/iam.serviceAccountOpenIdTokenCreator"
  }

  assert {
    condition = (
      google_service_account_iam_member.pubsub_agent_token_creator.role == "roles/iam.serviceAccountTokenCreator" &&
      endswith(google_service_account_iam_member.pubsub_agent_token_creator.service_account_id, "/ryft-push@ryftlab-prod.iam.gserviceaccount.com") &&
      alltrue([
        for b in concat(
          [google_service_account_iam_member.deployer_workload_identity, google_service_account_iam_member.smoke_deployer_id_token],
          values(google_service_account_iam_member.deployer_act_as),
          values(google_service_account_iam_member.smoke_operator_id_token),
        ) : b.role != "roles/iam.serviceAccountTokenCreator"
      ])
    )
    error_message = "roles/iam.serviceAccountTokenCreator is Pub/Sub's binding on ryft-push and no other"
  }

  # No runtime-account actAs reaches ryft-smoke (it is not in local.runtime).
  assert {
    condition     = alltrue([for b in google_service_account_iam_member.deployer_act_as : !endswith(b.service_account_id, "/ryft-smoke@ryftlab-prod.iam.gserviceaccount.com")])
    error_message = "the deployer may not act as ryft-smoke"
  }

  # In the files, narrowly: the OpenID role is the value of a `role =` line exactly twice (the deployer's
  # and the operators' bindings), and the broad role of exactly one (comments and descriptions that name
  # the roles don't count).
  assert {
    condition = (
      length(regexall("(?m)^\\s*role\\s*=\\s*\"roles/iam\\.serviceAccountOpenIdTokenCreator\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 2 &&
      length(regexall("(?m)^\\s*role\\s*=\\s*\"roles/iam\\.serviceAccountTokenCreator\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1
    )
    error_message = "role lines: serviceAccountOpenIdTokenCreator twice (deployer, operators), serviceAccountTokenCreator once (Pub/Sub)"
  }

  assert {
    condition     = length(google_service_account_iam_member.smoke_operator_id_token) == 0
    error_message = "with smoke_token_creators unset (default []) only CI mints: no operator binding"
  }
}

run "operators_get_the_openid_role_only" {
  command = plan

  variables {
    smoke_token_creators = ["user:operator@example.com", "group:ops@example.com"]
  }

  assert {
    condition = (
      toset(keys(google_service_account_iam_member.smoke_operator_id_token)) == toset(["user:operator@example.com", "group:ops@example.com"]) &&
      alltrue([
        for k, b in google_service_account_iam_member.smoke_operator_id_token :
        b.member == k &&
        b.role == "roles/iam.serviceAccountOpenIdTokenCreator" &&
        b.service_account_id == "projects/ryftlab-prod/serviceAccounts/ryft-smoke@ryftlab-prod.iam.gserviceaccount.com"
      ])
    )
    error_message = "each smoke_token_creators member holds roles/iam.serviceAccountOpenIdTokenCreator on ryft-smoke and nothing else"
  }

  assert {
    condition     = google_service_account_iam_member.smoke_deployer_id_token.member == "serviceAccount:ryft-deployer@ryftlab-prod.iam.gserviceaccount.com"
    error_message = "the deployer's binding does not depend on the variable"
  }
}

run "rejects_a_service_account" {
  command = plan

  variables {
    smoke_token_creators = ["serviceAccount:ryft-deployer@ryftlab-prod.iam.gserviceaccount.com"]
  }

  expect_failures = [var.smoke_token_creators]
}

run "rejects_public_members" {
  command = plan

  variables {
    smoke_token_creators = ["allAuthenticatedUsers", "allUsers"]
  }

  expect_failures = [var.smoke_token_creators]
}

run "rejects_a_domain" {
  command = plan

  variables {
    smoke_token_creators = ["domain:example.com"]
  }

  expect_failures = [var.smoke_token_creators]
}

run "rejects_a_name_without_an_address" {
  command = plan

  variables {
    smoke_token_creators = ["user:operator"]
  }

  expect_failures = [var.smoke_token_creators]
}

run "rejects_a_member_without_its_prefix" {
  command = plan

  variables {
    smoke_token_creators = ["operator@example.com"]
  }

  expect_failures = [var.smoke_token_creators]
}

run "rejects_a_duplicate" {
  command = plan

  variables {
    smoke_token_creators = ["user:operator@example.com", "user:operator@example.com"]
  }

  expect_failures = [var.smoke_token_creators]
}
