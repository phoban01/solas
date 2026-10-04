package sim

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

// seeds returns the seeds of this shard: SIM_SEEDS seeds from SIM_START,
// taking every SIM_SHARDS-th seed from SIM_SHARD.
func seeds(t *testing.T) []uint64 {
	t.Helper()
	get := func(name string, def int) int {
		if v := os.Getenv(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return n
		}
		return def
	}
	// A plain go test runs 20 seeds; the gate sets SIM_SEEDS.
	count, start := get("SIM_SEEDS", 20), get("SIM_START", 1)
	shards, shard := get("SIM_SHARDS", 1), get("SIM_SHARD", 0)
	var out []uint64
	for s := start; s < start+count; s++ {
		if (s-start)%shards == shard {
			out = append(out, uint64(s))
		}
	}
	return out
}

func run(t *testing.T, seed uint64, cfg Config) error {
	t.Helper()
	ctx := context.Background()
	w, err := NewWorld(ctx, seed, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w.Run(ctx)
}

//= spec/solas.md#8-4-safety
//= type=test
//# A sweeper deletes a `Member` only after the holder treats itself as not
//# live.

// TestSim runs the gate: every seed must keep every safety property.
func TestSim(t *testing.T) {
	for _, seed := range seeds(t) {
		if err := run(t, seed, DefaultConfig()); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSimSeed replays one seed, from SIM_SEED.
func TestSimSeed(t *testing.T) {
	v := os.Getenv("SIM_SEED")
	if v == "" {
		t.Skip("set SIM_SEED to replay one seed")
	}
	seed, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t, seed, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
}

// TestSimBrokenStore checks that the gate can fail: with the Device status
// rules of spec 5.3 off, some seed must break a property.
func TestSimBrokenStore(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Unconditional = true
	for seed := uint64(1); seed <= 200; seed++ {
		if err := run(t, seed, cfg); err != nil {
			t.Logf("found as expected: %s", strings.SplitN(err.Error(), "\n", 2)[0])
			return
		}
	}
	t.Fatal("the simulator found no violation with a broken store")
}

// TestSimDeterministic checks that a seed gives the same run twice.
func TestSimDeterministic(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultConfig()
	cfg.Steps = 500
	traces := make([][]string, 2)
	for i := range traces {
		w, err := NewWorld(ctx, 42, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Run(ctx); err != nil {
			t.Fatal(err)
		}
		traces[i] = w.Trace()
	}
	if strings.Join(traces[0], "\n") != strings.Join(traces[1], "\n") {
		for i := range traces[0] {
			if i >= len(traces[1]) || traces[0][i] != traces[1][i] {
				t.Fatalf("runs differ at line %d:\n  %s\n  %s", i, traces[0][i], traces[1][min(i, len(traces[1])-1)])
			}
		}
		t.Fatal("runs differ in length")
	}
}

// TestSimReachesPreemption is a witness: some seed must take a device from
// a holder through the grace period, or the gate would not test it.
func TestSimReachesPreemption(t *testing.T) {
	ctx := context.Background()
	for seed := uint64(1); seed <= 100; seed++ {
		w, err := NewWorld(ctx, seed, DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if w.sawPreempted {
			t.Logf("seed %d preempted a claim", seed)
			return
		}
	}
	t.Fatal("no seed preempted a claim")
}

// TestSimFormatUpgrade upgrades the format of the mesh, spec 11. Two
// clusters run release 1. One moves to release 2, and finalize is refused.
// The second moves, and finalize passes. The first rolls back to release 1
// and refuses to start. The checks of invariants.go hold after every step.
func TestSimFormatUpgrade(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultConfig()
	cfg.Clusters, cfg.NoLeave = 2, true
	w, err := NewWorld(ctx, 7, cfg)
	if err != nil {
		t.Fatal(err)
	}
	steps := func(n int) {
		t.Helper()
		for range n {
			w.step++
			w.Step(ctx)
			if err := w.check(ctx); err != nil {
				t.Fatal(w.fail(err))
			}
		}
	}
	// settle wakes every running cluster and lets its member renew, so its
	// Member shows the range of its release.
	settle := func() {
		t.Helper()
		for _, c := range w.clusters {
			if c.down {
				continue
			}
			c.paused = false
			for range 3 {
				w.log("%s member tick: %s", c.name, errText(c.members.Tick(ctx)))
			}
			if err := w.check(ctx); err != nil {
				t.Fatal(w.fail(err))
			}
		}
	}
	c0, c1 := w.clusters[0], w.clusters[1]

	steps(300)
	w.setRelease(c0, 2)
	steps(100)
	settle()
	if err := w.finalize(ctx, 2); err == nil || !strings.Contains(err.Error(), "c1 (highest format 1)") {
		t.Fatalf("finalize with c1 on release 1: err = %v, want a refusal that names c1", err)
	}
	w.setRelease(c1, 2)
	steps(100)
	settle()
	if err := w.finalize(ctx, 2); err != nil {
		t.Fatalf("finalize with both clusters on release 2: %v", err)
	}
	w.setRelease(c0, 1)
	if !c0.down {
		t.Fatal("c0 started on release 1 after the store was finalized at 2")
	}
	steps(300)
	if c0.members.Status().Live {
		t.Error("c0 is live on a release that refused to start")
	}
}
