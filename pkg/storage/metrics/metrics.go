// Package metrics holds the Prometheus metrics of the store layer, on
// the registry that the API server serves at /metrics. The stores count
// the conflicts of their writes and lists by resource and cause, and
// measure the lag of each event that a watch delivers.
package metrics

import (
	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
)

// The causes of a conflict.
const (
	// CauseCounter is a transaction that failed on the counter condition
	// and tried again, spec 2.3: the writers of a resource raced.
	CauseCounter = "counter"
	// CauseObject is a write that failed on its object condition, spec
	// 2.5: the object changed since the writer read it.
	CauseObject = "object"
	// CauseList is a list that read two counter values around its query
	// and tried again, spec 2.4.
	CauseList = "list"
)

var (
	// Conflicts counts the conflicts of the store by resource and cause.
	Conflicts = metrics.NewCounterVec(&metrics.CounterOpts{
		Namespace:      "solas",
		Subsystem:      "storage",
		Name:           "conflicts_total",
		Help:           "Writes and lists of the shared store that hit a conflict, by resource and cause: counter, object, or list.",
		StabilityLevel: metrics.ALPHA,
	}, []string{"resource", "cause"})

	// WatchLag is the time from the write of an event to its delivery by
	// a watch of this server, by resource.
	WatchLag = metrics.NewHistogramVec(&metrics.HistogramOpts{
		Namespace:      "solas",
		Name:           "watch_lag_seconds",
		Help:           "Time from the write of an event in the shared store to its delivery by a watch of this server, by resource.",
		Buckets:        []float64{0.01, 0.025, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10},
		StabilityLevel: metrics.ALPHA,
	}, []string{"resource"})
)

func init() {
	legacyregistry.MustRegister(Conflicts, WatchLag)
}

// Prepare creates the series of a resource at zero, so /metrics lists
// them before the first conflict or delivery, and a rate over them has a
// start. Each store calls it once for its resource.
func Prepare(resource string) {
	for _, cause := range []string{CauseCounter, CauseObject, CauseList} {
		Conflicts.WithLabelValues(resource, cause)
	}
	WatchLag.WithLabelValues(resource)
}
