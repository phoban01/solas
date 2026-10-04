# ADR 0021: Per-member limits, enforced in the server

Status: accepted

## Context

One member can bind the whole pool, or send preemption requests as fast
as it likes. Nothing stops it except the code of its own controller.

## Decision

An operator sets limits per member in a `MemberPolicy`, a cluster-scoped
solas resource in the shared store, with the name of the member:
`spec.maxDevices`, `spec.bindsPerMinute`, and
`spec.preemptionsPerMinute`. A member with no policy has no limit.

The server enforces the limits in the request that binds or asks:

- **The usage set.** A storage-level object per member holds the set of
  devices that the member holds or is binding, each with the time it was
  added. Before the device write of a bind, the server adds the device to
  the set with a conditional write, on condition that the set is below
  `maxDevices`. The device write follows in the same request.
- **Two writes, no transaction.** The etcd3 storage updates one key at a
  time, so the server cannot check the set inside the device write.
  Instead, the devices that a member holds are always in its usage set,
  and the set never passes the limit.
- **Release and cleanup.** A clear removes the device from the set. A
  crash between the two writes of a bind leaves an entry with no device:
  that is safe, because it only makes the set too large. The member's own
  server removes such an entry once it is older than a grace that is
  longer than the deadline of a request, by its own clock. A bind still
  in flight then cannot land after its entry is gone.
- **Rates.** The same object holds a token bucket for binds and one for
  preemption requests, updated in the same conditional write. Rates are
  a fairness rule, not a safety rule.
- **Errors.** A bind over `maxDevices` gets `403 Forbidden`, with the
  count and the limit. A bind or request over a rate gets
  `429 Too Many Requests`, with a `Retry-After`.

`quint/limits.qnt` checks that no member holds more devices than its
limit, with requests that have a deadline. The negative models
`no-reservation` and `early-cleanup` show the limit broken by a bind with
no entry, and by a cleanup that does not wait out the deadline.

## Consequences

- A `MemberPolicy` is a write to the shared store, so the RBAC of each
  member cluster must keep it to operators. A cluster admin of a member
  can still change it there; the real boundary is read and write scoping
  of the store, ADR 0022.
- A bind is one write more.

## Rejected alternatives

- A count of held devices that the server reads in the device write: two
  binds that run at once both read the old count.
- A transaction across the device item and a count item: DynamoDB has
  one, the etcd3 storage of `k8s.io/apiserver` does not, and the store
  contract (spec 2.5) has none.
