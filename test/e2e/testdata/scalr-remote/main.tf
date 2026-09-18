# Read-only fixture for TestRemoteStateScalr; the workspace already holds
# this state plus a workspace variable greeting and an environment variable
# env_scoped. To recreate: export TF_TOKEN_pulumi__proserv_scalr_io=<token>
# (Terraform doubles the underscore for the hyphen), init and apply here,
# then add both variables.

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
