# ADR 0018: Restore without regressing: a store epoch

Status: accepted

## Context

A restore of the store from a backup puts back old fencing tokens and
old resource versions. Tokens issued between the backup and the restore
are then issued again. A gatekeeper that saw token 7 rejects a new
holder that gets token 5. A gatekeeper that also lost its state accepts
the old holder of token 5. Watches and caches see resource versions go
back, which Kubernetes clients do not expect.

## Decision

The store has an epoch. A restore moves the store to a new epoch.

- **The epoch lives in the store**, in the `StoreFormat` object of spec
  11.1, so it moves with the store and with `solas migrate`.
- **A restore sets an epoch above every epoch the store had.** The epoch
  in the backup may be old: a second restore from the same backup must
  not reuse an epoch. The restore tool uses the time of the restore in
  Unix seconds, and at least the restored epoch plus 1.
- **A fencing token holds the epoch in its high 32 bits**, and a count in
  its low 32 bits. The first bind of a device in a new epoch gets
  `epoch * 2^32 + 1`. So a token stays one integer that compares as the
  pair (epoch, count), and gatekeepers and workloads do not change.
  Epoch 0 is the store before its first restore, so today's tokens keep
  their values.
- **Resource versions go up across a restore.** On DynamoDB, the restore
  tool raises each counter item to `epoch * 2^32`. On etcd, the operator
  restores with `etcdutl snapshot restore --bump-revision` and
  `--mark-compacted`, which do the same for revisions and make watches
  from old revisions fail with `410 Gone`.

`quint/restore.qnt` checks that every bind issues a token above every
token issued before, across restores, including two restores from one
backup. The negative model `no-epoch` shows a token going back without
the epoch.

## Consequences

- A restore is a procedure: restore the backup, then run the restore
  tool before any solas server starts on the store.
- Tokens jump by about 2^32 at each restore. They stay well inside an
  int64.
- A device that a backup shows as held keeps its holder after the
  restore, with its old token. Its next bind gets a token of the new
  epoch.

## Rejected alternatives

- A separate epoch field next to the token: every gatekeeper and
  workload would have to compare pairs.
- Raising every token by a fixed amount at a restore: the tool cannot
  know how many tokens were issued after the backup.
