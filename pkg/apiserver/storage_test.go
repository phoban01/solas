package apiserver_test

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/phoban01/solas/internal/etcdtest"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/registry/solas/device"
)

//= spec/solas.md#2-7-etcd-store
//= type=test
//# The etcd store MUST use the etcd3 storage of `k8s.io/apiserver`.

// TestEtcdRESTOptions runs the Device REST store on etcd: a create, a
// conditional update with a stale resource version, and a list.
func TestEtcdRESTOptions(t *testing.T) {
	getter := apiserver.RESTOptionsGetter{
		EtcdServers:     etcdtest.Servers(t),
		Codec:           apiserver.StorageCodec(),
		EncodeVersioner: apiserver.StorageVersioner(),
		Prefix:          etcdtest.Prefix(t),
	}
	r, _, err := device.NewREST(apiserver.Scheme, getter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Store.DestroyFunc)
	ctx := genericapirequest.WithNamespace(context.Background(), "")

	obj, err := r.Create(ctx, &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1"}},
		rest.ValidateAllObjectFunc, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d := obj.(*solas.Device)

	fresh := d.DeepCopy()
	fresh.Spec.Description = "first"
	if _, _, err := r.Update(ctx, "d1", rest.DefaultUpdatedObjectInfo(fresh), rest.ValidateAllObjectFunc,
		rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	stale := d.DeepCopy()
	stale.Spec.Description = "second"
	_, _, err = r.Update(ctx, "d1", rest.DefaultUpdatedObjectInfo(stale), rest.ValidateAllObjectFunc,
		rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	if !apierrors.IsConflict(err) {
		t.Fatalf("update with a stale resource version: err = %v, want 409 Conflict", err)
	}

	// The watch cache can still be starting. A list then fails with
	// "storage is (re)initializing", and a client retries.
	var list runtime.Object
	err = wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		list, err = r.List(ctx, &metainternalversion.ListOptions{})
		if err != nil && strings.Contains(err.Error(), "storage is (re)initializing") {
			return false, nil
		}
		return true, err
	})
	if err != nil {
		t.Fatal(err)
	}
	items := list.(*solas.DeviceList).Items
	if len(items) != 1 || items[0].Spec.Description != "first" {
		t.Fatalf("list = %+v, want d1 with description first", items)
	}
}
