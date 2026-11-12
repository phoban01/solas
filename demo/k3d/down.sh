#!/usr/bin/env bash
# Removes the demo: the clusters, the device gatekeeper, and the mesh.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
# shellcheck source=scripts/k3d-lib.sh
source scripts/k3d-lib.sh

for name in a b; do
  docker unpause "k3d-e2e-$name-server-0" >/dev/null 2>&1 || true
  cluster_delete "e2e-$name"
done
docker rm -f solas-device-gpu-1 solas-device-gpu-h100-1 >/dev/null 2>&1 || true
# A store that an outage test left paused cannot be removed.
docker unpause solas-ddb solas-etcd solas-pg >/dev/null 2>&1 || true
demo/k3d/mesh.sh down
rm -rf demo/k3d/.kube
echo "demo removed"
