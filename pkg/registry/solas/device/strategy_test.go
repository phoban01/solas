package device

import (
	"context"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/format"
)

var ref1 = &solas.ClaimRef{Member: "a", MemberUID: "mu", Namespace: "ns", Name: "c1", UID: "u1"}

func dev(desc string, ref *solas.ClaimRef) *solas.Device {
	return &solas.Device{
		ObjectMeta: metav1.ObjectMeta{Name: "d1", ResourceVersion: "2", Labels: map[string]string{"k": desc}},
		Spec:       solas.DeviceSpec{Description: desc},
		Status:     solas.DeviceStatus{ClaimRef: ref},
	}
}

func TestStrategiesAreConditional(t *testing.T) {
	s := NewStrategy(runtime.NewScheme())
	if s.AllowUnconditionalUpdate(context.Background()) || NewStatusStrategy(s).AllowUnconditionalUpdate(context.Background()) {
		t.Error("device updates must carry a resource version")
	}
	if s.NamespaceScoped() {
		t.Error("Device must be cluster-scoped")
	}
}

func TestCreateStartsFree(t *testing.T) {
	d := dev("x", ref1)
	NewStrategy(runtime.NewScheme()).PrepareForCreate(context.Background(), d)
	if d.Status.ClaimRef != nil {
		t.Error("a new device must have no claimRef")
	}
}

//= spec/solas.md#5-3-status-updates
//= type=test
//# A spec update MUST NOT change `status`.

func TestSpecUpdateKeepsStatus(t *testing.T) {
	old, d := dev("old", ref1), dev("new", nil)
	NewStrategy(runtime.NewScheme()).PrepareForUpdate(context.Background(), d, old)
	if d.Status.ClaimRef == nil || d.Spec.Description != "new" {
		t.Errorf("after spec update: spec %q, claimRef %v", d.Spec.Description, d.Status.ClaimRef)
	}
}

//= spec/solas.md#5-3-status-updates
//= type=test
//# A status update MUST NOT change `spec`.

func TestStatusUpdateKeepsSpec(t *testing.T) {
	old, d := dev("old", nil), dev("new", ref1)
	NewStatusStrategy(NewStrategy(runtime.NewScheme())).PrepareForUpdate(context.Background(), d, old)
	if d.Spec.Description != "old" || d.Labels["k"] != "old" || d.Status.ClaimRef == nil {
		t.Errorf("after status update: spec %q, label %q, claimRef %v", d.Spec.Description, d.Labels["k"], d.Status.ClaimRef)
	}
}

func TestStatusUpdateRejectsNewHolder(t *testing.T) {
	ss := NewStatusStrategy(NewStrategy(runtime.NewScheme()))
	other := *ref1
	other.Name, other.UID = "c2", "u2"
	if errs := ss.ValidateUpdate(context.Background(), dev("x", &other), dev("x", ref1)); len(errs) == 0 {
		t.Error("status strategy accepted a change of holder")
	}
	if errs := ss.ValidateUpdate(context.Background(), dev("x", ref1), dev("x", nil)); len(errs) != 0 {
		t.Errorf("status strategy rejected a bind of a free device: %v", errs)
	}
}

//= spec/solas.md#5-3-status-updates
//= type=test
//# When a status update sets `claimRef` on a free device, the server MUST
//# set `status.fencingToken` to the old value plus 1.

//= spec/solas.md#5-3-status-updates
//= type=test
//# The server MUST ignore the `status.fencingToken` that a client sends.

func TestFencingToken(t *testing.T) {
	ss := NewStatusStrategy(NewStrategy(runtime.NewScheme()))
	ctx := context.Background()
	update := func(old *solas.Device, ref *solas.ClaimRef, sent int64) *solas.Device {
		d := old.DeepCopy()
		d.Status.ClaimRef = ref
		d.Status.FencingToken = sent
		ss.PrepareForUpdate(ctx, d, old)
		return d
	}
	free := dev("x", nil)
	bound := update(free, ref1, 99)
	if bound.Status.FencingToken != 1 {
		t.Fatalf("first bind: token %d, want 1", bound.Status.FencingToken)
	}
	if same := update(bound, ref1, 0); same.Status.FencingToken != 1 {
		t.Errorf("update with the same holder: token %d, want 1", same.Status.FencingToken)
	}
	released := update(bound, nil, 0)
	if released.Status.FencingToken != 1 {
		t.Errorf("release: token %d, want 1", released.Status.FencingToken)
	}
	if again := update(released, ref1, 0); again.Status.FencingToken != 2 {
		t.Errorf("second bind: token %d, want 2", again.Status.FencingToken)
	}
}

//= spec/solas.md#10-7-bind-by-the-preemptor
//= type=test
//# The bind of the requesting claim MUST clear `status.preemption`.

func TestPreemptorBindClearsTheRequest(t *testing.T) {
	ss := NewStatusStrategy(NewStrategy(runtime.NewScheme()))
	req := *ref1
	req.Name, req.UID, req.Priority = "c2", "u2", 5
	old := dev("x", nil)
	old.Spec.Preemptible = true
	old.Status.FencingToken = 3
	old.Status.Preemption = &solas.PreemptionRequest{Claim: req}
	d := old.DeepCopy()
	d.Status.ClaimRef = &req
	ss.PrepareForUpdate(context.Background(), d, old)
	if d.Status.Preemption != nil || d.Status.FencingToken != 4 {
		t.Errorf("after the preemptor's bind: preemption %+v, token %d; want none and 4", d.Status.Preemption, d.Status.FencingToken)
	}
	if errs := ss.ValidateUpdate(context.Background(), d, old); len(errs) != 0 {
		t.Errorf("validate: %v", errs)
	}
}

//= spec/solas.md#8-5-reclaim-policy
//= type=test
//# The release MUST record who released the device.

func TestClearRecordsTheRelease(t *testing.T) {
	st := NewStatusStrategy(NewStrategy(nil))
	holder := &solas.ClaimRef{Member: "a", MemberUID: "mu", Namespace: "ns", Name: "c1", UID: "u1"}
	ctx := genericapirequest.WithUser(context.Background(), &user.DefaultInfo{Name: "alice"})

	old := dev("d", holder)
	cleared := dev("d", nil)
	st.PrepareForUpdate(ctx, cleared, old)
	r := cleared.Status.LastRelease
	if r == nil || r.By != "alice" || r.Claim.Name != "c1" || r.At.IsZero() {
		t.Fatalf("release record after a clear = %+v, want alice and c1", r)
	}

	// Another update keeps the record, whatever the client sends.
	next := cleared.DeepCopy()
	next.Status.LastRelease = &solas.Release{By: "mallory"}
	st.PrepareForUpdate(ctx, next, cleared)
	if next.Status.LastRelease == nil || next.Status.LastRelease.By != "alice" {
		t.Errorf("record after a later update = %+v, want the server's record", next.Status.LastRelease)
	}
}

//= spec/solas.md#13-2-fencing-tokens
//= type=test
//# When a status update sets `claimRef` on a free device whose token is
//# below `epoch * 2^32`, the server MUST set the token to `epoch * 2^32 + 1`.

// TestBindInANewEpoch binds a device with token 7 in epoch 3, in the
// strategy and in the storage guard.
func TestBindInANewEpoch(t *testing.T) {
	format.SetEpoch(3)
	t.Cleanup(func() { format.SetEpoch(0) })
	want := int64(3)<<32 + 1

	free := dev("d", nil)
	free.Status.FencingToken = 7
	bound := dev("d", &solas.ClaimRef{Member: "a", MemberUID: "mu", Namespace: "ns", Name: "c1", UID: "u1"})
	NewStatusStrategy(NewStrategy(nil)).PrepareForUpdate(context.Background(), bound, free)
	if bound.Status.FencingToken != want {
		t.Errorf("strategy: token %d, want %d", bound.Status.FencingToken, want)
	}
	if err := Transition(free, bound); err != nil {
		t.Errorf("guard rejected the epoch token: %v", err)
	}
	old := bound.DeepCopy()
	old.Status.FencingToken = 8
	if err := Transition(free, old); err == nil {
		t.Error("guard accepted token 8 in epoch 3")
	}
}
