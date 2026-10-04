// Model of per-member limits, ADR 0021 and spec 15. Two members bind and
// release devices. A bind is two writes in one request of the member's
// server: first it adds the device to the usage set of the member, on
// condition that the set is below the limit; then it writes the device.
// The second write does not read the usage set. A request has a deadline,
// so the device write lands only before TIMEOUT. The member's server
// removes an entry whose device the member does not hold after GRACE. The
// limit holds when GRACE is longer than TIMEOUT.
module limits {
  // The negative models: no reservation, or a cleanup that is too early.
  const useReservation: bool
  const GRACE: int
  const TIMEOUT: int

  val MEMBERS = Set("a", "b")
  val DEVICES = Set("d1", "d2", "d3")
  pure val NONE = ""
  // The limit of each member: a may hold 1 device, b 2.
  pure val LIMIT = Map("a" -> 1, "b" -> 2)

  type Flight = { m: str, d: str, at: int }

  // holder of each device; usage of each member, device -> time added;
  // the device writes in flight; the time of the member servers.
  var holder: str -> str
  var usage: str -> (str -> int)
  var flight: Set[Flight]
  var now: int

  action init = all {
    holder' = DEVICES.mapBy(_ => NONE),
    usage' = MEMBERS.mapBy(_ => Map()),
    flight' = Set(),
    now' = 0,
  }

  def held(m: str): Set[str] = DEVICES.filter(d => holder.get(d) == m)
  def drop(u: str -> int, d: str): str -> int = u.keys().exclude(Set(d)).mapBy(x => u.get(x))

  // The server reserves d for m on condition that the usage set of m is
  // below its limit, then sends the device write, spec 15.2.
  action reserve(m: str, d: str): bool = all {
    holder.get(d) == NONE,
    not(usage.get(m).keys().contains(d)),
    usage.get(m).keys().size() < LIMIT.get(m),
    usage' = usage.set(m, usage.get(m).put(d, now)),
    flight' = flight.union(Set({ m: m, d: d, at: now })),
    holder' = holder,
    now' = now,
  }

  // The negative model no-reservation sends the device write with no entry.
  action sendOnly(m: str, d: str): bool = all {
    not(useReservation),
    flight' = flight.union(Set({ m: m, d: d, at: now })),
    holder' = holder,
    usage' = usage,
    now' = now,
  }

  // The device write lands, before the deadline of its request. It is a
  // conditional write on the device, and it does not read the usage set.
  action land(f: Flight): bool = all {
    flight.contains(f),
    now < f.at + TIMEOUT,
    flight' = flight.exclude(Set(f)),
    holder' = if (holder.get(f.d) == NONE) holder.set(f.d, f.m) else holder,
    usage' = usage,
    now' = now,
  }

  // A write past its deadline fails.
  action expire(f: Flight): bool = all {
    flight.contains(f),
    now >= f.at + TIMEOUT,
    flight' = flight.exclude(Set(f)),
    holder' = holder,
    usage' = usage,
    now' = now,
  }

  // A release clears the device, then removes the entry.
  action release(d: str): bool = all {
    holder.get(d) != NONE,
    holder' = holder.set(d, NONE),
    usage' = usage,
    flight' = flight,
    now' = now,
  }

  // The member's server removes an entry whose device the member does not
  // hold, GRACE after it was added, spec 15.3. A release also ends here.
  action cleanup(m: str, d: str): bool = all {
    usage.get(m).keys().contains(d),
    holder.get(d) != m,
    now >= usage.get(m).get(d) + GRACE,
    usage' = usage.set(m, drop(usage.get(m), d)),
    holder' = holder,
    flight' = flight,
    now' = now,
  }

  action tick = all { now < 8, now' = now + 1, holder' = holder, usage' = usage, flight' = flight }

  action step = {
    nondet m = MEMBERS.oneOf()
    nondet d = DEVICES.oneOf()
    any {
      reserve(m, d), sendOnly(m, d), release(d), cleanup(m, d), tick,
      nondet f = flight.oneOf() any { land(f), expire(f) },
    }
  }

  // No member holds more devices than its limit, spec 15.1.
  val withinLimit = MEMBERS.forall(m => held(m).size() <= LIMIT.get(m))

  // Witness: b holds two devices.
  val witnessFull = held("b").size() < 2

  run limitTest =
    init
      .then(reserve("a", "d1"))
      .then(land({ m: "a", d: "d1", at: 0 }))
      .then(reserve("a", "d2").fail())
}

module limits2 {
  import limits(useReservation = true, GRACE = 3, TIMEOUT = 2).*
}
