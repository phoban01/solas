package sweeper

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/phoban01/solas/internal/fakekube"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

func member(name string, uid types.UID) *solasv1alpha1.Member {
	m := &solasv1alpha1.Member{ObjectMeta: metav1.ObjectMeta{Name: name, UID: uid}}
	m.Spec.LeaseDurationSeconds = 30
	m.Status.Phase = solasv1alpha1.MemberActive
	return m
}

func newSweeper(c client.Client, clk *clocktesting.FakePassiveClock) *Sweeper {
	return &Sweeper{Client: c, Reader: c, ClusterID: "a", Clock: clk, Interval: 10 * time.Second}
}

func exists(t *testing.T, c client.Client, name string) bool {
	t.Helper()
	var m solasv1alpha1.Member
	err := c.Get(context.Background(), client.ObjectKey{Name: name}, &m)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func renew(t *testing.T, c client.Client, name string) {
	t.Helper()
	var m solasv1alpha1.Member
	if err := c.Get(context.Background(), client.ObjectKey{Name: name}, &m); err != nil {
		t.Fatal(err)
	}
	now := metav1.NowMicro()
	m.Status.RenewTime = &now
	if err := c.Status().Update(context.Background(), &m); err != nil {
		t.Fatal(err)
	}
}

//= spec/solas.md#8-1-expiry-on-the-observer
//= type=test
//# The observer MUST treat the `Member` as expired from local time `T + D`.

func TestExpireAfterDOnTheObserverClock(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient(member("a", "ua"), member("b", "ub"))
	clk := clocktesting.NewFakePassiveClock(time.Unix(5000, 0))
	s := newSweeper(c, clk)

	if err := s.Run(ctx); err != nil { // first sight of b at T
		t.Fatal(err)
	}
	advance(t, s, clk, 29*time.Second)
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(t, c, "b") {
		t.Fatal("b deleted before T + D")
	}
	clk.SetTime(clk.Now().Add(time.Second))
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(t, c, "b") {
		t.Error("b not deleted at T + D")
	}
	if !exists(t, c, "a") {
		t.Error("the sweeper deleted its own member")
	}
}

func TestRenewRestartsTheTimer(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient(member("a", "ua"), member("b", "ub"))
	clk := clocktesting.NewFakePassiveClock(time.Unix(5000, 0))
	s := newSweeper(c, clk)
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	clk.SetTime(clk.Now().Add(20 * time.Second))
	renew(t, c, "b")
	if err := s.Run(ctx); err != nil { // sees the new version at T'
		t.Fatal(err)
	}
	clk.SetTime(clk.Now().Add(20 * time.Second)) // 40s after T, 20s after T'
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(t, c, "b") {
		t.Error("b deleted although it renewed")
	}
}

// staleMembers returns an old member list, as a sweeper that missed a
// renew would see it.
type staleMembers struct {
	client.Reader
	members solasv1alpha1.MemberList
}

func (s staleMembers) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if l, ok := list.(*solasv1alpha1.MemberList); ok {
		s.members.DeepCopyInto(l)
		return nil
	}
	return s.Reader.List(ctx, list, opts...)
}

//= spec/solas.md#8-2-delete-an-expired-member
//= type=test
//# The sweeper MUST delete an expired `Member` with the resource version
//# that it observed.

func TestRenewAndDeleteCannotBothSucceed(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient(member("a", "ua"), member("b", "ub"))
	clk := clocktesting.NewFakePassiveClock(time.Unix(5000, 0))
	s := newSweeper(c, clk)

	var old solasv1alpha1.MemberList
	if err := c.List(ctx, &old); err != nil {
		t.Fatal(err)
	}
	s.Reader = staleMembers{Reader: c, members: old}
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// b renews, but this sweeper does not see it and thinks b expired.
	renew(t, c, "b")
	clk.SetTime(clk.Now().Add(time.Minute))
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(t, c, "b") {
		t.Error("a stale observation deleted a member that had renewed")
	}
}

func device(name string, ref *solasv1alpha1.ClaimRef) *solasv1alpha1.Device {
	d := &solasv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: name}}
	d.Status.ClaimRef = ref
	return d
}

func ref(memberName string, uid types.UID) *solasv1alpha1.ClaimRef {
	return &solasv1alpha1.ClaimRef{Member: memberName, MemberUID: uid, Namespace: "ns", Name: "c", UID: "uc-" + uid}
}

// orderReader records the order of list calls.
type orderReader struct {
	client.Reader
	calls *[]string
}

func (o orderReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	switch list.(type) {
	case *solasv1alpha1.DeviceList:
		*o.calls = append(*o.calls, "devices")
	case *solasv1alpha1.MemberList:
		*o.calls = append(*o.calls, "members")
	}
	return o.Reader.List(ctx, list, opts...)
}

//= spec/solas.md#8-3-clear-stale-claim-references
//= type=test
//# For each device whose `claimRef` names a member UID that is not in the
//# member list, the sweeper MUST clear `claimRef`.

//= spec/solas.md#8-3-clear-stale-claim-references
//= type=test
//# The sweeper MUST do a consistent list of devices first.

func TestClearStaleRefsListsDevicesFirst(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient(member("a", "ua"),
		device("d1", ref("a", "ua")),
		device("d2", ref("b", "ub-gone")),
		device("d3", ref("a", "ua-old")))
	var calls []string
	s := newSweeper(c, clocktesting.NewFakePassiveClock(time.Unix(5000, 0)))
	s.Reader = orderReader{Reader: c, calls: &calls}
	if err := s.clearStale(ctx); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "devices" || calls[1] != "members" {
		t.Errorf("list order = %v, want [devices members]", calls)
	}
	for name, wantHeld := range map[string]bool{"d1": true, "d2": false, "d3": false} {
		var d solasv1alpha1.Device
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &d); err != nil {
			t.Fatal(err)
		}
		if held := d.Status.ClaimRef != nil; held != wantHeld {
			t.Errorf("%s held = %v, want %v", name, held, wantHeld)
		}
	}
}

//= spec/solas.md#10-9-stale-requests
//= type=test
//# The sweeper MUST clear a request whose member UID is not in the member
//# list, as it does for a `claimRef`, spec 8.3.

func TestClearStaleRequests(t *testing.T) {
	ctx := context.Background()
	held := device("d1", ref("a", "ua"))
	held.Spec.Preemptible = true
	held.Status.Preemption = &solasv1alpha1.PreemptionRequest{Claim: *ref("b", "ub-gone")}
	held.Status.Preemption.Claim.Priority = 5
	c := fakekube.NewClient(member("a", "ua"), held)
	s := newSweeper(c, clocktesting.NewFakePassiveClock(time.Unix(5000, 0)))
	if err := s.clearStale(ctx); err != nil {
		t.Fatal(err)
	}
	var d solasv1alpha1.Device
	if err := c.Get(ctx, client.ObjectKey{Name: "d1"}, &d); err != nil {
		t.Fatal(err)
	}
	if d.Status.Preemption != nil || d.Status.ClaimRef == nil {
		t.Errorf("request %+v, holder %+v; want the request gone and the holder kept", d.Status.Preemption, d.Status.ClaimRef)
	}
}

//= spec/solas.md#14-3-withdraw
//= type=test
//# The sweeper MUST clear an offer whose member UID is not in the member
//# list, as it does for a preemption request, spec 10.9.

func TestClearStaleOffers(t *testing.T) {
	ctx := context.Background()
	gone := device("d1", ref("a", "ua"))
	gone.Status.Offer = ref("b", "ub-gone")
	kept := device("d2", ref("a", "ua"))
	kept.Status.Offer = ref("a", "ua")
	kept.Status.Offer.Name, kept.Status.Offer.UID = "other", "other-uid"
	c := fakekube.NewClient(member("a", "ua"), gone, kept)
	s := newSweeper(c, clocktesting.NewFakePassiveClock(time.Unix(5000, 0)))
	if err := s.clearStale(ctx); err != nil {
		t.Fatal(err)
	}
	for name, wantOffer := range map[string]bool{"d1": false, "d2": true} {
		var d solasv1alpha1.Device
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &d); err != nil {
			t.Fatal(err)
		}
		if (d.Status.Offer != nil) != wantOffer || d.Status.ClaimRef == nil {
			t.Errorf("%s: offer %+v, holder %+v; want offer %v and the holder kept", name, d.Status.Offer, d.Status.ClaimRef, wantOffer)
		}
	}
}

// advance moves the clock by d in steps of at most the sweep interval, and
// runs the sweeper after each step, as Start does.
func advance(t *testing.T, s *Sweeper, clk *clocktesting.FakePassiveClock, d time.Duration) {
	t.Helper()
	for d > 0 {
		step := min(d, s.Interval)
		clk.SetTime(clk.Now().Add(step))
		d -= step
		if err := s.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

//= spec/solas.md#8-1-expiry-on-the-observer
//= type=test
//# When the observer has not read the member list for longer than the read
//# gap `G`, it MUST forget the local times that it recorded, and record
//# them again from its next read.

// TestOutageRestartsTheTimer: the store is down for longer than D, so the
// sweeper cannot list and b cannot renew. When the store returns, the
// sweeper does not delete b at once. It waits a full D from its first read
// after the outage.
func TestOutageRestartsTheTimer(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient(member("a", "ua"), member("b", "ub"))
	clk := clocktesting.NewFakePassiveClock(time.Unix(5000, 0))
	s := newSweeper(c, clk)
	if err := s.Run(ctx); err != nil { // first sight of b
		t.Fatal(err)
	}
	clk.SetTime(clk.Now().Add(2 * time.Minute)) // the outage: no run works
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(t, c, "b") {
		t.Fatal("b deleted on the first read after the outage")
	}
	advance(t, s, clk, 29*time.Second)
	if !exists(t, c, "b") {
		t.Fatal("b deleted before D after the outage")
	}
	advance(t, s, clk, time.Second)
	if exists(t, c, "b") {
		t.Error("b, which never renewed, not deleted D after the outage")
	}
}

//= spec/solas.md#8-5-reclaim-policy
//= type=test
//# Under `Retain`, the sweeper MUST NOT clear the `claimRef`.

//= spec/solas.md#8-5-reclaim-policy
//= type=test
//# Under `Delay`, the sweeper MUST NOT clear the `claimRef` until the
//# reclaim time `R` of the device has passed.

// TestReclaimPolicy holds one device under each policy for a member that
// is gone. Delete is freed at once, Delay after R = 20s, and Retain never.
func TestReclaimPolicy(t *testing.T) {
	ctx := context.Background()
	held := func(name string, policy solasv1alpha1.ReclaimPolicy, delay *int32) *solasv1alpha1.Device {
		d := &solasv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: name}}
		d.Spec.ReclaimPolicy, d.Spec.ReclaimDelaySeconds = policy, delay
		d.Status.ClaimRef = &solasv1alpha1.ClaimRef{Member: "gone", MemberUID: "ug", Namespace: "ns", Name: "c-" + name, UID: types.UID("u-" + name)}
		d.Status.FencingToken = 1
		return d
	}
	r := int32(20)
	c := fakekube.NewClient(member("a", "ua"),
		held("d-delete", solasv1alpha1.ReclaimDelete, nil),
		held("d-delay", solasv1alpha1.ReclaimDelay, &r),
		held("d-retain", solasv1alpha1.ReclaimRetain, nil))
	clk := clocktesting.NewFakePassiveClock(time.Unix(5000, 0))
	s := newSweeper(c, clk)
	free := func(name string) bool {
		t.Helper()
		var d solasv1alpha1.Device
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &d); err != nil {
			t.Fatal(err)
		}
		return d.Status.ClaimRef == nil
	}

	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !free("d-delete") || free("d-delay") || free("d-retain") {
		t.Fatalf("after the first sweep: delete free %v, delay free %v, retain free %v; want true, false, false",
			free("d-delete"), free("d-delay"), free("d-retain"))
	}
	advance(t, s, clk, 19*time.Second)
	if free("d-delay") {
		t.Fatal("d-delay freed before R")
	}
	advance(t, s, clk, time.Second)
	if !free("d-delay") {
		t.Error("d-delay not freed at R")
	}
	advance(t, s, clk, 10*time.Minute)
	if free("d-retain") {
		t.Error("the sweeper freed d-retain")
	}
}
