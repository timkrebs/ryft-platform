# PR D2 (the retirement): the root declares nothing, so a plan against ryft-prod-euc1-platform destroys
# everything its state holds (docs/OPERATIONS.md → GCP 13.2). Plan-only; the AWS provider is mocked.
#   terraform init -backend=false && terraform test
mock_provider "aws" {}

run "the_root_declares_nothing" {
  command = plan

  assert {
    condition     = fileset(path.module, "*.tf") == toset(["providers.tf", "terraform.tf"])
    error_message = "infra/aws holds only terraform.tf and providers.tf (PR D2): another .tf file could put something back into the destroy's plan"
  }

  assert {
    condition = alltrue([for f in fileset(path.module, "*.tf") :
      !can(regex("(?m)^[ \\t]*(resource|data|module|ephemeral|import|moved|removed|check|output|locals)\\b", file("${path.module}/${f}")))
    ])
    error_message = "no resource, data, module, ephemeral, import, moved, removed, check, output or locals block in infra/aws: the root declares nothing"
  }

  assert {
    condition     = length(fileset(path.module, "modules/**")) == 0 && length(fileset(path.module, "*.tf.json")) == 0
    error_message = "no modules/ and no .tf.json file in infra/aws"
  }
}

# The workspace whose state the destroy empties: tim-krebs-org's ryft-prod-euc1-platform, by name, and no other.
run "the_destroy_goes_to_the_old_workspace" {
  command = plan

  assert {
    condition = (
      length(regexall("(?m)^[ \\t]*organization[ \\t]*=[ \\t]*\"tim-krebs-org\"[ \\t]*$", file("${path.module}/terraform.tf"))) == 1 &&
      length(regexall("(?m)^[ \\t]*name[ \\t]*=[ \\t]*\"ryft-prod-euc1-platform\"[ \\t]*$", file("${path.module}/terraform.tf"))) == 1 &&
      length(regexall("(?m)^[ \\t]*(tags|project)[ \\t]*=", file("${path.module}/terraform.tf"))) == 0
    )
    error_message = "terraform.tf's cloud block names tim-krebs-org and the workspace ryft-prod-euc1-platform by name (no tags, no project): the destroy must not reach another workspace"
  }
}
