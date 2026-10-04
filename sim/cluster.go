package sim

import (
	"context"
	"math/rand/v2"
	"sort"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	"github.com/phoban01/solas/pkg/controller/claim"
	"github.com/phoban01/solas/pkg/controller/member"
	"github.com/phoban01/solas/pkg/controller/sweeper"
)

// Settings are the lease settings of each simulated cluster.
type Settings struct {
	Lease  time.Duration
	Margin time.Duration
	Sweep  time.Duration
	// ReadGap is the read gap G of the sweepers, spec 8.1. Zero means the
	// default.
	ReadGap time.Duration
}

// cluster is one member cluster. Its controllers are the real ones.
type cluster struct {
	name     string
	client   *router
	clock    *clocktesting.FakePassiveClock
	settings Settings
	rng      *rand.Rand

	members *member.Manager
	claims  *claim.Reconciler
	orphans *claim.Orphans
	sweeper *sweeper.Sweeper

	// paused stops every step of the cluster while its clock runs, as a
	// paused node or a long GC pause would.
	paused bool
	// maxFormat is the highest format of the release that the cluster
	// runs, spec 11. down is true when that release refused to start.
	maxFormat int32
	down      bool
}

func newCluster(name string, shared client.Client, start time.Time, s Settings, rng *rand.Rand, lag float64) *cluster {
	c := &cluster{
		name:      name,
		client:    newRouter(shared, rand.New(rand.NewPCG(rng.Uint64(), rng.Uint64())), lag),
		clock:     clocktesting.NewFakePassiveClock(start),
		settings:  s,
		rng:       rng,
		maxFormat: 1,
	}
	c.build()
	return c
}

// build makes new controllers. A restart calls it again, so the new
// controllers remember nothing.
func (c *cluster) build() {
	c.members = &member.Manager{
		Client: c.client, Reader: c.client, ClusterID: c.name,
		Lease: c.settings.Lease, Margin: c.settings.Margin, Clock: c.clock,
		MaxFormat: c.maxFormat,
	}
	c.claims = &claim.Reconciler{
		Client: c.client, Reader: c.client, ClusterID: c.name,
		Member: c.members, Resync: c.settings.Lease / 3,
		Rand: rand.New(rand.NewPCG(c.rng.Uint64(), c.rng.Uint64())),
	}
	c.members.Drained = c.claims.Drained
	c.members.MarkLost = c.claims.MarkLost
	c.members.BeforeRenew = c.claims.HandlePreemptions
	c.claims.Clock = c.clock
	c.orphans = &claim.Orphans{Reconciler: c.claims}
	c.sweeper = &sweeper.Sweeper{
		Client: c.client, Reader: c.client, ClusterID: c.name,
		Clock: c.clock, Interval: c.settings.Sweep, ReadGap: c.settings.ReadGap,
	}
}

func (c *cluster) restart() { c.build() }

// claimKeys lists the claims of this cluster in name order.
func (c *cluster) claimKeys(ctx context.Context) []client.ObjectKey {
	var list claimsv1alpha1.DeviceClaimList
	if err := c.client.List(ctx, &list); err != nil {
		return nil
	}
	keys := make([]client.ObjectKey, 0, len(list.Items))
	for i := range list.Items {
		keys = append(keys, client.ObjectKeyFromObject(&list.Items[i]))
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

// reconcile runs the claim reconciler once for key.
func (c *cluster) reconcile(ctx context.Context, key client.ObjectKey) error {
	_, err := c.claims.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	return err
}
