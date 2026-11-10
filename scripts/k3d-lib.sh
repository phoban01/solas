#!/usr/bin/env bash
# Shared functions for k3d clusters, ADR 0009. Source this file.

# The scripts need the tools that devbox pins: this kubectl and k3d, and
# openssl for the certificates. A script that starts outside devbox runs
# itself again through devbox.
if [ -z "${DEVBOX_SHELL_ENABLED:-}" ]; then
  self=$0
  # The scripts change to the repository root before they source this
  # file, so a relative path is relative to the directory they left.
  case $self in
  /*) ;;
  *) [ -f "$self" ] || self="${OLDPWD:-.}/$self" ;;
  esac
  exec devbox run -- "$(cd "$(dirname "$self")" && pwd)/$(basename "$self")" "$@"
fi

K3S_IMAGE=${K3S_IMAGE:-rancher/k3s:v1.37.0-k3s1}

# e2e_lock waits for the lock that heavy runs share, see CLAUDE.md.
e2e_lock() {
  local lock="${TMPDIR:-/tmp}/solas-e2e.lock"
  exec 9>"$lock"
  if ! flock -n 9; then
    echo "another run holds $lock, waiting"
    flock 9
  fi
}

# cluster_create <name> <kubeconfig> [docker-network] creates a k3d cluster
# with only what solas needs and writes its kubeconfig. Disk eviction
# starts only below 1% free, as kind does, so a full host disk does not
# evict the pods.
cluster_create() {
  local name=$1 kubeconfig=$2 network=${3:-}
  case $name in
  e2e-*) ;;
  *)
    echo "cluster name $name must start with e2e-" >&2
    return 1
    ;;
  esac
  k3d cluster create "$name" --image "$K3S_IMAGE" --wait --timeout 180s \
    --kubeconfig-update-default=false \
    ${network:+--network "$network"} \
    --k3s-arg "--disable=traefik@server:0" \
    --k3s-arg "--disable=servicelb@server:0" \
    --k3s-arg "--disable=metrics-server@server:0" \
    --k3s-arg "--kubelet-arg=eviction-hard=imagefs.available<1%,nodefs.available<1%@server:0" \
    --k3s-arg "--kubelet-arg=eviction-minimum-reclaim=imagefs.available=1%,nodefs.available=1%@server:0" \
    >/dev/null 2>&1
  k3d kubeconfig get "$name" >"$kubeconfig"
}

# cluster_delete <name> deletes a cluster whose name starts with e2e-.
cluster_delete() {
  case $1 in
  e2e-*) k3d cluster delete "$1" >/dev/null 2>&1 || true ;;
  *) echo "refusing to delete $1: name does not start with e2e-" >&2 ;;
  esac
}

# image_import <cluster> <image>... loads local images into the cluster in
# one call, then checks that the node has each one. k3d names its tarball
# by the second, so imports in quick succession can collide; a missing
# image is imported again.
image_import() {
  local cluster=$1
  shift
  local node="k3d-$cluster-server-0" missing attempt
  k3d image import "$@" --cluster "$cluster" >/dev/null 2>&1 || true
  for attempt in 1 2 3; do
    missing=()
    for image in "$@"; do
      # crictl images -q <ref> lists every image when <ref> is missing, so
      # check with inspecti, which fails.
      docker exec "$node" crictl inspecti "docker.io/library/$image" >/dev/null 2>&1 ||
        missing+=("$image")
    done
    [ ${#missing[@]} -eq 0 ] && return 0
    echo "image import: retry ${missing[*]} (attempt $attempt)" >&2
    sleep 1
    k3d image import "${missing[@]}" --cluster "$cluster" >/dev/null 2>&1 || true
  done
  echo "image import: ${missing[*]} not loaded into $cluster" >&2
  return 1
}

# solas_install <cluster-id> <storage url> [lease] [margin] [sweep]
# installs solas from deploy/solas into the cluster that KUBECONFIG names,
# and waits until the cluster serves the solas API. The ConfigMap and the
# Secret come first, because the Pod reads them at start.
solas_install() {
  local id=$1 url=$2 lease=${3:-30s} margin=${4:-3s} sweep=${5:-10s} workers=${6:-4}
  kubectl apply -f deploy/solas/namespace.yaml >/dev/null
  kubectl -n solas-system create secret generic solas-storage --dry-run=client -o yaml \
    --from-literal=url="$url" \
    --from-literal=accessKeyID="${AWS_ACCESS_KEY_ID:-local}" --from-literal=secretAccessKey="${AWS_SECRET_ACCESS_KEY:-local}" \
    --from-literal=sessionToken="${AWS_SESSION_TOKEN:-}" | kubectl apply -f - >/dev/null
  kubectl -n solas-system create configmap solas-member --dry-run=client -o yaml \
    --from-literal=clusterID="$id" --from-literal=leaseDuration="$lease" \
    --from-literal=leaseMargin="$margin" --from-literal=sweepInterval="$sweep" \
    --from-literal=claimWorkers="$workers" | kubectl apply -f - >/dev/null
  kubectl apply -f deploy/solas/crd.yaml >/dev/null
  kubectl wait --for=condition=Established crd/deviceclaims.claims.solas.dev --timeout=60s >/dev/null
  kubectl apply -f deploy/solas/ >/dev/null
  hack/gen-certs.sh >/dev/null
  kubectl -n solas-system rollout status deploy/solas --timeout=180s >/dev/null
  kubectl wait --for=condition=Available apiservice/v1alpha1.solas.dev --timeout=120s >/dev/null
}
