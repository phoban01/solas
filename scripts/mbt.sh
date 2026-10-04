#!/usr/bin/env bash
# Model-based tests: generate traces of quint/solas.qnt with a fixed seed,
# then replay each one against the real REST stores on dynamodb-local.
# The traces of step include faults, transfers, and recoveries. The
# traces of calmStep have none, so they reach preemption. The traces of
# calmTransferStep add the transfer to calmStep, so they reach a bind
# against an offer.
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

quint run quint/solas.qnt --main solas2 --step step --mbt \
  --max-steps "${MBT_STEPS:-30}" --max-samples "$count" --n-traces "$count" \
  --seed "$seed" --out-itf "$dir/trace{seq}.itf.json" >/dev/null
quint run quint/solas.qnt --main solas2 --step calmStep --mbt \
  --max-steps "${MBT_CALM_STEPS:-80}" --max-samples "$count" --n-traces "$count" \
  --seed "$seed" --out-itf "$dir/calm{seq}.itf.json" >/dev/null
quint run quint/solas.qnt --main solas2 --step calmTransferStep --mbt \
  --max-steps "${MBT_CALM_STEPS:-80}" --max-samples "$count" --n-traces "$count" \
  --seed "$seed" --out-itf "$dir/xfer{seq}.itf.json" >/dev/null
if ! grep -q 'actionTaken":"bindOffer"' "$dir"/xfer*.itf.json; then
  echo "mbt: no trace has a bind against an offer" >&2
  exit 1
fi
echo "mbt: generated $(find "$dir" -name '*.itf.json' | wc -l) traces," \
  "$(grep -l 'actionTaken":"requestPreemption"' "$dir"/*.itf.json | wc -l) with a preemption request,"\
  "$(grep -l 'actionTaken":"bindOffer"' "$dir"/*.itf.json | wc -l) with a transfer,"\
  "$(grep -l 'actionTaken":"recoverWrite"' "$dir"/*.itf.json | wc -l) with a recovery"

echo "mbt: replay on ${MBT_STORE:-dynamodb}"
MBT_TRACES=$dir TEST_PKGS=./mbt/... scripts/test.sh -count=1 -run 'TestMBT'
