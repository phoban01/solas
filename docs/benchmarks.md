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

| Date | Commit | Store | Members | Devices | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Errors |
|------|--------|-------|--------:|--------:|--------:|-----------:|------------:|------------:|-----------:|-----------:|---------------:|-------:|
| 2026-11-09 | 47da523 | dynamodb-local | 2 | 16 | 188.8 | 13.5 | 5.5 | 83.2 | 106.5 | 202.4 | 1970.4 | 10 |
| 2026-11-09 | 47da523 | dynamodb-local | 8 | 64 | 38.7 | 53.3 | 153.9 | 1359.8 | 106.2 | 201.0 | 220.2 | 0 |
| 2026-11-09 | 47da523 | dynamodb-local | 32 | 256 | 25.7 | 48.2 | 1822.9 | 10628.9 | 160.2 | 933.1 | 50.9 | 0 |
| 2026-11-09 | 47da523 | etcd | 2 | 16 | 3862.6 | 29.5 | 0.5 | 5.1 | 0.1 | 2.3 | - | 0 |
| 2026-11-09 | 47da523 | etcd | 8 | 64 | 3313.1 | 32.3 | 2.6 | 16.7 | 1.0 | 9.3 | - | 0 |
| 2026-11-09 | 47da523 | etcd | 32 | 256 | 1169.6 | 36.2 | 20.0 | 81.0 | 30.4 | 501.1 | - | 0 |

The 10 errors in the first row are `429` from the consistent list under
sustained writes, issue #233.

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
