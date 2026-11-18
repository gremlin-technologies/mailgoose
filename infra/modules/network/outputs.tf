output "vpc_id" {
  value = aws_vpc.this.id
}

output "vpc_cidr_block" {
  value = aws_vpc.this.cidr_block
}

output "public_subnet_ids" {
  value = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  value = aws_subnet.private[*].id
}

# Where Fargate tasks should run, given the NAT choice.
output "app_subnet_ids" {
  value = var.nat_gateway_mode == "none" ? aws_subnet.public[*].id : aws_subnet.private[*].id
}

output "app_assign_public_ip" {
  value = var.nat_gateway_mode == "none"
}
