package claim

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	"github.com/phoban01/solas/internal/fakekube"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

// drain returns the Events the fake recorder holds.
func drain(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// want fails unless one Event starts with prefix and names the device and
// the token.
func want(t *testing.T, got []string, prefix, device, token string) {
	t.Helper()
	for _, e := range got {
		if strings.HasPrefix(e, prefix) && strings.Contains(e, device) && strings.Contains(e, "fencing token "+token) {
			return
		}
	}
	t.Errorf("no Event %q naming %s and token %s in %q", prefix, device, token, got)
}

// TestEvents checks that a bind, a release, and a loss each record an
// Event on the claim that names the device and the fencing token.
func TestEvents(t *testing.T) {
	ctx := context.Background()

	c1 := claim("c1", "u1")
	k := fakekube.NewClient(device("d1", nil, nil), c1)
	rec := events.NewFakeRecorder(20)
	r := newReconciler(k, live())
	r.Events = rec
	settle(t, r, c1)
	want(t, drain(rec), "Normal Bound", "d1", "1")
	if err := k.Delete(ctx, getClaim(t, k, "c1")); err != nil {
		t.Fatal(err)
	}
	settle(t, r, c1)
	want(t, drain(rec), "Normal Released", "d1", "1")

	c2 := claim("c2", "u2")
	k2 := fakekube.NewClient(device("d2", nil, nil), c2,
		&solasv1alpha1.Member{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", UID: muid}})
	m := live()
	r2 := newReconciler(k2, m)
	r2.Events = rec
	settle(t, r2, c2)
	drain(rec)
	m.st.UID = "member-uid-2"
	settle(t, r2, c2)
	want(t, drain(rec), "Warning Lost", "d2", "1")
}
