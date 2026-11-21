# Deploys an environment on every push to one branch (main), from inside the
# AWS account: a CodeBuild project, triggered by a GitHub webhook, builds the
# image, pushes it to ECR and runs scripts/ci/deploy-ecs.sh.
#
# Why CodeBuild and not GitHub Actions with OIDC: the AWS project's managed
# service control policy denies iam:CreateOpenIDConnectProvider on both the
# Free and Paid plans (docs/decisions.md, 2026-11-21). CodeBuild needs no OIDC
# provider and no AWS keys anywhere: it runs as its own IAM role, and reaches
# GitHub through a CodeConnections connection (a GitHub App) made in infra/global.
#
# The webhook fires only on pushes to the branch, so the code that runs with
# this role is always code that was merged there. Pull requests, including
# ones from forks of this public repository, never start a build.

data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

locals {
  account     = data.aws_caller_identity.current.account_id
  region      = data.aws_region.current.region
  project_arn = "arn:aws:codebuild:${local.region}:${local.account}:project/${var.name}"
  log_group   = "/codebuild/${var.name}"
}

resource "aws_cloudwatch_log_group" "this" {
  name              = local.log_group
  retention_in_days = var.log_retention_days
}

data "aws_iam_policy_document" "trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["codebuild.amazonaws.com"]
    }

    # Only this project may use the role (confused-deputy guard).
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account]
    }

    condition {
      test     = "ArnEquals"
      variable = "aws:SourceArn"
      values   = [local.project_arn]
    }
  }
}

resource "aws_iam_role" "this" {
  name               = "${var.name}-codebuild"
  assume_role_policy = data.aws_iam_policy_document.trust.json
}

# The same deploy permissions the GitHub Actions role would get.
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

# What CodeBuild itself needs to run the build: write its log, and use the
# GitHub connection to fetch the source and post the build status.
data "aws_iam_policy_document" "build" {
  statement {
    sid       = "BuildLogs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.this.arn}:*"]
  }

  statement {
    sid = "GitHubConnection"
    actions = [
      "codeconnections:GetConnection",
      "codeconnections:GetConnectionToken",
      "codeconnections:UseConnection",
      "codestar-connections:GetConnection",
      "codestar-connections:GetConnectionToken",
      "codestar-connections:UseConnection",
    ]
    resources = [var.github_connection_arn]
  }
}

resource "aws_iam_role_policy" "build" {
  name   = "build"
  role   = aws_iam_role.this.id
  policy = data.aws_iam_policy_document.build.json
}

resource "aws_codebuild_project" "this" {
  name          = var.name
  description   = "Builds and deploys ${var.branch} of ${var.github_repository} to ${var.cluster_name}."
  service_role  = aws_iam_role.this.arn
  build_timeout = 30 # minutes
  # Enough for one deploy plus the next merge's; scripts/ci/codebuild-deploy.sh
  # skips a build whose commit is no longer the branch's head.
  concurrent_build_limit = 2

  source {
    type                = "GITHUB"
    location            = "https://github.com/${var.github_repository}.git"
    buildspec           = "scripts/ci/buildspec-deploy.yml"
    git_clone_depth     = 1
    report_build_status = true # shows the deploy as a check on the commit in GitHub

    auth {
      type     = "CODECONNECTIONS"
      resource = var.github_connection_arn
    }
  }

  source_version = var.branch

  artifacts {
    type = "NO_ARTIFACTS"
  }

  cache {
    type  = "LOCAL"
    modes = ["LOCAL_DOCKER_LAYER_CACHE"]
  }

  environment {
    # Fargate runs ARM64, so build on ARM: no emulation.
    type            = "ARM_CONTAINER"
    compute_type    = "BUILD_GENERAL1_SMALL"
    image           = var.build_image
    privileged_mode = true # needed to run Docker in the build

    environment_variable {
      name  = "ECR_REPO"
      value = var.ecr_repository_name
    }
    environment_variable {
      name  = "CLUSTER"
      value = var.cluster_name
    }
    environment_variable {
      name  = "API_SERVICE"
      value = var.api_service_name
    }
    environment_variable {
      name  = "WORKER_SERVICE"
      value = var.worker_service_name
    }
    environment_variable {
      name  = "READYZ_URL"
      value = var.readyz_url
    }
    environment_variable {
      name  = "GITHUB_REPOSITORY"
      value = var.github_repository
    }
    environment_variable {
      name  = "DEPLOY_BRANCH"
      value = var.branch
    }
  }

  logs_config {
    cloudwatch_logs {
      group_name = aws_cloudwatch_log_group.this.name
    }
  }

  depends_on = [aws_iam_role_policy.build]
}

resource "aws_codebuild_webhook" "this" {
  project_name = aws_codebuild_project.this.name
  build_type   = "BUILD"

  # A push to the branch, and nothing else: no pull requests, no other branches.
  filter_group {
    filter {
      type    = "EVENT"
      pattern = "PUSH"
    }

    filter {
      type    = "HEAD_REF"
      pattern = "^refs/heads/${var.branch}$"
    }
  }
}
