package apiserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/phoban01/solas/internal/teststore"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/registry/solas/device"
)

//= spec/solas.md#5-1-resource
//= type=test
//# The server MUST store `spec.parameters` as written, and MUST reject it
//# when it is larger than 64 KiB.

// TestParameters runs on each store: nested JSON that solas does not know
// survives an update of another field, and a too large value fails.
func TestParameters(t *testing.T) { teststore.Run(t, testParameters) }

func testParameters(t *testing.T, g apiserver.RESTOptionsGetter) {
	r, _, err := device.NewREST(apiserver.Scheme, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Store.DestroyFunc)
	ctx := genericapirequest.WithNamespace(context.Background(), "")
	params := []byte(`{"bmc":{"address":"10.0.0.7","creds":{"secretRef":"bmc-7"}},"nics":[{"mac":"aa:bb","speed":400},{"mac":"cc:dd"}]}`)
	obj, err := r.Create(ctx, &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1"},
		Spec: solas.DeviceSpec{Parameters: &runtime.RawExtension{Raw: params}}}, rest.ValidateAllObjectFunc, &metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d := obj.(*solas.Device).DeepCopy()
	d.Spec.Description = "updated"
	obj, _, err = r.Update(ctx, "d1", rest.DefaultUpdatedObjectInfo(d), rest.ValidateAllObjectFunc, rest.ValidateAllObjectUpdateFunc, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "d1", &metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var want, have any
	_ = json.Unmarshal(params, &want)
	if err := json.Unmarshal(got.(*solas.Device).Spec.Parameters.Raw, &have); err != nil {
		t.Fatal(err)
	}
	if w, h := mustJSON(want), mustJSON(have); !bytes.Equal(w, h) {
		t.Errorf("parameters after an update = %s, want %s", h, w)
	}

	big := append([]byte(`{"x":"`), bytes.Repeat([]byte("a"), 70*1024)...)
	big = append(big, []byte(`"}`)...)
	_, err = r.Create(ctx, &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d2"},
		Spec: solas.DeviceSpec{Parameters: &runtime.RawExtension{Raw: big}}}, rest.ValidateAllObjectFunc, &metav1.CreateOptions{})
	if !apierrors.IsInvalid(err) {
		t.Errorf("create with 70 KiB of parameters: err = %v, want Invalid", err)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
