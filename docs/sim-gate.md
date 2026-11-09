# The simulator gate

The simulator runs the real solas controllers of several member clusters
against in-memory stores. A seed drives every choice. After every step it
checks the safety properties of spec 8.4, the same ones that the Quint
model checks.

Run it with `devbox run sim-gate`. CI runs it on each change to the
controllers, the registry, the storage layer, the simulator, or the
models.

## What runs

- Three clusters, `c0` to `c2`, and two devices.
- Each cluster has the real member manager, claim reconciler, orphan
  sweep, and sweeper from `pkg/controller`.
- Devices and Members live in one shared store, as the table is shared.
  Claims live in a store of each cluster, as they live in its etcd. The
  shared store applies the Device status rules of spec 5.3.
- Each cluster has its own clock. The clocks start far apart and drift
  by up to 1% per step (rho = 0.01). The lease is 30s with a margin of
  3s, and the sweeper runs every 10s.

## What a step does

Each step picks one action: time moves, a member renews, a claim is
reconciled, a sweeper or an orphan sweep runs, a workload uses its
device, a claim is created or deleted, a cluster pauses or wakes up, a
cluster restarts with no memory, or an operator starts a graceful leave.

A device list can be late: with a chance of 0.3, a cluster gets the
device list of its call before. That stands for a list followed by other
steps before the cluster acts on it, which is how a race between a read
and a write looks. Member lists are never late, because the sweeper must
see devices before members (spec 8.3).

A paused cluster does nothing while its clock runs, as a paused node or
a long GC pause would. A workload uses its device whenever its local
claim says `Bound`. It does not check the lease, so after a pause it
acts on an old view. A gatekeeper checks its fencing token (spec 6.6).

## What it checks

- **tokensUnique:** each fencing token of a device belongs to one claim.
- **useMatchesBinding:** the last use a device accepted came from the
  claim that holds that token.
- **leaseSafety:** a member that is live by its own clock still has its
  Member, with its UID.
- **claimMatchesDevice:** a claim that is `Bound`, not being deleted, and
  under a live member, is named by its device.

A second test turns off the Device status rules of spec 5.3 and must
find a violation. It shows that the checks can fail. A third test runs
one seed twice and compares the traces.

## Replay a failure

A failure prints the seed, the step, the broken property, and the last
40 steps:

```sh
SIM_SEED=13 devbox run -- go test ./sim -run TestSimSeed -v
SIM_SEED=13 SIM_FULL_TRACE=1 devbox run -- go test ./sim -run TestSimSeed -v
```

## Settings

| Variable | Default | Meaning |
|----------|---------|---------|
| `SIM_SEEDS` | 400 in the gate, 20 in `go test` | seeds to run |
| `SIM_START` | 1 | first seed |
| `SIM_SHARDS` | 4 | shards, run in parallel |
| `SIM_SHARD` | all | run one shard only |

## What it found

The first run found a gap after a restart. The controller joined with a
new member UID and counted itself live before its old claims were
marked `Lost`. For that moment a claim looked `Bound` on a device that a
sweeper had freed. The Quint model marks claims `Lost` inside the join,
so it never had the gap. The member manager now marks them before it
counts itself live, spec 7.2.

The broken-store test first found nothing. Each reconcile read and wrote
in one step, so no race between two clusters could happen, and the
checks that guard against races were never needed. Late device lists
fixed that: with them, the broken store fails within a few dozen steps.

The store outage test (`TestSimStoreOutage`) first drove only the member
managers and the sweepers through the outage. When it also reconciled
the claims, it found that a `Bound` claim stayed `Bound` through the
whole outage: the reconciler listed the devices before it checked the
lease, and the list failed. The reconciler now suspends the claim before
it reads the store, spec 16.2.
