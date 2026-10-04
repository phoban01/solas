package claim

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/controller/member"
)

// AnnotationTransferTo asks the controller to hand the device of a Bound
// claim to another claim, spec 14. Its value is
// <member>/<namespace>/<name>/<uid> of the target claim. The UID is in the
// value because the holder cannot read a claim in another cluster.
const AnnotationTransferTo = "solas.dev/transfer-to"

// transferTarget parses the annotation of a claim. It returns nil when the
// claim has no annotation.
func transferTarget(claim *claimsv1alpha1.DeviceClaim) (*solasv1alpha1.ClaimRef, error) {
	v, ok := claim.Annotations[AnnotationTransferTo]
	if !ok {
		return nil, nil
	}
	p := strings.Split(v, "/")
	if len(p) != 4 || p[0] == "" || p[1] == "" || p[2] == "" || p[3] == "" {
		return nil, fmt.Errorf("%s %q: want member/namespace/name/uid", AnnotationTransferTo, v)
	}
	return &solasv1alpha1.ClaimRef{Member: p[0], Namespace: p[1], Name: p[2], UID: types.UID(p[3])}, nil
}

// sameTarget reports whether an offer names the target claim.
func sameTarget(offer, target *solasv1alpha1.ClaimRef) bool {
	return offer.Member == target.Member && offer.Namespace == target.Namespace &&
		offer.Name == target.Name && offer.UID == target.UID
}

// startTransfer sets a Bound claim with the annotation to Transferring.
// It reports whether it changed the claim.
func (r *Reconciler) startTransfer(ctx context.Context, claim *claimsv1alpha1.DeviceClaim, own *solasv1alpha1.Device) (bool, error) {
	target, err := transferTarget(claim)
	if err != nil {
		return true, r.setCondition(ctx, claim, "TransferValid", metav1.ConditionFalse, "Invalid", err.Error())
	}
	if target == nil || own == nil || own.Status.Preemption != nil || claim.Status.Phase != claimsv1alpha1.ClaimBound {
		return false, nil
	}
	//= spec/solas.md#14-1-offer
	//# Before it offers a device, the holder's controller MUST set its claim to
	//# `Transferring`.
	setPhase(claim, claimsv1alpha1.ClaimTransferring)
	return true, ignoreConflict(r.Client.Status().Update(ctx, claim))
}

// transferStep moves a Transferring claim on: it offers the device, sees
// the bind of the target, or withdraws the offer when the annotation is
// gone.
func (r *Reconciler) transferStep(ctx context.Context, claim *claimsv1alpha1.DeviceClaim,
	own *solasv1alpha1.Device, st member.Status) (reconcile.Result, error) {
	//= spec/solas.md#14-2-bind-against-the-offer
	//# When the old claim's controller sees that its device names another
	//# claim, it MUST stop treating the device as its own, and MUST NOT write to
	//# the device.
	if own == nil {
		log.FromContext(ctx).Info("device moved to another claim", "device", claim.Status.DeviceName)
		if _, ok := claim.Annotations[AnnotationTransferTo]; ok {
			delete(claim.Annotations, AnnotationTransferTo)
			return reconcile.Result{Requeue: true}, ignoreConflict(r.Client.Update(ctx, claim))
		}
		setPhase(claim, claimsv1alpha1.ClaimPending)
		claim.Status.DeviceName, claim.Status.FencingToken = "", 0
		return reconcile.Result{Requeue: true}, ignoreConflict(r.Client.Status().Update(ctx, claim))
	}
	target, err := transferTarget(claim)
	if err != nil {
		target = nil
	}
	offer := own.Status.Offer
	switch {
	//= spec/solas.md#14-3-withdraw
	//# The holder MAY withdraw an offer that nobody bound, with a status update
	//# that clears `status.offer`, and set its claim back to `Bound`.
	case target == nil && offer != nil, target != nil && offer != nil && !sameTarget(offer, target):
		d := own.DeepCopy()
		d.Status.Offer = nil
		if err := r.Client.Status().Update(ctx, d); err != nil {
			return reconcile.Result{Requeue: true}, ignoreConflict(ignoreInvalid(err))
		}
		return reconcile.Result{Requeue: true}, nil
	case target == nil:
		if !st.Live || st.Draining {
			setPhase(claim, claimsv1alpha1.ClaimSuspended)
		} else {
			claim.Status.Phase = claimsv1alpha1.ClaimBound
		}
		return reconcile.Result{Requeue: true}, ignoreConflict(r.Client.Status().Update(ctx, claim))
	case offer != nil:
		// The offer stands; wait for the target to bind.
		return reconcile.Result{RequeueAfter: r.Resync}, nil
	case own.Status.Preemption != nil:
		// A request came before the offer. The claim is not in effect, so
		// let the preemptor have the device.
		setPhase(claim, claimsv1alpha1.ClaimPreempted)
		return reconcile.Result{Requeue: true}, ignoreConflict(r.Client.Status().Update(ctx, claim))
	case !st.Live || st.Draining:
		return reconcile.Result{RequeueAfter: r.Resync}, nil
	}

	var m solasv1alpha1.Member
	if err := r.Reader.Get(ctx, client.ObjectKey{Name: target.Member}, &m); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{RequeueAfter: r.Resync},
				r.setCondition(ctx, claim, "TransferValid", metav1.ConditionFalse, "NoMember", "member "+target.Member+" is not in the mesh")
		}
		return reconcile.Result{}, err
	}
	//= spec/solas.md#14-1-offer
	//# The offer MUST be a status update of the device that sets
	//# `status.offer` to the named claim, its member, and its member UID.

	//= spec/solas.md#14-1-offer
	//# The offer MUST carry the resource version that the controller read.
	target.MemberUID = m.UID
	d := own.DeepCopy()
	d.Status.Offer = target
	if err := r.Client.Status().Update(ctx, d); err != nil {
		return reconcile.Result{Requeue: true}, ignoreConflict(ignoreInvalid(err))
	}
	log.FromContext(ctx).Info("offered", "device", d.Name, "to", claimKey(target))
	return reconcile.Result{RequeueAfter: r.Resync}, nil
}

// offerFor reports whether the offer of d names this claim of this member.
func offerFor(d *solasv1alpha1.Device, clusterID string, uid types.UID, claim *claimsv1alpha1.DeviceClaim) bool {
	o := d.Status.Offer
	return o != nil && o.Member == clusterID && o.MemberUID == uid && o.Namespace == claim.Namespace &&
		o.Name == claim.Name && o.UID == claim.UID
}

func claimKey(ref *solasv1alpha1.ClaimRef) string {
	return ref.Member + "/" + ref.Namespace + "/" + ref.Name
}

func ignoreInvalid(err error) error {
	if apierrors.IsInvalid(err) {
		return nil
	}
	return err
}
