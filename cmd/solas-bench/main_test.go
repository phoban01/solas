package main

import (
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	var none []time.Duration
	if got := percentile(none, 0.5); got != 0 {
		t.Errorf("percentile of nothing = %v, want 0", got)
	}
	ds := []time.Duration{5, 1, 4, 2, 3}
	if got := percentile(ds, 0.5); got != 3 {
		t.Errorf("p50 of 1..5 = %v, want 3", got)
	}
	if got := percentile(ds, 0.99); got != 5 {
		t.Errorf("p99 of 1..5 = %v, want 5", got)
	}
}

// TestWatchLagEitherOrder: the lag of a write is the time from the write
// to the event, whether the writer or the watcher records first.
func TestWatchLagEitherOrder(t *testing.T) {
	var st stats
	t0 := time.Unix(0, 0)
	st.wrote("d1/5", t0)
	st.saw("d1/5", t0.Add(30*time.Millisecond))
	st.saw("d2/7", t0.Add(50*time.Millisecond))
	st.wrote("d2/7", t0.Add(10*time.Millisecond))
	if len(st.watchLag) != 2 || st.watchLag[0] != 30*time.Millisecond || st.watchLag[1] != 40*time.Millisecond {
		t.Errorf("lags = %v, want [30ms 40ms]", st.watchLag)
	}
}

func TestOverBudget(t *testing.T) {
	for _, c := range []struct {
		used, limit int64
		want        bool
	}{{5, 10, false}, {10, 10, false}, {11, 10, true}, {1 << 40, 0, false}} {
		if got := overBudget(c.used, c.limit); got != c.want {
			t.Errorf("overBudget(%d, %d) = %v, want %v", c.used, c.limit, got, c.want)
		}
	}
}
