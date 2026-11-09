#!/usr/bin/env bash
# The shared part of the demo: one Docker network, one dynamodb-local, and
# one etcd. The clusters use one of the two stores, ADR 0014.
#   mesh.sh up                  create the network and both stores
#   mesh.sh down                remove them (delete the clusters first)
#   mesh.sh url dynamodb|etcd   print the storage URL that the clusters use
#   mesh.sh endpoint            print the dynamodb-local endpoint
# The URLs hold IP addresses, because pods cannot resolve Docker container
# names.
set -euo pipefail

network=solas-mesh
ddb=solas-ddb
ddb_image=amazon/dynamodb-local:3.3.1
etcd=solas-etcd
etcd_image=quay.io/coreos/etcd:v3.6.14

ip() {
  docker inspect -f "{{(index .NetworkSettings.Networks \"$network\").IPAddress}}" "$1"
}

endpoint() {
  echo "http://$(ip "$ddb"):8000"
}

url() {
  case ${1:-} in
  dynamodb) echo "dynamodb://solas?create-table=true&endpoint=$(endpoint)" ;;
  etcd) echo "etcd://$(ip "$etcd"):2379" ;;
  *)
    echo "usage: mesh.sh url dynamodb|etcd" >&2
    exit 2
    ;;
  esac
}

case ${1:-} in
up)
  docker network inspect "$network" >/dev/null 2>&1 || docker network create "$network" >/dev/null
  if ! docker inspect "$ddb" >/dev/null 2>&1; then
    docker run -d --quiet --name "$ddb" --network "$network" "$ddb_image" -jar DynamoDBLocal.jar -inMemory >/dev/null
  fi
  if ! docker inspect "$etcd" >/dev/null 2>&1; then
    # One etcd member. Its data lives in the container and goes with it.
    docker run -d --quiet --name "$etcd" --network "$network" "$etcd_image" etcd \
      --name solas-etcd --data-dir /tmp/etcd \
      --listen-client-urls http://0.0.0.0:2379 --advertise-client-urls "http://$etcd:2379" >/dev/null
  fi
  endpoint
  ;;
down)
  docker unpause "$ddb" "$etcd" >/dev/null 2>&1 || true
  docker rm -f "$ddb" "$etcd" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  ;;
url)
  url "${2:-}"
  ;;
endpoint)
  endpoint
  ;;
*)
  echo "usage: mesh.sh up|down|url dynamodb|etcd|endpoint" >&2
  exit 2
  ;;
esac
