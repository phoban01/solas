// Package pgtest connects tests to the Postgres that scripts/test.sh
// starts.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	storageurl "github.com/phoban01/solas/pkg/storage"
	"github.com/phoban01/solas/pkg/storage/postgres"
)

// URLEnv names the variable that holds the storage URL of the test
// Postgres. The user and the password are in PGUSER and PGPASSWORD.
const URLEnv = "SOLAS_POSTGRES_URL"

// URL returns the storage URL of the test Postgres. It skips the test
// when URLEnv is not set.
func URL(t testing.TB) string {
	t.Helper()
	u := os.Getenv(URLEnv)
	if u == "" {
		t.Skipf("%s is not set; run the tests with devbox run test", URLEnv)
	}
	return u
}

// Prefix returns a storage prefix that no other test uses, so tests that
// share one Postgres do not see each other's objects, and drops its
// schema when the test ends.
func Prefix(t testing.TB) string {
	t.Helper()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	prefix := "/test_" + hex.EncodeToString(b)
	u := URL(t)
	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := Connect(ctx, u)
		if err != nil {
			t.Logf("drop the schema of %s: %v", prefix, err)
			return
		}
		defer conn.Close(ctx)
		_, err = conn.Exec(ctx, `DROP SCHEMA IF EXISTS "`+postgres.SchemaName(prefix)+`" CASCADE`)
		if err != nil {
			t.Logf("drop the schema of %s: %v", prefix, err)
		}
	})
	return prefix
}

// Connect opens one connection to the Postgres that a storage URL names.
func Connect(ctx context.Context, u string) (*pgx.Conn, error) {
	sc, err := storageurl.Parse(u)
	if err != nil {
		return nil, err
	}
	return pgx.Connect(ctx, sc.PostgresURL())
}
