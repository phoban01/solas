#!/usr/bin/env bash
# The generic core of solas names no kind. A new kind touches its types,
# its registry package, the registration in pkg/apiserver/apiserver.go,
# RBAC, and tests, and nothing below the registry (docs/adding-a-kind.md).
# This check fails when a file of the core names a kind, so the claim
# stays true. Comments and the shared test helpers do not count.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

core=(
  pkg/storage
  pkg/apiserver/storage.go
  pkg/apiserver/progress.go
  pkg/apiserver/scheme.go
)
kinds='\b(Device|DeviceClaim|Member|MemberPolicy|MemberUsage|StoreFormat|devices|members|memberpolicies|memberusages|storeformats|claimRef|fencingToken|FencingToken|ClaimRef)\b'

status=0
while IFS= read -r file; do
  # Strip line comments before the match, so a comment may give an example.
  if hits=$(sed 's|//.*||' "$file" | grep -nE "$kinds"); then
    echo "kind-check: $file names a kind:" >&2
    echo "$hits" | sed 's/^/  /' >&2
    status=1
  fi
done < <(find "${core[@]}" -name '*.go' ! -name '*_test.go' 2>/dev/null | sort)
if [ "$status" -eq 0 ]; then
  echo "kind-check: the generic core names no kind"
fi
exit "$status"
