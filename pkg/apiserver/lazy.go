package apiserver

import (
	"context"
	"errors"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/storagebackend/factory"
	"k8s.io/klog/v2"
)

// lazyWait bounds how long a call waits for the store to be built: as
// long as a call of the DynamoDB store waits for an answer, spec 16.1.
const lazyWait = 10 * time.Second

// lazyRetry is the wait between two tries to build a store that does not
// answer.
const lazyRetry = 2 * time.Second

// errNotBuilt is the error of a call before the store exists. It is not
// an API error, so the storage guard turns it into 503
// ServiceUnavailable, spec 16.1.
var errNotBuilt = errors.New("the shared store has not answered since the server started")

//= spec/solas.md#16-1-the-server
//# The server MUST start when the store does not answer.

// lazy is a store that a constructor builds in the background, so the
// server starts, serves discovery, and reports ready when the store does
// not answer. The etcd3 storage of k8s.io/apiserver waits at start until
// it reaches etcd and fails after 20 seconds; this wrapper calls it again
// until it succeeds. Until then every call fails with errNotBuilt.
type lazy struct {
	mu      sync.RWMutex
	inner   storage.Interface
	destroy factory.DestroyFunc
	keys    storage.KeysFunc
	cancel  context.CancelFunc
	// built is closed once the store exists.
	built chan struct{}
}

// newLazy starts building the store with build, and returns at once.
func newLazy(build func() (storage.Interface, factory.DestroyFunc, error)) (*lazy, factory.DestroyFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &lazy{cancel: cancel, built: make(chan struct{})}
	go func() {
		for {
			s, destroy, err := build()
			if err == nil {
				l.mu.Lock()
				if ctx.Err() != nil {
					l.mu.Unlock()
					destroy()
					return
				}
				l.inner, l.destroy = s, destroy
				close(l.built)
				keys := l.keys
				l.mu.Unlock()
				if keys != nil {
					_ = s.EnableResourceSizeEstimation(keys)
				}
				return
			}
			klog.InfoS("waiting for the store", "err", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(lazyRetry):
			}
		}
	}()
	return l, l.close
}

// close stops the build, or destroys the store once built.
func (l *lazy) close() {
	l.cancel()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.destroy != nil {
		l.destroy()
		l.destroy = nil
	}
}

// wait returns the store once it is built, or errNotBuilt when ctx ends
// or lazyWait passes first. With the store up the build takes
// milliseconds, so a call at start waits for it instead of failing.
func (l *lazy) wait(ctx context.Context) (storage.Interface, error) {
	t := time.NewTimer(lazyWait)
	defer t.Stop()
	select {
	case <-l.built:
		return l.get()
	case <-ctx.Done():
	case <-t.C:
	}
	return l.get()
}

// get returns the store, or errNotBuilt with no wait.
func (l *lazy) get() (storage.Interface, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.inner == nil {
		return nil, errNotBuilt
	}
	return l.inner, nil
}

// Versioner is the one of every store of the server, built or not.
func (l *lazy) Versioner() storage.Versioner { return storage.APIObjectVersioner{} }

func (l *lazy) Create(ctx context.Context, key string, obj, out runtime.Object, ttl uint64) error {
	s, err := l.wait(ctx)
	if err != nil {
		return err
	}
	return s.Create(ctx, key, obj, out, ttl)
}

func (l *lazy) Delete(ctx context.Context, key string, out runtime.Object, preconditions *storage.Preconditions,
	validateDeletion storage.ValidateObjectFunc, cachedExistingObject runtime.Object, opts storage.DeleteOptions) error {
	s, err := l.wait(ctx)
	if err != nil {
		return err
	}
	return s.Delete(ctx, key, out, preconditions, validateDeletion, cachedExistingObject, opts)
}

func (l *lazy) Watch(ctx context.Context, key string, opts storage.ListOptions) (watch.Interface, error) {
	s, err := l.wait(ctx)
	if err != nil {
		return nil, err
	}
	return s.Watch(ctx, key, opts)
}

func (l *lazy) Get(ctx context.Context, key string, opts storage.GetOptions, objPtr runtime.Object) error {
	s, err := l.wait(ctx)
	if err != nil {
		return err
	}
	return s.Get(ctx, key, opts, objPtr)
}

func (l *lazy) GetList(ctx context.Context, key string, opts storage.ListOptions, listObj runtime.Object) error {
	s, err := l.wait(ctx)
	if err != nil {
		return err
	}
	return s.GetList(ctx, key, opts, listObj)
}

func (l *lazy) GuaranteedUpdate(ctx context.Context, key string, destination runtime.Object, ignoreNotFound bool,
	preconditions *storage.Preconditions, tryUpdate storage.UpdateFunc, cachedExistingObject runtime.Object) error {
	s, err := l.wait(ctx)
	if err != nil {
		return err
	}
	return s.GuaranteedUpdate(ctx, key, destination, ignoreNotFound, preconditions, tryUpdate, cachedExistingObject)
}

func (l *lazy) Stats(ctx context.Context) (storage.Stats, error) {
	s, err := l.wait(ctx)
	if err != nil {
		return storage.Stats{}, err
	}
	return s.Stats(ctx)
}

func (l *lazy) ReadinessCheck() error {
	s, err := l.get()
	if err != nil {
		return err
	}
	return s.ReadinessCheck()
}

func (l *lazy) RequestWatchProgress(ctx context.Context) error {
	s, err := l.wait(ctx)
	if err != nil {
		return err
	}
	return s.RequestWatchProgress(ctx)
}

func (l *lazy) GetCurrentResourceVersion(ctx context.Context) (uint64, error) {
	s, err := l.wait(ctx)
	if err != nil {
		return 0, err
	}
	return s.GetCurrentResourceVersion(ctx)
}

// EnableResourceSizeEstimation passes keys to the store, now or once it
// is built.
func (l *lazy) EnableResourceSizeEstimation(keys storage.KeysFunc) error {
	l.mu.Lock()
	inner := l.inner
	if inner == nil {
		l.keys = keys
	}
	l.mu.Unlock()
	if inner != nil {
		return inner.EnableResourceSizeEstimation(keys)
	}
	return nil
}

// CompactRevision is 0 until the store is built: nothing is compacted
// that the server has seen.
func (l *lazy) CompactRevision() int64 {
	s, err := l.get()
	if err != nil {
		return 0
	}
	return s.CompactRevision()
}
