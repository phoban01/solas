#!/usr/bin/env bash
# Smoke test: one k3d cluster serves kubectl get devices from one store in
# the cluster. SOLAS_STORE picks it: dynamodb (dynamodb-local, the default)
# etcd, or postgres. It takes the e2e lock, because heavy runs must not overlap.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
cluster=e2e-smoke
# shellcheck source=scripts/k3d-lib.sh
source "$root/scripts/k3d-lib.sh"

e2e_lock

work=$(mktemp -d)
export KUBECONFIG="$work/kubeconfig"
cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "smoke: FAILED; pods and events:"
    kubectl -n solas-system get pods -o wide 2>/dev/null || true
    kubectl -n solas-system get events --sort-by=.lastTimestamp 2>/dev/null | tail -15 || true
    echo "smoke: logs of solas:"
    kubectl -n solas-system logs deploy/solas --tail=80 2>/dev/null || true
  fi
  cluster_delete "$cluster"
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT

step() { echo "smoke: $*"; }
# expect_fail <what> <command...> passes when the command fails.
expect_fail() {
  local what=$1
  shift
  if "$@" >"$work/out" 2>&1; then
    echo "smoke: expected failure: $what"
    exit 1
  fi
  step "rejected as expected: $what ($(tail -1 "$work/out"))"
}

step "create k3d cluster $cluster"
cluster_create "$cluster" "$KUBECONFIG"

step "build and load the images"
scripts/images.sh >/dev/null
image_import "$cluster" solas:dev

store=${SOLAS_STORE:-dynamodb}
step "deploy one $store and solas as member smoke"
kubectl apply -f deploy/solas/namespace.yaml >/dev/null
case $store in
dynamodb)
  kubectl apply -f deploy/dynamodb-local/ >/dev/null
  kubectl -n solas-system rollout status deploy/dynamodb-local --timeout=180s >/dev/null
  url="dynamodb://solas?create-table=true&endpoint=http://dynamodb-local.solas-system.svc:8000"
  ;;
postgres)
  kubectl apply -f deploy/postgres/ >/dev/null
  kubectl -n solas-system rollout status deploy/postgres --timeout=180s >/dev/null
  url="postgres://postgres.solas-system.svc:5432?sslmode=disable&create-tables=true"
  ;;
etcd)
  kubectl apply -f deploy/etcd/ >/dev/null
  kubectl -n solas-system rollout status deploy/etcd --timeout=180s >/dev/null
  url="etcd://etcd.solas-system.svc:2379"
  ;;
*)
  echo "smoke: SOLAS_STORE=$store is not dynamodb, etcd, or postgres"
  exit 1
  ;;
esac
solas_install smoke "$url"

step "create and list a device"
kubectl apply -f - >/dev/null <<YAML
apiVersion: solas.dev/v1alpha1
kind: Device
metadata:
  name: d1
spec:
  description: smoke test device
YAML
kubectl get devices
kubectl get device d1 -o jsonpath='{.metadata.resourceVersion}{"\n"}' | grep -qx '[0-9]\+'

step "create a member"
kubectl apply -f - >/dev/null <<YAML
apiVersion: solas.dev/v1alpha1
kind: Member
metadata:
  name: cluster-a
YAML
test "$(kubectl get member cluster-a -o jsonpath='{.spec.leaseDurationSeconds}/{.status.phase}')" = "30/Active"
# The claimRefs below name cluster-a with its real UID. The sweeper of
# member smoke clears a claimRef whose member UID it does not know. It
# leaves cluster-a alone until its lease of 30s has passed, and the steps
# below take less than that.
uid_a=$(kubectl get member cluster-a -o jsonpath='{.metadata.uid}')

ref() {
  printf '{"status":{"claimRef":{"member":"cluster-a","memberUID":"%s","namespace":"ns","name":"%s","uid":"u-%s"}}}' "$uid_a" "$1" "$1"
}

step "bind d1 to claim c1"
kubectl patch device d1 --subresource=status --type=merge -p "$(ref c1)" >/dev/null
test "$(kubectl get device d1 -o jsonpath='{.status.claimRef.name}')" = c1

expect_fail "move d1 to claim c2" \
  kubectl patch device d1 --subresource=status --type=merge -p "$(ref c2)"
expect_fail "delete bound device d1" kubectl delete device d1

test "$(kubectl get device d1 -o jsonpath='{.status.fencingToken}')" = 1

step "release d1, bind it again, and check the token goes up"
kubectl patch device d1 --subresource=status --type=merge -p '{"status":{"claimRef":null}}' >/dev/null
kubectl patch device d1 --subresource=status --type=merge -p "$(ref c3)" >/dev/null
test "$(kubectl get device d1 -o jsonpath='{.status.fencingToken}')" = 2

step "release d1, then delete it"
kubectl patch device d1 --subresource=status --type=merge -p '{"status":{"claimRef":null}}' >/dev/null
kubectl delete device d1 >/dev/null

step "the controller part joined as member smoke"
for _ in $(seq 30); do kubectl get member smoke >/dev/null 2>&1 && break; sleep 1; done
test "$(kubectl get member smoke -o jsonpath='{.status.phase}')" = Active

step "a claim binds a device"
kubectl apply -f - >/dev/null <<YAML
apiVersion: solas.dev/v1alpha1
kind: Device
metadata:
  name: gpu-1
  labels: {kind: gpu}
YAML
kubectl create namespace work >/dev/null
kubectl apply -f - >/dev/null <<YAML
apiVersion: claims.solas.dev/v1alpha1
kind: DeviceClaim
metadata:
  name: job
  namespace: work
spec:
  selector:
    matchLabels: {kind: gpu}
YAML
kubectl -n work wait --for=jsonpath='{.status.phase}'=Bound deviceclaim/job --timeout=60s >/dev/null
kubectl -n work get deviceclaims
test "$(kubectl get device gpu-1 -o jsonpath='{.status.claimRef.member}/{.status.fencingToken}')" = smoke/1

step "deleting the claim frees the device"
kubectl -n work delete deviceclaim job --timeout=60s >/dev/null
test -z "$(kubectl get device gpu-1 -o jsonpath='{.status.claimRef}')"

step "PASS"
