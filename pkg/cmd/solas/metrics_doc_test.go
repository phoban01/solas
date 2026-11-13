package solas

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestMetricsDoc starts solas and checks that /metrics lists each solas_
// metric of docs/observability.md, and that the page names each solas_
// metric that /metrics lists, so the page and the code do not drift.
func TestMetricsDoc(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/observability.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile("`(solas_[a-z_]+)`").FindAllStringSubmatch(string(doc), -1) {
		documented[m[1]] = true
	}
	if len(documented) == 0 {
		t.Fatal("docs/observability.md names no solas_ metric")
	}

	_, _, get := startWithoutStore(t, "dynamodb://solas?create-table=true&endpoint=http://127.0.0.1:%d")
	code, body := get("/metrics")
	if code != http.StatusOK {
		t.Fatalf("/metrics = %d: %s", code, body)
	}
	served := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^# HELP (solas_[a-z_]+) `).FindAllStringSubmatch(body, -1) {
		served[m[1]] = true
	}
	for name := range documented {
		if !served[name] {
			t.Errorf("docs/observability.md names %s, but /metrics does not list it", name)
		}
	}
	for name := range served {
		if !documented[name] {
			t.Errorf("/metrics lists %s, but docs/observability.md does not name it", name)
		}
	}
	if t.Failed() {
		var lines []string
		for _, l := range strings.Split(body, "\n") {
			if strings.HasPrefix(l, "# HELP solas_") {
				lines = append(lines, l)
			}
		}
		t.Logf("solas_ metrics of /metrics:\n%s", strings.Join(lines, "\n"))
	}
}
