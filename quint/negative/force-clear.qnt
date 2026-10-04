// A preemptor clears a holder that did not respond, after the grace period
// plus D, as spec 10.8 first said. The holder stays live and keeps
// renewing, but its controller never sees the request. Its claim is still
// in effect when the device stops naming it.
module force_clear {
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
    skipTokenVersion: 0,
    readGap: 15,
    reclaim: Map("d1" -> "Delete", "d2" -> "Delete"),
    reclaimDelay: 0,
  }).* from "../solas"

  pure val noDrift = Map("a" -> 0, "b" -> 0)

  action liveTick = tickBy(noDrift).then(renewSend("a")).then(renewLand("a"))

  run forceClearTest =
    init
      .then(join("a"))
      .then(bindSend("a2", "d1"))
      .then(bindLand({ cluster: "a", claim: "a2", dev: "d1", rv: 0, muid: 1 }))
      .then(adopt("a2", "d1"))
      .then(bindSend("a1", "d2"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d2", rv: 0, muid: 1 }))
      .then(adopt("a1", "d2"))
      .then(join("b"))
      .then(requestPreemption("b1", "d1"))
      .then(liveTick)
      .then(liveTick)
      .then(liveTick)
      .then(liveTick)
      .then(liveTick)
      .then(liveTick)
      .then(renewSend("b"))
      .then(renewLand("b"))
      .then(forceClear("b1"))
      .expect(effective("a2") and not(claimMatchesDevice))
}
