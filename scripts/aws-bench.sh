#!/usr/bin/env bash
# The DynamoDB table of a benchmark on AWS, docs/plan-evidence.md M20.
#   aws-bench.sh up <region>     put the cost alarm, print the storage URL;
#                                the server creates the table on first use
#   aws-bench.sh down <region>   delete the table
#   aws-bench.sh url <region>    print the storage URL
#   aws-bench.sh env             print the export lines of a session of the
#                                bench role, for eval
#   aws-bench.sh ec2 up <region>     start one instance in the region
#                                (SOLAS_EC2_TYPE, default c6i.2xlarge)
#                                with the bench binary and an etcd
#   aws-bench.sh ec2 run <region> <solas-bench flags>
#                                run the bench on the instance against
#                                the table; print its rows
#   aws-bench.sh ec2 scale <region>  the series of #243 on the instance
#   aws-bench.sh ec2 etcd <region>   print the etcd endpoint on the
#                                instance, for SOLAS_ETCD_ENDPOINT
#   aws-bench.sh ec2 down <region>   terminate the instance, delete its
#                                key and its security group
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


# The EC2 commands use the identity the CLI is logged in to, since the
# bench role may touch tables only. The bench on the instance gets a
# session of the bench role over ssh, and nothing writes it down.
# The state of an instance is under .cache/aws-bench/<region>.
ec2_state() { echo ".cache/aws-bench/$1"; }

ec2_ssh() {
  local st
  st=$(ec2_state "$1")
  shift
  ssh -q -i "$st/key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=10 "ec2-user@$(cat "$st/ip")" "$@"
}

ec2_up() {
  local region=$1 st tag ami myip sg id ip ev
  st=$(ec2_state "$region")
  if [ -e "$st/id" ]; then
    echo "ec2: $(cat "$st/id") is up at $(cat "$st/ip")" >&2
    return
  fi
  mkdir -p "$st"
  tag=$(cat "$st/tag" 2>/dev/null || echo "solas-bench-$(date +%s)")
  ami=$(aws ssm get-parameter --region "$region" \
    --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 \
    --query Parameter.Value --output text)
  myip=$(curl -s https://checkip.amazonaws.com)
  if [ ! -e "$st/key" ]; then
    aws ec2 create-key-pair --region "$region" --key-name "$tag" --key-type ed25519 \
      --query KeyMaterial --output text >"$st/key"
    chmod 600 "$st/key"
    echo "$tag" >"$st/tag"
  fi
  # The group admits this host only, on ssh and on the etcd client port.
  if [ ! -e "$st/sg" ]; then
    sg=$(aws ec2 create-security-group --region "$region" --group-name "$tag" \
      --description "solas bench; aws-bench.sh ec2 down deletes it" --query GroupId --output text)
    echo "$sg" >"$st/sg"
    aws ec2 authorize-security-group-ingress --region "$region" --group-id "$sg" --ip-permissions \
      "IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=$myip/32}]" \
      "IpProtocol=tcp,FromPort=2379,ToPort=2379,IpRanges=[{CidrIp=$myip/32}]" >/dev/null
  fi
  sg=$(cat "$st/sg")
  id=$(aws ec2 run-instances --region "$region" --image-id "$ami" \
    --instance-type "${SOLAS_EC2_TYPE:-c6i.2xlarge}" --key-name "$tag" --security-group-ids "$sg" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$tag}]" \
    --query 'Instances[0].InstanceId' --output text)
  echo "$id" >"$st/id"
  aws ec2 wait instance-running --region "$region" --instance-ids "$id"
  ip=$(aws ec2 describe-instances --region "$region" --instance-ids "$id" \
    --query 'Reservations[0].Instances[0].PublicIpAddress' --output text)
  echo "$ip" >"$st/ip"
  echo "ec2: $id (${SOLAS_EC2_TYPE:-c6i.2xlarge}) at $ip" >&2
  for _ in $(seq 36); do
    ec2_ssh "$region" true 2>/dev/null && break
    sleep 5
  done
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$st/solas-bench" ./cmd/solas-bench
  scp -q -i "$st/key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    "$st/solas-bench" "ec2-user@$ip:solas-bench"
  # An etcd with fsync on, the version of the devbox profile, for the
  # rows at the same round trip as the table (#295).
  ev=$(etcd --version 2>/dev/null | sed -n 's/^etcd Version: //p')
  ev=${SOLAS_ETCD_VERSION:-${ev:-3.6.14}}
  ec2_ssh "$region" "curl -sL https://github.com/etcd-io/etcd/releases/download/v$ev/etcd-v$ev-linux-amd64.tar.gz \
    | tar xz --strip-components=1 etcd-v$ev-linux-amd64/etcd \
    && (nohup ./etcd --data-dir etcd-data --log-level error \
      --listen-client-urls http://0.0.0.0:2379 --advertise-client-urls http://$ip:2379 >etcd.log 2>&1 &) \
    && sleep 2 && curl -sf http://127.0.0.1:2379/health >/dev/null"
  echo "ec2: etcd $ev at http://$ip:2379, for SOLAS_ETCD_ENDPOINT" >&2
}

# ec2_run <region> <solas-bench flags...> runs the bench on the instance
# against the table of the region and prints its rows.
ec2_run() {
  local region=$1 rtt
  shift
  session
  rtt=$(ec2_ssh "$region" "curl -s -o /dev/null -w '%{time_connect}' https://dynamodb.$region.amazonaws.com/")
  echo "ec2: DynamoDB in $region, TCP connect ${rtt}s from the instance" >&2
  printf '%s %s %s\n' "$AWS_ACCESS_KEY_ID" "$AWS_SECRET_ACCESS_KEY" "$AWS_SESSION_TOKEN" \
    | ec2_ssh "$region" "read -r k s t; AWS_ACCESS_KEY_ID=\$k AWS_SECRET_ACCESS_KEY=\$s AWS_SESSION_TOKEN=\$t \
      AWS_REGION=$region ./solas-bench --storage-url '$(url "$region")' $(printf '%q ' "$@") \
      2> >(grep -vE '^(SDK |[IWE][0-9]{4} )' >&2)"
}

ec2_down() {
  local region=$1 st id
  st=$(ec2_state "$region")
  if [ ! -e "$st/tag" ]; then
    echo "ec2: no instance in $region" >&2
    return
  fi
  id=$(cat "$st/id" 2>/dev/null || true)
  if [ -n "$id" ]; then
    aws ec2 terminate-instances --region "$region" --instance-ids "$id" >/dev/null
    aws ec2 wait instance-terminated --region "$region" --instance-ids "$id"
  fi
  for _ in $(seq 12); do
    aws ec2 delete-security-group --region "$region" --group-id "$(cat "$st/sg")" 2>/dev/null && break
    sleep 5
  done
  aws ec2 delete-key-pair --region "$region" --key-name "$(cat "$st/tag")"
  rm -rf "$st"
  echo "ec2: terminated $id and deleted its group and key" >&2
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
ec2)
  region=${3:?usage: aws-bench.sh ec2 up|run|scale|etcd|down <region> [flags]}
  case ${2:-} in
  up) ec2_up "$region" ;;
  run)
    shift 3
    ec2_run "$region" "$@"
    ;;
  scale)
    # The series of #243: 32 and 128 members, the default pool and
    # 10,000 devices, 20 seconds each, one row per run.
    header=true
    for devices in 0 10000; do
      for members in 32 128; do
        ec2_run "$region" --members "$members" --claims 8 --duration 20s \
          --devices "$devices" --header="$header"
        header=false
      done
    done
    ;;
  etcd) echo "http://$(cat "$(ec2_state "$region")/ip"):2379" ;;
  down) ec2_down "$region" ;;
  *)
    echo "usage: aws-bench.sh ec2 up|run|scale|etcd|down <region> [flags]" >&2
    exit 2
    ;;
  esac
  ;;
env)
  session
  echo "export AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN=$AWS_SESSION_TOKEN"
  ;;
*)
  echo "usage: aws-bench.sh up|down|url <region> | env | ec2 up|run|scale|etcd|down <region>" >&2
  exit 2
  ;;
esac
