# ADR 0004: Watch polls a TTL event log

Status: accepted

## Context

Kubernetes clients and informers depend on watch. The store must deliver
every change after a given resource version, in order. When it cannot,
it must return `410 Gone`, so that the client lists again.

## Decision

Each write appends an event item to the resource's event log in the same
transaction. Event items expire through DynamoDB TTL. A watcher reads the
counter, then queries the log for the events after its last version, up
to the counter value. A gap in the result means expired events, and the
watch ends with `410 Gone`.

## Consequences

- Watch reuses the same strongly consistent reads as the rest of the
  store. It needs no other AWS service.
- Polling adds latency, bounded by the poll period.
- Each poll costs a read. The API server watch cache runs one poller per
  resource, not one per client.
- TTL deletes items late and in no set order. The gap check handles this.

## Rejected alternatives

- DynamoDB Streams. Streams give push delivery, but they add shard
  handling, a 24-hour retention, and extra LocalStack behavior to trust.
  We can add them later behind the same interface.
- No event log, with a full list on each poll. This loses the order and
  the delete events that watch must deliver.

## Decision kept: polling at 200 ms (2026-11-09)

The rows of docs/benchmarks.md, "Poll interval", measured the watch on a
real table in eu-west-1 at poll intervals of 100, 200, and 500 ms with
8 members: lag p50 of 65, 93, and 154 ms, lag p99 of 140, 233, and
541 ms, and 49, 40, and 23 store reads per member per second. The lag is
about the round trip plus half the interval. A lease of 30 seconds with
a renew every 10 does not feel a lag of a tenth of a second, and the
soak and the e2e tests show the reclaim and the fencing work with it.

DynamoDB Streams would cut the lag to the stream's own delivery and the
polls to zero, at the cost of shard handling, a 24 hour retention, and a
second delivery path to trust. Nothing in the rows asks for it. The
default stays 200 ms; `--poll-interval` moves it for a mesh that wants
less lag or fewer reads. This decision is revisited when a row shows the
polls as a cost that matters, or a workload needs a lag below 100 ms.
