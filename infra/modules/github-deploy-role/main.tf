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

# The permissions live in ../deploy-policy so the CodeBuild deploy role
# (../codebuild-deploy) grants exactly the same.
module "policy" {
  source = "../deploy-policy"

  ecr_repository_arns      = var.ecr_repository_arns
  ecs_service_arns         = var.ecs_service_arns
  pass_role_arns           = var.pass_role_arns
  run_task_cluster_arn     = var.run_task_cluster_arn
  run_task_definition_arns = var.run_task_definition_arns
}

resource "aws_iam_role_policy" "deploy" {
  name   = "deploy"
  role   = aws_iam_role.this.id
  policy = module.policy.json
}
