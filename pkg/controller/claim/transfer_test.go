package claim

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/phoban01/solas/internal/fakekube"
	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/controller/member"
)

const muidB = types.UID("member-uid-b")

// transferWorld has claim job on cluster-a, which holds d1, and a claim
// job on cluster-b that names d1. Both clusters share one fake client;
// their claims are in different namespaces.
func transferWorld(t *testing.T) (client.Client, *Reconciler, *Reconciler) {
	t.Helper()
	held := claim("job", "uid-a")
	held.Finalizers = []string{claimsv1alpha1.ReleaseFinalizer}
	held.Status = claimsv1alpha1.DeviceClaimStatus{Phase: claimsv1alpha1.ClaimBound, DeviceName: "d1", MemberUID: muid, FencingToken: 1}
	target := claim("job", "uid-b")
	target.Namespace = "nsb"
	target.Spec.DeviceName = "d1"
	// d2 is free but does not match the old claim, so after the move the
	// old claim stays Pending.
	free := device("d2", map[string]string{"kind": "cpu"}, nil)
	held.Spec.Selector = &claimsv1alpha1.DeviceSelector{LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"kind": "gpu"}}}
	k := fakekube.NewClient(device("d1", map[string]string{"kind": "gpu"}, refTo(held, muid)), free, held, target,
		&solasv1alpha1.Member{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", UID: muid}},
		&solasv1alpha1.Member{ObjectMeta: metav1.ObjectMeta{Name: "cluster-b", UID: muidB}})
	a := newReconciler(k, live())
	b := &Reconciler{Client: k, Reader: k, ClusterID: "cluster-b", Member: &fixedMember{member.Status{UID: muidB, Live: true}}, Resync: 10 * time.Second}
	return k, a, b
}

func annotate(t *testing.T, k client.Client, value string) {
	t.Helper()
	c := getClaim(t, k, "job")
	if value == "" {
		delete(c.Annotations, AnnotationTransferTo)
	} else {
		c.Annotations = map[string]string{AnnotationTransferTo: value}
	}
	if err := k.Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func getClaimIn(t *testing.T, k client.Client, ns, name string) *claimsv1alpha1.DeviceClaim {
	t.Helper()
	var dc claimsv1alpha1.DeviceClaim
	if err := k.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &dc); err != nil {
		t.Fatal(err)
	}
	return &dc
}

//= spec/solas.md#14-1-offer
//= type=test
//# Before it offers a device, the holder's controller MUST set its claim to
//# `Transferring`.

//= spec/solas.md#14-2-bind-against-the-offer
//= type=test
//# When the old claim's controller sees that its device names another
//# claim, it MUST stop treating the device as its own, and MUST NOT write to
//# the device.

//= spec/solas.md#14-4-pre-bound-claims
//= type=test
//# A claim that names a device MUST bind only that device: against an offer
//# that names the claim, or as a normal bind when the device is free.

// TestTransfer moves d1 from cluster-a to the pre-bound claim of
// cluster-b.
func TestTransfer(t *testing.T) {
	k, a, b := transferWorld(t)
	target := getClaimIn(t, k, "nsb", "job")

	// The target names d1, which is held, so it waits and does not take d2.
	settle(t, b, target)
	if got := getClaimIn(t, k, "nsb", "job"); got.Status.Phase != claimsv1alpha1.ClaimPending {
		t.Fatalf("target before the offer: phase %s, want Pending", got.Status.Phase)
	}
	if getDevice(t, k, "d2").Status.ClaimRef != nil {
		t.Fatal("a claim that names d1 bound d2")
	}

	annotate(t, k, "cluster-b/nsb/job/uid-b")
	settle(t, a, getClaim(t, k, "job"))
	if got := getClaim(t, k, "job"); got.Status.Phase != claimsv1alpha1.ClaimTransferring {
		t.Fatalf("holder after the annotation: phase %s, want Transferring", got.Status.Phase)
	}
	d := getDevice(t, k, "d1")
	if o := d.Status.Offer; o == nil || o.Member != "cluster-b" || o.MemberUID != muidB || o.UID != "uid-b" {
		t.Fatalf("offer = %+v, want claim uid-b of cluster-b", o)
	}
	rv := d.ResourceVersion
	settle(t, a, getClaim(t, k, "job"))
	if getDevice(t, k, "d1").ResourceVersion != rv {
		t.Error("the holder wrote the device again while the offer stands")
	}

	settle(t, b, target)
	got := getClaimIn(t, k, "nsb", "job")
	d = getDevice(t, k, "d1")
	if got.Status.Phase != claimsv1alpha1.ClaimBound || got.Status.FencingToken != 2 ||
		d.Status.ClaimRef.UID != "uid-b" || d.Status.Offer != nil {
		t.Fatalf("after the bind: claim %+v, device %+v", got.Status, d.Status)
	}

	rv = d.ResourceVersion
	settle(t, a, getClaim(t, k, "job"))
	old := getClaim(t, k, "job")
	if old.Status.Phase != claimsv1alpha1.ClaimPending || old.Annotations[AnnotationTransferTo] != "" {
		t.Errorf("old claim after the move: phase %s, annotations %v; want Pending, none", old.Status.Phase, old.Annotations)
	}
	if d := getDevice(t, k, "d1"); d.ResourceVersion != rv || d.Status.ClaimRef.UID != "uid-b" {
		t.Errorf("the old holder wrote d1 after the move: %+v", d.Status)
	}
}

//= spec/solas.md#14-3-withdraw
//= type=test
//# The holder MAY withdraw an offer that nobody bound, with a status update
//# that clears `status.offer`, and set its claim back to `Bound`.

// TestTransferWithdraw removes the annotation before the target binds.
func TestTransferWithdraw(t *testing.T) {
	k, a, b := transferWorld(t)
	annotate(t, k, "cluster-b/nsb/job/uid-b")
	settle(t, a, getClaim(t, k, "job"))
	if getDevice(t, k, "d1").Status.Offer == nil {
		t.Fatal("no offer")
	}
	annotate(t, k, "")
	settle(t, a, getClaim(t, k, "job"))
	if got := getClaim(t, k, "job"); got.Status.Phase != claimsv1alpha1.ClaimBound {
		t.Errorf("holder after the withdraw: phase %s, want Bound", got.Status.Phase)
	}
	d := getDevice(t, k, "d1")
	if d.Status.Offer != nil || d.Status.ClaimRef.UID != "uid-a" || d.Status.FencingToken != 1 {
		t.Fatalf("device after the withdraw: %+v", d.Status)
	}
	settle(t, b, getClaimIn(t, k, "nsb", "job"))
	if got := getClaimIn(t, k, "nsb", "job"); got.Status.Phase != claimsv1alpha1.ClaimPending {
		t.Errorf("target after the withdraw: phase %s, want Pending", got.Status.Phase)
	}
}
