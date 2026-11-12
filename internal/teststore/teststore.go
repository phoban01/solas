// Package teststore runs a test once on each store: the DynamoDB store on
// dynamodb-local, the etcd store, and the Postgres store, as
// scripts/test.sh starts them.
package teststore

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"time"

	"github.com/phoban01/solas/internal/ddbtest"
	"github.com/phoban01/solas/internal/etcdtest"
	"github.com/phoban01/solas/internal/pgtest"
	"github.com/phoban01/solas/pkg/apiserver"
	storageurl "github.com/phoban01/solas/pkg/storage"
	"github.com/phoban01/solas/pkg/storage/dynamo"
	"github.com/phoban01/solas/pkg/storage/postgres"
)

// Store names a store and builds an empty store of that kind.
type Store struct {
	Name string
	New  func(t testing.TB) apiserver.RESTOptionsGetter
}

// All returns every store.
func All() []Store {
	return []Store{{Name: "dynamodb", New: Dynamo}, {Name: "etcd", New: Etcd}, {Name: "postgres", New: Postgres}}
}

// Run runs f as a subtest on each store. A subtest skips when its store
// does not run.
func Run(t *testing.T, f func(t *testing.T, g apiserver.RESTOptionsGetter)) {
	t.Helper()
	for _, s := range All() {
		t.Run(s.Name, func(t *testing.T) { f(t, s.New(t)) })
	}
}

// Dynamo returns a getter on a new, empty table in dynamodb-local.
func Dynamo(t testing.TB) apiserver.RESTOptionsGetter {
	t.Helper()
	c := ddbtest.Client(t)
	table := ddbtest.TableName(t)
	ddbtest.DeleteTable(t, c, table)
	if err := dynamo.EnsureTable(context.Background(), c, table); err != nil {
		t.Fatal(err)
	}
	return apiserver.RESTOptionsGetter{
		Dynamo:          dynamo.Config{Client: c, Table: table, PollInterval: 20 * time.Millisecond},
		Codec:           apiserver.StorageCodec(),
		EncodeVersioner: apiserver.StorageVersioner(),
		Prefix:          "/registry",
	}
}

// Etcd returns a getter on a new, empty prefix in the test etcd.
func Etcd(t testing.TB) apiserver.RESTOptionsGetter {
	t.Helper()
	return apiserver.RESTOptionsGetter{
		EtcdServers:     etcdtest.Servers(t),
		Codec:           apiserver.StorageCodec(),
		EncodeVersioner: apiserver.StorageVersioner(),
		Prefix:          etcdtest.Prefix(t),
	}
}

// Postgres returns a getter on a new, empty schema in the test Postgres.
func Postgres(t testing.TB) apiserver.RESTOptionsGetter {
	t.Helper()
	sc, err := storageurl.Parse(pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), sc.PostgresURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := pgtest.Prefix(t)
	if err := postgres.EnsureTables(context.Background(), pool, prefix); err != nil {
		t.Fatal(err)
	}
	return apiserver.RESTOptionsGetter{
		Postgres:        postgres.Config{Pool: pool, PollInterval: 20 * time.Millisecond},
		Codec:           apiserver.StorageCodec(),
		EncodeVersioner: apiserver.StorageVersioner(),
		Prefix:          prefix,
	}
}
