package solas

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phoban01/solas/pkg/format"
)

// closedPort returns a port on which nothing listens, so a connection to
// it is refused at once.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

const kubeconfigTemplate = `apiVersion: v1
kind: Config
clusters:
- name: host
  cluster: {server: "https://127.0.0.1:%d", insecure-skip-tls-verify: true}
contexts:
- name: host
  context: {cluster: host, user: host}
current-context: host
users:
- name: host
  user: {token: none}
`

//= spec/solas.md#16-1-the-server
//= type=test
//# The readiness of the server MUST NOT depend on the store.

//= spec/solas.md#16-1-the-server
//= type=test
//# The server MUST start when the DynamoDB store does not answer.

//= spec/solas.md#16-1-the-server
//= type=test
//# Until the server has read the finalized format and the epoch of the
//# store, it MUST reject every write with `503 ServiceUnavailable`.

// TestReadyWithoutStore starts solas on a store endpoint that refuses
// every connection, on DynamoDB and on Postgres. The server reports
// ready and serves discovery, and a write fails with 503 because the
// finalized format is not read.
func TestReadyWithoutStore(t *testing.T) {
	for name, url := range map[string]string{
		"dynamodb": "dynamodb://solas?create-table=true&endpoint=http://127.0.0.1:%d",
		"postgres": "postgres://127.0.0.1:%d?sslmode=disable&create-tables=true",
	} {
		t.Run(name, func(t *testing.T) { testReadyWithoutStore(t, url) })
	}
}

// startWithoutStore starts solas on a store endpoint that refuses every
// connection, waits until it reports ready, and returns its base URL,
// an HTTP client for it, and a get function.
func startWithoutStore(t *testing.T, url string) (string, *http.Client, func(string) (int, string)) {
	t.Helper()
	closed := closedPort(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	// The controller part needs a kubeconfig for the host cluster. Nothing
	// listens there, so it waits for the API until the test ends.
	kube := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kube, fmt.Appendf(nil, kubeconfigTemplate, closed), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kube)
	t.Cleanup(func() {
		format.SetFinalized(1)
		format.SetEpoch(0)
	})

	o := NewOptions()
	o.Server.StorageURL = fmt.Sprintf(url, closed)
	o.Controller.ClusterID = "a"
	o.Controller.LeaderElection = false
	ro := o.Server.RecommendedOptions
	// No host cluster in a unit test: no delegated authentication or
	// authorization, no core informers, no admission, no flow control.
	ro.Authentication, ro.Authorization, ro.CoreAPI, ro.Admission = nil, nil, nil, nil
	ro.Features.EnablePriorityAndFairness = false
	ro.SecureServing.ServerCert.CertDirectory = t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ro.SecureServing.Listener = ln
	ro.SecureServing.BindPort = ln.Addr().(*net.TCPAddr).Port
	// Run applies the options, so the port is read before Run starts.
	port := ro.SecureServing.BindPort
	if err := o.Server.Registry.Set(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("Run did not return within 30s of the cancel")
		}
	})

	base := fmt.Sprintf("https://127.0.0.1:%d", port)
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	get := func(path string) (int, string) {
		resp, err := hc.Get(base + path)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	deadline := time.Now().Add(60 * time.Second)
	for {
		code, body := get("/readyz?verbose")
		if code == http.StatusOK {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned before the server was ready: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("/readyz is %d after 60s with the store down:\n%s", code, body)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return base, hc, get
}

func testReadyWithoutStore(t *testing.T, url string) {
	base, hc, get := startWithoutStore(t, url)
	if code, body := get("/apis/solas.dev/v1alpha1"); code != http.StatusOK {
		t.Errorf("discovery of solas.dev/v1alpha1 = %d with the store down, want 200: %s", code, body)
	}

	device := `{"apiVersion":"solas.dev/v1alpha1","kind":"Device","metadata":{"name":"d1"}}`
	resp, err := hc.Post(base+"/apis/solas.dev/v1alpha1/devices", "application/json", strings.NewReader(device))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("create with the store down = %d, want 503: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "finalized format") {
		t.Errorf("the 503 does not name the finalized format: %s", body)
	}
}
