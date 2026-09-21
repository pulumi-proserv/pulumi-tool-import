# Read-only fixture for TestRemoteStateScalr; the workspace already holds
# this state plus a workspace variable greeting, and the environment holds
# variables greeting and env_scoped (the workspace's greeting must win). To
# recreate: export TF_TOKEN_pulumi__proserv_scalr_io=<token> (Terraform's
# TF_TOKEN_ name replaces each dot with an underscore and each hyphen with a
# double underscore), init and apply here, then add the three variables.

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
