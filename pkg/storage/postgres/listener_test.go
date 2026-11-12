package postgres

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/apis/example"
	"k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/internal/pgtest"
	storageurl "github.com/phoban01/solas/pkg/storage"
)

//= spec/solas.md#2-8-postgres-store
//= type=test
//# The commit function SHOULD send a notification on the channel of the
//# schema, with the resource and the last version of the transaction.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# A notification MUST wake the poll of its resource, with the version
//# as `c`, as a counter read does in section 4.2.

// TestListenerWakesTheWatch gives a store a listener and a poll interval
// of 200 ms with a listener, so its timer polls every 2 seconds and only
// a notification can deliver within one. A
// second store, with no listener, writes; the watch of the first
// delivers the writes in order within 600 ms: the floor of one poll
// interval and a round trip.
func TestListenerWakesTheWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newTestStore(t)
	sc, err := storageurl.Parse(pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	s.listener = NewListener(ctx, sc.PostgresURL(), s.prefix)
	s.listener.register(s.resource, &s.watchers)
	s.pollInterval = 200 * time.Millisecond
	s.progressInterval = time.Hour
	writer, err := newStore(Config{Pool: s.pool}, codec, newPod, newPodList, s.prefix, "/pods/", testGR)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCurrentResourceVersion(ctx); err != nil {
		t.Fatal(err)
	}
	// The listener's connection is up once a write that lands after it
	// is delivered; give it a moment to run LISTEN.
	time.Sleep(300 * time.Millisecond)
	w, err := s.Watch(ctx, "/pods/", storage.ListOptions{ResourceVersion: "1", Recursive: true, Predicate: storage.Everything})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	for i, name := range []string{"a", "b", "c"} {
		createPods(t, writer, name)
		from := time.Now()
		select {
		case e := <-w.ResultChan():
			if e.Type != watch.Added || e.Object.(*example.Pod).Name != name {
				t.Fatalf("event %d = %v %v, want ADDED %s", i, e.Type, e.Object, name)
			}
			if d := time.Since(from); d > 600*time.Millisecond {
				t.Errorf("the write of %s took %s to arrive; the notification did not wake the poll", name, d)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no event for %s in 5 s with a poll interval of an hour: the notification did not wake the poll", name)
		}
	}
}
