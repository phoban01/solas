// The device accepts any token. After a reclaim, the old holder uses the
// device after the new holder did. Spec 6.6.
module no_token_check {
  import solas(CFG = {
    clusters: Set("a", "b"),
    devices: Set("d1", "d2"),
    claims: Set("a1", "a2", "b1"),
    owner: Map("a1" -> "a", "a2" -> "a", "b1" -> "b"),
    clock0: Map("a" -> 0, "b" -> 1000),
    D: 30,
    M: 6,
    tick: 10,
    drift: 1,
    unconditionalBind: false,
    membersFirst: false,
    adoptAnyUid: false,
    noTokenCheck: true,
    priority: Map("a1" -> 1, "a2" -> 0, "b1" -> 2),
    preemptible: Map("d1" -> true, "d2" -> false),
    grace: 20,
    forceClear: false,
    noPreemptionWait: false,
    version: Map("a" -> 1, "b" -> 2),
    skipTokenVersion: 0,
  }).* from "../solas"

  pure val fastB = Map("a" -> 0, "b" -> 1)

  // The steps of fencingTest in solas2. Here the device accepts a's stale
  // token, so usesInOrder breaks.
  run staleUseTest =
    init
      .then(join("a"))
      .then(bindSend("a1", "d1"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d1", rv: 0, muid: 1 }))
      .then(adopt("a1", "d1"))
      .then(use("a1"))
      .then(observe("b", "a"))
      .then(tickBy(fastB))
      .then(tickBy(fastB))
      .then(tickBy(fastB))
      .then(sweepDelete("b", "a"))
      .then(sweepListDevices("b"))
      .then(sweepListMembers("b"))
      .then(sweepClear("b", "d1"))
      .then(join("b"))
      .then(bindSend("b1", "d1"))
      .then(bindLand({ cluster: "b", claim: "b1", dev: "d1", rv: 2, muid: 2 }))
      .then(adopt("b1", "d1"))
      .then(use("b1"))
      .then(use("a1"))
      .expect(not(usesInOrder))
}
