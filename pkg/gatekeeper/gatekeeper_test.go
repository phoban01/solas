package gatekeeper

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/phoban01/solas/internal/fakekube"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

// upstream is a fake actuator that counts the calls that reach it.
type upstream struct {
	calls atomic.Int64
	// hold blocks each call until released, for the lock test.
	hold chan struct{}
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	u.calls.Add(1)
	if u.hold != nil {
		<-u.hold
	}
	w.WriteHeader(http.StatusOK)
}

func device(name, machine string, ref *solasv1alpha1.ClaimRef, token int64) *solasv1alpha1.Device {
	d := &solasv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: name}}
	d.Spec.Attributes = map[string]string{"machineID": machine}
	d.Status.ClaimRef = ref
	d.Status.FencingToken = token
	return d
}

func newGatekeeper(t *testing.T, up http.Handler, devices ...*solasv1alpha1.Device) *httptest.Server {
	t.Helper()
	us := httptest.NewServer(up)
	t.Cleanup(us.Close)
	u, _ := url.Parse(us.URL)
	k := fakekube.NewClient(toObjects(devices)...)
	g, err := New(Options{Upstream: u, Attribute: "machineID", Pattern: regexp.MustCompile(`^/machines/([^/]+)`),
		Index: ListIndex{Reader: k}, Reader: k})
	if err != nil {
		t.Fatal(err)
	}
	gs := httptest.NewServer(g)
	t.Cleanup(gs.Close)
	return gs
}

func call(t *testing.T, gs *httptest.Server, method, path, claim, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, gs.URL+path, strings.NewReader("{}"))
	if claim != "" {
		req.Header.Set(ClaimHeader, claim)
	}
	if token != "" {
		req.Header.Set(TokenHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

var holder = &solasv1alpha1.ClaimRef{Member: "a", MemberUID: "ua", Namespace: "work", Name: "job", UID: "u1"}

//= spec/solas.md#17-2-check
//= type=test
//# It MUST forward the request only when the `claimRef` of the device names
//# the member, namespace, and name of the header, and the `fencingToken`
//# of the device equals the token of the header.

//= spec/solas.md#17-2-check
//= type=test
//# It MUST reject every other request that changes a device with
//# `403 Forbidden`, and say why.

//= spec/solas.md#17-1-request
//= type=test
//# A request that changes a device MUST carry the claim of its workload in
//# the header `Solas-Claim`, as `member/namespace/name`.

//= spec/solas.md#17-1-request
//= type=test
//# It MUST carry the fencing token of the claim in the header
//# `Solas-Fencing-Token`.

//= spec/solas.md#17-2-check
//= type=test
//# A request whose path names no device MUST fail with `403 Forbidden`.

func TestGatekeeperChecks(t *testing.T) {
	up := &upstream{}
	gs := newGatekeeper(t, up, device("gpu-1", "m1", holder, 2), device("gpu-2", "m2", nil, 0))
	cases := []struct {
		name, path, claim, token string
		want                     int
		reason                   string
	}{
		{"holder with the token", "/machines/m1/deploy", "a/work/job", "2", http.StatusOK, ""},
		{"stale token", "/machines/m1/deploy", "a/work/job", "1", http.StatusForbidden, "has token 2"},
		{"another claim", "/machines/m1/deploy", "b/work/job", "2", http.StatusForbidden, "held by a/work/job"},
		{"free device", "/machines/m2/deploy", "a/work/job", "1", http.StatusForbidden, "is free"},
		{"no claim header", "/machines/m1/deploy", "", "2", http.StatusForbidden, ClaimHeader},
		{"no token header", "/machines/m1/deploy", "a/work/job", "", http.StatusForbidden, TokenHeader},
		{"unknown machine", "/machines/m9/deploy", "a/work/job", "2", http.StatusForbidden, "no device has machineID=m9"},
		{"path without a machine", "/status", "a/work/job", "2", http.StatusForbidden, "names no machine"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := up.calls.Load()
			resp := call(t, gs, http.MethodPost, c.path, c.claim, c.token)
			if resp.StatusCode != c.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, c.want)
			}
			reached := up.calls.Load() - before
			if c.want == http.StatusOK && reached != 1 || c.want != http.StatusOK && reached != 0 {
				t.Errorf("the actuator saw %d calls, want %d", reached, map[bool]int{true: 1, false: 0}[c.want == http.StatusOK])
			}
			if c.reason != "" {
				body := make([]byte, 256)
				n, _ := resp.Body.Read(body)
				if !strings.Contains(string(body[:n]), c.reason) {
					t.Errorf("reason = %q, want it to contain %q", string(body[:n]), c.reason)
				}
			}
		})
	}
}

//= spec/solas.md#17-1-request
//= type=test
//# A request with the method `GET`, `HEAD`, or `OPTIONS` reads and MUST
//# pass through without a check.

func TestReadsPassThrough(t *testing.T) {
	up := &upstream{}
	gs := newGatekeeper(t, up, device("gpu-1", "m1", holder, 2))
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if resp := call(t, gs, m, "/machines/m1", "", ""); resp.StatusCode != http.StatusOK {
			t.Errorf("%s without headers = %d, want 200", m, resp.StatusCode)
		}
	}
	if up.calls.Load() != 3 {
		t.Errorf("the actuator saw %d reads, want 3", up.calls.Load())
	}
}

//= spec/solas.md#17-3-lock
//= type=test
//# The gatekeeper MUST hold a lock per device from the check until the
//# actuator answers.

// TestLockPerDevice: while the actuator works on one request of a device,
// a second request of the same device waits, and a request of another
// device does not.
func TestLockPerDevice(t *testing.T) {
	up := &upstream{hold: make(chan struct{})}
	other := &solasv1alpha1.ClaimRef{Member: "b", MemberUID: "ub", Namespace: "work", Name: "job", UID: "u2"}
	gs := newGatekeeper(t, up, device("gpu-1", "m1", holder, 2), device("gpu-2", "m2", other, 1))
	var wg sync.WaitGroup
	first := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(first)
		call(t, gs, http.MethodPost, "/machines/m1/deploy", "a/work/job", "2")
	}()
	<-first
	deadline := time.Now().Add(5 * time.Second)
	for up.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Another device goes through while m1 is held.
	done := make(chan struct{})
	go func() {
		call(t, gs, http.MethodPost, "/machines/m2/power", "b/work/job", "1")
		close(done)
	}()
	// The second call reaches the upstream and blocks on hold too.
	deadline = time.Now().Add(5 * time.Second)
	for up.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if up.calls.Load() != 2 {
		t.Fatalf("a request of another device waited behind m1: %d calls reached the actuator, want 2", up.calls.Load())
	}
	// A second request of m1 must not reach the upstream while the first
	// holds the lock.
	second := make(chan struct{})
	go func() {
		call(t, gs, http.MethodPost, "/machines/m1/power", "a/work/job", "2")
		close(second)
	}()
	time.Sleep(200 * time.Millisecond)
	if up.calls.Load() != 2 {
		t.Fatalf("a second request of m1 reached the actuator while the first was in flight: %d calls", up.calls.Load())
	}
	close(up.hold)
	wg.Wait()
	<-done
	<-second
	if up.calls.Load() != 3 {
		t.Errorf("calls after release = %d, want 3", up.calls.Load())
	}
}

func toObjects(devices []*solasv1alpha1.Device) []client.Object {
	out := make([]client.Object, len(devices))
	for i, d := range devices {
		out[i] = d
	}
	return out
}
