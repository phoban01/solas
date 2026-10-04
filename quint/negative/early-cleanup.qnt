// The cleanup does not wait longer than the deadline of a request. It
// removes the entry of a bind that is still in flight, a second bind takes
// the free place, and the first bind lands too. Spec 15.3.
module early_cleanup {
  import limits(useReservation = true, GRACE = 1, TIMEOUT = 2).* from "../limits"

  // The test passes when the steps break withinLimit.
  run earlyCleanupTest =
    init
      .then(reserve("a", "d1"))
      .then(tick)
      .then(cleanup("a", "d1"))
      .then(reserve("a", "d2"))
      .then(land({ m: "a", d: "d2", at: 1 }))
      .then(land({ m: "a", d: "d1", at: 0 }))
      .expect(not(withinLimit))
}
