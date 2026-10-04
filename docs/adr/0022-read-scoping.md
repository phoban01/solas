# ADR 0022: Read and write scoping in the store

Status: proposed. It changes the stored claimRef, which breaks the API,
so it needs a decision (#185).

## Context

Every member can list every device, with the namespace and name of the
claim of every other member. A limit that only the solas binary enforces
(ADR 0021) is gone as soon as someone runs a changed binary with the same
store credentials. The table puts all devices of a resource in one
DynamoDB partition, and etcd keeps them under one prefix, so neither IAM
nor etcd roles can scope them today.

## What a bind needs

A bind reads the free devices that match a selector, and writes one of
them with a conditional write. Any member may bind any free device. So
every member must be able to read every device's spec and whether it is
free, and must be able to write the status of any device. The store
cannot limit device writes per member without a second item in the
same write, which the etcd store does not have (spec 2.5).

## Options

1. **One shared catalog, minimal holder data.** Devices stay in one
   partition or prefix that every member reads and writes. The stored
   `claimRef` keeps only the member name, the member UID, and the claim
   UID, plus the priority and protection that other members need. The
   claim's namespace and name stay in the member's own cluster, which
   maps the claim UID back to its claim. Other members learn which member
   holds a device, but not which workload.
2. **A free index and a partition per member.** A device moves from the
   free index to the partition of its holder on a bind. A bind then
   writes two items in one transaction: possible on DynamoDB, not on the
   etcd store.
3. **Encrypted holder data.** The claim details are encrypted with a key
   of the member. Other members can read the device but not the details.
   Key handling adds a new failure mode.

## Proposal

- Option 1 for devices.
- A partition or prefix per member for the objects that only that member
  writes: its `Member` and its usage set (ADR 0021). On DynamoDB, the
  member's IAM role gets `dynamodb:LeadingKeys` on its own partition, and
  read and write on the device partition. On etcd, the member's role
  gets read and write on its own prefix and the device prefix, and read
  on the others.
- `MemberPolicy` objects live in a partition that only operators write;
  members read it.
- The storage guard (#139) still checks every device write. A member
  with a changed binary can still write a claimRef that names another
  member; the store cannot stop that. The audit trail (#191) records it.

## Consequences, if accepted

- A breaking change of the stored `claimRef` and of the device table,
  with a format bump (spec 11) and two releases.
- The member list of the sweeper needs read access to every Member, which
  the roles above give.

## Rejected so far

- Scoping device writes per member: it conflicts with a shared pool of
  conditional writes.
