// A restore keeps the old epoch. The bind after the restore issues a token
// that a bind before the restore already issued, so a gatekeeper that saw
// the higher token rejects the new holder, or one that lost its state
// accepts an old holder. Spec 13.
module no_epoch {
  import restore(useEpoch = false, BIG = 100, MAXB = 6).* from "../restore"

  // The test passes when the steps break tokenOrder.
  run tokenBackTest =
    init
      .then(bind)
      .then(takeBackup)
      .then(bind)
      .then(bind)
      .then(restore)
      .then(bind)
      .expect(not(tokenOrder))
}
