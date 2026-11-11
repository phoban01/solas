package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/storage"
)

// objState is an object as the server read it.
type objState struct {
	obj  runtime.Object
	meta *storage.ResponseMeta
	// rev is 0 when the object does not exist.
	rev  uint64
	data []byte
}

//= spec/solas.md#2-8-postgres-store
//# A get MUST read the `objects` row.

// readObject returns the row of the object at key: its version and its
// value, or found false when it does not exist.
func (s *store) readObject(ctx context.Context, key string) (rv uint64, value []byte, found bool, err error) {
	var n int64
	err = s.pool.QueryRow(ctx, s.q.readObject, s.resource, key).Scan(&n, &value)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, fmt.Errorf("read object %s: %w", key, err)
	}
	return uint64(n), value, true, nil
}

// decode decodes data into out and sets its resource version.
func (s *store) decode(data []byte, out runtime.Object, rv uint64) error {
	if _, err := conversion.EnforcePtr(out); err != nil {
		return fmt.Errorf("unable to convert output object to pointer: %v", err)
	}
	if err := runtime.DecodeInto(s.codec, data, out); err != nil {
		return err
	}
	//= spec/solas.md#3-3-objects
	//# The `metadata.resourceVersion` of an object MUST equal the version of the
	//# write that last changed it.
	return s.versioner.UpdateObject(out, rv)
}

func (s *store) Create(ctx context.Context, key string, obj, out runtime.Object, ttl uint64) error {
	k, err := s.objectKey(key, false)
	if err != nil {
		return err
	}
	if version, err := s.versioner.ObjectResourceVersion(obj); err == nil && version != 0 {
		return storage.ErrResourceVersionSetOnCreate
	}
	if err := s.versioner.PrepareObjectForStorage(obj); err != nil {
		return fmt.Errorf("PrepareObjectForStorage failed: %v", err)
	}
	data, err := runtime.Encode(s.codec, obj)
	if err != nil {
		return err
	}
	rv, err := s.commit(ctx, write{key: k, action: actCreate, value: data})
	if errors.Is(err, errObjectConflict) {
		//= spec/solas.md#2-8-postgres-store
		//# When the insert of a create fails, the server MUST return
		//# `409 AlreadyExists` for that write.
		return storage.NewKeyExistsError(key, 0)
	}
	if err != nil {
		return err
	}
	if out != nil {
		return s.decode(data, out, rv)
	}
	return nil
}

func (s *store) Get(ctx context.Context, key string, opts storage.GetOptions, out runtime.Object) error {
	k, err := s.objectKey(key, false)
	if err != nil {
		return err
	}
	rv, value, found, err := s.readObject(ctx, k)
	if err != nil {
		return err
	}
	if opts.ResourceVersion != "" {
		current, err := s.readCounter(ctx)
		if err != nil {
			return err
		}
		if err := s.validateMinimumResourceVersion(opts.ResourceVersion, current); err != nil {
			return err
		}
	}
	if !found {
		if opts.IgnoreNotFound {
			return runtime.SetZeroValue(out)
		}
		return storage.NewKeyNotFoundError(key, 0)
	}
	//= spec/solas.md#2-4-reads
	//# A get MUST return the object from its object item.
	return s.decode(value, out, rv)
}

func (s *store) validateMinimumResourceVersion(minimum string, actual uint64) error {
	if minimum == "" {
		return nil
	}
	minRV, err := s.versioner.ParseResourceVersion(minimum)
	if err != nil {
		return apierrors.NewBadRequest(fmt.Sprintf("invalid resource version: %v", err))
	}
	if minRV > actual {
		return storage.NewTooLargeResourceVersionError(minRV, actual, 0)
	}
	return nil
}

// getState reads the current state of the object at sk.
func (s *store) getState(ctx context.Context, key, k string, v reflect.Value, ignoreNotFound bool) (*objState, error) {
	rv, value, found, err := s.readObject(ctx, k)
	if err != nil {
		return nil, err
	}
	state := &objState{meta: &storage.ResponseMeta{}}
	if u, ok := v.Addr().Interface().(runtime.Unstructured); ok {
		state.obj = u.NewEmptyInstance()
	} else {
		state.obj = reflect.New(v.Type()).Interface().(runtime.Object)
	}
	if !found {
		if !ignoreNotFound {
			return nil, storage.NewKeyNotFoundError(key, 0)
		}
		if err := runtime.SetZeroValue(state.obj); err != nil {
			return nil, err
		}
		return state, nil
	}
	state.rev = rv
	state.meta.ResourceVersion = state.rev
	state.data = value
	if err := s.decode(state.data, state.obj, state.rev); err != nil {
		return nil, err
	}
	return state, nil
}

// getStateFromObject builds a state from an object the caller has cached.
func (s *store) getStateFromObject(obj runtime.Object) (*objState, error) {
	state := &objState{obj: obj, meta: &storage.ResponseMeta{}}
	rv, err := s.versioner.ObjectResourceVersion(obj)
	if err != nil {
		return nil, fmt.Errorf("couldn't get resource version: %v", err)
	}
	state.rev = rv
	state.meta.ResourceVersion = rv
	// Encode the object with no resource version, as it is stored.
	if err := s.versioner.PrepareObjectForStorage(obj); err != nil {
		return nil, fmt.Errorf("PrepareObjectForStorage failed: %v", err)
	}
	state.data, err = runtime.Encode(s.codec, obj)
	if err != nil {
		return nil, err
	}
	if err := s.versioner.UpdateObject(state.obj, rv); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *store) Delete(ctx context.Context, key string, out runtime.Object, preconditions *storage.Preconditions,
	validateDeletion storage.ValidateObjectFunc, cachedExistingObject runtime.Object, opts storage.DeleteOptions) error {
	k, err := s.objectKey(key, false)
	if err != nil {
		return err
	}
	v, err := conversion.EnforcePtr(out)
	if err != nil {
		return fmt.Errorf("unable to convert output object to pointer: %v", err)
	}
	current := func() (*objState, error) { return s.getState(ctx, key, k, v, false) }

	var orig *objState
	var origIsCurrent bool
	if cachedExistingObject != nil {
		orig, err = s.getStateFromObject(cachedExistingObject)
	} else {
		orig, err = current()
		origIsCurrent = true
	}
	if err != nil {
		return err
	}

	for {
		if preconditions != nil {
			if err := preconditions.Check(key, orig.obj); err != nil {
				if origIsCurrent {
					return err
				}
				cachedRev, cachedErr := orig.rev, err
				if orig, err = current(); err != nil {
					return err
				}
				origIsCurrent = true
				if cachedRev == orig.rev {
					return cachedErr
				}
				continue
			}
		}
		if err := validateDeletion(ctx, orig.obj); err != nil {
			if origIsCurrent {
				return err
			}
			cachedRev, cachedErr := orig.rev, err
			if orig, err = current(); err != nil {
				return err
			}
			origIsCurrent = true
			if cachedRev == orig.rev {
				return cachedErr
			}
			continue
		}

		rv, err := s.commit(ctx, write{key: k, action: actDelete, expectRV: orig.rev, value: orig.data, prev: orig.data})
		//= spec/solas.md#2-8-postgres-store
		//# When the update or the delete of a write changes no row, the server MUST
		//# read the object again and retry that write, as in section 2.3.
		if errors.Is(err, errObjectConflict) {
			// The object changed or went away. Read it again and retry.
			if orig, err = current(); err != nil {
				return err
			}
			origIsCurrent = true
			continue
		}
		if err != nil {
			return err
		}
		//= spec/solas.md#3-3-objects
		//# The object in a `DELETED` event MUST carry the version of the delete.
		return s.decode(orig.data, out, rv)
	}
}

func (s *store) GuaranteedUpdate(ctx context.Context, key string, destination runtime.Object, ignoreNotFound bool,
	preconditions *storage.Preconditions, tryUpdate storage.UpdateFunc, cachedExistingObject runtime.Object) error {
	k, err := s.objectKey(key, false)
	if err != nil {
		return err
	}
	v, err := conversion.EnforcePtr(destination)
	if err != nil {
		return fmt.Errorf("unable to convert output object to pointer: %v", err)
	}
	current := func() (*objState, error) { return s.getState(ctx, key, k, v, ignoreNotFound) }

	var orig *objState
	var origIsCurrent bool
	if cachedExistingObject != nil {
		orig, err = s.getStateFromObject(cachedExistingObject)
	} else {
		orig, err = current()
		origIsCurrent = true
	}
	if err != nil {
		return err
	}

	for {
		if err := preconditions.Check(key, orig.obj); err != nil {
			if origIsCurrent {
				return err
			}
			if orig, err = current(); err != nil {
				return err
			}
			origIsCurrent = true
			continue
		}

		ret, err := s.updateState(orig, tryUpdate)
		if err != nil {
			if origIsCurrent {
				return err
			}
			cachedRev, cachedErr := orig.rev, err
			if orig, err = current(); err != nil {
				return err
			}
			origIsCurrent = true
			if cachedRev == orig.rev {
				return cachedErr
			}
			continue
		}

		data, err := runtime.Encode(s.codec, ret)
		if err != nil {
			return err
		}
		if orig.rev != 0 && bytes.Equal(data, orig.data) {
			// No change. Confirm against the stored object and skip the write.
			if !origIsCurrent {
				if orig, err = current(); err != nil {
					return err
				}
				origIsCurrent = true
				if !bytes.Equal(data, orig.data) {
					continue
				}
			}
			return s.decode(orig.data, destination, orig.rev)
		}

		w := write{key: k, action: actUpdate, expectRV: orig.rev, value: data, prev: orig.data}
		if orig.rev == 0 {
			w = write{key: k, action: actCreate, value: data}
		}
		rv, err := s.commit(ctx, w)
		if errors.Is(err, errObjectConflict) {
			//= spec/solas.md#2-8-postgres-store
			//# When the update or the delete of a write changes no row, the server MUST
			//# read the object again and retry that write, as in section 2.3.
			//
			// GuaranteedUpdate is the generic registry's retry loop, so it
			// reads the new state and calls tryUpdate again.
			if orig, err = current(); err != nil {
				return err
			}
			origIsCurrent = true
			continue
		}
		if err != nil {
			return err
		}
		return s.decode(data, destination, rv)
	}
}

func (s *store) updateState(st *objState, tryUpdate storage.UpdateFunc) (runtime.Object, error) {
	ret, _, err := tryUpdate(st.obj, *st.meta)
	if err != nil {
		return nil, err
	}
	if err := s.versioner.PrepareObjectForStorage(ret); err != nil {
		return nil, fmt.Errorf("PrepareObjectForStorage failed: %v", err)
	}
	return ret, nil
}
