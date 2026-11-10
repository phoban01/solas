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

A single counter would give one order across kinds, and a common
snapshot to a reader of two kinds, at the cost of one item for every
write of the store. The scale rows of
`docs/benchmarks.md` show what that item costs on dynamodb-local; a
counter per resource spreads the load, and nothing in the protocol
asks for more.

What the argument leaves unsaid, from a reading of it on 2026-11-10:

- A reader of two kinds gets no common snapshot. A list of Devices and
  a list of Members are two snapshots at two versions, and the reader
  cannot tell a Member that is gone from a Member that the second list
  started too early to see. The sweeper's rule is the model: list the
  kind that holds the reference first, then the kind it names, and
  compare by UID, spec 8.3. A dashboard or a controller that reads two
  kinds does the same.
- The usage set rule is one way. Every held device has an entry in the
  usage set of its member; an entry with no device is allowed and
  expires, spec 15.2. The entry is a reservation, not an order.
  `withinLimit` is checked in `quint/limits.qnt`, apart from the
  invariants of `quint/solas.qnt`.
- The rule for a new kind is a convention. `scripts/kind-check.sh`
  checks that the core names no kind; it does not check how a kind
  reads. `docs/adding-a-kind.md` states the rule, and a kind that reads
  another kind names the Quint invariant that checks it.
- The argument rests on the store contract of spec 2.5. `quint/store.qnt`
  proves the contract for the store, and `quint/solas.qnt` assumes it.
  ADR 0025 changes how a list reaches its snapshot, so issue #281
  proves the contract again for that write path, with a negative model
  where the list skips the settle wait.

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

What it gives, measured on an eu-west-1 table (docs/benchmarks.md,
"After the batched writes"): 11.1 to 28.4 binds per second at 8 members
with 2.8 writes per transaction, 12.5 to 39.1 at 8 members and 10,000
devices with 7 writes per transaction, and the same rate held for five
minutes. The writers of one server share a round trip, so a
server's throughput grows with its concurrency, up to 25 writes per
transaction. What it does not give: two servers still race on the
counter, and a server far from the table still waits two round trips
per batch. A counter per shard of a resource would lift that; issue #267
holds it. The claim controller runs one worker, so a cluster sends one
bind at a time and gains nothing from the batch until it runs several;
issue #268 is that.

## Amendment: the counter guess (2026-11-10)

After the batched writes, a transaction still cost two round trips: a
read of the counter, then the `TransactWriteItems` call conditional on
the value read. The rows of docs/benchmarks.md, "Bind burst", put a
bind's status write at 99 to 155 ms on a table 40 ms away, with the
counter read as one of its two trips.

Decision: the first attempt of a batch uses the last counter value that
this server read or issued, spec 2.3, without a read. Every counter
read raises that value, and the watchers poll the counter every 200 ms,
so the value is at most a poll interval old. A guess is never above the
counter, so a wrong one fails only the counter condition, which also
carries the seal check, and the retry reads the counter as before.
After a miss the committer reads for the next batch, then for two, then
four, up to 64, until a guess hits again. The rules of this ADR hold as
before: the condition on the counter is the same, only the source of
the expected value changes, and a failed transaction changes nothing.
The Quint store model has `writeGuess`; `TestCommitCounterGuess` counts
the reads of a hit, a miss, and the hold-off.

What it gives, measured on the same table: the status write at 62 ms
with four claim workers and 46 ms with one, and the burst of 50 claims
at 2.4 seconds with four workers, 21 binds per second, from 4.5. What it
does not give: the serialization of the transactions on the counter
item stays, so the ceiling per resource that issue #267 measures stays.
