package claim

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	"github.com/phoban01/solas/pkg/controller/member"
)

//= spec/solas.md#14-5-recovery-of-a-lost-claim
//# A member that joined again with a new UID MAY recover a device whose
//# `claimRef` names one of its `Lost` claims under an old member UID,
//# ADR 0020.

// recover takes back the device of a Lost claim that still names the
// claim under the old member UID of the claim. The server accepts the
// write only when no Member has that UID.
func (r *Reconciler) recover(ctx context.Context, claim *claimsv1alpha1.DeviceClaim, st member.Status) (reconcile.Result, error) {
	if st.UID == "" || !st.Live || st.Draining || claim.Status.MemberUID == "" || claim.Status.MemberUID == st.UID {
		return reconcile.Result{RequeueAfter: r.Resync}, nil
	}
	devices, err := r.listDevices(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}
	for i := range devices {
		ref := devices[i].Status.ClaimRef
		if ref == nil || ref.Member != r.ClusterID || ref.UID != claim.UID || ref.MemberUID != claim.Status.MemberUID {
			continue
		}
		//= spec/solas.md#14-5-recovery-of-a-lost-claim
		//# A recovery MUST be one status update that sets the `claimRef` to the
		//# member's current UID and raises `status.fencingToken`, spec 5.3 and 13.2.
		d := devices[i].DeepCopy()
		d.Status.ClaimRef.MemberUID = st.UID
		if err := r.Client.Status().Update(ctx, d); err != nil {
			if apierrors.IsConflict(err) || apierrors.IsInvalid(err) {
				return reconcile.Result{RequeueAfter: r.Resync}, nil
			}
			return reconcile.Result{}, err
		}
		log.FromContext(ctx).Info("recovered", "device", d.Name, "token", d.Status.FencingToken)
		//= spec/solas.md#14-5-recovery-of-a-lost-claim
		//# After a recovery, the controller MUST set the claim back to `Bound`.
		return r.setBound(ctx, claim, d, st.UID)
	}
	return reconcile.Result{RequeueAfter: r.Resync}, nil
}
