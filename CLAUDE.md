# solas

## Goal

This project builds an api-extension server that uses dynamodb as a storage backend.
The aim is to run multiple kubernetes management clusters with no master of masters needed. Global state can be written to Custom reosurces
that get stored in dynamodb and replicated to all other member clusters. The inented global state is a pool of devices that can be leased.
clusters can join and leave the mesh. a device may only be leased to a single claim at a time. 

The following custom resources should prove the pattern:
Device : global state object that describes a devices and whose status shows whether it is climaed or not
DeviceClaim : cluster local claim on a resource

The result should be a fully working demo (it is fine to use localstack if possible)
LocalStack now needs an auth token, so the project uses amazon/dynamodb-local
(docs/adr/0007-dynamodb-local.md).




## How work happens

1. **Spec first.** 
2. **Model in Quint.** 
3. **Cite with Duvet.** 
4. **Small issues.** Work is filed as GitHub issues an agent can finish in
   under five minutes. Each issue names its spec sentence, the exact task,
   the files, and the command that proves it is done. 

## Tests

- Unit tests run with the race detector: `devbox run test`.
- The simulator gate runs on every protocol change: `devbox run sim-gate`.
- End-to-end tests use `sigs.k8s.io/e2e-framework` against k3d clusters,
  with plain `testing`. No Ginkgo. No envtest. `devbox run e2e`.
- Model-based tests replay Quint traces against the real API server.
- `devbox run verify` runs unit tests, Quint checks, and the Duvet gate.

## Tooling

Use devbox for everything. Do not install tools globally or call `nix-shell`
by hand. `devbox.json` lists the packages and the scripts above.
`devbox run duvet-install` installs Duvet through cargo on first use.


## Local resources

The development VM has 12 GB of memory. Running out of it kills the
Claude Code session, so heavy jobs must not overlap.

- devbox caps the Java heap at 3 GB (`JVM_ARGS` for Apalache,
  `JAVA_TOOL_OPTIONS` for TLC and other JVMs), Go package parallelism at
  four (`GOFLAGS=-p=4`), and the simulator gate at four shards.
- Run at most two k3d clusters at once. Three clusters ran the VM out
  of memory.
- Run one heavy job at a time: a `quint verify`, a TLC run, an e2e run,
  or a race stress run. Do not start a second one in parallel.
- `devbox run e2e` takes a lock, so a second run waits for the first.
- A stress run uses one copy of `go test` with a moderate `-count`.
  Do not run many copies at once.
- Delete k3d clusters when a run ends, and delete only clusters whose
  names start with `e2e-`.

## Architecture decisions already made

- One DynamoDB table in one region is the shared store
  ([docs/adr/0001-single-table.md](docs/adr/0001-single-table.md)).
- `DeviceClaim` is a CRD in local etcd. `Device` and `Member` live in
  DynamoDB ([docs/adr/0002-claims-in-local-etcd.md](docs/adr/0002-claims-in-local-etcd.md)).
- Resource versions come from one counter item per resource
  ([docs/adr/0003-rv-counter.md](docs/adr/0003-rv-counter.md)).
- Watch polls a TTL event log
  ([docs/adr/0004-watch-by-polling.md](docs/adr/0004-watch-by-polling.md)).
- The member UID is the fencing token for reclaim
  ([docs/adr/0005-member-uid-fencing.md](docs/adr/0005-member-uid-fencing.md)).
- Tests and the demo use amazon/dynamodb-local, not LocalStack
  ([docs/adr/0007-dynamodb-local.md](docs/adr/0007-dynamodb-local.md)).
- Fencing tokens guard device use
  ([docs/adr/0008-fencing-tokens.md](docs/adr/0008-fencing-tokens.md)).
- Clusters run with k3d
  ([docs/adr/0009-k3d.md](docs/adr/0009-k3d.md)).
- A mutating webhook and a CLI pivot Devices from an etcd CRD into solas
  ([docs/adr/0010-pivot.md](docs/adr/0010-pivot.md)).
- The API groups are `solas.dev/v1alpha1` for Device and Member, and
  `claims.solas.dev/v1alpha1` for DeviceClaim
  ([docs/adr/0006-api-group.md](docs/adr/0006-api-group.md)).
- Members of two releases share the store; objects record their format,
  and `solas finalize` moves the shared format up
  ([docs/adr/0013-version-skew.md](docs/adr/0013-version-skew.md)).
- One flag, `--storage-url`, picks the shared store: a DynamoDB table or
  an etcd cluster
  ([docs/adr/0014-storage-url.md](docs/adr/0014-storage-url.md)).
- `solas migrate` moves the data to another store: it seals the source,
  copies, and verifies
  ([docs/adr/0015-migration.md](docs/adr/0015-migration.md)).

## Writing

Specs, issues, commit messages, and docs follow Simplified Technical
English: short sentences, one idea each, active voice, plain words. No
filler, no marketing adjectives, no emoji.

## Where things are

- `quint/` models; `scripts/quint-check.sh` positive and negative checks
- `.duvet/config.toml` and `.duvet/snapshot.txt` traceability gate
- `cmd/solas/` the one binary: the extension server and the controller;
  `demo/k3d/` two-cluster demo
- `docs/confidence.md` the trust ladder; `docs/sim-gate.md` the simulator;
  `docs/mbt.md` the model-based tests
- `.github/workflows/verify.yml` and `sim-gate.yml` the CI gates
