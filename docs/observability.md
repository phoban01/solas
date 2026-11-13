# Observability

A solas server tells an operator what it does in three ways: metrics at
`/metrics`, Events on the claims of its cluster, and structured log
lines for reclaim. This page lists each one. `TestMetricsDoc` in
`pkg/cmd/solas` starts solas and fails when `/metrics` does not list a
metric of this page, so the page and the code do not drift apart.

The server also serves the metrics of `k8s.io/apiserver`: request
latency, the watch cache, and the workqueues of the controllers. They
are not listed here.

## Metrics

All `solas_` metrics are on the registry that the API server serves at
`/metrics`, on its secure port.

### The store

| Metric | Type | Labels | What it counts |
|--------|------|--------|----------------|
| `solas_storage_conflicts_total` | counter | `resource`, `cause` | Writes and lists that hit a conflict. `cause` is `counter` (the writers of a resource raced on the counter, spec 2.3, DynamoDB only), `object` (a write whose object changed since it was read, spec 2.5), or `list` (a list that read two counter values and tried again, spec 2.4, DynamoDB only). |
| `solas_watch_lag_seconds` | histogram | `resource` | Time from the write of an event to its delivery by a watch of this server. The poll interval is the floor; a value well above it means the store is slow or the server is busy. |

A high rate of `object` conflicts on `devices` is the race of claims
for the same devices, spec 6.3, and is expected under load. A high
rate of `counter` conflicts on DynamoDB is the ceiling of one counter
item, docs/scale.md.

### The member

| Metric | Type | Labels | What it counts |
|--------|------|--------|----------------|
| `solas_member_renew_seconds` | histogram | | Time of each renew of this member, spec 7.3. A renew that takes longer than the lease margin `M` puts the member at risk of a sweep. |
| `solas_member_renew_errors_total` | counter | `cause` | Renews that failed. `cause` is `sealed` (the store moved, spec 12.1), `unavailable` (the store did not answer, spec 16.1), or `other`. |
| `solas_store_sealed` | gauge | | 1 while the last renew found the store sealed for a move, 0 otherwise. A member that shows 1 must switch to the new store within `D`, spec 12.4. |

### The sweeper

| Metric | Type | Labels | What it counts |
|--------|------|--------|----------------|
| `solas_sweeper_members_observed` | gauge | | Members in the last member list of the sweeper, this member included. |
| `solas_sweeper_members_swept_total` | counter | | Expired Members that the sweeper of this member deleted, spec 8.2. |
| `solas_sweeper_references_cleared_total` | counter | `kind` | References of gone members that the sweeper cleared from devices. `kind` is `claimRef` (spec 8.3), `preemption` (spec 10.9), or `offer` (spec 14.3). |

Each sweeper counts what it did itself. The sum over all members is
what the mesh reclaimed.

### Claims

| Metric | Type | Labels | What it counts |
|--------|------|--------|----------------|
| `solas_claims_binds_total` | counter | | Claims of this member that went `Bound`, spec 6.3. |
| `solas_claims_preemption_requests_total` | counter | | Preemption requests that claims of this member wrote on devices, spec 10.5. |

## Events

The claim controller records an Event on the claim for each step of
its life. Each Event names the device and the fencing token, so
`kubectl describe deviceclaim` shows the history of a claim and the
tokens it held. The Events use the `events.k8s.io` API, and the device
is the related object where the controller has it.

| Reason | Type | When |
|--------|------|------|
| `Bound` | Normal | The claim bound a device, spec 6.3. |
| `Suspended` | Warning | The member is not live or is draining, and the claim stopped being in effect, spec 6.5. |
| `Resumed` | Normal | The member renewed, and the claim is `Bound` again on the same device. |
| `Released` | Normal | The claim was deleted, and the controller cleared the device, spec 6.4. |
| `PreemptionRequested` | Normal | The claim asked the holder of a device to release it, spec 10.5. |
| `Preempted` | Warning | The claim released its device to a claim of higher priority, spec 10.6. |
| `Offered` | Normal | The claim offered its device to a named claim, spec 14.1. |
| `Transferred` | Normal | The named claim bound against the offer, spec 14.2. |
| `Recovered` | Normal | The member joined again and took back its device under its new UID, spec 14.5. |
| `Lost` | Warning | The member joined again with a new UID, and the claim lost its device, spec 6.5. |

## Log lines

The sweeper logs one structured line for each change it makes, spec
8.2 and 8.3. Each line has an `event` field to filter on. These lines
are the audit of reclaim: each names what the sweeper changed and whose
it was.

| `event` | Fields | When |
|---------|--------|------|
| `member-deleted` | `member`, `memberUID`, `resourceVersion`, `lease` | The sweeper deleted an expired Member, spec 8.2. |
| `claimref-cleared` | `device`, `member`, `memberUID`, `claim`, `fencingToken` | The sweeper freed a device whose member is gone, spec 8.3. |
| `preemption-cleared` | `device`, `member`, `memberUID`, `fencingToken` | The sweeper cleared a preemption request of a gone member, spec 10.9. |
| `offer-cleared` | `device`, `member`, `memberUID`, `fencingToken` | The sweeper cleared an offer to a gone member, spec 14.3. |

With the JSON log format of the server, one query finds every device a
member lost to reclaim:

```sh
kubectl -n solas-system logs deploy/solas | jq 'select(.event == "claimref-cleared" and .member == "cluster-b")'
```
