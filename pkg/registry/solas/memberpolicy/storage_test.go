package memberpolicy_test

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/phoban01/solas/internal/teststore"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/registry/solas/memberpolicy"
)

//= spec/solas.md#15-1-policy
//= type=test
//# A `MemberPolicy` MUST have the name of the member that it limits.

func TestMemberPolicy(t *testing.T) { teststore.Run(t, testMemberPolicy) }

func testMemberPolicy(t *testing.T, g apiserver.RESTOptionsGetter) {
	r, err := memberpolicy.NewREST(apiserver.Scheme, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Store.DestroyFunc)
	ctx := genericapirequest.WithNamespace(context.Background(), "")
	four := int32(4)
	if _, err := r.Create(ctx, &solas.MemberPolicy{ObjectMeta: metav1.ObjectMeta{Name: "a"},
		Spec: solas.MemberPolicySpec{MaxDevices: &four, AllowProtected: true}}, rest.ValidateAllObjectFunc, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "a", &metav1.GetOptions{})
	if err != nil || *got.(*solas.MemberPolicy).Spec.MaxDevices != 4 || !got.(*solas.MemberPolicy).Spec.AllowProtected {
		t.Fatalf("get = %+v, %v; want maxDevices 4 and allowProtected", got, err)
	}
	bad := int32(-1)
	_, err = r.Create(ctx, &solas.MemberPolicy{ObjectMeta: metav1.ObjectMeta{Name: "b"},
		Spec: solas.MemberPolicySpec{BindsPerMinute: &bad}}, rest.ValidateAllObjectFunc, &metav1.CreateOptions{})
	if !apierrors.IsInvalid(err) {
		t.Errorf("create with a negative rate: err = %v, want Invalid", err)
	}
}
