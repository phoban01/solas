package controller

import (
	"testing"
	"time"
)

func TestOptionsValidate(t *testing.T) {
	good := NewOptions()
	good.ClusterID = "cluster-a"
	if err := good.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	for name, change := range map[string]func(*Options){
		"no cluster id":    func(o *Options) { o.ClusterID = "" },
		"bad cluster id":   func(o *Options) { o.ClusterID = "Cluster_A" },
		"margin too small": func(o *Options) { o.LeaseMargin = time.Millisecond },
		"margin too large": func(o *Options) { o.LeaseMargin = o.LeaseDuration },
		"sweep above D":    func(o *Options) { o.SweepInterval = 2 * o.LeaseDuration },
	} {
		o := *good
		change(&o)
		if o.Validate() == nil {
			t.Errorf("%s: Validate accepted it", name)
		}
	}
}

func TestClaimWorkersDefaultAndBound(t *testing.T) {
	o := NewOptions()
	if o.ClaimWorkers != 4 {
		t.Errorf("default claim workers = %d, want 4", o.ClaimWorkers)
	}
	o.ClusterID = "a"
	o.ClaimWorkers = 0
	if err := o.Validate(); err == nil {
		t.Error("Validate accepted 0 claim workers")
	}
}
