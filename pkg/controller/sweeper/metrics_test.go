package sweeper

import (
	"context"
	"testing"
	"time"

	"k8s.io/component-base/metrics/testutil"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/phoban01/solas/internal/fakekube"
	ctrlmetrics "github.com/phoban01/solas/pkg/controller/metrics"
)

// TestMetrics checks that a sweep counts the members it observed and the
// one it deleted, and the claimRef it cleared.
func TestMetrics(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient(member("a", "ua"), member("gone", "ug"), device("d1", ref("gone", "ug")))
	clk := clocktesting.NewFakePassiveClock(time.Unix(1000, 0))
	s := newSweeper(c, clk)
	sweptBefore, _ := testutil.GetCounterMetricValue(ctrlmetrics.MembersSwept)
	clearedBefore, _ := testutil.GetCounterMetricValue(ctrlmetrics.ReferencesCleared.WithLabelValues(ctrlmetrics.ClearedClaimRef))

	if err := s.Run(ctx); err != nil { // observe
		t.Fatal(err)
	}
	if v, err := testutil.GetGaugeMetricValue(ctrlmetrics.MembersObserved); err != nil || v != 2 {
		t.Errorf("solas_sweeper_members_observed = %v, %v; want 2", v, err)
	}
	advance(t, s, clk, 31*time.Second) // within the read gap each step
	if err := s.Run(ctx); err != nil { // the expired member is gone, and its reference
		t.Fatal(err)
	}
	if exists(t, c, "gone") {
		t.Fatal("the expired member is still there")
	}
	if v, _ := testutil.GetCounterMetricValue(ctrlmetrics.MembersSwept); v != sweptBefore+1 {
		t.Errorf("solas_sweeper_members_swept_total = %v, want %v", v, sweptBefore+1)
	}
	if v, _ := testutil.GetCounterMetricValue(ctrlmetrics.ReferencesCleared.WithLabelValues(ctrlmetrics.ClearedClaimRef)); v != clearedBefore+1 {
		t.Errorf("solas_sweeper_references_cleared_total{kind=claimRef} = %v, want %v", v, clearedBefore+1)
	}
}
