#!/usr/bin/env bash
# The shared part of the demo: one Docker network, one dynamodb-local, one
# etcd, and one Postgres. The clusters use one of the stores, ADR 0014.
#   mesh.sh up                  create the network and the stores
#   mesh.sh down                remove them (delete the clusters first)
#   mesh.sh url <store>         print the storage URL that the clusters use:
#                               dynamodb, etcd, or postgres
#   mesh.sh endpoint            print the dynamodb-local endpoint
#   mesh.sh actuator            print the URL of the demo actuator, spec 17
# The URLs hold IP addresses, because pods cannot resolve Docker container
# names.
set -euo pipefail

network=solas-mesh
ddb=solas-ddb
ddb_image=amazon/dynamodb-local:3.3.1
etcd=solas-etcd
etcd_image=quay.io/coreos/etcd:v3.6.14
pg=solas-pg
pg_image=postgres:18-alpine
# The demo actuator: a fake provisioning service that checks nothing. The
# gatekeeper in each cluster sits in front of it, spec 17.
actuator=solas-actuator

ip() {
  docker inspect -f "{{(index .NetworkSettings.Networks \"$network\").IPAddress}}" "$1"
}

actuator_url() {
  echo "http://$(ip "$actuator"):9100"
}

endpoint() {
  echo "http://$(ip "$ddb"):8000"
}

url() {
  case ${1:-} in
  dynamodb) echo "dynamodb://solas?create-table=true&endpoint=$(endpoint)" ;;
  etcd) echo "etcd://$(ip "$etcd"):2379" ;;
  postgres) echo "postgres://$(ip "$pg"):5432?sslmode=disable&create-tables=true" ;;
  *)
    echo "usage: mesh.sh url dynamodb|etcd|postgres" >&2
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
  if ! docker inspect "$pg" >/dev/null 2>&1; then
    # One Postgres, with the user and the password that solas_install
    # puts in the Secret by default. Its data goes with the container.
    docker run -d --quiet --name "$pg" --network "$network" \
      -e POSTGRES_USER=solas -e POSTGRES_PASSWORD=solas -e POSTGRES_DB=solas \
      "$pg_image" -c fsync=off >/dev/null
  fi
  if ! docker inspect "$actuator" >/dev/null 2>&1; then
    docker run -d --quiet --name "$actuator" --network "$network" solas-demo:dev actuator >/dev/null
  fi
  endpoint
  ;;
down)
  docker unpause "$ddb" "$etcd" "$pg" >/dev/null 2>&1 || true
  docker rm -f "$ddb" "$etcd" "$pg" "$actuator" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  ;;
url)
  url "${2:-}"
  ;;
actuator)
  actuator_url
  ;;
endpoint)
  endpoint
  ;;
*)
  echo "usage: mesh.sh up|down|url dynamodb|etcd|postgres|endpoint|actuator" >&2
  exit 2
  ;;
esac
