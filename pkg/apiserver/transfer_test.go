package apiserver_test

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/phoban01/solas/internal/teststore"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/registry/solas/device"
)

//= spec/solas.md#14-2-bind-against-the-offer
//= type=test
//# Only the claim that the offer names, with the member UID that it names,
//# MAY bind against the offer.

//= spec/solas.md#14-2-bind-against-the-offer
//= type=test
//# The bind MUST be one status update that sets `claimRef` to the named
//# claim, sets `status.fencingToken` to the next token, spec 5.3 and 13.2,
//# and clears `status.offer`.

//= spec/solas.md#14-1-offer
//= type=test
//# The server MUST reject an offer on a free device, and a second offer
//# while one stands.

//= spec/solas.md#14-1-offer
//= type=test
//# The server MUST reject an offer while a preemption request stands.

//= spec/solas.md#14-1-offer
//= type=test
//# The server MUST reject a preemption request while an offer stands.

//= spec/solas.md#14-1-offer
//= type=test
//# When a status update clears `claimRef`, the server MUST clear
//# `status.offer`.

// TestTransfer runs on each store: an offer, a bind by another claim
// during it, a withdraw, a second offer, and the bind against it.
func TestTransfer(t *testing.T) { teststore.Run(t, testTransfer) }

func testTransfer(t *testing.T, g apiserver.RESTOptionsGetter) {
	r, status, err := device.NewREST(apiserver.Scheme, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Store.DestroyFunc)
	ctx := genericapirequest.WithNamespace(context.Background(), "")
	ref := func(member, name string) *solas.ClaimRef {
		return &solas.ClaimRef{Member: member, MemberUID: types.UID("uid-" + member), Namespace: "ns", Name: name, UID: types.UID("uid-" + name)}
	}
	get := func() *solas.Device {
		t.Helper()
		obj, err := r.Get(ctx, "d1", &metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return obj.(*solas.Device).DeepCopy()
	}
	write := func(d *solas.Device) error {
		_, _, err := status.Update(ctx, d.Name, rest.DefaultUpdatedObjectInfo(d),
			rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
		return err
	}
	must := func(what string, d *solas.Device) {
		t.Helper()
		if err := write(d); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	refuse := func(what string, d *solas.Device) {
		t.Helper()
		if err := write(d); !apierrors.IsInvalid(err) {
			t.Errorf("%s: err = %v, want Invalid", what, err)
		}
	}
	if _, err := r.Create(ctx, &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1"}, Spec: solas.DeviceSpec{Preemptible: true}},
		rest.ValidateAllObjectFunc, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	holder, target, third := ref("a", "job"), ref("b", "job"), ref("c", "other")

	d := get()
	d.Status.Offer = target
	refuse("an offer on a free device", d)

	d = get()
	d.Status.ClaimRef = holder
	must("bind", d)

	d = get()
	d.Status.Preemption = &solas.PreemptionRequest{Claim: *ref("c", "urgent")}
	d.Status.Preemption.Claim.Priority = 5
	must("preemption request", d)
	d = get()
	d.Status.Offer = target
	refuse("an offer while a request stands", d)
	d = get()
	d.Status.Preemption = nil
	must("withdraw the request", d)

	d = get()
	d.Status.Offer = target
	must("offer", d)
	token := get().Status.FencingToken

	d = get()
	d.Status.Offer = third
	refuse("a second offer", d)
	d = get()
	d.Status.ClaimRef = third
	refuse("a bind by a claim that the offer does not name", d)
	d = get()
	wrongUID := *target
	wrongUID.MemberUID = "uid-b-old"
	d.Status.ClaimRef = &wrongUID
	refuse("a bind with another member UID", d)
	d = get()
	d.Status.Preemption = &solas.PreemptionRequest{Claim: *ref("c", "urgent")}
	d.Status.Preemption.Claim.Priority = 5
	refuse("a preemption request while an offer stands", d)

	d = get()
	d.Status.Offer = nil
	must("withdraw", d)
	d = get()
	d.Status.ClaimRef = target
	refuse("a bind against a withdrawn offer", d)

	d = get()
	d.Status.Offer = target
	must("offer again", d)
	d = get()
	d.Status.ClaimRef = target
	must("bind against the offer", d)
	d = get()
	if d.Status.ClaimRef.Member != "b" || d.Status.Offer != nil || d.Status.FencingToken != token+1 {
		t.Errorf("after the bind: claimRef %+v, offer %+v, token %d; want b, none, %d",
			d.Status.ClaimRef, d.Status.Offer, d.Status.FencingToken, token+1)
	}

	d.Status.Offer = holder
	must("offer back", d)
	d = get()
	d.Status.ClaimRef = nil
	must("release with an offer", d)
	if d = get(); d.Status.Offer != nil {
		t.Errorf("after the release: offer %+v, want none", d.Status.Offer)
	}
}
