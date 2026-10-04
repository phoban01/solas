// Model of a restore of the store from a backup, ADR 0018 and spec 13.
// One device. Binds issue fencing tokens. A backup copies the store. A
// restore puts the backup back and, with the epoch, moves the store to a
// new epoch above every epoch it had. A token is epoch * BIG + n, so it
// compares as the pair (epoch, n).
module restore {
  // The negative model sets this to false: a restore keeps the old epoch.
  const useEpoch: bool
  // BIG is the size of the token range of one epoch, 2^32 in the code.
  const BIG: int
  // MAXB bounds the binds of a run.
  const MAXB: int

  type Snapshot = { token: int, epoch: int }

  // The store: the token of the device and the epoch of the store.
  var token: int
  var epoch: int
  // The one backup, taken at some point of the run.
  var backup: Snapshot
  var hasBackup: bool
  // Ghosts: every epoch the store had, every token a bind issued, and
  // false once a bind issued a token that is not above every earlier one.
  var maxEpoch: int
  var issued: Set[int]
  var tokensGrow: bool

  action init = all {
    token' = 0,
    epoch' = 0,
    backup' = { token: 0, epoch: 0 },
    hasBackup' = false,
    maxEpoch' = 0,
    issued' = Set(),
    tokensGrow' = true,
  }

  // A bind issues the next token, spec 5.3 and 13. The first bind of a new
  // epoch starts the epoch's range.
  action bind = {
    val nt = if (token < epoch * BIG) epoch * BIG + 1 else token + 1
    all {
      issued.size() < MAXB,
      token' = nt,
      issued' = issued.union(Set(nt)),
      tokensGrow' = (tokensGrow and issued.forall(t => t < nt)),
      epoch' = epoch,
      backup' = backup,
      hasBackup' = hasBackup,
      maxEpoch' = maxEpoch,
    }
  }

  action takeBackup = all {
    not(hasBackup),
    backup' = { token: token, epoch: epoch },
    hasBackup' = true,
    token' = token,
    epoch' = epoch,
    maxEpoch' = maxEpoch,
    issued' = issued,
    tokensGrow' = tokensGrow,
  }

  // A restore puts the backup back. With the epoch, the restore sets an
  // epoch above every epoch the store had, spec 13.
  action restore = {
    val e = if (useEpoch) maxEpoch + 1 else backup.epoch
    all {
      hasBackup,
      token' = backup.token,
      epoch' = e,
      maxEpoch' = if (e > maxEpoch) e else maxEpoch,
      backup' = backup,
      hasBackup' = hasBackup,
      issued' = issued,
      tokensGrow' = tokensGrow,
    }
  }

  action step = any { bind, takeBackup, restore }

  // Each bind issues a token above every token issued before, across
  // restores, so a gatekeeper never sees a token go back, spec 6.6.
  val tokenOrder = tokensGrow

  // Witness: a run restores and binds again.
  val witnessRestored = not(epoch > 0 and token > epoch * BIG)

  // A bind before a backup, a restore after a later bind, and a bind
  // after the restore: the last token is above all.
  run restoreTest =
    init
      .then(bind)
      .then(takeBackup)
      .then(bind)
      .then(bind)
      .then(restore)
      .then(bind)
      .expect(tokenOrder)

  // Two restores from the same backup still move up.
  run twoRestoresTest =
    restoreTest
      .then(restore)
      .then(bind)
      .expect(tokenOrder)
}

module restore2 {
  import restore(useEpoch = true, BIG = 100, MAXB = 6).*
}
