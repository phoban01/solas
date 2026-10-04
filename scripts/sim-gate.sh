#!/usr/bin/env bash
# Runs the simulator gate: SIM_SEEDS seeds split over SIM_SHARDS shards.
# Shard 0 also checks that a broken store fails and that a seed replays.
# One shard alone: SIM_SHARD=n scripts/sim-gate.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

shards=${SIM_SHARDS:-4}
export SIM_SEEDS=${SIM_SEEDS:-400} SIM_SHARDS=$shards

run_shard() {
  local shard=$1 tests='TestSim$'
  if [ "$shard" = 0 ]; then
    tests='TestSim$|TestSimBrokenStore|TestSimDeterministic|TestSimReachesPreemption|TestSimFormatUpgrade|TestSimStoreOutage|TestSimReachesRetained'
  fi
  SIM_SHARD=$shard go test -race -count=1 -timeout 60m -run "$tests" ./sim/ >"$out/$shard.log" 2>&1
}

out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT

if [ -n "${SIM_SHARD:-}" ]; then
  shard_list=$SIM_SHARD
else
  shard_list=$(seq 0 $((shards - 1)))
fi

pids=()
for shard in $shard_list; do
  run_shard "$shard" &
  pids+=("$!:$shard")
done

failed=0
for entry in "${pids[@]}"; do
  pid=${entry%%:*} shard=${entry##*:}
  if wait "$pid"; then
    echo "sim-gate: shard $shard ok"
  else
    echo "sim-gate: shard $shard FAILED"
    cat "$out/$shard.log"
    failed=1
  fi
done
[ "$failed" = 0 ] && echo "sim-gate: $SIM_SEEDS seeds passed"
exit "$failed"
