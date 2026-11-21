output "project_name" {
  value = aws_codebuild_project.this.name
}

output "role_arn" {
  value = aws_iam_role.this.arn
}

output "log_group" {
  value = aws_cloudwatch_log_group.this.name
}
