# Unit tests: every provider is mocked, nothing is created in AWS.
#   terraform init && terraform test

# The upstream modules validate ARNs and IAM policy JSON, so the mocks return
# well-formed values instead of random strings.
mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = {
      names = ["eu-central-1a", "eu-central-1b", "eu-central-1c"]
    }
  }
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
      arn        = "arn:aws:iam::123456789012:user/terraform"
    }
  }
  mock_data "aws_iam_session_context" {
    defaults = {
      issuer_arn = "arn:aws:iam::123456789012:user/terraform"
    }
  }
  mock_data "aws_partition" {
    defaults = {
      partition  = "aws"
      dns_suffix = "amazonaws.com"
    }
  }
  mock_data "aws_region" {
    defaults = {
      region = "eu-central-1"
    }
  }
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }
  mock_resource "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::123456789012:role/mock"
    }
  }
  mock_resource "aws_launch_template" {
    defaults = {
      id = "lt-0123456789abcdef0"
    }
  }
  mock_resource "aws_iam_policy" {
    defaults = {
      arn = "arn:aws:iam::123456789012:policy/mock"
    }
  }
  mock_resource "aws_kms_key" {
    defaults = {
      arn = "arn:aws:kms:eu-central-1:123456789012:key/00000000-0000-0000-0000-000000000000"
    }
  }
  mock_resource "aws_cloudwatch_log_group" {
    defaults = {
      arn = "arn:aws:logs:eu-central-1:123456789012:log-group:mock"
    }
  }
  mock_data "aws_eks_addon_version" {
    defaults = {
      version = "v1.0.0-eksbuild.1"
    }
  }
  mock_resource "aws_eks_cluster" {
    defaults = {
      arn                   = "arn:aws:eks:eu-central-1:123456789012:cluster/mock"
      certificate_authority = [{ data = "bW9jaw==" }]
      identity = [{
        oidc = [{
          issuer = "https://oidc.eks.eu-central-1.amazonaws.com/id/MOCK"
        }]
      }]
    }
  }
}

mock_provider "tls" {}
mock_provider "time" {}
mock_provider "null" {}
mock_provider "cloudinit" {}

run "names_derive_from_project_environment_and_location" {
  assert {
    condition     = output.cluster_name == "ryft-prod-euc1"
    error_message = "the cluster must be named <project>-<environment>-<location>"
  }

  assert {
    condition     = output.configure_kubectl == "aws eks update-kubeconfig --region eu-central-1 --name ryft-prod-euc1 --alias ryft-prod-euc1"
    error_message = "configure_kubectl must target the cluster in the configured region"
  }
}

run "other_environment_gets_its_own_names" {
  variables {
    environment = "dev"
  }

  assert {
    condition     = output.cluster_name == "ryft-dev-euc1"
    error_message = "environment must be part of the cluster name"
  }
}

run "location_is_part_of_every_name" {
  variables {
    location = "euw1"
  }

  assert {
    condition     = output.cluster_name == "ryft-prod-euw1"
    error_message = "location must be the third part of the cluster name"
  }
}

run "subnets_are_carved_from_the_vpc_cidr" {
  assert {
    condition     = module.network.private_subnet_cidr_blocks == tolist(["10.0.0.0/20", "10.0.16.0/20", "10.0.32.0/20"])
    error_message = "private subnets must be three /20s at the start of the VPC range"
  }

  assert {
    condition     = module.network.public_subnet_cidr_blocks == tolist(["10.0.48.0/24", "10.0.49.0/24", "10.0.50.0/24"])
    error_message = "public subnets must be three /24s after the private ones"
  }
}

run "rejects_invalid_environment" {
  command = plan

  variables {
    environment = "Prod!"
  }

  expect_failures = [var.environment]
}

run "rejects_invalid_project" {
  command = plan

  variables {
    project = "ryft-platform-x"
  }

  expect_failures = [var.project]
}

run "rejects_inconsistent_node_group_sizes" {
  command = plan

  variables {
    node_group = {
      min_size     = 3
      desired_size = 2
      max_size     = 4
    }
  }

  expect_failures = [var.node_group]
}

run "rejects_unknown_capacity_type" {
  command = plan

  variables {
    node_group = {
      capacity_type = "RESERVED"
    }
  }

  expect_failures = [var.node_group]
}

run "rejects_invalid_api_cidr" {
  command = plan

  variables {
    endpoint_public_access_cidrs = ["my-laptop"]
  }

  expect_failures = [var.endpoint_public_access_cidrs]
}

run "rejects_a_documentation_api_cidr" {
  command = plan

  variables {
    endpoint_public_access_cidrs = ["198.51.100.7/32", "203.0.113.10/32"]
  }

  expect_failures = [var.endpoint_public_access_cidrs]
}

run "budget_alarm_only_with_a_recipient" {
  command = plan

  assert {
    condition     = length(aws_budgets_budget.monthly) == 0
    error_message = "no budget without a recipient"
  }
}

run "budget_alarm_at_80_percent_and_forecast" {
  command = plan

  variables {
    budget_alert_emails = ["ops@example.com"]
  }
  assert {
    condition = (
      aws_budgets_budget.monthly[0].limit_amount == "165" && aws_budgets_budget.monthly[0].limit_unit == "USD" &&
      length(aws_budgets_budget.monthly[0].notification) == 2
    )
    error_message = "monthly budget, alerts at 80 % actual and 100 % forecast"
  }
}


# EKS authentication mode (docs/OPERATIONS.md -> EKS authentication mode API). The switch to
# API is one-way, so the default in the repository must stay the current mode and only an
# explicit -var may flip it.
run "authentication_mode_defaults_to_the_current_mode" {
  assert {
    condition     = output.authentication_mode == "API_AND_CONFIG_MAP"
    error_message = "merging must not switch the cluster to API; that is an explicit -var after the pre-checks"
  }

  assert {
    condition     = toset(module.eks.access_entry_keys) == toset(["cluster_creator"])
    error_message = "only the apply identity is a cluster admin unless admin_principal_arns says otherwise"
  }

  assert {
    condition     = module.eks.node_access_entry_principal == null
    error_message = "EKS owns the node role's access entry unless node_access_entry is turned on"
  }
}

run "authentication_mode_api_is_accepted" {
  variables {
    authentication_mode = "API"
  }

  assert {
    condition     = output.authentication_mode == "API"
    error_message = "authentication_mode API must reach the module"
  }
}

run "rejects_config_map_authentication_mode" {
  command = plan

  variables {
    authentication_mode = "CONFIG_MAP"
  }

  expect_failures = [var.authentication_mode]
}

run "node_role_entry_is_opt_in" {
  variables {
    node_access_entry = true
  }

  assert {
    condition     = module.eks.node_access_entry_principal == output.node_role_arn
    error_message = "the opt-in entry must be for the managed node group's role"
  }
}

run "extra_admin_gets_a_cluster_admin_entry" {
  variables {
    admin_principal_arns = {
      sso = "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/eu-central-1/AWSReservedSSO_Admin_0123456789abcdef"
    }
  }

  assert {
    condition     = toset(module.eks.access_entry_keys) == toset(["admin-sso", "cluster_creator"])
    error_message = "an extra admin must get its own access entry next to the cluster creator"
  }
}

run "rejects_an_admin_that_is_not_an_iam_arn" {
  command = plan

  variables {
    admin_principal_arns = { me = "tim" }
  }

  expect_failures = [var.admin_principal_arns]
}
