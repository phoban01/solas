package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	storemetrics "github.com/phoban01/solas/pkg/storage/metrics"
)

type action int

const (
	actCreate action = iota
	actUpdate
	actDelete
)

// name is the action as the commit function reads it.
func (a action) name() string {
	switch a {
	case actCreate:
		return "create"
	case actUpdate:
		return "update"
	default:
		return "delete"
	}
}

// write describes one write to one object.
type write struct {
	key    string
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
var errObjectConflict = errors.New("postgres: object condition failed")

// maxBatch is the most writes one transaction carries.
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
// the committer can carry them in one transaction, spec 2.3 and 2.8. The
// first writer that finds no committer running becomes it, and it runs
// until the queue is empty. The row lock orders the batches of the
// servers, so there is no counter guess and no counter retry.
type batcher struct {
	mu      sync.Mutex
	queue   []*pending
	running bool
}

//= spec/solas.md#2-8-postgres-store
//# A transaction MAY carry several writes of one resource that arrived at
//# the server together, as in section 2.3.

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
	for len(batch) > 0 {
		//= spec/solas.md#3-1-issue
		//# Each write MUST get a resource version exactly one more than the
		//# previous write to the same resource.
		//= spec/solas.md#3-2-order
		//# The resource version of a resource MUST strictly increase in commit
		//# order.
		//= spec/solas.md#2-8-postgres-store
		//# The writes of the transaction MUST get the versions `n + 1` to `n + k`,
		//# in the order of the transaction.
		base, conflicts, err := s.callCommit(ctx, batch)
		if err == nil {
			batchStats.transactions.Add(1)
			batchStats.writes.Add(int64(len(batch)))
			for i, p := range batch {
				p.done <- result{rv: base + 1 + uint64(i)}
			}
			return
		}
		if len(conflicts) > 0 {
			//= spec/solas.md#2-8-postgres-store
			//# The other writes of the transaction MUST retry in a new transaction.
			rest := batch[:0:0]
			for i, p := range batch {
				if conflicts[i] {
					storemetrics.Conflicts.WithLabelValues(s.groupResource.String(), storemetrics.CauseObject).Inc()
					p.done <- result{err: errObjectConflict}
				} else {
					rest = append(rest, p)
				}
			}
			batch = rest
			continue
		}
		fail(err)
		return
	}
}

//= spec/solas.md#2-8-postgres-store
//# Every write MUST be one transaction.

//= spec/solas.md#2-8-postgres-store
//# The transaction MUST first update the `counters` row of the resource,
//# on condition that `sealed` is false, and set `n` to `n + k`.

//= spec/solas.md#2-8-postgres-store
//# The update takes the row lock, and Postgres holds the lock until the
//# transaction ends.

//= spec/solas.md#2-8-postgres-store
//# The transaction MUST then run one conditional statement for each write.

//= spec/solas.md#2-8-postgres-store
//# For a create, the statement MUST be an insert that fails when the key
//# exists.

//= spec/solas.md#2-8-postgres-store
//# For an update, the statement MUST be an update on condition that `rv`
//# equals the version that the server read.

//= spec/solas.md#2-8-postgres-store
//# For a delete, the statement MUST be a delete on condition that `rv`
//# equals the version that the server read.

//= spec/solas.md#2-8-postgres-store
//# A statement that changes no row MUST roll the transaction back, the
//# counter update with it.

//= spec/solas.md#2-8-postgres-store
//# The transaction MUST insert one `events` row for each write, with the
//# version of the write.

//= spec/solas.md#4-1-event-log
//# Each write MUST add one event item to the event log of its resource.

//= spec/solas.md#4-1-event-log
//# The event item MUST have the same resource version as the write.

//= spec/solas.md#4-1-event-log
//# An event item MUST expire a set retention time after its write.

//= spec/solas.md#3-1-issue
//# When the server first uses a resource, it MUST create the counter item
//# with `n` equal to 1 and an event item of type `INIT` at version 1, in one
//# transaction.

// callCommit runs the commit function of the schema with the writes of
// a batch, and returns the counter before the batch. On a conflict it
// returns, for each write, whether its object condition failed. On a
// sealed store it returns the error of spec 12.1.
func (s *store) callCommit(ctx context.Context, batch []*pending) (base uint64, conflicts []bool, err error) {
	k := len(batch)
	keys := make([]string, k)
	actions := make([]string, k)
	expects := make([]int64, k)
	vals := make([][]byte, k)
	prevs := make([][]byte, k)
	for i, p := range batch {
		keys[i] = p.w.key
		actions[i] = p.w.action.name()
		expects[i] = int64(p.w.expectRV)
		vals[i] = p.w.value
		prevs[i] = p.w.prev
	}
	var n int64
	err = s.pool.QueryRow(ctx, s.q.commit, s.resource, keys, actions, expects, vals, prevs, s.now().Add(s.retention)).Scan(&n)
	if err == nil {
		return uint64(n), nil, nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return 0, nil, fmt.Errorf("commit: %w", err)
	}
	switch pgErr.Code {
	case codeSealed:
		return 0, nil, errSealed(s.resource)
	case codeConflict:
		conflicts = make([]bool, k)
		for _, f := range strings.Split(strings.TrimSuffix(pgErr.Detail, ","), ",") {
			i, err := strconv.Atoi(f)
			if err != nil || i < 1 || i > k {
				return 0, nil, fmt.Errorf("commit: the conflict detail %q names no write of %d", pgErr.Detail, k)
			}
			conflicts[i-1] = true
		}
		return 0, conflicts, errObjectConflict
	}
	return 0, nil, fmt.Errorf("commit: %w", err)
}

// readCounter returns the last issued resource version. It creates the
// counter when it does not exist. Lists and watches read a sealed counter
// as any other, because a sealed store serves reads, spec 12.1.
func (s *store) readCounter(ctx context.Context) (uint64, error) {
	n, _, err := s.readCounterSealed(ctx)
	return n, err
}

// readCounterSealed returns the last issued resource version and whether
// the resource is sealed. It creates the counter when it does not exist.
func (s *store) readCounterSealed(ctx context.Context) (uint64, bool, error) {
	for {
		var n int64
		var sealed bool
		err := s.pool.QueryRow(ctx, s.q.readCounter, s.resource).Scan(&n, &sealed)
		if err == nil {
			return uint64(n), sealed, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, false, fmt.Errorf("read counter: %w", err)
		}
		if err := s.initCounter(ctx); err != nil {
			return 0, false, err
		}
	}
}

// initCounter creates the counter at 1 with an INIT event at version 1,
// in one transaction. Kubernetes rejects 0 as the resource version of a
// list, so the counter never shows 0. The INIT event keeps the event log
// free of gaps. Another server may create the counter first; then this
// does nothing.
func (s *store) initCounter(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create counter: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, s.q.initCounter, s.resource)
	if err != nil {
		return fmt.Errorf("create counter: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, s.q.initEvent, s.resource, s.now().Add(s.retention)); err != nil {
			return fmt.Errorf("create counter: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create counter: %w", err)
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
