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
| 2026-11-12 | 8e31298 | postgres | 2 | 16 | 293.4 | 40.9 | 10.7 | 77.9 | 4.4 | 38.8 | 110.4 | 5.3 | 42.0 | - | 0 |
| 2026-11-12 | 8e31298 | postgres | 8 | 64 | 297.3 | 41.4 | 32.1 | 183.0 | 34.6 | 236.8 | 115.6 | 29.7 | 190.6 | - | 0 |
| 2026-11-12 | 8e31298 | postgres | 32 | 256 | 269.9 | 41.2 | 149.8 | 502.9 | 200.8 | 514.6 | 203.2 | 149.2 | 438.2 | - | 0 |

The Postgres rows of 2026-11-12 ran on the container of
`scripts/local-stores.sh`, with fsync off as the etcd runs, on the same
VM. Postgres gives about 300 binds per second at 2 to 32 members, a
tenth of etcd on the same host and ten times dynamodb-local at 8
members, and holds the rate to 32 members. The lock queue of the
counter row is the ceiling, as the rows of "Postgres" below measured
it alone.

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

### With the counter guess (spec 2.3, commit 93bbdf5)

The same runs after the counter guess of ADR 0003, which takes the
read before each transaction away. The bind burst rows above show what
it does for one cluster's latency. Here, with many members, it changes
nothing: the ceiling is where the batched rows left it.

| Date | Commit | Region, connect | Members | Devices | Run | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Writes/tx | Errors |
|------|--------|-----------------|--------:|--------:|----:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|----------:|-------:|
| 2026-11-10 | 93bbdf5 | eu-west-1, 67 ms | 2 | 16 | 20 s | 12.6 | 43.6 | 338.8 | 1175.0 | 42.1 | 88.3 | 125.2 | 53.4 | 198.4 | 55.8 | 1.6 | 0 |
| 2026-11-10 | 93bbdf5 | eu-west-1, 67 ms | 8 | 64 | 20 s | 26.9 | 45.7 | 404.1 | 2481.0 | 43.4 | 135.2 | 225.7 | 83.8 | 228.7 | 38.0 | 2.9 | 0 |
| 2026-11-10 | 93bbdf5 | eu-west-1, 67 ms | 32 | 256 | 20 s | 27.2 | 45.9 | 1447.9 | 8339.4 | 50.5 | 370.2 | 223.7 | 119.3 | 238.3 | 17.7 | 3.2 | 0 |

At 8 and 32 members the rate is 27 binds per second, about 54 writes
per second in transactions of 3 writes, so about 18 transactions per
second: one per round trip of 67 ms. The transactions of all members
touch the counter item, and DynamoDB lets one of them through at a
time; the guess of each member misses as soon as another member writes,
and the read and the retry cost the round trip again. The ceiling is
the counter item in the transaction, not the read before it. ADR 0025
proposes to take the counter out of the transaction.

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
### In the table's region (#243)

Method: `scripts/aws-bench.sh ec2 up eu-west-1` starts one c6i.2xlarge
(8 vCPUs, 16 GB) in eu-west-1 with the bench, and `ec2 scale` runs it
against the table at 32 and 128 members, with the default pool and with
10,000 devices, 20 seconds per run, 8 claims per member. The TCP connect
time from the instance to DynamoDB was 1.4 to 2.5 ms. The rows at 2 and
8 members, and the second row at 32 members and 10,000 devices, came
from a second instance a few minutes later with the bench of 410a4f3,
which waits 10 ms after a failed list.

| Date | Commit | Region, connect | Members | Devices | Run | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Writes/tx | Errors |
|------|--------|-----------------|--------:|--------:|----:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|----------:|-------:|
| 2026-11-10 | 410a4f3 | eu-west-1, in the region, 2 ms | 2 | 16 | 20 s | 35.6 | 42.2 | 113.8 | 434.9 | 4.1 | 13.8 | 102.7 | 19.3 | 186.8 | 124.3 | 1.6 | 0 |
| 2026-11-10 | 410a4f3 | eu-west-1, in the region, 2 ms | 8 | 64 | 20 s | 57.8 | 36.3 | 75.7 | 1404.8 | 3.9 | 83.8 | 102.9 | 69.0 | 199.0 | 51.7 | 3.0 | 0 |
| 2026-11-10 | 3d388b1 | eu-west-1, in the region, 2 ms | 32 | 256 | 20 s | 47.1 | 45.7 | 645.2 | 4014.0 | 4.0 | 211.2 | 105.2 | 90.7 | 199.5 | 23.3 | 3.1 | 0 |
| 2026-11-10 | 3d388b1 | eu-west-1, in the region, 2 ms | 128 | 1024 | 20 s | 21.9 | 45.1 | 11063.6 | 34968.7 | 377.6 | 660.1 | 110.0 | 98.9 | 200.0 | 13.6 | 3.4 | 0 |
| 2026-11-10 | 3d388b1 | eu-west-1, in the region, 2 ms | 32 | 10000 | 20 s | 48.1 | 1.5 | 1652.1 | 12433.7 | 25.9 | 545.7 | 1714.1 | 104.8 | 416.5 | 18.7 | 6.5 | 13408312 |
| 2026-11-10 | 410a4f3 | eu-west-1, in the region, 2 ms | 32 | 10000 | 20 s | 58.2 | 1.2 | 1530.7 | 9194.2 | 8.2 | 725.5 | 407.4 | 97.2 | 200.6 | 19.7 | 6.5 | 6388 |
| 2026-11-10 | 3d388b1 | eu-west-1, in the region, 2 ms | 128 | 10000 | 20 s | 20.7 | 5.3 | 22406.5 | 47900.6 | 1265.0 | 2713.7 | 2115.3 | 101.3 | 204.3 | 11.8 | 6.5 | 0 |

**The ceiling is the store's, not the distance.** From 2 ms away the
Device resource gave 58 binds per second at 8 members and 47 at 32,
against 27 from the VM at 67 ms: twice the binds for a round trip
thirty times shorter. A bind at 32 members took 645 ms at the median,
some three hundred round trips. The transactions of all members touch
the counter item. DynamoDB commits about 30 of them per second, with 3
writes each, cancels the ones that raced, 36 to 46 percent of the
attempts, and each loser reads the counter and tries again. More
members means more cancelled transactions, so 128 members get 22 binds
per second and a median bind of 11 seconds. This is the number the
second review asked for (docs/plan-evidence.md): the write path of today
sustains about 50 binds per second per resource, from anywhere, and the
counter item in the transaction is why. ADR 0025 takes the counter out
of the transaction; issue #290 measures what one item sustains without
it, before any of its code.

**At 10,000 devices the rate is the same and the conflicts are gone.**
The claims rarely pick the same device from a pool of 10,000, so a
transaction carries 6.5 writes, and the rate stays at the ceiling: 48
to 58 binds per second at 32 members, 21 at 128. The errors of the two
32 member runs are one member's watch cache built again during the
run, with the message `storage is (re)initializing`; issue #298 has the
cause to find. The bench of 3d388b1 retried the list in a loop during
the outage and counted every try, 13 million in under a second; the
bench of 410a4f3 waits 10 ms after a failed list, and counted 6,388 in
the same event.

The watch lag at the median is 90 to 105 ms with 32 members or more,
half the poll interval, now that the round trip is gone from it; at 2
members, with few writes, 19 ms.

### Same round trip (#295)

The etcd rows above come from an etcd on the VM, under 1 ms away, and
the DynamoDB rows from a table 20 to 67 ms away. For a comparison at
one distance, `ec2 up` starts an etcd (3.6.14, fsync on) on the
instance in eu-west-1, and `devbox run scale` ran from the VM against
it and against the table in the same minutes. TCP connect from the VM
to DynamoDB: 40 ms. The connect time to the instance was not measured;
the median bind on etcd at 2 members, 21 ms, bounds its round trip at
about 20 ms, so the instance was nearer than the table's endpoint by
about half. 20 seconds per run, 8 claims per member.

| Date | Commit | Store, connect | Members | Devices | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Reads/member/s | Writes/tx | Errors |
|------|--------|----------------|--------:|--------:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|---------------:|----------:|-------:|
| 2026-11-10 | 3d388b1 | etcd in eu-west-1, about 20 ms | 2 | 16 | 162.1 | 32.8 | 21.1 | 28.5 | 19.0 | 27.0 | 159.6 | 0.2 | 3.0 | - | - | 0 |
| 2026-11-10 | 3d388b1 | etcd in eu-west-1, about 20 ms | 8 | 64 | 560.8 | 33.7 | 23.2 | 32.3 | 22.3 | 31.9 | 163.6 | 0.4 | 4.4 | - | - | 0 |
| 2026-11-10 | 3d388b1 | etcd in eu-west-1, about 20 ms | 32 | 256 | 695.7 | 33.2 | 40.5 | 293.9 | 46.6 | 1089.0 | 165.4 | 36.1 | 2157.5 | - | - | 0 |
| 2026-11-10 | 3d388b1 | DynamoDB eu-west-1, 40 ms | 2 | 16 | 2.0 | 44.9 | 380.4 | 39768.4 | 40.5 | 80.2 | 122.8 | 47.2 | 195.3 | 13.2 | 1.5 | 0 |
| 2026-11-10 | 410a4f3 | DynamoDB eu-west-1, 41 ms | 2 | 16 | 15.5 | 40.7 | 188.4 | 816.8 | 42.0 | 79.0 | 122.1 | 49.3 | 198.4 | 61.4 | 1.8 | 0 |
| 2026-11-10 | 3d388b1 | DynamoDB eu-west-1, 40 ms | 8 | 64 | 28.5 | 45.6 | 436.4 | 2275.4 | 45.3 | 130.5 | 128.7 | 79.8 | 230.4 | 43.4 | 2.8 | 0 |
| 2026-11-10 | 3d388b1 | DynamoDB eu-west-1, 40 ms | 32 | 256 | 26.5 | 49.5 | 1746.2 | 7344.9 | 48.4 | 369.6 | 228.1 | 114.8 | 237.2 | 22.2 | 3.0 | 0 |

**At one distance, etcd gives twenty times the binds.** 561 binds per
second at 8 members and 696 at 32 on etcd, against 28 and 27 on
DynamoDB from the same VM in the same minutes. A bind on etcd takes one
round trip at the median, 21 to 40 ms, and the writes of all members
are in flight at once; on DynamoDB it takes 436 to 1746 ms, the queue
on the counter item. The difference in distance, about 20 ms, explains
none of it: the rows in the region above, at 2 ms, give the same 50
binds per second. The first DynamoDB row at 2 members, 2 binds per
second with a bind of 40 seconds at p99, ran right after the 128 member
run at 10,000 devices in the region; the run again a few minutes later
gave 15.5, the rate of the earlier rows. Both rows stay on the page.

### One item and one partition (#290)

ADR 0025 rests on two numbers: the update rate of one item, and the
rate of transactions that share no item. This series measures both
with the DynamoDB client alone, no API server, `solas-bench --mode
counter` and `--mode commit`, from the VM against the eu-west-1 table,
`scripts/aws-bench.sh item eu-west-1`. In counter mode every writer
sends `UpdateItem ADD n :1` to one item, the issue step of spec 3.1. In
commit mode every writer sends one `TransactWriteItems` that puts two
items of its own, 512 bytes each, conditional on their version, the
commit step of spec 2.3. The items of commit mode share one partition
key, as the object items of a resource do, spec 2.2; "commit spread"
gives each writer a partition key of its own. Each run lasted 20
seconds. Throttles are the calls that DynamoDB throttled and the SDK
retried inside the call; the ops/s count only the calls that returned.

| Date | Commit | Region, connect | Mode | Writers | Ops/s | p50 ms | p99 ms | Throttles | Errors |
|------|--------|-----------------|------|--------:|------:|-------:|-------:|----------:|-------:|
| 2026-11-10 | 0c166fe | eu-west-1, 29 ms | counter | 1 | 33.5 | 29.4 | 38.6 | 0 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 25 ms | counter | 4 | 177.7 | 21.9 | 31.3 | 0 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 21 ms | counter | 16 | 731.7 | 21.4 | 29.9 | 0 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 25 ms | counter | 64 | 967.9 | 23.2 | 1518.5 | 736 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 30 ms | commit | 1 | 23.6 | 41.8 | 58.2 | 0 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 25 ms | commit | 4 | 122.8 | 31.8 | 44.9 | 0 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 33 ms | commit | 16 | 244.5 | 39.9 | 1077.8 | 92 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 20 ms | commit | 64 | 229.8 | 30.2 | 3652.8 | 946 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 27 ms | commit spread | 1 | 29.0 | 33.9 | 42.6 | 0 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 24 ms | commit spread | 4 | 119.9 | 32.7 | 43.3 | 0 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 24 ms | commit spread | 16 | 475.4 | 33.1 | 44.7 | 0 | 0 |
| 2026-11-10 | 0c166fe | eu-west-1, 26 ms | commit spread | 64 | 1260.3 | 34.2 | 267.3 | 264 | 0 |

**One item takes about 1,000 updates per second.** Up to 16 writers,
the rate is the writers divided by the round trip: 21 ms at p50, the
connect time. At 64 writers the item reaches 968 updates per second
and DynamoDB throttles the rest; the p99 of 1.5 seconds is the backoff
of the SDK. The number of ADR 0025 holds: one item, on a real table,
from the VM, gives about 1,000 updates per second, each able to issue
up to 25 versions.

**A partition takes 1,000 write units per second, and the object items
of a resource share one.** The commit rows stop at 245 transactions per
second with throttles, at 16 writers and at 64, though no transaction
touches an item of another. A transaction that puts two items of 512
bytes costs 4 write units, and the partition of one key takes 1,000 per
second. With a partition key per writer, the same transactions reach
1,260 per second at 64 writers, with a p99 of 267 ms and a tenth of the
throttles. Transactions on different items do not wait for each other;
transactions on one partition key do. Spec 2.2 puts every object item
of a resource under one partition key, `obj#` and the resource, and
every event item under another, so a resource has two such partitions.

**What this bounds.** Under ADR 0025 the counter item is no longer in
the transaction, so the serialization of the rows above, about 30
transactions per second, goes. The next ceiling is not the counter: it
is the write units of the two partitions of the resource. A commit
writes one object item and one event item, so for objects under 1 KB
the partition of the object items takes about 500 commits per second,
and the partition of the event items the same. The two-step write
lifts the ceiling of a resource from about 30 commits per second to
about 500, not to 1,000. DynamoDB splits a hot partition key across
partitions after minutes of load; a 20 second run does not see it.
A bind of the claim rows is two commits, the bind and its release, so
500 commits per second is about 250 binds per second per resource,
five times the 50 of the write path of today.

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
## Postgres

Method: `solas-bench --mode counter|commit|poll` on a `postgres://`
URL, with the client alone and no store code, against an Aurora
Serverless v2 PostgreSQL 18.6 cluster in eu-west-1 from the VM, 20 ms
away: `scripts/aws-bench.sh pg up|item|down eu-west-1`, ADR 0026. In
counter mode every writer updates one counter row, `UPDATE ...
RETURNING`, in a transaction of its own. In commit mode every writer
runs the write of spec 2.3 as one function call: the counter update
that takes the row lock, two conditional updates of rows of its own,
512 bytes each, and an event insert; "commit spread" is the same
without the counter. In poll mode the writers are 8 commit writers,
and N pollers each read the events above their cursor every 200 ms, as
a watch of spec 4.2 does; the poll columns are the polls and the time
of one poll query, the wait excluded. Each run lasted 20 seconds. The
capacity column is the ACUs the cluster was at during the run, from its
CloudWatch metric; the cluster had a maximum of 8 ACUs in the first
series and 16 in the second.

| Date | Commit | Capacity | Mode | Writers | Ops/s | p50 ms | p99 ms | Polls/s | Poll p50 ms | Poll p99 ms | Errors |
|------|--------|---------:|------|--------:|------:|-------:|-------:|--------:|------------:|------------:|-------:|
| 2026-11-11 | eac6443, five round trips | 8 | commit | 1 | 9.0 | 109.9 | 165.0 | | | | 0 |
| 2026-11-11 | eac6443, five round trips | 8 | commit | 16 | 12.9 | 902.8 | 3803.3 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | counter | 1 | 50.0 | 19.7 | 26.8 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | counter | 4 | 203.0 | 19.4 | 26.0 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | counter | 16 | 687.8 | 22.1 | 42.6 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | counter | 64 | 394.6 | 119.4 | 659.8 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | commit | 1 | 49.3 | 20.0 | 25.6 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | commit | 4 | 193.4 | 20.0 | 32.7 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | commit | 16 | 552.6 | 26.6 | 59.1 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | commit | 64 | 338.2 | 132.9 | 821.4 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | commit spread | 1 | 45.9 | 20.2 | 43.4 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | commit spread | 4 | 181.1 | 20.3 | 47.4 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | commit spread | 16 | 776.2 | 19.9 | 27.5 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | commit spread | 64 | 2584.7 | 23.9 | 39.2 | | | | 0 |
| 2026-11-11 | 3e15430 | 8 | poll | 100 | 382.0 | 20.0 | 31.9 | 452.8 | 19.1 | 110.8 | 0 |
| 2026-11-11 | 3e15430 | 8 | poll | 1000 | 39.6 | 70.3 | 582.4 | 2128.4 | 24.0 | 361.8 | 0 |
| 2026-11-11 | 3e15430 | 9 | counter | 64 | 386.2 | 113.8 | 752.8 | | | | 0 |
| 2026-11-11 | 3e15430 | 9 | commit | 16 | 602.1 | 25.1 | 46.6 | | | | 0 |
| 2026-11-11 | 3e15430 | 9 | commit | 64 | 338.8 | 128.8 | 850.3 | | | | 0 |
| 2026-11-11 | 3e15430 | 9 to 12 | commit spread | 64 | 2846.2 | 22.0 | 31.9 | | | | 0 |
| 2026-11-11 | 3e15430 | 12 | poll | 100 | 386.6 | 20.0 | 32.2 | 453.7 | 19.6 | 59.2 | 0 |
| 2026-11-11 | 3e15430 | 12 | poll | 1000 | 254.1 | 22.7 | 134.2 | 4395.7 | 21.3 | 247.8 | 0 |
| 2026-11-11 | 3e15430 | 16 | poll | 3000 | 71.3 | 76.0 | 496.7 | 11444.3 | 33.5 | 437.9 | 0 |

**The row lock gives the order of today at the rate of etcd.** A commit
with the counter costs one round trip at one writer, 20 ms, and scales
with the writers to 553 to 602 per second at 16, against 30 on
DynamoDB from the same VM: twenty times, the gap the etcd rows of
"Same round trip" showed. The lock is held for the time of the
function on the server, so the queue of writers moves at the server's
pace, not at the round trip. The commit order of a resource is its
version order, and the log has no hole: the property that ADR 0025
gives up on DynamoDB, kept.

**The write must be one round trip.** The first version of the bench
held the lock across five statements, each a round trip from the VM,
and got 9 to 13 commits per second whatever the number of writers: the
lock hold time was the client's distance. A Postgres store writes
through one statement or one function, never a transaction that waits
on the client between statements.

**Past 16 writers in flight the lock queue slows.** At 64 writers the
counter row gives 386 to 395 updates per second, with or without the
other statements, at a median of 115 to 130 ms, the same at 8 ACUs and
at 9; the spread rows reach 2,846 per second at 64 writers on the same
cluster, so it is the lock, not the server. The writers in flight on
one resource are the servers with a batch in flight, one transaction
each, spec 2.3; a fleet of thousands of members whose writes are rare
keeps that number small.

**Polls hold the interval to 1,000 pollers; at 3,000 the CPU is the
limit.** At 16 ACUs, 1,000 pollers every 200 ms make 4,396 polls per
second with a p99 of 248 ms, and the 8 writers keep 254 commits per
second. At 8 ACUs the same run put the CPU at 100 percent, the polls
fell to 2,128 per second, and the writers to 40. 3,000 pollers, the
target fleet, connected directly, 3,009 connections, and made 11,444
polls per second with a p99 of 438 ms, with the cluster at its maximum
of 16 ACUs and the CPU at 100 percent; the writers fell to 71 per
second at a median of 76 ms. It is the polls that take the CPU, not
the connections. A watch that is woken by `NOTIFY` replaces 15,000
polls per second by the rate of the events, and is the next row to
produce before a fleet of that size.

The series cost under two dollars. The connect column of the bench
included a DNS lookup in these rows, so the round trip is the counter
row at one writer, 20 ms; the bench now resolves the name first.


### The watch woken by NOTIFY (#319)

The same bench on 2026-11-12 against a new Aurora Serverless v2 cluster
at 2 to 16 ACUs, from the VM at 20 ms, with the commit function of
the store, which notifies the channel of the schema at the end of
each transaction, and `--mode poll --notify`: each poller holds a
listener connection of its own, as a server would, and polls when a
notification arrives, no sooner than the poll interval after its
last poll, spec 2.8. The pollers without `--notify` poll every 200
ms. Eight writers commit beside the pollers. The capacity column is
the maximum of the minutes of the run, which a 20 second run shares
with the one before.

| Date | Commit | Mode | Pollers | Capacity | Commits/s | Commit p50 ms | Commit p99 ms | Polls/s | Poll p50 ms | Poll p99 ms | Errors |
|------|--------|------|--------:|----------|----------:|--------------:|--------------:|--------:|------------:|------------:|-------:|
| 2026-11-12 | 5bf2310 | pg commit | 16 | 9.5 ACU, CPU 32% | 570.0 | 26.0 | 55.0 | 0.0 | 0.0 | 0.0 | 0 |
| 2026-11-12 | 5bf2310 | pg poll | 100 | 14.5 ACU, CPU 88%, 109 connections | 380.6 | 20.2 | 33.9 | 451.5 | 19.7 | 115.8 | 0 |
| 2026-11-12 | 5bf2310 | pg poll notify | 100 | 16 ACU, CPU 100%, 209 connections | 222.4 | 23.7 | 226.5 | 484.8 | 20.7 | 227.6 | 0 |
| 2026-11-12 | 5bf2310 | pg poll | 1000 | 16 ACU, CPU 100%, 1,009 connections | 231.4 | 22.9 | 224.5 | 4275.3 | 21.6 | 417.6 | 0 |
| 2026-11-12 | 5bf2310 | pg poll notify | 1000 | 16 ACU, CPU 100%, 2,009 connections | 0.2 | 381.9 | 124592.8 | 4.9 | 157.1 | 17529.0 | 0 |
| 2026-11-12 | 5bf2310 | pg poll | 3000 | 16 ACU, CPU 100%, 3,009 connections | 19.9 | 104.4 | 15712.1 | 9549.8 | 31.1 | 735.4 | 0 |
| 2026-11-12 | 5bf2310 | pg poll notify | 3000 | 16 ACU | | | | | | | 3,000 listeners refused: `remaining connection slots are reserved` |

**The notification costs the writers, and the cost grows with the
listeners.** The commit row with the notification in the function and
no listener is 570 per second at 16 writers, the rate of the rows
without it. With 100 listeners the writers fall from 381 to 222 per
second beside the same polls per second; with 1,000 listeners they
stall at 0.2 per second with a p99 of two minutes, and the pollers,
with almost no commit to wake them, poll five times a second in all:
Postgres wakes every listening backend on each
commit and serializes the commits through the notification queue. At
3,000 the listeners do not connect: a listener is a connection of its
own, and the cluster has about 3,000 at 16 ACUs.

**So the watch polls.** The listener stays in the store behind
`--postgres-notify`, off by default, for a mesh of a few tens of
members where the idle load of the polls matters more than the
writers. A mesh of hundreds or thousands of members polls every 200
ms, as the rows of 1,000 and 3,000 pollers above measured, and puts
RDS Proxy in front of the cluster for the connections, ADR 0026. The
timer polls of 3,000 pollers take the CPU of a 16 ACU cluster; the
fleet of the target needs a poll interval of a second, spec 4.2, or
a larger cluster, and the row that says which comes before a
deployment at that size.

### Scale on Aurora Serverless v2 (#315)

Method: `SCALE_STORES=postgres devbox run scale` with `SOLAS_POSTGRES_URL`
from `scripts/aws-bench.sh pg up eu-west-1`, on 2026-11-12, from the
VM at 20 ms. The cluster is Aurora Serverless v2 PostgreSQL 18.6 with a
minimum of 2 ACUs and a maximum of 8, and a second series at a maximum
of 16 for the rows that reached 8. Each run lasted 20 seconds with 8
claims per member, and one run 300 seconds. The capacity column is the
maximum of `ServerlessDatabaseCapacity` and `CPUUtilization` in the
minutes of the run, which CloudWatch keeps per minute, so a 20 second
run shares its minute with the one before. The bench pool holds four
connections per member, commit d745a8c; the first series of the day,
with the driver's default of nine for the whole process, gave 61 binds
per second at 8 members with a list p50 of 225 ms, the pool queue and
not the store.

| Date | Commit | Region, connect, capacity | Members | Devices | Run | Binds/s | Conflict % | Bind p50 ms | Bind p99 ms | List p50 ms | List p99 ms | Sync ms | Lag p50 ms | Lag p99 ms | Writes/tx | Errors |
|------|--------|---------------------------|--------:|--------:|----:|--------:|-----------:|------------:|------------:|------------:|------------:|--------:|-----------:|-----------:|----------:|-------:|
| 2026-11-12 | d745a8c | eu-west-1, 20 ms, max 8, at 2.5 ACU, CPU 28% | 2 | 16 | 20 s | 41.8 | 42.8 | 99.4 | 232.4 | 40.2 | 83.8 | 327.6 | 43.0 | 111.7 | 2.9 | 0 |
| 2026-11-12 | d745a8c | eu-west-1, 20 ms, max 8, at 5.5 ACU, CPU 64% | 8 | 64 | 20 s | 147.7 | 45.6 | 88.8 | 288.0 | 44.3 | 112.4 | 427.0 | 45.6 | 110.1 | 2.7 | 0 |
| 2026-11-12 | d745a8c | eu-west-1, 20 ms, max 8, at 5.5 to 8 ACU, CPU 64 to 100% | 32 | 256 | 20 s | 403.9 | 45.6 | 118.9 | 596.4 | 61.3 | 525.0 | 422.2 | 54.3 | 243.0 | 2.8 | 0 |
| 2026-11-12 | d745a8c | eu-west-1, 20 ms, max 8, at 8 ACU, CPU 100% | 128 | 1024 | 20 s | 110.7 | 46.2 | 1400.6 | 10899.3 | 97.7 | 7295.1 | 1548.1 | 133.2 | 3287.2 | 3.2 | 0 |
| 2026-11-12 | d745a8c | eu-west-1, 20 ms, max 16, at 15.5 ACU, CPU 97% | 128 | 1024 | 20 s | 143.2 | 44.8 | 832.3 | 4774.7 | 413.4 | 5923.4 | 637.5 | 216.1 | 2699.2 | 2.5 | 0 |
| 2026-11-12 | d745a8c | eu-west-1, 20 ms, max 8, at 8 ACU, CPU 100% | 2 | 10000 | 20 s | 113.9 | 0.0 | 34.0 | 61.5 | 48.5 | 88.2 | 323.9 | 42.8 | 96.8 | 5.1 | 0 |
| 2026-11-12 | d745a8c | eu-west-1, 20 ms, max 8, at 8 ACU, CPU 76% | 8 | 10000 | 20 s | 378.4 | 0.2 | 37.7 | 103.9 | 61.3 | 167.8 | 428.3 | 47.8 | 138.2 | 3.6 | 0 |
| 2026-11-12 | d745a8c | eu-west-1, 20 ms, max 16, at 8 ACU | 2 | 10000 | 20 s | 79.0 | 0.1 | 45.4 | 93.9 | 75.6 | 141.3 | 543.2 | 62.4 | 132.8 | 5.6 | 0 |
| 2026-11-12 | d745a8c | eu-west-1, 20 ms, max 16, at 8 ACU, CPU 26% | 8 | 10000 | 20 s | 280.6 | 0.3 | 48.6 | 128.1 | 88.0 | 186.3 | 542.1 | 66.5 | 153.9 | 3.9 | 0 |
| 2026-11-12 | d745a8c | eu-west-1, 20 ms, max 8, at 8 ACU, CPU 59% | 8 | 64 | 300 s | 144.1 | 44.7 | 89.0 | 327.3 | 52.8 | 128.1 | 424.9 | 50.9 | 139.8 | 2.7 | 0 |

**The bind rate grows with the members until the cluster's CPU.** 42
binds per second at 2 members, 148 at 8, and 404 at 32, against 12, 11,
and 15 on DynamoDB from the same VM and 58 at 8 members from inside
the region. A bind on Postgres pays about five round trips, 100 ms at
20 ms each, so each member binds at the pace of the round trip, and
more members add binds until the store is busy: at 32 members the
cluster was at its maximum of 8 ACUs with the CPU at 100 percent. The
32 member row is 290 transactions per second on the Device resource,
half the commit ceiling of the row lock measured alone, "Postgres"
above. The five minute run at 8 members gives the same rate as the 20
second one, 144 against 148, so the numbers are not a warm cluster.

**128 members need more capacity, and then the VM.** At a maximum of 8
ACUs the 128 member row fell to 111 binds per second with the CPU at
100 percent and a bind p99 of 11 seconds. At a maximum of 16 the
cluster took 15.5 ACUs and gave 143, with the bind p50 at 0.8 seconds
and the list p99 at 6 seconds; 128 servers with their caches on a VM
with nine CPUs is the other half of that row, as the DynamoDB rows at
128 members found.

**10,000 devices.** The list stays under 90 ms at the median at 2 and
8 members from the cache, a member syncs the pool in 0.3 to 0.5
seconds, and the binds are 114 to 378 per second with no conflicts,
more than the 64 device rows: the claims rarely pick the same device.
The rows at 16 are lower than the rows at 8 by a fifth to a third,
within the noise of 20 second runs on a cluster that scales.

**What it costs.** The cluster ran for about an hour at 2 to 16 ACUs,
under three dollars with the storage, and `pg down` deleted it.

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

## Bind burst

Method: `scripts/burst.sh <workers> [claims]`, issue #268. One k3d
cluster gets 50 devices and then 50 claims at once, and the script
measures the time until every claim is Bound, with the claim workers of
`--claim-workers`. Each bind is a conditional write, spec 6.3, and the
store carries the writes of a server that arrive together in one
transaction, spec 2.3, so several workers share a round trip. The lease
is 10 seconds, as in the demo.

| Date | Commit | Store | Workers | Claims | Seconds to all Bound | Binds/s |
|------|--------|-------|--------:|-------:|---------------------:|--------:|
| 2026-11-09 | cde2cd8 | dynamodb-local | 1 | 50 | 5.7 | 8.8 |
| 2026-11-09 | cde2cd8 | dynamodb-local | 4 | 50 | 2.3 | 21.7 |
| 2026-11-10 | aa5d7c1 | DynamoDB eu-west-1, about 40 ms | 1 | 50 | 8.6 | 5.8 |
| 2026-11-10 | aa5d7c1 | DynamoDB eu-west-1, about 40 ms | 4 | 50 | 6.9 | 7.2 |
| 2026-11-10 | d8fecb7, the poll hint alone | DynamoDB eu-west-1, about 40 ms | 1 | 50 | 8.1 | 6.2 |
| 2026-11-10 | d8fecb7, the poll hint alone | DynamoDB eu-west-1, about 40 ms | 4 | 50 | 7.7 | 6.5 |
| 2026-11-10 | d8fecb7 | DynamoDB eu-west-1, about 40 ms | 1 | 50 | 8.1 | 6.2 |
| 2026-11-10 | d8fecb7 | DynamoDB eu-west-1, about 40 ms | 4 | 50 | 4.5 | 11.1 |
| 2026-11-10 | 55e3a3b | DynamoDB eu-west-1, about 40 ms | 1 | 50 | 5.2 | 9.6 |
| 2026-11-10 | 55e3a3b | DynamoDB eu-west-1, about 40 ms | 4 | 50 | 2.4 | 20.8 |
| 2026-11-12 | d745a8c | Postgres, Aurora eu-west-1, 20 ms | 1 | 50 | 6.3 | 7.9 |
| 2026-11-12 | d745a8c | Postgres, Aurora eu-west-1, 20 ms | 4 | 50 | 2.3 | 21.7 |

On Aurora Serverless v2 from the VM the burst takes the time it takes
on dynamodb-local: 6.3 seconds with one worker and 2.3 with four, the
round trips of a bind and nothing else, because the row lock cancels
no transaction.

With one worker a cluster binds one claim per reconcile, list and write
in turn. Four workers bind two and a half times as fast on a local
store, where the round trip is short and the gain is the parallel lists
and writes. On the real table at aa5d7c1 the gain was a quarter: each
bind paid a consistent list, which waited for the cache to catch up
with the counter through a progress poll, and then its writes.

The server's own histograms say where a bind goes. With
`SOLAS_BURST_METRICS=<dir>` the script dumps `/metrics` of the server
after the burst, and `scripts/metrics-summary.sh` prints the mean of
each request kind. The rows are the four-worker burst unless marked.

| Date | Commit | LIST devices, mean | Watch cache wait, mean | PUT devices/status, mean | GET devices, mean |
|------|--------|-------------------:|-----------------------:|-------------------------:|------------------:|
| 2026-11-10 | d8fecb7, the poll hint alone | 27 ms (318 calls) | 22 ms (85 waits) | 190 ms (143 calls) | 109 ms (100 calls) |
| 2026-11-10 | d8fecb7 | 42 ms (190 calls) | 28 ms (83 waits) | 155 ms (108 calls) | 28 ms (100 calls) |
| 2026-11-10 | d8fecb7, one worker | 40 ms (146 calls) | 27 ms (57 waits) | 99 ms (100 calls) | 28 ms (100 calls) |
| 2026-11-10 | 55e3a3b | 31 ms (172 calls) | 21 ms (69 waits) | 62 ms (105 calls) | 21 ms (100 calls) |
| 2026-11-10 | 55e3a3b, one worker | 27 ms (179 calls) | 21 ms (54 waits) | 46 ms (100 calls) | 63 ms (100 calls) |

The poll hint of spec 4.2 took the list to one round trip: the server
polls at once with the counter value it read for the list, and the
cache waits one query instead of a progress request and a second
counter read. That alone changed the burst little, because the bind's
status write cost four round trips: a read of the device, a read of
the member policy, the counter read, and the transaction. The
registry now runs the limit checks inside the store's update, on the
cached object, and reads the policy from the watch cache, spec 15.1.
The write then cost the counter read and the transaction, and the
four-worker burst bound in 4.5 seconds, 11 binds per second. One
worker stayed at 8 seconds: it binds one claim at a time, and each
costs a list, a status write, and the claim's own write to etcd, in
turn.

The counter guess of spec 2.3 removes the read before the transaction:
the committer tries the last counter value the server read or issued,
and the watchers' polls keep that value fresh. The status write falls
to 62 ms with four workers and 46 ms with one, about one round trip,
and the burst binds in 2.4 seconds with four workers, 21 binds per
second, and 5.2 seconds with one. On a 40 ms table, a bind now costs
the cluster a list of one round trip, a status write of one, and its
own claim write. The GET of 63 ms with one worker is the adopt read
after the bind, which goes to the table because the controller asks for
the newest object; it is not on the bind path. What remains above the
round trip is the controller's own queue. The controller's histograms
of the same bursts:

| Date | Commit | Workers | Reconciles | Reconcile, mean | Queue wait, mean |
|------|--------|--------:|-----------:|----------------:|-----------------:|
| 2026-11-10 | 55e3a3b | 4 | 263 (105 requeue, 158 requeue after) | 39 ms | 356 ms |
| 2026-11-10 | 55e3a3b | 1 | 265 (100 requeue, 165 requeue after) | 28 ms | 1216 ms |

A claim reconciles about five times on its way to `Bound`, and a
reconcile costs one round trip. With one worker a claim waits over a
second in the queue, with four about a third. The next gain for a
one-worker cluster is fewer reconciles per bind; the round trips per
reconcile are at one.

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
| 2026-11-09 | fd95ab6 | etcd, through the gatekeeper | 30 min, seed 1 | 13 store pauses, 26 node pauses, 17 Pod deletes, 15 claims created and deleted | 25 member UID changes, 79 Lost claims created again | 193 events, 91 binds, 94 releases | 0 | 1281 uses accepted, 87 rejected, every rejection for a stale token; 1081 actuator calls accepted, each from the holder of the token at the time |
| 2026-11-09 | dcbdf8e | dynamodb-local, through the gatekeeper, SOAK_LOAD=8 | 30 min, seed 1 | 12 store pauses, 25 node pauses, 15 Pod deletes, 15 claims created and deleted; solas-bench ran 8 more members with 4 claims each | 28 member UID changes, 71 Lost claims created again | 306,985 events, 153,455 binds, 153,456 releases, 32 deletes | 0 | 1243 uses accepted, 34 rejected, every rejection for a stale token; 966 actuator calls accepted, each from the holder of the token at the time |

The second row runs the H100 behind `solas gatekeeper` and the demo
actuator, spec 17. The verifier checks each call the actuator accepted
against the holder of the token in the replayed history.

The third row is the soak on etcd; it needs the live follower of
`pkg/audit`, because the apiserver library compacts etcd every five
minutes. The fourth row is the soak under load: `SOAK_LOAD=8` ran
`solas-bench` as eight more members against the same store, each with
a Member that renews, so the replay audited 306,985 events instead of a
few hundred, with the same faults and the same result. The follower
started its watch again ten times, once per store pause, and lost
nothing. The bench's own row from inside the soak: 85 binds per second
at 8 members with 917 errors, the errors being the twelve store pauses.

The replay covered the whole run: the event retention of the DynamoDB
store is one hour, and the run took 31 minutes. The 81 rejections are
the workloads of clusters whose node was paused for longer than D. Each
one woke up, still believed it held the H100, and used it with its old
token after the other cluster had bound the device with a higher one.
The gatekeeper rejected every such use, spec 6.6. No claim was Suspended
at the end, and no fault step was skipped.
