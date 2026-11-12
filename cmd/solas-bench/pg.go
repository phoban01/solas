package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	storageurl "github.com/phoban01/solas/pkg/storage"
)

// The Postgres modes measure what a Postgres store would give, with the
// client alone and no API server, before any store code exists. The URL
// is the storage URL of spec 2.6; the credentials come from PGUSER and
// PGPASSWORD, as the store's URL rule needs, spec 2.6.
//
//   - counter: every writer runs UPDATE ... RETURNING on one counter row,
//     in a transaction of its own. The row lock queues the writers.
//   - commit: every writer runs the write of spec 2.3 as one transaction:
//     the counter update, a conditional update of two rows of its own,
//     and an event insert. --spread drops the counter update, so the
//     rows show what the shared row costs.
//   - poll: --writers pollers each read the events above their cursor
//     every 200 ms, as a watch of spec 4.2 does, while 8 writers commit.
//     The row counts the polls and their latency.
//
// Each mode prints one row in the shape of the item modes.

// pgSchema is the schema of a run. The bench owns these tables and drops
// them at the end.
const pgSchema = `
CREATE TABLE IF NOT EXISTS bench_counter (id int PRIMARY KEY, n bigint NOT NULL);
CREATE TABLE IF NOT EXISTS bench_objects (id int PRIMARY KEY, rv bigint NOT NULL, value bytea NOT NULL);
CREATE TABLE IF NOT EXISTS bench_events (rv bigint PRIMARY KEY, id int NOT NULL, at timestamptz NOT NULL DEFAULT now());
INSERT INTO bench_counter (id, n) VALUES (1, 0) ON CONFLICT DO NOTHING;
`

func runPG(ctx context.Context, o options) error {
	sc, err := storageurl.Parse(o.storageURL)
	if err != nil {
		return fmt.Errorf("--storage-url: %w", err)
	}
	cfg, err := pgxpool.ParseConfig(sc.PostgresURL())
	if err != nil {
		return fmt.Errorf("--storage-url: %w", err)
	}
	conns := o.writers + 1
	if o.mode == "poll" {
		conns = o.writers + 8 + 1
	}
	cfg.MaxConns = int32(conns)
	cfg.MinConns = cfg.MaxConns
	connect, err := pgConnectTime(ctx, cfg.ConnConfig.Host, cfg.ConnConfig.Port)
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, pgSchema+pgCommitFunction); err != nil {
		return fmt.Errorf("create the bench tables: %w", err)
	}
	defer func() {
		_, err := pool.Exec(context.Background(), "DROP TABLE IF EXISTS bench_counter, bench_objects, bench_events; DROP FUNCTION IF EXISTS bench_commit")
		if err != nil {
			fmt.Fprintln(os.Stderr, "solas-bench: clean up:", err)
		}
	}()
	writers := o.writers
	if o.mode == "poll" {
		writers = 8
	}
	value := []byte(strings.Repeat("x", 512))
	for i := 0; i < 2*writers; i++ {
		_, err := pool.Exec(ctx, "INSERT INTO bench_objects (id, rv, value) VALUES ($1, 0, $2) ON CONFLICT DO NOTHING", i, value)
		if err != nil {
			return err
		}
	}

	// The listeners of --notify connect before the clock starts, 64 at a
	// time: thousands of TLS connections from one process take longer
	// than a run, and a server has its listener up before it serves.
	var listeners []*pgx.Conn
	if o.mode == "poll" && o.notify {
		listeners = make([]*pgx.Conn, o.writers)
		sem := make(chan struct{}, 64)
		var lwg sync.WaitGroup
		var lerr atomic.Int64
		for i := range listeners {
			lwg.Add(1)
			sem <- struct{}{}
			go func() {
				defer lwg.Done()
				defer func() { <-sem }()
				conn, err := pgx.Connect(ctx, cfg.ConnConfig.ConnString())
				if err == nil {
					_, err = conn.Exec(ctx, "LISTEN bench")
				}
				if err != nil {
					if lerr.Add(1) <= 5 {
						fmt.Fprintln(os.Stderr, "solas-bench: listener:", err)
					}
					return
				}
				listeners[i] = conn
			}()
		}
		lwg.Wait()
		if n := lerr.Load(); n > 0 {
			return fmt.Errorf("%d of %d listeners did not connect", n, o.writers)
		}
		defer func() {
			for _, c := range listeners {
				_ = c.Close(context.Background())
			}
		}()
	}
	var st, polls itemStats
	stop := make(chan struct{})
	timer := time.AfterFunc(o.duration, func() { close(stop) })
	defer timer.Stop()
	var wg sync.WaitGroup
	start := time.Now()
	// loop runs f until the end, times each call, and waits pause
	// between calls: 0 for a writer, the poll interval for a poller.
	loop := func(f func() error, s *itemStats, pause time.Duration) {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			from := time.Now()
			err := f()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				if s.errors.Add(1) <= 5 {
					fmt.Fprintln(os.Stderr, "solas-bench:", err)
				}
			} else {
				s.done(time.Since(from))
			}
			if pause > 0 {
				select {
				case <-stop:
					return
				case <-time.After(pause):
				}
			}
		}
	}
	for i := 0; i < writers; i++ {
		wg.Add(1)
		a, b := 2*i, 2*i+1
		var last int64
		switch o.mode {
		case "counter":
			go loop(func() error { return pgCounter(ctx, pool) }, &st, 0)
		default:
			go loop(func() error {
				rv, err := pgCommit(ctx, pool, a, b, last, value, !o.spread)
				if err == nil {
					last = rv
				}
				return err
			}, &st, 0)
		}
	}
	if o.mode == "poll" {
		for i := 0; i < o.writers; i++ {
			wg.Add(1)
			if o.notify {
				go notifyPoller(ctx, &wg, stop, pool, listeners[i], &polls)
				continue
			}
			var cursor int64
			go loop(func() error {
				next, err := pgPoll(ctx, pool, cursor)
				if err != nil {
					return err
				}
				cursor = next
				return nil
			}, &polls, 200*time.Millisecond)
		}
	}
	wg.Wait()
	elapsed := time.Since(start)
	if err := ctx.Err(); err != nil {
		return err
	}
	ms := func(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }
	mode := "pg " + o.mode
	if o.mode == "poll" && o.notify {
		mode = "pg poll notify"
	}
	if o.mode == "commit" && o.spread {
		mode = "pg commit spread"
	}
	cols := []string{"mode", "writers", "ops/s", "p50 ms", "p99 ms", "polls/s", "poll p50 ms", "poll p99 ms", "errors", "connect ms"}
	row := []string{mode, fmt.Sprint(o.writers), fmt.Sprintf("%.1f", float64(st.ops.Load())/elapsed.Seconds()),
		ms(percentile(st.latency, 0.5)), ms(percentile(st.latency, 0.99)),
		fmt.Sprintf("%.1f", float64(polls.ops.Load())/elapsed.Seconds()),
		ms(percentile(polls.latency, 0.5)), ms(percentile(polls.latency, 0.99)),
		fmt.Sprint(st.errors.Load() + polls.errors.Load()), ms(connect)}
	if o.header {
		fmt.Println(strings.Join(cols, "\t"))
	}
	fmt.Println(strings.Join(row, "\t"))
	return nil
}

// pgCounter is the issue step alone: one counter update in its own
// transaction.
func pgCounter(ctx context.Context, pool *pgxpool.Pool) error {
	var n int64
	return pool.QueryRow(ctx, "UPDATE bench_counter SET n = n + 1 WHERE id = 1 RETURNING n").Scan(&n)
}

// pgCommit is the write of spec 2.3 as one round trip: the function
// below takes the counter row lock and the version, updates two objects
// of the writer on the condition that they still hold the version it
// wrote last, inserts the event, and commits. The lock is held for the
// time of the function, not for a round trip per statement. Without the
// counter, the new version is the writer's last plus one, and there is
// no event. It returns the new version.
func pgCommit(ctx context.Context, pool *pgxpool.Pool, a, b int, last int64, value []byte, counter bool) (int64, error) {
	var rv int64
	err := pool.QueryRow(ctx, "SELECT bench_commit($1, $2, $3, $4, $5)", a, b, last, value, counter).Scan(&rv)
	return rv, err
}

// pgCommitFunction is the write as a function, so it is one round trip.
// A condition that fails raises, which rolls the transaction back and
// leaves the counter where it was.
const pgCommitFunction = `
CREATE OR REPLACE FUNCTION bench_commit(a int, b int, last bigint, v bytea, counter boolean) RETURNS bigint AS $$
DECLARE
  new_rv bigint := last + 1;
  cnt int;
BEGIN
  IF counter THEN
    UPDATE bench_counter SET n = n + 1 WHERE id = 1 RETURNING n INTO new_rv;
  END IF;
  UPDATE bench_objects SET rv = new_rv, value = v WHERE id = a AND rv = last;
  GET DIAGNOSTICS cnt = ROW_COUNT;
  IF cnt <> 1 THEN RAISE EXCEPTION 'object % condition failed', a; END IF;
  UPDATE bench_objects SET rv = new_rv, value = v WHERE id = b AND rv = last;
  GET DIAGNOSTICS cnt = ROW_COUNT;
  IF cnt <> 1 THEN RAISE EXCEPTION 'object % condition failed', b; END IF;
  IF counter THEN
    INSERT INTO bench_events (rv, id) VALUES (new_rv, a);
    PERFORM pg_notify('bench', new_rv::text);
  END IF;
  RETURN new_rv;
END;
$$ LANGUAGE plpgsql;
`

// pgPoll reads the events above the cursor, as a poll of the watch does,
// and returns the new cursor.
func pgPoll(ctx context.Context, pool *pgxpool.Pool, cursor int64) (int64, error) {
	rows, err := pool.Query(ctx, "SELECT rv FROM bench_events WHERE rv > $1 ORDER BY rv LIMIT 1000", cursor)
	if err != nil {
		return cursor, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&cursor); err != nil {
			return cursor, err
		}
	}
	return cursor, rows.Err()
}

// pgConnectTime measures one TCP connect to the server, with the name
// resolved first, so the row carries the round trip and not the lookup.
func pgConnectTime(ctx context.Context, host string, port uint16) (time.Duration, error) {
	if ips, err := net.DefaultResolver.LookupIPAddr(ctx, host); err == nil && len(ips) > 0 {
		host = ips[0].String()
	}
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	from := time.Now()
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("connect to %s: %w", addr, err)
	}
	d := time.Since(from)
	_ = conn.Close()
	return d, nil
}

// notifyPoller is a poller of --notify: it listens on the channel of
// the bench schema, as a server does, spec 2.8, and reads the events
// above its cursor when a notification arrives. The listener is a
// connection of its own, opened before the run; the poll goes through
// the pool.
func notifyPoller(ctx context.Context, wg *sync.WaitGroup, stop <-chan struct{}, pool *pgxpool.Pool, conn *pgx.Conn, s *itemStats) {
	defer wg.Done()
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-stop:
		case <-lctx.Done():
		}
		cancel()
	}()
	// wake holds one signal: the notifications that arrive during a poll
	// join the next one, and a poll starts no sooner than the poll
	// interval after the last, spec 2.8.
	wake := make(chan struct{}, 1)
	go func() {
		defer close(wake)
		for {
			if _, err := conn.WaitForNotification(lctx); err != nil {
				return
			}
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}()
	var cursor int64
	var last time.Time
	for range wake {
		if wait := 200*time.Millisecond - time.Since(last); wait > 0 {
			select {
			case <-lctx.Done():
				return
			case <-time.After(wait):
			}
		}
		last = time.Now()
		from := time.Now()
		next, err := pgPoll(ctx, pool, cursor)
		if ctx.Err() != nil || lctx.Err() != nil {
			return
		}
		if err != nil {
			if s.errors.Add(1) <= 5 {
				fmt.Fprintln(os.Stderr, "solas-bench:", err)
			}
			continue
		}
		cursor = next
		s.done(time.Since(from))
	}
}
