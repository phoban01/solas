package postgres

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/apis/example"
	"k8s.io/apiserver/pkg/storage"
)

// nextEvent returns the next event of w, or fails the test after 10s.
func nextEvent(t *testing.T, w watch.Interface) watch.Event {
	t.Helper()
	select {
	case e, ok := <-w.ResultChan():
		if !ok {
			t.Fatal("watch closed")
		}
		return e
	case <-time.After(10 * time.Second):
		t.Fatal("no event after 10s")
	}
	return watch.Event{}
}

// deleteEvent removes an event row, as the expiry of another server would.
func deleteEvent(t *testing.T, s *store, rv uint64) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(), "DELETE FROM "+s.schema+".events WHERE resource = $1 AND rv = $2", s.resource, int64(rv))
	if err != nil {
		t.Fatal(err)
	}
}

//= spec/solas.md#4-3-gaps
//= type=test
//# If the query result does not hold each version from `last + 1` to `c`
//# exactly once, the watch MUST end with `410 Gone`.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# A server MUST NOT depend on the time at which another server deletes
//# the expired events.

func TestWatchGapEndsWithGone(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createPods(t, s, "a") // version 2

	w, err := s.Watch(ctx, "/pods/", storage.ListOptions{ResourceVersion: "2", Recursive: true, Predicate: storage.Everything})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	createPods(t, s, "b") // version 3
	if e := nextEvent(t, w); e.Type != watch.Added || e.Object.(*example.Pod).Name != "b" {
		t.Fatalf("first event = %v %v, want ADDED b", e.Type, e.Object)
	}

	// Stop reads, so the next poll sees versions 4 and 5 together, and
	// delete version 4 before the watch can read it.
	w.Stop()
	createPods(t, s, "c", "d") // versions 4 and 5
	deleteEvent(t, s, 4)

	w, err = s.Watch(ctx, "/pods/", storage.ListOptions{ResourceVersion: "3", Recursive: true, Predicate: storage.Everything})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	e := nextEvent(t, w)
	status, ok := e.Object.(*metav1.Status)
	if e.Type != watch.Error || !ok || status.Code != http.StatusGone {
		t.Fatalf("event = %v %+v, want an error with code 410", e.Type, e.Object)
	}
	if _, open := <-w.ResultChan(); open {
		t.Error("watch still open after 410 Gone")
	}
}

//= spec/solas.md#4-2-poll
//= type=test
//# A watch MUST NOT deliver an `INIT` event.

func TestWatchSkipsInitEvent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.GetCurrentResourceVersion(ctx); err != nil { // creates the counter at 1
		t.Fatal(err)
	}
	w, err := s.Watch(ctx, "/pods/", storage.ListOptions{ResourceVersion: "0", Recursive: true, Predicate: storage.Everything})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	createPods(t, s, "a")
	if e := nextEvent(t, w); e.Type != watch.Added || e.Object.(*example.Pod).ResourceVersion != "2" {
		t.Fatalf("first event = %v %v, want ADDED at version 2", e.Type, e.Object)
	}
}

//= spec/solas.md#4-2-poll
//= type=test
//# When a watcher asks for progress, the server MUST poll before it sends
//# the bookmark.

// TestProgressRequestPollsFirst: a write lands, then the watch is asked
// for progress. The bookmark that answers carries the version of that
// write or a later one, because the poll runs before the bookmark.
func TestProgressRequestPollsFirst(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createPods(t, s, "a") // version 2
	w, err := s.Watch(ctx, "/pods/", storage.ListOptions{ResourceVersion: "2", Recursive: true,
		Predicate: storage.Everything, ProgressNotify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	for i := range 5 {
		createPods(t, s, fmt.Sprintf("p%d", i)) // version 3 + i
		written := uint64(3 + i)
		if err := s.RequestWatchProgress(ctx); err != nil {
			t.Fatal(err)
		}
		var bookmark uint64
		for bookmark == 0 {
			e := nextEvent(t, w)
			if e.Type != watch.Bookmark {
				continue
			}
			rv, err := s.versioner.ObjectResourceVersion(e.Object)
			if err != nil {
				t.Fatal(err)
			}
			bookmark = rv
		}
		if bookmark < written {
			t.Fatalf("bookmark after the progress request = %d, want at least %d: the watch sent it before it polled", bookmark, written)
		}
	}
}

//= spec/solas.md#4-2-poll
//= type=test
//# When the server reads the counter for a consistent list, it SHOULD poll
//# with that value at once and then send a bookmark.

// TestCurrentVersionReadPollsAtOnce: a write lands, the store reads the
// counter as the watch cache does before a consistent list, and the
// watch delivers the write and a bookmark with no poll of its own.
func TestCurrentVersionReadPollsAtOnce(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	// Only a hinted poll can deliver within the test.
	s.pollInterval = time.Hour
	s.progressInterval = time.Hour
	createPods(t, s, "a") // version 2
	w, err := s.Watch(ctx, "/pods/", storage.ListOptions{ResourceVersion: "2", Recursive: true,
		Predicate: storage.Everything, ProgressNotify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	for i := range 3 {
		createPods(t, s, fmt.Sprintf("p%d", i)) // version 3 + i
		written := uint64(3 + i)
		c, err := s.GetCurrentResourceVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if c != written {
			t.Fatalf("current version = %d, want %d", c, written)
		}
		e := nextEvent(t, w)
		if e.Type != watch.Added {
			t.Fatalf("event after the version read = %s, want the ADDED of the hinted poll", e.Type)
		}
		e = nextEvent(t, w)
		if e.Type != watch.Bookmark {
			t.Fatalf("event after the ADDED = %s, want a bookmark", e.Type)
		}
		rv, err := s.versioner.ObjectResourceVersion(e.Object)
		if err != nil {
			t.Fatal(err)
		}
		if rv < written {
			t.Fatalf("bookmark after the version read = %d, want at least %d", rv, written)
		}
	}
}

//= spec/solas.md#2-8-postgres-store
//= type=test
//# Each server MUST delete the `events` rows of each resource whose
//# `expires` is in the past.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# A watch MUST keep the gap rule of section 4.3.

// TestExpiryDeletesOldEvents writes with a clock in the past, so the
// events expire at once; the delete removes them, a watch from before
// them ends with 410 Gone, and a watch from the current version goes on.
func TestExpiryDeletesOldEvents(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	s.now = func() time.Time { return time.Now().Add(-2 * s.retention) }
	createPods(t, s, "a", "b") // versions 2 and 3, expired at once
	s.now = time.Now
	createPods(t, s, "c") // version 4, kept

	n, err := DeleteExpired(ctx, s.pool, s.prefix)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 { // INIT, a, b
		t.Errorf("deleted %d events, want 3", n)
	}

	w, err := s.Watch(ctx, "/pods/", storage.ListOptions{ResourceVersion: "1", Recursive: true, Predicate: storage.Everything})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	e := nextEvent(t, w)
	status, ok := e.Object.(*metav1.Status)
	if e.Type != watch.Error || !ok || status.Code != http.StatusGone {
		t.Fatalf("watch from version 1 after the expiry = %v %+v, want 410 Gone", e.Type, e.Object)
	}

	w2, err := s.Watch(ctx, "/pods/", storage.ListOptions{ResourceVersion: "3", Recursive: true, Predicate: storage.Everything})
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Stop()
	if e := nextEvent(t, w2); e.Type != watch.Added || e.Object.(*example.Pod).Name != "c" {
		t.Fatalf("watch from version 3 = %v %v, want ADDED c", e.Type, e.Object)
	}
}

// TestWatchDeliversTwoStoresInOrder runs writers on two stores of one
// resource, as two servers, and one watch sees every write in version
// order.
func TestWatchDeliversTwoStoresInOrder(t *testing.T) {
	ctx := context.Background()
	s1 := newTestStore(t)
	s2, err := newStore(Config{Pool: s1.pool, PollInterval: 20 * time.Millisecond}, codec, newPod, newPodList, s1.prefix, "/pods/", testGR)
	if err != nil {
		t.Fatal(err)
	}
	s2.schema, s2.q = s1.schema, s1.q
	if _, err := s1.GetCurrentResourceVersion(ctx); err != nil {
		t.Fatal(err)
	}
	w, err := s1.Watch(ctx, "/pods/", storage.ListOptions{ResourceVersion: "1", Recursive: true, Predicate: storage.Everything})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	const each = 10
	done := make(chan struct{})
	for i, s := range []*store{s1, s2} {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := range each {
				createPods(t, s, fmt.Sprintf("s%d-%d", i, j))
			}
		}()
	}
	<-done
	<-done
	last := uint64(1)
	for range 2 * each {
		e := nextEvent(t, w)
		if e.Type != watch.Added {
			t.Fatalf("event = %v %v, want ADDED", e.Type, e.Object)
		}
		rv, err := s1.versioner.ObjectResourceVersion(e.Object)
		if err != nil {
			t.Fatal(err)
		}
		if rv != last+1 {
			t.Fatalf("event at version %d after %d, want %d", rv, last, last+1)
		}
		last = rv
	}
}
