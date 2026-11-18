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
  value = module.deploy_role.role_arn
}
