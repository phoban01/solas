package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/storage"
)

// objRow is one row of the objects table.
type objRow struct {
	key   string
	rv    uint64
	value []byte
}

//= spec/solas.md#2-8-postgres-store
//# A list MUST read the `counters` row and the `objects` rows in one
//# transaction at the `REPEATABLE READ` level.

//= spec/solas.md#2-8-postgres-store
//# A snapshot at that level holds every transaction that committed before
//# it started, and no other.

//= spec/solas.md#2-8-postgres-store
//# So the list is a consistent snapshot at the counter value that it read.

//= spec/solas.md#2-8-postgres-store
//# The list MUST return that value as its resource version, with no second
//# read of the counter.

func (s *store) GetList(ctx context.Context, key string, opts storage.ListOptions, listObj runtime.Object) error {
	keyPrefix, err := storage.PrepareKey(s.resourcePrefix, key, opts.Recursive)
	if err != nil {
		return err
	}
	prefix, err := s.objectKey(key, opts.Recursive)
	if err != nil {
		return err
	}
	listPtr, err := meta.GetItemsPtr(listObj)
	if err != nil {
		return err
	}
	v, err := conversion.EnforcePtr(listPtr)
	if err != nil || v.Kind() != reflect.Slice {
		return fmt.Errorf("need ptr to slice: %v", err)
	}
	newItem := newItemFunc(listObj, v)

	withRev, continueKey, err := storage.ValidateListOptions(keyPrefix, s.versioner, opts)
	if err != nil {
		return err
	}
	start := prefix
	if continueKey != "" {
		if start, err = s.objectKey(continueKey, false); err != nil {
			return apierrors.NewBadRequest(fmt.Sprintf("invalid continue token: %v", err))
		}
	}

	for {
		rev, rows, err := s.snapshot(ctx, prefix, start, opts.Recursive)
		if errors.Is(err, pgx.ErrNoRows) {
			// The resource has no counter yet: make it, then read again.
			if err := s.initCounter(ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if withRev > 0 {
			if err := s.checkExactVersion(uint64(withRev), rev, continueKey != ""); err != nil {
				return err
			}
		} else if err := s.validateMinimumResourceVersion(opts.ResourceVersion, rev); err != nil {
			return err
		}
		return s.fillList(listObj, v, newItem, rows, keyPrefix, rev, opts)
	}
}

// snapshot reads the counter and the objects under prefix from start, in
// key order, in one REPEATABLE READ transaction: the rows are the state
// at the counter value. It returns pgx.ErrNoRows when the resource has no
// counter.
func (s *store) snapshot(ctx context.Context, prefix, start string, recursive bool) (uint64, []objRow, error) {
	// A continue token names the key after the last one of the page as
	// that key plus a NUL byte, which a text column cannot hold: the query
	// then starts after the key instead.
	query := s.q.listObjects
	if after, ok := strings.CutSuffix(start, "\x00"); ok {
		query, start = s.q.listObjectsAfter, after
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, nil, fmt.Errorf("list: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var n int64
	var sealed bool
	if err := tx.QueryRow(ctx, s.q.readCounter, s.resource).Scan(&n, &sealed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, err
		}
		return 0, nil, fmt.Errorf("list: read counter: %w", err)
	}
	if s.afterListCounter != nil {
		s.afterListCounter()
	}
	var rows pgx.Rows
	if recursive {
		rows, err = tx.Query(ctx, query, s.resource, start, prefix)
	} else {
		rows, err = tx.Query(ctx, s.q.readObjectRow, s.resource, prefix)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("list: query objects: %w", err)
	}
	defer rows.Close()
	var out []objRow
	for rows.Next() {
		var r objRow
		var rv int64
		if err := rows.Scan(&r.key, &rv, &r.value); err != nil {
			return 0, nil, fmt.Errorf("list: %w", err)
		}
		r.rv = uint64(rv)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("list: query objects: %w", err)
	}
	return uint64(n), out, nil
}

// checkExactVersion checks a list at an exact version. The store keeps only
// the latest state, so it serves only the current version.
func (s *store) checkExactVersion(want, current uint64, isContinue bool) error {
	if want > current {
		return storage.NewTooLargeResourceVersionError(want, current, 0)
	}
	if want == current {
		return nil
	}
	//= spec/solas.md#2-4-reads
	//# A request for a list at an older, exact resource version MUST be served
	//# by the watch cache, or it MUST fail with `410 Gone`.
	if isContinue {
		return apierrors.NewResourceExpired("The provided continue parameter is too old " +
			"to display a consistent list result. You can start a new list without " +
			"the continue parameter.")
	}
	return apierrors.NewResourceExpired(fmt.Sprintf(
		"resource version %d is older than the current version %d, and the store keeps no history", want, current))
}

// fillList decodes the rows, applies the predicate and the limit, and sets
// the list metadata.
func (s *store) fillList(listObj runtime.Object, v reflect.Value, newItem func() runtime.Object,
	rows []objRow, keyPrefix string, rev uint64, opts storage.ListOptions) error {
	pred := opts.Predicate
	paging := pred.Limit > 0
	var lastKey string
	var hasMore bool
	for _, r := range rows {
		if paging && int64(v.Len()) >= pred.Limit {
			hasMore = true
			break
		}
		lastKey = s.storageKey(r.key)
		obj := newItem()
		if err := s.decode(r.value, obj, r.rv); err != nil {
			return err
		}
		matched, err := pred.Matches(obj)
		if err != nil {
			return err
		}
		if matched {
			v.Set(reflect.Append(v, reflect.ValueOf(obj).Elem()))
		}
	}

	continueValue, remaining, err := storage.PrepareContinueToken(lastKey, keyPrefix, int64(rev), int64(len(rows)), hasMore, opts)
	if err != nil {
		return err
	}
	if v.IsNil() {
		v.Set(reflect.MakeSlice(v.Type(), 0, 0))
	}
	return s.versioner.UpdateList(listObj, rev, continueValue, remaining)
}

func newItemFunc(listObj runtime.Object, v reflect.Value) func() runtime.Object {
	if list, ok := listObj.(*unstructured.UnstructuredList); ok {
		if apiVersion := list.GetAPIVersion(); apiVersion != "" {
			return func() runtime.Object {
				return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion}}
			}
		}
	}
	elem := v.Type().Elem()
	return func() runtime.Object {
		return reflect.New(elem).Interface().(runtime.Object)
	}
}
