# Adding a kind

Solas stores five kinds in the shared store: Device, Member,
MemberPolicy, MemberUsage, and StoreFormat. The storage layer, the
resource version counter, the event log and the watch, the format and
epoch stamping, and the readiness logic name none of them. A new kind
is a set of files above that layer. This page lists them, with the
commit that added MemberPolicy as the proof, and names the check that
keeps the core generic.

## What a kind needs

| Part | Files | What goes there |
|------|-------|-----------------|
| Types | `pkg/apis/solas/types.go`, `pkg/apis/solas/v1alpha1/types.go`, both `register.go` | The internal and the versioned struct, and the registration of both in the scheme |
| Generated code | `pkg/apis/**/zz_generated.*.go`, `pkg/generated/**` | `devbox run codegen` writes conversion, deep copy, OpenAPI, clients, informers, and listers |
| Validation | `pkg/apis/solas/validation/validation.go` | The rules of the kind's fields, in Go. Spec sentences cite them through Duvet |
| Registry | `pkg/registry/solas/<kind>/storage.go` and its test | The REST store: `NewREST`, the strategy, the table columns. A kind with rules on its transitions also gives a `guard.Transition` |
| Registration | `pkg/apiserver/apiserver.go` | One `NewREST` call, one entry in the versioned resources map, and, for a kind with a transition guard, one entry in `guards` |
| RBAC | `deploy/solas/rbac.yaml` | Who may read and write it |
| Example | `deploy/solas/config/<kind>.yaml` | A manifest an operator can start from |
| Spec | `spec/solas.md` | The rules of the kind, with MUST only for what a test checks |

A kind that must be held by one claim at a time, as a Device is, needs
the holder rules too. Today those rules live in the Device registry
(`strategy.go` and `guard.go`). Issue #264 makes them a primitive that a
kind opts in to.

## What a kind never touches

- `pkg/storage/**`: the DynamoDB store, the storage guard, and the URL.
- `pkg/apiserver/storage.go` and `pkg/apiserver/progress.go`: the
  options getter, the watch cache wiring, and the progress support.
- `pkg/apiserver/scheme.go`. (`pkg/format` knows Members, because
  finalization counts the Active members, spec 11.4; it is protocol, not
  storage.)

`scripts/kind-check.sh` fails CI when a file of that core names a kind,
a resource, or a holder field. `devbox run verify` runs it.

## The proof

MemberPolicy was added in commit 2547d87. Its diff, by file:

```
deploy/solas/config/memberpolicy.yaml              11
deploy/solas/rbac.yaml                              3
pkg/apis/solas/register.go                          2
pkg/apis/solas/types.go                            34
pkg/apis/solas/v1alpha1/register.go                 2
pkg/apis/solas/v1alpha1/types.go                   41
pkg/apis/solas/validation/validation.go            18
pkg/apiserver/apiserver.go                          6
pkg/registry/solas/memberpolicy/storage.go        123
pkg/registry/solas/memberpolicy/storage_test.go    46
pkg/apis/**/zz_generated.*.go, pkg/generated/**   generated
```

`git show --stat 2547d87 -- pkg/storage pkg/apiserver/storage.go` is
empty. The six lines in `pkg/apiserver/apiserver.go` are the `NewREST`
call and the map entry. The same holds for MemberUsage and StoreFormat,
which came with the limits of ADR 0021 and the version skew of ADR 0013.

## The cost

Each kind is a release of solas, rolled out to every member cluster, as
with any aggregated API server. Validation is Go, not CEL, and one
version is served. A kind that lives in one cluster only, such as
DeviceClaim, is a CRD and pays none of this (ADR 0002). ADR 0023 says
which data belongs in the shared store at all: only what needs one
order across clusters.
