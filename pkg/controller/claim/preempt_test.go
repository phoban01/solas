package claim

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/phoban01/solas/internal/fakekube"
	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

func preemptibleDevice(name string, grace int32) *solasv1alpha1.Device {
	d := device(name, nil, nil)
	d.Spec.Preemptible = true
	d.Spec.PreemptionGracePeriodSeconds = &grace
	return d
}

func prioClaim(name string, prio int32) *claimsv1alpha1.DeviceClaim {
	c := claim(name, types.UID("u-"+name))
	c.Spec.Priority = prio
	return c
}

func reconcileOnce(t *testing.T, r *Reconciler, c *claimsv1alpha1.DeviceClaim) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(c)}); err != nil {
		t.Fatal(err)
	}
}

//= spec/solas.md#10-6-release-by-the-holder
//= type=test
//# When the grace period has passed since that time, by the holder's clock,
//# the controller MUST set its claim to `Preempted`.

//= spec/solas.md#10-6-release-by-the-holder
//= type=test
//# The controller MUST then set the claim back to `Pending`.

func TestPreemptionWithGracePeriod(t *testing.T) {
	low, high := prioClaim("low", 0), prioClaim("high", 5)
	k := fakekube.NewClient(preemptibleDevice("d1", 30), low)
	clk := clocktesting.NewFakePassiveClock(time.Unix(1000, 0))
	r := newReconciler(k, live())
	r.Clock = clk
	settle(t, r, low)
	if err := k.Create(context.Background(), high); err != nil {
		t.Fatal(err)
	}
	settle(t, r, high)

	d := getDevice(t, k, "d1")
	if d.Status.Preemption == nil || d.Status.Preemption.Claim.Name != "high" || d.Status.Preemption.Claim.Priority != 5 {
		t.Fatalf("request = %+v", d.Status.Preemption)
	}
	if got := getClaim(t, k, "high"); got.Status.Phase != claimsv1alpha1.ClaimPending || got.Status.PreemptionTarget != "d1" {
		t.Fatalf("high = %+v", got.Status)
	}

	// The holder sees the request and keeps the device through the grace.
	settle(t, r, low)
	if p := getClaim(t, k, "low").Status.Phase; p != claimsv1alpha1.ClaimPreempting {
		t.Fatalf("low phase = %s, want Preempting", p)
	}
	clk.SetTime(clk.Now().Add(29 * time.Second))
	settle(t, r, low)
	if p := getClaim(t, k, "low").Status.Phase; p != claimsv1alpha1.ClaimPreempting {
		t.Fatalf("low phase before the grace ended = %s", p)
	}
	clk.SetTime(clk.Now().Add(2 * time.Second))
	settle(t, r, low)
	got := getClaim(t, k, "low")
	if got.Status.Phase != claimsv1alpha1.ClaimPending || got.Status.DeviceName != "" {
		t.Fatalf("low after the grace = %+v, want Pending with no device", got.Status)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, "Preempted"); c == nil {
		t.Error("low has no Preempted condition")
	}

	// The device is kept for high, which binds it with token 2.
	settle(t, r, high)
	if got := getClaim(t, k, "high"); got.Status.Phase != claimsv1alpha1.ClaimBound || got.Status.FencingToken != 2 {
		t.Fatalf("high = %+v, want Bound with token 2", got.Status)
	}
	if d := getDevice(t, k, "d1"); d.Status.Preemption != nil || d.Status.ClaimRef.Priority != 5 {
		t.Errorf("d1 after the bind: request %+v, holder priority %d", d.Status.Preemption, d.Status.ClaimRef.Priority)
	}
	// low cannot take it back.
	settle(t, r, low)
	if d := getDevice(t, k, "d1"); d.Status.Preemption != nil {
		t.Error("low asked to preempt a claim of higher priority")
	}
}

func TestNoPreemptionWithoutRights(t *testing.T) {
	for name, tc := range map[string]struct {
		dev  *solasv1alpha1.Device
		prio int32
	}{
		"not preemptible": {device("d1", nil, nil), 5},
		"equal priority":  {preemptibleDevice("d1", 30), 0},
	} {
		low, high := prioClaim("low", 0), prioClaim("high", tc.prio)
		k := fakekube.NewClient(tc.dev, low)
		r := newReconciler(k, live())
		settle(t, r, low)
		if err := k.Create(context.Background(), high); err != nil {
			t.Fatal(err)
		}
		settle(t, r, high)
		if d := getDevice(t, k, "d1"); d.Status.Preemption != nil {
			t.Errorf("%s: request = %+v, want none", name, d.Status.Preemption)
		}
	}
}

//= spec/solas.md#10-8-holders-that-do-not-respond
//= type=test
//# Before each renew, a member MUST handle each preemption request on the
//# devices it holds, as section 10.6 describes.

func TestHandlePreemptionsBeforeRenew(t *testing.T) {
	low, high := prioClaim("low", 0), prioClaim("high", 5)
	k := fakekube.NewClient(preemptibleDevice("d1", 30), low)
	r := newReconciler(k, live())
	settle(t, r, low)
	if err := k.Create(context.Background(), high); err != nil {
		t.Fatal(err)
	}
	settle(t, r, high)
	if err := r.HandlePreemptions(context.Background(), muid); err != nil {
		t.Fatal(err)
	}
	if p := getClaim(t, k, "low").Status.Phase; p != claimsv1alpha1.ClaimPreempting {
		t.Errorf("low phase = %s, want Preempting", p)
	}
}

func TestPreemptorWithdrawsWhenItBindsElsewhere(t *testing.T) {
	low, high := prioClaim("low", 0), prioClaim("high", 5)
	k := fakekube.NewClient(preemptibleDevice("d1", 30), low)
	r := newReconciler(k, live())
	settle(t, r, low)
	if err := k.Create(context.Background(), high); err != nil {
		t.Fatal(err)
	}
	settle(t, r, high)
	// A new free device appears, and high binds it instead.
	if err := k.Create(context.Background(), device("d2", nil, nil)); err != nil {
		t.Fatal(err)
	}
	settle(t, r, high)
	reconcileOnce(t, r, high)
	if got := getClaim(t, k, "high"); got.Status.DeviceName != "d2" || got.Status.PreemptionTarget != "" {
		t.Fatalf("high = %+v", got.Status)
	}
	if d := getDevice(t, k, "d1"); d.Status.Preemption != nil {
		t.Error("the request on d1 stands after high bound d2")
	}
}

func TestOrphanSweepClearsRequestOfGoneClaim(t *testing.T) {
	low, high := prioClaim("low", 0), prioClaim("high", 5)
	k := fakekube.NewClient(preemptibleDevice("d1", 30), low)
	r := newReconciler(k, live())
	settle(t, r, low)
	if err := k.Create(context.Background(), high); err != nil {
		t.Fatal(err)
	}
	settle(t, r, high)
	// The claim goes away without its finalizer running.
	gone := getClaim(t, k, "high")
	gone.Finalizers = nil
	if err := k.Update(context.Background(), gone); err != nil {
		t.Fatal(err)
	}
	if err := k.Delete(context.Background(), gone); err != nil {
		t.Fatal(err)
	}
	if err := (&Orphans{Reconciler: r}).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := getDevice(t, k, "d1"); d.Status.Preemption != nil {
		t.Error("the request of a gone claim stands")
	}
}

//= spec/solas.md#10-10-protected-holders
//= type=test
//# A protected holder can only release its device.

// TestNoRequestForAProtectedHolder: a claim of higher priority does not
// ask for a preemptible device whose holder is protected.
func TestNoRequestForAProtectedHolder(t *testing.T) {
	low, high := prioClaim("low", 0), prioClaim("high", 5)
	held := refTo(low, muid)
	held.Protected = true
	d := preemptibleDevice("d1", 30)
	d.Status.ClaimRef, d.Status.FencingToken = held, 1
	low.Finalizers = []string{claimsv1alpha1.ReleaseFinalizer}
	low.Status = claimsv1alpha1.DeviceClaimStatus{Phase: claimsv1alpha1.ClaimBound, DeviceName: "d1", MemberUID: muid, FencingToken: 1}
	k := fakekube.NewClient(d, low, high)
	r := newReconciler(k, live())
	settle(t, r, high)
	if got := getDevice(t, k, "d1"); got.Status.Preemption != nil {
		t.Errorf("request = %+v, want none for a protected holder", got.Status.Preemption)
	}
}
