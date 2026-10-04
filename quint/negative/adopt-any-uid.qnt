// Adopt ignores the member UID. A bind from an old member identity can
// land after a rejoin. The claim adopts it, and the sweeper then clears
// it while the claim is in effect. Spec 6.3.
module adopt_any_uid {
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
    adoptAnyUid: true,
    noTokenCheck: false,
    priority: Map("a1" -> 1, "a2" -> 0, "b1" -> 2),
    preemptible: Map("d1" -> true, "d2" -> false),
    grace: 20,
    forceClear: false,
    noPreemptionWait: false,
    version: Map("a" -> 1, "b" -> 2),
    skipTokenVersion: 0,
    readGap: 15,
    reclaim: Map("d1" -> "Delete", "d2" -> "Delete"),
    reclaimDelay: 0,
    protected: Set(),
  }).* from "../solas"

  // Random search does not reach this bug, so a test plays it step by step.

  pure val fastB = Map("a" -> 0, "b" -> 1)

  run staleAdoptTest =
    init
      .then(join("a"))
      .then(bindSend("a1", "d1"))
      // The bind lands. The claim is still Pending.
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d1", rv: 0, muid: 1 }))
      .then(observe("b", "a"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      // b expires and deletes a's Member.
      .then(sweepDelete("b", "a"))
      // a's renew finds no Member, so a joins again with UID 2.
      .then(renewSend("a"))
      .then(renewLand("a"))
      .then(join("a"))
      // a adopts d1, which names the old UID 1.
      .then(adopt("a1", "d1"))
      .then(sweepListDevices("b"))
      .then(sweepListMembers("b"))
      // b clears d1, because no Member has UID 1.
      .then(sweepClear("b", "d1"))
      .expect(not(claimMatchesDevice))
}
