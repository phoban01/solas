#!/usr/bin/env bash
# Checks the Quint models.
#
# Positive models must hold their invariants and reach their witnesses.
# Negative models in quint/negative/ must break their invariant.
#
# QUINT_VERIFY=0 skips the Apalache runs, for a quick local check.
set -euo pipefail
cd "$(dirname "$0")/.."

seed=${QUINT_SEED:-0x2a}
steps=${QUINT_STEPS:-40}
samples=${QUINT_SAMPLES:-20000}
depth=${QUINT_VERIFY_DEPTH:-6}
verify=${QUINT_VERIFY:-1}

out=$(mktemp)
trap 'rm -f "$out"' EXIT
failed=0

# pass <label> <command...>: the command must exit 0.
pass() {
  local label=$1
  shift
  if "$@" >"$out" 2>&1; then
    echo "ok    $label"
  else
    echo "FAIL  $label"
    tail -20 "$out"
    failed=1
  fi
}

# violate <label> <command...>: the command must report a violation.
# A parse or type error does not count as a violation.
violate() {
  local label=$1
  shift
  "$@" >"$out" 2>&1 || true
  if grep -qE '^\[violation\]|found a counterexample' "$out"; then
    echo "ok    $label"
  else
    echo "FAIL  $label: expected a violation"
    tail -20 "$out"
    failed=1
  fi
}

run() {
  quint run "$@" --max-steps "$steps" --max-samples "$samples" --seed "$seed"
}

for f in quint/*.qnt quint/negative/*.qnt; do
  pass "typecheck $f" quint typecheck "$f"
done

pass "test solas2" quint test quint/solas.qnt --main solas2
pass "test migrate2" quint test quint/migrate.qnt --main migrate2
# The test passes when its scenario breaks claimMatchesDevice.
pass "negative adopt-any-uid" quint test quint/negative/adopt-any-uid.qnt \
  --main adopt_any_uid
# The test passes when its scenario breaks usesInOrder.
pass "negative no-token-check" quint test quint/negative/no-token-check.qnt \
  --main no_token_check
# The test passes when the forced clear of spec 10.8 breaks
# claimMatchesDevice.
# The test passes when a release that does not raise the token breaks
# tokensUnique, spec 11.2.
pass "negative no-token-raise" quint test quint/negative/no-token-raise.qnt \
  --main no_token_raise --match tokenRaiseTest
pass "negative force-clear" quint test quint/negative/force-clear.qnt \
  --main force_clear

# The test passes when a copy with no seal breaks noDoubleBind.
pass "negative no-seal" quint test quint/negative/no-seal.qnt --main no_seal \
  --match doubleBindTest

pass "run solas2 safety" run quint/solas.qnt --main solas2 --invariant safety
# Fencing does not depend on the lease margin: it holds even when M = 0.
pass "run no-margin fencing" run quint/negative/no-margin.qnt --invariant fencing
pass "run store3 storeSafety" run quint/store.qnt --main store3 --invariant storeSafety
pass "run migrate2 migrateSafety" run quint/migrate.qnt --main migrate2 --invariant migrateSafety

for w in witnessBound witnessCleared witnessLost witnessLeft; do
  violate "witness solas2 $w" run quint/solas.qnt --main solas2 --invariant "$w"
done
for w in witnessSwitched witnessBoundOnDst; do
  violate "witness migrate2 $w" run quint/migrate.qnt --main migrate2 --invariant "$w"
done
for w in witnessDelivered witnessGone witnessListed; do
  violate "witness store3 $w" run quint/store.qnt --main store3 --invariant "$w"
done

violate "negative no-seal run" \
  run quint/negative/no-seal.qnt --invariant noDoubleBind
violate "negative list-no-recheck" \
  run quint/negative/list-no-recheck.qnt --invariant listSnapshot
violate "negative watch-no-gap-check" \
  run quint/negative/watch-no-gap-check.qnt --invariant watchComplete

if [ "$verify" = 1 ]; then
  violate "negative unconditional-bind" quint verify \
    quint/negative/unconditional-bind.qnt --invariant noDoubleBind --max-steps 10
  violate "negative no-margin" quint verify \
    quint/negative/no-margin.qnt --invariant leaseSafety --max-steps 10
  violate "negative members-first" quint verify \
    quint/negative/members-first.qnt --invariant claimMatchesDevice --max-steps 10
  pass "verify solas2 safety depth $depth" quint verify quint/solas.qnt \
    --main solas2 --invariant safety --max-steps "$depth"
  pass "verify migrate2 migrateSafety depth 10" quint verify quint/migrate.qnt \
    --main migrate2 --invariant migrateSafety --max-steps 10
  pass "verify store3 storeSafety depth 12" quint verify quint/store.qnt \
    --main store3 --invariant storeSafety --max-steps 12
else
  echo "skip  quint verify (QUINT_VERIFY=0)"
fi

exit "$failed"
