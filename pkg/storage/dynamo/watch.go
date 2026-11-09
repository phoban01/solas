package dynamo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/features"
	"k8s.io/apiserver/pkg/storage"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	utilflowcontrol "k8s.io/apiserver/pkg/util/flowcontrol"
)

// event is one item of the event log.
type event struct {
	rv    uint64
	typ   watch.EventType
	sk    string
	value []byte
	prev  []byte
}

// watcher delivers the events of one watch. It polls the event log, as
// spec 4.2 gives.
type watcher struct {
	s              *store
	ctx            context.Context
	cancel         context.CancelFunc
	result         chan watch.Event
	sk             string
	recursive      bool
	pred           storage.SelectionPredicate
	progressNotify bool
	// progress asks the watcher to send a bookmark, see RequestWatchProgress.
	progress chan struct{}
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

func (s *store) Watch(ctx context.Context, key string, opts storage.ListOptions) (watch.Interface, error) {
	sk, err := s.sortKey(key, opts.Recursive)
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
		sk:             sk,
		recursive:      opts.Recursive,
		pred:           opts.Predicate,
		progressNotify: opts.ProgressNotify,
		progress:       make(chan struct{}, 1),
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

	t := time.NewTicker(w.s.pollInterval)
	var progress <-chan time.Time
	if w.progressNotify {
		pt := time.NewTicker(w.s.progressInterval)
		defer pt.Stop()
		progress = pt.C
	}
	defer t.Stop()
	//= spec/solas.md#4-2-poll
	//# When a watcher asks for progress, the server MUST poll before it sends
	//# the bookmark.
	//
	// bookmark is set by a progress request or by the progress ticker. The
	// poll at the top of the loop runs first, so the bookmark carries a
	// version at or above the counter at the time of the request, which is
	// what a consistent list from the watch cache waits for.
	bookmark := false
	for {
		next, ok := w.poll(last)
		if !ok {
			return
		}
		last = next
		if bookmark {
			bookmark = false
			if !w.sendBookmark(last, false) {
				return
			}
		}
		select {
		case <-w.ctx.Done():
			return
		case <-w.progress:
			bookmark = w.progressNotify
		case <-progress:
			bookmark = true
		case <-t.C:
		}
	}
}

// sendInitialEvents sends an ADDED event for each object in a consistent
// snapshot and returns the version of the snapshot.
func (w *watcher) sendInitialEvents() (uint64, bool) {
	for attempt := 0; ; attempt++ {
		before, err := w.s.readCounter(w.ctx)
		if err != nil {
			w.sendError(err)
			return 0, false
		}
		var items []map[string]types.AttributeValue
		if w.recursive {
			items, err = w.s.queryObjects(w.ctx, w.sk, w.sk)
		} else {
			var item map[string]types.AttributeValue
			if item, err = w.s.readObject(w.ctx, w.sk); item != nil {
				items = append(items, item)
			}
		}
		if err != nil {
			w.sendError(err)
			return 0, false
		}
		after, err := w.s.readCounter(w.ctx)
		if err != nil {
			w.sendError(err)
			return 0, false
		}
		if before != after {
			if err := sleepJitter(w.ctx, attempt); err != nil {
				return 0, false
			}
			continue
		}
		//= spec/solas.md#4-4-start
		//# It MUST then deliver an `ADDED` event for each object in the list.
		for _, item := range items {
			rv, err := numAttr(item, attrRV)
			if err != nil {
				w.sendError(err)
				return 0, false
			}
			obj, err := w.decode(binAttr(item, attrValue), rv)
			if err != nil {
				w.sendError(err)
				return 0, false
			}
			if w.filter(obj) && !w.send(watch.Event{Type: watch.Added, Object: obj}) {
				return 0, false
			}
		}
		return before, true
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
	if c <= last {
		//= spec/solas.md#4-2-poll
		//# If `c` equals `last`, the poll MUST return no events.
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

// readEvents returns the events with versions from first to last, in order.
func (s *store) readEvents(ctx context.Context, first, last uint64) ([]event, error) {
	var events []event
	var from map[string]types.AttributeValue
	for {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.table),
			KeyConditionExpression: aws.String("pk = :pk AND sk BETWEEN :first AND :last"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":    str(s.eventPK()),
				":first": str(eventSK(first)),
				":last":  str(eventSK(last)),
			},
			//= spec/solas.md#2-4-reads
			//# Every query of an event log MUST be a strongly consistent query.
			ConsistentRead:    aws.Bool(true),
			ExclusiveStartKey: from,
		})
		if err != nil {
			return nil, fmt.Errorf("query events: %w", err)
		}
		for _, item := range out.Items {
			var rv uint64
			if _, err := fmt.Sscanf(strAttr(item, attrSK), "%d", &rv); err != nil {
				return nil, fmt.Errorf("event sort key %q: %w", strAttr(item, attrSK), err)
			}
			events = append(events, event{
				rv:    rv,
				typ:   watch.EventType(strAttr(item, attrType)),
				sk:    strAttr(item, attrKey),
				value: binAttr(item, attrValue),
				prev:  binAttr(item, attrPrev),
			})
		}
		if out.LastEvaluatedKey == nil {
			return events, nil
		}
		from = out.LastEvaluatedKey
	}
}

// transform turns a log event into the watch event this watcher must see,
// or nil when the watcher must not see it.
func (w *watcher) transform(e event) (*watch.Event, error) {
	if e.typ == eventInit {
		//= spec/solas.md#4-2-poll
		//# A watch MUST NOT deliver an `INIT` event.
		return nil, nil
	}
	if w.recursive && !strings.HasPrefix(e.sk, w.sk) || !w.recursive && e.sk != w.sk {
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
