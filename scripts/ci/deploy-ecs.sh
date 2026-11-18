#!/usr/bin/env bash
# Deploys one image to the mailgoose ECS services of one environment.
#
# 1. Registers a new task definition revision per service (the latest revision
#    of the family, with only the "app" container's image swapped).
# 2. Runs "mailgoosed migrate up" once as a task of the new API definition.
#    A failed migration stops here, before any service changes.
# 3. Points each service at its new revision and waits for it to settle. The
#    services have a deployment circuit breaker with rollback, so a bad image
#    makes the wait fail instead of taking the service down.
#
# Needs only what infra/modules/github-deploy-role grants. Required env:
#   CLUSTER       ECS cluster name, e.g. mailgoose-staging
#   IMAGE         full image reference, e.g. <registry>/mailgoose:<tag>
#   API_SERVICE   e.g. mailgoose-staging-api (service and task family share a name)
#   WORKER_SERVICE
# Optional: STARTED_BY (<= 36 chars, shown on the migration task).
set -euo pipefail

: "${CLUSTER:?}" "${IMAGE:?}" "${API_SERVICE:?}" "${WORKER_SERVICE:?}"
STARTED_BY="${STARTED_BY:-deploy}"

# Fields register-task-definition accepts. describe-task-definition also
# returns read-only ones (revision, status, ...) that it would reject.
ALLOWED='["family","taskRoleArn","executionRoleArn","networkMode","containerDefinitions","volumes","placementConstraints","requiresCompatibilities","cpu","memory","runtimePlatform","ephemeralStorage","pidMode","ipcMode","proxyConfiguration","inferenceAccelerators"]'

register() { # family -> prints new task definition ARN
  local family="$1"
  aws ecs describe-task-definition --task-definition "$family" --query taskDefinition --output json |
    jq --arg image "$IMAGE" --argjson allowed "$ALLOWED" '
      with_entries(select(.key as $k | $allowed | index($k)))
      | .containerDefinitions |= map(if .name == "app" then .image = $image else . end)' >"$tmp/$family.json"
  aws ecs register-task-definition --cli-input-json "file://$tmp/$family.json" \
    --query taskDefinition.taskDefinitionArn --output text
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

api_td="$(register "$API_SERVICE")"
worker_td="$(register "$WORKER_SERVICE")"
echo "Registered $api_td and $worker_td"

# Migrations: same network, roles and environment as the API.
network="$(aws ecs describe-services --cluster "$CLUSTER" --services "$API_SERVICE" \
  --query 'services[0].networkConfiguration' --output json)"
task_arn="$(aws ecs run-task --cluster "$CLUSTER" --task-definition "$api_td" \
  --launch-type FARGATE --count 1 --started-by "$STARTED_BY" \
  --network-configuration "$network" \
  --overrides '{"containerOverrides":[{"name":"app","command":["migrate","up"]}]}' \
  --query 'tasks[0].taskArn' --output text)"
if [ -z "$task_arn" ] || [ "$task_arn" = "None" ]; then
  echo "Could not start the migration task" >&2
  exit 1
fi
echo "Migration task: $task_arn"
aws ecs wait tasks-stopped --cluster "$CLUSTER" --tasks "$task_arn"
result="$(aws ecs describe-tasks --cluster "$CLUSTER" --tasks "$task_arn" \
  --query 'tasks[0].containers[?name==`app`]|[0].[exitCode,reason]' --output text)"
exit_code="${result%%$'\t'*}"
if [ "$exit_code" != "0" ]; then
  echo "Migration failed (exit code/reason: $result). Deploy stopped; services unchanged." >&2
  echo "Logs: CloudWatch log group /ecs/$API_SERVICE" >&2
  exit 1
fi
echo "Migrations applied"

aws ecs update-service --cluster "$CLUSTER" --service "$API_SERVICE" --task-definition "$api_td" >/dev/null
aws ecs update-service --cluster "$CLUSTER" --service "$WORKER_SERVICE" --task-definition "$worker_td" >/dev/null
aws ecs wait services-stable --cluster "$CLUSTER" --services "$API_SERVICE" "$WORKER_SERVICE"
echo "Deployed $IMAGE to $CLUSTER"
