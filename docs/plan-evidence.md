# Plan: close the evidence gaps

A review of solas against another design (2026-11-09) found
that solas has the stronger safety design and the weaker evidence. This
plan closes each gap it named, and each step ends with a row in
[benchmarks.md](benchmarks.md) or a test that CI runs. The rule of the
plan: no claim without a number or a gate behind it.

The review rested on the briefs, not on the code, and two of its points
need a correction before the plan:

- The shared store holds five kinds, not two: Device, Member,
  MemberPolicy, MemberUsage, and StoreFormat. MemberPolicy was added in
  commit 2547d87 with six lines in `pkg/apiserver/apiserver.go` and no
  change in `pkg/storage`. The generality is real; it is not written up
  as evidence. M23 does that.
- The fencing token is checked by the demo gatekeeper only. That part
  of the review is right, and M21 is the answer.

## The gaps and the evidence that closes them

| Gap named in the review | Evidence to produce | Milestone |
|-------------------------|---------------------|-----------|
| No real DynamoDB run; the local numbers are an emulator's | Scale rows on a DynamoDB table at 2, 8, 32, and 128 members and at 10,000 devices, with the counter ceiling and the partition limit measured | M20 |
| No latency test | Bind, list, and watch lag against tables in three regions, so the rows cover three RTTs | M20 |
| Watch is a 200 ms poll | Lag and read cost at poll intervals of 100, 200, and 500 ms on DynamoDB; a decision on DynamoDB Streams with a number behind it | M20 |
| The etcd rows ran with no fsync | etcd rows with fsync on, next to the old ones | M22 |
| The gatekeeper is only proposed | ADR 0017 accepted; a `solas gatekeeper` proxy in front of an actuator; an e2e test; a soak that runs through it; the latency it adds, measured | M21 |
| The soak audited 226 events | A soak with load on the store during the faults, so the replay audits thousands of events; a two-hour soak on DynamoDB | M23 |
| Generality is a claim | A recipe for a new kind, the diff of the last kind added as proof, and a CI check that the generic core names no kind | M23 |
| No order across kinds | A written argument, checked by the Quint model, that no invariant of solas needs an order across two resources | M23 |
| Fencing is a Device feature, not a primitive | A holder status type and a generic holder guard that any kind opts in to, spec 5.3, 6.6, and 14 rewritten for "a kind with a holder", proved with a second held kind (#264) | M23 |
| 429 on consistent lists under writes; 1.6 s lists at 10,000 devices | Issues #233 and #237 fixed; the 10,000 device rows measured again | M22 |
| Claims suspend 2 to 4 times D after a store pause (#236) | The suspend within D plus one reconcile, measured again by `TestStoreOutage`, as a row | M22 |
| Wide surface, thin evidence | `docs/confidence.md`: every claim of the README and the ADRs, with the test, model, or row that backs it, or the word "open" | M25 |
| No server-side ownership | ADR 0022 decided and built: a partition per member, IAM and etcd roles per member, and a test that one member's credentials cannot write another member's objects | M24 |
| An operator cannot see conflicts, lag, or sweeps | The metrics and the audit trail of M19 (#225 to #229) | M24 |
| A move into solas, or between stores, has no evidence | Versions that never go back across a move, spec 12.2 and 13.1; a timed live move and its rollback as e2e tests with rows; the pivot in CI; admission and owner references across API servers tested; `docs/migration.md` | M26 |

Issues #232 (etcd at start) and the M19 fault injection issues (#217 to
#220) stay in M19 and run alongside.

## The second review (2026-11-10)

A second review read the rows of M20 to M23 and the proposal of ADR
0025, and a reader of it checked the order argument of ADR 0003. Their
points, and the evidence that answers each:

| Point | Evidence to produce | Issue |
|-------|---------------------|-------|
| The ceiling of 27 binds per second comes from the VM at 67 ms, and a bind costs about one round trip. One row in the table's region says whether the ceiling is the store's or the distance, and it is cheaper than ADR 0025 | The 32 and 128 member rows from an EC2 instance in eu-west-1 | #243 |
| ADR 0025 rests on an item update rate and a transaction rate that nothing measured | Rows for `UpdateItem` on one item and for transactions with no shared item, from the VM, before any code of ADR 0025 | #290 |
| Whether the ceiling is enough is a fleet size question that no page answers | A "Fleet size" section in docs/scale.md: members, binds per minute, and held devices per store, from the rows | #291 |
| ADR 0025 names what it adds, not what it gives up | ADR 0025 states the property it trades (commit order is version order, and the log has no hole), the rule for building it, and the fleet size of today's write path | #293 |
| The two stores compare only at the same round trip | etcd rows from an etcd in the table's region, next to the DynamoDB rows of the same day | #295 |
| The gatekeeper protects the calls that reach it; a fleet needs actuators that only it can reach, and no test proves that | A NetworkPolicy on the actuator and an e2e in which a direct call fails and the same call through the gatekeeper passes; ADR 0017 says what the gatekeeper does not protect | #294 |
| The order argument of ADR 0003 holds for the rules that exist, and leaves three things unsaid: no common snapshot across kinds, the usage entry is a reservation, and the rule for a new kind is a convention | The three points in ADR 0003 and the rule in docs/adding-a-kind.md | #292 |
| The argument rests on the store contract of spec 2.5, and ADR 0025 changes how a list reaches its snapshot | The store model with issue, commit, fence, and the settle wait, and a negative model where the list skips the wait | #281 |
| The exclusion rule is Device-specific | The holder primitive, as planned | #264 |
| Ownership and scoping are plan items | ADR 0022 built, and per-member credentials tested, as planned | #258, #259 |
| No two-hour soak on DynamoDB | As planned | #246 |

Two points of the review need no issue. The lag of a 200 ms poll is
decided in ADR 0004 with the poll interval rows; what the poll costs
under contention is the conflict rate, and every scale row carries it.
And "commit order is version order" is a property that any design
gives up to pass one sequencing point; the open question is the fleet
size at which solas has to, and #291 and #293 put that number on the
page.

## The store decision (2026-11-11)

The rows of M20 and the fleet size of #291 put the write path on
DynamoDB at about 30 transactions per second per resource, 190 to 390
members at the default lease, and a polling watch at the edge of a
partition limit at a few thousand members. The target is 100,000
devices and several thousand member clusters, on a managed service
with nothing to run. DynamoDB reaches that only after three protocol
changes: ADR 0025, lease items off the counter with a sharded sweep,
and a watch change. The cause is one property: the order of spec 2.5
needs a sequencer, and DynamoDB has none, so the server builds one on
a hot item.

ADR 0026 proposes a Postgres store on Aurora Serverless v2, which has
a sequencer, the row lock, and nothing to run. Its rows came first,
with the bench alone (benchmarks.md, "Postgres"): 553 to 602 commits
per second per resource with the order of today kept, twenty times
DynamoDB and the rate of etcd at the same distance; 1,000 pollers hold
the poll interval; 3,000 pollers saturate the CPU, so a fleet of that
size needs a watch woken by `NOTIFY`, which follows the store.

What this changes in the plan:

- ADR 0025 stays proposed and is not built. #279 to #286 are closed as not
  planned, with the rows of #290 as the reason. The lease item idea is
  not filed.
- The Postgres store is a new milestone, M27 (#304 to #319), with the same contract,
  conformance tests, and model-based traces as the other two stores.
  Its issues follow the order of the etcd store: the URL and the spec
  section, the store with its conformance run, the traces, the scale
  rows, the demo, the migration between stores, and then `NOTIFY`.
- #264, M24, M26, and M25 stay as they are, after M27.
- cask (a per-key consensus store with no global revision) was
  assessed and is a rejected alternative of ADR 0026: the order of
  spec 2.5 would need a sequencer register per resource or a rewrite
  of the list and the watch around per-object versions.

## Principles

1. **Spec first, then Quint, then Duvet**, as before. A protocol change
   starts in `spec/solas.md`.
2. **A number has a row.** Every measurement goes into `benchmarks.md`
   with the date, the commit, and the environment. A number in prose
   points at its row.
3. **One heavy job at a time** on the VM. Runs against DynamoDB are
   light on the VM; the store does the work.
4. **Small issues.** Each issue below names its spec sentence, its
   files, and the command that proves it is done.

## M20: Evidence on DynamoDB

Needs an AWS account; the cost is in the last section. Everything runs
from the VM or from one EC2 instance in the table's region.

- **Setup.** `scripts/aws-bench.sh` creates an on-demand table with TTL
  on `expires`, a billing alarm, and prints the storage URL. It deletes
  the table at the end. An IAM policy limits the credentials to tables
  named `solas-bench-*`.
- **Scale series.** `devbox run scale` against the table at 2, 8, and
  32 members with the default pool, from the VM. The rows show what a
  real counter item sustains and where the TransactionConflict retries
  start.
- **The counter ceiling.** `solas-bench` with claims that write without
  a list, so the counter item alone is the limit. The row gives the
  writes per second of one item, against the 1000 per second that ADR
  0003 assumed. A DynamoDB partition sustains 1000 write units per
  second, and a transaction costs two per item, so the plan expected about
  500 transactions per second per resource. The runs of 2026-11-09 found
  12 to 15 binds per second from 50 ms away and a third of that from
  140 ms, bound by the write loop, not the table: writers raced on the
  counter item and one got through per two round trips. Spec 2.3 now
  batches the writes of a server; #267 and #268 take it further. The
  expectation was wrong by a factor of twenty. The item rows of #290,
  done 2026-11-10 with the DynamoDB client alone, put the numbers on
  the page: one item takes 970 updates per second from the VM, and a
  two-item transaction under one partition key stops at 245 per second
  on the write units of the partition, 1,260 per second across
  partition keys (benchmarks.md, "One item and one partition").
- **The partition limit on lists.** All objects of a resource share one
  partition key, so a list of 10,000 devices reads about 10,000 read
  units from one partition, whose limit is 3000 per second. The 10,000
  device run on the table measures this; the fix is M22, and the row
  after M22 shows the difference.
- **128 members.** From one EC2 instance in the region, so the bench
  has the memory and no WAN in the way: 128 members, 8 claims each,
  default pool and 10,000 devices. Done 2026-11-10, with 2 and 8
  members too: 58 binds per second at 8 members, 47 at 32, and 22 at
  128, from 2 ms away, against 27 from the VM at 67 ms. The ceiling is
  the store's, not the distance (benchmarks.md, "In the table's
  region"). An etcd on the same instance gave twenty times the binds
  from the VM ("Same round trip").
- **Latency by region.** The same run against tables in eu-west-1,
  us-east-1, and ap-southeast-1 from the VM. The rows carry the RTT, so
  the bind and list costs read as multiples of it, as WAN
  measurements do.
- **Watch lag against poll interval.** Lag p50 and p99 and reads per
  member per second at `--poll-interval` 100 ms, 200 ms, and 500 ms.
  Then a decision in ADR 0004: keep polling, or add DynamoDB Streams
  behind the same interface, with the measured lag and cost as the
  reason.
- **A soak on DynamoDB.** `devbox run soak` with the two k3d clusters
  on the table for two hours. The replay audits the whole run; the
  event retention must be set above two hours for it.

## M21: The gatekeeper

ADR 0017 moves from proposed to accepted, with the measured cost.

- **Spec.** A section on the gatekeeper: the headers, the check against
  the store, the lock per device, and the pass-through of reads, from
  ADR 0017.
- **`solas gatekeeper`.** A subcommand that proxies an actuator's HTTP
  API. It reads the Device from the solas API of its cluster, accepts a
  request when `claimRef` names the claim of the request and the token
  matches, holds a lock per device from the check to the answer, and
  passes reads through.
- **An actuator to fence.** `solas-demo actuator`: a fake provisioning
  service with deploy, power, and release calls that records every call
  it accepts. It checks nothing itself, as a real one would not.
- **e2e.** The H100 scene runs through the gatekeeper and the actuator:
  the stale holder's deploy is rejected at the gatekeeper and never
  reaches the actuator.
- **Soak.** The soak runs through the gatekeeper and the actuator. The
  verifier reads the actuator's log and checks that every accepted call
  came from the holder of the device at that time, by the replayed
  history.
- **Cost.** The latency the gatekeeper adds to a call, p50 and p99, with
  a warm cache and with a cold one, as a row.

## M22: The list path

- **#237.** Serve consistent lists from the watch cache on DynamoDB.
  The 10,000 device rows run again on dynamodb-local and on the table.
- **#233.** A consistent list that does not fail under sustained
  writes, for the lists that still reach the store.
- **etcd with fsync.** `scripts/local-stores.sh` takes a flag; the
  scale rows run again with fsync on. Both sets stay on the page, so
  the reader sees what fsync costs.

## M23: Soak depth and generality

- **Load during the soak.** `solas-bench` runs against the same store
  during the faults, as more members, so the replay audits thousands of
  events and the takeovers happen under load. The soak row gains the
  bench's columns.
- **Soak on etcd.** `SOLAS_STORE=etcd devbox run soak`, 30 minutes, as a
  row.
- **A recipe for a kind.** `docs/adding-a-kind.md`: the files a new kind
  needs and the files it does not touch, with the diff of commit
  2547d87 as the proof. `scripts/kind-check.sh` fails CI when
  `pkg/storage` or `pkg/apiserver/storage.go` names a kind.
- **A holder primitive (#264).** Today only Device has a holder and a
  fencing token, and the rules live in Device's strategy and guard. A
  shared holder status type and a generic guard that any kind registers
  make the exclusion rule a primitive. Spec 5.3, 6.6, and 14 speak of
  "a kind with a holder"; Device is the first. A second held kind in a
  test, with an empty diff in `pkg/storage`, is the proof.
- **Order across kinds.** A section in ADR 0003: which rules of the
  spec compare two objects, and why each one rests on a conditional
  write or a causal order and not on one counter. The Quint model
  already has one counter per resource; the section names the
  invariants that check it.

## M24: Observability and trust

- **Metrics and the audit trail**, issues #225 to #229, as filed.
- **ADR 0022 decided.** Option 1: a shared catalog with minimal holder
  data, and a partition per member for its own objects.
- **Per-member credentials.** On DynamoDB, an IAM policy with
  `dynamodb:LeadingKeys` on the member's partition; on etcd, a role per
  member. A test with two members' credentials shows that one cannot
  write the other's Member or usage set.
- **#232.** The etcd store starts without etcd.

## M25: The confidence ladder

- **`docs/confidence.md`.** One table: each claim that the README, the
  ADRs, and the spec make, with the model check, the simulator
  property, the test, or the benchmark row that backs it. A claim with
  none says "open". CLAUDE.md already names this file; it does not
  exist yet.
- **`docs/brief.md`.** For engineers and leads who decide whether to
  back solas: the problem, the pattern, the evidence with a link to
  each row, the limits, the cost of a kind, and the open decisions. The
  same shape that a decision brief has, so it reads next to others.

## M26: Migration evidence

A team that adopts solas moves existing state into it, and later may
move the store. The pattern has the pieces: the pivot of ADR 0010 for a
kind that lives as a CRD today, `solas migrate` of ADR 0015 for a move
between stores, and the epoch of ADR 0018 so tokens and versions never
go back. What it lacks is evidence for each, and one rule.

- **#271, versions never go back across a move.** Spec 12.2 lets the
  destination start its versions at 1. A client that keeps a version
  from the source and watches the destination then gets unrelated
  events, or an error, and nothing tells it to list again. Rule: the
  tool moves the destination to a new epoch, spec 13.1, before the
  copy, so every version and token it issues is above every one of the
  source. On etcd the operator bumps the revision first, and the tool
  refuses a destination below the source. Quint: `migrate.qnt` gets a
  version order invariant. Test: a watch from a source version fails
  with `410 Gone` on the destination.
- **#272, a live move, timed.** An e2e with two clusters: seal, copy, switch,
  and every claim back to `Bound` with the same holder and token. The
  row: seconds from the seal to the last `Bound`, on dynamodb-local and
  on a real table.
- **#273, a rollback, tested.** The same e2e unseals before any write to the
  destination and shows the mesh back on the source.
- **#274, the pivot in CI.** `scripts/pivot-smoke.sh` runs by hand today. It
  becomes an e2e, with a rollback step: remove the webhook, and the old
  CRD serves on. Row: the time the webhook adds to a write of the old
  CRD.
- **#275, admission from the hosting cluster.** The server runs the admission
  chain of `k8s.io/apiserver`, so a validating webhook of the hosting
  cluster applies to a `Device`. An e2e shows a webhook denying a write.
- **#276, owner references across API servers.** A local object that names a
  `Device` as its owner: does the garbage collector of the hosting
  cluster handle an owner that an aggregated server serves? An e2e
  answers it, and `docs/adding-a-kind.md` records the answer.
- **#277, `docs/migration.md`.** The playbook: which kinds move, the phases
  with the rollback of each, the write freeze and its length from the
  row, and the rehearsal on k3d.

## M27: The Postgres store

ADR 0026, accepted 2026-11-11, adds a third store behind the storage
URL: Postgres, on Aurora Serverless v2 for a managed deployment and on
a container for the tests and the demo. The contract, the conformance
tests, and the model-based traces are the ones of the other two
stores. The parent is #304. The issues follow the order of the etcd
store, and each one is small enough for one sitting:

- **#305, the spec.** Section 2.8, the Postgres store: the three
  tables, the write as one transaction behind the row lock, the list
  in one snapshot, the watch by polling, and the event expiry by the
  servers. The `postgres` scheme in 2.6; the seal in 12.1 and the
  counter raise in 13.3.
- **#306, the model.** `quint/store.qnt` with the lock order, so the
  list holds with one counter read; the negative model with a sequence
  in place of the lock, which shows a list at a version whose write
  has not committed.
- **#307, the URL.** `postgres://<host>:<port>?database=&sslmode=&create-tables=`,
  with the credentials in `PGUSER` and `PGPASSWORD`.
- **#308, a Postgres for the tests.** A container from
  `scripts/local-stores.sh` and `internal/pgtest`.
- **#309, #310, #311, the store.** The write as one function that
  holds the lock for its own time only, the row of 2026-11-11 says
  why; the list in a `REPEATABLE READ` snapshot; the watch that polls
  the log and deletes the expired events.
- **#312, conformance.** Three subtests per conformance test, and the
  third column of `docs/store-contract.md`.
- **#313, the API server.** RESTOptions for the store, and a start
  without it, ADR 0024.
- **#314, the traces.** The model-based tests on three stores.
- **#315, the scale rows.** `devbox run scale` on the container, then
  on Aurora from the VM at 2 to 128 members and 10,000 devices, with
  the capacity beside each row, and the Postgres column of "Fleet
  size". This row closes #267.
- **#316, the demo.** Postgres in the mesh, and `SOLAS_STORE=postgres`
  for the demo, the e2e, and the smoke test.
- **#317, migrate.** Seal and epoch on the counter rows, a move in
  both directions between each pair of stores, and the playbook.
- **#318, the docs.** Which store for which fleet, and `docs/aurora.md`:
  the capacity, scale to zero, the connection limit, and when RDS
  Proxy is needed.
- **#319, `NOTIFY`.** The pollers woken by the commit, and the 3,000
  poller row again. The row of 2026-11-11 puts the CPU at 100 percent
  at that size with polling alone.

## Order

The second review changes the order. #243 first: it is one EC2 run and
one row, and it says whether the ceiling of 27 binds per second is the
store's or the VM's distance; #295 runs on the same instance. Then #290
and #291 from the VM, and #293 from their rows. ADR 0025 goes ahead,
#279 to #286, only when the in-region row stays under 100 binds per
second at 8 members and the item row puts the item well above it.
Both held on 2026-11-10: 58 binds per second at 8 members in the
region, and 970 updates per second on one item. The item rows also
found the next ceiling, the write units of the partition that holds
the object items of a resource, about 500 commits per second, so ADR
0025 gives about five times the binds of today, not twenty. #293 puts
that next to what the ADR gives up, and the decision on #279 to #286
follows it. The old rule stays for the record: the ADR goes ahead
only on those two rows; otherwise the write path of today stays, with
its fleet size on the page. Then #264, M24 with #294, M26, and M25
last. #233 and #266 of M22
stay open and run alongside.

The decision came on 2026-11-11, "The store decision" above: #279 to
#286 are closed as not planned, and M27 comes first, then #264, M24
with #294, M26, and M25.

## The cost of M20

DynamoDB on-demand in us-east-1 charges $0.625 per million write
request units and $0.125 per million read request units (prices of
November 2024; check the pricing page for the region). A transaction
costs two write units per item, and a bind or a release writes three
items of under 1 KB, so one bind costs about six write units. A
strongly consistent read of an item under 4 KB costs one read unit.

| Run | Writes | Reads | Cost |
|-----|-------:|------:|-----:|
| Scale series, default pool, 3 runs of 20 s | under 1 million | under 1 million | under $1 |
| Counter ceiling, 5 runs of 20 s | about 1 million | small | about $1 |
| 10,000 devices, 4 runs of 20 s, lists from the table | small | about 20 million, throttled by the partition | about $3 |
| 128 members from EC2, 4 runs | about 2 million | about 10 million | about $3 |
| Three regions, 3 runs each | under 1 million each | under 3 million each | about $2 |
| Poll interval series, 3 runs of 60 s | small | about 2 million | under $1 |
| Soak, 2 hours, two clusters | about 20 thousand | about 1 million | under $1 |
| EC2 c6i.2xlarge for the 128 member runs, 4 hours | | | about $1.50 |
| Data transfer out, under 20 GB | | | under $2 |

The expected total is about $15. A billing alarm at $50 and a hard stop
in the script when a run passes 10 million units keep a mistake small.
Every table is deleted at the end of its run, and nothing persists
between runs except the rows on the benchmarks page.

What the account needs: one IAM user or role with a policy limited to
`dynamodb:*` on tables named `solas-bench-*` in the three regions, plus
`cloudwatch:PutMetricAlarm` for the billing alarm, and, for the 128
member runs, `ec2:RunInstances` for one instance that the script
terminates. Credentials come from the environment and are never written
to the repository.
