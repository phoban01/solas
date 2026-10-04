package apiserver_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	k8sstorage "k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/internal/teststore"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/registry/solas/device"
	"github.com/phoban01/solas/pkg/storage/guard"
)

//= spec/solas.md#11-1-format-version
//= type=test
//# Each object MUST record the format version of its last write in the
//# annotation `solas.dev/format`.

//= spec/solas.md#11-3-newer-objects
//= type=test
//# A server MUST reject a write to an object whose format is newer than its
//# own maximum.

//= spec/solas.md#11-3-newer-objects
//= type=test
//# A server MAY read a newer object, on a best effort basis.

// TestFormat runs on each store. Each write records format 1. An object in
// a newer format can be read, but not updated or deleted, and it stays as
// it was.
func TestFormat(t *testing.T) { teststore.Run(t, testFormat) }

func testFormat(t *testing.T, g apiserver.RESTOptionsGetter) {
	r, _, err := device.NewREST(apiserver.Scheme, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Store.DestroyFunc)
	ctx := genericapirequest.WithNamespace(context.Background(), "")

	obj, err := r.Create(ctx, &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1"}}, rest.ValidateAllObjectFunc, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if f := obj.(*solas.Device).Annotations[guard.FormatAnnotation]; f != "1" {
		t.Errorf("create recorded format %q, want 1", f)
	}
	d := obj.(*solas.Device).DeepCopy()
	d.Spec.Description = "updated"
	d.Annotations[guard.FormatAnnotation] = "7"
	obj, _, err = r.Update(ctx, "d1", rest.DefaultUpdatedObjectInfo(d), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if f := obj.(*solas.Device).Annotations[guard.FormatAnnotation]; f != "1" {
		t.Errorf("update recorded format %q, want 1: the server writes its own format", f)
	}

	// A server of a newer release wrote d2 in format 2.
	opts, err := g.GetRESTOptions(solas.Resource("devices"), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, destroy, err := g.RawStorage(opts.StorageConfig, opts.ResourcePrefix,
		func() runtime.Object { return &solas.Device{} }, func() runtime.Object { return &solas.DeviceList{} })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(destroy)
	newer := &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d2", Annotations: map[string]string{guard.FormatAnnotation: "2"}}}
	newer.Spec.Description = "from release 2"
	key := opts.ResourcePrefix + "/d2"
	if err := raw.Create(ctx, key, newer, &solas.Device{}, 0); err != nil {
		t.Fatal(err)
	}
	var before solas.Device
	if err := raw.Get(ctx, key, k8sstorage.GetOptions{}, &before); err != nil {
		t.Fatal(err)
	}

	got, err := r.Get(ctx, "d2", &metav1.GetOptions{})
	if err != nil || got.(*solas.Device).Spec.Description != "from release 2" {
		t.Fatalf("read of a newer object = %v, %v; want it read", got, err)
	}
	upd := got.(*solas.Device).DeepCopy()
	upd.Spec.Description = "from release 1"
	if _, _, err := r.Update(ctx, "d2", rest.DefaultUpdatedObjectInfo(upd), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{}); !guard.IsRejected(err) {
		t.Errorf("update of a newer object: err = %v, want a guard rejection", err)
	}
	if _, _, err := r.Delete(ctx, "d2", rest.ValidateAllObjectFunc, &metav1.DeleteOptions{}); !guard.IsRejected(err) {
		t.Errorf("delete of a newer object: err = %v, want a guard rejection", err)
	}
	var after solas.Device
	if err := raw.Get(ctx, key, k8sstorage.GetOptions{}, &after); err != nil {
		t.Fatal(err)
	}
	if after.ResourceVersion != before.ResourceVersion || after.Spec.Description != "from release 2" {
		t.Errorf("newer object after rejected writes = %+v, want it unchanged", after)
	}
}
