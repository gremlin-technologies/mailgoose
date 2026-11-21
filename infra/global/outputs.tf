output "hosted_zone_id" {
  value = one(aws_route53_zone.main[*].zone_id)
}

output "hosted_zone_name_servers" {
  description = "Point the domain's registrar at these if the zone was created here."
  value       = one(aws_route53_zone.main[*].name_servers)
}

output "github_oidc_provider_arn" {
  value = one(aws_iam_openid_connect_provider.github[*].arn)
}

output "ecr_repository_urls" {
  value = { for k, r in aws_ecr_repository.this : k => r.repository_url }
}

output "github_connection_arn" {
  description = "Pass to envs/*/ as github_connection_arn to turn on CodeBuild deploys."
  value       = one(aws_codestarconnections_connection.github[*].arn)
}

output "github_connection_status" {
  value = one(aws_codestarconnections_connection.github[*].connection_status)
}
