package member

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/phoban01/solas/internal/fakekube"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

func newManager(c client.Client, clk *clocktesting.FakePassiveClock) *Manager {
	return &Manager{
		Client: c, Reader: c, ClusterID: "cluster-a",
		Lease: 30 * time.Second, Margin: 3 * time.Second, Clock: clk,
	}
}

func getMember(t *testing.T, c client.Client) *solasv1alpha1.Member {
	t.Helper()
	var m solasv1alpha1.Member
	if err := c.Get(context.Background(), client.ObjectKey{Name: "cluster-a"}, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

//= spec/solas.md#7-4-lease-on-the-holder
//= type=test
//# The member MUST treat itself as not live from local time `S + D - M`.

func TestJoinRenewAndLocalLease(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient()
	clk := clocktesting.NewFakePassiveClock(time.Unix(1000, 0))
	m := newManager(c, clk)

	if m.Status().Live {
		t.Fatal("live before join")
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	mem := getMember(t, c)
	if st := m.Status(); !st.Live || st.UID != mem.UID || mem.Spec.LeaseDurationSeconds != 30 {
		t.Fatalf("after join: status %+v, member uid %s lease %d", st, mem.UID, mem.Spec.LeaseDurationSeconds)
	}

	// Live until S + D - M = 27s after the send.
	clk.SetTime(clk.Now().Add(26 * time.Second))
	if !m.Status().Live {
		t.Error("not live 26s after the renew")
	}
	clk.SetTime(clk.Now().Add(time.Second))
	if m.Status().Live {
		t.Error("live 27s after the renew")
	}

	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if !m.Status().Live {
		t.Error("not live after a renew")
	}
	if got := getMember(t, c); got.UID != mem.UID || got.ResourceVersion == mem.ResourceVersion {
		t.Errorf("renew: uid %s rv %s, want the same uid and a new rv", got.UID, got.ResourceVersion)
	}
}

//= spec/solas.md#7-2-join
//= type=test
//# If the renew succeeds, the controller MUST keep the UID of that `Member`.

func TestStartRenewsAnExistingMember(t *testing.T) {
	ctx := context.Background()
	existing := &solasv1alpha1.Member{}
	existing.Name, existing.UID = "cluster-a", types.UID("old-uid")
	existing.Status.Phase = solasv1alpha1.MemberActive
	c := fakekube.NewClient(existing)
	m := newManager(c, clocktesting.NewFakePassiveClock(time.Unix(1000, 0)))
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.UID != "old-uid" || !st.Live {
		t.Errorf("status %+v, want uid old-uid and live", st)
	}
}

//= spec/solas.md#7-3-renew
//= type=test
//# After a `404 NotFound`, the member MUST join again as section 7.2
//# describes.

func TestRenewAfterDeleteJoinsWithANewUID(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient()
	m := newManager(c, clocktesting.NewFakePassiveClock(time.Unix(1000, 0)))
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	old := m.Status().UID

	// A sweeper deletes the Member.
	if err := c.Delete(ctx, getMember(t, c)); err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.UID != "" || st.Live {
		t.Fatalf("after 404: status %+v, want not joined", st)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.UID == "" || st.UID == old {
		t.Errorf("after rejoin: uid %q, want a new uid (old %q)", st.UID, old)
	}
}

func TestLeaveDeletesTheMemberWhenDrained(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient()
	m := newManager(c, clocktesting.NewFakePassiveClock(time.Unix(1000, 0)))
	drained := false
	m.Drained = func(context.Context, types.UID) (bool, error) { return drained, nil }
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// Another client starts the leave.
	mem := getMember(t, c)
	mem.Status.Phase = solasv1alpha1.MemberDraining
	if err := c.Status().Update(ctx, mem); err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(ctx); err != nil { // the renew conflicts and reads Draining
		t.Fatal(err)
	}
	if !m.Status().Draining {
		t.Fatal("manager did not see Draining")
	}
	if err := m.Tick(ctx); err != nil { // not drained yet: renew
		t.Fatal(err)
	}
	drained = true
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if !m.Status().Left {
		t.Error("manager did not leave")
	}
	var gone solasv1alpha1.Member
	if err := c.Get(ctx, client.ObjectKey{Name: "cluster-a"}, &gone); !apierrors.IsNotFound(err) {
		t.Errorf("member still exists: %v", err)
	}
	if err := m.Tick(ctx); err != nil || m.Status().UID != "" {
		t.Errorf("after leave the manager joined again: %+v, %v", m.Status(), err)
	}
}

//= spec/solas.md#7-2-join
//= type=test
//# After a join, the controller MUST set each claim with another
//# `status.memberUID` to `Lost` before it treats itself as live.

func TestJoinMarksLostBeforeLive(t *testing.T) {
	ctx := context.Background()
	c := fakekube.NewClient()
	m := newManager(c, clocktesting.NewFakePassiveClock(time.Unix(1000, 0)))
	fail := true
	var marked []types.UID
	m.MarkLost = func(_ context.Context, uid types.UID) error {
		if fail {
			return errors.New("claims store down")
		}
		marked = append(marked, uid)
		return nil
	}
	if err := m.Tick(ctx); err == nil {
		t.Fatal("join succeeded although MarkLost failed")
	}
	if m.Status().Live {
		t.Fatal("live before MarkLost succeeded")
	}
	fail = false
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); !st.Live || len(marked) != 1 || marked[0] != st.UID {
		t.Errorf("status %+v, marked %v", st, marked)
	}
}

//= spec/solas.md#11-4-finalization
//= type=test
//# Each member MUST report the lowest and the highest format that it
//# supports in its `Member` status.

func TestMemberReportsItsFormats(t *testing.T) {
	ctx := context.Background()
	// A Member that an older release created has no range.
	existing := &solasv1alpha1.Member{}
	existing.Name, existing.UID = "cluster-a", types.UID("old-uid")
	existing.Status.Phase = solasv1alpha1.MemberActive
	c := fakekube.NewClient(existing)
	m := newManager(c, clocktesting.NewFakePassiveClock(time.Unix(1000, 0)))
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := getMember(t, c).Status; got.MinFormat != 1 || got.MaxFormat != 1 {
		t.Errorf("after the first renew: formats %d-%d, want 1-1", got.MinFormat, got.MaxFormat)
	}

	// A new member reports its range when it joins.
	c = fakekube.NewClient()
	m = newManager(c, clocktesting.NewFakePassiveClock(time.Unix(1000, 0)))
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := getMember(t, c).Status; got.MinFormat != 1 || got.MaxFormat != 1 {
		t.Errorf("after the join: formats %d-%d, want 1-1", got.MinFormat, got.MaxFormat)
	}
}
