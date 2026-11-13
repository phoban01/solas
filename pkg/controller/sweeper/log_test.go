package sweeper

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/phoban01/solas/internal/fakekube"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

//= spec/solas.md#8-2-delete-an-expired-member
//= type=test
//# The sweeper MUST log each `Member` that it deletes, with its name and
//# UID.

//= spec/solas.md#8-3-clear-stale-claim-references
//= type=test
//# The sweeper MUST log each `claimRef` that it clears, with the device, the
//# member UID, and the fencing token.

// TestSweepLog runs a sweep that deletes an expired member and clears its
// claimRef, its preemption request, and its offer, and checks one
// structured line for each, with the fields an audit reads.
func TestSweepLog(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logger := funcr.NewJSON(func(obj string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, obj)
	}, funcr.Options{})
	ctx := log.IntoContext(context.Background(), logger)

	gone := ref("gone", "ug")
	held := device("d1", gone)
	held.Status.FencingToken = 7
	asked := device("d2", ref("a", "ua"))
	asked.Status.Preemption = &solasv1alpha1.PreemptionRequest{Claim: *gone}
	offered := device("d3", ref("a", "ua"))
	offered.Status.Offer = gone
	c := fakekube.NewClient(member("a", "ua"), member("gone", "ug"), held, asked, offered)
	clk := clocktesting.NewFakePassiveClock(time.Unix(1000, 0))
	s := newSweeper(c, clk)
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	for d := time.Duration(0); d < 31*time.Second; d += s.Interval {
		clk.SetTime(clk.Now().Add(s.Interval))
		if err := s.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, want := range [][]string{
		{`"event":"member-deleted"`, `"member":"gone"`, `"memberUID":"ug"`},
		{`"event":"claimref-cleared"`, `"device":"d1"`, `"memberUID":"ug"`, `"fencingToken":7`},
		{`"event":"preemption-cleared"`, `"device":"d2"`, `"memberUID":"ug"`},
		{`"event":"offer-cleared"`, `"device":"d3"`, `"memberUID":"ug"`},
	} {
		if !hasLine(lines, want) {
			t.Errorf("no log line with %v in:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

func hasLine(lines, fields []string) bool {
	for _, l := range lines {
		ok := true
		for _, f := range fields {
			ok = ok && strings.Contains(l, f)
		}
		if ok {
			return true
		}
	}
	return false
}
