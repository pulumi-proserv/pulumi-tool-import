# Read-only fixture for TestRemoteStatePulumiCloud; the workspace already
# holds this state as the stack team-ce/toolimport/e2e. To recreate: export
# TF_TOKEN_tf_pulumi_com=<token for team-ce>, then init and apply here.

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
