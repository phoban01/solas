package postgres

import (
	"context"
	"strconv"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/apis/example"
	"k8s.io/apiserver/pkg/storage"
)

func createPods(t *testing.T, s *store, names ...string) {
	t.Helper()
	for _, name := range names {
		pod := &example.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"}}
		if err := s.Create(context.Background(), "/pods/ns/"+name, pod, &example.Pod{}, 0); err != nil {
			t.Fatal(err)
		}
	}
}

func listNames(t *testing.T, s *store, opts storage.ListOptions) (string, []string) {
	t.Helper()
	list := &example.PodList{}
	if err := s.GetList(context.Background(), "/pods/", opts, list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list.Items {
		names = append(names, p.Name)
	}
	return list.ResourceVersion, names
}

//= spec/solas.md#2-4-reads
//= type=test
//# A request for a list at an older, exact resource version MUST be served
//# by the watch cache, or it MUST fail with `410 Gone`.

func TestListContinueAfterWriteIsGone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createPods(t, s, "a", "b", "c")

	opts := storage.ListOptions{Recursive: true, Predicate: storage.Everything}
	opts.Predicate.Limit = 1
	first := &example.PodList{}
	if err := s.GetList(ctx, "/pods/", opts, first); err != nil {
		t.Fatal(err)
	}
	if first.Continue == "" {
		t.Fatal("first page has no continue token")
	}

	// With no write in between, the next page is served.
	opts.Predicate.Continue = first.Continue
	second := &example.PodList{}
	if err := s.GetList(ctx, "/pods/", opts, second); err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Name != "b" {
		t.Fatalf("second page = %v, want [b]", second.Items)
	}

	// After a write, the old snapshot is gone.
	createPods(t, s, "d")
	opts.Predicate.Continue = second.Continue
	err := s.GetList(ctx, "/pods/", opts, &example.PodList{})
	if !apierrors.IsResourceExpired(err) {
		t.Fatalf("third page after a write: err = %v, want 410 Gone", err)
	}
}

func TestListExactVersion(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createPods(t, s, "a", "b")

	for _, tt := range []struct {
		rv   uint64
		want func(error) bool
	}{
		{rv: 3, want: func(err error) bool { return err == nil }},
		{rv: 2, want: apierrors.IsResourceExpired},
		{rv: 4, want: storage.IsTooLargeResourceVersion},
	} {
		opts := storage.ListOptions{
			Recursive:            true,
			Predicate:            storage.Everything,
			ResourceVersion:      strconv.FormatUint(tt.rv, 10),
			ResourceVersionMatch: metav1.ResourceVersionMatchExact,
		}
		if err := s.GetList(ctx, "/pods/", opts, &example.PodList{}); !tt.want(err) {
			t.Errorf("exact list at %d: unexpected err %v", tt.rv, err)
		}
	}
}

//= spec/solas.md#2-8-postgres-store
//= type=test
//# A list MUST read the `counters` row and the `objects` rows in one
//# transaction at the `REPEATABLE READ` level.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# So the list is a consistent snapshot at the counter value that it read.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# The list MUST return that value as its resource version, with no second
//# read of the counter.

// TestListIsOneSnapshot commits a write between the counter read and the
// object query of a list. The list returns the counter it read and the
// objects of that version, without the late write, and the next list
// shows both.
func TestListIsOneSnapshot(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createPods(t, s, "a")
	fired := false
	s.afterListCounter = func() {
		if fired {
			return
		}
		fired = true
		pod := &example.Pod{ObjectMeta: metav1.ObjectMeta{Name: "late", Namespace: "ns"}}
		if err := s.Create(ctx, "/pods/ns/late", pod, &example.Pod{}, 0); err != nil {
			t.Error(err)
		}
	}
	opts := storage.ListOptions{Recursive: true, Predicate: storage.Everything}
	rv, names := listNames(t, s, opts)
	if rv != "2" || len(names) != 1 || names[0] != "a" {
		t.Errorf("list during the write = rv %s %v, want rv 2 [a]", rv, names)
	}
	rv, names = listNames(t, s, opts)
	if rv != "3" || len(names) != 2 {
		t.Errorf("list after the write = rv %s %v, want rv 3 [a late]", rv, names)
	}
}

// TestListKeyOrderAndPrefix lists under a namespace prefix and checks
// that the keys come in byte order and that a sibling prefix is not in
// the list.
func TestListKeyOrderAndPrefix(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, k := range []string{"/pods/ns/b", "/pods/ns/a", "/pods/ns/B", "/pods/ns2/x", "/pods/ns/a-b"} {
		pod := &example.Pod{ObjectMeta: metav1.ObjectMeta{Name: k[len("/pods/"):], Namespace: "ns"}}
		if err := s.Create(ctx, k, pod, &example.Pod{}, 0); err != nil {
			t.Fatal(err)
		}
	}
	list := &example.PodList{}
	if err := s.GetList(ctx, "/pods/ns/", storage.ListOptions{Recursive: true, Predicate: storage.Everything}, list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list.Items {
		names = append(names, p.Name)
	}
	want := "[ns/B ns/a ns/a-b ns/b]"
	if got := strconv.Quote(joinNames(names)); got != strconv.Quote(want) {
		t.Errorf("list under /pods/ns/ = %v, want %s (byte order, no ns2)", names, want)
	}
	one := &example.PodList{}
	if err := s.GetList(ctx, "/pods/ns/a", storage.ListOptions{Predicate: storage.Everything}, one); err != nil {
		t.Fatal(err)
	}
	if len(one.Items) != 1 || one.Items[0].Name != "ns/a" {
		t.Errorf("list of one key = %v, want [ns/a]", one.Items)
	}
}

func joinNames(names []string) string {
	out := "["
	for i, n := range names {
		if i > 0 {
			out += " "
		}
		out += n
	}
	return out + "]"
}
