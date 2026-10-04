# ADR 0010: Pivot Devices from an etcd CRD into solas

Status: accepted

## Context

A cluster can already keep devices as a CRD in its own etcd, for example
`devices.inventory.example.com`. To join the mesh, those devices must
become solas `Device` objects, which live in the shared table. Clients
that write the old CRD should keep working while the move happens.

An admission webhook cannot change the kind or group of an object. So a
webhook cannot turn a write of the old CRD into a write of a solas
Device. It can write the solas Device itself, and it can patch the old
object.

## Decision

`solas-pivot` is one command with three parts.

- `copy` reads each object of the old CRD and creates or updates the
  matching solas Device. It is idempotent.
- `verify` checks that each old object has a matching Device.
- `webhook` is a mutating admission webhook on the old CRD. On a create
  or an update, it writes the matching Device first, then patches the
  old object with an annotation that names the Device. On a delete, it
  deletes the Device, or denies the delete when the Device is bound.

The old object and the Device name each other in annotations. The
Device gets the name, the labels, the annotations, and the description
of the old object. It also gets the whole `spec` of the old object in
`spec.parameters`, so no field is lost. It does not get the status. A
pivoted device starts free, because a claim on the old CRD is not a
solas claim.

## Consequences

- Clients of the old CRD keep working. Each of their writes reaches
  solas before the write to etcd is stored.
- The webhook writes the Device before the API server stores the old
  object. If the later write fails, the Device can be ahead of the old
  object for a while. The next write, `copy`, or `verify` finds it.
- The webhook uses `failurePolicy: Fail`, so no write of the old CRD
  skips solas.
- A delete of the old object fails while its Device is bound, because
  solas rejects the delete of a bound device (spec 5.4).

## Rejected alternatives

- Copy only, and remove the old CRD by hand. Clients that still write
  the old CRD would write to a copy that nobody reads.
- Serve the old group from solas. solas would have to serve any type,
  which is a much larger change.
- Write straight to the DynamoDB table. That skips the validation and
  the resource versions of the solas API.
