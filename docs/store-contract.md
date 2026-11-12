# The store contract

Spec 2.5 lists the rules that the protocol needs from a store. The Quint
model of the protocol, `quint/solas.qnt`, assumes only these rules. This
page shows how each store keeps each rule.

The DynamoDB store and the Postgres store are our code, so we check
them: with the Quint model `quint/store.qnt`, and with the upstream
storage tests of `k8s.io/apiserver` in
`pkg/storage/dynamo/conformance_test.go` and
`pkg/storage/postgres/conformance_test.go`. The etcd store is the etcd3
storage of `k8s.io/apiserver`, which the kube-apiserver uses. We rely on
etcd and on that code, and the REST store tests and model-based tests
run on every store.

| Rule of spec 2.5 | DynamoDB store | etcd store | Postgres store |
|------------------|----------------|------------|----------------|
| A conditional write applies only when the object still has the resource version that the writer read. | The object action of each transaction has the condition `rv = :rv`, spec 2.3. Conformance: `RunTestGuaranteedUpdateWithConflict`, `RunTestDeleteWithConflict`. | The etcd3 store commits in a transaction that compares the mod revision of the key. | The update and the delete of a write have `WHERE rv = <expected>`, and a create is an insert that changes no row when the key exists, spec 2.8. Conformance: the same tests. `commit_test.go`: `TestCommitObjectConflict`. |
| A write applies in full or not at all. | Each write is one `TransactWriteItems` call, spec 2.3, which DynamoDB applies in full or not at all. `quint/store.qnt` models the write as one step. | An etcd transaction is atomic. | Each batch is one call of the function `solas_commit`, one transaction, spec 2.8. A failed condition raises, and the transaction rolls back with the counter update. `quint/store.qnt`, module `store3pg`, models the write as one step. |
| A create fails when an object with the same key exists. | The put has the condition `attribute_not_exists(pk)`, spec 2.3. Conformance: `RunTestCreateWithKeyExist`. | The etcd3 store creates in a transaction that requires create revision 0. | The insert has `ON CONFLICT DO NOTHING` and a row count of 0 is a conflict, spec 2.8. Conformance: `RunTestCreateWithKeyExist`. |
| A read sees every write that completed before the read started. | Every read is strongly consistent, spec 2.4. | etcd reads are linearizable by default, spec 2.7. | Every read goes to the writer instance, spec 2.8, which holds every committed write. |
| A list is a consistent snapshot at the resource version that it returns. | The list reads the counter before and after the query, spec 2.4. `quint/store.qnt`: invariant `listSnapshot`; the negative model `quint/negative/list-no-recheck.qnt` shows that the second read is needed. | An etcd range read returns the state at one revision. | The list reads the counter and the objects in one `REPEATABLE READ` transaction, spec 2.8, and returns the counter it read. `quint/store.qnt`, `store3pg`: invariant `listSnapshot` with `snapshotList`; the negative model `quint/negative/sequence-not-lock.qnt` shows that the lock order is needed. `list_test.go`: `TestListIsOneSnapshot`. |
| The resource version of each write to a resource is greater than that of every earlier write. | The counter grows by 1 in the same transaction as the write, spec 3.1. `quint/store.qnt`: invariant `monotonicRV`. | etcd gives each write a revision, and revisions grow. | The counter row grows by `k` under its row lock in the same transaction as the writes, spec 2.8, so the commit order is the version order. `quint/store.qnt`, `store3pg`: invariant `monotonicRV`. `commit_test.go`: `TestCommitConcurrentWritersHaveNoGaps`. |
| A watch from a resource version delivers every later change, in order, or fails with `410 Gone`. | Polling of the event log, spec 4.2 and 4.3. `quint/store.qnt`: invariants `monotonicRV` and `watchComplete`; the negative model `quint/negative/watch-no-gap-check.qnt`. Conformance: `RunTestWatch`, `RunWatchSemantics`. | etcd watches deliver every event from a revision in order. A watch from a compacted revision fails with `410 Gone`. | Polling of the `events` table with the gap rule, spec 2.8, 4.2, and 4.3. `quint/store.qnt`, `store3pg`: invariants `monotonicRV` and `watchComplete`. Conformance: `RunTestWatch`, `RunWatchSemantics`. `watch_test.go`: `TestWatchGapEndsWithGone`, `TestExpiryDeletesOldEvents`. |
