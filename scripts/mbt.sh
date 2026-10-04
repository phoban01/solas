#!/usr/bin/env bash
# Model-based tests: generate traces of quint/solas.qnt with a fixed seed,
# then replay each one against the real REST stores on dynamodb-local.
# The traces of step include faults. The traces of calmStep have none, so
# they reach preemption.
#   MBT_COUNT  traces to generate of each step (default 40)
#   MBT_STEPS  steps per trace of step (default 30)
#   MBT_CALM_STEPS  steps per trace of calmStep (default 80)
#   MBT_SEED   seed of the trace generator (default 0x5eed)
#   MBT_STORE  the store to replay on: dynamodb (default) or etcd
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
count=${MBT_COUNT:-40}
seed=${MBT_SEED:-0x5eed}

quint run quint/solas.qnt --main solas2 --step baseStep --mbt \
  --max-steps "${MBT_STEPS:-30}" --max-samples "$count" --n-traces "$count" \
  --seed "$seed" --out-itf "$dir/trace{seq}.itf.json" >/dev/null
quint run quint/solas.qnt --main solas2 --step calmStep --mbt \
  --max-steps "${MBT_CALM_STEPS:-80}" --max-samples "$count" --n-traces "$count" \
  --seed "$seed" --out-itf "$dir/calm{seq}.itf.json" >/dev/null
echo "mbt: generated $(find "$dir" -name '*.itf.json' | wc -l) traces," \
  "$(grep -l 'actionTaken":"requestPreemption"' "$dir"/*.itf.json | wc -l) with a preemption request"

echo "mbt: replay on ${MBT_STORE:-dynamodb}"
MBT_TRACES=$dir TEST_PKGS=./mbt/... scripts/test.sh -count=1 -run 'TestMBT'
