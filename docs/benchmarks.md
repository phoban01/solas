# Benchmarks and measured runs

Every measured number of solas, with the date, the commit, and the
environment of its run. The method of each measurement is on its own
page; this page holds the results. A number without a row here is not a
measured number.

The environment of every row below, unless the row says otherwise: one
VM with 4 cores and 12 GB of memory, Docker, two k3d clusters where the
row needs clusters, dynamodb-local 3.3.1 or etcd 3.6 as the store, and
the bench or the test on the same VM. dynamodb-local is a Java emulator
on SQLite; it does not throttle and it is far slower than DynamoDB. No
row below is a DynamoDB number.

## Store outage

Method: the e2e test `TestStoreOutage` in `test/e2e/outage_test.go`,
spec 16. It pauses the store container, waits until every Bound claim is
Suspended, checks that a new claim stays Pending and that a namespace of
the host cluster can be deleted, unpauses the store, and waits until
every claim is Bound again on the same device with the same token. The
lease D is 10 seconds, the margin 1 second, the sweep every 3 seconds.

| Date | Commit | Store paused | Bound claims | Suspended after the pause | Bound again after the unpause | Namespace deleted during the pause |
|------|--------|--------------|-------------:|--------------------------:|------------------------------:|-----------------------------------|
| 2026-11-09 | bf0b95c | etcd | 6 | 41 s | 4 s | yes, in 9 s |
| 2026-11-09 | bf0b95c | dynamodb-local | 6 | 23 s | 4 s | yes, in 9 s |
| 2026-11-09 | 8a17ac0 | etcd | 6 | 9 s | 4 s | yes, in 10 s |
| 2026-11-09 | 8a17ac0 | dynamodb-local | 6 | 11 s | 4 s | yes, in 10 s |

The first two rows: the suspend took longer than D because the one
controller worker was held by a device list that waited for the store
until the request timeout of 20 seconds, and the retries of failed
reconciles backed off without a bound (#236). The last two rows: the
controllers bound each request to D / 3 and retry a failed reconcile
within D / 3, spec 16.2, so the suspend comes within D of the last
renew plus one request.

## Scale

Method: `solas-bench`, run by `devbox run scale`; [scale.md](scale.md)
explains the columns and what the numbers say. Each run lasted 10
seconds with 8 claims per member.

| Date | Commit | Store | Members | Devices | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Errors |
|------|--------|-------|--------:|--------:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|-------:|
| 2026-11-09 | e76c984 | dynamodb-local | 2 | 16 | 206.2 | 13.2 | 5.3 | 65.4 | 9.2 | 444.9 | 6.4 | 102.0 | 200.7 | 2115.6 | 16 |
| 2026-11-09 | e76c984 | dynamodb-local | 8 | 64 | 43.0 | 52.3 | 139.7 | 1229.6 | 5.4 | 83.6 | 3.9 | 99.1 | 200.2 | 250.5 | 0 |
| 2026-11-09 | e76c984 | dynamodb-local | 32 | 256 | 20.6 | 48.5 | 2080.7 | 10605.2 | 30.1 | 1550.3 | 10.9 | 121.6 | 685.5 | 57.1 | 0 |
| 2026-11-09 | e76c984 | etcd | 2 | 16 | 4030.1 | 29.6 | 0.5 | 4.8 | 0.6 | 4.9 | 108.0 | 0.1 | 2.0 | - | 0 |
| 2026-11-09 | e76c984 | etcd | 8 | 64 | 3336.2 | 32.8 | 2.6 | 16.7 | 3.4 | 18.8 | 106.4 | 1.0 | 9.4 | - | 0 |
| 2026-11-09 | e76c984 | etcd | 32 | 256 | 1117.4 | 37.4 | 18.9 | 68.4 | 46.6 | 793.6 | 110.6 | 27.2 | 638.4 | - | 0 |

These rows replace the six rows of commit 47da523, which the same runs
produced with an earlier tool that started the clock before every
worker ran and had no list or sync columns. The numbers agree within
the noise of a 10 second run. The 16 errors in the first row are `429`
from the consistent list under sustained writes, issue #233.

### 10,000 devices

The same tool with `SCALE_DEVICES=10000`, 20 seconds per run, 8 claims
per member, at 2 and 8 members. 32 members with 32 caches of 10,000
devices and 256 claims that each copy the pool on every list did not fit
the memory limit of the VM, so that row waits for a larger host.

| Date | Commit | Store | Members | Devices | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Errors |
|------|--------|-------|--------:|--------:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|-------:|
| 2026-11-09 | e76c984 | dynamodb-local | 2 | 10000 | 4.9 | 0.0 | 124.1 | 1120.3 | 1618.6 | 8889.2 | 196.2 | 156.7 | 380.3 | 110.1 | 1 |
| 2026-11-09 | e76c984 | dynamodb-local | 8 | 10000 | 7.7 | 0.6 | 2476.0 | 16023.5 | 587.5 | 3753.0 | 179.3 | 116.8 | 248.8 | 62.7 | 0 |
| 2026-11-09 | e76c984 | etcd | 2 | 10000 | 934.4 | 0.1 | 1.2 | 12.6 | 7.3 | 46.9 | 204.4 | 0.4 | 28.7 | - | 0 |
| 2026-11-09 | e76c984 | etcd | 8 | 10000 | 750.3 | 0.4 | 6.1 | 59.9 | 38.5 | 250.0 | 206.9 | 20.3 | 189.1 | - | 0 |

On etcd a consistent list of 10,000 devices takes 7 ms, because the
watch cache serves it. On dynamodb-local it takes 1.6 seconds at the
median and 9 seconds at p99, because the cache does not serve it: the
apiserver library serves a consistent list from the cache only when its
etcd feature checker reports support for watch progress requests, and
nothing reports that for the DynamoDB store. Every list then queries the
table for all 10,000 items. Issue #237 tracks it; [scale.md](scale.md)
has the detail. Both caches sync the pool in about 200 ms.


### After the list path change (#237)

Commit 5ce2cfe serves consistent lists from the watch cache on
DynamoDB, as etcd already did. The same runs again:

| Date | Commit | Store | Members | Devices | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Errors |
|------|--------|-------|--------:|--------:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|-------:|
| 2026-11-09 | 5ce2cfe | dynamodb-local | 2 | 16 | 130.2 | 27.7 | 12.4 | 118.9 | 60.9 | 102.4 | 102.9 | 47.6 | 101.2 | 626.3 | 0 |
| 2026-11-09 | 5ce2cfe | dynamodb-local | 8 | 64 | 46.5 | 50.7 | 114.1 | 1039.0 | 17.5 | 98.9 | 106.5 | 45.9 | 178.8 | 237.3 | 0 |
| 2026-11-09 | 5ce2cfe | dynamodb-local | 32 | 256 | 23.8 | 48.1 | 1881.3 | 9566.9 | 13.5 | 1239.7 | 108.7 | 100.1 | 596.0 | 59.4 | 0 |
| 2026-11-09 | 5ce2cfe | dynamodb-local | 2 | 10000 | 152.5 | 0.1 | 17.8 | 221.3 | 52.7 | 109.2 | 207.2 | 44.7 | 102.1 | 665.3 | 0 |
| 2026-11-09 | 5ce2cfe | dynamodb-local | 8 | 10000 | 52.0 | 0.4 | 338.8 | 3424.5 | 8.9 | 99.0 | 212.9 | 57.6 | 195.0 | 231.8 | 0 |
| 2026-11-09 | 5ce2cfe | etcd | 2 | 16 | 4102.6 | 29.7 | 0.5 | 4.8 | 0.6 | 4.9 | 102.7 | 0.1 | 1.9 | - | 0 |
| 2026-11-09 | 5ce2cfe | etcd | 8 | 64 | 3351.7 | 32.8 | 2.5 | 17.7 | 3.4 | 20.6 | 106.2 | 0.9 | 10.2 | - | 0 |
| 2026-11-09 | 5ce2cfe | etcd | 32 | 256 | 1084.5 | 37.7 | 19.7 | 67.2 | 49.3 | 574.6 | 107.0 | 31.3 | 639.9 | - | 0 |
| 2026-11-09 | 5ce2cfe | etcd | 2 | 10000 | 885.6 | 0.1 | 1.2 | 13.6 | 7.6 | 48.6 | 102.3 | 0.5 | 28.5 | - | 0 |
| 2026-11-09 | 5ce2cfe | etcd | 8 | 10000 | 743.2 | 0.3 | 6.0 | 68.4 | 38.8 | 254.6 | 207.0 | 20.4 | 179.7 | - | 0 |

At 10,000 devices on dynamodb-local the list p50 goes from 1619 ms to
53 ms and the binds per second from 4.9 to 152.5. No run has a `429`
any more: the lists no longer read the counter twice around a table
query. At 16 devices the list p50 rises from 9 ms to 61 ms, because a
consistent list from the cache waits for the next progress request,
which the library sends every 100 ms while a list waits; the reads per
member fall from 2116 to 626 per second, because the list itself reads
nothing from the table. [scale.md](scale.md) has the trade.

### etcd with fsync

The etcd rows above run etcd with `--unsafe-no-fsync`, as the unit tests
do. `SOLAS_ETCD_FSYNC=1 SCALE_STORES=etcd devbox run scale` keeps fsync
on. On this VM, whose disk is virtual, it changes nothing that the run
can see:

| Date | Commit | Store | Members | Devices | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Errors |
|------|--------|-------|--------:|--------:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|-------:|
| 2026-11-09 | 5ce2cfe | etcd, fsync on | 2 | 16 | 3946.1 | 29.8 | 0.5 | 4.8 | 0.7 | 5.0 | 102.2 | 0.1 | 2.0 | - | 0 |
| 2026-11-09 | 5ce2cfe | etcd, fsync on | 8 | 64 | 3335.6 | 32.8 | 2.6 | 17.7 | 3.4 | 19.3 | 106.9 | 0.9 | 8.9 | - | 0 |
| 2026-11-09 | 5ce2cfe | etcd, fsync on | 32 | 256 | 1353.6 | 37.6 | 19.0 | 51.7 | 44.8 | 310.1 | 107.1 | 27.5 | 326.8 | - | 0 |


## DynamoDB

Method: `devbox run scale` with `SOLAS_DYNAMODB_URL` from
`scripts/aws-bench.sh`, spec 3.1 and docs/plan-evidence.md M20. The
table is on-demand, new for each series, and deleted at the end. The
bench ran on the development VM; the TCP connect time to the region is
in the row, and the soak of the row above ran on the VM at the same
time. Each run lasted 20 seconds with 8 claims per member.

| Date | Commit | Region, connect | Members | Devices | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Errors |
|------|--------|-----------------|--------:|--------:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|-------:|
| 2026-11-09 | dfe164e | eu-west-1, 50 ms | 2 | 16 | 12.1 | 48.6 | 209.0 | 1112.9 | 59.9 | 121.6 | 120.2 | 62.1 | 176.6 | 140.6 | 0 |
| 2026-11-09 | dfe164e | eu-west-1, 50 ms | 8 | 64 | 11.1 | 83.7 | 126.8 | 2765.7 | 47.3 | 204.7 | 124.3 | 76.7 | 237.6 | 75.0 | 83 |
| 2026-11-09 | dfe164e | eu-west-1, 50 ms | 32 | 256 | 14.5 | 73.2 | 236.3 | 4912.9 | 67.7 | 372.4 | 422.1 | 96.2 | 235.1 | 32.7 | 1477 |

**The first real DynamoDB numbers, and they are low.** The Device
resource sustained 11 to 15 binds per second from 2 to 32 members, and
the errors at 8 and 32 members are `ThrottlingException: Throughput
exceeds the current capacity of your table or index ... check if you
have a hot key`. Every write of the resource goes to three items that
share three partition keys: the counter, the object partition
`obj#devices`, and the event partition. A partition sustains 1000 write
units per second, a transaction costs two units per item, and an event
item holds the object and its previous state, so one bind costs about
15 to 30 write units across the three hot partitions. That puts the
ceiling of the resource near 30 to 60 writes per second, which is what
the rows show, with a new on-demand table still scaling up. ADR 0003
assumed about 1000 writes per second per counter item; the partition,
not the counter, is the limit, and it is an order of magnitude lower.
Issue #241 measures the ceiling in a longer run, and the partition per
member of ADR 0022, or a partition per shard of devices, is the way
up.

The 10,000 device run on the table failed while it created the pool:
16 parallel creates hit the same counter item, and the second attempt
of a cancelled transaction met `TransactionInProgressException`. The
bench now gives each call eight attempts and retries the pool creation;
the row follows.
## Gatekeeper

Method: `go test ./pkg/gatekeeper/ -run TestGatekeeperLatency -v`, spec
17. It sends 2000 deploy calls to a fake actuator directly and 2000
through the gatekeeper, with a fake store that answers at once, so the
row shows what the proxy, the header checks, the lookup, the device read,
and the lock add. In a cluster the consistent read of the device adds
one store round trip on top: the bind p50 of the scale rows is the
closest measure of it.

| Date | Commit | Path | p50 | p99 |
|------|--------|------|----:|----:|
| 2026-11-09 | 9cf5083 | direct to the actuator | 11 µs | 114 µs |
| 2026-11-09 | 9cf5083 | through the gatekeeper, in process | 40 µs | 1.14 ms |
| 2026-11-09 | 9cf5083 | the gatekeeper adds | 28 µs | 1.02 ms |

The e2e reclaim scene shows the gatekeeper at work in a cluster: the
stale holder's deploy after a reclaim is rejected with 403 and never
reaches the actuator (`test/e2e/mesh_test.go`).
## Soak

Method: `devbox run soak`, the test `TestSoak` in
`test/e2e/soak_test.go`. Two clusters share one store and a device
pool. For the duration of the run, a seeded schedule pauses the store,
pauses a cluster's node for longer than D, deletes a solas Pod, and
creates and deletes claims. The H100 is behind the demo gatekeeper, and
a workload in each cluster uses it with the token of its claim. At the
end, the audit of `pkg/audit`, which has followed the Device history of
the store in version order through the whole run, has checked every
transition: a bind raises the token, a release keeps it, and the holder
changes only through a release, an offer, or a recovery. The gatekeeper
log is checked too: the accepted tokens never go down, and every
rejected use had a token below the highest accepted.

| Date | Commit | Store | Duration | Faults | Takeovers | History | Violations | Gatekeeper |
|------|--------|-------|---------:|--------|----------:|---------|-----------:|------------|
| 2026-11-09 | 35359f4 | dynamodb-local | 30 min, seed 1 | 13 store pauses of 15 to 35 s, 26 node pauses of 12 to 30 s, 17 Pod deletes, 15 claims created and deleted | 28 member UID changes, 87 Lost claims created again | 226 events, 108 binds, 102 releases | 0 | 1471 uses accepted, 81 rejected, every rejection for a token below the highest accepted; highest token 19 |
| 2026-11-09 | 58342bc | dynamodb-local, through the gatekeeper | 30 min, seed 1 | 13 store pauses, 26 node pauses, 17 Pod deletes, 15 claims created and deleted | 28 member UID changes, 86 Lost claims created again | 224 events, 107 binds, 101 releases | 0 | 1309 uses accepted, 81 rejected, every rejection for a stale token; 1093 actuator calls accepted through `solas gatekeeper`, each from the holder of the token at the time |

The second row runs the H100 behind `solas gatekeeper` and the demo
actuator, spec 17. The verifier checks each call the actuator accepted
against the holder of the token in the replayed history.

The replay covered the whole run: the event retention of the DynamoDB
store is one hour, and the run took 31 minutes. The 81 rejections are
the workloads of clusters whose node was paused for longer than D. Each
one woke up, still believed it held the H100, and used it with its old
token after the other cluster had bound the device with a higher one.
The gatekeeper rejected every such use, spec 6.6. No claim was Suspended
at the end, and no fault step was skipped.
