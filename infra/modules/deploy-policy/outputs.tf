output "json" {
  description = "IAM policy for an image deploy: push to ECR, register task definitions, run the migration task, update the services."
  value       = data.aws_iam_policy_document.this.json
}
