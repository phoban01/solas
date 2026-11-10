// Package dynamo implements the Kubernetes storage.Interface on one
// DynamoDB table. spec/solas.md sections 2 to 4 give the rules.
package dynamo

import (
	"context"
	"errors"

	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/storage"
)

// API is the part of the DynamoDB client that the store uses.
type API interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
}

// Default settings.
const (
	DefaultTable          = "solas"
	DefaultEventRetention = time.Hour
	//= spec/solas.md#4-2-poll
	//# The server SHOULD poll each resource at least once each second.

	//= spec/solas.md#4-2-poll
	//# The server SHOULD NOT poll a resource more often than every 100
	//# milliseconds.
	DefaultPollInterval     = 200 * time.Millisecond
	DefaultProgressInterval = time.Minute
)

// Config holds the settings that all stores of one API server share.
type Config struct {
	Client API
	//= spec/solas.md#2-1-table
	//# The table name MUST be a setting of the API server.
	Table string
	//= spec/solas.md#4-1-event-log
	//# The retention time MUST be a setting of the API server.
	EventRetention time.Duration
	PollInterval   time.Duration
	// ProgressInterval is how often a watch that asked for progress
	// notifications gets a bookmark.
	ProgressInterval time.Duration
}

type store struct {
	client           API
	table            string
	retention        time.Duration
	pollInterval     time.Duration
	progressInterval time.Duration
	codec            runtime.Codec
	versioner        storage.Versioner
	newFunc          func() runtime.Object
	newListFunc      func() runtime.Object
	resourcePrefix   string
	// resource names the counter item and the partitions of this resource.
	resource      string
	groupResource schema.GroupResource
	now           func() time.Time
	watchers      watchers
	// batch collects the writes of this server to the resource, spec 2.3.
	batch batcher
}

var _ storage.Interface = &store{}

// New returns a store for one resource. Keys passed to the store must start
// with resourcePrefix. prefix separates the data of different API servers
// in one table, as the etcd path prefix does.
func New(cfg Config, codec runtime.Codec, newFunc, newListFunc func() runtime.Object,
	prefix, resourcePrefix string, groupResource schema.GroupResource) (storage.Interface, error) {
	return newStore(cfg, codec, newFunc, newListFunc, prefix, resourcePrefix, groupResource)
}

func newStore(cfg Config, codec runtime.Codec, newFunc, newListFunc func() runtime.Object,
	prefix, resourcePrefix string, groupResource schema.GroupResource) (*store, error) {
	if cfg.Client == nil {
		return nil, errors.New("dynamo: Config.Client is nil")
	}
	if resourcePrefix == "" || resourcePrefix == "/" || resourcePrefix[0] != '/' {
		return nil, fmt.Errorf("dynamo: invalid resource prefix %q", resourcePrefix)
	}
	s := &store{
		client:           cfg.Client,
		table:            cfg.Table,
		retention:        cfg.EventRetention,
		pollInterval:     cfg.PollInterval,
		progressInterval: cfg.ProgressInterval,
		codec:            codec,
		//= spec/solas.md#3-3-objects
		//# The server MUST encode each resource version as a decimal string.
		versioner:      storage.APIObjectVersioner{},
		newFunc:        newFunc,
		newListFunc:    newListFunc,
		resourcePrefix: resourcePrefix,
		resource:       joinPrefix(prefix, strings.TrimSuffix(resourcePrefix, "/")),
		groupResource:  groupResource,
		now:            time.Now,
	}
	if s.table == "" {
		s.table = DefaultTable
	}
	if s.retention == 0 {
		s.retention = DefaultEventRetention
	}
	if s.progressInterval == 0 {
		s.progressInterval = DefaultProgressInterval
	}
	if s.pollInterval == 0 {
		s.pollInterval = DefaultPollInterval
	}
	return s, nil
}

func (s *store) Versioner() storage.Versioner { return s.versioner }

// Stats counts the objects of the resource.
func (s *store) Stats(ctx context.Context) (storage.Stats, error) {
	var count int64
	var from map[string]types.AttributeValue
	for {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(s.table),
			KeyConditionExpression:    aws.String("pk = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": str(s.objectPK())},
			Select:                    types.SelectCount,
			ExclusiveStartKey:         from,
		})
		if err != nil {
			return storage.Stats{}, fmt.Errorf("count objects: %w", err)
		}
		count += int64(out.Count)
		if out.LastEvaluatedKey == nil {
			return storage.Stats{ObjectCount: count}, nil
		}
		from = out.LastEvaluatedKey
	}
}

// ReadinessCheck checks that the table can be reached.
func (s *store) ReadinessCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(s.table)})
	if err != nil {
		return fmt.Errorf("table %s is not ready: %w", s.table, err)
	}
	return nil
}

// GetCurrentResourceVersion returns the counter of the resource.
func (s *store) GetCurrentResourceVersion(ctx context.Context) (uint64, error) {
	c, err := s.readCounter(ctx)
	if err != nil {
		return 0, err
	}
	//= spec/solas.md#4-2-poll
	//# When the server reads the counter for a consistent list, it SHOULD poll
	//# with that value at once and then send a bookmark.
	s.watchers.hint(c)
	return c, nil
}

// EnableResourceSizeEstimation does nothing. Stats reports no object size.
func (s *store) EnableResourceSizeEstimation(storage.KeysFunc) error { return nil }

// CompactRevision returns 0: the store does not compact. Expired events end
// a watch with 410 Gone instead, spec 4.3.
func (s *store) CompactRevision() int64 { return 0 }

// joinPrefix joins the path prefix and the resource prefix, so that
// "/registry" and "/devices" give "/registry/devices".
func joinPrefix(prefix, resourcePrefix string) string {
	if prefix == "" || prefix == "/" {
		return resourcePrefix
	}
	if prefix[0] != '/' {
		prefix = "/" + prefix
	}
	for len(prefix) > 1 && prefix[len(prefix)-1] == '/' {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + resourcePrefix
}

// RequestWatchProgress asks each open watch that set ProgressNotify to send
// a bookmark with its current resource version.
func (s *store) RequestWatchProgress(ctx context.Context) error {
	s.watchers.requestProgress()
	return nil
}
