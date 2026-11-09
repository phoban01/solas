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
have a hot key`. Every write of the resource is one transaction on
three items under three partition keys: the counter (85 bytes), the
object (under 1 KB), and the event, which holds the object and its
previous state (1.4 KB, measured on a local table). A transaction costs
two write units per kilobyte of each item, so a bind or a release costs
about 8 write units, 4 of them on the event partition. A partition
sustains 1000 write units per second, so the event partition alone
would allow about 250 binds and releases per second, and the counter
item 500. The rows show a tenth of that, with the table new and
DynamoDB reporting that it was still scaling the capacity of a hot key.
The five minute row below shows that it does not scale past it. ADR 0003
assumed about 1000 writes per second per counter item; on a real table
the hot partition keys, not the counter, set the ceiling, and the
partition per member of ADR 0022 or a partition per shard of devices
is the way up.

### 10,000 devices and a five minute run

The same table, with the bench giving each call eight attempts (commit
fd95ab6). The first two rows have 10,000 devices and 20 seconds; the
last has the default pool at 8 members for five minutes, to see where a
table settles once its capacity has scaled.

| Date | Commit | Region, connect | Members | Devices | Run | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Errors |
|------|--------|-----------------|--------:|--------:|----:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|-------:|
| 2026-11-09 | fd95ab6 | eu-west-1, 43 ms | 2 | 10000 | 20 s | 13.5 | 0.0 | 382.5 | 3781.2 | 62.9 | 123.3 | 826.9 | 64.4 | 209.7 | 138.8 | 0 |
| 2026-11-09 | fd95ab6 | eu-west-1, 43 ms | 8 | 10000 | 20 s | 12.5 | 0.0 | 1717.2 | 11369.9 | 67.4 | 157.1 | 2045.0 | 105.7 | 220.4 | 77.7 | 0 |
| 2026-11-09 | fd95ab6 | eu-west-1, 20 ms | 8 | 64 | 300 s | 11.2 | 52.1 | 576.7 | 4993.4 | 67.5 | 246.9 | 121.8 | 97.0 | 275.9 | 82.7 | 2 |

**The table does not scale past it.** Five minutes at 8 members gave
11.2 binds per second, the same as the 20 second runs, so the limit is
not a new table warming up. With eight attempts per call the throttling
no longer shows as errors; it shows as the bind p50 of 0.6 to 1.7
seconds and a p99 of 5 to 11 seconds, which is the SDK's backoff. The
Device resource on DynamoDB sustains about 25 transactions per second,
binds and releases together, from a client 20 to 50 ms away. That is
below the partition arithmetic above by a factor of ten, so the limit is
tighter than write units: DynamoDB serializes transactions that touch
one item, and every transaction of the resource touches the counter
item. The in-region run of #243 separates the round trip from the
server's share. Issue #267 is the design that lifts the ceiling.

**What holds up at 10,000 devices.** The list stays at 63 to 67 ms,
from the cache, and a member syncs the whole pool in 0.8 seconds alone
and 2 seconds when eight members sync together. The conflict rate is
zero, because the claims rarely pick the same device; the bind latency
is the counter, not the race.


### After the batched writes (spec 2.3, commit 92085cf)

The same runs on a new eu-west-1 table with the writes of a server
batched into one transaction. The column "writes/tx" is how many writes
a transaction carried on average.

| Date | Commit | Region, connect | Members | Devices | Run | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Writes/tx | Errors |
|------|--------|-----------------|--------:|--------:|----:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|----------:|-------:|
| 2026-11-09 | dcbdf8e | eu-west-1, 41 ms | 2 | 16 | 20 s | 11.8 | 45.5 | 336.1 | 878.8 | 58.8 | 109.7 | 119.0 | 68.6 | 215.3 | 59.4 | 1.6 | 0 |
| 2026-11-09 | dcbdf8e | eu-west-1, 41 ms | 8 | 64 | 20 s | 28.4 | 47.4 | 437.3 | 1853.0 | 63.1 | 119.5 | 123.6 | 86.9 | 229.3 | 43.3 | 2.8 | 0 |
| 2026-11-09 | dcbdf8e | eu-west-1, 41 ms | 32 | 256 | 20 s | 25.6 | 47.2 | 2016.4 | 7442.8 | 70.7 | 395.9 | 227.2 | 119.9 | 239.9 | 20.9 | 3.1 | 0 |
| 2026-11-09 | dcbdf8e | eu-west-1, 21 ms | 8 | 64 | 300 s | 27.9 | 46.4 | 432.8 | 1855.4 | 65.6 | 121.1 | 124.7 | 89.5 | 230.6 | 42.8 | 2.8 | 0 |
| 2026-11-09 | dcbdf8e | eu-west-1, 20 ms | 2 | 10000 | 20 s | 28.8 | 0.5 | 113.5 | 998.7 | 74.1 | 166.8 | 836.2 | 84.3 | 220.2 | 61.6 | 7.1 | 0 |
| 2026-11-09 | dcbdf8e | eu-west-1, 20 ms | 8 | 10000 | 20 s | 39.1 | 0.4 | 474.8 | 3564.4 | 69.3 | 184.1 | 840.6 | 112.0 | 240.1 | 35.1 | 6.9 | 0 |

**Two to three times the binds, and the batch size says why.** At 8
members the binds per second went from 11.1 to 28.4 with 2.8 writes per
transaction, and the five minute run held 27.9. At 10,000 devices,
where the claims rarely race for a device and so rarely wait on a
`409`, a transaction carried 7 writes and the binds went from 13.5 to
28.8 at 2 members and from 12.5 to 39.1 at 8. The rate now follows the
writes a server has in flight: one claim per member at 2 members gave
1.6 writes per transaction and no gain. The ceiling that remains is the
counter item shared by the servers, about one transaction per two round
trips across them all, which issue #267 is for; and a cluster's one
claim worker, which issue #268 is for. No run had an error once the
bench retried without the SDK's shared quota.
### Three regions, one client

The default series from the VM against a table in each region, 20
seconds per run, 8 claims per member, before the batched writes of
commit 92085cf. The TCP connect time to the region's endpoint stands for
the round trip.

| Date | Commit | Region, connect | Members | Devices | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Errors |
|------|--------|-----------------|--------:|--------:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|-------:|
| 2026-11-09 | dfe164e | eu-west-1, 50 ms | 2 | 16 | 12.1 | 48.6 | 209.0 | 1112.9 | 59.9 | 121.6 | 120.2 | 62.1 | 176.6 | 140.6 | 0 |
| 2026-11-09 | dfe164e | eu-west-1, 50 ms | 8 | 64 | 11.1 | 83.7 | 126.8 | 2765.7 | 47.3 | 204.7 | 124.3 | 76.7 | 237.6 | 75.0 | 83 |
| 2026-11-09 | dfe164e | eu-west-1, 50 ms | 32 | 256 | 14.5 | 73.2 | 236.3 | 4912.9 | 67.7 | 372.4 | 422.1 | 96.2 | 235.1 | 32.7 | 1477 |
| 2026-11-09 | 31883ff | us-east-1, 140 ms | 2 | 16 | 3.5 | 48.1 | 840.5 | 3759.4 | 218.6 | 341.7 | 400.7 | 197.3 | 293.8 | 45.4 | 0 |
| 2026-11-09 | 31883ff | us-east-1, 140 ms | 8 | 64 | 4.1 | 51.9 | 2454.4 | 13148.1 | 243.6 | 366.3 | 501.6 | 193.8 | 305.2 | 35.2 | 0 |
| 2026-11-09 | 31883ff | us-east-1, 140 ms | 32 | 256 | 3.2 | 43.4 | 16528.8 | 56122.9 | 422.4 | 1995.5 | 809.1 | 278.6 | 2528.1 | 14.9 | 16 |
| 2026-11-09 | 31883ff | ap-southeast-1, 280 ms | 2 | 16 | 1.0 | 40.0 | 4234.2 | 8590.6 | 441.0 | 838.2 | 1139.6 | 403.9 | 583.3 | 7.0 | 0 |
| 2026-11-09 | 31883ff | ap-southeast-1, 280 ms | 8 | 64 | 2.9 | 45.2 | 5089.3 | 13791.4 | 702.2 | 1054.6 | 1387.5 | 526.5 | 1047.1 | 6.7 | 0 |
| 2026-11-09 | 31883ff | ap-southeast-1, 280 ms | 32 | 256 | 3.7 | 41.3 | 12394.9 | 43271.2 | 813.7 | 911.6 | 1892.2 | 507.2 | 1032.1 | 5.5 | 0 |

**The ceiling scales with the round trip, so it is the write loop, not
the table.** Three times the round trip gave a third of the binds per
second: 12 to 15 at 50 ms, 3.5 at 140 ms, 1 to 4 at 280 ms. A server-side limit on the
counter item would not move with the client's distance. What moves is
the write loop of spec 2.3 before commit 92085cf: every writer reads
the counter, sends its transaction, and when two writers raced, DynamoDB
cancelled all but one, which then read again. One write per two round
trips got through, whatever the number of writers. A consistent list
costs a round trip, 60 ms at 50 ms and 220 ms at 140 ms, because the
cache reads the counter once. The watch lag follows the poll plus a
round trip. The batched writes of 92085cf let the writers of one server
share the round trip; the rows after it are below.

### Poll interval

`SCALE_POLL` sets how often the watch of the DynamoDB store polls the
event log, spec 4.2. Eight members, 8 claims each, 60 seconds per run,
eu-west-1 at 20 to 40 ms. These runs are also the first with the batched
writes of commit 92085cf: the 8 member row of the default series above
had 11.1 binds per second before it.

| Date | Commit | Region, connect | Poll | Members | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Errors |
|------|--------|-----------------|-----:|--------:|--------:|-----------:|------------:|------------:|------------:|------------:|-----------:|-----------:|---------------:|-------:|
| 2026-11-09 | 92085cf | eu-west-1, 41 ms | 100 ms | 8 | 27.3 | 45.2 | 407.7 | 2255.9 | 60.8 | 119.5 | 64.9 | 139.7 | 49.0 | 0 |
| 2026-11-09 | 92085cf | eu-west-1, 20 ms | 200 ms | 8 | 26.5 | 45.6 | 418.8 | 2203.7 | 66.2 | 128.1 | 93.4 | 232.6 | 39.7 | 0 |
| 2026-11-09 | 92085cf | eu-west-1, 21 ms | 500 ms | 8 | 20.0 | 39.3 | 316.1 | 1839.9 | 69.8 | 143.1 | 153.6 | 540.7 | 23.2 | 0 |

The lag at p50 is about the round trip plus half the poll interval, and
the p99 about the round trip plus the interval. The reads per member fall
from 49 to 23 per second between 100 and 500 ms, most of them the polls
of the Device and Member resources. For a lease of 30 seconds and a
renew every 10, a lag of a tenth of a second does not matter; ADR 0004
keeps polling at 200 ms and records the rows as the reason.
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
