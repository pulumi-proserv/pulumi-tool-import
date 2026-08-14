terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.100"
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
# ca_pem is given a harmless, fabricated value rather than left unset: that
# branch selection only checks certificate_pem, so AWS never reads ca_pem
# here -- but Terraform Core marks a Sensitive attribute's value redacted
# based on the schema alone, independent of whether it is null, and an
# unset (null) Sensitive attribute currently makes injection hard-fail
# trying to resolve a stack config key that digest tf never had a real
# value to write (DiscoverSensitiveSecrets skips null values). Giving ca_pem
# a real, non-secret string sidesteps that unrelated edge case while still
# exercising redaction and resolution for it like the other three fields.
resource "aws_iot_certificate" "cert" {
  active = true
  ca_pem = "not a real certificate authority -- placeholder value only, see comment above"
}

# aws_vpclattice_target_group is importable (declares Importer) and attaches
# to the fixture's existing VPC, adding no new dependency category.
resource "aws_vpclattice_target_group" "tg" {
  name = "${local.name}-tg"
  type = "IP"
  config {
    port            = 80
    protocol        = "HTTP"
    vpc_identifier  = aws_vpc.main.id
    ip_address_type = "IPV4"
    health_check {
      enabled = false
    }
  }
  tags = local.tags
}

# aws_vpclattice_target_group_attachment declares no importer and has a real
# nested list-of-objects "target" block ({id, port}) -- the first non-flat
# shape in this fixture, which every other injected resource lacks. The
# target need not be a real running workload: registering an IP-type target
# only requires an address that plausibly belongs to the target group's
# VPC, and health checking is disabled above, so AWS accepts the
# registration without ever probing it.
resource "aws_vpclattice_target_group_attachment" "attach" {
  target_group_identifier = aws_vpclattice_target_group.tg.id
  target {
    id   = "10.42.0.10"
    port = 80
  }
}

output "vpc_id" {
  value = aws_vpc.main.id
}

output "vpn_connection_id" {
  value = aws_vpn_connection.vpn.id
}
