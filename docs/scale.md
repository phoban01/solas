# Scale: throughput and conflicts at fleet size

Issue #190 asks where the single counter item of a resource (ADR 0003)
limits binds, and what a watch costs per cluster. `solas-bench` measures
it, and `devbox run scale` runs it on both local stores. This page has
the local results and what they mean. A run on a real DynamoDB table
still needs an AWS account; the last section has the command.

## Method

`cmd/solas-bench` runs N simulated member clusters against one storage
URL. Each simulated member has its own REST store of Devices, with its
own watch cache and poller, as each member cluster has its own API
server. Each member runs K claims. A claim does what the controller does
for a bind, spec 6.3: a consistent list of the devices, a status update
that sets `claimRef` with the resource version from the list, then a
status update that clears it. It counts a `409 Conflict` on the bind as a
conflict and lists again. Each member also runs one watch from the
version of its first list, and records the time from each write to the
event that reports it.

The columns:

| Column | Meaning |
|--------|---------|
| binds/s | successful binds per second over all members |
| conflict% | binds that failed with `409 Conflict`, as a share of all bind attempts |
| bind p50 ms, bind p99 ms | latency of a successful bind, from the request to the answer |
| list p50 ms, list p99 ms | latency of the consistent list before a bind attempt |
| sync ms | the longest time a member needed to serve the whole pool from its cache before the run, as an informer sync would |
| lag p50 ms, lag p99 ms | time from a write to the event at a watcher, over every watcher |
| reads/member/s | DynamoDB `GetItem` and `Query` calls per member per second, lists and polls included; not measured on etcd |
| errors | requests that failed with another error |

The pool has N times K devices, so a claim finds a free device most of
the time, and the conflicts come from two claims that pick the same
device from the same list.

## Local results

One VM with 4 cores and 12 GB, on 2026-11-09. The bench, both stores,
and all N watch caches ran on the same VM, so the numbers show the
protocol and the local stores together, not DynamoDB. dynamodb-local is a
Java emulator on SQLite; it does not throttle and it is far slower than
DynamoDB. Each run lasted 10 seconds with 8 claims per member.

| store | members | devices | binds/s | conflict% | bind p50 ms | bind p99 ms | lag p50 ms | lag p99 ms | reads/member/s | errors |
|-------|--------:|--------:|--------:|----------:|------------:|------------:|-----------:|-----------:|---------------:|-------:|
| dynamodb-local | 2 | 16 | 188.8 | 13.5 | 5.5 | 83.2 | 106.5 | 202.4 | 1970.4 | 10 |
| dynamodb-local | 8 | 64 | 38.7 | 53.3 | 153.9 | 1359.8 | 106.2 | 201.0 | 220.2 | 0 |
| dynamodb-local | 32 | 256 | 25.7 | 48.2 | 1822.9 | 10628.9 | 160.2 | 933.1 | 50.9 | 0 |
| etcd | 2 | 16 | 3862.6 | 29.5 | 0.5 | 5.1 | 0.1 | 2.3 | - | 0 |
| etcd | 8 | 64 | 3313.1 | 32.3 | 2.6 | 16.7 | 1.0 | 9.3 | - | 0 |
| etcd | 32 | 256 | 1169.6 | 36.2 | 20.0 | 81.0 | 30.4 | 501.1 | - | 0 |

## What the numbers say

**One counter item serializes every write of a resource.** On
dynamodb-local the binds per second fall from 189 at 2 members to 26 at
32 members, and the p99 of a bind rises to over 10 seconds. Every bind
and every release is one `TransactWriteItems` on the counter item of the
Device resource. When two writes race, DynamoDB cancels one with a
`TransactionConflict`, and the store retries it with a backoff
(`pkg/storage/dynamo/commit.go`). With 256 claims the retries dominate.
dynamodb-local makes this worse than DynamoDB would, because it runs the
transactions on one SQLite database. On DynamoDB, one item sustains
about 1000 writes per second (ADR 0003); the shape of the curve holds,
the knee is further out.

**The conflict rate shows the list-then-write race, not the counter.** A
conflict is a `409` on the bind: two claims listed the same free device
and one lost. The rate grows with the number of claims per free device,
and it is the same shape on both stores. The controller handles it as
spec 6.3 says: list again.

**A consistent list starves under sustained writes on DynamoDB.** The 10
errors in the first row are `429 Too Many Requests` from the list: it
reads the counter before and after the query, and retries when the two
differ, spec 2.4. With 16 claims that write without pause, the counter
changed in every one of the retries. A real fleet does not write without
pause, but a fleet near the counter limit would. Issue #233 tracks it.

**The watch lag is the poll interval.** On dynamodb-local the p50 lag is
about 100 ms and the p99 about 200 ms, half and all of the poll interval
of 200 ms, spec 4.2. On etcd the watch is a push, and the lag is the time
the process needs to dispatch the event. The p99 of 500 ms at 32 members
on etcd is the bench process itself: 32 watch caches and 256 claims on 4
cores.

**Reads per member fall with load, because they are mostly lists.** Each
bind attempt lists first, and a consistent list from the cache reads the
counter once. The polls add 5 reads per second per member, and more when
there are events to fetch. At 2 members a member made about 1970 reads
per second, almost all for the lists of its 8 claims. At 32 members the
same 8 claims waited on the counter, so they listed less. On DynamoDB,
the reads of a fleet are the sum over members of the binds it attempts
plus 5 per second per member for the polls.

**etcd is faster here, but the comparison is local.** etcd ran as one
process on the same VM, with no fsync (`--unsafe-no-fsync`). Its revision
is one sequence too, and it serializes writes in the same way. Its
figures show what the protocol and the bench can do when the store is
not the limit.

## Where the counter becomes the limit

A bind and a release are two writes. A member that binds and releases
once per device per hour makes `2 * devices / 3600` writes per second.
This section once put the ceiling at the 1000 writes per second of one
DynamoDB item, 1.8 million such binds and releases per hour. The rows
of 2026-11-10 measured the item at 970 updates per second (#290), but
the transaction that holds it commits about 30 times per second, with
about 3 writes each, so the write path of today gives a tenth of that:
"Fleet size" below has the fleet it supports, from the rows. The
counter is not the limit for a fleet of physical machines. It is the
limit for a workload that binds and releases devices many times per
second, as the bench does.

Two options, when a fleet needs more:

- **A counter per shard.** Split the Device resource into shards, each
  with its own counter item and event log, by a hash of the device name.
  Watch merges the shards. The resource version becomes a vector or a
  shard-prefixed value, which Kubernetes clients treat as opaque, but
  list-then-watch needs a version per shard. ADR 0003 chose one counter
  per resource because this is enough for the project.
- **A resource per device class.** Devices of different kinds that no
  claim selects together can live in separate resources, each with its
  own counter. This needs no protocol change, only more kinds.

## Fleet size

The rows of [benchmarks.md](benchmarks.md) give the write ceiling of one
resource. This section turns it into the fleet one store supports. Each
resource has its own counter item (ADR 0003), so the limit of the
Member resource and the limit of the Device resource add; they do not
share. Every number names its row.

**What one resource commits.** The store commits the writes of a server
in one transaction, spec 2.3. A bind of the bench is two writes, the
bind and its release, so a row's transactions per second are twice its
binds per second divided by its writes per transaction. From the VM at
67 ms (row 93bbdf5, 8 members) that is 19 transactions per second; from
an instance in the region (row 410a4f3, 8 members), 39. The round trip
moves the number by two, not by thirty, because the counter item in the
transaction serializes the members, "A run on DynamoDB" above.

**Members.** A member renews its Member every `D / 3`, spec 7.3, one
write in a transaction of its own, because a member is its own server
and the batching of spec 2.3 joins only the writes of one server. The
Member resource takes the transactions of one resource, so the members
per store are the transactions per second times `D / 3`. At the default
`D` of 30 seconds, the renews of 190 members fill the resource from
the VM, and 390 from the region. A `D` of 5 minutes gives ten times
that. A fleet above it renews late, and a sweeper then clears members
that are alive, spec 8.1; the lease margin of spec 7.4 is the room it
has.

**Devices.** The Device resource takes one write per bind and one per
release. The binds per minute at the ceiling are the row's binds per
second times 60. A pool of `N` devices that each change hands once per
hold time `H` needs `N / H` binds per second, so the pool a store
supports at a mean hold time is the binds per second times `H`. The
pool itself is bounded by the list cost, "10,000 devices" below, not by
this; this bounds how often the pool changes hands.

| Fleet | DynamoDB from the VM, 67 ms | DynamoDB in the region, 2 ms | Postgres from the VM, 20 ms | Row |
|-------|----------------------------:|-----------------------------:|----------------------------:|-----|
| Transactions per second, one resource | 19 | 39 | 290 | 93bbdf5 and 410a4f3, 8 members, 64 devices; d745a8c, 32 members, 256 devices |
| Members per store, `D` 30 s | 190 | 390 | 2,900 | the same rows, times `D / 3` |
| Members per store, `D` 5 min | 1,900 | 3,900 | 29,000 | the same rows, times `D / 3` |
| Binds per minute | 1,600 | 3,500 | 8,900 | 93bbdf5 and 410a4f3, 8 members; d745a8c, 8 members |
| Binds per minute, 32 members | 1,600 | 2,800 | 24,000 | 93bbdf5 and 3d388b1, 32 members; d745a8c, 32 members |
| Binds per minute, 128 members | | 1,300 | 8,600 | 3d388b1 and d745a8c, 128 members, 1,024 devices, at 16 ACUs |
| Devices that change hands once an hour | 97,000 | 208,000 | 1.45 million | 93bbdf5 and 410a4f3, 8 members, times 3,600; d745a8c, 32 members |
| Devices that change hands once a day | 2.3 million | 5.0 million | 35 million | the same rows, times 86,400 |

The VM rows are 20 to 67 ms from the table, and the in-region rows 2
ms; the 128 member row is the one with 1,024 devices. The 10,000 device
rows of the region give the same binds per second at 32 members, 48 to
58, so a large pool does not move the ceiling. These are the numbers
of the write path of today; ADR 0025 proposes a write path with the
counter outside the transaction, and the item rows of #290 put its
ceiling at about five times these.

**Postgres.** The Postgres column is the 32 member row on Aurora
Serverless v2 from the VM (benchmarks.md, "Scale on Aurora Serverless
v2"): 404 binds per second, 290 transactions per second with 2.8
writes each, on a cluster at its maximum of 8 ACUs. The row lock
measured alone commits 553 to 602 times per second ("Postgres"), so
the resource has about twice that left, with more members or a larger
cluster; the 128 member row at 16 ACUs is bound by the VM that runs
the 128 servers. A bind pays about five round trips, so a single
member binds at the pace of the round trip, 100 ms at 20 ms, and the
rate grows with the members: 42 at 2, 148 at 8, 404 at 32. The
Member resource takes the same transactions per second, so the
members per store at the default lease are about 2,900, and the
target of several thousand clusters is a lease of a few minutes or a
second resource, not a store change. The connection count is the
other limit: 128 members held 520 connections, and a fleet of
thousands needs RDS Proxy, ADR 0026.

## A run on DynamoDB

The rows are in [benchmarks.md](benchmarks.md), "DynamoDB": the VM
against a table in three regions, and, under "In the table's region",
an instance 2 ms from the table at 2 to 128 members. What they say:

**The counter item in the transaction sets the ceiling, at about 50
binds per second per resource, from any distance.** From the VM at
67 ms the Device resource gave 27 binds per second at 8 and at 32
members; from an instance in the region, 58 at 8 members, 47 at 32,
and 22 at 128. A bind from 2 ms away took 645 ms at the median at 32
members, hundreds of round trips: the transactions of all members
queue on the counter item, DynamoDB cancels the ones that raced, and
each retry reads the counter again. More members means more cancelled
transactions, so 128 members get less than 32. ADR 0025 is the proposal
that takes the counter out of the transaction; issue #290 measures what
one item sustains without it.

**At one distance, etcd gives twenty times the binds.** An etcd on the
same instance gave 561 binds per second at 8 members and 696 at 32,
against 28 and 27 on DynamoDB from the same VM in the same minutes
(benchmarks.md, "Same round trip"). A bind on etcd takes one round trip,
and the writes of all members are in flight at once.

The commands: `scripts/aws-bench.sh up <region>` puts the cost alarm
and prints the storage URL; `SOLAS_DYNAMODB_URL=<that url>
SCALE_STORES=dynamodb devbox run scale` runs the series from the VM;
`scripts/aws-bench.sh ec2 up|scale|down <region>` runs it from an
instance in the region, with an etcd on the instance for the rows at
one distance; `scripts/aws-bench.sh down <region>` deletes the table.

## 10,000 devices

`SCALE_DEVICES=10000 devbox run scale` fills the pool with 10,000
devices. The rows are in [benchmarks.md](benchmarks.md). What they say:

**The watch cache syncs 10,000 devices in about 200 ms on both stores.**
A member that starts reads the whole pool from the store into its cache
in that time. That is the informer sync of a controller.

**A consistent list comes from the cache on etcd and from the table on
DynamoDB.** On etcd the list of 10,000 devices takes 7 ms at the median.
On dynamodb-local it takes 1.6 seconds at the median and 9 seconds at
p99, and the bind rate falls to 5 per second with 16 claims. The cause
is in the apiserver library, not in the store: `delegator.
ConsistentReadSupported` asks the etcd feature checker whether the store
supports watch progress requests, and the checker learns that only from
an etcd client. The DynamoDB store answers progress requests (spec 4.2),
but nothing tells the checker, so the cache delegates every consistent
list to the store, which queries all 10,000 items. Issue #237 was to
register the support for the DynamoDB store; the next paragraph has the
result. With it, a consistent list
reads the counter once, waits until the cache has reached it, and serves
from memory, as etcd does. It also removes most of the `429` errors of
issue #233, because the list no longer reads the counter twice around a
query.

**Fixed in commit 5ce2cfe.** The server now tells the feature checker
that the DynamoDB store answers progress requests, and the cache serves
every consistent list. At 10,000 devices the list p50 on dynamodb-local
went from 1619 ms to 53 ms and the binds per second from 4.9 to 152.5;
the rows are in [benchmarks.md](benchmarks.md). The `429` errors of
issue #233 went with it, because the bench's lists no longer reach the
table. The raw list of the store still reads the counter twice around
the query, so #233 stays open for the lists that do reach it: the first
fill of a cache, and a list with an exact resource version.

**The trade at small pools.** A consistent list from the cache reads
the counter once, then waits until the cache has reached that value.
The library asks the store for progress every 100 ms while a list
waits, and the store polls on each request, so a list waits up to
100 ms and 50 ms at the median. At 16 devices the list p50 rose from
9 ms to 61 ms and the binds per second of 16 claims in a tight loop
fell from 206 to 130, while the reads per member per second fell from
2116 to 626. A controller lists once per bind, so the read saving
counts and the 50 ms does not; a pool of thousands of devices has no
other option.
**The counter still bounds the binds.** With 8 members and 64 claims on
10,000 devices, the conflict rate is below 1 percent, because the claims
rarely pick the same device. The bind p50 of 2.5 seconds on
dynamodb-local is the counter item and the slow lists in front of it,
not the device race.
