package apiserver_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	k8sstorage "k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/internal/teststore"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/registry/solas/device"
	"github.com/phoban01/solas/pkg/storage/guard"
)

//= spec/solas.md#5-3-status-updates
//= type=test
//# The server MUST reject a status update that changes `claimRef` from one
//# holder to a different holder.

//= spec/solas.md#5-3-status-updates
//= type=test
//# When a status update sets `claimRef` on a free device, the server MUST
//# set `status.fencingToken` to the old value plus 1.

// TestStorageGuard writes to the store below the registry, as a server of
// another release might, on each store. A take of a held device and a
// bind without a token raise fail, and leave the device as it was.
func TestStorageGuard(t *testing.T) { teststore.Run(t, testStorageGuard) }

func testStorageGuard(t *testing.T, g apiserver.RESTOptionsGetter) {
	r, status, err := device.NewREST(apiserver.Scheme, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Store.DestroyFunc)
	ctx := genericapirequest.WithNamespace(context.Background(), "")
	raw := r.Store.Storage.Storage
	key := "/solas.dev/devices/d1"
	ref := func(claim string) *solas.ClaimRef {
		return &solas.ClaimRef{Member: "a", MemberUID: "mu", Namespace: "ns", Name: claim, UID: types.UID("u-" + claim)}
	}

	obj, err := r.Create(ctx, &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1"}}, rest.ValidateAllObjectFunc, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d := obj.(*solas.Device).DeepCopy()
	d.Status.ClaimRef = ref("c1")
	if _, _, err := status.Update(ctx, "d1", rest.DefaultUpdatedObjectInfo(d), rest.ValidateAllObjectFunc,
		rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	write := func(change func(*solas.Device)) error {
		return raw.GuaranteedUpdate(ctx, key, &solas.Device{}, false, nil,
			func(in runtime.Object, _ k8sstorage.ResponseMeta) (runtime.Object, *uint64, error) {
				c := in.(*solas.Device).DeepCopy()
				change(c)
				return c, nil, nil
			}, nil)
	}
	stored := func() *solas.Device {
		var got solas.Device
		if err := raw.Get(ctx, key, k8sstorage.GetOptions{}, &got); err != nil {
			t.Fatal(err)
		}
		return &got
	}
	before := stored()

	if err := write(func(c *solas.Device) { c.Status.ClaimRef = ref("c2") }); !guard.IsRejected(err) {
		t.Errorf("take of a held device: err = %v, want a guard rejection", err)
	}
	if err := write(func(c *solas.Device) { c.Status.FencingToken = 9 }); !guard.IsRejected(err) {
		t.Errorf("token change with no bind: err = %v, want a guard rejection", err)
	}
	if got := stored(); got.ResourceVersion != before.ResourceVersion || got.Status.ClaimRef.Name != "c1" || got.Status.FencingToken != 1 {
		t.Fatalf("after rejected writes: %+v, want the device unchanged", got.Status)
	}

	if err := write(func(c *solas.Device) { c.Status.ClaimRef = nil }); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := write(func(c *solas.Device) { c.Status.ClaimRef = ref("c2") }); !guard.IsRejected(err) {
		t.Errorf("bind with no token raise: err = %v, want a guard rejection", err)
	}
	if got := stored(); got.Status.ClaimRef != nil || got.Status.FencingToken != 1 {
		t.Fatalf("after a rejected bind: %+v, want the device free with token 1", got.Status)
	}
	if err := write(func(c *solas.Device) { c.Status.ClaimRef = ref("c2"); c.Status.FencingToken = 2 }); err != nil {
		t.Errorf("bind with the token raised: %v", err)
	}

	held := &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d2"}}
	held.Status.ClaimRef = ref("c3")
	if err := raw.Create(ctx, "/solas.dev/devices/d2", held, &solas.Device{}, 0); !guard.IsRejected(err) {
		t.Errorf("create of a held device: err = %v, want a guard rejection", err)
	}
}
