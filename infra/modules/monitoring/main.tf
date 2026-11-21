# Alarms for one environment (OPS-4). Every alarm notifies one SNS topic,
# when it fires and again when it clears, and the topic emails alert_email.
#
# Error tracking is CloudWatch itself (the MG-53 default; see
# docs/decisions.md): the app logs JSON lines to stdout, ECS ships them to each
# service's log group, and a metric filter counts the ERROR lines. No extra
# vendor, account or secret. Sentry could replace the error alarm later
# without touching the rest.

locals {
  alarm_actions = [aws_sns_topic.alerts.arn]
  log_namespace = "mailgoose/${var.name}"
}

resource "aws_sns_topic" "alerts" {
  name = "${var.name}-alerts"
}

resource "aws_sns_topic_subscription" "email" {
  count = var.alert_email == "" ? 0 : 1

  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

# --- Queues ---

# Sends or SES events piling up: the worker is down, stuck or too slow.
resource "aws_cloudwatch_metric_alarm" "queue_age" {
  for_each = var.queues

  alarm_name        = "${var.name}-${each.key}-queue-age"
  alarm_description = "The oldest message on the ${each.key} queue has waited over ${var.queue_age_threshold_seconds / 60} minutes. Check that the worker is running and look at its logs."

  namespace   = "AWS/SQS"
  metric_name = "ApproximateAgeOfOldestMessage"
  dimensions  = { QueueName = each.value }
  statistic   = "Maximum"
  period      = 300

  evaluation_periods  = 2
  comparison_operator = "GreaterThanThreshold"
  threshold           = var.queue_age_threshold_seconds
  treat_missing_data  = "notBreaching"

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
}

# A message failed five times and was set aside. Nothing is lost, but
# someone has to look at it and redrive it.
resource "aws_cloudwatch_metric_alarm" "dlq_depth" {
  for_each = var.dead_letter_queues

  alarm_name        = "${var.name}-${each.key}-dlq"
  alarm_description = "Messages are waiting on the ${each.key} dead-letter queue: they failed five times. Read the worker's logs for their message_id, fix the cause, then redrive them from the SQS console."

  namespace   = "AWS/SQS"
  metric_name = "ApproximateNumberOfMessagesVisible"
  dimensions  = { QueueName = each.value }
  statistic   = "Maximum"
  period      = 300

  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = 0
  treat_missing_data  = "notBreaching"

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
}

# --- API ---

# Share of requests answered with a 5xx, by the API or by the load balancer
# itself (no healthy task). Quiet periods aren't judged.
resource "aws_cloudwatch_metric_alarm" "api_5xx_rate" {
  alarm_name        = "${var.name}-api-5xx-rate"
  alarm_description = "Over ${var.api_5xx_percent_threshold}% of API requests in 5 minutes ended in a server error. Search the API's logs for level ERROR."

  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = var.api_5xx_percent_threshold
  treat_missing_data  = "notBreaching"

  metric_query {
    id          = "rate"
    label       = "5xx percent"
    expression  = "IF(requests >= ${var.api_5xx_min_requests}, 100 * (FILL(target_5xx, 0) + FILL(elb_5xx, 0)) / requests, 0)"
    return_data = true
  }

  metric_query {
    id = "requests"
    metric {
      namespace   = "AWS/ApplicationELB"
      metric_name = "RequestCount"
      dimensions  = { LoadBalancer = var.alb_arn_suffix }
      stat        = "Sum"
      period      = 300
    }
  }

  metric_query {
    id = "target_5xx"
    metric {
      namespace   = "AWS/ApplicationELB"
      metric_name = "HTTPCode_Target_5XX_Count"
      dimensions  = { LoadBalancer = var.alb_arn_suffix }
      stat        = "Sum"
      period      = 300
    }
  }

  metric_query {
    id = "elb_5xx"
    metric {
      namespace   = "AWS/ApplicationELB"
      metric_name = "HTTPCode_ELB_5XX_Count"
      dimensions  = { LoadBalancer = var.alb_arn_suffix }
      stat        = "Sum"
      period      = 300
    }
  }

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
}

# The load balancer sees no healthy API task: the site is down.
resource "aws_cloudwatch_metric_alarm" "api_healthy_tasks" {
  count = var.services["api"].expect_running ? 1 : 0

  alarm_name        = "${var.name}-api-no-healthy-tasks"
  alarm_description = "The load balancer has had no healthy API task for 5 minutes, so the API and web app are down. Check the api service's events and stopped tasks in the ECS console."

  namespace = "AWS/ApplicationELB"
  dimensions = {
    LoadBalancer = var.alb_arn_suffix
    TargetGroup  = var.api_target_group_arn_suffix
  }
  metric_name = "HealthyHostCount"
  statistic   = "Minimum"
  period      = 60

  evaluation_periods  = 5
  comparison_operator = "LessThanThreshold"
  threshold           = 1
  # No data from the load balancer means nothing is registered: down too.
  treat_missing_data = "breaching"

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
}

# --- Tasks ---

# A service that should be running has no task reporting CPU: its tasks keep
# stopping or can't start. ECS sends CPUUtilization for every service at no
# extra cost; Container Insights' RunningTaskCount would cost extra.
resource "aws_cloudwatch_metric_alarm" "service_running" {
  for_each = { for k, s in var.services : k => s if s.expect_running }

  alarm_name        = "${var.name}-${each.key}-not-running"
  alarm_description = "No ${each.key} task has reported in for 10 minutes. Check the ${each.value.service_name} service's events and stopped tasks in the ECS console."

  namespace = "AWS/ECS"
  dimensions = {
    ClusterName = var.ecs_cluster_name
    ServiceName = each.value.service_name
  }
  metric_name = "CPUUtilization"
  statistic   = "SampleCount"
  period      = 300

  evaluation_periods  = 2
  comparison_operator = "LessThanThreshold"
  threshold           = 1
  treat_missing_data  = "breaching"

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
}

# --- Errors in the logs ---

resource "aws_cloudwatch_log_metric_filter" "errors" {
  for_each = var.services

  name           = "${var.name}-${each.key}-errors"
  log_group_name = each.value.log_group_name
  # The app's JSON lines carry "level"; panics are logged at ERROR too.
  pattern = "{ $.level = \"ERROR\" }"

  metric_transformation {
    namespace     = local.log_namespace
    name          = "${each.key}-error-lines"
    value         = "1"
    default_value = "0"
  }
}

resource "aws_cloudwatch_metric_alarm" "error_logs" {
  for_each = var.services

  alarm_name        = "${var.name}-${each.key}-errors"
  alarm_description = "The ${each.key} logged ${var.error_log_threshold} or more errors in 5 minutes. In CloudWatch Logs Insights, query ${each.value.log_group_name} with: filter level = \"ERROR\" | sort @timestamp desc. Each line's request_id or message_id leads to the rest of its lines."

  namespace   = local.log_namespace
  metric_name = aws_cloudwatch_log_metric_filter.errors[each.key].metric_transformation[0].name
  statistic   = "Sum"
  period      = 300

  evaluation_periods  = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = var.error_log_threshold
  treat_missing_data  = "notBreaching"

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
}

# --- SES reputation (account-wide) ---

resource "aws_cloudwatch_metric_alarm" "ses_bounce_rate" {
  count = var.ses_reputation_alarms ? 1 : 0

  alarm_name        = "${var.name}-ses-bounce-rate"
  alarm_description = "SES's bounce rate for the AWS account is over ${format("%.4g", var.ses_bounce_rate_threshold * 100)}%. AWS reviews accounts at 5% and may pause sending at 10%. Find the campaign with the most bounces in its report, and the account sending it."

  namespace   = "AWS/SES"
  metric_name = "Reputation.BounceRate"
  statistic   = "Maximum"
  period      = 3600

  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = var.ses_bounce_rate_threshold
  treat_missing_data  = "notBreaching"

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
}

resource "aws_cloudwatch_metric_alarm" "ses_complaint_rate" {
  count = var.ses_reputation_alarms ? 1 : 0

  alarm_name        = "${var.name}-ses-complaint-rate"
  alarm_description = "SES's complaint rate for the AWS account is over ${format("%.4g", var.ses_complaint_rate_threshold * 100)}%. AWS reviews accounts at 0.1% and may pause sending at 0.5%. Find the campaign with the most complaints in its report, and the account sending it."

  namespace   = "AWS/SES"
  metric_name = "Reputation.ComplaintRate"
  statistic   = "Maximum"
  period      = 3600

  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = var.ses_complaint_rate_threshold
  treat_missing_data  = "notBreaching"

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
}

# Early warnings, under the alarms above: nothing is at risk yet, but the
# account's rates are climbing towards AWS's review line (MG-81).

resource "aws_cloudwatch_metric_alarm" "ses_bounce_rate_warning" {
  count = var.ses_reputation_alarms ? 1 : 0

  alarm_name        = "${var.name}-ses-bounce-rate-warning"
  alarm_description = "Early warning: SES's bounce rate for the AWS account is over ${format("%.4g", var.ses_bounce_rate_warning_threshold * 100)}%. The alarm level is ${format("%.4g", var.ses_bounce_rate_threshold * 100)}% and AWS reviews accounts at 5%. Check the abuse digest and recent campaign reports for the account with the most hard bounces before it gets there."

  namespace   = "AWS/SES"
  metric_name = "Reputation.BounceRate"
  statistic   = "Maximum"
  period      = 3600

  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = var.ses_bounce_rate_warning_threshold
  treat_missing_data  = "notBreaching"

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
}

resource "aws_cloudwatch_metric_alarm" "ses_complaint_rate_warning" {
  count = var.ses_reputation_alarms ? 1 : 0

  alarm_name        = "${var.name}-ses-complaint-rate-warning"
  alarm_description = "Early warning: SES's complaint rate for the AWS account is over ${format("%.4g", var.ses_complaint_rate_warning_threshold * 100)}%. The alarm level is ${format("%.4g", var.ses_complaint_rate_threshold * 100)}% and AWS reviews accounts at 0.1%. Check the abuse digest and recent campaign reports for the account with the most complaints before it gets there."

  namespace   = "AWS/SES"
  metric_name = "Reputation.ComplaintRate"
  statistic   = "Maximum"
  period      = 3600

  evaluation_periods  = 1
  comparison_operator = "GreaterThanThreshold"
  threshold           = var.ses_complaint_rate_warning_threshold
  treat_missing_data  = "notBreaching"

  alarm_actions = local.alarm_actions
  ok_actions    = local.alarm_actions
}
