package dynamo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	mrand "math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/watch"
)

type action int

const (
	actCreate action = iota
	actUpdate
	actDelete
)

// write describes one write to one object.
type write struct {
	sk     string
	action action
	// expectRV is the version that the server read, for an update or a
	// delete.
	expectRV uint64
	// value is the new object for a create or an update, and the deleted
	// object for a delete. Objects are stored with no resource version.
	value []byte
	// prev is the previous object for an update or a delete.
	prev []byte
}

// errObjectConflict means that the object condition of a write failed.
var errObjectConflict = errors.New("dynamo: object condition failed")

// maxCounterRetries bounds the retries after a counter conflict.
const maxCounterRetries = 200

// maxBatch is the most writes one transaction carries. A transaction
// holds up to 100 items, and each write takes two, plus the counter.
const maxBatch = 25

// pending is a write that waits for the committer.
type pending struct {
	w    write
	done chan result
}

type result struct {
	rv  uint64
	err error
}

// batcher collects the writes of this server to one resource, so that
// the committer can carry them in one transaction, spec 2.3. The first
// writer that finds no committer running becomes it, and it runs until
// the queue is empty.
type batcher struct {
	mu      sync.Mutex
	queue   []*pending
	running bool
}

//= spec/solas.md#2-3-writes
//# A call MAY carry several writes of one resource that arrived at the
//# server together.

// commit queues one write and waits for its resource version. The
// committer applies it with the other writes that arrived meanwhile, in
// one transaction.
func (s *store) commit(ctx context.Context, w write) (uint64, error) {
	p := &pending{w: w, done: make(chan result, 1)}
	s.batch.mu.Lock()
	s.batch.queue = append(s.batch.queue, p)
	lead := !s.batch.running
	if lead {
		s.batch.running = true
	}
	s.batch.mu.Unlock()
	if lead {
		// The committer outlives this request: a write that the caller
		// gives up on may still commit, as a call in flight may today.
		go s.runBatches()
	}
	select {
	case r := <-p.done:
		return r.rv, r.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// take returns the next batch of pending writes, or nil and clears the
// running flag when the queue is empty.
func (s *store) take() []*pending {
	s.batch.mu.Lock()
	defer s.batch.mu.Unlock()
	if len(s.batch.queue) == 0 {
		s.batch.running = false
		return nil
	}
	n := min(len(s.batch.queue), maxBatch)
	batch := s.batch.queue[:n:n]
	s.batch.queue = s.batch.queue[n:]
	return batch
}

// runBatches commits batches until the queue is empty.
func (s *store) runBatches() {
	for batch := s.take(); batch != nil; batch = s.take() {
		s.commitBatch(batch)
	}
}

// commitTimeout bounds one batch, with its retries. The callers have
// their own contexts; the committer serves them all.
const commitTimeout = 2 * time.Minute

// commitBatch applies the writes of a batch in one transaction and
// answers each one. A write whose object condition fails leaves the
// batch with a conflict, and the rest try again.
func (s *store) commitBatch(batch []*pending) {
	ctx, cancel := context.WithTimeout(context.Background(), commitTimeout)
	defer cancel()
	fail := func(err error) {
		for _, p := range batch {
			p.done <- result{err: err}
		}
	}
	for attempt := 0; len(batch) > 0; attempt++ {
		n, sealed, err := s.readCounterSealed(ctx)
		if err != nil {
			fail(err)
			return
		}
		//= spec/solas.md#12-1-seal
		//# A sealed store MUST reject every write.
		if sealed {
			fail(errSealed(s.resource))
			return
		}
		//= spec/solas.md#3-1-issue
		//# Each write MUST get a resource version exactly one more than the
		//# previous write to the same resource.
		//= spec/solas.md#3-2-order
		//# The resource version of a resource MUST strictly increase in commit
		//# order.
		//= spec/solas.md#2-3-writes
		//# The writes of a call MUST get the versions `n + 1` to `n + k`, in the
		//# order of the call.
		_, err = s.client.TransactWriteItems(ctx, s.transaction(batch, n))
		if err == nil {
			batchStats.transactions.Add(1)
			batchStats.writes.Add(int64(len(batch)))
		}
		if err == nil {
			for i, p := range batch {
				p.done <- result{rv: n + 1 + uint64(i)}
			}
			return
		}
		conflicts, retry := classify(err, len(batch))
		if len(conflicts) > 0 {
			//= spec/solas.md#2-3-writes
			//# The other writes of the call MUST retry in a new call.
			rest := batch[:0:0]
			for i, p := range batch {
				if conflicts[i] {
					p.done <- result{err: errObjectConflict}
				} else {
					rest = append(rest, p)
				}
			}
			batch = rest
			continue
		}
		if retry && attempt < maxCounterRetries {
			//= spec/solas.md#2-3-writes
			//# If the transaction fails only on the counter condition, the server MUST
			//# read the counter again and retry the same writes.
			//= spec/solas.md#2-3-writes
			//# The server SHOULD wait a short random time before each such retry.
			if err := sleepJitter(ctx, attempt); err != nil {
				fail(err)
				return
			}
			continue
		}
		if len(batch) > 1 {
			// The error does not name a write. Each one goes alone, so the
			// write at fault gets its error and the others go through.
			for _, p := range batch {
				s.commitBatch([]*pending{p})
			}
			return
		}
		//= spec/solas.md#2-3-writes
		//# The server MUST reject a write whose event item would exceed the
		//# DynamoDB item size limit.
		if isItemTooLarge(err) {
			//= spec/solas.md#2-3-writes
			//# The server MUST return `413 RequestEntityTooLarge` for such a write.
			fail(apierrors.NewRequestEntityTooLargeError(err.Error()))
			return
		}
		fail(err)
		return
	}
}

// transaction builds the actions of a batch, spec 2.3: the counter
// update, then an object action and an event put for each write.
func (s *store) transaction(batch []*pending, n uint64) *dynamodb.TransactWriteItemsInput {
	//= spec/solas.md#2-3-writes
	//# Every write MUST be part of one `TransactWriteItems` call.
	//= spec/solas.md#2-3-writes
	//# A call MUST hold one counter update and, for each write that it
	//# carries, an object action and an event put.
	//= spec/solas.md#2-3-writes
	//# The counter update MUST set `n` to `n + k` on condition that `n` still
	//# has the value that the server read.
	items := make([]types.TransactWriteItem, 0, 1+2*len(batch))
	items = append(items, s.counterAction(n, n+uint64(len(batch))))
	for i, p := range batch {
		next := n + 1 + uint64(i)
		items = append(items, s.objectAction(p.w, next), s.eventAction(p.w, next))
	}
	return &dynamodb.TransactWriteItemsInput{
		//= spec/solas.md#2-3-writes
		//# Each call MUST set a `ClientRequestToken`.
		ClientRequestToken: aws.String(newToken()),
		TransactItems:      items,
	}
}

func (s *store) counterAction(n, next uint64) types.TransactWriteItem {
	pk, sk := s.counterKey()
	key := map[string]types.AttributeValue{attrPK: str(pk), attrSK: str(sk)}
	//= spec/solas.md#2-3-writes
	//# The counter update MUST set `n` to `n + k` on condition that `n` still
	//# has the value that the server read.

	//= spec/solas.md#12-1-seal
	//# A write MUST fail when the counter item of its resource has the
	//# attribute `sealed`.
	//
	// The seal check is part of the counter condition, so a write cannot
	// commit after the seal, even when it read the counter before it.
	return types.TransactWriteItem{Update: &types.Update{
		TableName:                 aws.String(s.table),
		Key:                       key,
		UpdateExpression:          aws.String("SET #n = :next"),
		ConditionExpression:       aws.String("#n = :cur AND attribute_not_exists(#sealed)"),
		ExpressionAttributeNames:  map[string]string{"#n": attrN, "#sealed": attrSealed},
		ExpressionAttributeValues: map[string]types.AttributeValue{":next": num(next), ":cur": num(n)},
	}}
}

func (s *store) objectAction(w write, next uint64) types.TransactWriteItem {
	key := map[string]types.AttributeValue{attrPK: str(s.objectPK()), attrSK: str(w.sk)}
	item := map[string]types.AttributeValue{
		attrPK: str(s.objectPK()),
		attrSK: str(w.sk),
		//= spec/solas.md#2-2-items
		//# An object item MUST hold the resource version of its last write in the
		//# number attribute `rv`.

		//= spec/solas.md#3-3-objects
		//# The `rv` attribute of the object item MUST equal the same version.
		attrRV: num(next),
		//= spec/solas.md#2-2-items
		//# An object item MUST hold the encoded object in the binary attribute
		//# `value`.
		attrValue: &types.AttributeValueMemberB{Value: w.value},
	}
	rvMatches := map[string]types.AttributeValue{":rv": num(w.expectRV)}
	names := map[string]string{"#rv": attrRV}
	switch w.action {
	case actCreate:
		//= spec/solas.md#2-3-writes
		//# For a create, the object action MUST be a put on condition that the
		//# object item does not exist.
		return types.TransactWriteItem{Put: &types.Put{
			TableName:           aws.String(s.table),
			Item:                item,
			ConditionExpression: aws.String("attribute_not_exists(pk)"),
		}}
	case actUpdate:
		//= spec/solas.md#2-3-writes
		//# For an update, the object action MUST be a put on condition that `rv`
		//# equals the version that the server read.
		return types.TransactWriteItem{Put: &types.Put{
			TableName:                 aws.String(s.table),
			Item:                      item,
			ConditionExpression:       aws.String("#rv = :rv"),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: rvMatches,
		}}
	default:
		//= spec/solas.md#2-3-writes
		//# For a delete, the object action MUST be a delete on condition that `rv`
		//# equals the version that the server read.
		return types.TransactWriteItem{Delete: &types.Delete{
			TableName:                 aws.String(s.table),
			Key:                       key,
			ConditionExpression:       aws.String("#rv = :rv"),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: rvMatches,
		}}
	}
}

func (s *store) eventAction(w write, next uint64) types.TransactWriteItem {
	//= spec/solas.md#2-2-items
	//# The event type MUST be one of `INIT`, `ADDED`, `MODIFIED`, or `DELETED`.
	eventType := map[action]watch.EventType{
		actCreate: watch.Added,
		actUpdate: watch.Modified,
		actDelete: watch.Deleted,
	}[w.action]
	//= spec/solas.md#4-1-event-log
	//# Each write MUST add one event item to the event log of its resource.

	//= spec/solas.md#4-1-event-log
	//# The event item MUST have the same resource version as the write.
	item := map[string]types.AttributeValue{
		attrPK: str(s.eventPK()),
		attrSK: str(eventSK(next)),
		//= spec/solas.md#2-2-items
		//# An event item MUST hold the event type in the attribute `type`.
		attrType: str(string(eventType)),
		attrKey:  str(w.sk),
		//= spec/solas.md#2-2-items
		//# An `ADDED`, `MODIFIED`, or `DELETED` event item MUST hold the encoded
		//# object in `value`.
		attrValue: &types.AttributeValueMemberB{Value: w.value},
		//= spec/solas.md#2-2-items
		//# An event item MUST hold its expiry time, in Unix seconds, in `expires`.

		//= spec/solas.md#4-1-event-log
		//# An event item MUST expire a set retention time after its write.
		attrExpires: num(uint64(s.now().Add(s.retention).Unix())),
	}
	if w.action != actCreate {
		//= spec/solas.md#2-2-items
		//# An event item for a `MODIFIED` or `DELETED` event MUST hold the encoded
		//# previous object in `prev`.
		item[attrPrev] = &types.AttributeValueMemberB{Value: w.prev}
	}
	//= spec/solas.md#2-3-writes
	//# The event put MUST be on condition that the event item does not exist.
	return types.TransactWriteItem{Put: &types.Put{
		TableName:           aws.String(s.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	}}
}

// readCounter returns the last issued resource version. It creates the
// counter when it does not exist. Lists and watches read a sealed counter
// as any other, because a sealed store serves reads, spec 12.1.
func (s *store) readCounter(ctx context.Context) (uint64, error) {
	n, _, err := s.readCounterSealed(ctx)
	return n, err
}

// initCounter creates the counter at 1 with an INIT event at version 1.
// Kubernetes rejects 0 as the resource version of a list, so the counter
// never shows 0. The INIT event keeps the event log free of gaps.
func (s *store) initCounter(ctx context.Context) error {
	pk, sk := s.counterKey()
	//= spec/solas.md#3-1-issue
	//# When the server first uses a resource, it MUST create the counter item
	//# with `n` equal to 1 and an event item of type `INIT` at version 1, in one
	//# transaction.
	_, err := s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		ClientRequestToken: aws.String(newToken()),
		TransactItems: []types.TransactWriteItem{
			{Put: &types.Put{
				TableName: aws.String(s.table),
				//= spec/solas.md#2-2-items
				//# A counter item MUST hold the last issued resource version in the number
				//# attribute `n`.
				Item:                map[string]types.AttributeValue{attrPK: str(pk), attrSK: str(sk), attrN: num(1)},
				ConditionExpression: aws.String("attribute_not_exists(pk)"),
			}},
			{Put: &types.Put{
				TableName: aws.String(s.table),
				Item: map[string]types.AttributeValue{
					attrPK:      str(s.eventPK()),
					attrSK:      str(eventSK(1)),
					attrType:    str(eventInit),
					attrExpires: num(uint64(s.now().Add(s.retention).Unix())),
				},
				ConditionExpression: aws.String("attribute_not_exists(pk)"),
			}},
		},
	})
	var tce *types.TransactionCanceledException
	if err != nil && !errors.As(err, &tce) {
		return fmt.Errorf("create counter: %w", err)
	}
	// Success, or another server created the counter first.
	return nil
}

// classify reads the cancellation reasons of a failed transaction of k
// writes. The reasons are in the order of the actions: the counter, then
// the object and the event of each write. It returns, for each write,
// whether its object condition failed, and whether the whole batch should
// try again: a counter condition that failed, or a transaction conflict.
func classify(err error, k int) (conflicts []bool, retry bool) {
	var tce *types.TransactionCanceledException
	if !errors.As(err, &tce) {
		var tcx *types.TransactionConflictException
		return nil, errors.As(err, &tcx)
	}
	code := func(i int) string {
		if i < len(tce.CancellationReasons) {
			return aws.ToString(tce.CancellationReasons[i].Code)
		}
		return ""
	}
	any := false
	conflicts = make([]bool, k)
	for i := range k {
		if code(1+2*i) == "ConditionalCheckFailed" {
			conflicts[i], any = true, true
		}
	}
	if any {
		return conflicts, false
	}
	for i := range tce.CancellationReasons {
		switch code(i) {
		case "ConditionalCheckFailed", "TransactionConflict":
			return nil, true
		}
	}
	return nil, false
}

func isItemTooLarge(err error) bool {
	return strings.Contains(err.Error(), "Item size has exceeded") ||
		strings.Contains(err.Error(), "Item size to update has exceeded")
}

// sleepJitter waits a random time that grows with the attempt, up to 50 ms.
func sleepJitter(ctx context.Context, attempt int) error {
	ceiling := time.Millisecond << min(attempt, 6)
	d := time.Duration(mrand.Int64N(int64(ceiling)) + 1)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on supported platforms.
		n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
		return n.String()
	}
	return hex.EncodeToString(b)
}

func str(v string) *types.AttributeValueMemberS { return &types.AttributeValueMemberS{Value: v} }

func num(v uint64) *types.AttributeValueMemberN {
	return &types.AttributeValueMemberN{Value: strconv.FormatUint(v, 10)}
}

func numAttr(item map[string]types.AttributeValue, name string) (uint64, error) {
	n, ok := item[name].(*types.AttributeValueMemberN)
	if !ok {
		return 0, fmt.Errorf("attribute %s is not a number", name)
	}
	return strconv.ParseUint(n.Value, 10, 64)
}

func strAttr(item map[string]types.AttributeValue, name string) string {
	if s, ok := item[name].(*types.AttributeValueMemberS); ok {
		return s.Value
	}
	return ""
}

func binAttr(item map[string]types.AttributeValue, name string) []byte {
	if b, ok := item[name].(*types.AttributeValueMemberB); ok {
		return b.Value
	}
	return nil
}

// batchStats counts the transactions and the writes they carried, for
// the benchmark's writes per transaction column.
var batchStats struct{ transactions, writes atomic.Int64 }

// BatchStats returns how many transactions the stores of this process
// committed and how many writes they carried.
func BatchStats() (transactions, writes int64) {
	return batchStats.transactions.Load(), batchStats.writes.Load()
}
