package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	storageurl "github.com/phoban01/solas/pkg/storage"
	"github.com/phoban01/solas/pkg/storage/dynamo"
)

// The item modes measure the store under ADR 0025, with the DynamoDB
// client alone and no API server, issue #290.
//
//   - counter: every writer sends UpdateItem ADD n :1 to one item, as the
//     issue step of the two-step write does, spec 3.1.
//   - commit: every writer sends TransactWriteItems with a put of two
//     items of its own, conditional on their version, as the commit step
//     does, spec 2.3. No item is shared, so the rows show whether
//     transactions on different items wait for each other. The items
//     share one partition key, as the object items of a resource do,
//     spec 2.2; --spread gives each writer a partition key of its own.
//
// Each mode prints one row: operations per second, the p50 and p99 of an
// operation, the throttles that the SDK retried, the errors, and the TCP
// connect time to the endpoint.

// itemPK is the partition of the items of these modes.
const itemPK = "bench#item"

// throttleCounting counts the throttles of a call, which the SDK retries
// inside the call, so the row shows them.
type throttleCounting struct {
	aws.Retryer
	throttles *atomic.Int64
}

func (t *throttleCounting) IsErrorRetryable(err error) bool {
	if retry.IsErrorThrottles(retry.DefaultThrottles).IsErrorThrottle(err).Bool() {
		t.throttles.Add(1)
	}
	return t.Retryer.IsErrorRetryable(err)
}

// itemStats are the counts of an item mode.
type itemStats struct {
	ops, errors, throttles atomic.Int64
	mu                     sync.Mutex
	latency                []time.Duration
}

func (s *itemStats) done(d time.Duration) {
	s.ops.Add(1)
	s.mu.Lock()
	s.latency = append(s.latency, d)
	s.mu.Unlock()
}

// runItem runs the counter or commit mode.
func runItem(ctx context.Context, o options) error {
	sc, err := storageurl.Parse(o.storageURL)
	if err != nil {
		return fmt.Errorf("--storage-url: %w", err)
	}
	if sc.Kind != storageurl.DynamoDB {
		return fmt.Errorf("--mode %s needs a DynamoDB store", o.mode)
	}
	client, err := storageurl.DynamoClientWithAttempts(ctx, sc, benchAttempts)
	if err != nil {
		return err
	}
	if sc.CreateTable {
		if err := dynamo.EnsureTable(ctx, client, sc.Table); err != nil {
			return err
		}
	}
	connect, err := connectTime(ctx, sc)
	if err != nil {
		return err
	}
	var st itemStats
	counting := func(opts *dynamodb.Options) {
		opts.Retryer = &throttleCounting{Retryer: opts.Retryer, throttles: &st.throttles}
	}
	run := fmt.Sprintf("%d", time.Now().UnixNano())
	keys := itemKeys(o.mode, run, o.writers, o.spread)
	defer func() {
		for _, k := range keys {
			_, err := client.DeleteItem(context.Background(), &dynamodb.DeleteItemInput{
				TableName: aws.String(sc.Table), Key: k})
			if err != nil {
				fmt.Fprintln(os.Stderr, "solas-bench: clean up:", err)
			}
		}
	}()

	stop := make(chan struct{})
	timer := time.AfterFunc(o.duration, func() { close(stop) })
	defer timer.Stop()
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < o.writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var version int64
			for {
				select {
				case <-stop:
					return
				default:
				}
				from := time.Now()
				var err error
				switch o.mode {
				case "counter":
					err = addOne(ctx, client, sc.Table, keys[0], counting)
				case "commit":
					version++
					err = putTwo(ctx, client, sc.Table, keys[2*i], keys[2*i+1], version, counting)
				}
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					if st.errors.Add(1) <= 5 {
						fmt.Fprintln(os.Stderr, "solas-bench:", err)
					}
					continue
				}
				st.done(time.Since(from))
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	if err := ctx.Err(); err != nil {
		return err
	}
	ms := func(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }
	mode := o.mode
	if o.mode == "commit" && o.spread {
		mode = "commit spread"
	}
	cols := []string{"mode", "writers", "ops/s", "p50 ms", "p99 ms", "throttles", "errors", "connect ms"}
	row := []string{mode, fmt.Sprint(o.writers), fmt.Sprintf("%.1f", float64(st.ops.Load())/elapsed.Seconds()),
		ms(percentile(st.latency, 0.5)), ms(percentile(st.latency, 0.99)),
		fmt.Sprint(st.throttles.Load()), fmt.Sprint(st.errors.Load()), ms(connect)}
	if o.header {
		fmt.Println(strings.Join(cols, "\t"))
	}
	fmt.Println(strings.Join(row, "\t"))
	return nil
}

// itemKeys returns the keys of a run: one item in counter mode, two per
// writer in commit mode, under one partition key, or one per writer when
// spread is set. The run id keeps two runs apart.
func itemKeys(mode, run string, writers int, spread bool) []map[string]types.AttributeValue {
	key := func(pk, sk string) map[string]types.AttributeValue {
		return map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: pk},
			"sk": &types.AttributeValueMemberS{Value: sk},
		}
	}
	if mode == "counter" {
		return []map[string]types.AttributeValue{key(itemPK, run)}
	}
	keys := make([]map[string]types.AttributeValue, 0, 2*writers)
	for i := 0; i < writers; i++ {
		pk := itemPK
		if spread {
			pk = fmt.Sprintf("%s#%d", itemPK, i)
		}
		keys = append(keys, key(pk, fmt.Sprintf("%s/%d/a", run, i)), key(pk, fmt.Sprintf("%s/%d/b", run, i)))
	}
	return keys
}

// addOne is the issue step: one atomic add to the counter item, which
// returns the new value.
func addOne(ctx context.Context, c *dynamodb.Client, table string, key map[string]types.AttributeValue, opt func(*dynamodb.Options)) error {
	_, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(table),
		Key:                       key,
		UpdateExpression:          aws.String("ADD n :one"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":one": &types.AttributeValueMemberN{Value: "1"}},
		ReturnValues:              types.ReturnValueUpdatedNew,
	}, opt)
	return err
}

// putTwo is the commit step: one transaction that puts two items of the
// writer, each conditional on the version the writer wrote last.
func putTwo(ctx context.Context, c *dynamodb.Client, table string, a, b map[string]types.AttributeValue, version int64, opt func(*dynamodb.Options)) error {
	put := func(key map[string]types.AttributeValue) types.TransactWriteItem {
		item := map[string]types.AttributeValue{
			"pk":    key["pk"],
			"sk":    key["sk"],
			"rv":    &types.AttributeValueMemberN{Value: fmt.Sprint(version)},
			"value": &types.AttributeValueMemberS{Value: strings.Repeat("x", 512)},
		}
		cond := "attribute_not_exists(rv) OR rv = :prev"
		return types.TransactWriteItem{Put: &types.Put{
			TableName:                 aws.String(table),
			Item:                      item,
			ConditionExpression:       aws.String(cond),
			ExpressionAttributeValues: map[string]types.AttributeValue{":prev": &types.AttributeValueMemberN{Value: fmt.Sprint(version - 1)}},
		}}
	}
	_, err := c.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{put(a), put(b)},
	}, opt)
	return err
}

// connectTime measures one TCP connect to the DynamoDB endpoint, so the
// row carries the round trip of the run.
func connectTime(ctx context.Context, sc storageurl.Config) (time.Duration, error) {
	region := sc.Region
	if region == "" {
		region = "us-east-1"
	}
	addr := fmt.Sprintf("dynamodb.%s.amazonaws.com:443", region)
	if sc.Endpoint != "" {
		u, err := url.Parse(sc.Endpoint)
		if err != nil {
			return 0, fmt.Errorf("endpoint: %w", err)
		}
		addr = u.Host
		if u.Port() == "" {
			addr += ":80"
		}
	}
	from := time.Now()
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("connect to %s: %w", addr, err)
	}
	d := time.Since(from)
	_ = conn.Close()
	return d, nil
}
