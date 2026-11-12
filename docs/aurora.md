# Run the store on Aurora Serverless v2

The Postgres store (ADR 0026, spec 2.8) runs on any Postgres. Aurora
Serverless v2 PostgreSQL is the managed target: nothing to run, a
capacity that follows the load, and a failover across zones. This page
has the settings and what the rows say about them. The numbers are in
[benchmarks.md](benchmarks.md), "Postgres" and "Scale on Aurora
Serverless v2", and the fleet they support in [scale.md](scale.md),
"Fleet size".

## The cluster

`scripts/aws-bench.sh pg up <region>` creates the cluster of the rows
and is the shortest statement of the settings:

- Engine `aurora-postgresql`, the latest version that `db.serverless`
  offers; the rows ran on 18.6.
- One instance of class `db.serverless` in the cluster. A second one in
  another zone is the failover; the store does not need it to run.
- `--serverless-v2-scaling-configuration MinCapacity=<min>,MaxCapacity=<max>`.
  One ACU is about 2 GiB of memory with the CPU to match.
- A security group that lets the member clusters reach port 5432, and
  nothing else.

The storage URL is
`postgres://<cluster endpoint>:5432?sslmode=require&create-tables=true`.
The cluster endpoint is the writer; every read of the store goes to it,
spec 2.8. Do not use the reader endpoint: a reader lags the writer, so
a read from it can miss a write that completed before it started.
`create-tables=true` makes the first server create the schema and the
tables, spec 2.6; the rest find them there.

## Capacity

The rows of 2026-11-12 ran from the development VM, 20 ms from the
region:

| Members | Binds per second | Capacity the cluster took |
|--------:|-----------------:|---------------------------|
| 2 | 42 | 2.5 ACUs |
| 8 | 148 | 5.5 ACUs |
| 32 | 404 | 8 ACUs, the maximum of that run, CPU at 100 percent |
| 128 | 143 | 15.5 ACUs with a maximum of 16, CPU at 97 percent |

So a mesh of a few members runs at the minimum, a mesh of tens of
members that bind at the rate of the bench takes 8 ACUs, and the
maximum should be 16 or more for a mesh of a hundred members. The
cluster scales up in seconds and down in minutes; a maximum that is
too low shows as the CPU at 100 percent and a bind p99 of seconds, as
the 128 member row at 8 ACUs shows.

The ceiling of one resource is the lock queue of its counter row, 553
to 602 commits per second measured alone ("Postgres", the commit rows),
not the capacity: more ACUs do not move it. A fleet that needs more
splits its devices over resources, [scale.md](scale.md), "Where the
counter becomes the limit".

## Scale to zero

`MinCapacity=0` pauses the cluster after a quiet period,
`SecondsUntilAutoPause`, 300 seconds by default. The first connection
resumes it, which takes about 15 seconds. A member cluster that starts
against a paused store serves its API at once and reports ready, ADR
0024; its writes fail with `503` until the store answers, and its
claims stay `Suspended`, spec 16.2, and go back to `Bound` when it
does. A mesh whose members renew every `D / 3` never pauses the store,
so scale to zero serves a mesh that is switched off at night, not a
running one. The rows ran with a minimum of 2.

## Connections

Each server holds a pool of 32 connections: one for the writes of each
resource, one for the poller of each watch, and the rest for reads.
The cluster's `max_connections` follows its maximum capacity, about
2,000 at 8 ACUs and 3,000 at 16. The 128 member row held 520
connections. A fleet of a few hundred members fits; a fleet of
thousands puts RDS Proxy in front of the cluster, which multiplexes the
transactions of many servers over fewer connections. A `NOTIFY` watch,
#319, needs a connection of its own per server that the proxy cannot
carry; the deployment with the proxy keeps the polling watch.

## Credentials

The server reads `PGUSER` and `PGPASSWORD` from its environment, spec
2.6; the Deployment takes them from the keys `pgUser` and `pgPassword`
of the Secret `solas-storage`. On AWS, keep the master password in
Secrets Manager with rotation on, and fill the Secret of each member
cluster from it, as the AWS keys of a DynamoDB store are filled today.
IAM authentication for Postgres gives a token that lasts 15 minutes,
which the pool cannot refresh; it is not supported.

## Events and retention

Each write adds a row to the `events` table, and each server deletes
the rows older than the retention time, `--event-retention`, one hour
by default, at a random interval between half of it and all of it,
spec 2.8. The table holds about one hour of writes times the object
size; at 400 binds per second and 2 KB per event, about 6 GB, which
the storage of the cluster grows into and shrinks from. A watch from
a version older than the retention gets `410 Gone` and lists again.

## Backups and a restore

Aurora backs the cluster up on its own. A restore from a backup puts
back old resource versions and old fencing tokens, so after a restore
and before any server starts, move the store to a new epoch:

```sh
PGUSER=solas PGPASSWORD=... solas restore-epoch \
  --storage-url 'postgres://<restored endpoint>:5432?sslmode=require'
```

The tool sets the epoch to the time in Unix seconds and raises the
counter of each resource above every version issued before, spec 13.

## Cost

The rows cost under three dollars: about an hour at 2 to 16 ACUs,
with the storage and the I/O of the runs. A cluster at a minimum of 2
ACUs costs about 0.25 dollars an hour in eu-west-1 before storage; at
8 ACUs under load, about one dollar an hour.
