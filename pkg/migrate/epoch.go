package migrate

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sstorage "k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/storage/dynamo"
)

//= spec/solas.md#13-1-epoch
//# After a restore, and before any server writes to the store, the restore
//# tool MUST set an epoch above every epoch that the store had.

// SetEpoch moves the store to epoch e after a restore. It refuses an epoch
// at or below the stored one; the write is conditional, so it only moves
// up. On DynamoDB it also raises the counter items to e * 2^32, so the
// resource versions go up too, spec 13.3.
func (s *Store) SetEpoch(ctx context.Context, e int64) error {
	up := func(in runtime.Object, _ k8sstorage.ResponseMeta) (runtime.Object, *uint64, error) {
		f := in.(*solas.StoreFormat).DeepCopy()
		if e <= f.Epoch {
			return nil, nil, fmt.Errorf("the store is at epoch %d; a restore must move it above that, not to %d", f.Epoch, e)
		}
		f.Epoch = e
		return f, nil, nil
	}
	err := s.Formats.GuaranteedUpdate(ctx, formatKey, &solas.StoreFormat{}, false, nil, up, nil)
	if k8sstorage.IsNotFound(err) {
		if e <= 0 {
			return fmt.Errorf("the store is at epoch 0; a restore must move it above that, not to %d", e)
		}
		err = s.Formats.Create(ctx, formatKey, &solas.StoreFormat{ObjectMeta: metav1.ObjectMeta{Name: "finalized"}, Epoch: e},
			&solas.StoreFormat{}, 0)
		if k8sstorage.IsExist(err) {
			return s.SetEpoch(ctx, e)
		}
	}
	if err != nil {
		return err
	}
	if s.Dynamo != nil {
		return dynamo.RaiseCounters(ctx, *s.Dynamo, s.Prefix, ResourcePrefixes(), uint64(e)<<32)
	}
	return nil
}

// HighestVersion returns the highest resource version that the store
// issued, over the resources that a move copies: the counter of each
// resource on DynamoDB, the revision on etcd.
func (s *Store) HighestVersion(ctx context.Context) (uint64, error) {
	var top uint64
	for _, r := range resources {
		v, err := s.storageFor(r).GetCurrentResourceVersion(ctx)
		if err != nil {
			return 0, fmt.Errorf("current version of %s: %w", r.gr.Resource, err)
		}
		top = max(top, v)
	}
	return top, nil
}

//= spec/solas.md#12-2-copy
//# After the verify, and before any cluster writes to the destination, the
//# tool MUST move the destination to a new epoch, spec 13.1, with every
//# resource version that it issues above every resource version of the
//# source, spec 13.3.

// MoveEpoch moves a DynamoDB destination to a new epoch after the copy:
// above the epoch of either store, above the time in Unix seconds as spec
// 13.1 asks, and high enough that every version the destination issues is
// above every version of the source. It returns the epoch.
func MoveEpoch(ctx context.Context, src, dst *Store, now time.Time) (int64, error) {
	srcEpoch, err := src.Epoch(ctx)
	if err != nil {
		return 0, err
	}
	dstEpoch, err := dst.Epoch(ctx)
	if err != nil {
		return 0, err
	}
	top, err := src.HighestVersion(ctx)
	if err != nil {
		return 0, err
	}
	e := max(now.Unix(), srcEpoch+1, dstEpoch+1, int64(top>>32)+1)
	return e, dst.SetEpoch(ctx, e)
}

//= spec/solas.md#12-2-copy
//# An etcd destination cannot raise its revision, so the tool MUST refuse
//# an etcd destination whose revision is below the highest resource version
//# of the source.

// CheckRevision refuses an etcd destination whose revision is below the
// highest version of the source. It runs before the copy, so a refusal
// leaves the destination empty.
func CheckRevision(ctx context.Context, src, dst *Store) error {
	top, err := src.HighestVersion(ctx)
	if err != nil {
		return err
	}
	rev, err := dst.HighestVersion(ctx)
	if err != nil {
		return err
	}
	if rev < top {
		return fmt.Errorf("the destination is at revision %d, below the highest resource version %d of the source; "+
			"raise it first with etcdutl snapshot restore --bump-revision, so no version goes back across the move", rev, top)
	}
	return nil
}
