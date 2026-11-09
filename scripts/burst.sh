#!/usr/bin/env bash
# Bind burst, issue #268: one k3d cluster gets N claims at once and the
# script measures how long until every one is Bound, with W claim
# workers. It prints one row: store, workers, claims, seconds, binds per
# second. The store is dynamodb-local or etcd on the mesh (SOLAS_STORE),
# or a real table when SOLAS_DYNAMODB_URL is set, with the bench role's
# session in the cluster's Secret. It takes the e2e lock.
#   burst.sh <workers> [claims]     default 50 claims
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
# shellcheck source=scripts/k3d-lib.sh
source scripts/k3d-lib.sh
e2e_lock

workers=${1:?usage: burst.sh <workers> [claims]}
claims=${2:-50}
cluster=e2e-burst
kube=demo/k3d/.kube/burst
store=${SOLAS_STORE:-dynamodb}
mkdir -p demo/k3d/.kube

cleanup() {
  cluster_delete "$cluster"
  rm -f "$kube"
}
trap cleanup EXIT

if [ -n "${SOLAS_DYNAMODB_URL:-}" ]; then
  eval "$(scripts/aws-bench.sh env)"
  url=$SOLAS_DYNAMODB_URL
  store=dynamodb
else
  demo/k3d/mesh.sh up >/dev/null
  url=$(demo/k3d/mesh.sh url "$store")
fi

scripts/images.sh >/dev/null
cluster_create "$cluster" "$kube" solas-mesh >/dev/null
k3d kubeconfig get "$cluster" >"$kube"
export KUBECONFIG="$root/$kube"
image_import "$cluster" solas:dev
# A short lease, as the demo uses; the workers are what this measures.
solas_install burst "$url" 10s 1s 3s "$workers"

# The pool and the claims. Every claim takes any free device of the burst.
pool=$(mktemp)
for i in $(seq 1 "$claims"); do
  printf -- '---\napiVersion: solas.dev/v1alpha1\nkind: Device\nmetadata:\n  name: burst-%03d\nspec:\n  attributes: {burst: "true"}\n' "$i"
done >"$pool"
kubectl apply -f "$pool" >/dev/null
for _ in $(seq 60); do
  [ "$(kubectl get devices -o name 2>/dev/null | grep -c burst-)" -ge "$claims" ] && break
  sleep 1
done
kubectl create namespace work >/dev/null 2>&1 || true
burst=$(mktemp)
for i in $(seq 1 "$claims"); do
  printf -- '---\napiVersion: claims.solas.dev/v1alpha1\nkind: DeviceClaim\nmetadata:\n  name: burst-%03d\n  namespace: work\nspec:\n  selector:\n    cel: device.spec.attributes.burst == "true"\n' "$i"
done >"$burst"

start=$(date +%s.%N)
kubectl apply -f "$burst" >/dev/null
for _ in $(seq 600); do
  bound=$(kubectl get deviceclaims -n work -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' 2>/dev/null | grep -c '^Bound$' || true)
  [ "$bound" -ge "$claims" ] && break
  sleep 0.5
done
end=$(date +%s.%N)
seconds=$(awk -v a="$start" -v b="$end" 'BEGIN { printf "%.1f", b - a }')
rate=$(awk -v n="$claims" -v s="$seconds" 'BEGIN { if (s > 0) printf "%.1f", n / s; else print "inf" }')
rm -f "$pool" "$burst"
if [ "$bound" -lt "$claims" ]; then
  echo "burst: only $bound of $claims claims Bound after ${seconds}s" >&2
  exit 1
fi
printf 'store\tworkers\tclaims\tseconds\tbinds/s\n'
printf '%s\t%s\t%s\t%s\t%s\n' "$store" "$workers" "$claims" "$seconds" "$rate"
