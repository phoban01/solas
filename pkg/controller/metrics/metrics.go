// Package metrics holds the Prometheus metrics of the controllers of a
// member, on the registry that the API server serves at /metrics: the
// renew of the member, the sweeps, the binds, the preemption requests,
// and the seal state of the store.
package metrics

import (
	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
)

// The kinds of reference the sweeper clears.
const (
	ClearedClaimRef   = "claimRef"
	ClearedPreemption = "preemption"
	ClearedOffer      = "offer"
)

var (
	// RenewSeconds is the time of each renew of the member, spec 7.3.
	RenewSeconds = metrics.NewHistogram(&metrics.HistogramOpts{
		Namespace:      "solas",
		Subsystem:      "member",
		Name:           "renew_seconds",
		Help:           "Time of each renew of this member in the shared store.",
		Buckets:        []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		StabilityLevel: metrics.ALPHA,
	})

	// RenewErrors counts the renews that failed, by cause: sealed,
	// unavailable, or other.
	RenewErrors = metrics.NewCounterVec(&metrics.CounterOpts{
		Namespace:      "solas",
		Subsystem:      "member",
		Name:           "renew_errors_total",
		Help:           "Renews of this member that failed, by cause: sealed, unavailable, or other.",
		StabilityLevel: metrics.ALPHA,
	}, []string{"cause"})

	// StoreSealed is 1 while the last renew found the store sealed, spec
	// 12.1, and 0 otherwise.
	StoreSealed = metrics.NewGauge(&metrics.GaugeOpts{
		Namespace:      "solas",
		Name:           "store_sealed",
		Help:           "1 while the last renew of this member found the shared store sealed for a move, 0 otherwise.",
		StabilityLevel: metrics.ALPHA,
	})

	// MembersObserved is the number of Members in the last list of the
	// sweeper, this member included.
	MembersObserved = metrics.NewGauge(&metrics.GaugeOpts{
		Namespace:      "solas",
		Subsystem:      "sweeper",
		Name:           "members_observed",
		Help:           "Members in the last member list of the sweeper, this member included.",
		StabilityLevel: metrics.ALPHA,
	})

	// MembersSwept counts the expired Members the sweeper deleted, spec 8.2.
	MembersSwept = metrics.NewCounter(&metrics.CounterOpts{
		Namespace:      "solas",
		Subsystem:      "sweeper",
		Name:           "members_swept_total",
		Help:           "Expired Members that the sweeper of this member deleted.",
		StabilityLevel: metrics.ALPHA,
	})

	// ReferencesCleared counts what the sweeper cleared from devices whose
	// member is gone, by kind: claimRef, preemption, or offer.
	ReferencesCleared = metrics.NewCounterVec(&metrics.CounterOpts{
		Namespace:      "solas",
		Subsystem:      "sweeper",
		Name:           "references_cleared_total",
		Help:           "References of gone members that the sweeper cleared from devices, by kind: claimRef, preemption, or offer.",
		StabilityLevel: metrics.ALPHA,
	}, []string{"kind"})

	// Binds counts the binds of this member's claims, spec 6.3.
	Binds = metrics.NewCounter(&metrics.CounterOpts{
		Namespace:      "solas",
		Subsystem:      "claims",
		Name:           "binds_total",
		Help:           "Claims of this member that went Bound.",
		StabilityLevel: metrics.ALPHA,
	})

	// PreemptionRequests counts the preemption requests this member's
	// claims wrote, spec 10.5.
	PreemptionRequests = metrics.NewCounter(&metrics.CounterOpts{
		Namespace:      "solas",
		Subsystem:      "claims",
		Name:           "preemption_requests_total",
		Help:           "Preemption requests that claims of this member wrote on devices.",
		StabilityLevel: metrics.ALPHA,
	})
)

func init() {
	legacyregistry.MustRegister(RenewSeconds, RenewErrors, StoreSealed, MembersObserved, MembersSwept, ReferencesCleared, Binds, PreemptionRequests)
}

// The fixed label values start at zero, so /metrics lists each series
// before the first event.
func init() {
	for _, cause := range []string{"sealed", "unavailable", "other"} {
		RenewErrors.WithLabelValues(cause)
	}
	for _, kind := range []string{ClearedClaimRef, ClearedPreemption, ClearedOffer} {
		ReferencesCleared.WithLabelValues(kind)
	}
}
