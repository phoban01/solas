#!/usr/bin/env bash
# The verify gate: the generated code, the kind check, the unit tests,
# the model checks, the model-based tests, and the Duvet report, with
# the time of each step. The model checks and the model-based tests skip
# when their inputs are unchanged since a green run, scripts/stamp-lib.sh;
# VERIFY_FORCE=1 runs them all the same.
set -euo pipefail
cd "$(dirname "$0")/.."
start=$SECONDS

# step <label> <command...> runs the command and prints its time.
step() {
  local label=$1 t0=$SECONDS
  shift
  echo "==== $label"
  "$@"
  echo "==== $label: $((SECONDS - t0))s"
}

step "codegen" hack/verify-codegen.sh
step "kind-check" scripts/kind-check.sh
step "unit tests" scripts/test.sh
step "quint" scripts/quint-check.sh
step "mbt" scripts/mbt.sh
step "duvet" bash -c 'rm -rf .duvet/requirements && duvet report --ci'
echo "==== verify: $((SECONDS - start))s"
