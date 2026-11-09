#!/usr/bin/env bash
# Soak run: two k3d clusters, one store, a gatekeeper, and faults for
# SOAK_DURATION (default 30m): store pauses, node pauses, pod deletes,
# and claim churn. At the end a verifier replays the Device history of
# the store and reads the gatekeeper log. docs/benchmarks.md has the
# results. It takes the e2e lock, because it runs two clusters.
#   SOAK_DURATION  length of the fault loop, default 30m
#   SOAK_SEED      seed of the fault schedule, default 1
#   SOLAS_STORE    dynamodb (default) or etcd
#   SOAK_LOAD      simulated members that solas-bench runs against the
#                  same store during the faults (default 0, none)
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
# shellcheck source=scripts/k3d-lib.sh
source scripts/k3d-lib.sh
e2e_lock
export AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID:-local} AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY:-local} AWS_REGION=${AWS_REGION:-us-east-1}
bench=$(mktemp -d)/solas-bench
trap 'rm -rf "$(dirname "$bench")"' EXIT
go build -o "$bench" ./cmd/solas-bench
export SOAK_BENCH=$bench
go test -tags e2e,soak -count=1 -timeout 120m -run 'TestSoak$' -v ./test/e2e/...
