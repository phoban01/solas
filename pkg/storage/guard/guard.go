// Package guard checks each write below the registry, on any store. The
// registry strategy is the first check; the guard is a second one that
// does not depend on the strategy, so servers of two releases enforce the
// same rules on the stored objects, spec 11.
package guard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/pkg/format"
)

// Transition checks a write from old to new. old is nil for a create.
type Transition func(old, new runtime.Object) error

// store wraps a storage.Interface. It stamps the format of each write,
// rejects a write or a delete of an object in a newer format, runs the
// transition check of the resource, and maps an error of a store that
// does not answer to 503 ServiceUnavailable, spec 16.1.
type store struct {
	storage.Interface
	check Transition
}

// Wrap returns s with the guard. check may be nil. Reads pass through,
// except that an error of a store that does not answer becomes a 503.
func Wrap(s storage.Interface, check Transition) storage.Interface {
	return &store{Interface: s, check: check}
}

// Get maps an error of a store that does not answer to a 503.
func (s *store) Get(ctx context.Context, key string, opts storage.GetOptions, objPtr runtime.Object) error {
	return unreachable(s.Interface.Get(ctx, key, opts, objPtr))
}

// GetList maps an error of a store that does not answer to a 503.
func (s *store) GetList(ctx context.Context, key string, opts storage.ListOptions, listObj runtime.Object) error {
	return unreachable(s.Interface.GetList(ctx, key, opts, listObj))
}

// Create checks a new object against no old object, and stamps it.
func (s *store) Create(ctx context.Context, key string, obj, out runtime.Object, ttl uint64) error {
	if err := notRead(); err != nil {
		return err
	}
	if s.check != nil {
		if err := s.check(nil, obj); err != nil {
			return rejected(err)
		}
	}
	if err := stamp(obj, format.Write()); err != nil {
		return rejected(err)
	}
	return unreachable(s.Interface.Create(ctx, key, obj, out, ttl))
}

// GuaranteedUpdate checks each result of tryUpdate against the object it
// started from. The store commits only when the stored object is still
// that object, so the check is atomic with the write on every store.
func (s *store) GuaranteedUpdate(ctx context.Context, key string, destination runtime.Object, ignoreNotFound bool,
	preconditions *storage.Preconditions, tryUpdate storage.UpdateFunc, cachedExistingObject runtime.Object) error {
	if err := notRead(); err != nil {
		return err
	}
	checked := func(input runtime.Object, res storage.ResponseMeta) (runtime.Object, *uint64, error) {
		if err := checkFormat(input); err != nil {
			return nil, nil, &callerError{rejected(err)}
		}
		out, ttl, err := tryUpdate(input, res)
		if err != nil {
			return nil, nil, &callerError{err}
		}
		if s.check != nil {
			if err := s.check(input, out); err != nil {
				return nil, nil, &callerError{rejected(err)}
			}
		}
		if err := stamp(out, format.Write()); err != nil {
			return nil, nil, &callerError{rejected(err)}
		}
		return out, ttl, nil
	}
	return unreachable(s.Interface.GuaranteedUpdate(ctx, key, destination, ignoreNotFound, preconditions, checked, cachedExistingObject))
}

// Delete rejects the delete of an object in a newer format.
func (s *store) Delete(ctx context.Context, key string, out runtime.Object, preconditions *storage.Preconditions,
	validateDeletion storage.ValidateObjectFunc, cachedExistingObject runtime.Object, opts storage.DeleteOptions) error {
	if err := notRead(); err != nil {
		return err
	}
	checked := func(ctx context.Context, obj runtime.Object) error {
		if err := checkFormat(obj); err != nil {
			return &callerError{rejected(err)}
		}
		if err := validateDeletion(ctx, obj); err != nil {
			return &callerError{err}
		}
		return nil
	}
	return unreachable(s.Interface.Delete(ctx, key, out, preconditions, checked, cachedExistingObject, opts))
}

// rejected is an internal error, not a conflict, so a client does not
// retry the same write.
func rejected(err error) error {
	return apierrors.NewInternalError(fmt.Errorf("storage guard: %w", err))
}

// IsRejected reports whether err comes from the guard.
func IsRejected(err error) bool {
	return apierrors.IsInternalError(err) && strings.Contains(err.Error(), "storage guard: ")
}

//= spec/solas.md#16-1-the-server
//# Until the server has read the finalized format and the epoch of the
//# store, it MUST reject every write with `503 ServiceUnavailable`.

// notRead returns a 503 while the server has not read the finalized
// format and the epoch of the store.
func notRead() error {
	if format.Known() {
		return nil
	}
	return apierrors.NewServiceUnavailable("the server has not read the finalized format of the store yet")
}

// callerError marks an error that the registry returned from tryUpdate or
// validateDeletion, or that the guard itself raised. It is not an error
// of the store, so unreachable returns it as it was.
type callerError struct{ err error }

func (e *callerError) Error() string { return e.err.Error() }
func (e *callerError) Unwrap() error { return e.err }

//= spec/solas.md#16-1-the-server
//# A write that fails because the server cannot reach the store MUST fail
//# with `503 ServiceUnavailable`.

// unreachable maps an error of the store to the error that the client
// sees. An error of the caller, an API error, and a storage error with a
// meaning of its own pass through. Any other error means that the store
// did not answer, and becomes a 503 ServiceUnavailable.
func unreachable(err error) error {
	if err == nil {
		return nil
	}
	var caller *callerError
	if errors.As(err, &caller) {
		return caller.err
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		return err
	}
	if storage.IsNotFound(err) || storage.IsExist(err) || storage.IsConflict(err) || storage.IsInvalidObj(err) ||
		storage.IsInvalidError(err) || storage.IsCorruptObject(err) || storage.IsTooLargeResourceVersion(err) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return apierrors.NewServiceUnavailable(fmt.Sprintf("the shared store did not answer: %v", err))
}
