// Package pgtest connects tests to the Postgres that scripts/test.sh
// starts.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	storageurl "github.com/phoban01/solas/pkg/storage"
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
// schema when the test ends. The prefix is chosen so that its schema is
// the prefix without the slash, as pkg/storage/postgres names it, which
// this package cannot import: the tests of the store use this package.
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
		_, err = conn.Exec(ctx, `DROP SCHEMA IF EXISTS "`+Schema(prefix)+`" CASCADE`)
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

// Schema is the schema of a prefix from Prefix: the prefix without its
// leading slash.
func Schema(prefix string) string { return strings.TrimPrefix(prefix, "/") }
