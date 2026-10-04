# ADR 0023: Inventories and OS images stay out of the shared store

Status: accepted

## Context

Device and Member live in the shared store. Every cluster also needs
other data: an inventory of the hardware, such as rack, serial number,
and firmware, and a set of OS images to install on a device. Each kind
of data can live in solas, or be copied to each cluster.

The shared store costs something for each resource. A DynamoDB item
holds at most 400 KB, and an event item holds the object and the
previous object (spec 5.1). Each write of a resource takes the counter
item of that resource, ADR 0003. While the store is down, a cluster can
read none of it.

We judge each kind of data by five questions:

1. Does a write need one order across clusters, or a conditional write
   on a value that another cluster can change?
2. How often does it change?
3. Does it fit in an item, with room for the previous object?
4. How much load does it put on the counter item of its resource?
5. What does a cluster do with it while the store is down?

## Decision

A resource goes in the shared store only when its writes need one order
across clusters. A lease, a claim, and a fencing token need this. Data
that one writer publishes and every cluster reads does not.

**Inventory.** The inventory of one device goes in `spec.parameters` of
its Device, spec 5.1. There is no separate inventory resource.

- Order: the owner of the inventory writes it, and no cluster writes it
  on a condition. It needs no order of its own. It is in solas only
  because the Device is.
- Change: it changes when hardware changes, a few times a year for each
  device.
- Size: one device's record is a few KB, well under the 64 KiB cap.
- Counter: an inventory write is a Device write, rare against binds.
- Outage: a cluster keeps the last copy in its watch cache, and binds
  stop anyway.

A pivot copies the spec of an old inventory CRD into `spec.parameters`,
spec 9.1. A whole inventory as one object fails the size question for a
large pool.

**OS images.** OS images do not go in solas. The image files go in an
image registry or an object store. The list of images, with a name, a
version, a URL, and a checksum, goes in a CRD in the local etcd of each
cluster. A GitOps tool copies it to each cluster from one Git
repository.

- Order: a release adds an image. No cluster writes the list, so no
  write needs an order across clusters.
- Change: a few times a month.
- Size: an image is many MB and can never fit in an item. The list fits,
  but it does not need the store.
- Counter: no load.
- Outage: a cluster must keep installing devices that it holds while the
  store is down. With a local list, it can.

A device names the images it accepts by name in `spec.parameters`. Each
cluster resolves a name with its local list.

## Consequences

- solas stays small: it holds only the state that needs one order.
- A new kind of global data gets the same five questions before it goes
  in the store.
- The image list in two clusters can differ for a short time during a
  rollout. A controller that uses an image should resolve its name before
  it binds a device, not after.

## Rejected alternatives

- An `OSImage` resource in solas: it adds a counter item and an outage
  dependency, and gives nothing that Git does not give.
- One `Inventory` object for the pool: it passes the item size limit for
  a large pool, and each change rewrites the whole object.
- A separate inventory resource for each device: it doubles the objects
  in the store and needs a join on each read, with no gain over
  `spec.parameters`.
