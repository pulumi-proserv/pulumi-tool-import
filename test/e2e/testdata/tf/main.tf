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

output "vpc_id" {
  value = aws_vpc.main.id
}

output "vpn_connection_id" {
  value = aws_vpn_connection.vpn.id
}
