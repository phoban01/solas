package migrate

import (
	"context"
	"strconv"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/phoban01/solas/pkg/apis/solas"
)

//= spec/solas.md#13-1-epoch
//= type=test
//# After a restore, and before any server writes to the store, the restore
//# tool MUST set an epoch above every epoch that the store had.

//= spec/solas.md#13-3-resource-versions
//= type=test
//# On the DynamoDB store, the restore tool MUST raise each counter item to
//# at least `epoch * 2^32`.

func TestSetEpoch(t *testing.T) {
	for name, openStore := range map[string]func(*testing.T) (*Store, string){"dynamodb": openDynamo, "etcd": openEtcd} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, _ := openStore(t)
			if e, err := s.Epoch(ctx); err != nil || e != 0 {
				t.Fatalf("a new store is at epoch %d, %v; want 0", e, err)
			}
			if err := s.SetEpoch(ctx, 5); err != nil {
				t.Fatal(err)
			}
			if e, _ := s.Epoch(ctx); e != 5 {
				t.Errorf("epoch = %d, want 5", e)
			}
			for _, e := range []int64{5, 3} {
				if err := s.SetEpoch(ctx, e); err == nil {
					t.Errorf("SetEpoch(%d) at epoch 5 passed", e)
				}
			}
			if s.Dynamo == nil {
				return
			}
			d := &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1"}}
			out := &solas.Device{}
			if err := s.Devices.Create(ctx, devices.prefix()+"/d1", d, out, 0); err != nil {
				t.Fatal(err)
			}
			rv, _ := strconv.ParseUint(out.ResourceVersion, 10, 64)
			if rv <= 5<<32 {
				t.Errorf("first write after the epoch got resource version %d, want above %d", rv, uint64(5)<<32)
			}
		})
	}
}
