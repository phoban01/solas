#!/usr/bin/env bash
# Summarizes Prometheus text files: for each histogram or summary with a
# _sum and a _count, it prints the count and the mean, grouped by the
# labels. A bench run writes the files (SOLAS_BURST_METRICS), and this
# turns them into the rows of docs/benchmarks.md.
#   metrics-summary.sh <file>... [-- <grep pattern>]
set -euo pipefail
pattern='apiserver_request_duration_seconds|apiserver_watch_cache_read_wait_seconds|apiserver_watch_cache_initializations|controller_runtime_reconcile_time_seconds|workqueue_queue_duration_seconds|rest_client_request_duration_seconds'
files=()
for a in "$@"; do
  if [ "$a" = "--" ]; then
    shift
    pattern=${1:-$pattern}
    break
  fi
  files+=("$a")
  shift
done
grep -hE "^(${pattern})_(sum|count)" "${files[@]}" | awk '
{
  # name{labels} value, or name value
  split($1, a, "{")
  name = a[1]
  labels = (length(a) > 1) ? "{" a[2] : ""
  sub(/_sum$/, "", name); kind = "count"
  if ($1 ~ /_sum[{ ]|_sum$/) kind = "sum"; else sub(/_count$/, "", name)
  key = name " " labels
  v[key, kind] = $2
  keys[key] = 1
}
END {
  for (k in keys) {
    c = v[k, "count"] + 0
    if (c == 0) continue
    printf "%-10d %9.1f ms  %s\n", c, 1000 * v[k, "sum"] / c, k
  }
}' | sort -k3
