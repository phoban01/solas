package pgtest

import (
	"context"
	"testing"
)

// TestPrefix shows that the test Postgres answers, that a schema can be
// made under the prefix, and that the cleanup drops it.
func TestPrefix(t *testing.T) {
	ctx := context.Background()
	u := URL(t)
	var prefix string
	t.Run("schema", func(t *testing.T) {
		prefix = Prefix(t)
		conn, err := Connect(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, `CREATE SCHEMA "`+Schema(prefix)+`"`); err != nil {
			t.Fatal(err)
		}
	})
	conn, err := Connect(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	err = conn.QueryRow(ctx, "SELECT count(*) FROM information_schema.schemata WHERE schema_name = $1", Schema(prefix)).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the schema of %s is still there after the subtest ended", prefix)
	}
}
