package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"k8s.io/klog/v2"
)

//= spec/solas.md#2-8-postgres-store
//# A server SHOULD hold one connection that listens on that channel.

//= spec/solas.md#2-8-postgres-store
//# A listener needs a connection of its own, which a pooler that
//# multiplexes transactions cannot carry.

// A Listener holds one connection that listens on the channel of a
// schema, and wakes the pollers of the stores that register with it,
// spec 2.8. A server runs one per process. The connection is its own,
// outside the pool: a notification arrives on the connection that ran
// LISTEN, which a pooler that multiplexes transactions cannot carry.
type Listener struct {
	url    string
	schema string
	mu     sync.Mutex
	stores map[string][]hinter
}

// hinter is the part of a store that a notification wakes: its watchers.
type hinter interface{ notify(c uint64) }

// NewListener returns a listener on the channel of the schema of prefix,
// and runs it until ctx ends. A connection that fails is opened again
// after a short wait; meanwhile the stores poll at their fallback
// interval and lose nothing.
func NewListener(ctx context.Context, url, prefix string) *Listener {
	l := &Listener{url: url, schema: SchemaName(prefix), stores: map[string][]hinter{}}
	go l.run(ctx)
	return l
}

// register adds a store to the ones a notification of its resource wakes.
func (l *Listener) register(resource string, h hinter) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stores[resource] = append(l.stores[resource], h)
}

// unregister removes a store.
func (l *Listener) unregister(resource string, h hinter) {
	l.mu.Lock()
	defer l.mu.Unlock()
	hs := l.stores[resource]
	for i, x := range hs {
		if x == h {
			l.stores[resource] = append(hs[:i:i], hs[i+1:]...)
			return
		}
	}
}

// run listens until ctx ends.
func (l *Listener) run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := l.listen(ctx); err != nil && ctx.Err() == nil {
			klog.ErrorS(err, "the listener lost its connection; the stores poll until it is back", "channel", l.schema)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// listen opens the connection, runs LISTEN, and dispatches the
// notifications until the connection fails or ctx ends.
func (l *Listener) listen(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, l.url)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{l.schema}.Sanitize()); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		resource, version, ok := strings.Cut(n.Payload, " ")
		c, perr := strconv.ParseUint(version, 10, 64)
		if !ok || perr != nil {
			klog.ErrorS(fmt.Errorf("payload %q", n.Payload), "a notification the listener cannot read", "channel", l.schema)
			continue
		}
		//= spec/solas.md#2-8-postgres-store
		//# A notification MUST wake the poll of its resource, with the version
		//# as `c`, as a counter read does in section 4.2.
		l.mu.Lock()
		hs := append([]hinter(nil), l.stores[resource]...)
		l.mu.Unlock()
		for _, h := range hs {
			h.notify(c)
		}
	}
}
