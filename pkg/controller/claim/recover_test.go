package claim

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/phoban01/solas/internal/fakekube"
	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

//= spec/solas.md#14-5-recovery-of-a-lost-claim
//= type=test
//# A recovery MUST be one status update that sets the `claimRef` to the
//# member's current UID and raises `status.fencingToken`, spec 5.3 and 13.2.

//= spec/solas.md#14-5-recovery-of-a-lost-claim
//= type=test
//# The server MUST reject a recovery unless the member name and the claim
//# UID are the same, and no `Member` has the old member UID.

//= spec/solas.md#14-5-recovery-of-a-lost-claim
//= type=test
//# After a recovery, the controller MUST set the claim back to `Bound`.

// TestRecoverAfterRejoin: cluster-a joined again with a new UID. Its Lost
// claim gets back the retained device that names the old UID, with a new
// token. While a Member still has the old UID, the server refuses.
func TestRecoverAfterRejoin(t *testing.T) {
	const newUID = types.UID("member-uid-2")
	for _, c := range []struct {
		name      string
		memberUID types.UID
		recovered bool
	}{
		{"old UID gone", newUID, true},
		{"old UID still a Member", muid, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			lost := claim("c1", "u1")
			lost.Finalizers = []string{claimsv1alpha1.ReleaseFinalizer}
			lost.Status = claimsv1alpha1.DeviceClaimStatus{Phase: claimsv1alpha1.ClaimLost, DeviceName: "d1", MemberUID: muid, FencingToken: 1}
			retained := device("d1", nil, refTo(lost, muid))
			retained.Spec.ReclaimPolicy = solasv1alpha1.ReclaimRetain
			k := fakekube.NewClient(retained, lost,
				&solasv1alpha1.Member{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", UID: c.memberUID}})
			m := live()
			m.st.UID = newUID
			settle(t, newReconciler(k, m), lost)

			got, d := getClaim(t, k, "c1"), getDevice(t, k, "d1")
			if !c.recovered {
				if got.Status.Phase != claimsv1alpha1.ClaimLost || d.Status.ClaimRef.MemberUID != muid || d.Status.FencingToken != 1 {
					t.Errorf("claim %+v, device %+v; want Lost and the device unchanged", got.Status, d.Status)
				}
				return
			}
			if got.Status.Phase != claimsv1alpha1.ClaimBound || got.Status.MemberUID != newUID || got.Status.FencingToken != 2 {
				t.Errorf("claim after the recovery: %+v; want Bound, the new UID, token 2", got.Status)
			}
			if d.Status.ClaimRef.MemberUID != newUID || d.Status.FencingToken != 2 {
				t.Errorf("device after the recovery: %+v; want the new UID and token 2", d.Status)
			}
		})
	}
}
