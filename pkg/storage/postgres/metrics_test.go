package postgres

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/component-base/metrics/testutil"

	storemetrics "github.com/phoban01/solas/pkg/storage/metrics"
)

// TestMetrics checks that a delivered event observes its lag, and that an
// object conflict counts under its cause. The watch comes first: the raw
// writes of the conflict are not objects a watch can decode.
func TestMetrics(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	resource := s.groupResource.String()
	countBefore, err := lagCount(resource)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Watch(ctx, "/pods/", storage.ListOptions{ResourceVersion: "1", Recursive: true, Predicate: storage.Everything})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	createPods(t, s, "b")
	for e := range w.ResultChan() {
		if e.Type == watch.Added {
			break
		}
	}
	countAfter, err := lagCount(resource)
	if err != nil {
		t.Fatal(err)
	}
	if countAfter < countBefore+1 {
		t.Errorf("solas_watch_lag_seconds{resource=%q} count = %d, want at least %d", resource, countAfter, countBefore+1)
	}
	conflicts := storemetrics.Conflicts.WithLabelValues(resource, storemetrics.CauseObject)
	before, err := testutil.GetCounterMetricValue(conflicts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.commit(ctx, write{key: "/a", action: actCreate, value: []byte("1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.commit(ctx, write{key: "/a", action: actUpdate, expectRV: 7, value: []byte("x"), prev: []byte("1")}); !errors.Is(err, errObjectConflict) {
		t.Fatalf("stale update = %v, want errObjectConflict", err)
	}
	after, err := testutil.GetCounterMetricValue(conflicts)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("solas_storage_conflicts_total{resource=%q,cause=object} = %v, want %v", resource, after, before+1)
	}

}

// lagCount returns the sample count of solas_watch_lag_seconds for a
// resource, from the registry that /metrics serves.
func lagCount(resource string) (uint64, error) {
	fams, err := legacyregistry.DefaultGatherer.Gather()
	if err != nil {
		return 0, err
	}
	for _, f := range fams {
		if f.GetName() != "solas_watch_lag_seconds" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "resource" && l.GetValue() == resource {
					return m.GetHistogram().GetSampleCount(), nil
				}
			}
		}
	}
	return 0, nil
}
