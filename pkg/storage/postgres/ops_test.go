package postgres

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/apis/example"
	"k8s.io/apiserver/pkg/storage"
)

// TestOps runs an object through Create, Get, GuaranteedUpdate, and
// Delete, and checks the versions that each returns.
func TestOps(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	key := "/pods/ns/a"

	out := &example.Pod{}
	in := &example.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"}}
	if err := s.Create(ctx, key, in, out, 0); err != nil {
		t.Fatal(err)
	}
	if out.ResourceVersion != "2" {
		t.Errorf("create: resourceVersion = %q, want 2", out.ResourceVersion)
	}
	if err := s.Create(ctx, key, in, &example.Pod{}, 0); !storage.IsExist(err) {
		t.Errorf("create again = %v, want an exists error", err)
	}

	got := &example.Pod{}
	if err := s.Get(ctx, key, storage.GetOptions{}, got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "a" || got.ResourceVersion != "2" {
		t.Errorf("get = %s at %q, want a at 2", got.Name, got.ResourceVersion)
	}
	if err := s.Get(ctx, "/pods/ns/none", storage.GetOptions{}, &example.Pod{}); !storage.IsNotFound(err) {
		t.Errorf("get of a missing key = %v, want not found", err)
	}

	updated := &example.Pod{}
	err := s.GuaranteedUpdate(ctx, key, updated, false, nil, func(obj runtime.Object, _ storage.ResponseMeta) (runtime.Object, *uint64, error) {
		p := obj.(*example.Pod)
		p.Labels = map[string]string{"k": "v"}
		return p, nil, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ResourceVersion != "3" || updated.Labels["k"] != "v" {
		t.Errorf("update = %q %v, want 3 with the label", updated.ResourceVersion, updated.Labels)
	}

	deleted := &example.Pod{}
	if err := s.Delete(ctx, key, deleted, nil, storage.ValidateAllObjectFunc, nil, storage.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if deleted.ResourceVersion != "4" {
		t.Errorf("delete: resourceVersion = %q, want 4", deleted.ResourceVersion)
	}
	if err := s.Get(ctx, key, storage.GetOptions{}, &example.Pod{}); !storage.IsNotFound(err) {
		t.Errorf("get after the delete = %v, want not found", err)
	}
	stats, err := s.Stats(ctx)
	if err != nil || stats.ObjectCount != 0 {
		t.Errorf("stats = %+v, %v; want 0 objects", stats, err)
	}
}
