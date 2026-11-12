#!/usr/bin/env bash
# Starts the local stores for tests and benchmarks: dynamodb-local in
# Docker, a Postgres in Docker, and an etcd from the devbox profile.
# Source this file, then call
# start_local_stores. It exports SOLAS_DYNAMODB_ENDPOINT,
# SOLAS_POSTGRES_URL with PGUSER and PGPASSWORD, and
# SOLAS_ETCD_ENDPOINT, and leaves a store that already runs alone when
# its variable is set. It registers the cleanup in the array `cleanup`,
# which the caller runs on exit:
#   trap 'for c in "${cleanup[@]}"; do eval "$c"; done' EXIT

ddb_image=amazon/dynamodb-local:3.3.1
pg_image=postgres:18-alpine
cleanup=()

# wait_port <port> waits until something listens on the port.
wait_port() {
  for _ in $(seq 50); do
    if (echo >"/dev/tcp/127.0.0.1/$1") 2>/dev/null; then
      return 0
    fi
    sleep 0.2
  done
  echo "nothing listens on port $1" >&2
  return 1
}


# wait_pg <container> waits until the Postgres in the container accepts
# connections. The port opens before the server is ready, so this asks
# the server.
wait_pg() {
  for _ in $(seq 100); do
    if docker exec "$1" pg_isready -q -U solas 2>/dev/null; then
      return 0
    fi
    sleep 0.2
  done
  echo "postgres in $1 is not ready" >&2
  docker logs "$1" >&2
  return 1
}

# free_port prints a port that nothing listens on.
free_port() {
  local p
  while :; do
    p=$((20000 + RANDOM % 20000))
    if ! (echo >"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
      echo "$p"
      return
    fi
  done
}

# start_local_stores <tag> starts the stores that do not run yet. The tag
# names the dynamodb-local container.
start_local_stores() {
  local tag=$1
  if [ -z "${SOLAS_DYNAMODB_ENDPOINT:-}" ] && command -v docker >/dev/null \
    && docker info >/dev/null 2>&1; then
    local name="solas-ddb-$tag-$$"
    docker run -d --rm --name "$name" -p 127.0.0.1::8000 "$ddb_image" \
      -jar DynamoDBLocal.jar -inMemory >/dev/null
    cleanup+=("docker rm -f $name >/dev/null 2>&1 || true")
    local port
    port=$(docker port "$name" 8000/tcp | head -1 | sed 's/.*://')
    wait_port "$port"
    export SOLAS_DYNAMODB_ENDPOINT="http://127.0.0.1:$port"
    echo "$tag: dynamodb-local at $SOLAS_DYNAMODB_ENDPOINT"
  elif [ -z "${SOLAS_DYNAMODB_ENDPOINT:-}" ]; then
    echo "$tag: no Docker; nothing that needs DynamoDB runs"
  elif [ "${SOLAS_DYNAMODB_ENDPOINT}" != none ]; then
    echo "$tag: DynamoDB at $SOLAS_DYNAMODB_ENDPOINT"
  fi

  if [ -z "${SOLAS_ETCD_ENDPOINT:-}" ] && command -v etcd >/dev/null; then
    local dir client peer
    dir=$(mktemp -d)
    client=$(free_port)
    peer=$(free_port)
    while [ "$peer" = "$client" ]; do peer=$(free_port); done
    # SOLAS_ETCD_FSYNC=1 keeps fsync on, for a benchmark of what it costs.
    local fsync_flag=--unsafe-no-fsync
    [ -n "${SOLAS_ETCD_FSYNC:-}" ] && fsync_flag=""
    # shellcheck disable=SC2086
    etcd --data-dir "$dir" --log-level error $fsync_flag \
      --listen-client-urls "http://127.0.0.1:$client" --advertise-client-urls "http://127.0.0.1:$client" \
      --listen-peer-urls "http://127.0.0.1:$peer" --initial-advertise-peer-urls "http://127.0.0.1:$peer" \
      --initial-cluster "default=http://127.0.0.1:$peer" >"$dir/log" 2>&1 &
    cleanup+=("kill $! 2>/dev/null || true; wait $! 2>/dev/null || true; rm -rf $dir")
    if ! wait_port "$client"; then
      cat "$dir/log" >&2
      return 1
    fi
    export SOLAS_ETCD_ENDPOINT="http://127.0.0.1:$client"
    echo "$tag: etcd at $SOLAS_ETCD_ENDPOINT"
  elif [ -z "${SOLAS_ETCD_ENDPOINT:-}" ]; then
    echo "$tag: no etcd; nothing that needs etcd runs"
  fi

  if [ -z "${SOLAS_POSTGRES_URL:-}" ] && command -v docker >/dev/null \
    && docker info >/dev/null 2>&1; then
    local pg="solas-pg-$tag-$$" pgport
    # SOLAS_POSTGRES_FSYNC=1 keeps fsync on, for a benchmark of what it costs.
    local pg_fsync=off
    [ -n "${SOLAS_POSTGRES_FSYNC:-}" ] && pg_fsync=on
    docker run -d --rm --name "$pg" -p 127.0.0.1::5432 \
      -e POSTGRES_USER=solas -e POSTGRES_PASSWORD=solas -e POSTGRES_DB=solas \
      --tmpfs /var/lib/postgresql "$pg_image" -c fsync=$pg_fsync >/dev/null
    cleanup+=("docker rm -f $pg >/dev/null 2>&1 || true")
    pgport=$(docker port "$pg" 5432/tcp | head -1 | sed 's/.*://')
    wait_pg "$pg"
    export SOLAS_POSTGRES_URL="postgres://127.0.0.1:$pgport?sslmode=disable&create-tables=true"
    export PGUSER=solas PGPASSWORD=solas
    echo "$tag: postgres at $SOLAS_POSTGRES_URL"
  elif [ -z "${SOLAS_POSTGRES_URL:-}" ]; then
    echo "$tag: no Docker; nothing that needs Postgres runs"
  elif [ "${SOLAS_POSTGRES_URL}" != none ]; then
    echo "$tag: Postgres at $SOLAS_POSTGRES_URL"
  fi
}
