package postgres

import (
	"context"
	"fmt"
	mrand "math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/klog/v2"
)

// eventInit is the type of the event at version 1 of a resource.
const eventInit = "INIT"

//= spec/solas.md#2-8-postgres-store
//# Postgres does not expire rows.

//= spec/solas.md#2-8-postgres-store
//# Each server MUST delete the `events` rows of each resource whose
//# `expires` is in the past.

// DeleteExpired deletes the events of every resource in the schema of
// the prefix whose expiry time is past, and returns how many.
func DeleteExpired(ctx context.Context, pool *pgxpool.Pool, prefix string) (int64, error) {
	schema := pgx.Identifier{SchemaName(prefix)}.Sanitize()
	tag, err := pool.Exec(ctx, "DELETE FROM "+schema+".events WHERE expires < now()")
	if err != nil {
		return 0, fmt.Errorf("delete the expired events: %w", err)
	}
	return tag.RowsAffected(), nil
}

//= spec/solas.md#2-8-postgres-store
//# A server MUST run that delete at a random interval between half of the
//# retention time and the retention time.

// RunExpiry deletes the expired events of the schema at a random
// interval between half of the retention time and the retention time,
// until ctx ends. The server runs one per process. A failed delete is
// logged and tried again at the next interval: the events stay until a
// server deletes them, and nothing depends on when.
func RunExpiry(ctx context.Context, pool *pgxpool.Pool, prefix string, retention time.Duration) {
	if retention <= 0 {
		retention = DefaultEventRetention
	}
	for {
		wait := retention/2 + time.Duration(mrand.Int64N(int64(retention/2)+1))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		n, err := DeleteExpired(ctx, pool, prefix)
		if err != nil {
			if ctx.Err() == nil {
				klog.ErrorS(err, "the expired events stay until the next try")
			}
			continue
		}
		if n > 0 {
			klog.V(4).InfoS("deleted expired events", "count", n)
		}
	}
}
