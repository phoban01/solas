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

The suspend takes longer than D because the one controller worker is
held by a device list that waits for the store until the request timeout
of 20 seconds, and on etcd also by the keepalive of the etcd client.

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

## Soak

Method: `devbox run soak`, the test `TestSoak` in
`test/e2e/soak_test.go`. Two clusters share one store and a device
pool. For the duration of the run, a seeded schedule pauses the store,
pauses a cluster's node for longer than D, deletes a solas Pod, and
creates and deletes claims. The H100 is behind the demo gatekeeper, and
a workload in each cluster uses it with the token of its claim. At the
end, `pkg/audit` replays the Device history of the store in version
order and checks every transition: a bind raises the token, a release
keeps it, and the holder changes only through a release, an offer, or a
recovery. The gatekeeper log is checked too: the accepted tokens never
go down, and every rejected use had a token below the highest accepted.

| Date | Commit | Store | Duration | Faults | Takeovers | History | Violations | Gatekeeper |
|------|--------|-------|---------:|--------|----------:|---------|-----------:|------------|
| 2026-11-09 | 35359f4 | dynamodb-local | 30 min, seed 1 | 13 store pauses of 15 to 35 s, 26 node pauses of 12 to 30 s, 17 Pod deletes, 15 claims created and deleted | 28 member UID changes, 87 Lost claims created again | 226 events, 108 binds, 102 releases | 0 | 1471 uses accepted, 81 rejected, every rejection for a token below the highest accepted; highest token 19 |

The replay covered the whole run: the event retention of the DynamoDB
store is one hour, and the run took 31 minutes. The 81 rejections are
the workloads of clusters whose node was paused for longer than D. Each
one woke up, still believed it held the H100, and used it with its old
token after the other cluster had bound the device with a higher one.
The gatekeeper rejected every such use, spec 6.6. No claim was Suspended
at the end, and no fault step was skipped.
