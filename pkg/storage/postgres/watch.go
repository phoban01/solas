package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/features"
	"k8s.io/apiserver/pkg/storage"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	utilflowcontrol "k8s.io/apiserver/pkg/util/flowcontrol"

	storemetrics "github.com/phoban01/solas/pkg/storage/metrics"
)

// event is one row of the event log.
type event struct {
	rv    uint64
	typ   watch.EventType
	key   string
	value []byte
	prev  []byte
	// written is the time of the write, from the expiry of the event and
	// the retention, for the lag of the delivery.
	written time.Time
}

// watcher delivers the events of one watch. It polls the event log, as
// spec 4.2 gives.
type watcher struct {
	s              *store
	ctx            context.Context
	cancel         context.CancelFunc
	result         chan watch.Event
	key            string
	recursive      bool
	pred           storage.SelectionPredicate
	progressNotify bool
	// progress asks the watcher to send a bookmark, see RequestWatchProgress.
	progress chan struct{}
	// hint wakes the poller with a counter value that the store read for a
	// consistent list, see watchers.hint. hinted holds the highest value.
	hint chan struct{}
	// notified wakes the poller with a notification of the commit, spec
	// 2.8, which keeps the poll interval as the floor between two polls.
	notified chan struct{}
	hinted   atomic.Uint64
}

func (w *watcher) Stop()                          { w.cancel() }
func (w *watcher) ResultChan() <-chan watch.Event { return w.result }

// watchers holds the open watches of a store, for RequestWatchProgress.
type watchers struct {
	mu  sync.Mutex
	set map[*watcher]struct{}
}

func (ws *watchers) add(w *watcher) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.set == nil {
		ws.set = map[*watcher]struct{}{}
	}
	ws.set[w] = struct{}{}
}

func (ws *watchers) remove(w *watcher) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	delete(ws.set, w)
}

func (ws *watchers) requestProgress() {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for w := range ws.set {
		select {
		case w.progress <- struct{}{}:
		default:
		}
	}
}

//= spec/solas.md#2-8-postgres-store
//# A notification MUST wake the poll of its resource, with the version
//# as `c`, as a counter read does in section 4.2.

// notify gives every watcher the version of a notification of the
// commit, spec 2.8. The watcher polls with it, as with a hint, but no
// sooner than the poll interval after its last poll.
func (ws *watchers) notify(c uint64) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for w := range ws.set {
		for {
			old := w.hinted.Load()
			if old >= c || w.hinted.CompareAndSwap(old, c) {
				break
			}
		}
		select {
		case w.notified <- struct{}{}:
		default:
		}
	}
}

// hint gives every watcher a counter value that the store has just read,
// so the next poll can use it without a read of its own, spec 4.2. The
// watch cache reads the counter before a consistent list and then waits
// for the cache to reach it; the hinted poll gets it there after one
// query, with no progress request.
func (ws *watchers) hint(c uint64) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for w := range ws.set {
		for {
			old := w.hinted.Load()
			if old >= c || w.hinted.CompareAndSwap(old, c) {
				break
			}
		}
		select {
		case w.hint <- struct{}{}:
		default:
		}
	}
}

func (s *store) Watch(ctx context.Context, key string, opts storage.ListOptions) (watch.Interface, error) {
	k, err := s.objectKey(key, opts.Recursive)
	if err != nil {
		return nil, err
	}
	rv, err := s.versioner.ParseResourceVersion(opts.ResourceVersion)
	if err != nil {
		return nil, err
	}
	start := rv
	if rv == 0 && utilfeature.DefaultFeatureGate.Enabled(features.WatchList) &&
		opts.SendInitialEvents != nil && !*opts.SendInitialEvents {
		// No initial events: start from the current version.
		if start, err = s.readCounter(ctx); err != nil {
			return nil, err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	w := &watcher{
		s:              s,
		ctx:            ctx,
		cancel:         cancel,
		result:         make(chan watch.Event, 100),
		key:            k,
		recursive:      opts.Recursive,
		pred:           opts.Predicate,
		progressNotify: opts.ProgressNotify,
		progress:       make(chan struct{}, 1),
		hint:           make(chan struct{}, 1),
		notified:       make(chan struct{}, 1),
	}
	s.watchers.add(w)
	utilflowcontrol.WatchInitialized(ctx)
	go func() {
		defer s.watchers.remove(w)
		w.run(start, initialEventsRequired(rv, opts), initialEventsEndBookmarkRequired(opts))
	}()
	return w, nil
}

func initialEventsRequired(rv uint64, opts storage.ListOptions) bool {
	if opts.SendInitialEvents == nil && rv == 0 {
		return true
	}
	if !utilfeature.DefaultFeatureGate.Enabled(features.WatchList) {
		return false
	}
	return opts.SendInitialEvents != nil && *opts.SendInitialEvents
}

func initialEventsEndBookmarkRequired(opts storage.ListOptions) bool {
	if !utilfeature.DefaultFeatureGate.Enabled(features.WatchList) {
		return false
	}
	return opts.SendInitialEvents != nil && *opts.SendInitialEvents && opts.Predicate.AllowWatchBookmarks
}

func (w *watcher) run(start uint64, initialEvents, endBookmark bool) {
	defer close(w.result)
	defer w.cancel()

	last := start
	if initialEvents {
		if start > 0 {
			current, err := w.s.readCounter(w.ctx)
			if err != nil {
				w.sendError(err)
				return
			}
			if start > current {
				w.sendError(storage.NewTooLargeResourceVersionError(start, current, 1))
				return
			}
		}
		//= spec/solas.md#4-4-start
		//# A watch with no resource version MUST first list the current state.
		rev, ok := w.sendInitialEvents()
		if !ok {
			return
		}
		//= spec/solas.md#4-4-start
		//# It MUST then continue from the version of the list.
		last = rev
	}
	if endBookmark && !w.sendBookmark(last, true) {
		return
	}

	//= spec/solas.md#2-8-postgres-store
	//# A server that listens MUST still poll each resource at a fallback
	//# interval of ten times the poll interval.
	interval := w.s.pollInterval
	if w.s.listener != nil {
		interval *= 10
	}
	t := time.NewTicker(interval)
	var progress <-chan time.Time
	if w.progressNotify {
		pt := time.NewTicker(w.s.progressInterval)
		defer pt.Stop()
		progress = pt.C
	}
	defer t.Stop()
	// The first poll runs at once, so a watch from a version delivers
	// what came after it without a wait for the ticker.
	lastPoll := time.Now()
	last, ok := w.poll(last)
	if !ok {
		return
	}
	//= spec/solas.md#4-2-poll
	//# When a watcher asks for progress, the server MUST poll before it sends
	//# the bookmark.
	//
	// Each turn of the loop waits for a reason to poll, polls, and then
	// sends a bookmark when the reason was a progress request, the progress
	// ticker, or a hint. The poll runs before the bookmark, so the bookmark
	// carries a version at or above the counter at the time of the request,
	// which is what a consistent list from the watch cache waits for.
	var floor <-chan time.Time
	for {
		//= spec/solas.md#2-8-postgres-store
		//# A poll that a notification wakes MUST NOT start sooner than the poll
		//# interval after the start of the last poll of the resource.
		//= spec/solas.md#2-8-postgres-store
		//# A notification that arrives in that time joins the next poll.
		//
		// floor is armed by a notification that came too soon, and fires
		// the poll when the interval has passed; a notification meanwhile
		// raises hinted and joins it.
		var hinted uint64
		bookmark := false
		select {
		case <-w.ctx.Done():
			return
		case <-w.hint:
			//= spec/solas.md#4-2-poll
			//# A poll MAY use a counter value from an earlier read as `c`, instead of
			//# a read of its own.
			hinted = w.hinted.Load()
			bookmark = w.progressNotify
		case <-w.notified:
			if wait := w.s.pollInterval - time.Since(lastPoll); wait > 0 {
				if floor == nil {
					floor = time.After(wait)
				}
				continue
			}
			hinted = w.hinted.Load()
		case <-floor:
			floor = nil
			hinted = w.hinted.Load()
		case <-w.progress:
			bookmark = w.progressNotify
		case <-progress:
			bookmark = true
		case <-t.C:
		}
		lastPoll = time.Now()
		if hinted > 0 {
			last, ok = w.pollAt(last, hinted)
		} else {
			last, ok = w.poll(last)
		}
		if !ok {
			return
		}
		if bookmark && !w.sendBookmark(last, false) {
			return
		}
	}
}

//= spec/solas.md#4-4-start
//# A watch with no resource version MUST first list the current state.

// sendInitialEvents sends an ADDED event for each object in one snapshot
// and returns the version of the snapshot, spec 2.8.
func (w *watcher) sendInitialEvents() (uint64, bool) {
	for {
		rev, rows, err := w.s.snapshot(w.ctx, w.key, w.key, w.recursive)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := w.s.initCounter(w.ctx); err != nil {
				w.sendError(err)
				return 0, false
			}
			continue
		}
		if err != nil {
			w.sendError(err)
			return 0, false
		}
		//= spec/solas.md#4-4-start
		//# It MUST then deliver an `ADDED` event for each object in the list.
		for _, r := range rows {
			obj, err := w.decode(r.value, r.rv)
			if err != nil {
				w.sendError(err)
				return 0, false
			}
			if w.filter(obj) && !w.send(watch.Event{Type: watch.Added, Object: obj}) {
				return 0, false
			}
		}
		return rev, true
	}
}

// poll delivers the events after last and returns the new last version.
func (w *watcher) poll(last uint64) (uint64, bool) {
	//= spec/solas.md#4-2-poll
	//# Each poll MUST read the counter first.
	c, err := w.s.readCounter(w.ctx)
	if err != nil {
		if w.ctx.Err() == nil {
			w.sendError(err)
		}
		return 0, false
	}
	return w.pollAt(last, c)
}

// pollAt delivers the events after last up to c, a counter value that
// the store has read, and returns the new last version.
func (w *watcher) pollAt(last, c uint64) (uint64, bool) {
	if c <= last {
		//= spec/solas.md#4-2-poll
		//# If `c` equals `last`, the poll MUST return no events.

		//= spec/solas.md#4-2-poll
		//# If that value is not above `last`, the poll returns no events.
		return last, true
	}
	//= spec/solas.md#4-2-poll
	//# If `c` is greater than `last`, the poll MUST query the event log for the
	//# versions from `last + 1` to `c`.
	events, err := w.s.readEvents(w.ctx, last+1, c)
	if err != nil {
		if w.ctx.Err() == nil {
			w.sendError(err)
		}
		return 0, false
	}
	//= spec/solas.md#4-1-event-log
	//# The server MUST NOT depend on the time at which DynamoDB deletes an
	//# expired item.

	//= spec/solas.md#4-2-poll
	//# A watch from version `r` MUST deliver every event with a version greater
	//# than `r`.
	if !complete(events, last+1, c) {
		//= spec/solas.md#4-3-gaps
		//# If the query result does not hold each version from `last + 1` to `c`
		//# exactly once, the watch MUST end with `410 Gone`.
		w.sendError(apierrors.NewResourceExpired(fmt.Sprintf(
			"events between resource versions %d and %d are no longer in the event log", last+1, c)))
		return 0, false
	}
	//= spec/solas.md#4-2-poll
	//# A watch MUST deliver events in version order.

	//= spec/solas.md#4-2-poll
	//# A watch MUST NOT deliver the same event twice.
	for _, e := range events {
		ev, err := w.transform(e)
		if err != nil {
			w.sendError(err)
			return 0, false
		}
		if ev != nil {
			storemetrics.WatchLag.WithLabelValues(w.s.groupResource.String()).Observe(time.Since(e.written).Seconds())
		}
		if ev != nil && !w.send(*ev) {
			return 0, false
		}
	}
	return c, true
}

// complete reports whether events hold each version from first to last
// exactly once, in order.
func complete(events []event, first, last uint64) bool {
	if uint64(len(events)) != last-first+1 {
		return false
	}
	for i, e := range events {
		if e.rv != first+uint64(i) {
			return false
		}
	}
	return true
}

//= spec/solas.md#2-8-postgres-store
//# A watch MUST poll the `events` rows of its resource as in section 4.2,
//# with `c` from the `counters` row.

// readEvents returns the events with versions from first to last, in order.
func (s *store) readEvents(ctx context.Context, first, last uint64) ([]event, error) {
	rows, err := s.pool.Query(ctx, s.q.readEvents, s.resource, int64(first), int64(last))
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()
	var events []event
	for rows.Next() {
		var e event
		var rv int64
		var typ string
		var expires time.Time
		if err := rows.Scan(&rv, &typ, &e.key, &e.value, &e.prev, &expires); err != nil {
			return nil, fmt.Errorf("query events: %w", err)
		}
		e.rv, e.typ, e.written = uint64(rv), watch.EventType(typ), expires.Add(-s.retention)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	return events, nil
}

// transform turns a log event into the watch event this watcher must see,
// or nil when the watcher must not see it.
func (w *watcher) transform(e event) (*watch.Event, error) {
	if e.typ == eventInit {
		//= spec/solas.md#4-2-poll
		//# A watch MUST NOT deliver an `INIT` event.
		return nil, nil
	}
	if w.recursive && !strings.HasPrefix(e.key, w.key) || !w.recursive && e.key != w.key {
		return nil, nil
	}
	switch e.typ {
	case watch.Added:
		cur, err := w.decode(e.value, e.rv)
		if err != nil || !w.filter(cur) {
			return nil, err
		}
		return &watch.Event{Type: watch.Added, Object: cur}, nil
	case watch.Deleted:
		// The deleted object carries the version of the delete, spec 3.3.
		old, err := w.decode(e.value, e.rv)
		if err != nil || !w.filter(old) {
			return nil, err
		}
		return &watch.Event{Type: watch.Deleted, Object: old}, nil
	case watch.Modified:
		cur, err := w.decode(e.value, e.rv)
		if err != nil {
			return nil, err
		}
		if w.pred.Empty() {
			return &watch.Event{Type: watch.Modified, Object: cur}, nil
		}
		old, err := w.decode(e.prev, e.rv)
		if err != nil {
			return nil, err
		}
		curPasses, oldPasses := w.filter(cur), w.filter(old)
		switch {
		case curPasses && oldPasses:
			return &watch.Event{Type: watch.Modified, Object: cur}, nil
		case curPasses:
			return &watch.Event{Type: watch.Added, Object: cur}, nil
		case oldPasses:
			return &watch.Event{Type: watch.Deleted, Object: old}, nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown event type %q at resource version %d", e.typ, e.rv)
}

func (w *watcher) decode(data []byte, rv uint64) (runtime.Object, error) {
	obj := w.s.newFunc()
	if err := w.s.decode(data, obj, rv); err != nil {
		return nil, err
	}
	return obj, nil
}

func (w *watcher) filter(obj runtime.Object) bool {
	if w.pred.Empty() {
		return true
	}
	matched, err := w.pred.Matches(obj)
	return err == nil && matched
}

func (w *watcher) send(e watch.Event) bool {
	select {
	case w.result <- e:
		return true
	case <-w.ctx.Done():
		return false
	}
}

func (w *watcher) sendBookmark(rv uint64, initialEventsEnd bool) bool {
	obj := w.s.newFunc()
	if err := w.s.versioner.UpdateObject(obj, rv); err != nil {
		w.sendError(err)
		return false
	}
	if initialEventsEnd {
		if err := storage.AnnotateInitialEventsEndBookmark(obj); err != nil {
			w.sendError(err)
			return false
		}
	}
	return w.send(watch.Event{Type: watch.Bookmark, Object: obj})
}

// sendError sends an error event. The caller then ends the watch.
func (w *watcher) sendError(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	var status metav1.Status
	if se, ok := err.(apierrors.APIStatus); ok {
		status = se.Status()
	} else {
		status = apierrors.NewInternalError(err).ErrStatus
	}
	w.send(watch.Event{Type: watch.Error, Object: &status})
}
