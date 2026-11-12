// The list skips the second counter read. A write that commits during the
// query then shows in a list at an older version. Spec 2.4.
module list_no_recheck {
  import store(MAXW = 3, noGapCheck = false, noListRecheck = true, lockOrder = true, snapshotList = false).* from "../store"
}
