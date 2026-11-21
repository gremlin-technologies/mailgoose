# Non-secret settings.
domain = "mailgoose.app"

# mailgoose.app's DNS stays at GoDaddy (it carries live Microsoft 365 email and
# the website). Only staging.mailgoose.app is delegated to Route 53, through NS
# records at GoDaddy. See infra/README.md, "DNS".
hosted_zone_name = "staging.mailgoose.app"

# The AWS project's managed service control policy denies
# iam:CreateOpenIDConnectProvider (seen 2026-11-17), so no GitHub OIDC deploys
# yet. See docs/decisions.md.
create_github_oidc_provider = false

# CodeBuild deploys instead (docs/decisions.md, 2026-11-21). The first apply
# that creates the connection runs with -var github_connection_ready=false;
# see infra/README.md, "Turning on CI deploys".
create_github_connection = true
github_connection_ready  = true
