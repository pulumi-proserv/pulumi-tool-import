# Fixture for the Pulumi Cloud Terraform-backend remote-state e2e test
# (test/e2e/remote_state_test.go). The workspace already exists and holds
# state for exactly this configuration; the test only reads it, never
# applies. Everything in it is public test data: one terraform_data
# resource whose input is "hello". Pulumi Cloud stores the workspace as the
# stack team-ce/toolimport/e2e, which is why the workspace name must be
# project_stack.
#
# To recreate the workspace: export TF_TOKEN_tf_pulumi_com=<Pulumi access
# token for team-ce>, then `terraform init` and `terraform apply` here.

terraform {
  cloud {
    hostname     = "tf.pulumi.com"
    organization = "team-ce"
    workspaces {
      name = "toolimport_e2e"
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
