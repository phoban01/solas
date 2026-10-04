package apiserver_test

import (
	"context"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/phoban01/solas/internal/teststore"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/registry/solas/device"
	"github.com/phoban01/solas/pkg/registry/solas/usage"
)

//= spec/solas.md#15-1-policy
//= type=test
//# A member MUST NOT hold more devices than its `maxDevices`.

// TestMaxDevices runs on each store: a member with maxDevices 1 binds two
// devices at once, and only one bind succeeds. After a release, it can
// bind again.
func TestMaxDevices(t *testing.T) { teststore.Run(t, testMaxDevices) }

func testMaxDevices(t *testing.T, g apiserver.RESTOptionsGetter) {
	ctx := genericapirequest.WithNamespace(context.Background(), "")
	one := int32(1)
	policies := func(_ context.Context, member string) (*solas.MemberPolicy, error) {
		if member == "a" {
			return &solas.MemberPolicy{Spec: solas.MemberPolicySpec{MaxDevices: &one}}, nil
		}
		return nil, nil
	}
	ro, err := g.GetRESTOptions(usage.Resource, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, destroy, err := g.RawStorage(ro.StorageConfig, ro.ResourcePrefix,
		func() runtime.Object { return &solas.MemberUsage{} }, func() runtime.Object { return &solas.MemberUsageList{} })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(destroy)
	r, status, err := device.NewRESTWithOptions(apiserver.Scheme, g, device.Options{Policies: policies, Usage: usage.New(raw)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Store.DestroyFunc)

	var devs []*solas.Device
	for _, name := range []string{"d1", "d2", "d3"} {
		obj, err := r.Create(ctx, &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: name}}, rest.ValidateAllObjectFunc, &metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		devs = append(devs, obj.(*solas.Device))
	}
	bind := func(d *solas.Device, claim string) error {
		d = d.DeepCopy()
		d.Status.ClaimRef = &solas.ClaimRef{Member: "a", MemberUID: "ua", Namespace: "ns", Name: claim, UID: types.UID("u-" + claim)}
		_, _, err := status.Update(ctx, d.Name, rest.DefaultUpdatedObjectInfo(d), rest.ValidateAllObjectFunc,
			rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
		return err
	}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = bind(devs[i], "c"+string(rune('1'+i)))
		}()
	}
	wg.Wait()
	ok, forbidden := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case apierrors.IsForbidden(err):
			forbidden++
		default:
			t.Fatalf("bind: %v", err)
		}
	}
	if ok != 1 || forbidden != 1 {
		t.Fatalf("two binds at once with maxDevices 1: %d passed and %d got 403, want 1 and 1", ok, forbidden)
	}

	// Release the bound device; the place is free again.
	held := devs[0]
	if errs[0] != nil {
		held = devs[1]
	}
	cur, err := r.Get(ctx, held.Name, &metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cleared := cur.(*solas.Device).DeepCopy()
	cleared.Status.ClaimRef = nil
	if _, _, err := status.Update(ctx, cleared.Name, rest.DefaultUpdatedObjectInfo(cleared), rest.ValidateAllObjectFunc,
		rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := bind(devs[2], "c3"); err != nil {
		t.Errorf("bind after a release: %v", err)
	}
}
