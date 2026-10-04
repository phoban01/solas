# ADR 0013: Version skew and format finalization

Status: accepted

## Context

Member clusters upgrade solas in waves. For a while, servers of two
releases share one store. A newer release can add a field. An older
server that reads an object with that field, changes it, and writes it
back drops the field. A newer release can also change how it protects a
device, so two servers can disagree about which writes are allowed.

Nothing in the spec covered upgrades.

## Decision

- Members roll out in waves, so two releases share the store at once.
- The skew policy is N-1: a release writes only what the previous
  release can read.
- A new field takes two releases. The first release reads the field and
  keeps it. The second release writes it.
- Each object records the format in which its last write encoded it, in
  the annotation `solas.dev/format`. A storage guard below the registry
  (#139) sets and checks it, so the rule holds on every store (ADR 0014).
- The store holds one finalized format. Servers write only in the
  finalized format, so an older server can read every object. A server
  rejects a write to an object in a format newer than its own maximum,
  and so never drops a field it does not know.
- Each member reports the range of formats it supports in its `Member`.
  `solas finalize --to N` moves the finalized format up only when every
  `Active` member supports `N`.
- Rollback is allowed until finalization, not after: a server refuses to
  start when the finalized format is above its maximum.

## Consequences

- An upgrade that changes the format has two steps: roll out the new
  release everywhere, then finalize.
- Before finalization, the new release writes in the old format, so a
  rollback is safe.
- After finalization, a rollback below the finalized format fails at
  start, which is better than a silent loss of fields.

## Rejected alternatives

- No format record: an older server cannot tell that an object holds
  fields it does not know.
- A format per member, with no shared finalized format: each server
  would write in its own format, and an older server could read objects
  that it cannot keep.
- A record as a DynamoDB attribute: it does not exist on the etcd store.
