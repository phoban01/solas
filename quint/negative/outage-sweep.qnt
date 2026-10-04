// The observer keeps its observations across a store outage, as spec 8.1
// first said. When the store returns after more than D, b sweeps a at
// once, although a renews as soon as it can. Every member of the mesh
// loses its devices at the same time. Spec 8.1.
module outage_sweep {
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
    readGap: 0,
    reclaim: Map("d1" -> "Delete", "d2" -> "Delete"),
    reclaimDelay: 0,
  }).* from "../solas"

  pure val fastB = Map("a" -> 0, "b" -> 1)

  // The test passes when b sweeps a right after the outage.
  run sweepOnReturnTest =
    init
      .then(join("a"))
      .then(join("b"))
      .then(observe("b", "a"))
      .then(tickBy(fastB))
      .then(tickBy(fastB))
      .then(tickBy(fastB))
      .then(sweepDelete("b", "a"))
      .expect(not(s.members.get("a").exists))
}
