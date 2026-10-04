package validation

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/phoban01/solas/pkg/apis/solas"
)

func ref(claim string) *solas.ClaimRef {
	return &solas.ClaimRef{Member: "a", MemberUID: "mu", Namespace: "ns", Name: claim, UID: types.UID("u-" + claim)}
}

func device(r *solas.ClaimRef) *solas.Device {
	return &solas.Device{
		ObjectMeta: metav1.ObjectMeta{Name: "d1", ResourceVersion: "2"},
		Status:     solas.DeviceStatus{ClaimRef: r},
	}
}

//= spec/solas.md#5-3-status-updates
//= type=test
//# The server MUST reject a status update that sets a `claimRef` with an
//# empty field.

func TestClaimRefNeedsEveryField(t *testing.T) {
	if errs := ValidateDevice(device(ref("c1"))); len(errs) != 0 {
		t.Fatalf("full claimRef: %v", errs)
	}
	for _, clear := range []func(*solas.ClaimRef){
		func(r *solas.ClaimRef) { r.Member = "" },
		func(r *solas.ClaimRef) { r.MemberUID = "" },
		func(r *solas.ClaimRef) { r.Namespace = "" },
		func(r *solas.ClaimRef) { r.Name = "" },
		func(r *solas.ClaimRef) { r.UID = "" },
	} {
		r := ref("c1")
		clear(r)
		if errs := ValidateDeviceStatusUpdate(device(r), device(nil)); len(errs) != 1 {
			t.Errorf("claimRef %+v: got %d errors, want 1: %v", *r, len(errs), errs)
		}
	}
}

//= spec/solas.md#5-3-status-updates
//= type=test
//# The server MUST reject a status update that changes `claimRef` from one
//# holder to a different holder.

func TestHolderToHolderIsRejected(t *testing.T) {
	tests := []struct {
		name     string
		old, new *solas.ClaimRef
		wantErr  bool
	}{
		{"bind a free device", nil, ref("c1"), false},
		{"release", ref("c1"), nil, false},
		{"keep the same holder", ref("c1"), ref("c1"), false},
		{"move to another claim", ref("c1"), ref("c2"), true},
		{"change only the member UID", ref("c1"), func() *solas.ClaimRef { r := ref("c1"); r.MemberUID = "other"; return r }(), true},
	}
	for _, tt := range tests {
		errs := ValidateDeviceStatusUpdate(device(tt.new), device(tt.old))
		if (len(errs) > 0) != tt.wantErr {
			t.Errorf("%s: errors = %v, wantErr %v", tt.name, errs, tt.wantErr)
		}
	}
}

func TestMember(t *testing.T) {
	good := &solas.Member{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
		Spec:       solas.MemberSpec{LeaseDurationSeconds: 30},
		Status:     solas.MemberStatus{Phase: solas.MemberActive},
	}
	if errs := ValidateMember(good); len(errs) != 0 {
		t.Fatalf("good member: %v", errs)
	}
	noLease := good.DeepCopy()
	noLease.Spec.LeaseDurationSeconds = 0
	if errs := ValidateMember(noLease); len(errs) != 1 {
		t.Errorf("lease 0: got %v, want 1 error", errs)
	}
	badPhase := good.DeepCopy()
	badPhase.Status.Phase = "Gone"
	if errs := ValidateMember(badPhase); len(errs) != 1 {
		t.Errorf("phase Gone: got %v, want 1 error", errs)
	}
}

func preemptible(holder *solas.ClaimRef, req *solas.ClaimRef) *solas.Device {
	d := device(holder)
	d.Spec.Preemptible = true
	if req != nil {
		d.Status.Preemption = &solas.PreemptionRequest{Claim: *req}
	}
	return d
}

func withPrio(r *solas.ClaimRef, p int32) *solas.ClaimRef {
	r.Priority = p
	return r
}

//= spec/solas.md#10-5-preemption-request
//= type=test
//# The server MUST reject a request whose priority is not greater than the
//# priority of the holder.

//= spec/solas.md#10-5-preemption-request
//= type=test
//# The server MUST reject a request that replaces a request of equal or
//# greater priority.

func TestPreemptionRequest(t *testing.T) {
	holder := withPrio(ref("c1"), 1)
	tests := []struct {
		name     string
		old, new *solas.Device
		wantErr  bool
	}{
		{"higher priority on a preemptible device", preemptible(holder, nil), preemptible(holder, withPrio(ref("c2"), 2)), false},
		{"equal priority", preemptible(holder, nil), preemptible(holder, withPrio(ref("c2"), 1)), true},
		{"not preemptible", device(holder), func() *solas.Device {
			d := device(holder)
			d.Status.Preemption = &solas.PreemptionRequest{Claim: *withPrio(ref("c2"), 5)}
			return d
		}(), true},
		{"free device", preemptible(nil, nil), preemptible(nil, withPrio(ref("c2"), 5)), true},
		{"replace with a higher request", preemptible(holder, withPrio(ref("c2"), 2)), preemptible(holder, withPrio(ref("c3"), 3)), false},
		{"replace with an equal request", preemptible(holder, withPrio(ref("c2"), 2)), preemptible(holder, withPrio(ref("c3"), 2)), true},
		{"withdraw a request", preemptible(holder, withPrio(ref("c2"), 2)), preemptible(holder, nil), false},
	}
	for _, tt := range tests {
		errs := ValidateDeviceStatusUpdate(tt.new, tt.old)
		if (len(errs) > 0) != tt.wantErr {
			t.Errorf("%s: errors = %v, wantErr %v", tt.name, errs, tt.wantErr)
		}
	}
}

//= spec/solas.md#10-7-bind-by-the-preemptor
//= type=test
//# While a device has a request, the server MUST reject a bind that does not
//# name the requesting claim.

func TestReservedDevice(t *testing.T) {
	req := withPrio(ref("c2"), 2)
	old := preemptible(nil, req) // the holder released; the request stands
	if errs := ValidateDeviceStatusUpdate(preemptible(withPrio(ref("c9"), 5), req), old); len(errs) == 0 {
		t.Error("a claim other than the preemptor bound a kept device")
	}
	if errs := ValidateDeviceStatusUpdate(preemptible(req, req), old); len(errs) != 0 {
		t.Errorf("the preemptor could not bind: %v", errs)
	}
}

func TestBoundAtDoesNotChangeTheHolder(t *testing.T) {
	old, d := device(ref("c1")), device(ref("c1"))
	now := metav1.Now()
	d.Status.ClaimRef.BoundAt = &now
	if errs := ValidateDeviceStatusUpdate(d, old); len(errs) != 0 {
		t.Errorf("a new bind time counted as a new holder: %v", errs)
	}
}

//= spec/solas.md#8-5-reclaim-policy
//= type=test
//# A `Device` MAY set a reclaim policy in `spec.reclaimPolicy`: `Delete`,
//# `Delay`, or `Retain`, ADR 0016.

func TestReclaimPolicy(t *testing.T) {
	r := func(n int32) *int32 { return &n }
	cases := []struct {
		policy solas.ReclaimPolicy
		delay  *int32
		ok     bool
	}{
		{"", nil, true},
		{solas.ReclaimDelete, nil, true},
		{solas.ReclaimRetain, nil, true},
		{solas.ReclaimDelay, r(300), true},
		{solas.ReclaimDelay, r(0), true},
		{solas.ReclaimDelay, nil, false},
		{solas.ReclaimDelay, r(-1), false},
		{solas.ReclaimRetain, r(300), false},
		{solas.ReclaimDelete, r(300), false},
		{"Keep", nil, false},
	}
	for _, c := range cases {
		d := device(nil)
		d.Spec.ReclaimPolicy, d.Spec.ReclaimDelaySeconds = c.policy, c.delay
		if errs := ValidateDevice(d); (len(errs) == 0) != c.ok {
			t.Errorf("policy %q delay %v: errors %v, want ok %v", c.policy, c.delay, errs, c.ok)
		}
	}
}

//= spec/solas.md#10-10-protected-holders
//= type=test
//# The server MUST reject a preemption request on a device whose holder is
//# protected.

func TestNoRequestOnAProtectedHolder(t *testing.T) {
	holder := ref("c1")
	holder.Protected = true
	old := device(holder)
	old.Spec.Preemptible = true
	d := old.DeepCopy()
	asker := ref("c2")
	asker.Priority = 9
	d.Status.Preemption = &solas.PreemptionRequest{Claim: *asker}
	if errs := ValidateDeviceStatusUpdate(d, old); len(errs) == 0 {
		t.Error("a preemption request on a protected holder passed")
	}
}
