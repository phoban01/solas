package claim

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/phoban01/solas/internal/fakekube"
	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/controller/member"
)

const muid = types.UID("member-uid-1")

type fixedMember struct{ st member.Status }

func (f *fixedMember) Status() member.Status { return f.st }

func live() *fixedMember { return &fixedMember{member.Status{UID: muid, Live: true}} }

func device(name string, lbls map[string]string, ref *solasv1alpha1.ClaimRef) *solasv1alpha1.Device {
	d := &solasv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls}}
	d.Status.ClaimRef = ref
	if ref != nil {
		d.Status.FencingToken = 1
	}
	return d
}

func claim(name string, uid types.UID) *claimsv1alpha1.DeviceClaim {
	return &claimsv1alpha1.DeviceClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, UID: uid}}
}

func refTo(c *claimsv1alpha1.DeviceClaim, memberUID types.UID) *solasv1alpha1.ClaimRef {
	return &solasv1alpha1.ClaimRef{Member: "cluster-a", MemberUID: memberUID, Namespace: c.Namespace, Name: c.Name, UID: c.UID}
}

func newReconciler(c client.Client, m MemberState) *Reconciler {
	return &Reconciler{Client: c, Reader: c, ClusterID: "cluster-a", Member: m, Resync: 10 * time.Second}
}

// settle reconciles the claim until it asks for no immediate requeue.
func settle(t *testing.T, r *Reconciler, c *claimsv1alpha1.DeviceClaim) {
	t.Helper()
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(c)}
	for range 10 {
		res, err := r.Reconcile(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Requeue {
			return
		}
	}
	t.Fatal("claim did not settle after 10 reconciles")
}

func getClaim(t *testing.T, c client.Client, name string) *claimsv1alpha1.DeviceClaim {
	t.Helper()
	var dc claimsv1alpha1.DeviceClaim
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: name}, &dc); err != nil {
		t.Fatal(err)
	}
	return &dc
}

func getDevice(t *testing.T, c client.Client, name string) *solasv1alpha1.Device {
	t.Helper()
	var d solasv1alpha1.Device
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

//= spec/solas.md#6-3-bind
//= type=test
//# After a successful bind, the controller MUST set the claim phase to
//# `Bound`.

//= spec/solas.md#6-3-bind
//= type=test
//# It MUST also set `status.fencingToken` to the fencing token of the device.

func TestBindMatchingFreeDevice(t *testing.T) {
	c1 := claim("c1", "u1")
	c1.Spec.Selector = &claimsv1alpha1.DeviceSelector{LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"kind": "gpu"}}}
	k := fakekube.NewClient(device("cpu-1", map[string]string{"kind": "cpu"}, nil),
		device("gpu-1", map[string]string{"kind": "gpu"}, nil), c1)
	settle(t, newReconciler(k, live()), c1)

	got := getClaim(t, k, "c1")
	if got.Status.Phase != claimsv1alpha1.ClaimBound || got.Status.DeviceName != "gpu-1" ||
		got.Status.MemberUID != muid || got.Status.FencingToken != 1 {
		t.Fatalf("claim status = %+v", got.Status)
	}
	if len(got.Finalizers) != 1 || got.Finalizers[0] != claimsv1alpha1.ReleaseFinalizer {
		t.Errorf("finalizers = %v", got.Finalizers)
	}
	if ref := getDevice(t, k, "gpu-1").Status.ClaimRef; ref == nil || ref.UID != c1.UID || ref.MemberUID != muid || ref.BoundAt == nil {
		t.Errorf("gpu-1 claimRef = %+v", ref)
	}
	if getDevice(t, k, "cpu-1").Status.ClaimRef != nil {
		t.Error("cpu-1 was bound, but it does not match the selector")
	}
}

//= spec/solas.md#6-2-controller
//= type=test
//# The controller MUST NOT bind while its member is not live by its own
//# clock, as section 7.4 defines.

func TestNoBindWhileNotLiveOrDraining(t *testing.T) {
	for _, st := range []member.Status{
		{UID: muid, Live: false},
		{UID: muid, Live: true, Draining: true},
		{},
	} {
		c1 := claim("c1", "u1")
		k := fakekube.NewClient(device("d1", nil, nil), c1)
		settle(t, newReconciler(k, &fixedMember{st}), c1)
		if getDevice(t, k, "d1").Status.ClaimRef != nil {
			t.Errorf("member %+v: device was bound", st)
		}
	}
}

// staleReader returns a device list captured before another cluster bound
// the device, as a racing controller would see it.
type staleReader struct {
	client.Reader
	devices solasv1alpha1.DeviceList
}

func (s staleReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if l, ok := list.(*solasv1alpha1.DeviceList); ok {
		s.devices.DeepCopyInto(l)
		return nil
	}
	return s.Reader.List(ctx, list, opts...)
}

//= spec/solas.md#6-3-bind
//= type=test
//# If the bind fails with `409 Conflict`, the controller MUST list again
//# and MUST NOT retry with the old version.

func TestTwoClaimsRaceForOneDevice(t *testing.T) {
	ctx := context.Background()
	c2 := claim("c2", "u2")
	c2.Finalizers = []string{claimsv1alpha1.ReleaseFinalizer}
	c2.Status.Phase = claimsv1alpha1.ClaimPending
	k := fakekube.NewClient(device("d1", nil, nil), c2)

	var before solasv1alpha1.DeviceList
	if err := k.List(ctx, &before); err != nil {
		t.Fatal(err)
	}
	// Another cluster binds d1 after c2's controller listed.
	other := getDevice(t, k, "d1")
	other.Status.ClaimRef = &solasv1alpha1.ClaimRef{Member: "cluster-b", MemberUID: "mb", Namespace: "ns", Name: "x", UID: "ux"}
	if err := k.Status().Update(ctx, other); err != nil {
		t.Fatal(err)
	}

	r := newReconciler(k, live())
	r.Reader = staleReader{Reader: k, devices: before}
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(c2)})
	if err != nil || !res.Requeue {
		t.Fatalf("stale bind: result %+v, err %v; want an immediate requeue", res, err)
	}
	if ref := getDevice(t, k, "d1").Status.ClaimRef; ref.Member != "cluster-b" {
		t.Errorf("d1 holder = %s, want cluster-b", ref.Member)
	}
	if p := getClaim(t, k, "c2").Status.Phase; p != claimsv1alpha1.ClaimPending {
		t.Errorf("c2 phase = %s, want Pending", p)
	}

	// With a fresh list, c2 finds no free device and stays Pending.
	r.Reader = k
	settle(t, r, c2)
	if p := getClaim(t, k, "c2").Status.Phase; p != claimsv1alpha1.ClaimPending {
		t.Errorf("c2 phase = %s, want Pending", p)
	}
}

//= spec/solas.md#6-3-bind
//= type=test
//# The controller MUST NOT adopt a device whose `claimRef` has an older
//# member UID.

func TestAdoptOnlyWithTheCurrentMemberUID(t *testing.T) {
	c1 := claim("c1", "u1")
	// d1 names c1 under the current UID: adopt it. d0 names it under an old
	// UID: leave it for the sweeper.
	k := fakekube.NewClient(device("d0", nil, refTo(c1, "old-uid")), device("d1", nil, refTo(c1, muid)), c1)
	settle(t, newReconciler(k, live()), c1)
	got := getClaim(t, k, "c1")
	if got.Status.Phase != claimsv1alpha1.ClaimBound || got.Status.DeviceName != "d1" || got.Status.FencingToken != 1 {
		t.Fatalf("claim status = %+v, want Bound to d1 with token 1", got.Status)
	}
}

//= spec/solas.md#6-3-bind
//= type=test
//# It MUST adopt the device only if that read still shows the same `claimRef`.

func TestNoAdoptFromALateList(t *testing.T) {
	ctx := context.Background()
	c1 := claim("c1", "u1")
	c1.Finalizers = []string{claimsv1alpha1.ReleaseFinalizer}
	c1.Status.Phase = claimsv1alpha1.ClaimPending
	k := fakekube.NewClient(device("d1", nil, refTo(c1, muid)), c1)

	var before solasv1alpha1.DeviceList
	if err := k.List(ctx, &before); err != nil {
		t.Fatal(err)
	}
	// c1 released d1 after the list, as a preempted holder does.
	d1 := getDevice(t, k, "d1")
	d1.Status.ClaimRef = nil
	if err := k.Status().Update(ctx, d1); err != nil {
		t.Fatal(err)
	}

	r := newReconciler(k, live())
	r.Reader = staleReader{Reader: k, devices: before}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(c1)}); err != nil {
		t.Fatal(err)
	}
	if p := getClaim(t, k, "c1").Status.Phase; p != claimsv1alpha1.ClaimPending {
		t.Errorf("c1 phase = %s, want Pending: it adopted a device that no longer names it", p)
	}
}

func TestDuplicateDeviceIsReleased(t *testing.T) {
	c1 := claim("c1", "u1")
	c1.Finalizers = []string{claimsv1alpha1.ReleaseFinalizer}
	c1.Status = claimsv1alpha1.DeviceClaimStatus{Phase: claimsv1alpha1.ClaimBound, DeviceName: "d2", MemberUID: muid, FencingToken: 1}
	k := fakekube.NewClient(device("d1", nil, refTo(c1, muid)), device("d2", nil, refTo(c1, muid)), c1)
	settle(t, newReconciler(k, live()), c1)
	if getDevice(t, k, "d1").Status.ClaimRef != nil {
		t.Error("the duplicate d1 still names the claim")
	}
	if getDevice(t, k, "d2").Status.ClaimRef == nil {
		t.Error("the claim's own device d2 was released")
	}
}

//= spec/solas.md#6-4-release
//= type=test
//# After the release, the controller MUST remove the finalizer.

func TestDeleteReleasesTheDevice(t *testing.T) {
	ctx := context.Background()
	c1 := claim("c1", "u1")
	k := fakekube.NewClient(device("d1", nil, nil), device("other", nil, &solasv1alpha1.ClaimRef{
		Member: "cluster-b", MemberUID: "mb", Namespace: "ns", Name: "x", UID: "ux"}), c1)
	r := newReconciler(k, live())
	settle(t, r, c1)
	if err := k.Delete(ctx, getClaim(t, k, "c1")); err != nil {
		t.Fatal(err)
	}
	settle(t, r, c1)
	if getDevice(t, k, "d1").Status.ClaimRef != nil {
		t.Error("d1 still names the deleted claim")
	}
	if getDevice(t, k, "other").Status.ClaimRef == nil {
		t.Error("release changed a device of another claim")
	}
	var gone claimsv1alpha1.DeviceClaim
	if err := k.Get(ctx, client.ObjectKeyFromObject(c1), &gone); !apierrors.IsNotFound(err) {
		t.Errorf("claim still exists: %v", err)
	}
}

//= spec/solas.md#6-5-phases
//= type=test
//# When the controller's member is not live by its own clock, the
//# controller MUST set each `Bound` claim to `Suspended`.

func TestSuspendAndResume(t *testing.T) {
	c1 := claim("c1", "u1")
	k := fakekube.NewClient(device("d1", nil, nil), c1)
	m := live()
	r := newReconciler(k, m)
	settle(t, r, c1)

	m.st.Live = false
	settle(t, r, c1)
	if p := getClaim(t, k, "c1").Status.Phase; p != claimsv1alpha1.ClaimSuspended {
		t.Fatalf("phase = %s, want Suspended", p)
	}
	m.st.Live = true
	settle(t, r, c1)
	if p := getClaim(t, k, "c1").Status.Phase; p != claimsv1alpha1.ClaimBound {
		t.Errorf("phase = %s, want Bound again", p)
	}
}

//= spec/solas.md#6-5-phases
//= type=test
//# When the controller finds that its member UID changed, it MUST set each
//# claim with the old `status.memberUID` to `Lost`.

func TestNewMemberUIDMakesClaimsLost(t *testing.T) {
	c1 := claim("c1", "u1")
	// The Member still has the old UID, so the Lost claim cannot recover
	// its device, spec 14.5.
	k := fakekube.NewClient(device("d1", nil, nil), c1,
		&solasv1alpha1.Member{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", UID: muid}})
	m := live()
	r := newReconciler(k, m)
	settle(t, r, c1)

	m.st.UID = "member-uid-2"
	settle(t, r, c1)
	if p := getClaim(t, k, "c1").Status.Phase; p != claimsv1alpha1.ClaimLost {
		t.Fatalf("phase = %s, want Lost", p)
	}
	settle(t, r, c1)
	if p := getClaim(t, k, "c1").Status.Phase; p != claimsv1alpha1.ClaimLost {
		t.Errorf("a Lost claim changed to %s", p)
	}
}

//= spec/solas.md#6-4-release
//= type=test
//# The controller MUST clear a `claimRef` that names its own member and a
//# claim UID that does not exist in its cluster.

func TestOrphanRefsAreCleared(t *testing.T) {
	ctx := context.Background()
	live1 := claim("c1", "u1")
	gone := claim("c2", "u2")
	recreated := claim("c3", "u3-new")
	oldC3 := claim("c3", "u3-old")
	otherCluster := &solasv1alpha1.ClaimRef{Member: "cluster-b", MemberUID: "mb", Namespace: "ns", Name: "c9", UID: "u9"}
	k := fakekube.NewClient(live1, recreated,
		device("d1", nil, refTo(live1, muid)),
		device("d2", nil, refTo(gone, muid)),
		device("d3", nil, refTo(oldC3, muid)),
		device("d4", nil, otherCluster))
	o := &Orphans{Reconciler: newReconciler(k, live())}
	if err := o.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	for name, wantHeld := range map[string]bool{"d1": true, "d2": false, "d3": false, "d4": true} {
		if held := getDevice(t, k, name).Status.ClaimRef != nil; held != wantHeld {
			t.Errorf("%s held = %v, want %v", name, held, wantHeld)
		}
	}
}

//= spec/solas.md#7-6-leave
//= type=test
//# The member MUST NOT release the device of a claim that is still `Bound`.

func TestDrainSuspendsThenReleases(t *testing.T) {
	ctx := context.Background()
	c1 := claim("c1", "u1")
	k := fakekube.NewClient(device("d1", nil, nil), c1)
	m := live()
	r := newReconciler(k, m)
	settle(t, r, c1)

	m.st.Draining = true
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(c1)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	// The first step suspends and keeps the device.
	if p := getClaim(t, k, "c1").Status.Phase; p != claimsv1alpha1.ClaimSuspended {
		t.Fatalf("phase = %s, want Suspended", p)
	}
	if getDevice(t, k, "d1").Status.ClaimRef == nil {
		t.Fatal("device released while the claim was still Bound")
	}
	if drained, _ := r.Drained(ctx, muid); drained {
		t.Fatal("Drained is true while d1 names the member")
	}
	settle(t, r, c1)
	if getDevice(t, k, "d1").Status.ClaimRef != nil {
		t.Error("device not released after the suspend")
	}
	if drained, err := r.Drained(ctx, muid); !drained || err != nil {
		t.Errorf("Drained = %v, %v; want true", drained, err)
	}
}

//= spec/solas.md#10-3-selection
//= type=test
//# A device matches a claim only when the label selector and the CEL
//# expression both match it.

func TestCELSelectsOnConditions(t *testing.T) {
	healthy := device("gpu-ok", map[string]string{"kind": "gpu"}, nil)
	healthy.Spec.Attributes = map[string]string{"model": "A100"}
	healthy.Status.Conditions = []metav1.Condition{{Type: "Healthy", Status: metav1.ConditionTrue, Reason: "Ok", LastTransitionTime: metav1.Now()}}
	sick := device("gpu-sick", map[string]string{"kind": "gpu"}, nil)
	sick.Spec.Attributes = map[string]string{"model": "A100"}
	sick.Status.Conditions = []metav1.Condition{{Type: "Healthy", Status: metav1.ConditionFalse, Reason: "Fan", LastTransitionTime: metav1.Now()}}
	other := device("gpu-h100", map[string]string{"kind": "gpu"}, nil)
	other.Spec.Attributes = map[string]string{"model": "H100"}
	other.Status.Conditions = healthy.Status.Conditions

	c1 := claim("c1", "u1")
	c1.Spec.Selector = &claimsv1alpha1.DeviceSelector{
		LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"kind": "gpu"}},
		CEL: `device.spec.attributes.model == "A100" &&
		      device.status.conditions.exists(c, c.type == "Healthy" && c.status == "True")`,
	}
	k := fakekube.NewClient(sick, other, healthy, c1)
	settle(t, newReconciler(k, live()), c1)
	if got := getClaim(t, k, "c1"); got.Status.DeviceName != "gpu-ok" {
		t.Errorf("claim bound %q, want gpu-ok", got.Status.DeviceName)
	}
}

func TestInvalidCELNeverBinds(t *testing.T) {
	c1 := claim("c1", "u1")
	c1.Spec.Selector = &claimsv1alpha1.DeviceSelector{CEL: `device.spec.(`}
	k := fakekube.NewClient(device("d1", nil, nil), c1)
	settle(t, newReconciler(k, live()), c1)
	got := getClaim(t, k, "c1")
	if got.Status.Phase == claimsv1alpha1.ClaimBound {
		t.Fatal("a claim with invalid CEL bound a device")
	}
	c := meta.FindStatusCondition(got.Status.Conditions, "SelectorValid")
	if c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("SelectorValid = %+v, want False", c)
	}
}

//= spec/solas.md#10-4-lease-display
//= type=test
//# The controller SHOULD clear it when the claim becomes `Pending`,
//# `Suspended`, or `Lost`.

func TestLeaseExpiresAtIsShown(t *testing.T) {
	c1 := claim("c1", "u1")
	k := fakekube.NewClient(device("d1", nil, nil), c1)
	m := live()
	m.st.LeaseEnd = time.Unix(5000, 0)
	r := newReconciler(k, m)
	settle(t, r, c1)
	// The bind ends the first reconcile; the next one shows the lease end.
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(c1)}); err != nil {
		t.Fatal(err)
	}
	if got := getClaim(t, k, "c1").Status.LeaseExpiresAt; got == nil || !got.Time.Equal(time.Unix(5000, 0)) {
		t.Errorf("leaseExpiresAt = %v, want 5000", got)
	}

	// The lease lapses: the claim is Suspended and shows no lease end.
	m.st.Live = false
	settle(t, r, c1)
	if got := getClaim(t, k, "c1").Status; got.Phase != claimsv1alpha1.ClaimSuspended || got.LeaseExpiresAt != nil {
		t.Errorf("claim status = %+v, want Suspended with no leaseExpiresAt", got)
	}
}

//= spec/solas.md#7-2-join
//= type=test
//# This MUST include `Preempting` and `Preempted` claims, because they still
//# hold a device.

func TestMarkLostCoversEveryHeldPhase(t *testing.T) {
	phases := map[string]claimsv1alpha1.ClaimPhase{
		"bound":      claimsv1alpha1.ClaimBound,
		"suspended":  claimsv1alpha1.ClaimSuspended,
		"preempting": claimsv1alpha1.ClaimPreempting,
		"preempted":  claimsv1alpha1.ClaimPreempted,
		"pending":    claimsv1alpha1.ClaimPending,
	}
	var objs []client.Object
	for name, p := range phases {
		c := claim(name, types.UID("u-"+name))
		c.Status.Phase = p
		c.Status.MemberUID = "old-uid"
		objs = append(objs, c)
	}
	k := fakekube.NewClient(objs...)
	if err := newReconciler(k, live()).MarkLost(context.Background(), muid); err != nil {
		t.Fatal(err)
	}
	for name, p := range phases {
		want := claimsv1alpha1.ClaimLost
		if p == claimsv1alpha1.ClaimPending {
			want = p
		}
		if got := getClaim(t, k, name).Status.Phase; got != want {
			t.Errorf("%s: phase = %s, want %s", name, got, want)
		}
	}
}
