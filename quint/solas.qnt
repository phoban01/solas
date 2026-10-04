// Model of the solas protocol: members, devices, claims, and the sweeper.
// spec/solas.md sections 5 to 10 give the rules. The store is a
// linearizable map with conditional writes. The model assumes only the
// store contract of spec 2.5, so it holds for every store that keeps the
// contract. docs/store-contract.md shows how each store keeps it.
module solas {
  // A claimRef, spec 5.2. The claim ID stands for the claim UID.
  // prio is the priority of the claim, spec 10.2.
  type ClaimRef = { member: str, muid: int, claim: str, prio: int }
  type OptRef = Held(ClaimRef) | Free

  // token is the fencing token of the last bind, spec 5.3.
  // preempt is a preemption request, spec 10.5.
  // offer is a transfer offer to a named claim, spec 14.
  type Device = { rv: int, ref: OptRef, token: int, preempt: OptRef, offer: OptRef }

  // A Member object in the table, spec 7.1.
  type MemberObj = { exists: bool, uid: int, rv: int }

  // A DeviceClaim in the owner's etcd, spec 6.1.
  type Claim = {
    exists: bool,
    deleting: bool,
    phase: str,
    dev: str,
    muid: int,
    // The fencing token of the bind, spec 6.3.
    token: int,
    // The holder's local time when it first saw a preemption request.
    seenAt: int,
    // The preemptor's local time when it wrote its request, and the device.
    reqAt: int,
    reqDev: str,
  }

  // What an observer saw of another Member, spec 8.1. rv -1 means none.
  type Seen = { rv: int, at: int }

  // The local state of one member cluster and its controller.
  type Cluster = {
    up: bool,
    left: bool,
    clock: int,
    uid: int,
    memberRv: int,
    renewedAt: int,
    renewInFlight: bool,
    renewSentAt: int,
    renewRv: int,
    draining: bool,
    seen: str -> Seen,
    // lastRead is the local time of the last read of the member list that
    // worked, spec 8.1.
    lastRead: int,
    // stale holds, for each device, the member UID of a holder that the
    // sweeper found gone, and the local time at which it first did, for
    // the reclaim time of Delay, spec 8.5.
    stale: str -> { muid: int, at: int },
    sweepPhase: int,
    sweepDevs: str -> Device,
    sweepUids: Set[int],
  }

  // A bind write in flight from a controller to the table.
  type BindMsg = { cluster: str, claim: str, dev: str, rv: int, muid: int }

  // A use of a device that the device accepted.
  type Use = { token: int, claim: str }

  // A bind: the claim that got a token of a device.
  type Binding = { dev: str, token: int, claim: str }

  type Config = {
    clusters: Set[str],
    devices: Set[str],
    claims: Set[str],
    owner: str -> str,
    clock0: str -> int,
    D: int,
    M: int,
    tick: int,
    drift: int,
    // Flags that break one rule each, for the negative models.
    unconditionalBind: bool,
    membersFirst: bool,
    adoptAnyUid: bool,
    noTokenCheck: bool,
    // Priority of each claim and preemptibility of each device, spec 10.
    priority: str -> int,
    preemptible: str -> bool,
    grace: int,
    // forceClear lets a preemptor clear a holder that did not respond,
    // spec 10.8. noPreemptionWait drops the wait before it.
    forceClear: bool,
    noPreemptionWait: bool,
    // The release of the solas server of each cluster, spec 11. The
    // server of the cluster that binds issues the token. skipTokenVersion
    // names a release that does not raise the token on a bind; 0 names
    // none. The negative model no-token-raise sets it.
    version: str -> int,
    skipTokenVersion: int,
    // readGap is how long an observer may go without a read of the member
    // list before it forgets what it saw, spec 8.1. 0 turns it off; the
    // negative model outage-sweep does that.
    readGap: int,
    // reclaim is the reclaim policy of each device: Delete, Delay, or
    // Retain, spec 8.5. reclaimDelay is the reclaim time R of Delay.
    reclaim: str -> str,
    reclaimDelay: int,
    // protected names the claims that an operator lets the member
    // protect: their devices can only be released, spec 10.10.
    protected: Set[str],
  }

  type State = {
    now: int,
    devices: str -> Device,
    devCounter: int,
    members: str -> MemberObj,
    memberCounter: int,
    nextUid: int,
    clusters: str -> Cluster,
    claims: str -> Claim,
    inflight: Set[BindMsg],
    // Ghost flags for the witnesses.
    cleared: bool,
    someLeft: bool,
    // Device side of fencing, spec 6.6: the highest accepted token and
    // the last accepted use of each device.
    highest: str -> int,
    lastUse: str -> Use,
    // Ghosts for the fencing invariants and witness.
    bindings: Set[Binding],
    staleAccepted: bool,
    staleRejected: bool,
    // Ghost: true once the sweeper cleared a device with the Retain
    // policy, spec 8.5. The invariant retainNeverSwept keeps it false.
    sweptRetained: bool,
  }

  const CFG: Config

  var s: State

  pure val NOSEEN: Seen = { rv: -1, at: 0 }
  pure val NOUSE: Use = { token: 0, claim: "" }

  pure def initCluster(cfg: Config, c: str): Cluster = {
    up: true,
    left: false,
    clock: cfg.clock0.get(c),
    uid: 0,
    memberRv: 0,
    renewedAt: 0,
    renewInFlight: false,
    renewSentAt: 0,
    renewRv: 0,
    draining: false,
    seen: cfg.clusters.mapBy(_ => NOSEEN),
    lastRead: cfg.clock0.get(c),
    stale: cfg.devices.mapBy(_ => { muid: -1, at: 0 }),
    sweepPhase: 0,
    sweepDevs: cfg.devices.mapBy(_ => { rv: 0, ref: Free, token: 0, preempt: Free, offer: Free }),
    sweepUids: Set(),
  }

  action init = s' = {
    now: 0,
    devices: CFG.devices.mapBy(_ => { rv: 0, ref: Free, token: 0, preempt: Free, offer: Free }),
    devCounter: 0,
    members: CFG.clusters.mapBy(_ => { exists: false, uid: 0, rv: 0 }),
    memberCounter: 0,
    nextUid: 1,
    clusters: CFG.clusters.mapBy(c => initCluster(CFG, c)),
    claims: CFG.claims.mapBy(_ => {
      exists: true, deleting: false, phase: "Pending", dev: "", muid: 0, token: 0,
      seenAt: 0, reqAt: 0, reqDev: "",
    }),
    inflight: Set(),
    cleared: false,
    someLeft: false,
    highest: CFG.devices.mapBy(_ => 0),
    lastUse: CFG.devices.mapBy(_ => NOUSE),
    bindings: Set(),
    staleAccepted: false,
    staleRejected: false,
    sweptRetained: false,
  }

  // ---- Time, spec 7.5 ----

  // Real time moves by one tick. Each clock moves by one tick plus or
  // minus the drift, so the rate drift bound rho is drift / tick.
  action tickBy(adv: str -> int): bool = s' = {
    ...s,
    now: s.now + CFG.tick,
    clusters: s.clusters.keys().mapBy(c => {
      ...s.clusters.get(c),
      clock: s.clusters.get(c).clock + CFG.tick + adv.get(c),
    }),
  }

  action tick = {
    nondet adv = setOfMaps(CFG.clusters, Set(-CFG.drift, 0, CFG.drift)).oneOf()
    tickBy(adv)
  }

  // ---- Helpers ----

  pure def active(cl: Cluster): bool = cl.up and not(cl.left)

  // The holder's own view of its lease, spec 7.4.
  def localLive(c: str): bool = {
    val cl = s.clusters.get(c)
    active(cl) and cl.uid != 0 and cl.clock < cl.renewedAt + CFG.D - CFG.M
  }

  pure def names(ref: OptRef, claim: str): bool = match ref {
    | Held(r) => r.claim == claim
    | Free => false
  }

  pure def namesWith(ref: OptRef, claim: str, muid: int): bool = match ref {
    | Held(r) => r.claim == claim and r.muid == muid
    | Free => false
  }

  pure def refMuid(ref: OptRef): int = match ref {
    | Held(r) => r.muid
    | Free => 0
  }

  pure def refMember(ref: OptRef): str = match ref {
    | Held(r) => r.member
    | Free => ""
  }

  pure def isFree(ref: OptRef): bool = match ref {
    | Held(_) => false
    | Free => true
  }

  // Claims of cluster c that hold a device under another member UID become
  // Lost, spec 6.5. A Preempting or Preempted claim still holds its device.
  def markLost(c: str, uid: int): str -> Claim =
    s.claims.keys().mapBy(k => {
      val cm = s.claims.get(k)
      if (CFG.owner.get(k) == c
          and cm.phase.in(Set("Bound", "Suspended", "Preempting", "Preempted", "Transferring"))
          and cm.muid != uid)
        { ...cm, phase: "Lost" }
      else cm
    })

  // ---- Membership, spec 7.2, 7.3 ----

  // Join, or renew the existing Member after a controller start.
  action join(c: str): bool = {
    val cl = s.clusters.get(c)
    val mo = s.members.get(c)
    val rv = s.memberCounter + 1
    val uid = if (mo.exists) mo.uid else s.nextUid
    all {
      active(cl),
      cl.uid == 0,
      s' = {
        ...s,
        members: s.members.set(c, { exists: true, uid: uid, rv: rv }),
        memberCounter: rv,
        nextUid: if (mo.exists) s.nextUid else s.nextUid + 1,
        clusters: s.clusters.set(c, {
          ...cl,
          uid: uid,
          memberRv: rv,
          renewedAt: cl.clock,
          renewInFlight: false,
        }),
        claims: markLost(c, uid),
      },
    }
  }

  // The holder records S, the local time at which it sends the renew.
  action renewSend(c: str): bool = {
    val cl = s.clusters.get(c)
    all {
      active(cl),
      cl.uid != 0,
      not(cl.renewInFlight),
      // Before it renews, the member handles each preemption request on the
      // devices it holds, spec 10.8. The old forced-clear design skipped this.
      CFG.forceClear or requestsSeen(c),
      s' = {
        ...s,
        clusters: s.clusters.set(c, {
          ...cl,
          renewInFlight: true,
          renewSentAt: cl.clock,
          renewRv: cl.memberRv,
        }),
      },
    }
  }

  // The renew lands on condition that the Member rv is unchanged.
  action renewLand(c: str): bool = {
    val cl = s.clusters.get(c)
    val mo = s.members.get(c)
    val mine = mo.exists and mo.uid == cl.uid
    val rv = s.memberCounter + 1
    all {
      active(cl),
      cl.renewInFlight,
      if (mine and mo.rv == cl.renewRv)
        s' = {
          ...s,
          members: s.members.set(c, { ...mo, rv: rv }),
          memberCounter: rv,
          clusters: s.clusters.set(c, {
            ...cl,
            renewInFlight: false,
            memberRv: rv,
            renewedAt: cl.renewSentAt,
          }),
        }
      else
        // 404 NotFound clears the UID, so the member joins again.
        // 409 Conflict reads the rv again.
        s' = {
          ...s,
          clusters: s.clusters.set(c, {
            ...cl,
            renewInFlight: false,
            uid: if (mine) cl.uid else 0,
            memberRv: mo.rv,
          }),
        },
    }
  }

  // A crash stops the cluster. Its claims stay in its etcd, and its bind
  // writes in flight can still land.
  action crash(c: str): bool = {
    val cl = s.clusters.get(c)
    all {
      active(cl),
      s' = { ...s, clusters: s.clusters.set(c, { ...cl, up: false }) },
    }
  }

  // The controller starts again with no memory of its lease or its sweeps.
  action recover(c: str): bool = {
    val cl = s.clusters.get(c)
    all {
      not(cl.up),
      not(cl.left),
      s' = {
        ...s,
        clusters: s.clusters.set(c, {
          ...cl,
          up: true,
          uid: 0,
          renewInFlight: false,
          draining: false,
          seen: CFG.clusters.mapBy(_ => NOSEEN),
          stale: CFG.devices.mapBy(_ => { muid: -1, at: 0 }),
          sweepPhase: 0,
        }),
      },
    }
  }

  // ---- Sweeper, spec 8 ----

  // The observer records the local time at which it first saw the current
  // version of another Member, spec 8.1.
  action observe(c: str, m: str): bool = {
    val cl = s.clusters.get(c)
    val mo = s.members.get(m)
    val now = if (mo.exists) { rv: mo.rv, at: cl.clock } else NOSEEN
    val seen = afterGap(cl).set(m, now)
    all {
      active(cl),
      m != c,
      cl.seen.get(m).rv != now.rv,
      s' = {
        ...s,
        clusters: s.clusters.set(c, { ...cl, seen: seen, lastRead: cl.clock }),
      },
    }
  }

  // An observer that went longer than readGap without a read of the member
  // list forgets what it saw, and measures again from its next read. So
  // after a store outage it waits a full D from the return, spec 8.1.
  def afterGap(cl: Cluster): str -> Seen =
    if (CFG.readGap > 0 and cl.clock > cl.lastRead + CFG.readGap)
      cl.seen.keys().mapBy(_ => NOSEEN)
    else cl.seen

  // A read of the member list that works and shows no change, spec 8.1.
  action readMembers(c: str): bool = {
    val cl = s.clusters.get(c)
    all {
      active(cl),
      cl.lastRead != cl.clock,
      s' = {
        ...s,
        clusters: s.clusters.set(c, { ...cl, seen: afterGap(cl), lastRead: cl.clock }),
      },
    }
  }

  // Delete an expired Member with the observed rv, spec 8.2.
  action sweepDelete(c: str, m: str): bool = {
    val cl = s.clusters.get(c)
    val seen = cl.seen.get(m)
    val mo = s.members.get(m)
    val rv = s.memberCounter + 1
    all {
      active(cl),
      m != c,
      seen.rv >= 0,
      cl.clock >= seen.at + CFG.D,
      // The observer read the member list a short time ago, spec 8.1.
      CFG.readGap == 0 or cl.clock <= cl.lastRead + CFG.readGap,
      mo.exists,
      mo.rv == seen.rv,
      s' = {
        ...s,
        members: s.members.set(m, { exists: false, uid: mo.uid, rv: rv }),
        memberCounter: rv,
      },
    }
  }

  pure def liveUids(members: str -> MemberObj): Set[int] =
    members.keys().filter(m => members.get(m).exists).map(m => members.get(m).uid)

  // The sweeper lists devices, then members, spec 8.3. The membersFirst
  // flag swaps the order.
  action sweepListDevices(c: str): bool = {
    val cl = s.clusters.get(c)
    val from = if (CFG.membersFirst) 1 else 0
    all {
      active(cl),
      cl.sweepPhase == from,
      s' = {
        ...s,
        clusters: s.clusters.set(c, {
          ...cl,
          sweepPhase: cl.sweepPhase + 1,
          sweepDevs: s.devices,
        }),
      },
    }
  }

  action sweepListMembers(c: str): bool = {
    val cl = s.clusters.get(c)
    val from = if (CFG.membersFirst) 0 else 1
    all {
      active(cl),
      cl.sweepPhase == from,
      s' = {
        ...s,
        clusters: s.clusters.set(c, {
          ...cl,
          sweepPhase: cl.sweepPhase + 1,
          sweepUids: liveUids(s.members),
        }),
      },
    }
  }

  // Clear a claimRef whose member UID is not in the member list. The clear
  // carries the rv from the device list.
  action sweepClear(c: str, d: str): bool = {
    val cl = s.clusters.get(c)
    val listed = cl.sweepDevs.get(d)
    val rv = s.devCounter + 1
    val st = cl.stale.get(d)
    val policy = CFG.reclaim.get(d)
    all {
      active(cl),
      cl.sweepPhase == 2,
      not(isFree(listed.ref)),
      not(cl.sweepUids.contains(refMuid(listed.ref))),
      s.devices.get(d).rv == listed.rv,
      // The reclaim policy of the device, spec 8.5. Delete clears at once.
      // Delay clears R after this sweeper first found the holder gone, by
      // its own clock. Retain never lets the sweeper clear.
      policy == "Delete"
        or (policy == "Delay" and st.muid == refMuid(listed.ref) and cl.clock >= st.at + CFG.reclaimDelay),
      s' = {
        ...s,
        devices: s.devices.set(d, { ...s.devices.get(d), rv: rv, ref: Free, offer: Free }),
        devCounter: rv,
        cleared: true,
        sweptRetained: s.sweptRetained or policy == "Retain",
      },
    }
  }

  // The sweeper records when it first found the holder of a device gone,
  // for the reclaim time of Delay, spec 8.5.
  action markStale(c: str, d: str): bool = {
    val cl = s.clusters.get(c)
    val listed = cl.sweepDevs.get(d)
    all {
      active(cl),
      cl.sweepPhase == 2,
      not(isFree(listed.ref)),
      not(cl.sweepUids.contains(refMuid(listed.ref))),
      cl.stale.get(d).muid != refMuid(listed.ref),
      s' = {
        ...s,
        clusters: s.clusters.set(c, { ...cl, stale: cl.stale.set(d, { muid: refMuid(listed.ref), at: cl.clock }) }),
      },
    }
  }

  // An operator releases a retained device whose holder's member is gone,
  // spec 8.5. The release is one conditional write, as any clear is.
  action operatorRelease(d: str): bool = {
    val dv = s.devices.get(d)
    val rv = s.devCounter + 1
    all {
      CFG.reclaim.get(d) == "Retain",
      not(isFree(dv.ref)),
      not(liveUids(s.members).contains(refMuid(dv.ref))),
      s' = { ...s, devices: s.devices.set(d, { ...dv, rv: rv, ref: Free, offer: Free }), devCounter: rv, cleared: true },
    }
  }

  action sweepDone(c: str): bool = {
    val cl = s.clusters.get(c)
    all {
      active(cl),
      cl.sweepPhase == 2,
      s' = { ...s, clusters: s.clusters.set(c, { ...cl, sweepPhase: 0 }) },
    }
  }

  // ---- Bind, spec 5.3 and 6.3 ----

  pure def refClaim(ref: OptRef): str = match ref {
    | Held(r) => r.claim
    | Free => ""
  }

  // Set the device ref and give the write a new rv.
  def writeRef(d: str, ref: OptRef): State = {
    val rv = s.devCounter + 1
    val dv = s.devices.get(d)
    { ...s, devices: s.devices.set(d, { ...dv, rv: rv, ref: ref }), devCounter: rv }
  }

  // Put a bind in flight. The rv comes from a consistent read. The
  // controller adopts first, so no device may name the claim with the
  // current member UID.
  action bindSend(k: str, d: str): bool = {
    val c = CFG.owner.get(k)
    val cl = s.clusters.get(c)
    val cm = s.claims.get(k)
    val dv = s.devices.get(d)
    all {
      localLive(c),
      not(cl.draining),
      cm.exists,
      not(cm.deleting),
      cm.phase == "Pending",
      CFG.devices.forall(e => not(namesWith(s.devices.get(e).ref, k, cl.uid))),
      isFree(dv.ref),
      // A device with a preemption request is kept for the preemptor, 10.7.
      isFree(dv.preempt) or names(dv.preempt, k),
      s.inflight.size() < 3,
      s' = {
        ...s,
        inflight: s.inflight.union(Set({
          cluster: c, claim: k, dev: d, rv: dv.rv, muid: cl.uid,
        })),
      },
    }
  }

  // The bind lands at any later time. The server applies it on the rv
  // condition and rejects a change from one holder to another.
  action bindLand(msg: BindMsg): bool = {
    val dv = s.devices.get(msg.dev)
    val ref = Held({ member: msg.cluster, muid: msg.muid, claim: msg.claim, prio: CFG.priority.get(msg.claim) })
    // While a request stands, only the preemptor may bind, spec 10.7.
    val reserved = isFree(dv.preempt) or names(dv.preempt, msg.claim)
    val ok = CFG.unconditionalBind
      or (dv.rv == msg.rv and (isFree(dv.ref) or dv.ref == ref) and reserved)
    val rest = s.inflight.exclude(Set(msg))
    // A bind of a free device gets the old token plus 1, spec 5.3.
    // The server of the binding cluster issues it. Members of two releases
    // share the store, spec 11, and the token rule must hold across them.
    val raise = CFG.version.get(msg.cluster) != CFG.skipTokenVersion
    val token = if (isFree(dv.ref) and raise) dv.token + 1 else dv.token
    val written = writeRef(msg.dev, ref)
    all {
      s.inflight.contains(msg),
      if (ok)
        s' = {
          ...written,
          // The bind of the preemptor clears the request, spec 10.7.
          devices: written.devices.set(msg.dev, { ...written.devices.get(msg.dev), token: token, preempt: Free }),
          bindings: s.bindings.union(Set({ dev: msg.dev, token: token, claim: msg.claim })),
          inflight: rest,
        }
      else
        s' = { ...s, inflight: rest },
    }
  }

  // ---- Device use, spec 6.6 ----

  // A workload uses the device of a Bound claim with the claim's token. It
  // acts on the phase it last saw, even when its member is no longer live,
  // as a paused workload would. The device accepts a token equal to or
  // higher than the highest it has accepted.
  action use(k: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    val d = cm.dev
    val accept = CFG.noTokenCheck or cm.token >= s.highest.get(d)
    all {
      active(cl),
      cm.exists,
      cm.phase == "Bound",
      if (accept)
        s' = {
          ...s,
          highest: s.highest.set(d, max(s.highest.get(d), cm.token)),
          lastUse: s.lastUse.set(d, { token: cm.token, claim: k }),
          staleAccepted: s.staleAccepted or cm.token < s.lastUse.get(d).token,
        }
      else
        s' = { ...s, staleRejected: true },
    }
  }

  pure def max(a: int, b: int): int = if (a > b) a else b

  action landSome = all {
    s.inflight.size() > 0,
    nondet msg = s.inflight.oneOf()
    bindLand(msg),
  }

  // Adopt a device that already names the claim with the current member
  // UID. The adoptAnyUid flag drops the UID check.
  action adopt(k: str, d: str): bool = {
    val c = CFG.owner.get(k)
    val cl = s.clusters.get(c)
    val cm = s.claims.get(k)
    val ref = s.devices.get(d).ref
    all {
      active(cl),
      cl.uid != 0,
      not(cl.draining),
      cm.exists,
      not(cm.deleting),
      cm.phase == "Pending",
      if (CFG.adoptAnyUid) names(ref, k) else namesWith(ref, k, cl.uid),
      s' = {
        ...s,
        claims: s.claims.set(k, {
          ...cm, phase: "Bound", dev: d, muid: cl.uid, token: s.devices.get(d).token,
        }),
      },
    }
  }

  // Release a second device that names a claim. Keep the claim's device.
  action releaseDuplicate(k: str, d: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    all {
      active(cl),
      cm.phase == "Bound" or cm.phase == "Suspended",
      d != cm.dev,
      names(s.devices.get(d).ref, k),
      s' = writeRef(d, Free),
    }
  }

  // Clear a ref that names this member and a claim that no longer exists.
  // A bind can land after the claim's release removed the finalizer.
  action clearOrphan(c: str, d: str): bool = {
    val cl = s.clusters.get(c)
    val ref = s.devices.get(d).ref
    all {
      active(cl),
      refMember(ref) == c,
      not(s.claims.get(refClaim(ref)).exists),
      s' = writeRef(d, Free),
    }
  }

  // ---- Claim phases, delete, and release, spec 6.4 and 6.5 ----

  action suspend(k: str): bool = {
    val c = CFG.owner.get(k)
    val cl = s.clusters.get(c)
    val cm = s.claims.get(k)
    all {
      active(cl),
      cm.phase == "Bound" or cm.phase == "Preempting",
      not(localLive(c)) or cl.draining,
      s' = { ...s, claims: s.claims.set(k, { ...cm, phase: "Suspended" }) },
    }
  }

  action resume(k: str): bool = {
    val c = CFG.owner.get(k)
    val cl = s.clusters.get(c)
    val cm = s.claims.get(k)
    all {
      localLive(c),
      not(cl.draining),
      cm.exists,
      not(cm.deleting),
      cm.phase == "Suspended",
      cm.muid == cl.uid,
      namesWith(s.devices.get(cm.dev).ref, k, cl.uid),
      s' = { ...s, claims: s.claims.set(k, { ...cm, phase: "Bound" }) },
    }
  }

  // A user deletes the claim. The finalizer keeps it until release.
  action deleteClaim(k: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    all {
      active(cl),
      cm.exists,
      not(cm.deleting),
      s' = { ...s, claims: s.claims.set(k, { ...cm, deleting: true }) },
    }
  }

  // Clear each device that names the deleted claim, with any member UID.
  action release(k: str, d: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    all {
      active(cl),
      cm.exists,
      cm.deleting,
      names(s.devices.get(d).ref, k),
      s' = writeRef(d, Free),
    }
  }

  action removeFinalizer(k: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    all {
      active(cl),
      cm.exists,
      cm.deleting,
      CFG.devices.forall(d => not(names(s.devices.get(d).ref, k))),
      s' = { ...s, claims: s.claims.set(k, { ...cm, exists: false }) },
    }
  }

  // ---- Graceful leave, spec 7.6 ----

  action startDrain(c: str): bool = {
    val cl = s.clusters.get(c)
    all {
      active(cl),
      cl.uid != 0,
      not(cl.draining),
      s' = { ...s, clusters: s.clusters.set(c, { ...cl, draining: true }) },
    }
  }

  // A draining member releases a device only after its claim is not Bound.
  action drainRelease(k: str, d: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    all {
      active(cl),
      cl.draining,
      cm.phase != "Bound",
      names(s.devices.get(d).ref, k),
      s' = writeRef(d, Free),
    }
  }

  action finishLeave(c: str): bool = {
    val cl = s.clusters.get(c)
    val mo = s.members.get(c)
    val rv = s.memberCounter + 1
    all {
      active(cl),
      cl.draining,
      cl.uid != 0,
      CFG.devices.forall(d => not(refMember(s.devices.get(d).ref) == c
        and refMuid(s.devices.get(d).ref) == cl.uid)),
      s' = {
        ...s,
        members: s.members.set(c, { ...mo, exists: false, rv: rv }),
        memberCounter: rv,
        clusters: s.clusters.set(c, { ...cl, left: true, uid: 0 }),
        someLeft: true,
      },
    }
  }

  // ---- Preemption, spec 10 ----

  // requestsSeen is true when no Bound claim of c holds a device with a
  // request that c has not handled yet.
  def requestsSeen(c: str): bool = CFG.devices.forall(d => {
    val dv = s.devices.get(d)
    isFree(dv.ref) or isFree(dv.preempt) or refMember(dv.ref) != c
      or s.claims.get(refClaim(dv.ref)).phase != "Bound"
  })

  pure def refPrio(ref: OptRef): int = match ref {
    | Held(r) => r.prio
    | Free => -1
  }

  // A Pending claim that finds no free device requests preemption of a
  // preemptible device whose holder has a lower priority, spec 10.5. The
  // server checks the priorities, and the write is conditional, so it is
  // one step here.
  action requestPreemption(k: str, d: str): bool = {
    val c = CFG.owner.get(k)
    val cl = s.clusters.get(c)
    val cm = s.claims.get(k)
    val dv = s.devices.get(d)
    val p = CFG.priority.get(k)
    val rv = s.devCounter + 1
    all {
      localLive(c),
      not(cl.draining),
      cm.exists,
      not(cm.deleting),
      cm.phase == "Pending",
      CFG.devices.forall(e => not(isFree(s.devices.get(e).ref))),
      CFG.preemptible.get(d),
      not(isFree(dv.ref)),
      refPrio(dv.ref) < p,
      // A protected holder can only release its device, spec 10.10.
      not(CFG.protected.contains(refClaim(dv.ref))),
      refPrio(dv.preempt) < p,
      s' = {
        ...s,
        devices: s.devices.set(d, {
          ...dv, rv: rv, preempt: Held({ member: c, muid: cl.uid, claim: k, prio: p }),
        }),
        devCounter: rv,
        claims: s.claims.set(k, { ...cm, reqAt: cl.clock, reqDev: d }),
      },
    }
  }

  // The holder sees a request on its device, spec 10.6.
  action seePreemption(k: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    val dv = s.devices.get(cm.dev)
    all {
      active(cl),
      cm.phase == "Bound",
      names(dv.ref, k),
      not(isFree(dv.preempt)),
      s' = { ...s, claims: s.claims.set(k, { ...cm, phase: "Preempting", seenAt: cl.clock }) },
    }
  }

  // The grace period ends on the holder's clock.
  action markPreempted(k: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    all {
      active(cl),
      cm.phase == "Preempting",
      cl.clock >= cm.seenAt + CFG.grace,
      s' = { ...s, claims: s.claims.set(k, { ...cm, phase: "Preempted" }) },
    }
  }

  // The holder clears its ref, keeps the request, and goes back to Pending.
  action releasePreempted(k: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    val pending = { ...cm, phase: "Pending", dev: "" }
    all {
      active(cl),
      cm.phase == "Preempted",
      if (names(s.devices.get(cm.dev).ref, k))
        s' = { ...writeRef(cm.dev, Free), claims: s.claims.set(k, pending) }
      else
        s' = { ...s, claims: s.claims.set(k, pending) },
    }
  }

  // A preemptor clears a holder that did not respond, spec 10.8.
  action forceClear(k: str): bool = {
    val c = CFG.owner.get(k)
    val cl = s.clusters.get(c)
    val cm = s.claims.get(k)
    val dv = s.devices.get(cm.reqDev)
    all {
      CFG.forceClear,
      localLive(c),
      cm.exists,
      not(cm.deleting),
      cm.phase == "Pending",
      cm.reqDev != "",
      names(dv.preempt, k),
      not(isFree(dv.ref)),
      not(names(dv.ref, k)),
      CFG.noPreemptionWait or cl.clock >= cm.reqAt + CFG.grace + CFG.D,
      s' = writeRef(cm.reqDev, Free),
    }
  }

  // A preemptor withdraws a request that it no longer needs, and a
  // controller clears a request of its own member for a claim that is
  // gone, spec 10.9.
  action withdrawPreemption(c: str, d: str): bool = {
    val cl = s.clusters.get(c)
    val dv = s.devices.get(d)
    val k = refClaim(dv.preempt)
    val cm = s.claims.get(k)
    val rv = s.devCounter + 1
    all {
      active(cl),
      refMember(dv.preempt) == c,
      not(cm.exists) or cm.deleting or cm.phase != "Pending",
      s' = { ...s, devices: s.devices.set(d, { ...dv, rv: rv, preempt: Free }), devCounter: rv },
    }
  }

  // The sweeper clears a request whose member UID is gone, spec 10.9.
  action sweepClearPreempt(c: str, d: str): bool = {
    val cl = s.clusters.get(c)
    val listed = cl.sweepDevs.get(d)
    val dv = s.devices.get(d)
    val rv = s.devCounter + 1
    all {
      active(cl),
      cl.sweepPhase == 2,
      not(isFree(listed.preempt)),
      not(cl.sweepUids.contains(refMuid(listed.preempt))),
      dv.rv == listed.rv,
      s' = { ...s, devices: s.devices.set(d, { ...dv, rv: rv, preempt: Free }), devCounter: rv },
    }
  }

  // ---- Transfer, spec 14 ----

  // The holder starts a transfer. Its claim stops being in effect before
  // it offers the device, so its workload stops first, spec 14.1.
  action startTransfer(k: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    all {
      active(cl),
      cm.exists,
      not(cm.deleting),
      cm.phase == "Bound",
      s' = { ...s, claims: s.claims.set(k, { ...cm, phase: "Transferring" }) },
    }
  }

  // The holder offers its device to claim t of another cluster, with a
  // conditional write, spec 14.1. The offer names t and the member UID of
  // t's cluster.
  action offer(k: str, t: str): bool = {
    val cm = s.claims.get(k)
    val d = cm.dev
    val dv = s.devices.get(d)
    val tc = s.clusters.get(CFG.owner.get(t))
    val rv = s.devCounter + 1
    all {
      active(s.clusters.get(CFG.owner.get(k))),
      cm.phase == "Transferring",
      CFG.owner.get(t) != CFG.owner.get(k),
      tc.uid != 0,
      namesWith(dv.ref, k, cm.muid),
      isFree(dv.offer),
      isFree(dv.preempt),
      s' = {
        ...s,
        devices: s.devices.set(d, { ...dv, rv: rv,
          offer: Held({ member: CFG.owner.get(t), muid: tc.uid, claim: t, prio: CFG.priority.get(t) }) }),
        devCounter: rv,
      },
    }
  }

  // The named claim binds against the offer in one write: claimRef moves
  // to it, the token goes up, and the offer goes, spec 14.2. The device is
  // never free, so no third claim can take it. adopt then sets the claim
  // Bound, as after any bind.
  action bindOffer(t: str, d: str): bool = {
    val c = CFG.owner.get(t)
    val cl = s.clusters.get(c)
    val dv = s.devices.get(d)
    val rv = s.devCounter + 1
    val ref = Held({ member: c, muid: cl.uid, claim: t, prio: CFG.priority.get(t) })
    all {
      active(cl),
      cl.uid != 0,
      not(cl.draining),
      s.claims.get(t).exists,
      s.claims.get(t).phase == "Pending",
      namesWith(dv.offer, t, cl.uid),
      not(isFree(dv.ref)),
      s' = {
        ...s,
        devices: s.devices.set(d, { ...dv, rv: rv, ref: ref, token: dv.token + 1, offer: Free }),
        devCounter: rv,
        bindings: s.bindings.union(Set({ dev: d, token: dv.token + 1, claim: t })),
      },
    }
  }

  // The holder withdraws an offer that nobody bound, and its claim is in
  // effect again, spec 14.3. The write is conditional, so a withdraw and a
  // bind against the offer cannot both succeed.
  action withdrawOffer(k: str): bool = {
    val cm = s.claims.get(k)
    val dv = s.devices.get(cm.dev)
    val rv = s.devCounter + 1
    all {
      active(s.clusters.get(CFG.owner.get(k))),
      cm.phase == "Transferring",
      namesWith(dv.ref, k, cm.muid),
      s' = {
        ...s,
        devices: s.devices.set(cm.dev, { ...dv, rv: rv, offer: Free }),
        devCounter: rv,
        claims: s.claims.set(k, { ...cm, phase: "Bound" }),
      },
    }
  }

  // The old claim sees that its device names another claim, and lets go
  // without a write to the device, spec 14.2.
  action transferDone(k: str): bool = {
    val cm = s.claims.get(k)
    all {
      active(s.clusters.get(CFG.owner.get(k))),
      cm.phase == "Transferring",
      not(names(s.devices.get(cm.dev).ref, k)),
      s' = { ...s, claims: s.claims.set(k, { ...cm, phase: "Pending", dev: "" }) },
    }
  }

  // A sweeper clears an offer to a member UID that is not in the member
  // list, as it does for a request, spec 14.3.
  action sweepClearOffer(c: str, d: str): bool = {
    val cl = s.clusters.get(c)
    val listed = cl.sweepDevs.get(d)
    val dv = s.devices.get(d)
    val rv = s.devCounter + 1
    all {
      active(cl),
      cl.sweepPhase == 2,
      not(isFree(listed.offer)),
      not(cl.sweepUids.contains(refMuid(listed.offer))),
      dv.rv == listed.rv,
      s' = { ...s, devices: s.devices.set(d, { ...dv, rv: rv, offer: Free }), devCounter: rv },
    }
  }

  // ---- Recovery of a Lost claim, spec 14.5 ----

  // A member that joined again with a new UID takes back a device that
  // still names one of its claims under its old, gone member UID. One
  // conditional write moves claimRef to the new UID and raises the token,
  // so the old identity is fenced. The device is never free.
  action recoverWrite(k: str, d: str): bool = {
    val c = CFG.owner.get(k)
    val cl = s.clusters.get(c)
    val dv = s.devices.get(d)
    val old = refMuid(dv.ref)
    val rv = s.devCounter + 1
    all {
      active(cl),
      cl.uid != 0,
      not(cl.draining),
      localLive(c),
      s.claims.get(k).exists,
      s.claims.get(k).phase == "Lost",
      names(dv.ref, k),
      old != cl.uid,
      not(liveUids(s.members).contains(old)),
      s' = {
        ...s,
        devices: s.devices.set(d, { ...dv, rv: rv, ref: Held({ member: c, muid: cl.uid, claim: k, prio: CFG.priority.get(k) }),
          token: dv.token + 1, offer: Free }),
        devCounter: rv,
        bindings: s.bindings.union(Set({ dev: d, token: dv.token + 1, claim: k })),
      },
    }
  }

  // The claim is in effect again once its device names it with the current
  // member UID.
  action recoverDone(k: str, d: str): bool = {
    val cl = s.clusters.get(CFG.owner.get(k))
    val cm = s.claims.get(k)
    all {
      active(cl),
      cm.phase == "Lost",
      namesWith(s.devices.get(d).ref, k, cl.uid),
      s' = { ...s, claims: s.claims.set(k, { ...cm, phase: "Bound", dev: d, muid: cl.uid, token: s.devices.get(d).token }) },
    }
  }

  // ---- Invariants, spec 8.4 ----

  // A request always beats the holder of its device, spec 10.5.
  val requestBeatsHolder = CFG.devices.forall(d => {
    val dv = s.devices.get(d)
    isFree(dv.preempt) or isFree(dv.ref) or refPrio(dv.ref) < refPrio(dv.preempt)
  })

  // A claim is in effect while it is Bound and its member is live by the
  // member's own clock. Workloads use a device only then.
  def effective(k: str): bool = {
    val cm = s.claims.get(k)
    // A Preempting claim is in effect, as a Bound one is, spec 10.6.
    cm.exists and not(cm.deleting) and (cm.phase == "Bound" or cm.phase == "Preempting")
      and localLive(CFG.owner.get(k))
  }

  // No two claims in effect hold the same device.
  val noDoubleBind = CFG.claims.forall(k1 => CFG.claims.forall(k2 =>
    (k1 != k2 and effective(k1) and effective(k2))
      implies s.claims.get(k1).dev != s.claims.get(k2).dev
  ))

  // A claim in effect is named by its device.
  val claimMatchesDevice = CFG.claims.forall(k =>
    effective(k) implies names(s.devices.get(s.claims.get(k).dev).ref, k)
  )

  // A member that is live by its own clock still has its Member. So no
  // sweeper has deleted it, and no sweeper can clear its refs.
  val leaseSafety = CFG.clusters.forall(c =>
    localLive(c) implies (
      s.members.get(c).exists
        and s.members.get(c).uid == s.clusters.get(c).uid
    )
  )

  // Each fencing token of a device belongs to one bind, spec 8.4.
  val tokensUnique = s.bindings.forall(b1 => s.bindings.forall(b2 =>
    (b1.dev == b2.dev and b1.token == b2.token) implies b1.claim == b2.claim
  ))

  // A device never accepts a use from an older bind after it has accepted
  // a use from a newer bind, spec 8.4.
  val usesInOrder = not(s.staleAccepted)

  // The last accepted use of a device comes from the claim that got its
  // token in a bind.
  val useMatchesBinding = CFG.devices.forall(d => {
    val u = s.lastUse.get(d)
    u == NOUSE or s.bindings.contains({ dev: d, token: u.token, claim: u.claim })
  })

  val fencing = tokensUnique and usesInOrder and useMatchesBinding

  // The sweeper never clears a device with the Retain policy, spec 8.5.
  val retainNeverSwept = not(s.sweptRetained)

  val safety = noDoubleBind and claimMatchesDevice and leaseSafety and fencing and requestBeatsHolder
    and retainNeverSwept

  // ---- Witnesses ----
  // Each witness must be violated in a run. A violation shows that the
  // model reaches the state, so the invariants are not true by accident.

  val witnessBound = CFG.claims.forall(k => not(effective(k)))
  val witnessCleared = not(s.cleared)
  val witnessLost = CFG.claims.forall(k => s.claims.get(k).phase != "Lost")
  val witnessLeft = not(s.someLeft)
  // A retained device still names a holder whose member is gone, spec 8.5.
  val witnessRetained = not(CFG.devices.exists(d => CFG.reclaim.get(d) == "Retain"
    and not(isFree(s.devices.get(d).ref)) and not(liveUids(s.members).contains(refMuid(s.devices.get(d).ref)))))
  // A device rejects a use with a stale token. Random runs rarely reach
  // it, so fencingTest plays it step by step.
  val witnessStaleRejected = not(s.staleRejected)

  action baseStep = any {
    nondet c = CFG.clusters.oneOf()
    nondet m = CFG.clusters.oneOf()
    nondet d = CFG.devices.oneOf()
    nondet k = CFG.claims.oneOf()
    any {
      tick,
      landSome,
      bindSend(k, d),
      adopt(k, d),
      releaseDuplicate(k, d),
      clearOrphan(c, d),
      suspend(k),
      resume(k),
      deleteClaim(k),
      release(k, d),
      removeFinalizer(k),
      startDrain(c),
      drainRelease(k, d),
      finishLeave(c),
      use(k),
      requestPreemption(k, d),
      seePreemption(k),
      markPreempted(k),
      releasePreempted(k),
      forceClear(k),
      withdrawPreemption(c, d),
      sweepClearPreempt(c, d),
      join(c),
      renewSend(c),
      renewLand(c),
      crash(c),
      recover(c),
      observe(c, m),
      readMembers(c),
      sweepDelete(c, m),
      sweepListDevices(c),
      sweepListMembers(c),
      sweepClear(c, d),
      markStale(c, d),
      operatorRelease(d),
      sweepDone(c),
    },
  }

  // The transfer of spec 14. The model-based tests replay baseStep only,
  // until the server has the transfer.
  action transferStep = {
    nondet c = CFG.clusters.oneOf()
    nondet d = CFG.devices.oneOf()
    nondet k = CFG.claims.oneOf()
    nondet t = CFG.claims.oneOf()
    any {
      startTransfer(k),
      offer(k, t),
      bindOffer(k, d),
      withdrawOffer(k),
      transferDone(k),
      sweepClearOffer(c, d),
      recoverWrite(k, d),
      recoverDone(k, d),
    }
  }

  action step = any { baseStep, transferStep }

  // A step with no faults and no leave, for model-based tests. Random
  // traces of step rarely bind two claims, so they rarely reach a
  // preemption. Traces of this step do.
  action calmStep = any {
    nondet c = CFG.clusters.oneOf()
    nondet d = CFG.devices.oneOf()
    nondet k = CFG.claims.oneOf()
    any {
      tick,
      landSome,
      bindSend(k, d),
      adopt(k, d),
      use(k),
      requestPreemption(k, d),
      seePreemption(k),
      markPreempted(k),
      releasePreempted(k),
      withdrawPreemption(c, d),
      join(c),
      renewSend(c),
      renewLand(c),
    },
  }
}

// Two clusters, two devices, and three claims. The clocks start far
// apart, because solas does not depend on synchronized clocks.
module solas2 {
  import solas(CFG = {
    clusters: Set("a", "b"),
    devices: Set("d1", "d2"),
    claims: Set("a1", "a2", "b1"),
    owner: Map("a1" -> "a", "a2" -> "a", "b1" -> "b"),
    clock0: Map("a" -> 0, "b" -> 1000),
    D: 30,
    M: 6,
    tick: 10,
    drift: 1,
    unconditionalBind: false,
    membersFirst: false,
    adoptAnyUid: false,
    noTokenCheck: false,
    priority: Map("a1" -> 1, "a2" -> 0, "b1" -> 2),
    preemptible: Map("d1" -> true, "d2" -> false),
    grace: 20,
    forceClear: false,
    noPreemptionWait: false,
    version: Map("a" -> 1, "b" -> 2),
    skipTokenVersion: 0,
    readGap: 15,
    reclaim: Map("d1" -> "Delete", "d2" -> "Retain"),
    reclaimDelay: 20,
    protected: Set(),
  }).*

  pure val fastB = Map("a" -> 0, "b" -> 1)

  // a binds d1 for a1. The claim is in effect.
  run bindTest =
    init
      .then(join("a"))
      .then(bindSend("a1", "d1"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d1", rv: 0, muid: 1 }))
      .then(adopt("a1", "d1"))
      .expect(effective("a1") and safety)

  // a crashes while it holds d1. b expires a's lease, deletes its Member,
  // and clears d1. When a comes back, its claim is Lost.
  run reclaimTest =
    init
      .then(join("a"))
      .then(bindSend("a1", "d1"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d1", rv: 0, muid: 1 }))
      .then(adopt("a1", "d1"))
      .then(observe("b", "a"))
      .then(crash("a"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(sweepDelete("b", "a"))
      .then(sweepListDevices("b"))
      .then(sweepListMembers("b"))
      .then(sweepClear("b", "d1"))
      .expect(safety and s.devices.get("d1").ref == Free)
      .then(recover("a"))
      .then(join("a"))
      .expect(safety and s.claims.get("a1").phase == "Lost")

  // The steps of the adopt-any-uid negative model. Here adopt must fail,
  // because d1 names the old member UID.
  run staleAdoptBlockedTest =
    init
      .then(join("a"))
      .then(bindSend("a1", "d1"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d1", rv: 0, muid: 1 }))
      .then(observe("b", "a"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(sweepDelete("b", "a"))
      .then(renewSend("a"))
      .then(renewLand("a"))
      .then(join("a"))
      .then(adopt("a1", "d1").fail())

  // Holder a uses d1, then stops renewing while its workload still sees
  // Bound. b reclaims d1, binds it, and uses it. Then a's workload tries
  // again with its old token, and the device rejects it.
  run fencingTest =
    init
      .then(join("a"))
      .then(bindSend("a1", "d1"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d1", rv: 0, muid: 1 }))
      .then(adopt("a1", "d1"))
      .then(use("a1"))
      .then(observe("b", "a"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(sweepDelete("b", "a"))
      .then(sweepListDevices("b"))
      .then(sweepListMembers("b"))
      .then(sweepClear("b", "d1"))
      .then(join("b"))
      .then(bindSend("b1", "d1"))
      .then(bindLand({ cluster: "b", claim: "b1", dev: "d1", rv: 2, muid: 2 }))
      .then(adopt("b1", "d1"))
      .expect(s.claims.get("a1").token == 1 and s.claims.get("b1").token == 2)
      .then(use("b1"))
      // a1 is still Bound in a's etcd, so its workload tries again.
      .expect(s.claims.get("a1").phase == "Bound")
      .then(use("a1"))
      .expect(s.staleRejected and s.lastUse.get("d1").claim == "b1" and safety)

  pure val noDrift = Map("a" -> 0, "b" -> 0)

  // a2 (priority 0) holds the preemptible d1 and a1 (priority 1) holds d2.
  // b joins with b1 (priority 2), which finds no free device.
  run allHeld =
    init
      .then(join("a"))
      .then(bindSend("a2", "d1"))
      .then(bindLand({ cluster: "a", claim: "a2", dev: "d1", rv: 0, muid: 1 }))
      .then(adopt("a2", "d1"))
      .then(bindSend("a1", "d2"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d2", rv: 0, muid: 1 }))
      .then(adopt("a1", "d2"))
      .then(join("b"))

  // b1 requests d1. a2 sees the request, runs out its grace period, and
  // releases. Spec 10.5 and 10.6.
  run released =
    allHeld
      .then(requestPreemption("b1", "d1"))
      .then(seePreemption("a2"))
      .expect(s.claims.get("a2").phase == "Preempting" and effective("a2") and safety)
      .then(tickBy(noDrift))
      .then(tickBy(noDrift))
      .then(markPreempted("a2"))
      .then(releasePreempted("a2"))
      .expect(s.claims.get("a2").phase == "Pending" and s.devices.get("d1").ref == Free and safety)

  // The device is kept for b1, which binds it with token 2. Spec 10.7.
  run preemptionTest =
    released
      .then(bindSend("b1", "d1"))
      .then(bindLand({ cluster: "b", claim: "b1", dev: "d1", rv: 4, muid: 2 }))
      .then(adopt("b1", "d1"))
      .expect(s.claims.get("b1").phase == "Bound" and s.claims.get("b1").token == 2
        and s.devices.get("d1").preempt == Free and safety)

  // While the request stands, a2 cannot take d1 back. Spec 10.7.
  run reservationTest = released.then(bindSend("a2", "d1").fail())

  // d2 is not preemptible. Spec 10.5.
  run notPreemptibleTest = allHeld.then(requestPreemption("b1", "d2").fail())

  // A holder that has not handled a request on its device cannot renew,
  // spec 10.8.
  run stuckRenewTest = allHeld.then(requestPreemption("b1", "d1")).then(renewSend("a").fail())

  // a never handles the request, so its lease lapses. b's sweeper deletes
  // a's Member and frees d1, and b1 binds it. No claim in effect loses its
  // device on the way.
  run stuckHolderTest =
    allHeld
      .then(requestPreemption("b1", "d1"))
      .then(observe("b", "a"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(sweepDelete("b", "a"))
      .then(sweepListDevices("b"))
      .then(sweepListMembers("b"))
      .then(sweepClear("b", "d1"))
      .expect(not(effective("a2")) and safety)
      .then(renewSend("b"))
      .then(renewLand("b"))
      .then(bindSend("b1", "d1"))
      .then(bindLand({ cluster: "b", claim: "b1", dev: "d1", rv: 4, muid: 2 }))
      .then(adopt("b1", "d1"))
      .expect(s.claims.get("b1").phase == "Bound" and s.claims.get("b1").token == 2 and safety)

  // a2 is Preempting when a crashes. b's sweeper frees d1. When a comes
  // back, a2 is Lost, as a Bound claim would be. The simulator found this
  // case.
  run preemptingLostTest =
    allHeld
      .then(requestPreemption("b1", "d1"))
      .then(seePreemption("a2"))
      .then(observe("b", "a"))
      .then(crash("a"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(sweepDelete("b", "a"))
      .then(sweepListDevices("b"))
      .then(sweepListMembers("b"))
      .then(sweepClear("b", "d1"))
      .then(recover("a"))
      .then(join("a"))
      .expect(safety and s.claims.get("a2").phase == "Lost")

  // The store is down for longer than D: no read and no write works while
  // the clocks go on. b saw a's Member before the outage, spec 8.1.
  run outage =
    init
      .then(join("a"))
      .then(join("b"))
      .then(observe("b", "a"))
      .then(tickBy(fastB))
      .then(tickBy(fastB))
      .then(tickBy(fastB))

  // When the store returns, b cannot sweep a at once: its reads are not
  // fresh, and its first read starts the measurement again.
  run outageNoSweepTest = outage.then(sweepDelete("b", "a").fail())
  run outageFirstReadTest = outage.then(readMembers("b")).then(sweepDelete("b", "a").fail())

  // a renews after the outage and keeps its Member.
  run outageRenewTest =
    outage
      .then(readMembers("b"))
      .then(renewSend("a"))
      .then(renewLand("a"))
      .then(observe("b", "a"))
      .expect(s.members.get("a").exists and safety)

  // A member that stops for good is still swept, a full D after the store
  // returns.
  run outageStillSweptTest =
    outage
      .then(crash("a"))
      .then(readMembers("b"))
      .then(observe("b", "a"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(sweepDelete("b", "a"))
      .expect(not(s.members.get("a").exists) and safety)

  // a holds the retained d2 and crashes. b sweeps a's Member, but it cannot
  // clear d2, spec 8.5.
  run retainedHeld =
    init
      .then(join("a"))
      .then(bindSend("a1", "d2"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d2", rv: 0, muid: 1 }))
      .then(adopt("a1", "d2"))
      .then(join("b"))
      .then(observe("b", "a"))
      .then(crash("a"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(sweepDelete("b", "a"))
      .then(sweepListDevices("b"))
      .then(sweepListMembers("b"))
      .expect(not(isFree(s.devices.get("d2").ref)) and safety)

  run retainNoSweepTest = retainedHeld.then(sweepClear("b", "d2").fail())

  // An operator releases d2, and b1 can bind it.
  run operatorReleaseTest =
    retainedHeld
      .then(operatorRelease("d2"))
      .expect(isFree(s.devices.get("d2").ref) and safety)

  // a2 holds d1. a starts a transfer of d1 to b1, spec 14.
  run offered =
    init
      .then(join("a"))
      .then(bindSend("a2", "d1"))
      .then(bindLand({ cluster: "a", claim: "a2", dev: "d1", rv: 0, muid: 1 }))
      .then(adopt("a2", "d1"))
      .then(join("b"))
      .then(startTransfer("a2"))
      .expect(not(effective("a2")) and safety)
      .then(offer("a2", "b1"))
      .expect(names(s.devices.get("d1").offer, "b1") and safety)

  // b1 binds against the offer: d1 names b1 with token 2 in one write, and
  // a2 lets go without a write.
  run transferTest =
    offered
      .then(bindOffer("b1", "d1"))
      .then(adopt("b1", "d1"))
      .then(transferDone("a2"))
      .expect(s.claims.get("b1").phase == "Bound" and s.devices.get("d1").token == 2
        and s.claims.get("a2").phase == "Pending" and isFree(s.devices.get("d1").offer) and safety)

  // While the offer stands, a third claim cannot bind d1: a1 sent its bind
  // before a2 took d1, and the bind lands during the offer.
  run offerThirdClaimTest =
    init
      .then(join("a"))
      .then(bindSend("a1", "d1"))
      .then(bindSend("a2", "d1"))
      .then(bindLand({ cluster: "a", claim: "a2", dev: "d1", rv: 0, muid: 1 }))
      .then(adopt("a2", "d1"))
      .then(join("b"))
      .then(startTransfer("a2"))
      .then(offer("a2", "b1"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d1", rv: 0, muid: 1 }))
      .expect(names(s.devices.get("d1").ref, "a2") and names(s.devices.get("d1").offer, "b1") and safety)

  // a withdraws the offer: a2 is in effect again, and b1 can no longer bind.
  run withdrawn = offered.then(withdrawOffer("a2"))
    .expect(effective("a2") and isFree(s.devices.get("d1").offer) and safety)
  run withdrawTest = withdrawn.then(bindOffer("b1", "d1").fail())

  // a comes back with a new UID after b swept it, and a1 is Lost. d2 is
  // retained, so it still names a1 under a's old UID. a recovers d2: the
  // token goes up, and a1 is Bound again, spec 14.5.
  run lostWithDevice =
    retainedHeld
      .then(recover("a"))
      .then(join("a"))
      .expect(s.claims.get("a1").phase == "Lost" and safety)

  run recoveryTest =
    lostWithDevice
      .then(recoverWrite("a1", "d2"))
      .then(recoverDone("a1", "d2"))
      .expect(s.claims.get("a1").phase == "Bound" and effective("a1") and s.devices.get("d2").token == 2
        and s.claims.get("a1").token == 2 and safety)

  // A plain adopt cannot take d2, because it names the old member UID.
  run recoveryNotByAdoptTest = lostWithDevice.then(adopt("a1", "d2").fail())
}

// solas2 with d2 under the Delay policy: the sweeper clears it only R = 20
// after it first found the holder gone, by its own clock, spec 8.5.
module solas2delay {
  import solas(CFG = {
    clusters: Set("a", "b"),
    devices: Set("d1", "d2"),
    claims: Set("a1", "a2", "b1"),
    owner: Map("a1" -> "a", "a2" -> "a", "b1" -> "b"),
    clock0: Map("a" -> 0, "b" -> 1000),
    D: 30,
    M: 6,
    tick: 10,
    drift: 1,
    unconditionalBind: false,
    membersFirst: false,
    adoptAnyUid: false,
    noTokenCheck: false,
    priority: Map("a1" -> 1, "a2" -> 0, "b1" -> 2),
    preemptible: Map("d1" -> true, "d2" -> false),
    grace: 20,
    forceClear: false,
    noPreemptionWait: false,
    version: Map("a" -> 1, "b" -> 2),
    skipTokenVersion: 0,
    readGap: 15,
    reclaim: Map("d1" -> "Delete", "d2" -> "Delay"),
    reclaimDelay: 20,
    protected: Set(),
  }).*

  pure val fastB = Map("a" -> 0, "b" -> 1)

  run delayedHeld =
    init
      .then(join("a"))
      .then(bindSend("a1", "d2"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d2", rv: 0, muid: 1 }))
      .then(adopt("a1", "d2"))
      .then(join("b"))
      .then(observe("b", "a"))
      .then(crash("a"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(tickBy(fastB)).then(readMembers("b"))
      .then(sweepDelete("b", "a"))
      .then(sweepListDevices("b"))
      .then(sweepListMembers("b"))
      .then(markStale("b", "d2"))

  run delayNotYetTest = delayedHeld.then(sweepClear("b", "d2").fail())

  run delayClearTest =
    delayedHeld
      .then(tickBy(fastB))
      .then(tickBy(fastB))
      .then(sweepClear("b", "d2"))
      .expect(isFree(s.devices.get("d2").ref) and safety)
}

// solas2 with a2 protected: its device can only be released, spec 10.10.
module solas2protect {
  import solas(CFG = {
    clusters: Set("a", "b"),
    devices: Set("d1", "d2"),
    claims: Set("a1", "a2", "b1"),
    owner: Map("a1" -> "a", "a2" -> "a", "b1" -> "b"),
    clock0: Map("a" -> 0, "b" -> 1000),
    D: 30,
    M: 6,
    tick: 10,
    drift: 1,
    unconditionalBind: false,
    membersFirst: false,
    adoptAnyUid: false,
    noTokenCheck: false,
    priority: Map("a1" -> 1, "a2" -> 0, "b1" -> 2),
    preemptible: Map("d1" -> true, "d2" -> false),
    grace: 20,
    forceClear: false,
    noPreemptionWait: false,
    version: Map("a" -> 1, "b" -> 2),
    skipTokenVersion: 0,
    readGap: 15,
    reclaim: Map("d1" -> "Delete", "d2" -> "Retain"),
    reclaimDelay: 20,
    protected: Set("a2"),
  }).*

  // a2 holds the preemptible d1 and a1 holds d2. b1 has a higher priority
  // than a2, but a2 is protected, so b1 cannot ask for d1.
  run protectedHeld =
    init
      .then(join("a"))
      .then(bindSend("a2", "d1"))
      .then(bindLand({ cluster: "a", claim: "a2", dev: "d1", rv: 0, muid: 1 }))
      .then(adopt("a2", "d1"))
      .then(bindSend("a1", "d2"))
      .then(bindLand({ cluster: "a", claim: "a1", dev: "d2", rv: 0, muid: 1 }))
      .then(adopt("a1", "d2"))
      .then(join("b"))
  run protectedTest = protectedHeld.then(requestPreemption("b1", "d1").fail())
}
