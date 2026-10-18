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

