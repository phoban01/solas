package audit_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/internal/teststore"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/audit"
)

// TestFollowSeesEveryChange runs Follow on each store while devices are
// created, bound, and released below the registry, and checks that the
// summary counts them and finds no violation.
func TestFollowSeesEveryChange(t *testing.T) { teststore.Run(t, testFollow) }

func testFollow(t *testing.T, g apiserver.RESTOptionsGetter) {
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
	ctx := context.Background()
	key := opts.ResourcePrefix + "/d1"
	if err := raw.Create(ctx, key, &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1"}}, &solas.Device{}, 0); err != nil {
		t.Fatal(err)
	}

	r := audit.New()
	fctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.Follow(fctx, raw, opts.ResourcePrefix) }()
	// The follower first takes the current state; the changes below must
	// come after it, or they fold into the first state and count nothing.
	deadline := time.Now().Add(10 * time.Second)
	for r.Snapshot().Events == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r.Snapshot().Events == 0 {
		t.Fatal("the follower did not see the current state within 10s")
	}

	ref := &solas.ClaimRef{Member: "a", MemberUID: types.UID("ua"), Namespace: "ns", Name: "c1", UID: types.UID("u1")}
	set := func(ref *solas.ClaimRef, token int64) {
		t.Helper()
		err := raw.GuaranteedUpdate(ctx, key, &solas.Device{}, false, nil,
			func(in runtime.Object, _ storage.ResponseMeta) (runtime.Object, *uint64, error) {
				d := in.(*solas.Device).DeepCopy()
				d.Status.ClaimRef = ref
				d.Status.FencingToken = token
				return d, nil, nil
			}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	set(ref, 1)
	set(nil, 1)
	set(ref, 2)

	deadline = time.Now().Add(10 * time.Second)
	for r.Snapshot().Binds < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if s.Binds != 2 || s.Releases != 1 || len(s.Violations) != 0 {
		t.Errorf("summary = binds %d, releases %d, violations %v; want 2, 1, none", s.Binds, s.Releases, s.Violations)
	}
	if h, ok := r.Holder("d1", 2); !ok || h != "a/ns/c1" {
		t.Errorf("Holder(d1, 2) = %q, %v", h, ok)
	}
}
