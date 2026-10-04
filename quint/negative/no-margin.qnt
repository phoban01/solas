// The holder uses no safety margin, M = 0. With clock drift, an observer
// can expire the lease while the holder still counts itself live.
// Spec 7.5.
module no_margin {
  import solas(CFG = {
    clusters: Set("a", "b"),
    devices: Set("d1", "d2"),
    claims: Set("a1", "a2", "b1"),
    owner: Map("a1" -> "a", "a2" -> "a", "b1" -> "b"),
    clock0: Map("a" -> 0, "b" -> 1000),
    D: 30,
    M: 0,
    tick: 10,
    drift: 1,
    unconditionalBind: false,
    membersFirst: false,
    adoptAnyUid: false,
    noTokenCheck: false,
    priority: Map("a1" -> 1, "a2" -> 0, "b1" -> 2),
    preemptible: Map("d1" -> true, "d2" -> false),
    grace: 20,
    forceClear: false,
    noPreemptionWait: false,
    version: Map("a" -> 1, "b" -> 2),
    skipTokenVersion: 0,
  }).* from "../solas"
}
