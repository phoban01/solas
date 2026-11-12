package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/storage"
)

// Default settings.
const (
	DefaultEventRetention = time.Hour
	//= spec/solas.md#4-2-poll
	//# The server SHOULD poll each resource at least once each second.

	//= spec/solas.md#4-2-poll
	//# The server SHOULD NOT poll a resource more often than every 100
	//# milliseconds.
	DefaultPollInterval     = 200 * time.Millisecond
	DefaultProgressInterval = time.Minute
)

// Config holds the settings that all stores of one API server share.
type Config struct {
	// Pool is the connection pool to the database of the storage URL.
	Pool *pgxpool.Pool
	//= spec/solas.md#4-1-event-log
	//# The retention time MUST be a setting of the API server.
	EventRetention time.Duration
	PollInterval   time.Duration
	// ProgressInterval is how often a watch that asked for progress
	// notifications gets a bookmark.
	ProgressInterval time.Duration
}

type store struct {
	pool             *pgxpool.Pool
	retention        time.Duration
	pollInterval     time.Duration
	progressInterval time.Duration
	codec            runtime.Codec
	versioner        storage.Versioner
	newFunc          func() runtime.Object
	newListFunc      func() runtime.Object
	resourcePrefix   string
	// schema is the quoted schema of the storage prefix, spec 2.8.
	schema string
	// resource names the counter row, the objects, and the events of this
	// resource in the schema.
	resource      string
	groupResource schema.GroupResource
	now           func() time.Time
	// batch collects the writes of this server to the resource, spec 2.3.
	batch batcher
	// q holds the statements of this store with the schema in place.
	q queries
	// afterListCounter runs between the counter read and the object query
	// of a list, in tests that write at that moment.
	afterListCounter func()
}

var _ storage.Interface = &store{}

// New returns a store for one resource. Keys passed to the store must start
// with resourcePrefix. prefix is the storage prefix of the API server; it
// names the schema that separates the data of different API servers in
// one database.
func New(cfg Config, codec runtime.Codec, newFunc, newListFunc func() runtime.Object,
	prefix, resourcePrefix string, groupResource schema.GroupResource) (storage.Interface, error) {
	return newStore(cfg, codec, newFunc, newListFunc, prefix, resourcePrefix, groupResource)
}

func newStore(cfg Config, codec runtime.Codec, newFunc, newListFunc func() runtime.Object,
	prefix, resourcePrefix string, groupResource schema.GroupResource) (*store, error) {
	if cfg.Pool == nil {
		return nil, errors.New("postgres: Config.Pool is nil")
	}
	if resourcePrefix == "" || resourcePrefix == "/" || resourcePrefix[0] != '/' {
		return nil, fmt.Errorf("postgres: invalid resource prefix %q", resourcePrefix)
	}
	s := &store{
		pool:             cfg.Pool,
		retention:        cfg.EventRetention,
		pollInterval:     cfg.PollInterval,
		progressInterval: cfg.ProgressInterval,
		codec:            codec,
		//= spec/solas.md#3-3-objects
		//# The server MUST encode each resource version as a decimal string.
		versioner:      storage.APIObjectVersioner{},
		newFunc:        newFunc,
		newListFunc:    newListFunc,
		resourcePrefix: resourcePrefix,
		schema:         pgx.Identifier{SchemaName(prefix)}.Sanitize(),
		resource:       strings.TrimSuffix(resourcePrefix, "/"),
		groupResource:  groupResource,
		now:            time.Now,
	}
	if s.retention == 0 {
		s.retention = DefaultEventRetention
	}
	if s.progressInterval == 0 {
		s.progressInterval = DefaultProgressInterval
	}
	if s.pollInterval == 0 {
		s.pollInterval = DefaultPollInterval
	}
	s.q = newQueries(s.schema)
	return s, nil
}

// queries holds the SQL of a store, with the schema name in place.
type queries struct {
	commit, readCounter, initCounter, initEvent, readObject, readObjectRow, listObjects, listObjectsAfter, count string
}

func newQueries(schema string) queries {
	r := func(stmt string) string { return strings.ReplaceAll(stmt, "{S}", schema) }
	return queries{
		commit:           r(`SELECT {S}.solas_commit($1, $2, $3, $4, $5, $6, $7)`),
		readCounter:      r(`SELECT n, sealed FROM {S}.counters WHERE resource = $1`),
		initCounter:      r(`INSERT INTO {S}.counters (resource, n) VALUES ($1, 1) ON CONFLICT DO NOTHING`),
		initEvent:        r(`INSERT INTO {S}.events (resource, rv, type, expires) VALUES ($1, 1, 'INIT', $2)`),
		readObject:       r(`SELECT rv, value FROM {S}.objects WHERE resource = $1 AND key = $2`),
		readObjectRow:    r(`SELECT key, rv, value FROM {S}.objects WHERE resource = $1 AND key = $2`),
		listObjects:      r(`SELECT key, rv, value FROM {S}.objects WHERE resource = $1 AND key >= $2 AND starts_with(key, $3) ORDER BY key`),
		listObjectsAfter: r(`SELECT key, rv, value FROM {S}.objects WHERE resource = $1 AND key > $2 AND starts_with(key, $3) ORDER BY key`),
		count:            r(`SELECT count(*) FROM {S}.objects WHERE resource = $1`),
	}
}

func (s *store) Versioner() storage.Versioner { return s.versioner }

// Stats counts the objects of the resource.
func (s *store) Stats(ctx context.Context) (storage.Stats, error) {
	var count int64
	if err := s.pool.QueryRow(ctx, s.q.count, s.resource).Scan(&count); err != nil {
		return storage.Stats{}, fmt.Errorf("count objects: %w", err)
	}
	return storage.Stats{ObjectCount: count}, nil
}

// ReadinessCheck checks that the database answers.
func (s *store) ReadinessCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("the database is not ready: %w", err)
	}
	return nil
}

// GetCurrentResourceVersion returns the counter of the resource.
func (s *store) GetCurrentResourceVersion(ctx context.Context) (uint64, error) {
	return s.readCounter(ctx)
}

// EnableResourceSizeEstimation does nothing. Stats reports no object size.
func (s *store) EnableResourceSizeEstimation(storage.KeysFunc) error { return nil }

// CompactRevision returns 0: the store does not compact. Expired events end
// a watch with 410 Gone instead, spec 4.3.
func (s *store) CompactRevision() int64 { return 0 }

// RequestWatchProgress asks each open watch that set ProgressNotify to send
// a bookmark with its current resource version. The watch comes with #311.
func (s *store) RequestWatchProgress(ctx context.Context) error { return nil }
