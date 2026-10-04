package migrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sstorage "k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/format"
	"github.com/phoban01/solas/pkg/registry/solas/member"
)

// formatKey is the storage key of the one StoreFormat of a store.
var formatKey = formats.prefix() + "/finalized"

//= spec/solas.md#11-1-format-version
//# The finalized format starts at 1.

// Finalized returns the finalized format of the store. A store with no
// record is at format 1.
func (s *Store) Finalized(ctx context.Context) (int32, error) {
	var f solas.StoreFormat
	err := s.Formats.Get(ctx, formatKey, k8sstorage.GetOptions{}, &f)
	switch {
	case k8sstorage.IsNotFound(err):
		return 1, nil
	case err != nil:
		return 0, err
	case f.Finalized < 1:
		return 1, nil
	}
	return f.Finalized, nil
}

//= spec/solas.md#11-4-finalization
//# The finalized format MUST only move up.

//= spec/solas.md#11-4-finalization
//# The finalized format MUST move to a value only when every `Active`
//# member reports a highest format at or above that value.

// Finalize moves the finalized format of the store to n. It refuses when
// an Active member supports less than n, and names those members. It moves
// only up: the write is conditional on the stored value, so two runs at
// once cannot move it down. Finalizing to the current value does nothing.
func (s *Store) Finalize(ctx context.Context, n int32) error {
	objs, err := s.list(ctx, members)
	if err != nil {
		return err
	}
	var ms []format.Member
	for _, o := range objs {
		m := o.(*solas.Member)
		_, hi := member.FormatRange(m)
		ms = append(ms, format.Member{Name: m.Name, Active: m.Status.Phase == solas.MemberActive, Max: hi})
	}
	if behind := format.Behind(ms, n); len(behind) > 0 {
		return fmt.Errorf("cannot finalize format %d: these Active members do not support it: %s", n, strings.Join(behind, ", "))
	}

	errDown := errors.New("the finalized format only moves up")
	up := func(in runtime.Object, _ k8sstorage.ResponseMeta) (runtime.Object, *uint64, error) {
		f := in.(*solas.StoreFormat).DeepCopy()
		cur := max(f.Finalized, 1)
		if n < cur {
			return nil, nil, fmt.Errorf("%w: it is %d, not moving to %d", errDown, cur, n)
		}
		f.Finalized = n
		return f, nil, nil
	}
	err = s.Formats.GuaranteedUpdate(ctx, formatKey, &solas.StoreFormat{}, false, nil, up, nil)
	if k8sstorage.IsNotFound(err) {
		if n < 1 {
			return fmt.Errorf("%w: it is 1, not moving to %d", errDown, n)
		}
		err = s.Formats.Create(ctx, formatKey, &solas.StoreFormat{ObjectMeta: metav1.ObjectMeta{Name: "finalized"}, Finalized: n},
			&solas.StoreFormat{}, 0)
		if k8sstorage.IsExist(err) {
			// Another run created it first; try the conditional move again.
			return s.Finalize(ctx, n)
		}
	}
	return err
}

// Epoch returns the epoch of the store, spec 13.1. A store with no record
// is at epoch 0.
func (s *Store) Epoch(ctx context.Context) (int64, error) {
	var f solas.StoreFormat
	err := s.Formats.Get(ctx, formatKey, k8sstorage.GetOptions{}, &f)
	if k8sstorage.IsNotFound(err) {
		return 0, nil
	}
	return f.Epoch, err
}
