#!/usr/bin/env bash
# Moves the demo mesh to another store, ADR 0015.
#   switch-store.sh copy <to>    solas migrate seals the current store,
#                                copies every Device and Member to <to>,
#                                and verifies the copy
#   switch-store.sh point <to>   sets the storage URL of each cluster to
#                                <to> and restarts solas: the one setting
#                                that changes
#   switch-store.sh <to>         both steps
# <to> is dynamodb, etcd, or postgres; the current store comes from the
# Secret of the first cluster. Each cluster must run on the new store within
# D of the seal, spec 12.4. The demo lease D is 10 seconds, so point
# restarts both clusters at once.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
# shellcheck source=scripts/k3d-lib.sh
source scripts/k3d-lib.sh

usage() {
  echo "usage: switch-store.sh [copy|point] dynamodb|etcd|postgres" >&2
  exit 2
}
step=both
case ${1:-} in
copy | point)
  step=$1
  shift
  ;;
esac
to=${1:-}
case $to in
dynamodb | etcd | postgres) ;;
*) usage ;;
esac

to_url=$(demo/k3d/mesh.sh url "$to")
clusters=()
for kube in demo/k3d/.kube/*; do
  # Each cluster has one kubeconfig, named after it.
  case $(basename "$kube") in *.*) continue ;; esac
  clusters+=("$kube")
done
k() { KUBECONFIG="$root/$1" kubectl "${@:2}"; }
# The current store is the one in the Secret of the first cluster.
from_url=$(k "${clusters[0]}" -n solas-system get secret solas-storage -o jsonpath='{.data.url}' | base64 -d)
from=${from_url%%://*}
if [ "$from" = "$to" ]; then
  echo "the mesh is on $to already" >&2
  exit 2
fi

copy() {
  if [ "$from" = etcd ]; then
    # An etcd store cannot be sealed: stop solas first, spec 12.3.
    for kube in "${clusters[@]}"; do
      k "$kube" -n solas-system scale deploy/solas --replicas=0 >/dev/null
    done
  fi
  if [ "$to" = etcd ]; then
    # An etcd destination cannot raise its revision, so the tool refuses
    # one below the source, spec 12.2. The operator raises it first; in
    # the demo a helper writes to the empty etcd until it is above.
    docker run --rm --network solas-mesh \
      -e AWS_ACCESS_KEY_ID=local -e AWS_SECRET_ACCESS_KEY=local -e PGUSER=solas -e PGPASSWORD=solas \
      solas-demo:dev raise-etcd --from "$from_url" --to "$to_url"
  fi
  # The tool runs on the mesh network, so it reaches both stores. The
  # demo lease D is 10 seconds; an etcd source is watched that long.
  docker run --rm --network solas-mesh \
    -e AWS_ACCESS_KEY_ID=local -e AWS_SECRET_ACCESS_KEY=local -e PGUSER=solas -e PGPASSWORD=solas \
    solas:dev migrate --from "$from_url" --to "$to_url" --lease-duration 10s
}

point() {
  for kube in "${clusters[@]}"; do
    k "$kube" -n solas-system create secret generic solas-storage --dry-run=client -o yaml \
      --from-literal=url="$to_url" \
      --from-literal=accessKeyID=local --from-literal=secretAccessKey=local \
      --from-literal=pgUser=solas --from-literal=pgPassword=solas | k "$kube" apply -f - >/dev/null
  done
  for kube in "${clusters[@]}"; do
    k "$kube" -n solas-system scale deploy/solas --replicas=1 >/dev/null
    k "$kube" -n solas-system rollout restart deploy/solas >/dev/null
  done
  for kube in "${clusters[@]}"; do
    k "$kube" -n solas-system rollout status deploy/solas --timeout=180s >/dev/null
    echo "$(basename "$kube"): storage URL $to_url"
  done
}

case $step in
copy) copy ;;
point) point ;;
both)
  copy
  point
  ;;
esac
