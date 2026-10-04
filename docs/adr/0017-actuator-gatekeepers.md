# ADR 0017: Gatekeepers for actuators

Status: proposed. The MAAS part is ready. The OCN part waits for its API
(#179).

## Context

A fencing token protects a device only when the thing that changes the
device checks the token (spec 6.6, ADR 0008). The demo device checks it
itself. Real devices are driven through actuators that do not check
tokens, such as MAAS for machines. A workload of a paused cluster that
still believes it holds a machine can then deploy or power it while
another cluster holds it. Without a check in front of the actuator,
fencing is nominal.

## Decision

One gatekeeper sits in front of each actuator, as a proxy of its API.

- **Only the gatekeeper holds the actuator's credentials.** The member
  clusters hold credentials for the gatekeeper only, so every operation
  goes through it.
- **Each request carries the claim and its token**, in the headers
  `Solas-Claim` (member/namespace/name) and `Solas-Fencing-Token`.
- **The highest token is the token in the store.** The gatekeeper reads
  the `Device` from the solas API and accepts a request only when the
  device's `claimRef` names the claim and its `fencingToken` equals the
  token of the request. The token in the store only goes up (spec 5.3),
  so this rule is at least as strict as spec 6.6, and the gatekeeper
  keeps no state of its own: a restart loses nothing.
- **The gatekeeper serializes the requests of each device.** It holds a
  lock per device from the check until the actuator answers, so a bind
  of a new holder cannot slip in between the check and the operation.
  One gatekeeper per actuator keeps the lock local; a second replica
  would need the lock in the store.
- **Reads pass through.** Only operations that change a device need a
  token.

### MAAS

- The device maps to a MAAS machine by the attribute `maas.systemId`.
- These operations need a token: `allocate`, `deploy`, `release`,
  `power_on`, `power_off`, `commission`, `mark_broken`, and any write to
  the machine or its interfaces and storage.
- The gatekeeper allocates the machine to its own MAAS user when solas
  binds it, so MAAS's own owner check also stops a direct call from
  another user.

### OCN

Open. The issue asks for OCN as well. Its API, and which operations
change a device, are needed before this section can name the check.

## Consequences

- Until a gatekeeper exists for an actuator, spec 6.6 and the README say
  that fencing does not protect the devices it drives.
- The gatekeeper is on the path of every change of a device. When it is
  down, devices cannot change, but they keep running.
- The gatekeeper needs read access to Devices in one member cluster, or
  its own storage URL with read-only credentials.

## Rejected alternatives

- A check in each workload: a paused workload is exactly the one that
  acts on an old view.
- A highest token in the gatekeeper's memory or local disk: it is lost
  or forked on a restart or a second replica. The store already has it.
- A check in an agent on each device: a machine that MAAS powers off or
  redeploys has no running agent.
