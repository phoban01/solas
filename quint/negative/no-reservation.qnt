// The bind skips the usage set. Member a, with a limit of 1, binds two
// devices. Spec 15.2.
module no_reservation {
  import limits(useReservation = false, GRACE = 3, TIMEOUT = 2).* from "../limits"

  // The test passes when the steps break withinLimit.
  run overLimitTest =
    init
      .then(sendOnly("a", "d1"))
      .then(sendOnly("a", "d2"))
      .then(land({ m: "a", d: "d1", at: 0 }))
      .then(land({ m: "a", d: "d2", at: 0 }))
      .expect(not(withinLimit))
}
