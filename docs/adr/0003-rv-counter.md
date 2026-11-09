# ADR 0003: Resource versions come from a counter item

Status: accepted

## Context

The Kubernetes API needs a `resourceVersion` on each object. Watch and
list use it to resume. Within one resource, the versions must strictly
increase in commit order. etcd gives this through its global revision.
DynamoDB has no such counter.

## Decision

Each resource has one counter item in the table. Every write is one
`TransactWriteItems` call. The call increments the counter from `n` to
`n+1` on condition that it is still `n`, writes the object, and appends
an event with version `n+1`.

## Consequences

- Versions within a resource strictly increase and have no gaps.
- A transaction for `n+1` cannot commit before the transaction for `n`.
  So the event log is complete up to the counter value.
- All writes to one resource contend on one item. On a DynamoDB table in
  eu-west-1, 20 to 50 ms from the client, the Device resource sustained
  about 25 transactions per second, binds and releases together, in
  runs of 20 seconds and of five minutes (docs/benchmarks.md, DynamoDB,
  2026-11-09). DynamoDB serializes the transactions that touch one item,
  so the counter, not the write units of a partition, sets the limit.
  This ADR first assumed about 1000 writes per second; the measured
  figure is forty times lower, and issue #267 holds the design that
  lifts it. The original text follows.
- All writes to one resource contend on one item. This limits a resource
  to roughly 1000 writes per second. That is enough for this project.
- Versions from two resources do not compare. Kubernetes does not need
  them to.

## Rejected alternatives

- A timestamp as the version. Clocks on different servers disagree, so
  the order is not safe.
- One counter for the whole table. This makes every resource contend on
  one item, and gains nothing.
- The DynamoDB Streams sequence number. Streams are per shard and are
  not known at write time.

## Order across resources (2026-11-09)

A counter per resource gives no order between a write to a Device and a
write to a Member. A review asked whether any rule of solas needs one.
None does. Each rule that relates two resources rests on a conditional
write or on a causal order, which this section lists, with the Quint
invariant of `quint/solas.qnt` that checks it.

| Rule | How it holds without one order | Invariant |
|------|--------------------------------|-----------|
| A `claimRef` names the UID of the member's current `Member` (spec 6.3) | The bind is a conditional write on the device, and the UID is a fact of the Member that existed when the member joined. A later sweep of that Member clears the ref by its UID, so a stale UID never passes as live. | `claimMatchesDevice`, `leaseSafety` |
| A member creates its `Member` before it binds (spec 7.2) | Causal: the controller writes the Member, waits for the answer, then binds. A reader that sees the `claimRef` reads after the Member write completed, because reads are linearizable (spec 2.5). | `claimMatchesDevice` |
| The sweeper lists devices first, then members (spec 8.3) | Causal, from the rule above: a `claimRef` in the device list names a Member that existed before the member list started. The order of the two lists, not an order of versions, gives the conclusion that a missing Member is gone. | `leaseSafety`, `claimMatchesDevice`; the negative model `members-first.qnt` shows the other order fails |
| The usage set entry comes before the device write (spec 15.2) | Both writes are in one request of one server, each conditional. The entry is a reservation; the device write follows or the entry expires. No reader compares their versions. | `withinLimit` in `quint/limits.qnt` |
| A bind raises the token (spec 5.3) | One resource; the counter of that resource orders it. | `tokensUnique`, `useMatchesBinding` |

The Quint model has had one counter per resource from the start, so
these invariants already check the rules under that assumption. A new
kind that relates to another kind must do the same: a conditional write
on the object it changes, and a causal order for what it reads first.
Issue #264 gives the holder rules to any kind as a primitive.

A single counter would give one order across kinds at the cost of one
item for every write of the store. The scale rows of
`docs/benchmarks.md` show what that item costs on dynamodb-local; a
counter per resource spreads the load, and nothing in the protocol
asks for more.

## Amendment: batched writes (2026-11-09)

The rows of docs/benchmarks.md on real tables showed the Device
resource at 12 to 15 binds per second from 50 ms away and 3.5 from
140 ms away, whatever the number of writers. The ceiling moved with the
round trip, so it was the write loop: each writer read the counter and
sent its own transaction, DynamoDB cancelled all but one of the
transactions that raced on the counter item, and the losers read again.
One write got through per two round trips.

Decision: a server carries the writes of a resource that arrive together
in one transaction, spec 2.3. The counter moves from `n` to `n + k`, and
the `k` writes get `n + 1` to `n + k` in order, with an object action and
an event put each. The rules of this ADR hold as before: versions
strictly increase in commit order with no gap, and the event log is
complete up to the counter, because the batch commits or not at all.
A write whose own object condition fails leaves the batch with a
conflict; the rest try again. The Quint store model has the batched
write, and `TestCommitBatchesConcurrentWrites` shows forty concurrent
writes in fewer transactions with consecutive versions.

What it gives: the writers of one server share a round trip, so a
server's throughput grows with its concurrency, up to 25 writes per
transaction. What it does not give: two servers still race on the
counter, and a server far from the table still waits two round trips
per batch. A counter per shard of a resource would lift that; issue #267
holds it. The claim controller runs one worker, so a cluster sends one
bind at a time and gains nothing from the batch until it runs several;
issue #268 is that.
