package apiserver

import (
	"context"
	"sync"

	clientv3 "go.etcd.io/etcd/client/v3"
	"k8s.io/apiserver/pkg/storage"
	etcdfeature "k8s.io/apiserver/pkg/storage/feature"
)

//= spec/solas.md#4-2-poll
//# A consistent list through the API server MUST come from the watch cache
//# once the cache has reached the counter value.

// The API server library serves a consistent list from the watch cache
// only when its feature checker says that the store answers progress
// requests, and the checker learns that from an etcd client's version.
// The DynamoDB store answers progress requests, spec 4.2, so the server
// tells the checker through a client that reports a version with the
// support. Without this every consistent list queries the table.

var markProgressOnce sync.Once

// markProgressSupport tells the feature checker that the store answers
// progress requests. It runs once per process.
func markProgressSupport() {
	markProgressOnce.Do(func() {
		etcdfeature.DefaultFeatureSupportChecker.CheckClient(context.Background(), progressClient{}, storage.RequestWatchProgress)
	})
}

// progressClient stands in for an etcd client whose endpoints support
// progress requests. The checker accepts etcd 3.4.31, 3.5.13, and later.
type progressClient struct{}

func (progressClient) Endpoints() []string { return []string{"dynamodb"} }

func (progressClient) Status(context.Context, string) (*clientv3.StatusResponse, error) {
	return &clientv3.StatusResponse{Version: "3.6.0"}, nil
}
