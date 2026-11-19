output "alerts_topic_arn" {
  description = "Every alarm notifies this topic. Subscribe more addresses or a chat hook to it."
  value       = aws_sns_topic.alerts.arn
}
