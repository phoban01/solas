#!/usr/bin/env bash
# Scale test, issue #190: runs solas-bench on dynamodb-local and on etcd at
# 2, 8, and 32 members, and prints one table. Each run binds and releases
# for SCALE_DURATION (default 10s). The runs are one at a time, so the
# memory stays within the limits of CLAUDE.md.
#   SCALE_MEMBERS   member counts, default "2 8 32"
#   SCALE_CLAIMS    claims per member, default 8
#   SCALE_DURATION  length of each run, default 10s
#   SCALE_STORES    stores to run on, default "dynamodb etcd"
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

# shellcheck source=scripts/local-stores.sh
source scripts/local-stores.sh
trap 'for c in "${cleanup[@]}"; do eval "$c"; done' EXIT
start_local_stores scale >&2

bin=$(mktemp -d)/solas-bench
cleanup+=("rm -rf $(dirname "$bin")")
go build -o "$bin" ./cmd/solas-bench

header=true
for store in ${SCALE_STORES:-dynamodb etcd}; do
  case $store in
  dynamodb)
    [ -n "${SOLAS_DYNAMODB_ENDPOINT:-}" ] || continue
    url="dynamodb://solas-bench?create-table=true&endpoint=$SOLAS_DYNAMODB_ENDPOINT"
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
      --duration "${SCALE_DURATION:-10s}" --header="$header" 2> >(grep -vE '^(SDK |[IWE][0-9]{4} )' >&2)
    header=false
  done
done
