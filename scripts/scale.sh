#!/usr/bin/env bash
# Scale test, issue #190: runs solas-bench on dynamodb-local and on etcd at
# 2, 8, and 32 members, and prints one table. Each run binds and releases
# for SCALE_DURATION (default 10s). The runs are one at a time, so the
# memory stays within the limits of CLAUDE.md.
#   SCALE_MEMBERS   member counts, default "2 8 32"
#   SCALE_CLAIMS    claims per member, default 8
#   SCALE_DURATION  length of each run, default 10s
#   SCALE_STORES    stores to run on, default "dynamodb etcd postgres"
#   SCALE_POLL      watch poll interval of the DynamoDB store, default 200ms
#   SCALE_DEVICES   devices in the pool, default members * claims
#   SOLAS_DYNAMODB_URL  a storage URL of a real table, from
#                   scripts/aws-bench.sh url <region>; then no dynamodb-local
#                   starts, and the script prints the round trip to the region
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

# shellcheck source=scripts/local-stores.sh
source scripts/local-stores.sh
trap 'for c in "${cleanup[@]}"; do eval "$c"; done' EXIT
if [ -n "${SOLAS_DYNAMODB_URL:-}" ]; then
  # A real table: no local DynamoDB, and the bench role's session.
  export SOLAS_DYNAMODB_ENDPOINT=none
  eval "$(scripts/aws-bench.sh env)"
  region=$(echo "$SOLAS_DYNAMODB_URL" | sed -n 's/.*region=\([a-z0-9-]*\).*/\1/p')
  rtt=$(curl -s -o /dev/null -w '%{time_connect}' "https://dynamodb.$region.amazonaws.com/" || echo unknown)
  echo "scale: DynamoDB in $region, TCP connect ${rtt}s from this host" >&2
fi
start_local_stores scale >&2

bin=$(mktemp -d)/solas-bench
cleanup+=("rm -rf $(dirname "$bin")")
go build -o "$bin" ./cmd/solas-bench

header=true
for store in ${SCALE_STORES:-dynamodb etcd postgres}; do
  case $store in
  dynamodb)
    [ -n "${SOLAS_DYNAMODB_ENDPOINT:-}" ] || continue
    url=${SOLAS_DYNAMODB_URL:-"dynamodb://solas-bench?create-table=true&endpoint=$SOLAS_DYNAMODB_ENDPOINT"}
    ;;
  postgres)
    [ -n "${SOLAS_POSTGRES_URL:-}" ] || continue
    url=$SOLAS_POSTGRES_URL
    ;;
  etcd)
    [ -n "${SOLAS_ETCD_ENDPOINT:-}" ] || continue
    url="etcd://${SOLAS_ETCD_ENDPOINT#http://}"
    ;;
  *)
    echo "scale: unknown store $store" >&2
    exit 2
    ;;
  esac
  for members in ${SCALE_MEMBERS:-2 8 32}; do
    AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID:-local} AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY:-local} \
      AWS_REGION=${AWS_REGION:-us-east-1} \
      "$bin" --storage-url "$url" --members "$members" --claims "${SCALE_CLAIMS:-8}" \
      --duration "${SCALE_DURATION:-10s}" --devices "${SCALE_DEVICES:-0}" --poll-interval "${SCALE_POLL:-200ms}" --header="$header" 2> >(grep -vE '^(SDK |[IWE][0-9]{4} )' >&2)
    header=false
  done
done
