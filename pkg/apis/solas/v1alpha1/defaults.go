package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
)

// DefaultLeaseDurationSeconds is the default lease duration D, spec 7.3.
const DefaultLeaseDurationSeconds = 30

func addDefaultingFuncs(scheme *runtime.Scheme) error {
	return RegisterDefaults(scheme)
}

// SetDefaults_MemberSpec sets the lease duration when it is not set.
func SetDefaults_MemberSpec(obj *MemberSpec) {
	if obj.LeaseDurationSeconds == 0 {
		obj.LeaseDurationSeconds = DefaultLeaseDurationSeconds
	}
}

// DefaultPreemptionGracePeriodSeconds is the default grace period, spec 10.1.
const DefaultPreemptionGracePeriodSeconds = 30

//= spec/solas.md#10-1-device-metadata
//# `Device.spec.preemptionGracePeriodSeconds` MUST be 30 unless it is set.

// SetDefaults_DeviceSpec sets the grace period when it is not set.
func SetDefaults_DeviceSpec(obj *DeviceSpec) {
	//= spec/solas.md#8-5-reclaim-policy
	//# The default reclaim policy is `Delete`.
	if obj.ReclaimPolicy == "" {
		obj.ReclaimPolicy = ReclaimDelete
	}
	if obj.PreemptionGracePeriodSeconds == nil {
		g := int32(DefaultPreemptionGracePeriodSeconds)
		obj.PreemptionGracePeriodSeconds = &g
	}
}
