package migrate

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	k8sstorage "k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/pkg/apis/solas"
)

//= spec/solas.md#11-4-finalization
//= type=test
//# The finalized format MUST only move up.

//= spec/solas.md#11-4-finalization
//= type=test
//# The finalized format MUST move to a value only when every `Active`
//# member reports a highest format at or above that value.

func TestFinalize(t *testing.T) {
	for name, openStore := range map[string]func(*testing.T) (*Store, string){"dynamodb": openDynamo, "etcd": openEtcd} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, _ := openStore(t)
			if f, err := s.Finalized(ctx); err != nil || f != 1 {
				t.Fatalf("a new store is at format %d, %v; want 1", f, err)
			}
			member := func(name string, phase solas.MemberPhase, hi int32) {
				m := &solas.Member{ObjectMeta: metav1.ObjectMeta{Name: name, UID: k8stypes.UID("uid-" + name)},
					Spec: solas.MemberSpec{LeaseDurationSeconds: 30}, Status: solas.MemberStatus{Phase: phase, MinFormat: 1, MaxFormat: hi}}
				if err := s.Members.Create(ctx, members.prefix()+"/"+name, m, &solas.Member{}, 0); err != nil {
					t.Fatal(err)
				}
			}
			member("a", solas.MemberActive, 2)
			member("b", solas.MemberActive, 1)
			member("c", solas.MemberDraining, 1)

			err := s.Finalize(ctx, 2)
			if err == nil || !strings.Contains(err.Error(), "b (highest format 1)") || strings.Contains(err.Error(), "c (") {
				t.Fatalf("finalize while b supports 1: err = %v, want a refusal that names b and not the draining c", err)
			}
			if err := s.Members.GuaranteedUpdate(ctx, members.prefix()+"/b", &solas.Member{}, false, nil,
				func(in runtime.Object, _ k8sstorage.ResponseMeta) (runtime.Object, *uint64, error) {
					m := in.(*solas.Member).DeepCopy()
					m.Status.MaxFormat = 2
					return m, nil, nil
				}, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.Finalize(ctx, 2); err != nil {
				t.Fatalf("finalize when every Active member supports 2: %v", err)
			}
			if f, _ := s.Finalized(ctx); f != 2 {
				t.Errorf("finalized format = %d, want 2", f)
			}
			if err := s.Finalize(ctx, 1); err == nil {
				t.Error("finalize down to 1 passed")
			}
			if f, _ := s.Finalized(ctx); f != 2 {
				t.Errorf("finalized format after a refused move down = %d, want 2", f)
			}
		})
	}
}
