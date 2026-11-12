# Model-based tests

The model-based tests check that the real solas API server behaves as the
Quint model says it does. They replay traces of `quint/solas.qnt` against
the real Device and Member REST stores, on dynamodb-local.

Run them with `devbox run mbt`, which replays the traces on dynamodb-local,
on etcd, and on Postgres; `MBT_STORE` picks one store. `devbox run verify` runs them too,
and skips them when the model and the code are unchanged since a green run.

## How it works

1. `quint run --mbt` writes traces. Each state says which action ran,
   which choices it made, and the model state after it.
2. The driver in `mbt/driver.go` turns each server write of the model
   into a REST call:

   | Model action | REST call |
   |--------------|-----------|
   | `join` | create the Member, or renew it when it exists |
   | `renewLand` | status update of the Member, with the version the member last saw |
   | `sweepDelete`, `finishLeave` | delete the Member with a version precondition |
   | `landSome` | status update of the Device that sets `claimRef` |
   | `sweepClear`, `release`, `drainRelease`, `releaseDuplicate`, `clearOrphan` | status update of the Device that clears `claimRef` |

   Other actions, such as `tick` or `observe`, stay inside a controller
   in the model and write nothing.
3. The driver keeps a table from model resource versions to real ones.
   A write carries the real version of the model version.
4. Each write must succeed when the model applied it, and fail when the
   model did not.
5. After each step, every device must have the holder and the fencing
   token of the model, and every Member must exist or not, with the UID
   of the model.

`TestMBTCatchesADivergence` changes a trace on purpose and checks that
the replay reports it.

## What it covers, and what not

It covers the server side of the protocol: conditional writes, the
holder-to-holder rule, the fencing token rule, and version preconditions
on Members. It uses the real strategies, the real validation, the watch
cache, and each of the three stores.

It does not cover the controllers. The simulator gate
(`docs/sim-gate.md`) runs the real controllers against in-memory stores.
The two together cover the code on both sides of the API.

## Settings

| Variable | Default | Meaning |
|----------|---------|---------|
| `MBT_COUNT` | 40 | traces to generate |
| `MBT_STEPS` | 30 | steps per trace |
| `MBT_SEED` | `0x5eed` | seed of the trace generator |

A one-off run of 300 traces of 50 steps with seed `0xbeef` found no
difference.
