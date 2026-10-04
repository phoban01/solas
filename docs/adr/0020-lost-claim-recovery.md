# ADR 0020: Recovery of Lost claims

Status: accepted

## Context

"A `Lost` claim MUST NOT bind again" (spec 6.5). A member that joins
again with a new UID cannot take back a device that still names its old
UID, even when no one else took it. The user must delete the claim and
create it again. With the `Retain` and `Delay` reclaim policies (ADR
0016), devices keep naming an old member UID for longer, so this case
becomes common.

## Decision

A rejoined member recovers such a device with one conditional status
update, in the manner of a bind against an offer (ADR 0019):

- The device must name the same member name and the same claim UID, with
  a member UID that no `Member` has. That old identity is gone, so its
  claims are not in effect anywhere.
- The update moves `claimRef` to the member's current UID and raises the
  fencing token. The old identity's paused workload is fenced, as after
  any bind.
- The device is never free, so no other claim can take it. A sweeper
  clear and a recovery are both conditional writes; only one succeeds.
- The `Lost` claim then goes back to `Bound`.

A plain adopt (spec 6.3) still refuses a device with an older member
UID. Only a recovery, with its token raise, may move it.

`quint/solas.qnt` checks recovery: safety holds, a `Lost` claim with a
retained device is `Bound` again with token 2, and a plain adopt of the
device fails.

## Consequences

- With `Retain`, a member that comes back gets its devices back, with
  no operator action.
- The server must check that the old member UID is gone and that the
  claim UID and member name match, below the strategy as well (#139).

## Rejected alternatives

- Let adopt take a device with an older member UID: no token raise, so
  the old identity's workload is not fenced. The negative model
  `adopt-any-uid` shows the result.
- Keep `Lost` final: the device waits for an operator or the sweeper
  even when its holder came back.
