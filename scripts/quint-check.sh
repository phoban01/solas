#!/usr/bin/env bash
# Checks the Quint models.
#
# Positive models must hold their invariants and reach their witnesses.
# Negative models in quint/negative/ must break their invariant.
#
# The type checks, the tests, and the random runs are independent, so
# they run QUINT_JOBS at a time (default 4). The Apalache verifies run
# one at a time: each takes the Java heap of devbox.json.
#
# Each Apalache verify has a stamp of its own, keyed by the model it reads,
# so a change of one model runs only the verifies of that model.
# The script skips when the models, this script, the settings, and the
# quint version are unchanged since a green run, scripts/stamp-lib.sh.
# VERIFY_FORCE=1 runs it all the same.
# QUINT_VERIFY=0 skips the Apalache runs, for a quick local check.
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=scripts/stamp-lib.sh
source scripts/stamp-lib.sh
seed=${QUINT_SEED:-0x2a}
steps=${QUINT_STEPS:-40}
samples=${QUINT_SAMPLES:-20000}
depth=${QUINT_VERIFY_DEPTH:-6}
verify=${QUINT_VERIFY:-1}
jobs=${QUINT_JOBS:-4}

run() {
  quint run "$@" --max-steps "$steps" --max-samples "$samples" --seed "$seed"
}

# A pool job: --job <dir> <n> runs check n of the queue in dir and writes
# its result line. The pool below starts this script once per job.
job() {
  local work=$1 i=$2 kind label rc=0
  { read -r kind; read -r label; } <"$work/$i.job"
  if eval "$(cat "$work/$i.cmd")" >"$work/$i.out" 2>&1; then rc=0; else rc=$?; fi
  case $kind in
  pass)
    if [ "$rc" = 0 ]; then
      echo "ok    $label" >"$work/$i.result"
    else
      { echo "FAIL  $label"; tail -20 "$work/$i.out"; } >"$work/$i.result"
    fi
    ;;
  violate)
    # A parse or type error does not count as a violation.
    if grep -qE '^\[violation\]|found a counterexample' "$work/$i.out"; then
      echo "ok    $label" >"$work/$i.result"
    else
      { echo "FAIL  $label: expected a violation"; tail -20 "$work/$i.out"; } >"$work/$i.result"
    fi
    ;;
  esac
}
if [ "${1:-}" = --job ]; then
  job "$2" "$3"
  exit 0
fi

qv=$(quint --version)
key=$(stamp_key "$qv seed=$seed steps=$steps samples=$samples depth=$depth verify=$verify" \
  quint scripts/quint-check.sh)
if stamp_fresh quint "$key"; then
  echo "skip  quint-check: the models and the checks are unchanged since a green run ($key); VERIFY_FORCE=1 runs them"
  exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
n=0
failed=0

# queue <pass|violate> <label> <command...> adds a check to the pool.
# pass: the command must exit 0. violate: it must report a violation.
queue() {
  n=$((n + 1))
  printf '%s\n%s\n' "$1" "$2" >"$work/$n.job"
  shift 2
  printf '%q ' "$@" >"$work/$n.cmd"
}
# pass and violate run a check at once, for the Apalache verifies.
pass() {
  local label=$1
  shift
  if "$@" >"$work/now.out" 2>&1; then
    echo "ok    $label"
  else
    echo "FAIL  $label"
    tail -20 "$work/now.out"
    failed=1
  fi
}
violate() {
  local label=$1
  shift
  "$@" >"$work/now.out" 2>&1 || true
  if grep -qE '^\[violation\]|found a counterexample' "$work/now.out"; then
    echo "ok    $label"
  else
    echo "FAIL  $label: expected a violation"
    tail -20 "$work/now.out"
    failed=1
  fi
}

for f in quint/*.qnt quint/negative/*.qnt; do
  queue pass "typecheck $f" quint typecheck "$f"
done

queue pass "test solas2" quint test quint/solas.qnt --main solas2
queue pass "test solas2delay" quint test quint/solas.qnt --main solas2delay
queue pass "test solas2protect" quint test quint/solas.qnt --main solas2protect
queue pass "test migrate2" quint test quint/migrate.qnt --main migrate2
queue pass "test restore2" quint test quint/restore.qnt --main restore2
queue pass "test limits2" quint test quint/limits.qnt --main limits2
# The test passes when its scenario breaks claimMatchesDevice.
queue pass "negative adopt-any-uid" quint test quint/negative/adopt-any-uid.qnt \
  --main adopt_any_uid
# The test passes when its scenario breaks usesInOrder.
queue pass "negative no-token-check" quint test quint/negative/no-token-check.qnt \
  --main no_token_check
# The test passes when an observer that keeps its observations across a
# store outage sweeps a member right after it, spec 8.1.
queue pass "negative outage-sweep" quint test quint/negative/outage-sweep.qnt \
  --main outage_sweep --match sweepOnReturnTest
# The tests pass when a bind with no reservation, or a cleanup that does
# not wait out the deadline, breaks withinLimit, spec 15.
queue pass "negative no-reservation" quint test quint/negative/no-reservation.qnt \
  --main no_reservation --match overLimitTest
queue pass "negative early-cleanup" quint test quint/negative/early-cleanup.qnt \
  --main early_cleanup --match earlyCleanupTest
# The test passes when a restore that keeps the old epoch sends a token
# back, spec 13.
queue pass "negative no-epoch" quint test quint/negative/no-epoch.qnt \
  --main no_epoch --match tokenBackTest
# The test passes when a release that does not raise the token breaks
# tokensUnique, spec 11.2.
queue pass "negative no-token-raise" quint test quint/negative/no-token-raise.qnt \
  --main no_token_raise --match tokenRaiseTest
# The test passes when a copy that keeps the versions of the destination
# sends a version back, spec 12.2.
queue pass "negative no-move-epoch" quint test quint/negative/no-move-epoch.qnt \
  --main no_move_epoch --match versionBackTest
# The test passes when the forced clear of spec 10.8 breaks
# claimMatchesDevice.
queue pass "negative force-clear" quint test quint/negative/force-clear.qnt \
  --main force_clear
# The test passes when a copy with no seal breaks noDoubleBind.
queue pass "negative no-seal" quint test quint/negative/no-seal.qnt --main no_seal \
  --match doubleBindTest

queue pass "run solas2 safety" run quint/solas.qnt --main solas2 --invariant safety
# Fencing does not depend on the lease margin: it holds even when M = 0.
queue pass "run no-margin fencing" run quint/negative/no-margin.qnt --invariant fencing
queue pass "run store3 storeSafety" run quint/store.qnt --main store3 --invariant storeSafety
queue pass "run store3pg storeSafety" run quint/store.qnt --main store3pg --invariant storeSafety
queue pass "run migrate2 migrateSafety" run quint/migrate.qnt --main migrate2 --invariant migrateSafety
queue pass "run restore2 tokenOrder" run quint/restore.qnt --main restore2 --invariant tokenOrder
queue pass "run limits2 withinLimit" run quint/limits.qnt --main limits2 --invariant withinLimit

for w in witnessBound witnessCleared witnessLeft; do
  queue violate "witness solas2 $w" run quint/solas.qnt --main solas2 --invariant "$w"
done
# witnessLost is not run at random: the run to a loss is long, and the
# tests reclaimTest and preemptingLostTest of solas2 reach it directly.
queue violate "witness solas2 witnessRetained" quint run quint/solas.qnt --main solas2 \
  --invariant witnessRetained --max-steps 80 --max-samples "$samples" --seed "$seed"
queue violate "witness restore2 witnessRestored" run quint/restore.qnt --main restore2 --invariant witnessRestored
queue violate "witness limits2 witnessFull" run quint/limits.qnt --main limits2 --invariant witnessFull
for w in witnessSwitched witnessBoundOnDst; do
  queue violate "witness migrate2 $w" run quint/migrate.qnt --main migrate2 --invariant "$w"
done
for w in witnessDelivered witnessGone witnessListed; do
  queue violate "witness store3pg $w" run quint/store.qnt --main store3pg --invariant "$w"
  queue violate "witness store3 $w" run quint/store.qnt --main store3 --invariant "$w"
done

queue violate "negative no-seal run" \
  run quint/negative/no-seal.qnt --invariant noDoubleBind
queue violate "negative list-no-recheck" \
  run quint/negative/list-no-recheck.qnt --invariant listSnapshot
queue violate "negative sequence-not-lock" \
  run quint/negative/sequence-not-lock.qnt --invariant listSnapshot
queue violate "negative watch-no-gap-check" \
  run quint/negative/watch-no-gap-check.qnt --invariant watchComplete

echo "quint: $n checks, $jobs at a time"
t0=$SECONDS
seq 1 "$n" | xargs -P "$jobs" -I{} "$0" --job "$work" {}
for i in $(seq 1 "$n"); do
  cat "$work/$i.result"
  grep -q '^ok' "$work/$i.result" || failed=1
done

echo "quint: the pool took $((SECONDS - t0))s"
t0=$SECONDS
# verified <pass|violate> <label> <file>... -- <command>...: an Apalache
# verify with a stamp of its own, keyed by the files it reads, so a change
# of one model runs only the verifies of that model.
verified() {
  local kind=$1 label=$2 t0=$SECONDS before=$failed k
  local -a files=()
  shift 2
  while [ "$1" != -- ]; do
    files+=("$1")
    shift
  done
  shift
  k=$(stamp_key "$qv $label $*" "${files[@]}")
  if stamp_fresh "verify-${label// /-}" "$k"; then
    echo "skip  $label: unchanged since a green run"
    return
  fi
  "$kind" "$label" "$@"
  echo "      $label took $((SECONDS - t0))s"
  if [ "$failed" = "$before" ]; then
    stamp_set "verify-${label// /-}" "$k"
  fi
}

if [ "$verify" = 1 ]; then
  verified violate "negative unconditional-bind" quint/solas.qnt quint/negative/unconditional-bind.qnt -- \
    quint verify quint/negative/unconditional-bind.qnt --invariant noDoubleBind --max-steps 10
  verified violate "negative no-margin" quint/solas.qnt quint/negative/no-margin.qnt -- \
    quint verify quint/negative/no-margin.qnt --invariant leaseSafety --max-steps 10
  verified violate "negative members-first" quint/solas.qnt quint/negative/members-first.qnt -- \
    quint verify quint/negative/members-first.qnt --invariant claimMatchesDevice --max-steps 10
  verified pass "verify solas2 safety depth $depth" quint/solas.qnt -- \
    quint verify quint/solas.qnt --main solas2 --invariant safety --max-steps "$depth"
  verified pass "verify limits2 withinLimit depth 8" quint/limits.qnt -- \
    quint verify quint/limits.qnt --main limits2 --invariant withinLimit --max-steps 8
  verified pass "verify restore2 tokenOrder depth 10" quint/restore.qnt -- \
    quint verify quint/restore.qnt --main restore2 --invariant tokenOrder --max-steps 10
  verified pass "verify migrate2 migrateSafety depth 10" quint/migrate.qnt -- \
    quint verify quint/migrate.qnt --main migrate2 --invariant migrateSafety --max-steps 10
  verified pass "verify store3 storeSafety depth 12" quint/store.qnt -- \
    quint verify quint/store.qnt --main store3 --invariant storeSafety --max-steps 12
  verified pass "verify store3pg storeSafety depth 12" quint/store.qnt -- \
    quint verify quint/store.qnt --main store3pg --invariant storeSafety --max-steps 12
else
  echo "skip  quint verify (QUINT_VERIFY=0)"
fi

echo "quint: the Apalache verifies took $((SECONDS - t0))s"
if [ "$failed" = 0 ]; then
  stamp_set quint "$key"
fi
exit "$failed"
