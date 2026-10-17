#!/usr/bin/env bash
# Print the last N CloudWatch log lines of a SageMaker training job.
# Usage: logs.sh <job-name> [lines]
set -euo pipefail
job=$1; n=${2:-80}
P=(--region "${AWS_REGION:-us-east-1}")
[ -n "${AWS_PROFILE:-}" ] && P+=(--profile "$AWS_PROFILE")
stream=$(aws logs describe-log-streams "${P[@]}" --log-group-name /aws/sagemaker/TrainingJobs \
  --log-stream-name-prefix "$job/" --query 'logStreams[0].logStreamName' --output text)
[ "$stream" = "None" ] && { echo "no log stream yet"; exit 0; }
aws logs get-log-events "${P[@]}" --log-group-name /aws/sagemaker/TrainingJobs --log-stream-name "$stream" \
  --no-start-from-head --limit "$n" --query 'events[].message' --output text | tr '\t' '\n'
