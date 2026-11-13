# ADR 0017: Gatekeepers for actuators

Status: accepted; spec section 17 (2026-11-09)

## Context

A fencing token protects a device only when the thing that changes the
device checks the token (spec 6.6, ADR 0008). The demo device checks it
itself. Real devices are driven through actuators that do not check
tokens, such as a machine provisioning service. A workload of a paused
cluster that still believes it holds a machine can then deploy or power
it while another cluster holds it. Without a check in front of the
actuator, fencing is nominal.

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

### A machine provisioning service

- The device maps to a machine by an attribute that holds the machine ID
  of the service.
- These operations need a token: allocate, deploy, release, power on,
  power off, and any write to the machine, its interfaces, or its
  storage.
- The gatekeeper allocates the machine to its own service account when
  solas binds it, so the owner check of the service also stops a direct
  call from another account.

### Other actuators

Each other actuator needs its own section, with the operations that
change a device, before its gatekeeper is designed.

## What the gatekeeper does not protect

The gatekeeper checks the calls that go through it. Three paths do not:

- **A path around it.** A workload, or anything else in the network,
  that calls the actuator straight skips the check. A network policy
  closes this path: the actuator admits calls from the gatekeeper's
  address only. `deploy/gatekeeper/actuator-policy.yaml` is the
  ingress policy for an actuator that runs as a Pod;
  `demo/k3d/manifests/actuator-policy.yaml` is the egress policy the
  demo uses, because its actuator runs outside the clusters. The mesh
  e2e test sends one call from a pod of a member cluster straight to
  the actuator and expects it to fail, and the same pod reaches the
  gatekeeper (#294).
  An egress policy in the namespaces of the workloads is the weaker form:
  the policy controller of k3s adds a new pod to its rules a moment
  after the pod starts, so the first calls of a new pod pass, and a
  namespace with no policy is open. The ingress policy, or a firewall,
  on the actuator side has neither gap.
- **An actuator with credentials of its own.** An operator, a script,
  or another system that holds the actuator's credentials changes a
  device without a token. The policy does not stop a caller that the
  actuator trusts. The rule is that only the gatekeeper holds the
  actuator's credentials; any other caller needs a gatekeeper of its
  own.
- **A device that an agent on it changes.** An agent on the machine
  acts on the device with no call to the actuator. Fencing protects
  such a device only when the agent checks tokens itself, spec 6.6, as
  the demo device does.

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
- A check in an agent on each device: a machine that is powered off or
  deployed again has no running agent.
