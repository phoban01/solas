package solas

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Device is a leasable device. It is global: every member cluster sees
// the same devices.
type Device struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   DeviceSpec
	Status DeviceStatus
}

// DeviceSpec describes a device.
type DeviceSpec struct {
	// Description holds free text about the device.
	Description string
	// Attributes holds free-form metadata, spec 10.1.
	Attributes map[string]string
	// Preemptible lets a claim of higher priority take the device, spec 10.5.
	Preemptible bool
	// PreemptionGracePeriodSeconds is how long a holder may keep the device
	// after a preemption request. The default is 30.
	PreemptionGracePeriodSeconds *int32
	// ReclaimPolicy decides when a sweeper may clear the claimRef of a
	// member that is gone, spec 8.5.
	ReclaimPolicy ReclaimPolicy
	// ReclaimDelaySeconds is the reclaim time R of the Delay policy.
	ReclaimDelaySeconds *int32
}

// DeviceStatus shows which claim holds the device.
type DeviceStatus struct {
	// ClaimRef names the holder. A device with no ClaimRef is free.
	ClaimRef *ClaimRef
	// FencingToken is the fencing token of the last bind. The server sets
	// it, spec 5.3.
	FencingToken int64
	// Preemption is a request of a claim of higher priority, spec 10.5.
	Preemption *PreemptionRequest
	// Conditions hold the health of the device, spec 10.1.
	Conditions []metav1.Condition
	// LastRelease records the last clear of claimRef: who did it, when,
	// and which claim held the device, spec 8.5. The server sets it.
	LastRelease *Release
}

// PreemptionRequest asks the holder to give up the device, spec 10.5.
type PreemptionRequest struct {
	// Claim names the claim that asks, as a claimRef does.
	Claim ClaimRef
	// RequestedAt is when the claim asked, by its own clock. For display.
	RequestedAt *metav1.Time
}

// ClaimRef names the claim that holds a device, spec 5.2.
type ClaimRef struct {
	// Member is the name of the member cluster of the claim.
	Member string
	// MemberUID is the UID of the Member when it wrote the ref.
	MemberUID types.UID
	// Namespace is the namespace of the claim.
	Namespace string
	// Name is the name of the claim.
	Name string
	// UID is the UID of the claim.
	UID types.UID
	// Priority is the priority of the claim, spec 10.2.
	Priority int32
	// BoundAt is when the bind happened, by the binder's clock. For
	// display only.
	BoundAt *metav1.Time
	// Protected means that the device can only be released, not
	// preempted, spec 10.10.
	Protected bool
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DeviceList is a list of devices.
type DeviceList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []Device
}

// MemberPhase is the phase of a member cluster.
type MemberPhase string

// Member phases, spec 7.1.
const (
	MemberActive   MemberPhase = "Active"
	MemberDraining MemberPhase = "Draining"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Member is one member cluster and its lease. Its name is the cluster ID.
type Member struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   MemberSpec
	Status MemberStatus
}

// MemberSpec holds the lease settings of a member.
type MemberSpec struct {
	// LeaseDurationSeconds is the lease duration D.
	LeaseDurationSeconds int32
}

// MemberStatus holds the state of the lease.
type MemberStatus struct {
	// RenewTime is the time of the last renew, by the holder's clock.
	// Observers do not compare it with their own clocks, spec 8.1.
	RenewTime *metav1.MicroTime
	// Phase is Active or Draining.
	Phase MemberPhase
	// MinFormat and MaxFormat are the lowest and the highest format that
	// the member supports, spec 11.4. Zero reads as 1.
	MinFormat int32
	MaxFormat int32
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemberList is a list of members.
type MemberList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []Member
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StoreFormat holds the finalized format of the store, spec 11.1. The API
// does not serve it: solas finalize writes it at the storage level, and
// solas migrate copies it.
type StoreFormat struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	// Finalized is the finalized format. Zero reads as 1.
	Finalized int32
	// Epoch is the epoch of the store, spec 13.1. A restore moves it up.
	Epoch int64
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StoreFormatList is a list of store formats. A store holds at most one.
type StoreFormatList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []StoreFormat
}

// ReclaimPolicy is the reclaim policy of a device, spec 8.5.
type ReclaimPolicy string

// The reclaim policies, ADR 0016.
const (
	ReclaimDelete ReclaimPolicy = "Delete"
	ReclaimDelay  ReclaimPolicy = "Delay"
	ReclaimRetain ReclaimPolicy = "Retain"
)

// Release records a clear of the claimRef of a device, spec 8.5.
type Release struct {
	// By is the user that made the request, as the API server saw it.
	By string
	// At is the time of the clear, by the clock of the API server.
	At metav1.Time
	// Claim is the claimRef that the clear removed.
	Claim ClaimRef
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemberPolicy limits one member, spec 15.1. Its name is the cluster ID of
// the member. An operator writes it.
type MemberPolicy struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec MemberPolicySpec
}

// MemberPolicySpec holds the limits of a member. A limit that is not set
// is no limit.
type MemberPolicySpec struct {
	// MaxDevices is the most devices that the member may hold.
	MaxDevices *int32
	// BindsPerMinute and PreemptionsPerMinute are rates, spec 15.2.
	BindsPerMinute       *int32
	PreemptionsPerMinute *int32
	// AllowProtected lets the member mark its claims as protected, so
	// their devices cannot be preempted, spec 10.10.
	AllowProtected bool
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemberPolicyList is a list of member policies.
type MemberPolicyList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []MemberPolicy
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemberUsage is the usage set of a limited member, spec 15.2: the devices
// that it holds or is binding. The API does not serve it; the server
// writes it at the storage level. Its name is the member name.
type MemberUsage struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Entries []UsageEntry
	// Buckets are the token buckets of the rates, spec 15.2.
	Buckets []Bucket
}

// UsageEntry is one device in a usage set.
type UsageEntry struct {
	Device string
	// Added is when the server added the entry, by its own clock.
	Added metav1.Time
}

// Bucket is a token bucket of one rate: binds or preemptions.
type Bucket struct {
	Kind string
	// Tokens is the number of tokens left, in thousandths.
	Tokens int64
	// Last is when the bucket was last filled, by the server clock.
	Last metav1.Time
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemberUsageList is a list of usage sets.
type MemberUsageList struct {
	metav1.TypeMeta
	metav1.ListMeta

	Items []MemberUsage
}
