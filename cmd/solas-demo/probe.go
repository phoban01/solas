package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"time"
)

// runProbe makes one GET to a URL and prints what came back. It exits 0
// on any HTTP answer and 1 when the call does not connect within the
// timeout. The e2e test runs it in a pod to check which addresses the
// workloads can reach, ADR 0017.
func runProbe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	url := fs.String("url", "", "the URL to GET")
	timeout := fs.Duration("timeout", 3*time.Second, "how long to wait for an answer")
	delay := fs.Duration("delay", 0, "how long to wait before the call, so the network policy of a new pod is in place")
	if err := fs.Parse(args); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(*delay):
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("UNREACHABLE %s: %v\n", *url, err)
		return fmt.Errorf("unreachable")
	}
	resp.Body.Close()
	fmt.Printf("REACHED %s: %d\n", *url, resp.StatusCode)
	return nil
}
