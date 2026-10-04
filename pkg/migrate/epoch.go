package migrate

import (
	"context"
	"fmt"

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
