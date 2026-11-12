package migrate

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/phoban01/solas/internal/ddbtest"
	"github.com/phoban01/solas/internal/etcdtest"
	"github.com/phoban01/solas/internal/pgtest"
)

// openDynamo opens a store on a new table in dynamodb-local.
func openDynamo(t *testing.T) (*Store, string) {
	t.Helper()
	endpoint := os.Getenv(ddbtest.EndpointEnv)
	if endpoint == "" {
		t.Skipf("%s is not set; run the tests with devbox run test", ddbtest.EndpointEnv)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "local")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "local")
	table := ddbtest.TableName(t)
	ddbtest.DeleteTable(t, ddbtest.Client(t), table)
	u := "dynamodb://" + table + "?create-table=true&endpoint=" + url.QueryEscape(endpoint)
	return open(t, u, "/registry"), u
}

// openEtcd opens a store under a new prefix in the test etcd.
func openEtcd(t *testing.T) (*Store, string) {
	t.Helper()
	u := "etcd://" + strings.TrimPrefix(etcdtest.Servers(t)[0], "http://")
	return open(t, u, etcdtest.Prefix(t)), u
}

func open(t *testing.T, u, prefix string) *Store {
	t.Helper()
	s, err := Open(context.Background(), u, prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// openPostgres opens a store in a new schema of the test Postgres.
func openPostgres(t *testing.T) (*Store, string) {
	t.Helper()
	u := pgtest.URL(t)
	return open(t, u, pgtest.Prefix(t)), u
}
