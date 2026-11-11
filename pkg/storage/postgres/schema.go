// Package postgres is the Postgres store, spec 2.8: three tables in a
// schema named by the storage prefix, the write as one transaction
// behind the row lock of the counter row, the list in one snapshot, and
// the watch by polling. ADR 0026.
package postgres

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var notIdent = regexp.MustCompile(`[^a-z0-9_]+`)

//= spec/solas.md#2-8-postgres-store
//# The Postgres store MUST keep its tables in a schema named by the
//# storage prefix of the API server.

// SchemaName is the schema that holds the tables of a storage prefix,
// spec 2.8: the prefix without its leading slash, in lower case, with
// every run of other characters as one underscore. "/registry" is
// "registry".
func SchemaName(prefix string) string {
	s := strings.ToLower(strings.TrimPrefix(prefix, "/"))
	s = notIdent.ReplaceAllString(s, "_")
	if s == "" {
		return "registry"
	}
	return s
}

// Error codes that the commit function raises. A five character code
// outside the standard classes is the server's own.
const (
	codeSealed   = "SLS01"
	codeConflict = "SLS02"
)

//= spec/solas.md#2-8-postgres-store
//# The store MUST have the tables `counters`, `objects`, and `events`.

//= spec/solas.md#2-8-postgres-store
//# A `counters` row holds, for one resource, the last issued resource
//# version `n` and the flag `sealed`.

//= spec/solas.md#2-8-postgres-store
//# An `objects` row holds, for one object, its resource, its key, the
//# resource version `rv` of its last write, and the encoded object `value`.

//= spec/solas.md#2-8-postgres-store
//# An `events` row holds, for one write, its resource, its resource
//# version `rv`, the event type, the key, the encoded object, and the time
//# `expires`.

// ddl is the schema, with {S} for the quoted schema name. Every statement
// is safe to run again.
const ddl = `
CREATE SCHEMA IF NOT EXISTS {S};
CREATE TABLE IF NOT EXISTS {S}.counters (
  resource text PRIMARY KEY,
  n bigint NOT NULL,
  sealed boolean NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS {S}.objects (
  resource text NOT NULL,
  key text NOT NULL,
  rv bigint NOT NULL,
  value bytea NOT NULL,
  PRIMARY KEY (resource, key)
);
CREATE TABLE IF NOT EXISTS {S}.events (
  resource text NOT NULL,
  rv bigint NOT NULL,
  type text NOT NULL,
  key text NOT NULL DEFAULT '',
  value bytea,
  prev bytea,
  expires timestamptz NOT NULL,
  PRIMARY KEY (resource, rv)
);
CREATE INDEX IF NOT EXISTS events_expires ON {S}.events (expires);
`

//= spec/solas.md#2-8-postgres-store
//# The server MUST run the transaction as one call of a function in the
//# database.

//= spec/solas.md#2-8-postgres-store
//# A lock held across a round trip to the server caps the resource at a
//# few writes per second, whatever the number of writers.

// commitFunction is the write of spec 2.8 as one function, so the lock
// on the counter row is held for the time of the function and not for a
// round trip per statement. It takes the writes of a batch as arrays of
// the same length, in the order of the batch, and returns the counter
// before the batch: the writes get the versions after it, in order.
//
// The counter update comes first and takes the row lock. The conditional
// statements follow; one that changes no row marks its write as a
// conflict and gets no event. When any write is a conflict the function
// raises, which rolls the transaction back, the counter update with it:
// the versions are never issued and the log has no hole. A sealed
// counter returns no row from the update, and the function raises with
// its own code.
const commitFunction = `
CREATE OR REPLACE FUNCTION {S}.solas_commit(
  res text, keys text[], actions text[], expects bigint[], vals bytea[], prevs bytea[], expires timestamptz
) RETURNS bigint AS $$
DECLARE
  k int := coalesce(array_length(keys, 1), 0);
  base bigint;
  cnt int;
  bad text := '';
  i int;
BEGIN
  INSERT INTO {S}.counters (resource, n) VALUES (res, 1) ON CONFLICT DO NOTHING;
  IF FOUND THEN
    INSERT INTO {S}.events (resource, rv, type, expires) VALUES (res, 1, 'INIT', expires);
  END IF;
  UPDATE {S}.counters SET n = n + k WHERE resource = res AND NOT sealed RETURNING n - k INTO base;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'store is sealed' USING ERRCODE = 'SLS01';
  END IF;
  FOR i IN 1..k LOOP
    IF actions[i] = 'create' THEN
      INSERT INTO {S}.objects (resource, key, rv, value) VALUES (res, keys[i], base + i, vals[i])
        ON CONFLICT DO NOTHING;
    ELSIF actions[i] = 'update' THEN
      UPDATE {S}.objects SET rv = base + i, value = vals[i]
        WHERE resource = res AND key = keys[i] AND rv = expects[i];
    ELSE
      DELETE FROM {S}.objects WHERE resource = res AND key = keys[i] AND rv = expects[i];
    END IF;
    GET DIAGNOSTICS cnt = ROW_COUNT;
    IF cnt <> 1 THEN
      bad := bad || i || ',';
    ELSIF actions[i] = 'create' THEN
      INSERT INTO {S}.events (resource, rv, type, key, value, expires)
        VALUES (res, base + i, 'ADDED', keys[i], vals[i], expires);
    ELSIF actions[i] = 'update' THEN
      INSERT INTO {S}.events (resource, rv, type, key, value, prev, expires)
        VALUES (res, base + i, 'MODIFIED', keys[i], vals[i], prevs[i], expires);
    ELSE
      INSERT INTO {S}.events (resource, rv, type, key, value, prev, expires)
        VALUES (res, base + i, 'DELETED', keys[i], vals[i], prevs[i], expires);
    END IF;
  END LOOP;
  IF bad <> '' THEN
    RAISE EXCEPTION 'object condition failed' USING ERRCODE = 'SLS02', DETAIL = bad;
  END IF;
  RETURN base;
END;
$$ LANGUAGE plpgsql;
`

// EnsureTables creates the schema of a storage prefix, its tables, and
// the commit function, when they do not exist. The server runs it at
// start when the storage URL has create-tables=true, spec 2.6.
func EnsureTables(ctx context.Context, pool *pgxpool.Pool, prefix string) error {
	schema := pgx.Identifier{SchemaName(prefix)}.Sanitize()
	for _, stmt := range []string{ddl, commitFunction} {
		if _, err := pool.Exec(ctx, strings.ReplaceAll(stmt, "{S}", schema)); err != nil {
			return fmt.Errorf("create the tables in %s: %w", schema, err)
		}
	}
	return nil
}
