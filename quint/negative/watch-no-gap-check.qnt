// The watcher delivers what it finds and skips the gap check. An expired
// event is then lost with no 410 Gone. Spec 4.3.
module watch_no_gap_check {
  import store(MAXW = 3, noGapCheck = true, noListRecheck = false, lockOrder = true, snapshotList = false).* from "../store"
}
