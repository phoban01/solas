package guard

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/format"
)

// fakeStore is a store whose every call fails with err, after it has run
// the callback of the write, as a store that does not answer would.
type fakeStore struct {
	storage.Interface
	err error
}

func (f *fakeStore) Create(context.Context, string, runtime.Object, runtime.Object, uint64) error {
	return f.err
}

func (f *fakeStore) GuaranteedUpdate(_ context.Context, _ string, _ runtime.Object, _ bool,
	_ *storage.Preconditions, tryUpdate storage.UpdateFunc, _ runtime.Object) error {
	if _, _, err := tryUpdate(&solas.Device{}, storage.ResponseMeta{}); err != nil {
		return err
	}
	return f.err
}

func (f *fakeStore) Delete(ctx context.Context, _ string, _ runtime.Object, _ *storage.Preconditions,
	validate storage.ValidateObjectFunc, _ runtime.Object, _ storage.DeleteOptions) error {
	if err := validate(ctx, &solas.Device{}); err != nil {
		return err
	}
	return f.err
}

func (f *fakeStore) Get(context.Context, string, storage.GetOptions, runtime.Object) error {
	return f.err
}

func (f *fakeStore) GetList(context.Context, string, storage.ListOptions, runtime.Object) error {
	return f.err
}

// writes runs each kind of write on s and returns its error, by name.
func writes(s storage.Interface) map[string]error {
	ctx := context.Background()
	keep := func(in runtime.Object, _ storage.ResponseMeta) (runtime.Object, *uint64, error) { return in, nil, nil }
	return map[string]error{
		"create": s.Create(ctx, "/k", &solas.Device{}, &solas.Device{}, 0),
		"update": s.GuaranteedUpdate(ctx, "/k", &solas.Device{}, false, nil, keep, nil),
		"delete": s.Delete(ctx, "/k", &solas.Device{}, nil, storage.ValidateAllObjectFunc, nil, storage.DeleteOptions{}),
	}
}

//= spec/solas.md#16-1-the-server
//= type=test
//# Until the server has read the finalized format and the epoch of the
//# store, it MUST reject every write with `503 ServiceUnavailable`.

// TestWritesWaitForTheFormat: after Forget, every write fails with a 503
// before it reaches the store, and reads pass. SetFinalized ends it.
func TestWritesWaitForTheFormat(t *testing.T) {
	format.Forget()
	t.Cleanup(func() { format.SetFinalized(1) })
	s := Wrap(&fakeStore{}, nil)
	for name, err := range writes(s) {
		if !apierrors.IsServiceUnavailable(err) {
			t.Errorf("%s before the format is read: err = %v, want 503 ServiceUnavailable", name, err)
		}
	}
	if err := s.Get(context.Background(), "/k", storage.GetOptions{}, &solas.Device{}); err != nil {
		t.Errorf("get before the format is read: %v, want no error", err)
	}
	format.SetFinalized(1)
	for name, err := range writes(s) {
		if err != nil {
			t.Errorf("%s after the format is read: %v", name, err)
		}
	}
}

//= spec/solas.md#16-1-the-server
//= type=test
//# A write that fails because the server cannot reach the store MUST fail
//# with `503 ServiceUnavailable`.

// TestStoreDidNotAnswer: an error of the store that is not an API error
// and not a storage error with a meaning of its own becomes a 503, on
// writes and on reads.
func TestStoreDidNotAnswer(t *testing.T) {
	for _, err := range []error{errors.New("dial tcp 10.0.0.1:8000: connection refused"), context.DeadlineExceeded} {
		s := Wrap(&fakeStore{err: err}, nil)
		for name, got := range writes(s) {
			if !apierrors.IsServiceUnavailable(got) {
				t.Errorf("%s with store error %q: err = %v, want 503 ServiceUnavailable", name, err, got)
			}
		}
		ctx := context.Background()
		if got := s.Get(ctx, "/k", storage.GetOptions{}, &solas.Device{}); !apierrors.IsServiceUnavailable(got) {
			t.Errorf("get with store error %q: err = %v, want 503", err, got)
		}
		if got := s.GetList(ctx, "/k", storage.ListOptions{}, &solas.DeviceList{}); !apierrors.IsServiceUnavailable(got) {
			t.Errorf("list with store error %q: err = %v, want 503", err, got)
		}
	}
}

// TestStoreErrorsKeepTheirMeaning: a storage error, an API error, and a
// canceled request pass through as they are.
func TestStoreErrorsKeepTheirMeaning(t *testing.T) {
	for _, err := range []error{
		storage.NewKeyNotFoundError("/k", 0),
		storage.NewKeyExistsError("/k", 0),
		storage.NewResourceVersionConflictsError("/k", 0),
		apierrors.NewConflict(solas.Resource("devices"), "d1", errors.New("stale")),
		apierrors.NewServiceUnavailable("store is sealed"),
		context.Canceled,
	} {
		s := Wrap(&fakeStore{err: err}, nil)
		for name, got := range writes(s) {
			if !errors.Is(got, err) {
				t.Errorf("%s with store error %q: err = %v, want the same error", name, err, got)
			}
		}
	}
}

// TestCallerErrorsPassThrough: an error of the registry's callback, and a
// rejection by the guard, come back as they were, not as a 503.
func TestCallerErrorsPassThrough(t *testing.T) {
	ctx := context.Background()
	s := Wrap(&fakeStore{}, nil)
	boom := errors.New("boom")
	fail := func(runtime.Object, storage.ResponseMeta) (runtime.Object, *uint64, error) { return nil, nil, boom }
	if err := s.GuaranteedUpdate(ctx, "/k", &solas.Device{}, false, nil, fail, nil); err != boom {
		t.Errorf("update whose callback fails: err = %v, want the callback's error", err)
	}
	invalid := apierrors.NewBadRequest("no")
	reject := func(context.Context, runtime.Object) error { return invalid }
	if err := s.Delete(ctx, "/k", &solas.Device{}, nil, reject, nil, storage.DeleteOptions{}); err != invalid {
		t.Errorf("delete whose validation fails: err = %v, want the validation error", err)
	}
	checked := Wrap(&fakeStore{}, func(_, _ runtime.Object) error { return errors.New("held") })
	if err := checked.Create(ctx, "/k", &solas.Device{}, &solas.Device{}, 0); !IsRejected(err) {
		t.Errorf("create that the transition check rejects: err = %v, want a guard rejection", err)
	}
}
