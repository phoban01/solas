// Package sweeper expires the Members of other clusters and frees the
// devices they held, spec section 8.
package sweeper

import (
	"context"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

// observation is what this cluster saw of another Member, spec 8.1.
type observation struct {
	rv  string
	uid types.UID
	// at is the local time this cluster first saw rv.
	at time.Time
}

// Sweeper runs in every member cluster.
type Sweeper struct {
	// Client writes. Reader reads with no cache.
	Client    client.Client
	Reader    client.Reader
	ClusterID string
	Clock     clock.PassiveClock
	Interval  time.Duration
	// ReadGap is the read gap G of spec 8.1. Zero means twice Interval.
	ReadGap time.Duration

	mu   sync.Mutex
	seen map[string]observation
	// stale holds, for each device under the Delay policy, the member UID
	// of the gone holder and the local time of the first sweep that found
	// it gone, spec 8.5.
	stale map[string]observation
	// lastRead is the local time of the last member list that worked.
	lastRead time.Time
}

//= spec/solas.md#8-1-expiry-on-the-observer
//# Each member cluster MUST run a sweeper.

// Start runs the sweeper every Interval until ctx ends.
func (s *Sweeper) Start(ctx context.Context) error {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.Run(ctx); err != nil {
			log.FromContext(ctx).Error(err, "sweep failed")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Run does one sweep: expire members, then clear stale refs.
func (s *Sweeper) Run(ctx context.Context) error {
	if err := s.expire(ctx); err != nil {
		return err
	}
	return s.clearStale(ctx)
}

// expire observes the Members and deletes the ones that expired.
func (s *Sweeper) expire(ctx context.Context) error {
	var members solasv1alpha1.MemberList
	if err := s.Reader.List(ctx, &members); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	//= spec/solas.md#8-1-expiry-on-the-observer
	//# An observer MUST count only time during which it reads the member list.

	//= spec/solas.md#8-1-expiry-on-the-observer
	//# When the observer has not read the member list for longer than the read
	//# gap `G`, it MUST forget the local times that it recorded, and record
	//# them again from its next read.
	//
	// The list above worked. After a gap, as in a store outage, the
	// observations start again, so no member counts as expired before it
	// had D to renew. The deletes below run right after this read, so they
	// keep the rule that the last read is at most G old.
	now := s.Clock.Now()
	if !s.lastRead.IsZero() && now.Sub(s.lastRead) > s.readGap() {
		s.seen = nil
	}
	s.lastRead = now
	if s.seen == nil {
		s.seen = map[string]observation{}
	}
	listed := map[string]bool{}
	for i := range members.Items {
		m := &members.Items[i]
		listed[m.Name] = true
		//= spec/solas.md#8-2-delete-an-expired-member
		//# The sweeper MUST NOT delete its own `Member`.
		if m.Name == s.ClusterID {
			continue
		}
		//= spec/solas.md#8-1-expiry-on-the-observer
		//# An observer MUST measure the lease of another member with its own clock.

		//= spec/solas.md#8-1-expiry-on-the-observer
		//# The observer MUST record the local time at which it first saw the
		//# current resource version of each `Member`.

		//= spec/solas.md#8-1-expiry-on-the-observer
		//# The observer MUST NOT compare `renewTime` with its own clock.
		ob, ok := s.seen[m.Name]
		if !ok || ob.rv != m.ResourceVersion || ob.uid != m.UID {
			s.seen[m.Name] = observation{rv: m.ResourceVersion, uid: m.UID, at: s.Clock.Now()}
			continue
		}
		//= spec/solas.md#8-1-expiry-on-the-observer
		//# The observer MUST treat the `Member` as expired from local time `T + D`.
		d := time.Duration(m.Spec.LeaseDurationSeconds) * time.Second
		if s.Clock.Since(ob.at) < d {
			continue
		}
		//= spec/solas.md#8-2-delete-an-expired-member
		//# The sweeper MUST delete an expired `Member` with the resource version
		//# that it observed.
		err := s.Client.Delete(ctx, m, client.Preconditions{UID: &ob.uid, ResourceVersion: &ob.rv})
		switch {
		case err == nil:
			log.FromContext(ctx).Info("deleted an expired member", "member", m.Name, "uid", m.UID)
			delete(s.seen, m.Name)
		case apierrors.IsConflict(err), apierrors.IsNotFound(err):
			// The member renewed, or another sweeper deleted it first.
			delete(s.seen, m.Name)
		default:
			return err
		}
	}
	for name := range s.seen {
		if !listed[name] {
			delete(s.seen, name)
		}
	}
	return nil
}

// clearStale clears each claimRef whose member UID has no Member.
func (s *Sweeper) clearStale(ctx context.Context) error {
	//= spec/solas.md#8-3-clear-stale-claim-references
	//# The sweeper MUST do a consistent list of devices first.
	var devices solasv1alpha1.DeviceList
	if err := s.Reader.List(ctx, &devices); err != nil {
		return err
	}
	//= spec/solas.md#8-3-clear-stale-claim-references
	//# The sweeper MUST then do a consistent list of members.
	var members solasv1alpha1.MemberList
	if err := s.Reader.List(ctx, &members); err != nil {
		return err
	}
	live := map[types.UID]bool{}
	for i := range members.Items {
		live[members.Items[i].UID] = true
	}
	//= spec/solas.md#10-9-stale-requests
	//# The sweeper MUST clear a request whose member UID is not in the member
	//# list, as it does for a `claimRef`, spec 8.3.
	for i := range devices.Items {
		d := &devices.Items[i]
		//= spec/solas.md#14-3-withdraw
		//# The sweeper MUST clear an offer whose member UID is not in the member
		//# list, as it does for a preemption request, spec 10.9.
		staleReq := d.Status.Preemption != nil && !live[d.Status.Preemption.Claim.MemberUID]
		staleOffer := d.Status.Offer != nil && !live[d.Status.Offer.MemberUID]
		if staleReq || staleOffer {
			cleared := d.DeepCopy()
			if staleReq {
				cleared.Status.Preemption = nil
			}
			if staleOffer {
				cleared.Status.Offer = nil
			}
			if err := s.Client.Status().Update(ctx, cleared); err == nil {
				*d = *cleared
			} else if !apierrors.IsConflict(err) && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	for i := range devices.Items {
		d := &devices.Items[i]
		ref := d.Status.ClaimRef
		if ref == nil || live[ref.MemberUID] || !s.mayReclaim(d, ref.MemberUID) {
			continue
		}
		//= spec/solas.md#8-3-clear-stale-claim-references
		//# For each device whose `claimRef` names a member UID that is not in the
		//# member list, the sweeper MUST clear `claimRef`.

		//= spec/solas.md#8-3-clear-stale-claim-references
		//# The clear MUST be a status update with the resource version from the
		//# device list.
		cleared := d.DeepCopy()
		cleared.Status.ClaimRef = nil
		err := s.Client.Status().Update(ctx, cleared)
		switch {
		case err == nil:
			log.FromContext(ctx).Info("freed a device of a gone member", "device", d.Name, "member", ref.Member)
			s.mu.Lock()
			delete(s.stale, d.Name)
			s.mu.Unlock()
		case apierrors.IsConflict(err), apierrors.IsNotFound(err):
			//= spec/solas.md#8-3-clear-stale-claim-references
			//# If the clear fails with `409 Conflict`, the sweeper MUST skip the device
			//# until its next run.
		default:
			return err
		}
	}
	return nil
}

//= spec/solas.md#8-1-expiry-on-the-observer
//# The default `G` is twice the sweep interval.

func (s *Sweeper) readGap() time.Duration {
	if s.ReadGap > 0 {
		return s.ReadGap
	}
	return 2 * s.Interval
}

//= spec/solas.md#8-5-reclaim-policy
//# Under `Delete`, the sweeper MUST clear the `claimRef` as section 8.3
//# describes.

//= spec/solas.md#8-5-reclaim-policy
//# Under `Delay`, the sweeper MUST NOT clear the `claimRef` until the
//# reclaim time `R` of the device has passed.

//= spec/solas.md#8-5-reclaim-policy
//# The sweeper MUST measure `R` on its own clock, from its first sweep that
//# found the holder gone.

//= spec/solas.md#8-5-reclaim-policy
//# Under `Retain`, the sweeper MUST NOT clear the `claimRef`.

// mayReclaim reports whether the reclaim policy of d lets the sweeper
// clear its claimRef now. The holder, uid, is gone.
func (s *Sweeper) mayReclaim(d *solasv1alpha1.Device, uid types.UID) bool {
	switch d.Spec.ReclaimPolicy {
	case solasv1alpha1.ReclaimRetain:
		return false
	case solasv1alpha1.ReclaimDelay:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.stale == nil {
			s.stale = map[string]observation{}
		}
		now := s.Clock.Now()
		ob, ok := s.stale[d.Name]
		if !ok || ob.uid != uid {
			ob = observation{uid: uid, at: now}
			s.stale[d.Name] = ob
		}
		var r time.Duration
		if d.Spec.ReclaimDelaySeconds != nil {
			r = time.Duration(*d.Spec.ReclaimDelaySeconds) * time.Second
		}
		return now.Sub(ob.at) >= r
	}
	return true
}
