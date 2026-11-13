package apiserver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/storagebackend/factory"
)

// okStore answers Get and nothing else.
type okStore struct{ storage.Interface }

func (okStore) Get(context.Context, string, storage.GetOptions, runtime.Object) error { return nil }

//= spec/solas.md#16-1-the-server
//= type=test
//# The server MUST start when the store does not answer.

// TestLazyStore builds a store that fails its first builds, as an etcd
// store does while etcd is down. A call before the build fails with
// errNotBuilt when its context ends; the build is tried again until it
// works; a call after it reaches the store; and close destroys it.
func TestLazyStore(t *testing.T) {
	var tries atomic.Int32
	var destroyed atomic.Bool
	release := make(chan struct{})
	l, destroy := newLazy(func() (storage.Interface, factory.DestroyFunc, error) {
		if tries.Add(1) < 2 {
			return nil, nil, errors.New("etcd grpc connection not ready")
		}
		<-release
		return okStore{}, func() { destroyed.Store(true) }, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := l.Get(ctx, "/k", storage.GetOptions{}, nil); !errors.Is(err, errNotBuilt) {
		t.Fatalf("Get before the build = %v, want errNotBuilt", err)
	}
	if l.CompactRevision() != 0 || l.ReadinessCheck() == nil {
		t.Error("a store that is not built must have revision 0 and not be ready")
	}

	close(release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*lazyRetry+time.Second)
	defer cancel2()
	if err := l.Get(ctx2, "/k", storage.GetOptions{}, nil); err != nil {
		t.Fatalf("Get after the build = %v, want nil", err)
	}
	if n := tries.Load(); n != 2 {
		t.Errorf("builds = %d, want 2", n)
	}
	destroy()
	if !destroyed.Load() {
		t.Error("close did not destroy the built store")
	}
}
