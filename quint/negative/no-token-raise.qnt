// Release 2 does not raise the fencing token on a bind. Cluster a runs
// release 1 and b runs release 2. After b reclaims d1 from a, b binds it
// with the same token that a got, so two binds share one token. Spec 5.3
// and 11.2: the token rule must hold across releases.
module no_token_raise {
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
    noTokenCheck: false,
    priority: Map("a1" -> 1, "a2" -> 0, "b1" -> 2),
    preemptible: Map("d1" -> true, "d2" -> false),
    grace: 20,
    forceClear: true,
    noPreemptionWait: false,
    version: Map("a" -> 1, "b" -> 2),
    skipTokenVersion: 2,
    readGap: 15,
    reclaim: Map("d1" -> "Delete", "d2" -> "Delete"),
    reclaimDelay: 0,
  }).* from "../solas"

  pure val fastB = Map("a" -> 0, "b" -> 1)

  // The test passes when the steps break tokensUnique.
  run tokenRaiseTest =
    init
      .then(join("a"))
      .then(bindSend("a1", "d1"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d1", rv: 0, muid: 1 }))
      .then(adopt("a1", "d1"))
      .then(join("b"))
      .then(observe("b", "a"))
      .then(crash("a"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(sweepDelete("b", "a"))
      .then(sweepListDevices("b"))
      .then(sweepListMembers("b"))
      .then(sweepClear("b", "d1"))
      .then(renewSend("b"))
      .then(renewLand("b"))
      .then(bindSend("b1", "d1"))
      .then(bindLand({ cluster: "b", claim: "b1", dev: "d1", rv: 2, muid: 2 }))
      .expect(not(tokensUnique))
}
