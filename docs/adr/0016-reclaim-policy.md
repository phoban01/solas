# ADR 0016: Reclaim policy

Status: accepted

## Context

One lease controls two things today: whether a member may bind, and
whether the devices it holds may be taken. A member that loses its lease
for a short time, for example in a network split, loses every device it
holds once the other members sweep it. For some devices, such as one
that holds the state of a long job, that is too quick. An operator wants
to decide.

## Decision

The member lease `D` keeps its meaning: it decides when a member is
gone, and when its claims stop being in effect. A new reclaim policy on
each `Device` decides when the sweeper may clear the `claimRef` of a
member that is gone, as the reclaim policy of a PersistentVolume does:

- `Delete`, the default: the sweeper clears it at once, as today.
- `Delay`: the sweeper clears it only after the reclaim time `R` of the
  device, which it measures on its own clock from the first sweep that
  found the holder gone.
- `Retain`: the sweeper never clears it. An operator releases the device
  with `solas release <device>`, which refuses while the holder's member
  is live and records who released it.

The policy lives on the `Device`, so the owner of the device sets it. A
member cannot keep the devices it holds by its own choice.

A workload still stops using a device when its member is not live, spec
6.5. A retained device stays reserved for its old holder: no other claim
can bind it until an operator releases it, or the old holder takes it
back through recovery (#182).

## Consequences

- A policy only removes or delays a clear, so the safety of spec 8.4
  holds for every policy. `quint/solas.qnt` checks it with a retained
  device, and `solas2delay` checks the delay.
- A retained device of a member that never comes back waits for an
  operator. The device table shows it as held by a member that does not
  exist, and a metric counts such devices (#191).
- `Delay` and `Retain` make the pool smaller during an outage of a
  member. That is the point of them.

## Rejected alternatives

- A second, longer lease per member for reclaim. It is the same as
  `Delay` for every device of the member, and the owner of a device
  cannot choose.
- A policy on the claim. A member would choose `Retain` for every claim.
- Letting a workload keep using a retained device while its member is
  not live. No other claim can take the device, but the member cannot
  see a preemption request or a release by an operator.
