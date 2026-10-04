# Solas specification

## 1. Introduction

### 1.1. Scope

This document specifies solas. Solas is a Kubernetes extension API
server that stores global resources in a shared store: a DynamoDB table
or an etcd cluster. Several member clusters share one store and so
share one view of the global resources. The document also specifies the
controller that binds devices to claims.

### 1.2. Terms

- **Member cluster**: a Kubernetes cluster that runs the solas API server
  and the solas controller.
- **Store**: the one DynamoDB table or etcd cluster that all member
  clusters share.
- **Table**: the store, when it is a DynamoDB table.
- **Resource**: a group and resource name, for example
  `solas.dev/devices`.
- **Write**: a create, update, or delete of one object.
- **Resource version**: the `metadata.resourceVersion` of an object, or
  the version of a list or a watch.
- **Holder**: the claim that a `Device.status.claimRef` names.
- **Observer**: a member cluster that measures the lease of another
  member.

### 1.3. Conventions

The requirement key words of RFC 2119 and RFC 8174 have the meaning given
there when they appear in all capitals.

Each normative sentence holds one requirement. Code cites the sentence
with a Duvet annotation.

## 2. Storage

Sections 2.1 to 2.4, 3, and 4 describe the DynamoDB store. Section 2.7
describes the etcd store. Section 2.5 holds the rules that every store
keeps.

### 2.1. Table

All member clusters MUST use the same table.
The table MUST be in one AWS region.
The table MUST have a string partition key named `pk` and a string sort
key named `sk`.
The table MUST enable TTL on the attribute `expires`.
The table name MUST be a setting of the API server.
The default table name is `solas`.

### 2.2. Items

The table holds three kinds of item: object items, counter items, and
event items.

An object item MUST have `pk` equal to `obj#` followed by the resource.
An object item MUST have `sk` equal to the part of its storage key after
the resource name.
The sort key starts with `/`, so it is never empty.
For a namespaced object, `sk` is `/`, the namespace, `/`, and the name.
For a cluster-scoped object, `sk` is `/` and the name.
An object item MUST hold the resource version of its last write in the
number attribute `rv`.
An object item MUST hold the encoded object in the binary attribute
`value`.

A counter item MUST have `pk` equal to `rv` and `sk` equal to the
resource.
A counter item MUST hold the last issued resource version in the number
attribute `n`.

An event item MUST have `pk` equal to `ev#` followed by the resource.
An event item MUST have `sk` equal to its resource version as a decimal
string, padded with zeros to 20 digits.
The padding makes the string order of `sk` equal to the number order of
the versions.
An event item MUST hold the event type in the attribute `type`.
The event type MUST be one of `INIT`, `ADDED`, `MODIFIED`, or `DELETED`.
An `ADDED`, `MODIFIED`, or `DELETED` event item MUST hold the encoded
object in `value`.
An event item for a `MODIFIED` or `DELETED` event MUST hold the encoded
previous object in `prev`.
An event item MUST hold its expiry time, in Unix seconds, in `expires`.

### 2.3. Writes

Every write MUST be one `TransactWriteItems` call.
The call MUST hold three actions: a counter update, an object action, and
an event put.

The counter update MUST set `n` to `n + 1` on condition that `n` still
has the value that the server read.

For a create, the object action MUST be a put on condition that the
object item does not exist.
For an update, the object action MUST be a put on condition that `rv`
equals the version that the server read.
For a delete, the object action MUST be a delete on condition that `rv`
equals the version that the server read.

The event put MUST be on condition that the event item does not exist.

Each call MUST set a `ClientRequestToken`.
The token makes a retry after a lost reply safe, because DynamoDB does not
apply the same token twice.

If the transaction fails only on the counter condition, the server MUST
read the counter again and retry the same write.
The server SHOULD wait a short random time before each such retry.
If the transaction fails on the object condition of a create, the server
MUST return `409 AlreadyExists`.
If the transaction fails on the object condition of an update or a
delete, the server MUST read the object again and retry.
The retry runs the preconditions and the update function again, on the
new state.
When a request carries a resource version that no longer matches, the
Kubernetes generic registry returns `409 Conflict`.

The server MUST reject a write whose event item would exceed the
DynamoDB item size limit.
The server MUST return `413 RequestEntityTooLarge` for such a write.

### 2.4. Reads

Every read of an object item MUST be a strongly consistent read.
Every read of a counter item MUST be a strongly consistent read.
Every query of an event log MUST be a strongly consistent query.

A get MUST return the object from its object item.

A list MUST read the counter before it queries the object items.
A list MUST read the counter again after the query.
If the two counter values differ, the list MUST retry.
If the two counter values are equal, the list MUST return that value as
its resource version.
A counter that does not change during the query shows that no write
committed during the query.
So the list is a consistent snapshot at that version.

The store keeps only the latest state of each object.
A request for a list at an older, exact resource version MUST be served
by the watch cache, or it MUST fail with `410 Gone`.

### 2.5. Store contract

Sections 5 to 10 depend only on the rules of this section.
Every store MUST keep them.

A conditional write MUST apply only when the object still has the
resource version that the writer read.
A write MUST apply in full or not at all.
A create MUST fail when an object with the same key exists.
A read MUST see every write that completed before the read started.
A list MUST be a consistent snapshot at the resource version that it
returns.
The resource version of each write to a resource MUST be greater than the
resource version of every earlier write to that resource.
A watch from a resource version MUST deliver every later change of the
resource, in order, or it MUST fail with `410 Gone`.

### 2.6. Storage URL

The store MUST be a setting of the API server, in the form of one
storage URL.
A URL with the scheme `dynamodb` MUST select the DynamoDB store.
The host part of a `dynamodb` URL is the table name.
An empty table name selects the default table name.
The query key `region` sets the AWS region of the table.
The query key `endpoint` sets the URL of the DynamoDB endpoint.
The query key `create-table` with the value `true` makes the server
create the table at start when it does not exist.
A URL with the scheme `etcd` MUST select the etcd store.
The host part of an `etcd` URL is a list of `host:port` endpoints,
separated by commas.
The server MUST reject a storage URL with another scheme.
The server MUST reject a storage URL with a query key that this section
does not name for its scheme.
All member clusters MUST use the same store.
The URLs of two member clusters can differ only in the address by which
each cluster reaches the store.

### 2.7. etcd store

The etcd store MUST use the etcd3 storage of `k8s.io/apiserver`.
The etcd store MUST keep each object under the storage prefix of the API
server.
Every read of the etcd store MUST be a linearizable read.

etcd gives each write one revision, in one order for the whole cluster.
The etcd store uses that revision as the resource version.
A conditional write is an etcd transaction that compares the revision of
the key.
A watch from a revision that etcd has compacted fails with `410 Gone`.
So the etcd store keeps the rules of section 2.5.

## 3. Resource versions

### 3.1. Issue

Each resource MUST have its own counter item.
Kubernetes does not accept 0 as the resource version of a list.
So the counter of a resource never shows 0.
When the server first uses a resource, it MUST create the counter item
with `n` equal to 1 and an event item of type `INIT` at version 1, in one
transaction.
The first write to a resource MUST get resource version 2.
Each write MUST get a resource version exactly one more than the
previous write to the same resource.
The write transaction in section 2.3 gives this, because the counter
update and the object action commit together or not at all.

### 3.2. Order

A transaction for version `n + 1` cannot commit before the transaction
for version `n`, because its counter condition needs the value `n`.
So when the counter shows the value `c`, every write with a version up to
`c` has committed.

The resource version of a resource MUST strictly increase in commit
order.
The server MUST NOT compare resource versions of two different resources.

### 3.3. Objects

The `metadata.resourceVersion` of an object MUST equal the version of the
write that last changed it.
The `rv` attribute of the object item MUST equal the same version.
The object in a `DELETED` event MUST carry the version of the delete.
The server MUST encode each resource version as a decimal string.
Kubernetes clients treat a resource version as an opaque string.

## 4. Watch

### 4.1. Event log

Each write MUST add one event item to the event log of its resource.
The event item MUST have the same resource version as the write.
An event item MUST expire a set retention time after its write.
The retention time MUST be a setting of the API server.
The default retention time is one hour.
The server MUST NOT depend on the time at which DynamoDB deletes an
expired item.
DynamoDB can delete expired items late and in any order.

### 4.2. Poll

A watch from version `r` MUST deliver every event with a version greater
than `r`.
A watch MUST deliver events in version order.
A watch MUST NOT deliver the same event twice.
A watch MUST NOT deliver an `INIT` event.

Let `last` be the version of the last event that the watch delivered, or
`r` before the first event.
Each poll MUST read the counter first.
Let `c` be the counter value.
If `c` equals `last`, the poll MUST return no events.
If `c` is greater than `last`, the poll MUST query the event log for the
versions from `last + 1` to `c`.
Every write up to `c` has committed before the query starts, so a
complete log holds each of these versions.

The server SHOULD poll each resource at least once each second.
The server SHOULD NOT poll a resource more often than every 100
milliseconds.
The server SHOULD run one poller for each resource and share it between
watchers.
The API server watch cache gives this sharing.

### 4.3. Gaps

If the query result does not hold each version from `last + 1` to `c`
exactly once, the watch MUST end with `410 Gone`.
A gap means that DynamoDB has deleted expired events.
A client that gets `410 Gone` lists again and starts a new watch, as
Kubernetes requires.

### 4.4. Start

A watch with no resource version MUST first list the current state.
It MUST then deliver an `ADDED` event for each object in the list.
It MUST then continue from the version of the list.

## 5. Device

### 5.1. Resource

`Device` MUST be a cluster-scoped resource in the group `solas.dev`,
version `v1alpha1`.
The solas API server MUST serve `Device` from the table.
`Device` MUST have a `status` subresource.
Claims select devices by their labels.
`Device.spec.description` holds free text that describes the device.

### 5.2. Claim reference

`Device.status.claimRef` names the holder of the device.
A `claimRef` MUST hold the member name, the member UID, the claim
namespace, the claim name, and the claim UID.
A device is free when it has no `claimRef`.
A device is bound when it has a `claimRef`.
`Device.status.fencingToken` holds the fencing token of the last bind.

### 5.3. Status updates

The server MUST NOT accept an unconditional update of `Device` status.
Each status update MUST carry the resource version that the client read.
The server MUST reject a status update that changes `claimRef` from one
holder to a different holder.
A holder is different when any field of `claimRef` differs.
To move a device, a client MUST first clear `claimRef` and then set it
in a second update.
The one exception is a bind against an offer, section 14.2.
When a status update sets `claimRef` on a free device, the server MUST
set `status.fencingToken` to the old value plus 1.
The first bind of a device gets token 1.
The server MUST ignore the `status.fencingToken` that a client sends.
In every other status update, the server MUST keep the old token.
The token of a device never goes down, so a later bind always has a
higher token.
The server MUST reject a status update that sets a `claimRef` with an
empty field.

A spec update MUST NOT change `status`.
A status update MUST NOT change `spec`.

### 5.4. Delete

The server MUST reject a delete of a bound device.
The server MUST check this on the state that the delete removes, using
the delete condition on `rv` from section 2.3.
So a bind that commits before the delete makes the delete fail.

## 6. Claim

### 6.1. Resource

`DeviceClaim` MUST be a namespaced CRD in the group `claims.solas.dev`,
version `v1alpha1`.
It cannot share `solas.dev/v1alpha1`, because the aggregator sends that
whole group version to the extension server, ADR 0006.
Each member cluster MUST store its claims in its own etcd.
`DeviceClaim.spec.selector` MUST be a label selector over `Device`
labels.
`DeviceClaim.status.phase` MUST be one of `Pending`, `Bound`,
`Suspended`, or `Lost`.
`DeviceClaim.status.deviceName` names the bound device.
`DeviceClaim.status.memberUID` records the member UID that bound the
claim.
`DeviceClaim.status.fencingToken` records the fencing token of the bind.

### 6.2. Controller

Each member cluster MUST run at most one active claim controller.
The controller MUST use a leader election lease in its own cluster.
The controller MUST NOT bind while its member is not live by its own
clock, as section 7.4 defines.
The controller MUST NOT bind while its member is draining.

### 6.3. Bind

The controller MUST NOT bind a claim that has a deletion timestamp.
Before it binds a claim, the controller MUST add the finalizer
`solas.dev/release` to the claim.
Before it picks a device, the controller MUST do a consistent list of
devices.
If a device in that list has a `claimRef` with the claim UID and the UID
of the member's current `Member`, the controller MUST adopt that device
and MUST NOT bind another.
This covers a crash after the device write and before the claim write.
Before it adopts a device, the controller MUST read the device again with
a consistent read.
It MUST adopt the device only if that read still shows the same `claimRef`.
A list can be older than the last release of the claim, spec 10.6, and
adopt makes no write that the server can reject.
The controller MUST NOT adopt a device whose `claimRef` has an older
member UID.
A bind from an old member identity can land after a rejoin.
The sweeper clears that `claimRef`, as section 8.3 describes.

The controller MUST pick a device from the free devices that match the
selector.
The controller SHOULD pick at random among the matching devices.
Random choice lowers the chance that two clusters pick the same device.

The bind MUST be a status update of the device that sets `claimRef`.
The bind MUST carry the resource version from the consistent list.
The `claimRef` MUST hold the UID of the member's current `Member`.
If the bind fails with `409 Conflict`, the controller MUST list again
and MUST NOT retry with the old version.

After a successful bind, the controller MUST set the claim phase to
`Bound`.
It MUST also set `status.deviceName` and `status.memberUID`.
It MUST also set `status.fencingToken` to the fencing token of the device.
The controller MUST NOT set a claim to `Bound` from a bind that carried
an older member UID.

If more than one device has a `claimRef` with the same claim UID, the
controller MUST keep the device in `status.deviceName`.
It MUST release the other devices as section 6.4 describes.
The controller MUST NOT move a claim to another device while the claim
is `Bound`, because a workload can be using the device.
Two devices can name one claim when an old leader's write lands after a
new leader's list.

### 6.4. Release

When a claim has a deletion timestamp, the controller MUST release its
device.
To release, the controller MUST do a consistent list of devices.
For each device whose `claimRef` has the claim UID, the controller MUST
clear `claimRef` with a status update.
That update MUST carry the resource version from the list.
If the update fails with `409 Conflict`, the controller MUST list again.
The controller MUST NOT change a device whose `claimRef` does not have
the claim UID.
After the release, the controller MUST remove the finalizer.

A bind can land after the release has removed the finalizer.
The controller MUST clear a `claimRef` that names its own member and a
claim UID that does not exist in its cluster.
The clear MUST be a status update with the resource version that the
controller read.

### 6.5. Phases

A new claim MUST start in phase `Pending`.
When the controller's member is not live by its own clock, the
controller MUST set each `Bound` claim to `Suspended`.
When the member renews its lease, the controller MUST set each
`Suspended` claim back to `Bound` if its device still names it.
When the controller finds that its member UID changed, it MUST set each
claim with the old `status.memberUID` to `Lost`.
A `Lost` claim MUST NOT bind again, except by a recovery, section 14.5.
A user deletes a `Lost` claim to free its finalizer.

A claim is in effect while it is `Bound`, it has no deletion timestamp,
and its member is live by the member's own clock.
Workloads MUST use a device only while its claim is in effect.
The safety properties in section 8.4 are about claims in effect.

The controller can set a claim to `Suspended` only some time after its
lease ends, and a paused workload can act on an old phase.
So the phase alone cannot keep an old holder away from a device.
Fencing tokens do, as section 6.6 describes.

### 6.6. Device use

A workload MUST present the fencing token of its claim each time it uses
the device.
The device MUST remember the highest token that it has accepted.
The device MUST reject a use with a token lower than the highest token
that it has accepted.
The device MUST accept a use with a token equal to or higher than the
highest token that it has accepted, and record that token as the highest.
A device that cannot check tokens MUST sit behind a gatekeeper that
checks them.
Fencing protects a device only when every change of the device passes a
token check.
Until a gatekeeper exists for an actuator, fencing does not protect the
devices that the actuator drives.
A gatekeeper MAY use the `fencingToken` of the device in the store as
the highest token, because it only goes up, ADR 0017.

A bind gives the device a higher token, spec 5.3.
So once the new holder has used the device, the device rejects the old
holder, however long the old holder paused.
The old holder can still use the device after a reclaim and before the
new holder's first use.
The device sees these uses in one order, so they never overlap.
The lease margin `M` decides when a sweeper may reclaim.
It does not decide whether two holders use a device at once.

## 7. Membership

### 7.1. Resource

`Member` MUST be a cluster-scoped resource in the group `solas.dev`,
version `v1alpha1`.
The solas API server MUST serve `Member` from the table.
The name of a `Member` MUST be the cluster ID of its member cluster.
`Member.spec.leaseDurationSeconds` MUST hold the lease duration `D`.
`Member.status.renewTime` holds the time of the last renew, as the
holder's clock gives it.
`Member.status.phase` MUST be `Active` or `Draining`.
A member is live while its `Member` exists.

### 7.2. Join

A member cluster MUST create its `Member` before it binds any device.
The server gives each new `Member` a new UID.
The member MUST use the UID of its current `Member` in each `claimRef`
that it writes.

When the controller starts, it MUST try to renew its existing `Member`.
If the renew succeeds, the controller MUST keep the UID of that `Member`.
If the `Member` does not exist, the controller MUST create a new one.
If the new UID differs from the UID in a claim's `status.memberUID`, the
claim is `Lost`, as section 6.5 describes.
After a join, the controller MUST set each claim with another
`status.memberUID` to `Lost` before it treats itself as live.
Otherwise a claim can look `Bound` for a moment under a member that has
lost its device.
This MUST include `Preempting` and `Preempted` claims, because they still
hold a device.

### 7.3. Renew

A renew MUST be an update of `Member.status.renewTime`.
The renew MUST carry the resource version that the member last read or
wrote.
A renew that fails with `404 NotFound` means that a sweeper deleted the
`Member`.
After a `404 NotFound`, the member MUST join again as section 7.2
describes.
A member SHOULD renew every `D / 3`.
The default `D` is 30 seconds.

### 7.4. Lease on the holder

Let `S` be the local time at which the member sent its last successful
renew.
Let `M` be the safety margin.
The member MUST treat itself as not live from local time `S + D - M`.
The member MUST treat itself as live again only after a later renew
succeeds.

### 7.5. Clock drift

Let `rho` be the bound on the rate drift between any two clocks.
The design assumes that `rho` is at most 0.01.
The margin `M` MUST be at least `2 * rho * D`.
The default `M` is `D / 10`.
The default leaves room for scheduling delays on top of drift.
Solas does not depend on synchronized clocks.
Solas depends only on the drift bound.

### 7.6. Leave

To leave, the member MUST first set its phase to `Draining`.
A draining member MUST NOT bind.
The member MUST then set each `Bound` claim to `Suspended`.
The member MUST then release each device that it holds, as section 6.4
describes.
The member MUST NOT release the device of a claim that is still `Bound`.
The member MUST then delete its `Member`.

## 8. Reclaim

### 8.1. Expiry on the observer

Each member cluster MUST run a sweeper.
An observer MUST measure the lease of another member with its own clock.
The observer MUST record the local time at which it first saw the
current resource version of each `Member`.
Let `T` be that local time.
The observer MUST treat the `Member` as expired from local time `T + D`.
The observer MUST NOT compare `renewTime` with its own clock.
`renewTime` comes from another clock, and the two clocks can disagree by
any amount.

An observer MUST count only time during which it reads the member list.
When the observer has not read the member list for longer than the read
gap `G`, it MUST forget the local times that it recorded, and record
them again from its next read.
The default `G` is twice the sweep interval.
The sweeper MUST NOT delete a `Member` unless its last read of the
member list is at most `G` old.
In a store outage longer than `D`, no member can renew.
Without this rule, every observer would treat every member as expired
when the store returns, and sweep the whole mesh at once.
Forgetting only makes a sweep later, so it keeps the safety of 8.4.
`quint/negative/outage-sweep.qnt` shows the sweep without the rule.

### 8.2. Delete an expired member

The sweeper MUST delete an expired `Member` with the resource version
that it observed.
If the member renewed after the observation, the delete fails with
`409 Conflict`.
So a renew and a delete of the same `Member` cannot both succeed.
The sweeper MUST NOT delete its own `Member`.

### 8.3. Clear stale claim references

The sweeper MUST do a consistent list of devices first.
The sweeper MUST then do a consistent list of members.
The order matters.
A member creates its `Member` before it writes any `claimRef`.
So a `claimRef` in the device list names a member that existed before
the member list.
If that member is not in the member list, it is gone.

For each device whose `claimRef` names a member UID that is not in the
member list, the sweeper MUST clear `claimRef`.
The clear MUST be a status update with the resource version from the
device list.
If the clear fails with `409 Conflict`, the sweeper MUST skip the device
until its next run.
The sweeper MUST run at least once every `D`.

### 8.4. Safety

The rules in sections 7.4, 7.5, 8.1, and 8.2 give this result.
A sweeper deletes a `Member` only after the holder treats itself as not
live.
The observer sees a renew at or after the time the holder sent it.
The observer waits `D` on its own clock from then.
The holder waits at most `D - M` on its own clock.
With `M` at least `2 * rho * D`, the holder stops first.

The Quint model checks these properties:

- No two claims are bound to the same device.
- If a claim is `Bound` to a device, the device's `claimRef` names the
  claim, or the claim's member is not live.
- A sweeper does not clear a `claimRef` while the holder is live by its
  own clock.
- The resource version of each resource strictly increases.
- A watch from `r` sees every event after `r` in order, or it gets
  `410 Gone`.
- A device whose member leaves or crashes becomes free.
- Each fencing token of a device belongs to one bind.
- A device never accepts a use from an older bind after it has accepted
  a use from a newer bind.
- A claim for a free, matching device becomes `Bound` while its member
  is live.

### 8.5. Reclaim policy

A `Device` MAY set a reclaim policy in `spec.reclaimPolicy`: `Delete`,
`Delay`, or `Retain`, ADR 0016.
The default reclaim policy is `Delete`.
The reclaim policy decides only when a sweeper may clear the `claimRef`
of a member that is gone.
It does not change when a member is gone, or when a claim is in effect.

Under `Delete`, the sweeper MUST clear the `claimRef` as section 8.3
describes.
Under `Delay`, the sweeper MUST NOT clear the `claimRef` until the
reclaim time `R` of the device has passed.
The sweeper MUST measure `R` on its own clock, from its first sweep that
found the holder gone.
`R` is `spec.reclaimDelaySeconds` of the device.
Under `Retain`, the sweeper MUST NOT clear the `claimRef`.

An operator MAY release a retained device.
The release MUST be a status update that clears `claimRef` and carries
the resource version that the operator read.
The release MUST fail while the holder's member UID is live.
The release MUST record who released the device.

A policy only removes or delays a clear, so the rules of 8.4 hold for
every policy.

## 9. Pivot

### 9.1. Mapping

A pivot moves the objects of an existing Device CRD in a cluster's etcd
into solas Devices, ADR 0010.
The old object and its Device MUST have the same name.
The Device MUST get the labels of the old object.
The Device MUST get the annotations of the old object, except
`kubectl.kubernetes.io/last-applied-configuration`.
The Device MUST get its description from a field of the old object that
the pivot names.
The pivot MUST NOT copy the status of the old object.
A pivoted device starts free.
The Device MUST have the annotation `solas.dev/pivoted-from`, set to the
group, the resource, the namespace, and the name of the old object.
It names the object, not its UID, because a webhook sees a new object
before the API server gives it a UID.
The old object MUST have the annotation `solas.dev/pivoted-to`, set to
`solas.dev/devices/` and the name.

### 9.2. Copy

The copy MUST create the Device when it does not exist.
The copy MUST update the Device when its `solas.dev/pivoted-from` names
the old object.
The copy MUST NOT change a Device whose `solas.dev/pivoted-from` is not
set or names another object.
It MUST report each such Device as a conflict.
Running the copy twice MUST give the same result as running it once.
With `--dry-run`, the copy MUST NOT write.

### 9.3. Verify

The verify MUST check that each old object has a Device.
It MUST check that the Device names the old object, and that the labels
and the description match.
It MUST exit with a status other than 0 when any check fails.

### 9.4. Webhook

The webhook MUST be a mutating admission webhook on the old resource,
for create, update, and delete.
On a create or an update, the webhook MUST write the Device before it
allows the request.
It MUST then add `solas.dev/pivoted-to` to the old object with a patch.
If the webhook cannot write the Device, it MUST deny the request.
The webhook configuration MUST use `failurePolicy: Fail`.
On a dry-run request, the webhook MUST NOT write the Device.
On a delete, the webhook MUST delete the Device.
If solas rejects the delete of the Device, the webhook MUST deny the
delete of the old object.

## 10. Priority and preemption

### 10.1. Device metadata

`Device.spec.attributes` holds free-form metadata as string pairs.
`Device.status.conditions` holds the health of the device.
The party that runs the device sets the conditions.
The server MUST keep the other rules of section 5.3 when a status update
changes the conditions.
`Device.spec.preemptible` MUST be false unless it is set.
`Device.spec.preemptionGracePeriodSeconds` MUST be 30 unless it is set.

### 10.2. Holder

A `claimRef` MUST also hold the priority of the claim.
A `claimRef` MAY hold the time of the bind, by the clock of the binder.
The server MUST NOT use that time for any decision.

### 10.3. Selection

`DeviceClaim.spec.priority` is a number, and 0 unless it is set.
A higher number is more urgent.
`DeviceClaim.spec.selector.cel` MAY hold a CEL expression over the
variable `device`.
A device matches a claim only when the label selector and the CEL
expression both match it.
If the expression does not compile, the controller MUST NOT bind the
claim and MUST set the condition `SelectorValid` to `False`.
A CRD cannot check that a string is valid CEL.
If the expression fails at run time for a device, that device MUST NOT
match.

### 10.4. Lease display

The controller SHOULD set `DeviceClaim.status.leaseExpiresAt` on each
`Bound` claim to the end of its member's lease, `S + D - M`, as a wall
time by the holder's clock.
The controller SHOULD clear it when the claim becomes `Pending`,
`Suspended`, or `Lost`.
No component MUST use `leaseExpiresAt` for any decision.

### 10.5. Preemption request

A `Pending` claim MAY request preemption of a device only when:
the device matches the claim, the device is preemptible, the device is
bound, and the holder has a lower priority than the claim.
A request MUST be a status update of the device that sets
`status.preemption`.
The request MUST name the requesting claim as a `claimRef` does, with its
priority.
The request MUST carry the resource version that the controller read.
The server MUST reject a request whose priority is not greater than the
priority of the holder.
The server MUST reject a request that replaces a request of equal or
greater priority.

### 10.6. Release by the holder

When the holder's controller sees a request on its device, it MUST set
its claim to `Preempting`.
A `Preempting` claim is in effect, as a `Bound` claim is.
The controller MUST record the local time at which it first saw the
request.
When the grace period has passed since that time, by the holder's clock,
the controller MUST set its claim to `Preempted`.
After the claim is `Preempted`, the controller MUST clear the `claimRef`
of the device with a status update, and keep the request.
The controller MUST then set the claim back to `Pending`.

### 10.7. Bind by the preemptor

While a device has a request, the server MUST reject a bind that does not
name the requesting claim.
The bind of the requesting claim MUST clear `status.preemption`.
The bind raises the fencing token, as every bind does, spec 5.3.

### 10.8. Holders that do not respond

The preemptor MUST NOT clear the `claimRef` of the holder.
Before each renew, a member MUST handle each preemption request on the
devices it holds, as section 10.6 describes.
If it cannot, it MUST NOT renew.
A holder that stops responding then loses its lease, and the sweeper
frees its device, section 8.
The device then stays kept for the preemptor, section 10.7.
So no claim in effect loses its device, and a stuck holder delays a
preemption by at most one lease duration `D`.

### 10.9. Stale requests

The sweeper MUST clear a request whose member UID is not in the member
list, as it does for a `claimRef`, spec 8.3.
The controller MUST clear a request that names its own member and a claim
that does not exist, as it does for a `claimRef`, spec 6.4.

### 10.10. Protected holders

A claim MAY ask for protection in `DeviceClaim.spec.protected`.
The `claimRef` of a protected claim MUST carry `protected: true`.
The server MUST reject a `claimRef` with `protected: true` unless the
`MemberPolicy` of its member sets `allowProtected`, spec 15.1.
So only an operator decides which members may protect their claims.
The server MUST reject a preemption request on a device whose holder is
protected.
A protected holder can only release its device.

## 11. Version skew

Member clusters upgrade in waves, so servers of two releases share one
store, ADR 0013.

### 11.1. Format version

The store MUST hold one finalized format version.
The finalized format starts at 1.
Each object MUST record the format version of its last write in the
annotation `solas.dev/format`.
An object with no record has format 1.
Each server has a maximum format: the newest format that it can read and
write.

### 11.2. Write rule

A server MUST NOT write an object in a format newer than the finalized
format.
A server MUST be able to read every format from 1 up to its own maximum.
A release MUST be able to read every format that the previous release
can write.
A new field takes two releases: the first reads and keeps it, and the
second writes it.

### 11.3. Newer objects

A server MUST reject a write to an object whose format is newer than its
own maximum.
A server MUST NOT drop a field that it does not know.
The rejection keeps this rule: a server never decodes and writes back an
object that may hold fields it does not know.
A server MAY read a newer object, on a best effort basis.

### 11.4. Finalization

Each member MUST report the lowest and the highest format that it
supports in its `Member` status.
The finalized format MUST only move up.
The finalized format MUST move to a value only when every `Active`
member reports a highest format at or above that value.

### 11.5. Rollback

A member MAY roll back to a release whose maximum format is at or above
the finalized format.
A server MUST refuse to start when the finalized format is above its
maximum format.

## 12. Migration

A mesh can move from one store to another, ADR 0015. The tool seals the
source, copies every `Device` and `Member`, and verifies the copy. Then
each member cluster changes its storage URL, spec 2.6.

### 12.1. Seal

A sealed store MUST reject every write.
A sealed store MUST serve reads.
A seal MUST NOT change any object.

The DynamoDB store seals with the attribute `sealed` on the counter item
of each resource.
A write MUST fail when the counter item of its resource has the
attribute `sealed`.
The condition on the counter update of each write, spec 2.3, holds this
check, so a write cannot commit after the seal.
To seal a resource that has no counter item, the tool MUST create the
counter item with the attribute `sealed`.
A write that fails because the store is sealed MUST return
`503 ServiceUnavailable`.

An unseal removes the attribute `sealed`.
The tool MUST NOT unseal a source when the destination holds an object
that is not in the source, or that differs from its source object except
in resource version.
Such an object shows that a cluster wrote to the destination.
A destination that holds part of a copy, or nothing, does not stop an
unseal.

### 12.2. Copy

The tool MUST seal the source before it reads it.
The tool MUST NOT copy into a store that holds a `Device` or a `Member`.
The tool MUST copy at the storage level, not through the API server.
The status strategy of a `Device` sets the fencing token on each bind,
spec 5.3, so a copy through the API server would change the tokens.
The copy MUST keep the name, UID, labels, annotations, spec, status, and
creation time of each object.
The copy MUST NOT keep the resource version.
The destination gives each object a new resource version.
After the copy, the tool MUST verify that each object in the destination
equals its source, except for the resource version.

### 12.3. A store that cannot be sealed

The etcd store has no counter item, so the tool cannot seal it.
Before a move from such a store, the operator MUST stop solas in every
member cluster.
The tool MUST then list the `Member` objects, wait `D` by its own clock,
and list them again.
The tool MUST NOT copy when a `Member` renewed in that time, or when a
`Device` or a `Member` changed.

### 12.4. Switch

Each member cluster SHOULD switch to the destination within `D` of the
seal.
A cluster that cannot write to its store does not renew, so by its own
clock its claims stop being in effect, spec 6.5.
On the destination, the member renews its `Member`, which has the same
UID, spec 7.2, and its claims go back to `Bound`.
A cluster that switches later than `D` finds that the other members
swept its `Member`, spec 8.
It joins again, and its claims become `Lost`, spec 6.5.

## 13. Restore

A restore of the store from a backup puts back old fencing tokens and old
resource versions, ADR 0018.

### 13.1. Epoch

The store MUST hold an epoch.
The epoch of a store before its first restore is 0.
After a restore, and before any server writes to the store, the restore
tool MUST set an epoch above every epoch that the store had.
The epoch in the backup can be old, so the tool MUST NOT only add 1 to
it.
The restore tool SHOULD use the time of the restore in Unix seconds, and
at least the restored epoch plus 1.

### 13.2. Fencing tokens

A fencing token MUST hold the epoch of its bind in its high 32 bits.
When a status update sets `claimRef` on a free device whose token is
below `epoch * 2^32`, the server MUST set the token to `epoch * 2^32 + 1`.
In every other bind, the token is the old value plus 1, spec 5.3.
So a token compares as the pair (epoch, count), and a bind after a
restore gets a token above every token issued before it.

### 13.3. Resource versions

After a restore, every resource version MUST be above every resource
version issued before the restore.
On the DynamoDB store, the restore tool MUST raise each counter item to
at least `epoch * 2^32`.
On the etcd store, the operator MUST restore with
`etcdutl snapshot restore --bump-revision` and `--mark-compacted`.

## 14. Transfer

A holder can hand its device to a named claim, ADR 0019.

### 14.1. Offer

Before it offers a device, the holder's controller MUST set its claim to
`Transferring`.
A `Transferring` claim is not in effect, so its workload stops before
the offer exists.
The offer MUST be a status update of the device that sets
`status.offer` to the named claim, its member, and its member UID.
The offer MUST carry the resource version that the controller read.
The server MUST reject an offer while a preemption request stands, and a
second offer while one stands.

### 14.2. Bind against the offer

Only the claim that the offer names, with the member UID that it names,
MAY bind against the offer.
The bind MUST be one status update that sets `claimRef` to the named
claim, sets `status.fencingToken` to the next token, spec 5.3 and 13.2,
and clears `status.offer`.
The device is never free in a transfer, so no third claim can take it.
When the old claim's controller sees that its device names another
claim, it MUST stop treating the device as its own, and MUST NOT write to
the device.

### 14.3. Withdraw

The holder MAY withdraw an offer that nobody bound, with a status update
that clears `status.offer`, and set its claim back to `Bound`.
The withdraw and the bind against the offer are both conditional writes,
so only one succeeds.
The sweeper MUST clear an offer whose member UID is not in the member
list, as it does for a preemption request, spec 10.9.

### 14.4. Pre-bound claims

A claim MAY name its device in `spec.deviceName`.
A claim that names a device MUST bind only that device: against an offer
that names the claim, or as a normal bind when the device is free.

### 14.5. Recovery of a Lost claim

A member that joined again with a new UID MAY recover a device whose
`claimRef` names one of its `Lost` claims under an old member UID,
ADR 0020.
A recovery MUST be one status update that sets the `claimRef` to the
member's current UID and raises `status.fencingToken`, spec 5.3 and 13.2.
The server MUST reject a recovery unless the member name and the claim
UID are the same, and no `Member` has the old member UID.
After a recovery, the controller MUST set the claim back to `Bound`.
A plain adopt MUST NOT take such a device, spec 6.3.

## 15. Limits

An operator MAY limit a member, ADR 0021.

### 15.1. Policy

A `MemberPolicy` MUST have the name of the member that it limits.
`spec.maxDevices` is the most devices that the member may hold.
`spec.bindsPerMinute` and `spec.preemptionsPerMinute` are rates.
A member with no `MemberPolicy` has no limit.
A member MUST NOT hold more devices than its `maxDevices`.

### 15.2. Usage set

The server MUST keep a usage set for each limited member: the devices
that the member holds or is binding, each with the time it was added.
Before the device write of a bind, the server MUST add the device to the
usage set with a conditional write, on condition that the set holds
fewer devices than `maxDevices`.
The device write MUST follow in the same request.
So every device that a member holds is in its usage set.
A bind over the limit MUST fail with `403 Forbidden`.
A bind or a preemption request over a rate MUST fail with
`429 Too Many Requests`.

### 15.3. Cleanup

A clear of a device MUST remove it from the usage set of its member.
The member's own server MAY remove an entry whose device the member does
not hold.
It MUST NOT remove such an entry before a grace that is longer than the
deadline of a request has passed since the entry was added, by its own
clock.
A bind still in flight then cannot land after its entry is gone.
