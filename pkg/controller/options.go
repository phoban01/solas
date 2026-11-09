// Package controller builds the controller part of solas: the member
// manager, the claim reconciler, and the sweeper, spec sections 6 to 8.
package controller

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Options holds the settings of the controller part.
type Options struct {
	// ClusterID names this member cluster. It is the name of its Member.
	ClusterID string
	// LeaseDuration is the lease duration D, spec 7.1.
	LeaseDuration time.Duration
	// LeaseMargin is the safety margin M, spec 7.4.
	LeaseMargin time.Duration
	// SweepInterval is how often the sweeper runs. It must not exceed D.
	SweepInterval time.Duration
	// ClaimWorkers is how many claims the controller reconciles at once.
	// The store carries the writes of a server that arrive together in one
	// transaction, spec 2.3, so several workers share a round trip.
	ClaimWorkers int

	MetricsAddr      string
	ProbeAddr        string
	LeaderElection   bool
	LeaderElectionNS string
}

// NewOptions returns the defaults: D is 30s and M is D / 10, spec 7.5.
func NewOptions() *Options {
	return &Options{
		LeaseDuration:    30 * time.Second,
		LeaseMargin:      3 * time.Second,
		SweepInterval:    10 * time.Second,
		ClaimWorkers:     4,
		MetricsAddr:      ":8080",
		ProbeAddr:        ":8081",
		LeaderElection:   true,
		LeaderElectionNS: "solas-system",
	}
}

// AddFlags adds the flags of the options.
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.ClusterID, "cluster-id", o.ClusterID, "ID of this member cluster, and the name of its Member.")
	fs.DurationVar(&o.LeaseDuration, "lease-duration", o.LeaseDuration, "Lease duration D.")
	fs.DurationVar(&o.LeaseMargin, "lease-margin", o.LeaseMargin, "Safety margin M. It must be at least 2 * rho * D.")
	fs.DurationVar(&o.SweepInterval, "sweep-interval", o.SweepInterval, "How often the sweeper runs. At most D.")
	fs.IntVar(&o.ClaimWorkers, "claim-workers", o.ClaimWorkers, "How many claims the controller reconciles at once. Their writes share a transaction, spec 2.3.")
	fs.StringVar(&o.MetricsAddr, "metrics-bind-address", o.MetricsAddr, "Address of the metrics endpoint.")
	fs.StringVar(&o.ProbeAddr, "health-probe-bind-address", o.ProbeAddr, "Address of the health probes.")
	fs.BoolVar(&o.LeaderElection, "leader-elect", o.LeaderElection, "Use a leader election lease in this cluster.")
	fs.StringVar(&o.LeaderElectionNS, "leader-election-namespace", o.LeaderElectionNS, "Namespace of the leader election lease.")
}

// rho is the bound on clock rate drift that the design assumes, spec 7.5.
const rho = 0.01

// Validate checks the options.
func (o *Options) Validate() error {
	var errs []error
	//= spec/solas.md#7-1-resource
	//# The name of a `Member` MUST be the cluster ID of its member cluster.
	for _, msg := range validation.IsDNS1123Subdomain(o.ClusterID) {
		errs = append(errs, fmt.Errorf("--cluster-id: %s", msg))
	}
	if o.ClaimWorkers < 1 {
		errs = append(errs, errors.New("--claim-workers must be at least 1"))
	}
	if o.LeaseDuration <= 0 {
		errs = append(errs, errors.New("--lease-duration must be above 0"))
	}
	//= spec/solas.md#7-5-clock-drift
	//# The margin `M` MUST be at least `2 * rho * D`.
	if float64(o.LeaseMargin) < 2*rho*float64(o.LeaseDuration) || o.LeaseMargin >= o.LeaseDuration {
		errs = append(errs, fmt.Errorf("--lease-margin must be at least %v and below --lease-duration",
			time.Duration(2*rho*float64(o.LeaseDuration))))
	}
	//= spec/solas.md#8-3-clear-stale-claim-references
	//# The sweeper MUST run at least once every `D`.
	if o.SweepInterval <= 0 || o.SweepInterval > o.LeaseDuration {
		errs = append(errs, errors.New("--sweep-interval must be above 0 and at most --lease-duration"))
	}
	return errors.Join(errs...)
}
