#!/usr/bin/env bash
# Runs in CodeBuild (infra/modules/codebuild-deploy) on every push to the
# deploy branch: builds the image for the pushed commit, pushes it to ECR and
# hands over to deploy-ecs.sh, which migrates, switches the services and
# checks /readyz. AWS credentials are the CodeBuild project's role; nothing
# here holds keys.
#
# Two merges close together start two builds. Each one checks that its commit
# is still the branch's head before it touches the services, and stops quietly
# if not, so an older build can't roll staging back over a newer one.
#
# When SLACK_WEBHOOK_SECRET_ID names a Secrets Manager secret holding a Slack
# incoming-webhook URL, the result (deployed or failed) is posted there. A
# build that stops because main moved on posts nothing. If the secret has no
# value yet, or Slack doesn't answer, the deploy's result is unchanged.
set -euo pipefail

: "${ECR_REPO:?}" "${CLUSTER:?}" "${API_SERVICE:?}" "${WORKER_SERVICE:?}"
: "${GITHUB_REPOSITORY:?}" "${DEPLOY_BRANCH:?}" "${CODEBUILD_RESOLVED_SOURCE_VERSION:?}"

sha="$CODEBUILD_RESOLVED_SOURCE_VERSION"
tag="${sha::12}"
region="${AWS_REGION:-${AWS_DEFAULT_REGION:?}}"

still_head() {
  local head
  head="$(git ls-remote "https://github.com/${GITHUB_REPOSITORY}.git" "refs/heads/${DEPLOY_BRANCH}" | cut -f1)"
  if [ "$head" != "$sha" ]; then
    echo "${DEPLOY_BRANCH} has moved on to ${head:-?}; skipping ${sha} so the newer build deploys."
    return 1
  fi
}

# notify <deployed|failed>: one Slack post, best effort.
notify() {
  local result="$1" url subject author emoji text
  [ -n "${SLACK_WEBHOOK_SECRET_ID:-}" ] || return 0
  if ! url="$(aws secretsmanager get-secret-value --secret-id "$SLACK_WEBHOOK_SECRET_ID" \
    --query SecretString --output text 2>/dev/null)" || [[ "$url" != https://hooks.slack.com/* ]]; then
    echo "No Slack webhook URL in $SLACK_WEBHOOK_SECRET_ID yet; not posting."
    return 0
  fi
  subject="$(git log -1 --format=%s "$sha" 2>/dev/null || echo "?")"
  author="$(git log -1 --format=%an "$sha" 2>/dev/null || echo "?")"
  if [ "$result" = deployed ]; then emoji=":white_check_mark:"; else emoji=":x:"; fi
  text="${emoji} ${DEPLOY_ENVIRONMENT:-$CLUSTER} ${result}: <https://github.com/${GITHUB_REPOSITORY}/commit/${sha}|${tag::7}> ${subject} (${author}). <${CODEBUILD_BUILD_URL:-}|Build log>"
  jq -n --arg text "$text" '{text: $text}' |
    curl -fsS --max-time 10 -X POST -H 'Content-Type: application/json' --data @- "$url" >/dev/null ||
    echo "Slack post failed; the deploy result stands." >&2
}

still_head || exit 0

# From here on, every exit posts its result.
trap 'status=$?; if [ "$status" = 0 ]; then notify deployed; else notify failed; fi; exit "$status"' EXIT

account="$(aws sts get-caller-identity --query Account --output text)"
registry="${account}.dkr.ecr.${region}.amazonaws.com"
image="${registry}/${ECR_REPO}:${tag}"

# Tags are immutable, so a retried build of the same commit must not push again.
if aws ecr describe-images --repository-name "$ECR_REPO" --image-ids "imageTag=${tag}" >/dev/null 2>&1; then
  echo "Image ${image} is already in ECR"
else
  aws ecr get-login-password --region "$region" | docker login --username AWS --password-stdin "$registry"
  # Built on an ARM64 machine, for ARM64 Fargate: no emulation, one platform.
  DOCKER_BUILDKIT=1 docker build \
    --build-arg VERSION="$tag" --build-arg COMMIT="$sha" \
    --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -t "$image" .
  docker push "$image"
fi

if ! still_head; then
  trap - EXIT
  exit 0
fi

export IMAGE="$image"
export STARTED_BY="codebuild-${tag}"
export EXPECT_COMMIT="$sha"
scripts/ci/deploy-ecs.sh
