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
