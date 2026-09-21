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
