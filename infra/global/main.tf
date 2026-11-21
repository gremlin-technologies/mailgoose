# Account-wide pieces shared by every environment.

resource "aws_route53_zone" "main" {
  count = var.create_hosted_zone ? 1 : 0
  name  = var.hosted_zone_name != "" ? var.hosted_zone_name : var.domain
}

# Lets GitHub Actions assume AWS roles with short-lived OIDC tokens, so no
# long-lived AWS keys live in GitHub. One provider per account; the per-env
# deploy roles (modules/github-deploy-role) trust it.
resource "aws_iam_openid_connect_provider" "github" {
  count = var.create_github_oidc_provider ? 1 : 0

  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

# Connection to GitHub for CodeBuild deploys (modules/codebuild-deploy), used
# instead of the OIDC provider above while the AWS project denies that.
# Terraform creates it PENDING; someone signed in to AWS finishes it once in
# the console (Developer Tools > Settings > Connections > Update pending
# connection), which installs the "AWS Connector for GitHub" app on the
# GitHub organization. See infra/README.md, "Turning on CI deploys".
resource "aws_codestarconnections_connection" "github" {
  count = var.create_github_connection ? 1 : 0

  name          = "${var.project}-github"
  provider_type = "GitHub"
}

# Makes that connection CodeBuild's credential for GitHub in this account and
# Region, so projects can clone, take webhooks and post build status. Turn on
# only after the connection shows Available.
resource "aws_codebuild_source_credential" "github" {
  count = var.create_github_connection && var.github_connection_ready ? 1 : 0

  auth_type   = "CODECONNECTIONS"
  server_type = "GITHUB"
  token       = aws_codestarconnections_connection.github[0].arn
}

resource "aws_ecr_repository" "this" {
  for_each = toset(var.ecr_repositories)

  name                 = each.value
  image_tag_mutability = "IMMUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }
}

resource "aws_ecr_lifecycle_policy" "this" {
  for_each   = aws_ecr_repository.this
  repository = each.value.name

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Keep the last 50 images"
        selection = {
          tagStatus   = "any"
          countType   = "imageCountMoreThan"
          countNumber = 50
        }
        action = { type = "expire" }
      }
    ]
  })
}
