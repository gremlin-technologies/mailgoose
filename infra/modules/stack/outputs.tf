output "app_urls" {
  value = [for h in var.hostnames : "https://${h}"]
}

output "alb_dns_name" {
  value = module.edge.alb_dns_name
}

output "database_endpoint" {
  value = module.database.endpoint
}

output "database_secret_arn" {
  value = module.database.master_user_secret_arn
}

output "app_secret_arn" {
  description = "Put application secrets (JSON) here."
  value       = aws_secretsmanager_secret.app.arn
}

output "send_queue_url" {
  value = module.send_queue.url
}

output "send_dlq_url" {
  value = module.send_queue.dlq_url
}

output "ses_events_queue_url" {
  value = module.events_queue.url
}

output "ses_configuration_set" {
  value = module.email.configuration_set_name
}

output "ecs_cluster_name" {
  value = aws_ecs_cluster.this.name
}

output "github_deploy_role_arn" {
  value = one(module.deploy_role[*].role_arn)
}

output "codebuild_deploy_project" {
  value = one(module.codebuild_deploy[*].project_name)
}

output "deploy_slack_webhook_secret" {
  description = "Put the #deployments incoming-webhook URL in this secret (infra/README.md)."
  value       = one(module.codebuild_deploy[*].slack_webhook_secret_name)
}

output "images_bucket" {
  value = module.images.bucket_name
}

output "images_base_url" {
  description = "Where uploaded images are served from (the API's IMAGES_BASE_URL)."
  value       = module.images.base_url
}

output "alerts_topic_arn" {
  value = module.monitoring.alerts_topic_arn
}
