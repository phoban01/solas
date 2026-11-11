package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/apimachinery/pkg/api/apitesting"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/apis/example"
	examplev1 "k8s.io/apiserver/pkg/apis/example/v1"

	"github.com/phoban01/solas/internal/pgtest"
	storageurl "github.com/phoban01/solas/pkg/storage"
)

var (
	scheme = runtime.NewScheme()
	codecs = serializer.NewCodecFactory(scheme)
	codec  = apitesting.TestCodec(codecs, examplev1.SchemeGroupVersion)
)

func init() {
	metav1.AddToGroupVersion(scheme, metav1.SchemeGroupVersion)
	utilruntime.Must(example.AddToScheme(scheme))
	utilruntime.Must(examplev1.AddToScheme(scheme))
}

func newPod() runtime.Object     { return &example.Pod{} }
func newPodList() runtime.Object { return &example.PodList{} }

var testGR = schema.GroupResource{Group: "example.apiserver.k8s.io", Resource: "pods"}

// newTestPool opens a pool on the test Postgres and closes it at the end.
func newTestPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

// newTestStore returns a store in a schema of its own in the test
// Postgres.
func newTestStore(t *testing.T) *store {
	t.Helper()
	pool := newTestPool(t)
	prefix := pgtest.Prefix(t)
	if err := EnsureTables(context.Background(), pool, prefix); err != nil {
		t.Fatal(err)
	}
	s, err := newStore(Config{Pool: pool, PollInterval: 20 * time.Millisecond, ProgressInterval: 200 * time.Millisecond},
		codec, newPod, newPodList, prefix, "/pods/", testGR)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
