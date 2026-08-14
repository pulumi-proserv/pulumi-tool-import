terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.100"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.4"
    }
  }
}

provider "aws" {
  region = "us-west-2"
}

locals {
  name = "tool-import-e2e"
  tags = {
    Name      = local.name
    ManagedBy = "pulumi-tool-import-e2e"
    Purpose   = "issue-22-non-importable-injection"
  }
}

# The topology mirrors the v0.2.0 end-to-end run: a VPC with three route
# tables, a VPN gateway whose route propagation onto each table is a resource
# type Terraform cannot import, and a VPN connection carrying a static route
# which likewise cannot be imported.

resource "aws_vpc" "main" {
  cidr_block           = "10.42.0.0/16"
  enable_dns_hostnames = true
  tags                 = local.tags
}

resource "aws_route_table" "rt" {
  count  = 3
  vpc_id = aws_vpc.main.id
  tags   = merge(local.tags, { Name = "${local.name}-rt-${count.index}" })
}

resource "aws_vpn_gateway" "vgw" {
  vpc_id          = aws_vpc.main.id
  amazon_side_asn = 64512
  tags            = local.tags
}

# aws_vpn_gateway_route_propagation declares no importer: "pulumi import"
# fails with a misleading "resource does not exist" even though the IDs are
# correct. These are the resources the injection path must write into state.
resource "aws_vpn_gateway_route_propagation" "prop" {
  count          = 3
  vpn_gateway_id = aws_vpn_gateway.vgw.id
  route_table_id = aws_route_table.rt[count.index].id
}

resource "aws_customer_gateway" "cgw" {
  bgp_asn    = 65000
  ip_address = "203.0.113.1" # TEST-NET-3, documentation range
  type       = "ipsec.1"
  tags       = local.tags
}

# static_routes_only is required for aws_vpn_connection_route to be usable.
resource "aws_vpn_connection" "vpn" {
  vpn_gateway_id      = aws_vpn_gateway.vgw.id
  customer_gateway_id = aws_customer_gateway.cgw.id
  type                = "ipsec.1"
  static_routes_only  = true
  tags                = local.tags
}

# aws_vpn_connection_route is the second non-importable type in this fixture.
resource "aws_vpn_connection_route" "route" {
  destination_cidr_block = "10.99.0.0/16"
  vpn_connection_id      = aws_vpn_connection.vpn.id
}

# aws_iot_certificate declares no importer and is the only resource in this
# fixture whose attributes the AWS provider schema marks Sensitive:
# private_key, certificate_pem, ca_pem and public_key (see
# internal/service/iot/certificate.go in the terraform-provider-aws source).
# It has zero AWS dependencies and creates/destroys in under a second.
#
# Leaving certificate_pem and csr unset makes the provider take its
# CreateKeysAndCertificate path: AWS itself generates a fresh keypair at
# create time, so private_key exists only in Terraform state and (via
# "digest tf") Pulumi stack config -- never in this repo.
#
# ca_pem is deliberately left unset. That branch selection only checks
# certificate_pem, so AWS never reads ca_pem here -- it stays null in state,
# a Sensitive attribute with nothing to redact. This is the shape real
# operators hit: a resource with some sensitive attributes populated by AWS
# (private_key, certificate_pem, public_key) and others left unset. See
# redactSensitivePaths in pkg/module_map.go for the null-value handling this
# exercises.
resource "aws_iot_certificate" "cert" {
  active = true
}

# --- Minimal Lambda function to back the LAMBDA-type target group below. It
# is never invoked by this test; it only needs to exist and be registerable
# as a VPC Lattice target.

data "archive_file" "lambda" {
  type        = "zip"
  output_path = "${path.module}/.terraform-tmp/lambda.zip"
  source {
    content  = "exports.handler = async () => ({statusCode: 200, body: \"ok\"});"
    filename = "index.js"
  }
}

data "aws_iam_policy_document" "lambda_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda" {
  name               = "${local.name}-lambda"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
  tags               = local.tags
}

resource "aws_lambda_function" "target" {
  function_name    = "${local.name}-target"
  role             = aws_iam_role.lambda.arn
  handler          = "index.handler"
  runtime          = "nodejs20.x"
  filename         = data.archive_file.lambda.output_path
  source_code_hash = data.archive_file.lambda.output_base64sha256
  tags             = local.tags
}

# VPC Lattice must be allowed to invoke the function before it can be
# registered as a target group target.
resource "aws_lambda_permission" "vpclattice" {
  statement_id  = "AllowVPCLatticeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.target.function_name
  principal     = "vpc-lattice.amazonaws.com"
  source_arn    = aws_vpclattice_target_group.tg.arn
}

# aws_vpclattice_target_group is importable (declares Importer) and attaches
# to the fixture's existing VPC, adding no new dependency category.
#
# type = "LAMBDA" rather than "IP": an original version of this fixture used
# an IP-type target group with a made-up address ("10.42.0.10") and nothing
# actually listening on it, so the target group attachment below never
# became healthy and "tofu apply" timed out after 21 retries
# ("waiting for VPC Lattice Target Group Attachment ... create: couldn't
# find resource"). Lambda targets have no health-check state machine at all
# -- VPC Lattice considers a registered Lambda target healthy as soon as
# it is registered -- so this avoids the wait entirely while still needing
# no long-lived compute (no EC2 instance, no ALB) behind it. See the
# archive_file/aws_iam_role/aws_lambda_function/aws_lambda_permission
# resources above for the minimal function this target group points at.
# "config" is deliberately omitted: it is documented as not applicable
# (and rejected) for LAMBDA-type target groups.
resource "aws_vpclattice_target_group" "tg" {
  name = "${local.name}-tg"
  type = "LAMBDA"
  tags = local.tags
}

# aws_vpclattice_target_group_attachment declares no importer and has a real
# nested list-of-objects "target" block ({id, port}) -- the first non-flat
# shape in this fixture, which every other injected resource lacks. "port"
# is omitted: it is not applicable to a Lambda target and the provider
# rejects it if set.
resource "aws_vpclattice_target_group_attachment" "attach" {
  target_group_identifier = aws_vpclattice_target_group.tg.id
  target {
    id = aws_lambda_function.target.arn
  }
  depends_on = [aws_lambda_permission.vpclattice]
}

output "vpc_id" {
  value = aws_vpc.main.id
}

output "vpn_connection_id" {
  value = aws_vpn_connection.vpn.id
}
