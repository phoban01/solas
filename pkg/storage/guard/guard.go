// Package guard checks each write below the registry, on any store. The
// registry strategy is the first check; the guard is a second one that
// does not depend on the strategy, so servers of two releases enforce the
// same rules on the stored objects, spec 11.
package guard

import (
	"context"
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
// rejects a write or a delete of an object in a newer format, and runs the
// transition check of the resource.
type store struct {
	storage.Interface
	check Transition
}

// Wrap returns s with the guard. check may be nil. Reads pass through.
func Wrap(s storage.Interface, check Transition) storage.Interface {
	return &store{Interface: s, check: check}
}

// Create checks a new object against no old object, and stamps it.
func (s *store) Create(ctx context.Context, key string, obj, out runtime.Object, ttl uint64) error {
	if s.check != nil {
		if err := s.check(nil, obj); err != nil {
			return rejected(err)
		}
	}
	if err := stamp(obj, format.Write()); err != nil {
		return rejected(err)
	}
	return s.Interface.Create(ctx, key, obj, out, ttl)
}

// GuaranteedUpdate checks each result of tryUpdate against the object it
// started from. The store commits only when the stored object is still
// that object, so the check is atomic with the write on every store.
func (s *store) GuaranteedUpdate(ctx context.Context, key string, destination runtime.Object, ignoreNotFound bool,
	preconditions *storage.Preconditions, tryUpdate storage.UpdateFunc, cachedExistingObject runtime.Object) error {
	checked := func(input runtime.Object, res storage.ResponseMeta) (runtime.Object, *uint64, error) {
		if err := checkFormat(input); err != nil {
			return nil, nil, rejected(err)
		}
		out, ttl, err := tryUpdate(input, res)
		if err != nil {
			return nil, nil, err
		}
		if s.check != nil {
			if err := s.check(input, out); err != nil {
				return nil, nil, rejected(err)
			}
		}
		if err := stamp(out, format.Write()); err != nil {
			return nil, nil, rejected(err)
		}
		return out, ttl, nil
	}
	return s.Interface.GuaranteedUpdate(ctx, key, destination, ignoreNotFound, preconditions, checked, cachedExistingObject)
}

// Delete rejects the delete of an object in a newer format.
func (s *store) Delete(ctx context.Context, key string, out runtime.Object, preconditions *storage.Preconditions,
	validateDeletion storage.ValidateObjectFunc, cachedExistingObject runtime.Object, opts storage.DeleteOptions) error {
	checked := func(ctx context.Context, obj runtime.Object) error {
		if err := checkFormat(obj); err != nil {
			return rejected(err)
		}
		return validateDeletion(ctx, obj)
	}
	return s.Interface.Delete(ctx, key, out, preconditions, checked, cachedExistingObject, opts)
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
