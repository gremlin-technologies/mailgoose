output "identity_arn" {
  value = aws_sesv2_email_identity.this.arn
}

output "configuration_set_name" {
  value = aws_sesv2_configuration_set.this.configuration_set_name
}

output "configuration_set_arn" {
  value = aws_sesv2_configuration_set.this.arn
}

output "events_topic_arn" {
  value = aws_sns_topic.events.arn
}

output "shared_identity_arn" {
  description = "The shared sending domain's SES identity, or null when there is none."
  value       = local.shared ? aws_sesv2_email_identity.shared[0].arn : null
}
