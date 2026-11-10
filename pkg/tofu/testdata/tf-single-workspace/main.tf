terraform {
  backend "remote" {
    hostname     = "__TEST_HOST__"
    organization = "__TEST_ORG__"
    workspaces {
      name = "__TEST_WORKSPACE__"
    }
  }
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
  }
}

resource "random_string" "example" {
  length = 16
}
