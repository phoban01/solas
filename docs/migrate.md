# Move a mesh to another store

A mesh runs on one store: a DynamoDB table or an etcd cluster (ADR
0014). This page moves the `Device` and `Member` objects of the mesh to
another store, and then moves every member cluster to it. Spec section
12 has the rules, and ADR 0015 has the reasons. `quint/migrate.qnt`
models the move.

The move keeps the holder of each device, its fencing token, any
preemption request, and the UID of each member. The resource versions
start again in the new store.

## Before you start

- The destination must hold no `Device` and no `Member`. The tool
  checks this.
- Know the lease duration `D` of the members: the `leaseDuration` of the
  ConfigMap `solas-member`, 30 seconds by default.
- Have a way to change the Secret `solas-storage` and restart
  `deploy/solas` in each cluster quickly. All clusters must run on the
  new store within `D` of the seal.
- Try it first with `--dry-run`. A dry run seals nothing and writes
  nothing:

  ```sh
  solas migrate --from <old url> --to <new url> --dry-run
  ```

## From DynamoDB

1. Seal, copy, and verify:

   ```sh
   solas migrate --from 'dynamodb://solas?region=eu-west-1' --to 'etcd://etcd-0:2379,etcd-1:2379'
   ```

   The tool seals the table first. From then on, the table rejects
   every write with `503 store is sealed`. Reads keep working. Then the
   tool copies every `Device` and `Member` and checks the copy.

   Last, the tool moves the new store to a new epoch, so every
   resource version and fencing token it issues is above every one
   of the old store, and a client that kept a version of the old
   store gets `410 Gone` and lists again. An etcd store cannot raise
   its revision, so the tool refuses one whose revision is below
   the highest version of the old store; raise it first:

   ```sh
   etcdutl snapshot restore empty.db --bump-revision <highest version> --mark-compacted
   ```

2. In each member cluster, set the `url` key of the Secret
   `solas-storage` to the new URL, and restart solas:

   ```sh
   kubectl -n solas-system patch secret solas-storage --type merge \
     -p '{"stringData":{"url":"etcd://etcd-0:2379,etcd-1:2379"}}'
   kubectl -n solas-system rollout restart deploy/solas
   ```

   Do this in all clusters at once.

3. Check that no claim stays `Suspended`, and that the holders are the
   same:

   ```sh
   kubectl get deviceclaims -A
   kubectl get devices
   ```

## From etcd

The tool cannot seal an etcd store. So you stop the writers, and the
tool checks that they stopped:

1. In each member cluster, stop solas:

   ```sh
   kubectl -n solas-system scale deploy/solas --replicas=0
   ```

2. Copy and verify. The tool lists the objects, waits `D` by its own
   clock, and lists them again. It copies only when no `Member` renewed
   and nothing changed:

   ```sh
   solas migrate --from 'etcd://etcd-0:2379' --to 'dynamodb://solas?region=eu-west-1' --lease-duration 30s
   ```

3. In each member cluster, set the new URL and start solas:

   ```sh
   kubectl -n solas-system patch secret solas-storage --type merge \
     -p '{"stringData":{"url":"dynamodb://solas?region=eu-west-1"}}'
   kubectl -n solas-system scale deploy/solas --replicas=1
   ```

## Why each cluster must switch within D

During the move, a cluster that still points at the sealed store cannot
renew its lease. At the end of its lease, by its own clock, its claims
stop being in effect (spec 6.5), so no workload keeps a device that it
should not have.

When the cluster starts on the new store, it renews its old `Member`,
which has the same UID, and its claims go back to `Bound`. But if the
other members run on the new store for longer than `D` while this
`Member` does not renew, their sweepers delete the `Member` and free its
devices (spec 8). The late cluster then joins with a new UID, and its
claims become `Lost`. A user must delete a `Lost` claim and create it
again.

## Roll back

Before any cluster writes to the new store, you can unseal the old one:

```sh
solas unseal --from <old url> --to <new url>
```

The tool refuses when the new store holds an object that is not an
exact copy of the old one, because then a cluster wrote to it. A partial
copy, or an empty new store, does not stop an unseal. After an unseal,
point every cluster back at the old URL, and empty the new store before
you try again.

Only a DynamoDB store can be sealed, so there is nothing to unseal
after a move from etcd. Start solas on the old URL again.

## Check a move

`solas migrate verify` compares two stores at any time. It ignores
resource versions and the epoch:

```sh
solas migrate verify --from <old url> --to <new url>
```

## Try it in the demo

`devbox run demo` ends with this move: scene 7 moves the two demo
clusters from dynamodb-local to the etcd container with
`demo/k3d/switch-store.sh`. With `SOLAS_STORE=etcd`, it moves from etcd
to dynamodb-local.
