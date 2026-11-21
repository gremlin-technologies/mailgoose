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

still_head || exit 0

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

still_head || exit 0

export IMAGE="$image"
export STARTED_BY="codebuild-${tag}"
export EXPECT_COMMIT="$sha"
exec scripts/ci/deploy-ecs.sh
