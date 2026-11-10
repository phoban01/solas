// Model of a move from one store to another, spec section 12 and ADR
// 0015. One device, two clusters with one claim each, a source store and
// a destination store. A cluster binds and releases in the store it
// writes to. The tool seals the source, copies it, and each cluster
// switches to the destination.
//
// The protocol model, solas.qnt, covers bind and release in one store.
// This model covers only what changes in a move: two stores, and
// clusters that write to different stores for a while.
module migrate {
  // The negative model sets this to false: the tool copies with no seal.
  const useSeal: bool
  // The negative model sets this to false: the copy leaves the versions
  // of the destination where they were, instead of moving it to a new
  // epoch above every version of the source, spec 12.2.
  const useEpoch: bool

  // rv is the last resource version that the store issued.
  type Store = { holder: str, token: int, rv: int }
  val NONE = ""
  val CLUSTERS = Set("a", "b")
  val STORES = Set("src", "dst")

  // stores: the device in each store. at: the store that each cluster
  // writes to. bound: the claim of the cluster is in effect.
  var stores: str -> Store
  var at: str -> str
  var bound: str -> bool
  var sealed: bool
  var copied: bool
  // The last fencing token that a bind issued, in any store, and false
  // once a bind issued a token that is not above it.
  var lastToken: int
  var tokensGrow: bool
  // The last resource version that a write got, in any store, and false
  // once a write got a version that is not above it.
  var lastRV: int
  var rvGrow: bool

  action init = all {
    stores' = STORES.mapBy(_ => { holder: NONE, token: 0, rv: 0 }),
    at' = CLUSTERS.mapBy(_ => "src"),
    bound' = CLUSTERS.mapBy(_ => false),
    sealed' = false,
    copied' = false,
    lastToken' = 0,
    tokensGrow' = true,
    lastRV' = 0,
    rvGrow' = true,
  }

  // A sealed store rejects every write, spec 12.1.
  def writable(s: str): bool = not(s == "src" and sealed)

  // Bind the free device in the cluster's store, spec 6.3. The store
  // gives the next token, spec 5.3, and the next version, spec 3.1.
  action bind(c: str): bool = {
    val s = at.get(c)
    val d = stores.get(s)
    val t = d.token + 1
    val v = d.rv + 1
    all {
      writable(s),
      not(bound.get(c)),
      d.holder == NONE,
      stores' = stores.set(s, { holder: c, token: t, rv: v }),
      bound' = bound.set(c, true),
      lastToken' = if (t > lastToken) t else lastToken,
      tokensGrow' = (tokensGrow and t > lastToken),
      lastRV' = if (v > lastRV) v else lastRV,
      rvGrow' = (rvGrow and v > lastRV),
      at' = at,
      sealed' = sealed,
      copied' = copied,
    }
  }

  // Release the device, spec 6.4. The claim stops being in effect first.
  action release(c: str): bool = {
    val s = at.get(c)
    val v = stores.get(s).rv + 1
    all {
      writable(s),
      bound.get(c),
      stores.get(s).holder == c,
      stores' = stores.set(s, { ...stores.get(s), holder: NONE, rv: v }),
      bound' = bound.set(c, false),
      at' = at,
      sealed' = sealed,
      copied' = copied,
      lastToken' = lastToken,
      tokensGrow' = tokensGrow,
      lastRV' = if (v > lastRV) v else lastRV,
      rvGrow' = (rvGrow and v > lastRV),
    }
  }

  // The tool seals the source, spec 12.1.
  action seal = all {
    useSeal,
    not(sealed),
    sealed' = true,
    stores' = stores,
    at' = at,
    bound' = bound,
    copied' = copied,
    lastToken' = lastToken,
    tokensGrow' = tokensGrow,
    lastRV' = lastRV,
    rvGrow' = rvGrow,
  }

  // The tool copies the source into an empty destination, after the seal,
  // spec 12.2. The copy keeps the holder and the token. Then the tool
  // moves the destination to a new epoch: its next version is above every
  // version of the source.
  action copy = {
    val src = stores.get("src")
    val dst = stores.get("dst")
    all {
      sealed or not(useSeal),
      not(copied),
      dst.holder == NONE and dst.token == 0,
      stores' = stores.set("dst", { holder: src.holder, token: src.token,
        rv: if (useEpoch and src.rv > dst.rv) src.rv else dst.rv }),
      copied' = true,
      at' = at,
      bound' = bound,
      sealed' = sealed,
      lastToken' = lastToken,
      tokensGrow' = tokensGrow,
      lastRV' = lastRV,
      rvGrow' = rvGrow,
    }
  }

  // A cluster changes its storage URL and restarts, spec 12.4. Its claim
  // stays in effect only when the destination names it: the member renews
  // its Member, which has the same UID, spec 7.2, and the claim goes back
  // to Bound only if its device still names it, spec 6.5.
  action switchStore(c: str): bool = all {
    copied,
    at.get(c) == "src",
    at' = at.set(c, "dst"),
    bound' = bound.set(c, bound.get(c) and stores.get("dst").holder == c),
    stores' = stores,
    sealed' = sealed,
    copied' = copied,
    lastToken' = lastToken,
    tokensGrow' = tokensGrow,
    lastRV' = lastRV,
    rvGrow' = rvGrow,
  }

  action step = {
    nondet c = CLUSTERS.oneOf()
    any { bind(c), release(c), seal, copy, switchStore(c) }
  }

  // No two claims in effect hold the device, in either store.
  val noDoubleBind = not(bound.get("a") and bound.get("b"))

  // Each bind issues a token above every earlier one, so the device never
  // sees a token go down, spec 12.2.
  val tokenOrder = tokensGrow

  // Each write gets a version above every earlier one, in the source
  // before the move and in the destination after it, spec 12.2.
  val versionOrder = rvGrow

  val migrateSafety = noDoubleBind and tokenOrder and versionOrder

  // Witnesses: both clusters switch, and a claim is in effect on the
  // destination.
  val witnessSwitched = not(at.get("a") == "dst" and at.get("b") == "dst")
  val witnessBoundOnDst = not(CLUSTERS.exists(c => at.get(c) == "dst" and bound.get(c)))

  // a holds the device. The tool seals and copies.
  run moved =
    init
      .then(bind("a"))
      .then(seal)
      .then(copy)

  // A sealed source rejects a bind, spec 12.1.
  run sealedTest = init.then(seal).then(bind("a").fail())

  // The tool copies only after the seal, spec 12.2.
  run copyNeedsSealTest = init.then(copy.fail())

  // a keeps the device after both clusters switch.
  run keepHolderTest =
    moved
      .then(switchStore("a"))
      .then(switchStore("b"))
      .expect(bound.get("a") and stores.get("dst") == { holder: "a", token: 1, rv: 1 } and migrateSafety)

  // b cannot bind the device on the destination while a holds it.
  run noSecondHolderTest = keepHolderTest.then(bind("b").fail())

  // After the move, the next bind gets the old token plus 1, and a version
  // above every version of the source.
  run nextTokenTest =
    keepHolderTest
      .then(release("a"))
      .then(bind("b"))
      .expect(stores.get("dst").token == 2 and stores.get("dst").rv == 3 and migrateSafety)
}

module migrate2 {
  import migrate(useSeal = true, useEpoch = true).*
}
