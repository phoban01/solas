# ADR 0019: Transfer between holders, and pre-bound claims

Status: accepted

## Context

A device moves between claims only by a release and a bind, so any
claim can take it in between. There is no way to hand a device to a
named claim, for example to move a job from one cluster to another, and
no way to create a claim for one named device.

## Decision

A transfer is the mirror of preemption (spec 10):

1. The holder sets its claim to `Transferring`. The claim is no longer
   in effect, so its workload stops before the offer exists.
2. The holder offers the device to a named claim with a status update
   that sets `status.offer`, with the claim, its member, and its member
   UID.
3. The named claim binds against the offer. One status update moves
   `claimRef` from the holder to the named claim, raises the fencing
   token, and clears the offer. This is the one exception to the rule
   that a holder must be cleared before another is set (spec 5.3). The
   device is never free, so no third claim can take it between the two
   holders, and the token fences the old holder.
4. The old claim sees that its device names another claim, and lets go
   without a write to the device.

The holder can withdraw an offer that nobody bound; its claim is then in
effect again. The withdraw and the bind are both conditional writes, so
only one succeeds. A sweeper clears an offer to a member UID that is
gone, as it does for a preemption request.

A pre-bound claim names its device in `spec.deviceName`, as a
`PersistentVolumeClaim` names its volume. It binds only that device:
against an offer that names it, or as a normal bind when the device is
free. Section 6.3 adopt accepts a device that an offer moved to the
claim, because the device then names the claim with the current member
UID.

`quint/solas.qnt` models the transfer: safety holds, a third claim
cannot bind during an offer, and a withdrawn offer cannot be bound.

## Consequences

- Recovery of a `Lost` claim (#182) can use the transfer: the rejoined
  member takes the device from its own old identity with a token raise.
- The status strategy and the storage guard need the exception of step
  3, with exactly the claim and member UID that the offer names.
- An offer to a claim that never binds keeps the device out of use until
  the holder withdraws it or the target member is gone.

## Rejected alternatives

- A release by the holder with a reservation for the target, as in
  preemption: the device is free for a moment, and every rule that reads
  "free" must also read the reservation.
- An offer to any claim of a member: the holder would not know which
  workload gets its device.
