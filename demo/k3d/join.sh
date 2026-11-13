#!/usr/bin/env bash
# Brings up cluster e2e-<name> on the mesh and joins it to solas. The
# cluster shares nothing with the others except the table.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
# shellcheck source=scripts/k3d-lib.sh
source scripts/k3d-lib.sh

name=${1:?usage: join.sh <name>}
cluster=e2e-$name
kube=demo/k3d/.kube/$name
mkdir -p demo/k3d/.kube
# SOLAS_STORE picks the shared store: dynamodb, the default, etcd, or postgres.
store=${SOLAS_STORE:-dynamodb}
url=$(demo/k3d/mesh.sh url "$store")
say() { echo "[$name] $*"; }

if ! k3d cluster get "$cluster" >/dev/null 2>&1; then
  say "create cluster $cluster"
  cluster_create "$cluster" "$kube" solas-mesh
fi
k3d kubeconfig get "$cluster" >"$kube"
export KUBECONFIG="$root/$kube"

say "load images"
image_import "$cluster" solas:dev solas-demo:dev

say "install solas as member $name, store $url"
# A short lease, so the demo scenes take seconds.
solas_install "$name" "$url" 10s 1s 3s
for _ in $(seq 60); do
  kubectl get member "$name" >/dev/null 2>&1 && break
  sleep 1
done
say "joined as member $name, uid $(kubectl get member "$name" -o jsonpath='{.metadata.uid}')"

say "install the gatekeeper in front of the demo actuator"
# One gatekeeper per actuator, spec 17. It reads devices through this
# cluster's solas API and forwards a change only to the holder with the
# current token.
sed "s|ACTUATOR_URL|$(demo/k3d/mesh.sh actuator)|" deploy/gatekeeper/gatekeeper.yaml | kubectl apply -f - >/dev/null
kubectl -n solas-system rollout status deploy/solas-gatekeeper --timeout=120s >/dev/null

say "workloads reach the actuator only through the gatekeeper"
# The actuator is outside the cluster, so an egress policy in the
# namespace of the workloads keeps them from its address, ADR 0017.
actuator_ip=$(demo/k3d/mesh.sh actuator | sed -e 's|^http://||' -e 's|:.*||')
kubectl create namespace work --dry-run=client -o yaml | kubectl apply -f - >/dev/null
sed "s|ACTUATOR_IP|$actuator_ip|" demo/k3d/manifests/actuator-policy.yaml | kubectl apply -f - >/dev/null
