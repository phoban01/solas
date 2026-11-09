package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// defaultRegion is the AWS region when neither the storage URL nor the
// environment names one.
const defaultRegion = "us-east-1"

// CallTimeout bounds each call to DynamoDB, so a store that hangs fails
// the request instead of holding it, spec 16.1. Each call makes at most
// CallAttempts attempts in the server, which turns a store that is
// throttled or down into a quick 503 that the controllers retry within
// D / 3. A tool that fills a table, such as solas-bench, asks for more.
const (
	CallTimeout  = 10 * time.Second
	CallAttempts = 2
)

// DynamoClient returns a DynamoDB client for a DynamoDB storage URL, with
// the server's timeout and attempts. The credentials come from the AWS
// environment.
func DynamoClient(ctx context.Context, c Config) (*dynamodb.Client, error) {
	return DynamoClientWithAttempts(ctx, c, CallAttempts)
}

// DynamoClientWithAttempts returns a client that makes up to attempts
// attempts per call, with the SDK's backoff between them.
func DynamoClientWithAttempts(ctx context.Context, c Config, attempts int) (*dynamodb.Client, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if c.Region != "" {
		opts = append(opts, awsconfig.WithRegion(c.Region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = defaultRegion
	}
	return dynamodb.NewFromConfig(cfg, func(opts *dynamodb.Options) {
		if c.Endpoint != "" {
			opts.BaseEndpoint = aws.String(c.Endpoint)
		}
		opts.HTTPClient = awshttp.NewBuildableClient().WithTimeout(CallTimeout)
		opts.RetryMaxAttempts = attempts
	}), nil
}
