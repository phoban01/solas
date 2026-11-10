#!/usr/bin/env bash
# The DynamoDB table of a benchmark on AWS, docs/plan-evidence.md M20.
#   aws-bench.sh up <region>     put the cost alarm, print the storage URL;
#                                the server creates the table on first use
#   aws-bench.sh down <region>   delete the table
#   aws-bench.sh url <region>    print the storage URL
#   aws-bench.sh env             print the export lines of a session of the
#                                bench role, for eval
# The commands assume the role SOLAS_AWS_ROLE (default: the solas-bench
# role of the account that the CLI is logged in to), which may touch
# tables named solas-bench-* only. Credentials never reach the repository.
# SOLAS_ALARM_USD (default 50) is the estimated charges that raise the
# alarm; billing metrics live in us-east-1 and need "Receive Billing
# Alerts" on in the account.
set -euo pipefail

role=${SOLAS_AWS_ROLE:-}
if [ -z "$role" ]; then
  account=$(aws sts get-caller-identity --query Account --output text)
  role="arn:aws:iam::${account}:role/solas-bench"
fi

# session exports a one hour session of the bench role.
session() {
  if [ "${AWS_SESSION_TOKEN:-}" != "" ] && aws sts get-caller-identity --query Arn --output text 2>/dev/null | grep -q 'assumed-role/solas-bench/'; then
    return
  fi
  local creds
  creds=$(aws sts assume-role --role-arn "$role" --role-session-name "solas-bench-$$" --duration-seconds 3600 \
    --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)
  AWS_ACCESS_KEY_ID=$(echo "$creds" | cut -f1)
  AWS_SECRET_ACCESS_KEY=$(echo "$creds" | cut -f2)
  AWS_SESSION_TOKEN=$(echo "$creds" | cut -f3)
  export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
}

table() { echo "solas-bench-$1"; }
url() { echo "dynamodb://$(table "$1")?region=$1&create-table=true"; }

alarm() {
  aws cloudwatch put-metric-alarm --region us-east-1 --alarm-name solas-bench-cost \
    --alarm-description "solas benchmarks: estimated charges of the account" \
    --namespace AWS/Billing --metric-name EstimatedCharges --dimensions Name=Currency,Value=USD \
    --statistic Maximum --period 21600 --evaluation-periods 1 --threshold "${SOLAS_ALARM_USD:-50}" \
    --comparison-operator GreaterThanThreshold --treat-missing-data notBreaching
}

case ${1:-} in
up)
  region=${2:?usage: aws-bench.sh up <region>}
  session
  alarm
  echo "alarm solas-bench-cost at ${SOLAS_ALARM_USD:-50} USD (us-east-1)" >&2
  url "$region"
  ;;
down)
  region=${2:?usage: aws-bench.sh down <region>}
  session
  if aws dynamodb describe-table --region "$region" --table-name "$(table "$region")" >/dev/null 2>&1; then
    aws dynamodb delete-table --region "$region" --table-name "$(table "$region")" >/dev/null
    aws dynamodb wait table-not-exists --region "$region" --table-name "$(table "$region")"
    echo "deleted $(table "$region") in $region" >&2
  else
    echo "no table $(table "$region") in $region" >&2
  fi
  ;;
url)
  url "${2:?usage: aws-bench.sh url <region>}"
  ;;
env)
  session
  echo "export AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN=$AWS_SESSION_TOKEN"
  ;;
*)
  echo "usage: aws-bench.sh up|down|url <region> | env" >&2
  exit 2
  ;;
esac
