# solas

A Kubernetes extension API server that stores custom resources in a
shared store: a DynamoDB table or an etcd cluster.

Several management clusters share global state through one store. There
is no central control cluster. Clusters can join and leave. The demo
uses a pool of devices: each cluster can lease a device, and a device is
bound to at most one claim at a time. Fencing tokens stop a cluster that
paused from using a device that another cluster now holds.
Fencing works only where something checks the token: the demo device
does, but an actuator such as MAAS needs a gatekeeper in front of it
(ADR 0017). Without one, fencing does not protect the devices it drives.

- [The demo](demo/k3d/README.md): two clusters, a race for a device, a
  reclaim, and fencing. Run it with `devbox run demo`.
- [The specification](spec/solas.md) and the [ADRs](docs/adr/).
- [The plan](docs/plan.md).

## Choose a store

The flag `--storage-url` picks the store (ADR 0014). All member clusters
must use the same store.

| Store | URL |
|-------|-----|
| DynamoDB | `dynamodb://<table>?region=<region>` |
| dynamodb-local | `dynamodb://solas?create-table=true&endpoint=http://<host>:8000` |
| etcd | `etcd://<host:port>[,<host:port>...]` |

The Deployment in `deploy/solas` reads the URL from the key `url` of the
Secret `solas-storage`. `deploy/solas/config/storage.yaml` is an example.
On DynamoDB, the AWS credentials come from the environment or from the
same Secret. [docs/store-contract.md](docs/store-contract.md) shows what
the protocol needs from a store, and how each store keeps it.

To move a running mesh to another store, see
[docs/migrate.md](docs/migrate.md): `solas migrate` seals the old store,
copies, and verifies, and each cluster then gets the new URL.

## Tests

`devbox run verify` runs the unit tests, the Quint model checks, the
model-based tests on both stores, and the Duvet traceability gate. Set
`SOLAS_STORE=etcd` to run `devbox run demo`, `devbox run e2e`, or
`devbox run smoke` on etcd.
