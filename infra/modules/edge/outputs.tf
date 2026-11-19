output "alb_security_group_id" {
  value = aws_security_group.alb.id
}

output "alb_dns_name" {
  value = aws_lb.this.dns_name
}

output "api_target_group_arn" {
  value = aws_lb_target_group.api.arn

  # ECS refuses a target group that no listener uses yet, so anything taking
  # this ARN (the API service) must wait for the HTTPS listener.
  depends_on = [aws_lb_listener.https]
}

output "https_listener_arn" {
  value = aws_lb_listener.https.arn
}

# For CloudWatch alarms on the load balancer and the API's target group.
output "alb_arn_suffix" {
  value = aws_lb.this.arn_suffix
}

output "api_target_group_arn_suffix" {
  value = aws_lb_target_group.api.arn_suffix
}
