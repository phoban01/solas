# ADR 0026: A Postgres store, on Aurora Serverless v2

Status: accepted (2026-11-11)

## Context

The rows of docs/benchmarks.md put the write path on DynamoDB at about
30 transactions and 50 binds per second per resource, from any
distance, with a bind of 645 ms at the median at 32 members and 11
seconds at 128 (docs/scale.md, "A run on DynamoDB"). The cause is the
counter item in the transaction, spec 2.3: DynamoDB cancels the
transactions that race on it, and the losers try again. The rows of
#290 show that the two-step write of ADR 0025 lifts the ceiling by
about five times, to the write units of one partition, and gives up the
order property that the transaction gives today.

The target is 100,000 devices and several thousand member clusters.
docs/scale.md, "Fleet size", turns the rows into what one store
supports: 190 to 390 members at the default lease, and a watch that
polls two partition keys at the edge of their read limit at a few
thousand members. DynamoDB reaches the target only after three
protocol changes.

The property the protocol needs from a store is one order of writes per
resource, spec 2.5: a version for each write, a consistent list at a
version, and a watch from a version that delivers every change in
order. A store either hands out that order, or the server builds it on
one hot item. DynamoDB, Aurora DSQL, Keyspaces, and S3 have no
sequencer, and their optimistic concurrency cancels the losers. etcd,
Postgres, and a single Valkey shard have one, and queue the writers.
etcd on the same instance as the table gave twenty times the binds
(docs/benchmarks.md, "Same round trip"), but someone runs etcd.

Aurora Serverless v2 PostgreSQL is Postgres with nothing to run: it
scales from zero to a set maximum, patches itself, and fails over
across zones. A row lock on a counter row queues writers in order and
each holds it for the time of its transaction, milliseconds, so the
ceiling is the lock hold time and not the number of cancelled
transactions, and the order property of today is kept.

## Decision

A third store behind the storage URL of ADR 0014, in
`pkg/storage/postgres`, with the same contract as the other two
(docs/store-contract.md) and the same conformance tests and
model-based traces. The protocol does not change.

**Schema.** Three tables, in a schema named by the storage prefix.

- `counters(resource PRIMARY KEY, n, sealed)`: one row per
  resource, the counter item of spec 3.1.
- `objects(resource, key, rv, value, PRIMARY KEY (resource, key))`: the
  object items of spec 2.2, the value encoded as today.
- `events(resource, rv, type, key, value, expires, PRIMARY KEY
  (resource, rv))`: the event log of spec 4.1.

**Write.** The batched write of spec 2.3 is one transaction:

1. `UPDATE counters SET n = n + k WHERE resource = $1 AND NOT sealed
   RETURNING n`. The row lock queues the writers of the resource; a
   sealed counter returns no row and the write fails as sealed, spec
   12.1.
2. For each write of the batch, a conditional statement on its object:
   an insert that fails when the key exists, an update or a delete
   with `WHERE rv = $expected`. A statement that touches no row is a
   conflict: the transaction rolls back, the counter update with it,
   and the writer gets `409` with the versions unused. No version is
   lost.
3. One insert per write into `events`.
4. `COMMIT`.

The lock is held until the commit, so the writer of version `n + 1`
takes the lock after the writer of version `n` committed. The commit
order of a resource is its version order, and the log has no hole: the
property that ADR 0025 gives up, kept at no cost.

**Read.** A get is one select. A list reads the counter and the objects
in one snapshot, a single `REPEATABLE READ` transaction, so the list is
a snapshot at the version it returns, spec 2.4, with no second read of
the counter: a counter value visible in a snapshot has every version
up to it committed, because of the lock order above.

**Watch.** The poll of spec 4.2, one select of the events above the
last version delivered, in version order, and the gap rule of spec
4.3. The first version polls, as the DynamoDB store does; the rows
show the polls of 3,000 members take the CPU of the cluster, so the
second version wakes the pollers with a `NOTIFY` from the commit,
behind the same interface, and polls only as the fallback. `LISTEN`
needs a connection of its own per server, which a pooler that
multiplexes transactions cannot carry. Expired events are deleted by
each server at a
jittered interval of the retention time, since Postgres has no TTL;
spec 4.1's rule that nothing depends on the time of that delete holds
as it is.

**Seal, epoch, migrate.** `sealed` lives on the counter row. The epoch
lives in the StoreFormat object, as on the other stores. A restore
raises `n` of each counter row, as on DynamoDB. So `solas migrate` and
`solas restore` work as they do today, ADR 0015 and ADR 0018.

**URL.** `postgres://<host>:<port>?database=<name>&sslmode=<mode>&create-tables=true`.
The rules of spec 2.6 hold: no path, no credentials in the URL. The
user and the password come from `PGUSER` and `PGPASSWORD`, which the
driver reads, as the AWS credentials come from the environment today.

**Connections.** A server keeps a small pool: one connection for the
batcher, one per watch poller, and a few for reads. Aurora's connection
limit grows with its capacity, about 1,800 at 8 ACUs, so a fleet of
thousands of members needs RDS Proxy in front of the cluster, which
multiplexes transactions. The bench measures 1,000 pollers against the
cluster directly; the row says where the proxy becomes necessary.

**Managed target.** Aurora Serverless v2, with scale to zero on. A
paused cluster resumes on the first connection in about 15 seconds;
ADR 0024 covers a server whose store is away. Any Postgres works, so
the tests and the demo run on a Postgres container, as they run on
dynamodb-local for DynamoDB.

## Rows

The rows came first, with the bench alone and no store code, as #290
did for DynamoDB: `solas-bench --mode counter|commit|poll` on a
`postgres://` URL, `scripts/aws-bench.sh pg up|item|down <region>`,
on 2026-11-11 against Aurora Serverless v2 PostgreSQL 18.6 in
eu-west-1 from the VM, 20 ms away. docs/benchmarks.md, "Postgres", has
them all. What they say:

- A commit with the counter lock, as one function call, costs one
  round trip at one writer and reaches 553 to 602 per second at 16
  writers in flight, against 30 on DynamoDB from the same VM and 561
  binds per second on etcd at the same distance. The lock keeps the
  order of today.
- The write must be one round trip. With the lock held across five
  statements from the VM, the rate was 9 to 13 per second at any
  number of writers. The store writes through one function.
- Past 16 writers in flight the lock queue slows, to about 340 per
  second at 64, while the same server does 2,846 transactions per
  second without the shared row. The writers in flight are the servers
  with a batch in flight, not the members.
- 1,000 pollers at 200 ms hold the interval at 16 ACUs, 4,396 polls
  per second with a p99 of 248 ms, with 254 commits per second beside
  them. 3,000 pollers connect directly and make 11,444 polls per
  second, with the cluster at its maximum of 16 ACUs and the CPU at
  100 percent, and the writers fall to 71 per second. The polls take
  the CPU, not the connections.

So the two conditions of acceptance hold: the commit row lands at the
etcd rows, and the poll row at 1,000 holds the interval. The 3,000
poller row adds a condition for a fleet of that size: the watch is
woken by `NOTIFY` rather than polled, which is the first change after
the store exists, and its row comes before a deployment at that size.

## Consequences

- The DynamoDB store stays, with its rows and its fleet size on the
  page, for a fleet inside them. ADR 0025 stays proposed and is not
  built; its rows and the reason are on the page.
- A third store means a third column in docs/store-contract.md, a
  third set of conformance runs, and the model-based traces on three
  stores. `scripts/local-stores.sh` starts a Postgres container.
- Event expiry is the server's job, not the store's. A server that is
  away for long leaves events for the others to delete.
- Thousands of members need RDS Proxy. The deployment docs say when.

## Rejected alternatives

- **Aurora DSQL.** Serverless Postgres with nothing to run, but no
  sequences by design and optimistic concurrency that aborts the
  losers: a counter row there has the shape of the counter item.
- **MemoryDB for Valkey.** A shard is an ordered executor and its
  streams are a log, faster than Postgres, but a cluster you size and
  the whole state in memory.
- **MSK Serverless.** A partition is an ordered log, but Kafka in a VPC
  with a floor of hundreds of dollars a month.
- **cask.** Per-key consensus with no global revision, by design; the
  order of spec 2.5 would be a sequencer register per resource, which
  its own design rules out, or a rewrite of the list and watch around
  per-object versions.
- **Keep DynamoDB and build ADR 0025, the lease items, and a watch
  change.** Three protocol changes for about five times the binds, and
  a polling watch at the edge of a partition limit.
