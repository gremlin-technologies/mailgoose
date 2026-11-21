# Only what an image deploy needs: push to ECR, register a task definition,
# run the one-off migration task, update the services. Infra changes stay with
# a human running terraform apply.
data "aws_iam_policy_document" "this" {
  statement {
    sid       = "EcrLogin"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid = "EcrPush"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:CompleteLayerUpload",
      "ecr:DescribeImages",
      "ecr:GetDownloadUrlForLayer",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
    ]
    resources = var.ecr_repository_arns
  }

  statement {
    sid       = "TaskDefinitions" # these actions do not support resource-level scoping
    actions   = ["ecs:DescribeTaskDefinition", "ecs:RegisterTaskDefinition"]
    resources = ["*"]
  }

  statement {
    sid       = "UpdateServices"
    actions   = ["ecs:DescribeServices", "ecs:UpdateService"]
    resources = var.ecs_service_arns
  }

  # One-off "migrate up" task, run with the new API task definition before the
  # services switch over. Limited to this cluster and this task definition family.
  dynamic "statement" {
    for_each = var.run_task_cluster_arn == null ? [] : [1]

    content {
      sid       = "RunMigrationTask"
      actions   = ["ecs:RunTask"]
      resources = var.run_task_definition_arns

      condition {
        test     = "ArnEquals"
        variable = "ecs:cluster"
        values   = [var.run_task_cluster_arn]
      }
    }
  }

  dynamic "statement" {
    for_each = var.run_task_cluster_arn == null ? [] : [1]

    content {
      sid       = "WatchMigrationTask"
      actions   = ["ecs:DescribeTasks"]
      resources = ["*"] # task ARNs are not known ahead of time; scoped by cluster below

      condition {
        test     = "ArnEquals"
        variable = "ecs:cluster"
        values   = [var.run_task_cluster_arn]
      }
    }
  }

  statement {
    sid       = "PassTaskRoles"
    actions   = ["iam:PassRole"]
    resources = var.pass_role_arns

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }
}
