// The tool copies with no seal. A cluster still on the source binds the
// device after the copy, and a cluster on the destination binds it too.
// Spec 12.2: "The tool MUST seal the source before it reads it."
module no_seal {
  import migrate(useSeal = false, useEpoch = true).* from "../migrate"

  // The steps of the counterexample. The test passes when they break
  // noDoubleBind.
  run doubleBindTest =
    init
      .then(copy)
      .then(switchStore("b"))
      .then(bind("a"))
      .then(bind("b"))
      .expect(not(noDoubleBind))
}
