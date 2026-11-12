package claim

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics/testutil"

	"github.com/phoban01/solas/internal/fakekube"
	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	ctrlmetrics "github.com/phoban01/solas/pkg/controller/metrics"
)

// TestMetrics checks that a bind counts.
func TestMetrics(t *testing.T) {
	c1 := claim("c1", "u1")
	c1.Spec.Selector = &claimsv1alpha1.DeviceSelector{LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"kind": "gpu"}}}
	k := fakekube.NewClient(device("gpu-1", map[string]string{"kind": "gpu"}, nil), c1)
	before, _ := testutil.GetCounterMetricValue(ctrlmetrics.Binds)
	settle(t, newReconciler(k, live()), c1)
	if got := getClaim(t, k, "c1"); got.Status.Phase != claimsv1alpha1.ClaimBound {
		t.Fatalf("claim phase = %s, want Bound", got.Status.Phase)
	}
	if v, _ := testutil.GetCounterMetricValue(ctrlmetrics.Binds); v != before+1 {
		t.Errorf("solas_claims_binds_total = %v, want %v", v, before+1)
	}
}
