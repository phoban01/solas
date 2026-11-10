// The tool copies but does not move the destination to a new epoch. The
// first write on the destination gets version 1, below every version of
// the source, so a client that keeps a version of the source is never
// told to list again. Spec 12.2: "After the verify, and before any cluster
// writes to the destination, the tool MUST move the destination to a new
// epoch, spec 13.1, with every resource version that it issues above
// every resource version of the source, spec 13.3."
module no_move_epoch {
  import migrate(useSeal = true, useEpoch = false).* from "../migrate"

  // The steps of the counterexample. The test passes when they break
  // versionOrder.
  run versionBackTest =
    init
      .then(bind("a"))
      .then(release("a"))
      .then(seal)
      .then(copy)
      .then(switchStore("a"))
      .then(switchStore("b"))
      .then(bind("b"))
      .expect(not(versionOrder))
}
