locals {
  # Exact match unless a subject uses a wildcard, so "ref:refs/heads/main"
  # can never match another branch name.
  subject_test = anytrue([for s in var.allowed_subjects : strcontains(s, "*")]) ? "StringLike" : "StringEquals"
}

data "aws_iam_policy_document" "trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [var.oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = local.subject_test
      variable = "token.actions.githubusercontent.com:sub"
      values   = [for s in var.allowed_subjects : "repo:${var.github_repository}:${s}"]
    }
  }
}

resource "aws_iam_role" "this" {
  name                 = var.name
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = 3600
}

# Only what an image deploy needs: push to ECR, register a task definition,
# run the one-off migration task, update the services. Infra changes stay with
# a human running terraform apply.
data "aws_iam_policy_document" "deploy" {
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

resource "aws_iam_role_policy" "deploy" {
  name   = "deploy"
  role   = aws_iam_role.this.id
  policy = data.aws_iam_policy_document.deploy.json
}
