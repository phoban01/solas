package usage_test

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sstorage "k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/internal/teststore"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/registry/solas/usage"
)

func raw(t *testing.T, g apiserver.RESTOptionsGetter, gr string, newFunc, newList func() runtime.Object) k8sstorage.Interface {
	t.Helper()
	ro, err := g.GetRESTOptions(solas.Resource(gr), nil)
	if err != nil {
		t.Fatal(err)
	}
	s, destroy, err := g.RawStorage(ro.StorageConfig, ro.ResourcePrefix, newFunc, newList)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(destroy)
	return s
}

//= spec/solas.md#15-3-cleanup
//= type=test
//# It MUST NOT remove such an entry before a grace that is longer than the
//# deadline of a request has passed since the entry was added, by its own
//# clock.

// TestCleanup runs on each store. Member a reserved d1 and d2, and holds
// only d1. The cleanup leaves d2 before the grace and removes it after.
func TestCleanup(t *testing.T) { teststore.Run(t, testCleanup) }

func testCleanup(t *testing.T, g apiserver.RESTOptionsGetter) {
	ctx := context.Background()
	u := usage.New(raw(t, g, "memberusages",
		func() runtime.Object { return &solas.MemberUsage{} }, func() runtime.Object { return &solas.MemberUsageList{} }))
	devices := raw(t, g, "devices",
		func() runtime.Object { return &solas.Device{} }, func() runtime.Object { return &solas.DeviceList{} })
	for _, d := range []string{"d1", "d2"} {
		if err := u.Reserve(ctx, "a", d, 5); err != nil {
			t.Fatal(err)
		}
	}
	held := &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1"}}
	held.Status.ClaimRef = &solas.ClaimRef{Member: "a", MemberUID: "ua", Namespace: "ns", Name: "c1", UID: "u1"}
	if err := devices.Create(ctx, "/solas.dev/devices/d1", held, &solas.Device{}, 0); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if removed, err := u.Cleanup(ctx, devices, "a", usage.DefaultGrace, now.Add(time.Minute)); err != nil || len(removed) != 0 {
		t.Fatalf("cleanup before the grace removed %v, %v; want nothing", removed, err)
	}
	removed, err := u.Cleanup(ctx, devices, "a", usage.DefaultGrace, now.Add(3*time.Minute))
	if err != nil || len(removed) != 1 || removed[0] != "d2" {
		t.Fatalf("cleanup after the grace removed %v, %v; want d2", removed, err)
	}
	entries, err := u.Entries(ctx, "a")
	if err != nil || len(entries) != 1 || entries[0].Device != "d1" {
		t.Errorf("entries after the cleanup = %+v, %v; want d1 only", entries, err)
	}
}

//= spec/solas.md#15-2-usage-set
//= type=test
//# A bind or a preemption request over a rate MUST fail with
//# `429 Too Many Requests`.

// TestBindRate runs on each store: with bindsPerMinute 2, a burst of three
// binds gets 429 on the third, and a token comes back after 30 seconds.
func TestBindRate(t *testing.T) { teststore.Run(t, testBindRate) }

func testBindRate(t *testing.T, g apiserver.RESTOptionsGetter) {
	ctx := context.Background()
	now := time.Unix(10000, 0)
	u := usage.New(raw(t, g, "memberusages",
		func() runtime.Object { return &solas.MemberUsage{} }, func() runtime.Object { return &solas.MemberUsageList{} })).
		WithClock(func() time.Time { return now })
	rate := int32(2)
	for i, d := range []string{"d1", "d2"} {
		if err := u.Admit(ctx, "a", d, nil, &rate); err != nil {
			t.Fatalf("bind %d: %v", i+1, err)
		}
	}
	err := u.Admit(ctx, "a", "d3", nil, &rate)
	if !apierrors.IsTooManyRequests(err) {
		t.Fatalf("third bind in a burst: err = %v, want 429", err)
	}
	if s, ok := apierrors.SuggestsClientDelay(err); !ok || s < 1 {
		t.Errorf("429 suggests a delay of %d, %v; want at least 1 second", s, ok)
	}
	now = now.Add(30 * time.Second)
	if err := u.Admit(ctx, "a", "d3", nil, &rate); err != nil {
		t.Errorf("bind after 30 seconds: %v", err)
	}
}
