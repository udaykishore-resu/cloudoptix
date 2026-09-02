output "vpc_id" {
  description = "ID of the VPC."
  value       = aws_vpc.this.id
}

output "vpc_cidr" {
  value = aws_vpc.this.cidr_block
}

output "public_subnet_ids" {
  value = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  value = aws_subnet.private[*].id
}

output "database_subnet_ids" {
  value = aws_subnet.database[*].id
}

output "availability_zones" {
  value = local.azs
}

output "nat_gateway_ids" {
  value = aws_nat_gateway.this[*].id
}

output "vpc_endpoints_security_group_id" {
  description = "Security group fronting the interface VPC endpoints; other modules' security groups should allow egress to it rather than to 0.0.0.0/0:443 when reaching AWS APIs."
  value       = var.enable_vpc_endpoints ? aws_security_group.vpc_endpoints[0].id : null
}

output "database_route_table_id" {
  value = aws_route_table.database.id
}
