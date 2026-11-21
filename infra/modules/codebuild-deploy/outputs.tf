output "project_name" {
  value = aws_codebuild_project.this.name
}

output "role_arn" {
  value = aws_iam_role.this.arn
}

output "log_group" {
  value = aws_cloudwatch_log_group.this.name
}

output "slack_webhook_secret_name" {
  value = one(aws_secretsmanager_secret.slack_webhook[*].name)
}
