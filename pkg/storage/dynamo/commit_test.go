package dynamo

import (
	"context"
	"errors"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/apis/example"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// eventVersions returns the versions in the event log of the store.
func eventVersions(t *testing.T, s *store) []uint64 {
	t.Helper()
	var got []uint64
	var start map[string]types.AttributeValue
	for {
		out, err := s.client.Query(context.Background(), &dynamodb.QueryInput{
			TableName:                 aws.String(s.table),
			KeyConditionExpression:    aws.String("pk = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(s.eventPK())},
			ConsistentRead:            aws.Bool(true),
			ExclusiveStartKey:         start,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range out.Items {
			var v uint64
			if _, err := fmt.Sscanf(strAttr(item, attrSK), "%d", &v); err != nil {
				t.Fatal(err)
			}
			got = append(got, v)
		}
		if out.LastEvaluatedKey == nil {
			return got
		}
		start = out.LastEvaluatedKey
	}
}

//= spec/solas.md#3-1-issue
//= type=test
//# The first write to a resource MUST get resource version 2.

func TestCommitSequence(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rv1, err := s.commit(ctx, write{sk: "/a", action: actCreate, value: []byte("1")})
	if err != nil || rv1 != 2 {
		t.Fatalf("create = %d, %v; want 2, nil", rv1, err)
	}
	rv2, err := s.commit(ctx, write{sk: "/a", action: actUpdate, expectRV: 2, value: []byte("2"), prev: []byte("1")})
	if err != nil || rv2 != 3 {
		t.Fatalf("update = %d, %v; want 3, nil", rv2, err)
	}
	rv3, err := s.commit(ctx, write{sk: "/a", action: actDelete, expectRV: 3, value: []byte("2"), prev: []byte("2")})
	if err != nil || rv3 != 4 {
		t.Fatalf("delete = %d, %v; want 4, nil", rv3, err)
	}
	if got := eventVersions(t, s); fmt.Sprint(got) != "[1 2 3 4]" {
		t.Errorf("event log = %v, want [1 2 3 4] (1 is INIT)", got)
	}
}

//= spec/solas.md#2-3-writes
//= type=test
//# For an update, the object action MUST be a put on condition that `rv`
//# equals the version that the server read.

//= spec/solas.md#2-3-writes
//= type=test
//# For a delete, the object action MUST be a delete on condition that `rv`
//# equals the version that the server read.

func TestCommitObjectConflict(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.commit(ctx, write{sk: "/a", action: actCreate, value: []byte("1")}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		w    write
	}{
		{"create existing", write{sk: "/a", action: actCreate, value: []byte("x")}},
		{"update stale", write{sk: "/a", action: actUpdate, expectRV: 7, value: []byte("x"), prev: []byte("1")}},
		{"delete stale", write{sk: "/a", action: actDelete, expectRV: 7, value: []byte("1"), prev: []byte("1")}},
		{"update missing", write{sk: "/b", action: actUpdate, expectRV: 1, value: []byte("x"), prev: []byte("1")}},
	}
	for _, tt := range tests {
		if _, err := s.commit(ctx, tt.w); !errors.Is(err, errObjectConflict) {
			t.Errorf("%s: err = %v, want errObjectConflict", tt.name, err)
		}
	}
	if n, err := s.readCounter(ctx); err != nil || n != 2 {
		t.Errorf("counter = %d, %v; want 2, nil after failed writes", n, err)
	}
}

//= spec/solas.md#3-1-issue
//= type=test
//# Each write MUST get a resource version exactly one more than the
//# previous write to the same resource.

//= spec/solas.md#4-1-event-log
//= type=test
//# Each write MUST add one event item to the event log of its resource.

func TestCommitConcurrentWritersHaveNoGaps(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	const writers, each = 8, 5
	var mu sync.Mutex
	var rvs []uint64
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				rv, err := s.commit(ctx, write{sk: fmt.Sprintf("/w%d-%d", w, i), action: actCreate, value: []byte("x")})
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
			t.Fatalf("versions = %v, want 2 to %d with no gaps or repeats", rvs, writers*each+1)
		}
	}
	if got := eventVersions(t, s); len(got) != writers*each+1 || got[len(got)-1] != writers*each+1 {
		t.Errorf("event log = %v, want 1 to %d", got, writers*each+1)
	}
}

// countingAPI counts the transactions the store sends and the items it
// reads. Between two commits with no watch and no list, every read is a
// counter read.
type countingAPI struct {
	API
	transactions atomic.Int64
	reads        atomic.Int64
}

func (c *countingAPI) GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	c.reads.Add(1)
	return c.API.GetItem(ctx, in, opts...)
}

func (c *countingAPI) TransactWriteItems(ctx context.Context, in *dynamodb.TransactWriteItemsInput, opts ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	c.transactions.Add(1)
	return c.API.TransactWriteItems(ctx, in, opts...)
}

//= spec/solas.md#2-3-writes
//= type=test
//# A call MAY carry several writes of one resource that arrived at the
//# server together.

//= spec/solas.md#2-3-writes
//= type=test
//# The writes of a call MUST get the versions `n + 1` to `n + k`, in the
//# order of the call.

// TestCommitBatchesConcurrentWrites: forty writers that start together
// commit in fewer transactions than writes, every write gets its own
// version, the versions are consecutive, and the event log has no gap.
func TestCommitBatchesConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	counting := &countingAPI{API: s.client}
	s.client = counting
	createPods(t, s, "seed") // version 2, and the counter exists
	before := counting.transactions.Load()

	const writers = 40
	versions := make(chan uint64, writers)
	errs := make(chan error, writers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			name := fmt.Sprintf("p%02d", i)
			out := &example.Pod{}
			if err := s.Create(ctx, "/pods/ns/"+name, &example.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"}}, out, 0); err != nil {
				errs <- err
				return
			}
			rv, err := s.versioner.ObjectResourceVersion(out)
			if err != nil {
				errs <- err
				return
			}
			versions <- rv
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(versions)
	for err := range errs {
		t.Fatal(err)
	}
	var got []uint64
	for rv := range versions {
		got = append(got, rv)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i, rv := range got {
		if rv != 3+uint64(i) {
			t.Fatalf("versions = %v, want 3 to %d with no gap", got, 2+writers)
		}
	}
	if n := counting.transactions.Load() - before; n >= writers {
		t.Errorf("%d writes took %d transactions; batching should take fewer", writers, n)
	} else {
		t.Logf("%d writes in %d transactions", writers, n)
	}
	if evs := eventVersions(t, s); len(evs) != 2+writers {
		t.Errorf("event log has %d events, want %d", len(evs), 2+writers)
	}
}

//= spec/solas.md#2-3-writes
//= type=test
//# The server MAY use the last counter value that it read or issued as that
//# value, without a new read.

//= spec/solas.md#2-3-writes
//= type=test
//# A wrong value fails only the counter condition, so the retry below reads
//# the counter, and the writes keep their order.

// A server that writes alone commits without a counter read. After a
// write by another server, its guess misses, and it reads, retries, and
// gets the version after the other's. The next batch reads too, once,
// and then the server guesses again.
func TestCommitCounterGuess(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	c := &countingAPI{API: s.client}
	s.client = c
	other, err := newStore(Config{Client: c.API, Table: s.table, PollInterval: time.Hour, ProgressInterval: time.Hour},
		codec, newPod, newPodList, "/", "/pods/", testGR)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.commit(ctx, write{sk: "/a", action: actCreate, value: []byte("1")}); err != nil {
		t.Fatal(err)
	}
	c.reads.Store(0)
	rv, err := s.commit(ctx, write{sk: "/b", action: actCreate, value: []byte("1")})
	if err != nil || rv != 3 {
		t.Fatalf("second create = %d, %v; want 3, nil", rv, err)
	}
	if n := c.reads.Load(); n != 0 {
		t.Errorf("the second commit read the counter %d times, want a guess with no read", n)
	}

	if rv, err := other.commit(ctx, write{sk: "/c", action: actCreate, value: []byte("1")}); err != nil || rv != 4 {
		t.Fatalf("other's create = %d, %v; want 4, nil", rv, err)
	}
	c.reads.Store(0)
	rv, err = s.commit(ctx, write{sk: "/d", action: actCreate, value: []byte("1")})
	if err != nil || rv != 5 {
		t.Fatalf("create after the other's = %d, %v; want 5, nil", rv, err)
	}
	if n := c.reads.Load(); n != 1 {
		t.Errorf("the commit after a miss read the counter %d times, want 1", n)
	}
	c.reads.Store(0)
	if rv, err := s.commit(ctx, write{sk: "/e", action: actCreate, value: []byte("1")}); err != nil || rv != 6 {
		t.Fatalf("create in the holdoff = %d, %v; want 6, nil", rv, err)
	}
	if n := c.reads.Load(); n != 1 {
		t.Errorf("the commit in the holdoff read the counter %d times, want 1", n)
	}
	c.reads.Store(0)
	if rv, err := s.commit(ctx, write{sk: "/f", action: actCreate, value: []byte("1")}); err != nil || rv != 7 {
		t.Fatalf("create after the holdoff = %d, %v; want 7, nil", rv, err)
	}
	if n := c.reads.Load(); n != 0 {
		t.Errorf("the commit after the holdoff read the counter %d times, want a guess", n)
	}
	if got := eventVersions(t, s); fmt.Sprint(got) != "[1 2 3 4 5 6 7]" {
		t.Errorf("event log = %v, want [1 2 3 4 5 6 7]", got)
	}
}
