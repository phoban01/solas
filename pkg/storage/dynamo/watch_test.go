package dynamo

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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

// deleteEvent removes an event item, as DynamoDB TTL would.
func deleteEvent(t *testing.T, s *store, rv uint64) {
	t.Helper()
	_, err := s.client.(*dynamodb.Client).DeleteItem(context.Background(), &dynamodb.DeleteItemInput{
		TableName: aws.String(s.table),
		Key:       map[string]types.AttributeValue{attrPK: str(s.eventPK()), attrSK: str(eventSK(rv))},
	})
	if err != nil {
		t.Fatal(err)
	}
}

//= spec/solas.md#4-3-gaps
//= type=test
//# If the query result does not hold each version from `last + 1` to `c`
//# exactly once, the watch MUST end with `410 Gone`.

//= spec/solas.md#4-1-event-log
//= type=test
//# The server MUST NOT depend on the time at which DynamoDB deletes an
//# expired item.

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
