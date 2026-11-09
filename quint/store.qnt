// Model of one resource in the store: the counter, the event log, TTL
// deletes, one watcher, and one lister. spec/solas.md sections 2.4, 3,
// and 4 give the rules.
module store {
  type Watcher = {
    // The version of the last delivered event. The watch starts at 0.
    last: int,
    delivered: Set[int],
    // False once a delivery holds a version not above an earlier one.
    ordered: bool,
    gone: bool,
    // 0: idle. 1: read the counter, value c.
    phase: int,
    c: int,
  }

  type Lister = {
    // 0: idle. 1: read the counter, value c1. 2: queried the objects.
    phase: int,
    c1: int,
    // The last write that the query saw. The writes are 1 to counter, so
    // the objects of the query are the state after write q.
    q: int,
    // The version of the last list that returned, and the write that its
    // objects show.
    done: bool,
    rv: int,
    shows: int,
  }

  // The number of writes the model allows.
  const MAXW: int
  // The negative models set these to skip the gap check of spec 4.3, or
  // the second counter read of a list, spec 2.4.
  const noGapCheck: bool
  const noListRecheck: bool

  var counter: int
  var log: Set[int]
  var w: Watcher
  var l: Lister

  action init = all {
    counter' = 0,
    log' = Set(),
    w' = { last: 0, delivered: Set(), ordered: true, gone: false, phase: 0, c: 0 },
    l' = { phase: 0, c1: 0, q: 0, done: false, rv: 0, shows: 0 },
  }

  // A write increments the counter and adds its event in one
  // transaction, spec 2.3 and 3.1.
  action write = all {
    counter < MAXW,
    counter' = counter + 1,
    log' = log.union(Set(counter + 1)),
    w' = w,
    l' = l,
  }

  // Several writes of one server arrive together and commit in one
  // transaction: the counter moves by their number and the log gets each
  // of the versions, spec 2.3.
  action batchWrite = all {
    counter < MAXW,
    nondet k = 1.to(3).filter(x => counter + x <= MAXW).oneOf()
    all {
      counter' = counter + k,
      log' = log.union((counter + 1).to(counter + k)),
      w' = w,
      l' = l,
    },
  }

  // DynamoDB deletes an expired event, late and in any order, spec 4.1.
  action expire = all {
    log.size() > 0,
    nondet v = log.oneOf()
    all {
      counter' = counter,
      log' = log.exclude(Set(v)),
      w' = w,
      l' = l,
    },
  }

  // Each poll reads the counter first, spec 4.2.
  action pollRead = all {
    not(w.gone),
    w.phase == 0,
    counter' = counter,
    log' = log,
    w' = { ...w, phase: 1, c: counter },
    l' = l,
  }

  // Then it queries the versions from last + 1 to c.
  action pollQuery = {
    val want = 1.to(MAXW).filter(v => v > w.last and v <= w.c)
    val found = want.intersect(log)
    val complete = want.subseteq(log)
    all {
      not(w.gone),
      w.phase == 1,
      counter' = counter,
      log' = log,
      l' = l,
      if (complete or noGapCheck)
        w' = {
          ...w,
          phase: 0,
          last: w.c,
          delivered: w.delivered.union(found),
          ordered: w.ordered and found.forall(v => w.delivered.forall(u => u < v)),
        }
      else
        w' = { ...w, phase: 0, gone: true },
    }
  }

  // A list reads the counter, spec 2.4.
  action listRead = all {
    l.phase == 0,
    counter' = counter,
    log' = log,
    w' = w,
    l' = { ...l, phase: 1, c1: counter },
  }

  // Then it queries the object items. They show every write so far.
  action listQuery = all {
    l.phase == 1,
    counter' = counter,
    log' = log,
    w' = w,
    l' = { ...l, phase: 2, q: counter },
  }

  // Then it reads the counter again. Equal values return the list at c1.
  // Different values retry. The negative model returns c1 at once.
  action listCheck = all {
    l.phase == 2,
    counter' = counter,
    log' = log,
    w' = w,
    if (noListRecheck or counter == l.c1)
      l' = { ...l, phase: 0, done: true, rv: l.c1, shows: l.q }
    else
      l' = { ...l, phase: 0 },
  }

  action step = any { write, batchWrite, expire, pollRead, pollQuery, listRead, listQuery, listCheck }

  // Delivered versions strictly increase, spec 3.2 and 4.2.
  val monotonicRV = w.ordered

  // A watch delivers every version up to last, or it ends with 410 Gone,
  // spec 4.2 and 4.3.
  val watchComplete = w.gone
    or 1.to(MAXW).forall(v => (v <= w.last) == w.delivered.contains(v))

  // A list is a consistent snapshot at the version it returns: its
  // objects show exactly the writes up to that version, spec 2.4 and 2.5.
  val listSnapshot = not(l.done) or l.shows == l.rv

  val storeSafety = monotonicRV and watchComplete and listSnapshot

  // Witnesses: a watch delivers events, a watch ends with 410 Gone, and a
  // list returns after a write.
  val witnessDelivered = w.delivered.size() < 2
  val witnessGone = not(w.gone)
  val witnessListed = not(l.done and l.rv > 0)
}

module store3 {
  import store(MAXW = 3, noGapCheck = false, noListRecheck = false).*
}
