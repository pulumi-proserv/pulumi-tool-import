# Fixture for the Terraform Cloud remote-state e2e test
# (test/e2e/remote_state_test.go). The workspace already exists and holds
# state for exactly this configuration; the test only reads it, never
# applies. Everything in the workspace is public test data: one
# terraform_data resource whose input is "hello", and one workspace
# variable, greeting = "from-tfc".
#
# To recreate the workspace: `terraform login`, then `terraform init` and
# `terraform apply` in this directory, then add the `greeting` variable in
# the workspace settings.

terraform {
  cloud {
    hostname     = "app.terraform.io"
    organization = "import-tool-test"
    workspaces {
      name = "tool-import-e2e"
    }
  }
}

variable "greeting" {
  type    = string
  default = "hello"
}

resource "terraform_data" "fixture" {
  input = var.greeting
}

output "greeting" {
  value = terraform_data.fixture.output
}
