package migrate

import (
	"context"
	"io"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	k8sstorage "k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/pkg/apis/solas"
)

//= spec/solas.md#12-2-copy
//= type=test
//# After the copy, the tool MUST verify that each object in the destination
//# equals its source, except for the resource version and the epoch.

func TestVerify(t *testing.T) {
	ctx := context.Background()
	src, _ := openDynamo(t)
	dst, _ := openEtcd(t)
	seed(t, src)
	if _, err := Copy(ctx, src, dst, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := Verify(ctx, src, dst); err != nil {
		t.Fatalf("verify after a copy: %v", err)
	}

	// A changed token in the destination.
	if err := dst.Devices.GuaranteedUpdate(ctx, devices.prefix()+"/d1", &solas.Device{}, false, nil,
		func(in runtime.Object, _ k8sstorage.ResponseMeta) (runtime.Object, *uint64, error) {
			d := in.(*solas.Device).DeepCopy()
			d.Status.FencingToken = 1
			return d, nil, nil
		}, nil); err != nil {
		t.Fatal(err)
	}
	// A missing Member.
	if err := dst.Members.Delete(ctx, members.prefix()+"/a", &solas.Member{}, nil,
		k8sstorage.ValidateAllObjectFunc, nil, k8sstorage.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	err := Verify(ctx, src, dst)
	if err == nil || !strings.Contains(err.Error(), "devices/d1 differs") || !strings.Contains(err.Error(), "members/a is missing") {
		t.Errorf("verify = %v; want a difference in devices/d1 and a missing members/a", err)
	}
}
