package claim

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
)

// errRetry asks the caller to list again; controller-runtime retries.
var errRetry = errors.New("device changed; list again")

// Orphans clears refs to claims of this cluster that no longer exist.
type Orphans struct {
	Reconciler *Reconciler
	Interval   time.Duration
}

// Start runs Sweep every Interval until ctx ends.
func (o *Orphans) Start(ctx context.Context) error {
	t := time.NewTicker(o.Interval)
	defer t.Stop()
	for {
		if err := o.Sweep(ctx); err != nil && !errors.Is(err, errRetry) {
			log.FromContext(ctx).Error(err, "orphan sweep failed")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

//= spec/solas.md#6-4-release
//# The controller MUST clear a `claimRef` that names its own member and a
//# claim UID that does not exist in its cluster.

// Sweep clears each device that names this member and a claim that is gone.
// A bind can land after a release has removed the finalizer.
func (o *Orphans) Sweep(ctx context.Context) error {
	r := o.Reconciler
	devices, err := r.listDevices(ctx)
	if err != nil {
		return err
	}
	for i := range devices {
		req := devices[i].Status.Preemption
		if req == nil || req.Claim.Member != r.ClusterID {
			continue
		}
		exists, err := o.claimExists(ctx, req.Claim.Namespace, req.Claim.Name, req.Claim.UID)
		if err != nil {
			return err
		}
		if !exists {
			d := devices[i].DeepCopy()
			d.Status.Preemption = nil
			if err := r.Client.Status().Update(ctx, d); err != nil && !apierrors.IsConflict(err) {
				return client.IgnoreNotFound(err)
			}
			// The ref loop below lists again, so it sees the new version.
			if devices, err = r.listDevices(ctx); err != nil {
				return err
			}
		}
	}
	for i := range devices {
		ref := devices[i].Status.ClaimRef
		if ref == nil || ref.Member != r.ClusterID {
			continue
		}
		exists, err := o.claimExists(ctx, ref.Namespace, ref.Name, ref.UID)
		if err != nil || exists {
			if err != nil {
				return err
			}
			continue
		}
		log.FromContext(ctx).Info("clearing an orphan ref", "device", devices[i].Name, "claim", ref.Namespace+"/"+ref.Name)
		//= spec/solas.md#6-4-release
		//# The clear MUST be a status update with the resource version that the
		//# controller read.
		if err := r.clear(ctx, &devices[i]); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orphans) claimExists(ctx context.Context, ns, name string, uid types.UID) (bool, error) {
	var claim claimsv1alpha1.DeviceClaim
	err := o.Reconciler.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &claim)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return claim.UID == uid, nil
}

// Drained reports whether no device names the member UID. The member
// manager calls it before it deletes its Member on leave.
func (r *Reconciler) Drained(ctx context.Context, uid types.UID) (bool, error) {
	devices, err := r.listDevices(ctx)
	if err != nil {
		return false, err
	}
	for i := range devices {
		if ref := devices[i].Status.ClaimRef; ref != nil && ref.MemberUID == uid {
			return false, nil
		}
	}
	return true, nil
}

//= spec/solas.md#7-2-join
//# This MUST include `Preempting` and `Preempted` claims, because they still
//# hold a device.

// MarkLost sets each claim of this cluster that holds a device under a
// member UID other than uid to Lost. The member manager calls it after a
// join, before it counts itself live, spec 7.2.
func (r *Reconciler) MarkLost(ctx context.Context, uid types.UID) error {
	var claims claimsv1alpha1.DeviceClaimList
	if err := r.Client.List(ctx, &claims); err != nil {
		return err
	}
	for i := range claims.Items {
		c := &claims.Items[i]
		var held bool
		switch c.Status.Phase {
		case claimsv1alpha1.ClaimBound, claimsv1alpha1.ClaimSuspended,
			claimsv1alpha1.ClaimPreempting, claimsv1alpha1.ClaimPreempted:
			held = true
		}
		if !held || c.Status.MemberUID == uid {
			continue
		}
		setPhase(c, claimsv1alpha1.ClaimLost)
		r.event(c, nil, true, ReasonLost, "lost device %s with fencing token %d: the member joined again with a new UID", c.Status.DeviceName, c.Status.FencingToken)
		if err := r.Client.Status().Update(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
