package member

import (
	"context"
	"testing"
	"time"

	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/component-base/metrics/testutil"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/phoban01/solas/internal/fakekube"
	ctrlmetrics "github.com/phoban01/solas/pkg/controller/metrics"
)

// TestMetrics checks that a renew observes its time and leaves the seal
// gauge at 0.
func TestMetrics(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient()
	clk := clocktesting.NewFakePassiveClock(time.Unix(1000, 0))
	m := newManager(c, clk)
	before, err := renewCount()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(ctx); err != nil { // join
		t.Fatal(err)
	}
	clk.SetTime(clk.Now().Add(27 * time.Second))
	if err := m.Tick(ctx); err != nil { // renew
		t.Fatal(err)
	}
	after, err := renewCount()
	if err != nil {
		t.Fatal(err)
	}
	if after < before+1 {
		t.Errorf("solas_member_renew_seconds count = %d, want at least %d", after, before+1)
	}
	if v, err := testutil.GetGaugeMetricValue(ctrlmetrics.StoreSealed); err != nil || v != 0 {
		t.Errorf("solas_store_sealed = %v, %v; want 0", v, err)
	}
}

// renewCount returns the sample count of solas_member_renew_seconds from
// the registry that /metrics serves.
func renewCount() (uint64, error) {
	fams, err := legacyregistry.DefaultGatherer.Gather()
	if err != nil {
		return 0, err
	}
	for _, f := range fams {
		if f.GetName() == "solas_member_renew_seconds" && len(f.GetMetric()) > 0 {
			return f.GetMetric()[0].GetHistogram().GetSampleCount(), nil
		}
	}
	return 0, nil
}
