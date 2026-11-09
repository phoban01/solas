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
At the 1000 writes per second of one DynamoDB item, the Device resource
sustains about 1.8 million such binds and releases per hour, or 1000
devices that each change hands twice a second. The counter is not the
limit for a fleet of physical machines. It is the limit for a workload
that binds and releases devices many times per second, as the bench
does.

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

## A run on DynamoDB

The local numbers do not show the limits of DynamoDB. One run on a real
table needs an AWS account with a table in one region:

```sh
AWS_PROFILE=<profile> go run ./cmd/solas-bench \
  --storage-url 'dynamodb://solas-bench?region=<region>&create-table=true' \
  --members 8 --claims 8 --duration 30s
```

The tool creates the table when `create-table=true`, deletes every
Device under its prefix when it ends, and leaves the table. Run it at 2,
8, 32, and 128 members, and add the rows here with the region and the
date. Expect the binds per second to rise with members until the counter
item reaches its write limit, and the p99 to rise from there.

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
list to the store, which queries all 10,000 items. Issue #237 is to
register the support for the DynamoDB store. With it, a consistent list
reads the counter once, waits until the cache has reached it, and serves
from memory, as etcd does. It also removes most of the `429` errors of
issue #233, because the list no longer reads the counter twice around a
query.

**The counter still bounds the binds.** With 8 members and 64 claims on
10,000 devices, the conflict rate is below 1 percent, because the claims
rarely pick the same device. The bind p50 of 2.5 seconds on
dynamodb-local is the counter item and the slow lists in front of it,
not the device race.
