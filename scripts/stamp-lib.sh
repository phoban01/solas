# Stamps of green runs. A check whose inputs did not change since a green
# run skips, so the gate pays only for what changed. The stamp of a check
# is an empty file named by the check and a hash of its inputs, under
# .cache/verify, which git ignores. VERIFY_FORCE=1 ignores the stamps.
#
# Source this file; it defines three functions.
stampdir=.cache/verify

# stamp_key <extra> <path>... prints a hash of the files under the paths
# and of the extra string, which names the settings of the check.
stamp_key() {
  local extra=$1
  shift
  {
    echo "$extra"
    find "$@" -type f -print0 | sort -z | xargs -0 sha256sum
  } | sha256sum | cut -c1-16
}

# stamp_fresh <name> <key> is true when a green run with this key is
# stamped and VERIFY_FORCE is not set.
stamp_fresh() {
  [ -z "${VERIFY_FORCE:-}" ] && [ -f "$stampdir/$1-$2" ]
}

# stamp_set <name> <key> stamps a green run.
stamp_set() {
  mkdir -p "$stampdir"
  touch "$stampdir/$1-$2"
}
