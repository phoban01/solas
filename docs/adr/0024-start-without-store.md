# ADR 0024: The server starts and reports ready without the store

Status: accepted (#188)

## Context

The kube-apiserver of each member cluster reaches the solas resources
through an APIService. The APIService is available only while a solas
Pod is ready. When it is not available, discovery of the cluster's APIs
fails, and the namespace controller and the garbage collector of the
host cluster stop. So a shared store that does not answer would stop
namespace deletion in every member cluster.

Before this decision, solas read the store at start: it created the
table, read the finalized format and the epoch, and refused to start
when any of these failed. A Pod restart during a store outage then left
the APIService unavailable.

## Decision

- The readiness of the server does not depend on the store, spec 16.1.
  `/readyz` has no check that calls the store.
- The server starts when the DynamoDB store does not answer. It creates
  the table and reads the finalized format and the epoch in the
  background, and tries again every two seconds until the store answers.
- Until the server has read the finalized format and the epoch, the
  storage guard rejects every write with `503 ServiceUnavailable`. A
  write before that read could use a wrong format or a wrong epoch.
- A write or a read that fails because the store did not answer fails
  with `503 ServiceUnavailable`. The storage guard maps the error on
  every store, so clients and controllers see one error for an outage
  and retry.
- Each request to the server has a timeout of 20 seconds, so a request
  that waits for a store that hangs fails and frees the controller
  worker that made it.
- Each call of the DynamoDB client has a timeout of 10 seconds and at
  most two attempts, so a store that hangs fails a request instead of
  holding it.
- The claim controller sets a `Bound` claim to `Suspended` before it
  reads the store, spec 16.2. In an outage the device list fails, and
  the claim must still leave `Bound` when the lease lapses.

## Consequences

- A cluster keeps deleting namespaces and collecting garbage during a
  store outage. The e2e test `TestStoreOutage` pauses the store
  container and checks this.
- A server that starts during an outage serves discovery and reads from
  the watch cache, once the cache has filled, and fails writes with a
  `503` until the store returns.
- When the store is finalized above the release, the server exits right
  after its start instead of refusing to start. Writes were rejected
  with a `503` until then, so no write in a wrong format lands.
- The etcd store is the etcd3 storage of `k8s.io/apiserver`, which waits
  at start until it reaches etcd, as the kube-apiserver does. A server
  on the etcd store still needs etcd at start. An outage after the start
  does not stop it.

## Rejected alternatives

- A readiness check that calls the store. It makes the APIService
  unavailable in an outage, which is the failure this ADR removes. A
  store outage shows in the `503` errors and the `Suspended` claims
  instead.
- A lazy etcd client that connects on first use. It needs a wrapper
  around the etcd3 storage factory of `k8s.io/apiserver`. The DynamoDB
  store is the main store, and the kube-apiserver has the same limit, so
  this waits for a need.
- Internal errors (`500`) for a store that does not answer. Clients do
  not retry a `500` in the same way, and the error does not say that the
  store is the cause.
