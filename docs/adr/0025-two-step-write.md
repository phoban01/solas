# ADR 0025: Versions issued outside the transaction

Status: proposed (2026-11-10), issue #267.

## Context

Every write of a resource is one `TransactWriteItems` call that moves
the counter item and writes the object and its event, spec 2.3. The
rows of docs/benchmarks.md, "DynamoDB", put the ceiling of a resource at
about 27 binds per second at 8 and at 32 members, on a table 67 ms away.
That is about 18 transactions per second: one per round trip. Every
transaction of every member touches the counter item, DynamoDB lets one
of them through at a time, and the losers read the counter again and
retry. The batched writes of ADR 0003 gave each server more writes per
transaction, and the counter guess took the read before it away; the
rows after both show the same ceiling, because the counter item in the
transaction is what serializes the members. ADR 0003 assumed about 1000
writes per second from the counter; the rows of #290 measured 970, and the
counter gives that only when no transaction holds it.

## Decision

A write takes two steps.

1. **Issue.** The server moves the counter with one `UpdateItem`:
   `ADD n :k`, on condition that the counter has no `sealed` attribute,
   and reads the new value. The `k` writes of the batch get the versions
   `n + 1` to `n + k`. An update of one item does not conflict with
   another update of it; DynamoDB applies them in some order, each
   with its own result.
2. **Commit.** The server writes the objects and the events in one
   `TransactWriteItems` call, with the conditions of spec 2.3 on the
   objects and on the events, and no counter item. Transactions on
   different objects do not wait for each other.

A write whose object condition fails gets `409`, as before. Its version
is used: the retry of the batch carries a `NOOP` event at that version,
and the other writes keep theirs. So the log stays dense without a new
issue. A watch does not deliver a `NOOP` event, as it does not deliver
`INIT`.

A version is **settled** when the log holds an event at it. The store
has a settle time `F`, a setting with a default of 5 seconds. A commit
lands within `F` of its issue, or the committer abandons those versions
and issues again. A reader that finds a version missing for `F` fences
it: it puts a `NOOP` event at that version. A commit that lands after
the fence fails on the condition of its event put, and the committer
issues again, so no write is lost to a fence.

The readers change with it.

- A poll, spec 4.2, delivers the events up to the first missing version
  and keeps the rest for the next poll. A version missing above the
  version at which the poller started is a hole in flight: the poller
  waits, and fences it after `F`. A version missing at or below the
  start, still missing after `F`, ends the watch with `410 Gone`, as a
  gap does today: it may be an expired event.
- A list, spec 2.4, waits until the log is settled to the counter value
  it read, and then queries the objects. The API server already serves
  a consistent list from the watch cache once the cache has reached the
  counter, spec 4.2; the cache reaches it when its poller has settled
  the log to it.
- A seal, spec 12.1, is checked at the issue. A write issued before the
  seal can commit for `F` after it, so the migration tool waits `F`
  after the seal before it reads.

The rules of spec 2.5 hold: a conditional write applies only on the
version the writer read, the versions of a resource strictly increase in
log order, a list is a snapshot at its version because the log is
settled to it before the query, and a watch delivers every change in
order or fails with `410 Gone`.

## Consequences

- The ceiling of a resource moves from one transaction per round trip to
  the write units of its two partitions. The rows of #290
  (`docs/benchmarks.md`, "One item and one partition") measure what
  bounds it: one item takes about 970 updates per second from the VM,
  each able to issue up to 25 versions, so the counter is not the
  limit; a transaction that shares no item with another runs at over
  1,200 per second across partition keys, but at about 245 per second
  under one partition key, where a two-item transaction costs 4 of the
  1,000 write units a partition takes per second. Spec 2.2 puts the
  object items of a resource under one partition key and its event
  items under another, so a commit of one object item and one event
  item under 1 KB each gives about 500 commits per second per resource,
  about 250 binds per second against the 50 of the write path of
  today. The ceiling moves by about five times, not twenty, unless the
  object items get more partition keys, which is a change to spec 2.2
  and to every list.
- A watch sees a write when its commit lands, and the poll delivers it
  once every lower version has landed. The lag grows by the time of one
  commit, about a round trip, when writes overlap.
- A server that dies between its issue and its commit leaves a hole. The
  readers wait `F` for it, then fence it. A new watch that starts below
  such a hole gets `410 Gone` and lists again. Nothing is lost: the
  write that died was never acknowledged.
- The event log holds `NOOP` events, one for each `409` and each fence.
  A watch skips them. The audit of `pkg/audit` skips them too.
- A migration waits `F` longer after the seal.
- The store model in `quint/store.qnt` gets the issue, the commit, the
  failed commit, and the fence, with the settle wait as the precondition
  of a list.

## Rejected alternatives

- **A counter per shard of a resource.** Lifts the ceiling by the number
  of shards, but a list then has one version per shard, and Kubernetes
  needs one resource version for a list and a watch. No encoding of
  several counters into one number keeps the order of spec 2.5.
- **DynamoDB Streams as the event log.** Delivers each change without a
  poll, but across shards in no single order, so the versions would come
  from a counter all the same.
- **Keep the transaction and tune the retry.** The rows say the loser of
  a conflict pays a round trip whatever it does; the serialization is in
  DynamoDB.
