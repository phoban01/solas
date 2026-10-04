#!/usr/bin/env bash
# Pivot smoke test: one k3d cluster with solas and an old Device CRD in its
# etcd. It copies and verifies the old devices, then installs the webhook
# and checks that writes of the old CRD reach solas. ADR 0010, spec 9.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
# shellcheck source=scripts/k3d-lib.sh
source scripts/k3d-lib.sh
cluster=e2e-pivot
e2e_lock

work=$(mktemp -d)
export KUBECONFIG="$work/kubeconfig"
cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "pivot-smoke: FAILED; webhook logs:"
    kubectl -n solas-system logs deploy/solas-pivot --tail=30 2>/dev/null || true
  fi
  cluster_delete "$cluster"
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT

step() { echo "pivot-smoke: $*"; }
pivot() { go run ./cmd/solas-pivot "$1" --from inventory.example.com/v1/devices "${@:2}"; }
desc() { kubectl get devices.solas.dev "$1" -o jsonpath='{.spec.description}' 2>/dev/null; }
expect_fail() {
  local what=$1
  shift
  if "$@" >"$work/out" 2>&1; then
    echo "pivot-smoke: expected failure: $what"
    exit 1
  fi
  step "rejected as expected: $what ($(tail -1 "$work/out"))"
}

step "create cluster and deploy solas"
cluster_create "$cluster" "$KUBECONFIG"
scripts/images.sh >/dev/null
image_import "$cluster" solas:dev solas-pivot:dev
kubectl apply -f deploy/solas/namespace.yaml >/dev/null
kubectl apply -f deploy/dynamodb-local/ >/dev/null
kubectl -n solas-system rollout status deploy/dynamodb-local --timeout=180s >/dev/null
solas_install pivot "dynamodb://solas?create-table=true&endpoint=http://dynamodb-local.solas-system.svc:8000"

step "the old CRD holds two devices in etcd"
kubectl apply -f deploy/pivot/example/crd.yaml >/dev/null
kubectl wait --for=condition=Established crd/devices.inventory.example.com --timeout=60s >/dev/null
kubectl apply -f deploy/pivot/example/devices.yaml >/dev/null
kubectl get devices.inventory.example.com

step "copy, dry run first"
pivot copy --dry-run
test -z "$(kubectl get devices.solas.dev -o name)"
pivot copy
pivot verify
kubectl get devices.solas.dev
test "$(desc gpu-old-1)" = "an A100 kept in etcd"
params=$(kubectl get devices.solas.dev gpu-old-1 -o jsonpath='{.spec.parameters.description}')
test "$params" = "an A100 kept in etcd"
test "$(kubectl get devices.inventory.example.com gpu-old-1 -o jsonpath='{.metadata.annotations.solas\.dev/pivoted-to}')" = solas.dev/devices/gpu-old-1
pivot copy | grep -q 'unchanged 2'

step "install the webhook"
kubectl apply -f deploy/pivot/rbac.yaml -f deploy/pivot/deployment.yaml -f deploy/pivot/webhook.yaml >/dev/null
hack/gen-webhook-certs.sh >/dev/null
kubectl -n solas-system rollout status deploy/solas-pivot --timeout=120s >/dev/null

step "a new old device reaches solas"
new_device() {
  kubectl apply -f - <<YAML
apiVersion: inventory.example.com/v1
kind: Device
metadata:
  name: gpu-old-3
  labels: {kind: gpu}
spec:
  description: $1
YAML
}
for _ in $(seq 30); do new_device "created after cutover" >/dev/null 2>&1 && break; sleep 1; done
test "$(desc gpu-old-3)" = "created after cutover"
test "$(kubectl get devices.inventory.example.com gpu-old-3 -o jsonpath='{.metadata.annotations.solas\.dev/pivoted-to}')" = solas.dev/devices/gpu-old-3

step "an update of the old device reaches solas"
new_device "updated through the old CRD" >/dev/null
test "$(desc gpu-old-3)" = "updated through the old CRD"

step "a delete of the old device reaches solas"
kubectl delete devices.inventory.example.com gpu-old-3 >/dev/null
! kubectl get devices.solas.dev gpu-old-3 >/dev/null 2>&1

step "a delete of an old device whose solas Device is bound is denied"
# The claimRef names a real Member, so the sweeper of member pivot leaves
# it alone for the lease of 30s.
kubectl apply -f - >/dev/null <<YAML
apiVersion: solas.dev/v1alpha1
kind: Member
metadata:
  name: cluster-a
YAML
uid_a=$(kubectl get member cluster-a -o jsonpath='{.metadata.uid}')
kubectl patch devices.solas.dev gpu-old-1 --subresource=status --type=merge -p \
  "{\"status\":{\"claimRef\":{\"member\":\"cluster-a\",\"memberUID\":\"$uid_a\",\"namespace\":\"ns\",\"name\":\"job\",\"uid\":\"u1\"}}}" >/dev/null
expect_fail "delete old gpu-old-1 while bound" kubectl delete devices.inventory.example.com gpu-old-1
kubectl get devices.inventory.example.com gpu-old-1 >/dev/null

pivot verify
step "PASS"
