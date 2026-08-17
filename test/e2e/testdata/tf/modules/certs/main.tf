# A module holding one non-importable resource, so the migration has a
# module -> Pulumi component mapping to resolve.
#
# Real migrations map Terraform modules to Pulumi components, which makes a
# component parent the common shape rather than an edge case. It changes two
# things the flat resources in the root module never exercise: the import
# entry carries a "parent", and the resulting URN's qualified type becomes
# "parentType$childType" rather than the bare resource type.
#
# aws_iot_certificate again because it is free, has no dependencies, and
# creates and destroys in under a second -- the module is here for its
# structure, not its contents.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

resource "aws_iot_certificate" "inmodule" {
  active = true
}
