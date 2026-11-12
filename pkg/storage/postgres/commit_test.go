package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
)

// eventVersions returns the versions in the event log of the store.
func eventVersions(t *testing.T, s *store) []uint64 {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), "SELECT rv FROM "+s.schema+".events WHERE resource = $1 ORDER BY rv", s.resource)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []uint64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, uint64(v))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

//= spec/solas.md#3-1-issue
//= type=test
//# The first write to a resource MUST get resource version 2.

func TestCommitSequence(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rv1, err := s.commit(ctx, write{key: "/a", action: actCreate, value: []byte("1")})
	if err != nil || rv1 != 2 {
		t.Fatalf("create = %d, %v; want 2, nil", rv1, err)
	}
	rv2, err := s.commit(ctx, write{key: "/a", action: actUpdate, expectRV: 2, value: []byte("2"), prev: []byte("1")})
	if err != nil || rv2 != 3 {
		t.Fatalf("update = %d, %v; want 3, nil", rv2, err)
	}
	rv3, err := s.commit(ctx, write{key: "/a", action: actDelete, expectRV: 3, value: []byte("2"), prev: []byte("2")})
	if err != nil || rv3 != 4 {
		t.Fatalf("delete = %d, %v; want 4, nil", rv3, err)
	}
	if got := eventVersions(t, s); fmt.Sprint(got) != "[1 2 3 4]" {
		t.Errorf("event log = %v, want [1 2 3 4] (1 is INIT)", got)
	}
	if _, _, found, err := s.readObject(ctx, "/a"); err != nil || found {
		t.Errorf("readObject after the delete = found %v, %v; want not found", found, err)
	}
}

//= spec/solas.md#2-8-postgres-store
//= type=test
//# For a create, the statement MUST be an insert that fails when the key
//# exists.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# For an update, the statement MUST be an update on condition that `rv`
//# equals the version that the server read.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# For a delete, the statement MUST be a delete on condition that `rv`
//# equals the version that the server read.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# A statement that changes no row MUST roll the transaction back, the
//# counter update with it.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# The versions of a rolled back transaction are never issued, so the
//# event log has no hole.

func TestCommitObjectConflict(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.commit(ctx, write{key: "/a", action: actCreate, value: []byte("1")}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		w    write
	}{
		{"create existing", write{key: "/a", action: actCreate, value: []byte("x")}},
		{"update stale", write{key: "/a", action: actUpdate, expectRV: 7, value: []byte("x"), prev: []byte("1")}},
		{"delete stale", write{key: "/a", action: actDelete, expectRV: 7, value: []byte("1"), prev: []byte("1")}},
		{"update missing", write{key: "/b", action: actUpdate, expectRV: 1, value: []byte("x"), prev: []byte("1")}},
	}
	for _, tt := range tests {
		if _, err := s.commit(ctx, tt.w); !errors.Is(err, errObjectConflict) {
			t.Errorf("%s: err = %v, want errObjectConflict", tt.name, err)
		}
	}
	if n, err := s.readCounter(ctx); err != nil || n != 2 {
		t.Errorf("counter = %d, %v; want 2, nil after failed writes", n, err)
	}
	if got := eventVersions(t, s); fmt.Sprint(got) != "[1 2]" {
		t.Errorf("event log = %v, want [1 2]", got)
	}
}

// TestCommitBatchConflictSplits puts a conflict and a good write in one
// batch: the conflict gets its error, the good write commits in a new
// transaction, and the log has no hole.
func TestCommitBatchConflictSplits(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.commit(ctx, write{key: "/a", action: actCreate, value: []byte("1")}); err != nil {
		t.Fatal(err)
	}
	batch := []*pending{
		{w: write{key: "/a", action: actCreate, value: []byte("x")}, done: make(chan result, 1)},
		{w: write{key: "/b", action: actCreate, value: []byte("y")}, done: make(chan result, 1)},
	}
	s.commitBatch(batch)
	if r := <-batch[0].done; !errors.Is(r.err, errObjectConflict) {
		t.Errorf("the create of an existing key = %+v, want errObjectConflict", r)
	}
	if r := <-batch[1].done; r.err != nil || r.rv != 3 {
		t.Errorf("the good write = %+v, want version 3", r)
	}
	if got := eventVersions(t, s); fmt.Sprint(got) != "[1 2 3]" {
		t.Errorf("event log = %v, want [1 2 3]", got)
	}
}

//= spec/solas.md#3-1-issue
//= type=test
//# Each write MUST get a resource version exactly one more than the
//# previous write to the same resource.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# So the transactions of one resource commit one at a time, in the order
//# in which they took the lock.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# The commit order of the writes of a resource is their version order.

//= spec/solas.md#4-1-event-log
//= type=test
//# Each write MUST add one event item to the event log of its resource.

// TestCommitConcurrentWritersHaveNoGaps runs two stores on one resource,
// as two servers do, with writers on each: every version from 2 up is
// issued once and the log holds each one.
func TestCommitConcurrentWritersHaveNoGaps(t *testing.T) {
	ctx := context.Background()
	s1 := newTestStore(t)
	s2, err := newStore(Config{Pool: s1.pool}, codec, newPod, newPodList, s1.prefix, "/pods/", testGR)
	if err != nil {
		t.Fatal(err)
	}
	s2.schema, s2.q = s1.schema, s1.q

	const writers, each = 8, 5
	var mu sync.Mutex
	var rvs []uint64
	var wg sync.WaitGroup
	for w := range writers {
		s := s1
		if w%2 == 1 {
			s = s2
		}
		wg.Go(func() {
			for i := range each {
				rv, err := s.commit(ctx, write{key: fmt.Sprintf("/w%d-%d", w, i), action: actCreate, value: []byte("x")})
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				rvs = append(rvs, rv)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	sort.Slice(rvs, func(i, j int) bool { return rvs[i] < rvs[j] })
	for i, rv := range rvs {
		if rv != uint64(i+2) {
			t.Fatalf("versions = %v, want 2 to %d with no gap", rvs, writers*each+1)
		}
	}
	if got := eventVersions(t, s1); len(got) != writers*each+1 {
		t.Errorf("event log has %d versions, want %d", len(got), writers*each+1)
	}
	if n, err := s1.readCounter(ctx); err != nil || n != writers*each+1 {
		t.Errorf("counter = %d, %v; want %d", n, err, writers*each+1)
	}
}

//= spec/solas.md#12-1-seal
//= type=test
//# A sealed store MUST reject every write.

//= spec/solas.md#2-8-postgres-store
//= type=test
//# When the counter update changes no row, the store is sealed, section
//# 12.1, and the write MUST fail with `503 ServiceUnavailable`.

func TestCommitSealed(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.commit(ctx, write{key: "/a", action: actCreate, value: []byte("1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE "+s.schema+".counters SET sealed = true WHERE resource = $1", s.resource); err != nil {
		t.Fatal(err)
	}
	_, err := s.commit(ctx, write{key: "/b", action: actCreate, value: []byte("2")})
	if !IsSealed(err) {
		t.Fatalf("write to a sealed store = %v, want the sealed error", err)
	}
	//= spec/solas.md#12-1-seal
	//= type=test
	//# A sealed store MUST serve reads.
	if n, err := s.readCounter(ctx); err != nil || n != 2 {
		t.Errorf("counter = %d, %v; want 2, nil", n, err)
	}
	if _, _, found, err := s.readObject(ctx, "/a"); err != nil || !found {
		t.Errorf("read on a sealed store = found %v, %v; want found", found, err)
	}
}

// TestCommitInitOnRead shows that a read of a new resource creates the
// counter at 1 with the INIT event, and that a write after it gets 2.
func TestCommitInitOnRead(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if n, err := s.readCounter(ctx); err != nil || n != 1 {
		t.Fatalf("first read of the counter = %d, %v; want 1, nil", n, err)
	}
	if got := eventVersions(t, s); fmt.Sprint(got) != "[1]" {
		t.Errorf("event log = %v, want [1]", got)
	}
	if rv, err := s.commit(ctx, write{key: "/a", action: actCreate, value: []byte("1")}); err != nil || rv != 2 {
		t.Errorf("first write = %d, %v; want 2, nil", rv, err)
	}
}
