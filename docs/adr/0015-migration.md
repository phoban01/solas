# ADR 0015: Offline migration with a sealed source

Status: accepted

## Context

ADR 0014 lets an operator pick the store with one URL. A mesh that runs
on one store and wants to move to another needs its `Device` and
`Member` objects in the new store. The objects carry the safety state of
the mesh: who holds each device, the fencing token of each device, any
preemption request, and the UID of each member.

The move must not break the rule that a device has at most one holder.
The danger is a cluster that still writes to the old store after the
copy. It can bind a device that the new store shows as free, so two
claims in two clusters are in effect on one device. It can also renew
its lease in the old store, so nobody sweeps it.

## Decision

The command `solas migrate --from <url> --to <url>` moves the data in
three steps:

1. Seal the source. A sealed store rejects every write. The DynamoDB
   store seals atomically: every write already updates a counter item
   under a condition, and the seal adds "not sealed" to that condition.
2. Copy every `Device` and `Member` into the destination, at the storage
   level, so the UID, the status, and the fencing token stay the same.
   The resource versions do not move; the destination gives new ones.
   The destination must hold no `Device` and no `Member`.
3. Verify that each object in the destination equals its source,
   except for the resource version.

Then each cluster changes its storage URL and restarts. Writes stop from
the seal until a cluster runs on the destination. A cluster that cannot
renew in that time stops treating its claims as in effect, as spec 6.5
already says. When it starts on the destination, it renews its old
`Member`, which has the same UID, and its claims go back to `Bound`.

The etcd store has no counter item to seal. When the source is etcd,
the tool instead watches the `Member` objects for `D` by its own clock
and copies only when none renews and no object changes. The operator
must stop solas in every cluster first.

`solas unseal <url>` rolls back, before any cluster writes to the
destination.

## Consequences

- A planned outage of writes: from the seal until each cluster restarts.
  Each cluster should switch within `D` of the seal. After that, the
  other members sweep it, and its claims become `Lost`.
- Reads keep working during the move.
- The fencing tokens in the destination continue from the source, so a
  device never sees a token go down.

## Rejected alternatives

- Dual writes to both stores. The stores can disagree about a holder,
  and there is no single order of writes.
- A copy with no seal. A write after the copy is lost, and a cluster
  still on the source can bind a device that the destination shows as
  free.
- A copy through the API server. The status strategy sets the fencing
  token on each bind, so the copy would not keep the tokens.

## Amendment: versions never go back across a move (2026-11-10)

The copy gave each object a new resource version from the destination,
which started at 1 on a new store. A client that kept a version of the
source and watched the destination then got unrelated events, or on an
etcd destination a watch that waited for a revision that was never
reached, and nothing told it to list again. The fencing tokens were
safe, because the copy keeps them and a bind continues from them.

Decision: a move is a restore from the point of view of a client. After
the verify, the tool moves the destination to a new epoch, ADR 0018:
every counter item goes to `epoch * 2^32`, above every version of the
source, and the next bind on a free device gets a token of the new
epoch. A client with a version of the source gets `410 Gone` from the
destination and lists again. An etcd destination cannot raise its
revision from the tool, so the tool refuses one whose revision is below
the highest version of the source, and the operator raises it first
with `etcdutl snapshot restore --bump-revision`. The verify and the
unseal check ignore the epoch of the store format, which the move
raises. The model `quint/migrate.qnt` carries the version order as an
invariant, and `quint/negative/no-move-epoch.qnt` shows a copy without
the epoch move breaking it. Issue #271.
