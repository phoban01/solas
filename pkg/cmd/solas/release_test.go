package solas

import (
	"bytes"
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/phoban01/solas/internal/fakekube"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

//= spec/solas.md#8-5-reclaim-policy
//= type=test
//# The release MUST fail while the holder's member UID is live.

func TestRelease(t *testing.T) {
	ctx := context.Background()
	m := &solasv1alpha1.Member{ObjectMeta: metav1.ObjectMeta{Name: "a", UID: "ua"}}
	d := &solasv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1"}}
	d.Spec.ReclaimPolicy = solasv1alpha1.ReclaimRetain
	d.Status.ClaimRef = &solasv1alpha1.ClaimRef{Member: "a", MemberUID: "ua", Namespace: "ns", Name: "c1", UID: "u1"}
	d.Status.FencingToken = 1
	c := fakekube.NewClient(m, d)

	var out bytes.Buffer
	if err := release(ctx, c, "d1", &out); err == nil {
		t.Fatal("release while the holder's member is live passed")
	}
	if err := c.Delete(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := release(ctx, c, "d1", &out); err != nil {
		t.Fatalf("release after the member is gone: %v\n%s", err, out.String())
	}
	var got solasv1alpha1.Device
	if err := c.Get(ctx, client.ObjectKey{Name: "d1"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ClaimRef != nil || got.Status.FencingToken != 1 {
		t.Errorf("device after the release = %+v, want free with token 1", got.Status)
	}
	if got.Status.LastRelease == nil || got.Status.LastRelease.Claim.Name != "c1" {
		t.Errorf("release record = %+v, want one that names c1", got.Status.LastRelease)
	}
}
