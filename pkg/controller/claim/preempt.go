package claim

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/controller/member"
)

func (r *Reconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

// requestFor reports whether req is a request of the claim.
func requestFor(req *solasv1alpha1.PreemptionRequest, claim *claimsv1alpha1.DeviceClaim) bool {
	return req != nil && req.Claim.UID == claim.UID
}

// keptFor reports whether d may be bound by the claim: it has no request,
// or the request is the claim's own, spec 10.7.
func keptFor(d *solasv1alpha1.Device, claim *claimsv1alpha1.DeviceClaim) bool {
	return d.Status.Preemption == nil || requestFor(d.Status.Preemption, claim)
}

// requestPreemption asks for a preemptible device that matches the claim
// and that a lower priority holds, spec 10.5. It is called when no free
// device matches.
func (r *Reconciler) requestPreemption(ctx context.Context, claim *claimsv1alpha1.DeviceClaim, st member.Status,
	devices []solasv1alpha1.Device, sel *matcher) (reconcile.Result, error) {
	prio := claim.Spec.Priority
	if t := claim.Status.PreemptionTarget; t != "" {
		for i := range devices {
			if devices[i].Name == t && requestFor(devices[i].Status.Preemption, claim) {
				// The request stands; wait for the holder.
				return reconcile.Result{RequeueAfter: r.Resync}, nil
			}
		}
	}
	//= spec/solas.md#10-5-preemption-request
	//# A `Pending` claim MAY request preemption of a device only when:
	//# the device matches the claim, the device is preemptible, the device is
	//# bound, and the holder has a lower priority than the claim.
	var best *solasv1alpha1.Device
	for i := range devices {
		d := &devices[i]
		ref := d.Status.ClaimRef
		if ref == nil || !d.Spec.Preemptible || !d.DeletionTimestamp.IsZero() || !sel.Matches(d) {
			continue
		}
		//= spec/solas.md#10-10-protected-holders
		//# A protected holder can only release its device.
		if ref.Priority >= prio || ref.Protected {
			continue
		}
		if p := d.Status.Preemption; p != nil && p.Claim.Priority >= prio {
			continue
		}
		if best == nil || ref.Priority < best.Status.ClaimRef.Priority {
			best = d
		}
	}
	if best == nil {
		return reconcile.Result{RequeueAfter: r.Resync}, nil
	}
	d := best.DeepCopy()
	at := metav1.NewTime(r.now())
	//= spec/solas.md#10-5-preemption-request
	//# A request MUST be a status update of the device that sets
	//# `status.preemption`.

	//= spec/solas.md#10-5-preemption-request
	//# The request MUST name the requesting claim as a `claimRef` does, with its
	//# priority.

	//= spec/solas.md#10-5-preemption-request
	//# The request MUST carry the resource version that the controller read.
	d.Status.Preemption = &solasv1alpha1.PreemptionRequest{
		Claim: solasv1alpha1.ClaimRef{
			Member: r.ClusterID, MemberUID: st.UID, Namespace: claim.Namespace,
			Name: claim.Name, UID: claim.UID, Priority: prio,
		},
		RequestedAt: &at,
	}
	if err := r.Client.Status().Update(ctx, d); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsInvalid(err) {
			return reconcile.Result{Requeue: true}, nil
		}
		return reconcile.Result{}, err
	}
	log.FromContext(ctx).Info("requested preemption", "device", d.Name,
		"holder", d.Status.ClaimRef.Namespace+"/"+d.Status.ClaimRef.Name, "priority", prio)
	claim.Status.PreemptionTarget = d.Name
	return reconcile.Result{RequeueAfter: r.Resync}, ignoreConflict(r.Client.Status().Update(ctx, claim))
}

// preemptionStep moves a held claim through Preempting and Preempted,
// spec 10.6. It reports whether it changed the claim.
func (r *Reconciler) preemptionStep(ctx context.Context, claim *claimsv1alpha1.DeviceClaim,
	own *solasv1alpha1.Device) (bool, error) {
	switch claim.Status.Phase {
	case claimsv1alpha1.ClaimBound:
		if own == nil || own.Status.Preemption == nil {
			return false, nil
		}
		//= spec/solas.md#10-6-release-by-the-holder
		//# When the holder's controller sees a request on its device, it MUST set
		//# its claim to `Preempting`.

		//= spec/solas.md#10-6-release-by-the-holder
		//# The controller MUST record the local time at which it first saw the
		//# request.
		seen := metav1.NewTime(r.now())
		claim.Status.Phase = claimsv1alpha1.ClaimPreempting
		claim.Status.PreemptionSeenAt = &seen
		return true, ignoreConflict(r.Client.Status().Update(ctx, claim))

	case claimsv1alpha1.ClaimPreempting:
		if own == nil || own.Status.Preemption == nil {
			// The request is gone; keep the device.
			claim.Status.Phase = claimsv1alpha1.ClaimBound
			claim.Status.PreemptionSeenAt = nil
			return true, ignoreConflict(r.Client.Status().Update(ctx, claim))
		}
		grace := 30 * time.Second
		if g := own.Spec.PreemptionGracePeriodSeconds; g != nil {
			grace = time.Duration(*g) * time.Second
		}
		seen := claim.Status.PreemptionSeenAt
		//= spec/solas.md#10-6-release-by-the-holder
		//# When the grace period has passed since that time, by the holder's clock,
		//# the controller MUST set its claim to `Preempted`.
		if seen == nil || r.now().Before(seen.Add(grace)) {
			return false, nil
		}
		claim.Status.Phase = claimsv1alpha1.ClaimPreempted
		return true, ignoreConflict(r.Client.Status().Update(ctx, claim))

	case claimsv1alpha1.ClaimPreempted:
		if own != nil {
			//= spec/solas.md#10-6-release-by-the-holder
			//# After the claim is `Preempted`, the controller MUST clear the `claimRef`
			//# of the device with a status update, and keep the request.
			if err := r.clear(ctx, own); err != nil {
				return true, err
			}
		}
		//= spec/solas.md#10-6-release-by-the-holder
		//# The controller MUST then set the claim back to `Pending`.
		setPhase(claim, claimsv1alpha1.ClaimPending)
		claim.Status.DeviceName = ""
		claim.Status.PreemptionSeenAt = nil
		meta.SetStatusCondition(&claim.Status.Conditions, metav1.Condition{
			Type: "Preempted", Status: metav1.ConditionTrue, Reason: "HigherPriority",
			Message: "a claim of higher priority took the device", ObservedGeneration: claim.Generation,
		})
		return true, ignoreConflict(r.Client.Status().Update(ctx, claim))
	}
	return false, nil
}

// withdraw clears the requests of the claim on every device, when the
// claim no longer needs them, spec 10.9.
func (r *Reconciler) withdraw(ctx context.Context, claim *claimsv1alpha1.DeviceClaim,
	devices []solasv1alpha1.Device) error {
	for i := range devices {
		if requestFor(devices[i].Status.Preemption, claim) {
			d := devices[i].DeepCopy()
			d.Status.Preemption = nil
			if err := r.Client.Status().Update(ctx, d); err != nil {
				if apierrors.IsConflict(err) {
					return errRetry
				}
				return err
			}
		}
	}
	return nil
}

// HandlePreemptions sets each Bound claim of this member whose device has a
// request to Preempting. The member manager calls it before each renew,
// spec 10.8.
func (r *Reconciler) HandlePreemptions(ctx context.Context, uid types.UID) error {
	var claims claimsv1alpha1.DeviceClaimList
	if err := r.Client.List(ctx, &claims); err != nil {
		return err
	}
	var devices []solasv1alpha1.Device
	for i := range claims.Items {
		c := &claims.Items[i]
		if c.Status.Phase != claimsv1alpha1.ClaimBound || c.Status.MemberUID != uid {
			continue
		}
		if devices == nil {
			var err error
			if devices, err = r.listDevices(ctx); err != nil {
				return err
			}
		}
		for j := range devices {
			d := &devices[j]
			if d.Name == c.Status.DeviceName && d.Status.ClaimRef != nil && d.Status.ClaimRef.UID == c.UID {
				if _, err := r.preemptionStep(ctx, c, d); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
