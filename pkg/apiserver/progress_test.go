package apiserver_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/apiserver/pkg/storage/cacher/delegator"

	"github.com/phoban01/solas/internal/teststore"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/registry/solas/device"
	"github.com/phoban01/solas/pkg/storage/dynamo"
)

// countingQueries counts the Query calls of the DynamoDB client, which a
// list from the table makes and a list from the cache does not.
type countingQueries struct {
	dynamo.API
	queries atomic.Int64
}

func (c *countingQueries) Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	c.queries.Add(1)
	return c.API.Query(ctx, in, opts...)
}

//= spec/solas.md#4-2-poll
//= type=test
//# A consistent list through the API server MUST come from the watch cache
//# once the cache has reached the counter value.

// TestConsistentListFromCache: on the DynamoDB store, a list with no
// resource version comes from the watch cache once the cache has the
// devices, so it makes no Query of the table.
func TestConsistentListFromCache(t *testing.T) {
	g := teststore.Dynamo(t)
	counting := &countingQueries{API: g.Dynamo.Client}
	g.Dynamo.Client = counting
	r, _, err := device.NewREST(apiserver.Scheme, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Store.DestroyFunc)
	ctx := genericapirequest.WithNamespace(context.Background(), "")
	for _, name := range []string{"d1", "d2", "d3"} {
		if _, err := r.Create(ctx, &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: name}}, rest.ValidateAllObjectFunc, &metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// The cache fills from the store; wait until a consistent list works.
	deadline := time.Now().Add(10 * time.Second)
	for {
		obj, err := r.List(ctx, &metainternalversion.ListOptions{})
		if err == nil && len(obj.(*solas.DeviceList).Items) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("list after 10s: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !delegator.ConsistentReadSupported() {
		t.Fatal("the feature checker does not report support for progress requests on the DynamoDB store")
	}

	before := counting.queries.Load()
	for range 5 {
		obj, err := r.List(ctx, &metainternalversion.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if n := len(obj.(*solas.DeviceList).Items); n != 3 {
			t.Fatalf("list has %d devices, want 3", n)
		}
	}
	if q := counting.queries.Load() - before; q != 0 {
		t.Errorf("5 consistent lists made %d Query calls to the table, want 0: the cache did not serve them", q)
	}
}
