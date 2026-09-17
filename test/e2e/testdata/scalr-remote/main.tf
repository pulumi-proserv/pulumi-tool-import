# Fixture for the Scalr remote-state e2e test
# (test/e2e/remote_state_test.go). The workspace already exists and holds
# state for exactly this configuration; the test only reads it, never
# applies. Everything in it is public test data: one terraform_data resource
# whose input is "hello", a workspace variable greeting = "from-scalr-
# workspace", and an environment variable env_scoped = "from-scalr-
# environment". On Scalr the TFE-compatible organization is the
# environment ID.
#
# To recreate the workspace: export TF_TOKEN_pulumi__proserv_scalr_io=<Scalr
# token> (Terraform doubles the underscore for the hyphen), then
# `terraform init` and `terraform apply` here, then create the two
# variables through the Scalr UI or API.

terraform {
  cloud {
    hostname     = "pulumi-proserv.scalr.io"
    organization = "env-v0pdr54u1htkjaue6"
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
