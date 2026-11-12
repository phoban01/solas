#!/usr/bin/env bash
# Runs the unit tests with the race detector. The storage tests need a
# DynamoDB, a Postgres, and an etcd. When Docker is present, the script starts
# dynamodb-local and a Postgres; when etcd is present, it starts an etcd. It removes them
# on exit. SOLAS_DYNAMODB_ENDPOINT, SOLAS_POSTGRES_URL, and SOLAS_ETCD_ENDPOINT point the tests
# at stores that already run.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if [ -z "$(go list ./... 2>/dev/null)" ]; then
  echo "test: no Go packages yet"
  exit 0
fi

# shellcheck source=scripts/local-stores.sh
source "$root/scripts/local-stores.sh"
trap 'for c in "${cleanup[@]}"; do eval "$c"; done' EXIT
start_local_stores test

go test -race "$@" ${TEST_PKGS:-./...}
