# The solas demo

Two Kubernetes clusters share one pool of devices. They have no link to
each other. Each cluster runs solas and reads and writes one shared store:
a DynamoDB table, or an etcd cluster. The store is the only thing they
share.

## Run it

```sh
devbox run demo        # build, bring up, and run every scene
devbox run demo-down   # remove the clusters, the stores, and the network
```

`devbox run e2e` runs the same scenes as a Go test. `demo/k3d/demo.sh`
and `demo/k3d/down.sh` also work on their own: outside devbox, they run
themselves again through devbox, which has the tools they need.

The demo runs on DynamoDB. To run it on etcd, set one variable:

```sh
SOLAS_STORE=etcd devbox run demo
```

`SOLAS_STORE` picks the storage URL that each cluster gets, from
`mesh.sh url`. Nothing else changes: the clusters, the manifests, and
the scenes are the same. `SOLAS_STORE=etcd devbox run e2e` runs the
e2e test on etcd.

The demo needs Docker and about 2 GB of free memory. It uses k3d
(ADR 0009), and `amazon/dynamodb-local` (ADR 0007) or etcd (ADR 0014).
The setup runs quietly and writes its log to `demo/k3d/setup.log`. It
shortens the lease to 10 seconds, with a margin of 1 second and a sweep
every 3 seconds, so each scene takes seconds, not minutes.

## What it shows

| Scene | What happens | Spec |
|-------|--------------|------|
| 1 | Eight devices are created in cluster `a` and appear in cluster `b`. Each has labels and attributes. The demo sets a `Ready` condition on each; `gpu-t4-2` reports `Ready=False`. | 2, 4, 10.1 |
| 2 | Cluster `a` claims an A100, a small GPU, and a 400G NIC. Cluster `b` claims an A100 and the FPGA. CEL picks by attribute and by condition, so `infer` skips `gpu-t4-2`. Three devices stay free. | 6.3, 10.3 |
| 3 | `b/net` names `nic-1`, which `a/net` holds. `a/net` gets the annotation `solas.dev/transfer-to` and becomes `Transferring`. Its controller offers `nic-1` to `b/net`, and `-o wide` shows the offer. `b/net` binds against the offer with token 2. A watch shows that `nic-1` is never free, and it has no release record. `a/net` goes back to `Pending`. | 14 |
| 4 | `urgent` in `b` has priority 5 and wants an A100. Both are taken, so it asks for the one that `a/train` holds at priority 1. `train` becomes `Preempting`, keeps the device for its 10 second grace period, then lets it go and goes back to `Pending`. `urgent` binds with token 2. | 10.5 to 10.7 |
| 5 | Claims `job` in `a` and in `b` race for `gpu-h100-1`. One wins with token 1. The other stays `Pending`. | 5.3, 6.3 |
| 6 | The winner's node is paused for longer than its lease. The other cluster's sweeper deletes the winner's `Member` and frees its devices. The other `job` binds the H100 with token 2. When the winner wakes up, its workload still believes it holds the device and uses token 1. The device rejects it. | 6.6, 8 |
| 7 | The winner's controller finds its `Member` gone. It joins again with a new UID, and its old claims become `Lost`. | 6.5, 7.2 |
| 8 | The mesh moves to the other store with one setting. `solas migrate` seals dynamodb-local, copies every `Device` and `Member` to etcd, and verifies the copy. A write to the sealed store fails. Then each cluster gets the new `url` in the Secret `solas-storage`, and solas restarts. Every holder and fencing token is the same, and a new claim binds on etcd. [docs/migrate.md](../../docs/migrate.md) has the steps for a real mesh. | 12 |

`kubectl get devices` shows whether each device is ready, and the member
and claim that hold it. `-o wide` adds whether it is preemptible, any
preemption request, and any transfer offer. `kubectl get deviceclaims`
shows the phase, the device, and when the member's lease ends. The fencing token and the
priority are in the objects: `-o yaml` shows them.

Scene 6 is the reason for fencing tokens (ADR 0008). The paused workload
acts on an old view, and no clock check can stop it. The device sees the
higher token first, so it rejects the lower one.

## The parts

- `manifests/devices.yaml`: the device pool.
- `manifests/claims-a.yaml`, `manifests/claims-b.yaml`: the claims of
  each cluster, with label and CEL selectors and priorities.
- `manifests/urgent.yaml`: the claim that preempts.
- `manifests/transfer-b.yaml`: the pre-bound claim in `b` that takes
  `nic-1` in a transfer.
- `manifests/claim.yaml`: the `job` claim for the H100, and a workload
  that uses the device with the claim's token.
- `cmd/solas-demo`: the device gatekeeper (`device`) and the workload
  (`workload`). They are not part of solas.
- `mesh.sh up|down|url dynamodb|etcd`: the Docker network `solas-mesh`,
  the table container `solas-ddb`, and the etcd container `solas-etcd`.
- `join.sh <name>`: the k3d cluster `e2e-<name>`, with solas deployed.
  Its kubeconfig goes to `demo/k3d/.kube/<name>`.
- `switch-store.sh [copy|point] etcd|dynamodb`: moves the mesh to the other
  store. `copy` runs `solas migrate`; `point` sets the new storage URL in
  each cluster and restarts solas.
- `demo.sh`: the scenes above, with a check after each one.
- `down.sh`: removes everything.

## Try it by hand

```sh
demo/k3d/mesh.sh up
demo/k3d/join.sh a
export KUBECONFIG=demo/k3d/.kube/a
kubectl apply -f demo/k3d/manifests/devices.yaml
kubectl apply -f demo/k3d/manifests/claims-a.yaml
kubectl get devices
kubectl get deviceclaims -n work
```

Without a `Ready` condition, the CEL selectors that check it match no
device. Set one with a status patch:

```sh
kubectl patch device gpu-a100-1 --subresource=status --type=merge -p \
  '{"status":{"conditions":[{"type":"Ready","status":"True","reason":"Healthy",
  "message":"by hand","lastTransitionTime":"2026-01-01T00:00:00Z"}]}}'
```

`demo/k3d/down.sh` removes everything when you are done.
