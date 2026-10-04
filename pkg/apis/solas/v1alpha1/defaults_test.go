package v1alpha1

import "testing"

func TestDefaultReclaimPolicy(t *testing.T) {
	var s DeviceSpec
	SetDefaults_DeviceSpec(&s)
	if s.ReclaimPolicy != ReclaimDelete {
		t.Errorf("default reclaim policy = %q, want Delete", s.ReclaimPolicy)
	}
	s = DeviceSpec{ReclaimPolicy: ReclaimRetain}
	SetDefaults_DeviceSpec(&s)
	if s.ReclaimPolicy != ReclaimRetain {
		t.Errorf("defaulting changed a set policy to %q", s.ReclaimPolicy)
	}
}
