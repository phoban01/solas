// The versions come from a sequence, not from the row lock: a write takes
// its version in one step and commits in a later one. A list in one
// snapshot then returns the counter as its version while a write below
// it has not committed. Spec 2.8.
module sequence_not_lock {
  import store(MAXW = 3, noGapCheck = false, noListRecheck = false, lockOrder = false, snapshotList = true).* from "../store"
}
