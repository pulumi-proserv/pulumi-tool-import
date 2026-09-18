# Read-only fixture for TestRemoteStateTerraformCloud; the workspace already
# holds this state plus a workspace variable greeting = "from-tfc". To
# recreate: `terraform login`, init and apply here, then add the variable.

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
