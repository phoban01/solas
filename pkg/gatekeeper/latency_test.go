package gatekeeper

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// TestGatekeeperLatency measures what the gatekeeper adds to a call in
// process: the proxy, the header checks, the lookup, the device read,
// and the lock, with a fake store that answers at once. The store round
// trip of the consistent read comes on top of it in a cluster; the
// scale rows of docs/benchmarks.md give that. Run with -v for the row.
func TestGatekeeperLatency(t *testing.T) {
	up := &upstream{}
	direct := httptest.NewServer(up)
	t.Cleanup(direct.Close)
	gs := newGatekeeper(t, up, device("gpu-1", "m1", holder, 2))

	measure := func(url string, withHeaders bool) (p50, p99 time.Duration) {
		const n = 2000
		ds := make([]time.Duration, 0, n)
		for range n {
			req, _ := http.NewRequest(http.MethodPost, url+"/machines/m1/deploy", http.NoBody)
			if withHeaders {
				req.Header.Set(ClaimHeader, "a/work/job")
				req.Header.Set(TokenHeader, "2")
			}
			start := time.Now()
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			ds = append(ds, time.Since(start))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[n/2], ds[n*99/100]
	}
	d50, d99 := measure(direct.URL, false)
	g50, g99 := measure(gs.URL, true)
	t.Logf("direct to the actuator: p50 %v, p99 %v", d50, d99)
	t.Logf("through the gatekeeper: p50 %v, p99 %v", g50, g99)
	t.Logf("the gatekeeper adds: p50 %v, p99 %v, with an in-process store", g50-d50, g99-d99)
}
