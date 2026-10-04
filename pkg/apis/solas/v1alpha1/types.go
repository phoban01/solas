package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Device is a leasable device. It is global: every member cluster sees
// the same devices.
type Device struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceSpec   `json:"spec,omitempty"`
	Status DeviceStatus `json:"status,omitempty"`
}

// DeviceSpec describes a device.
type DeviceSpec struct {
	// Description holds free text about the device.
	// +optional
	Description string `json:"description,omitempty"`
	// Attributes holds free-form metadata, for example vendor and model.
	// +optional
	Attributes map[string]string `json:"attributes,omitempty"`
	// Parameters holds any JSON that the owner of the device needs. The
	// server stores it as written and never reads inside it, spec 5.1.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Parameters *runtime.RawExtension `json:"parameters,omitempty"`
	// Preemptible lets a claim of higher priority take the device.
	// +optional
	Preemptible bool `json:"preemptible,omitempty"`
	// PreemptionGracePeriodSeconds is how long a holder may keep the device
	// after a preemption request. The default is 30.
	// +optional
	PreemptionGracePeriodSeconds *int32 `json:"preemptionGracePeriodSeconds,omitempty"`
	// ReclaimPolicy decides when a sweeper may clear the claimRef of a
	// member that is gone: Delete, Delay, or Retain. The default is Delete,
	// spec 8.5.
	// +optional
	ReclaimPolicy ReclaimPolicy `json:"reclaimPolicy,omitempty"`
	// ReclaimDelaySeconds is the reclaim time R of the Delay policy.
	// +optional
	ReclaimDelaySeconds *int32 `json:"reclaimDelaySeconds,omitempty"`
}

// DeviceStatus shows which claim holds the device.
type DeviceStatus struct {
	// ClaimRef names the holder. A device with no claimRef is free.
	// +optional
	ClaimRef *ClaimRef `json:"claimRef,omitempty"`
	// FencingToken is the fencing token of the last bind. The server sets it
	// on each bind and ignores the value a client sends.
	// +optional
	FencingToken int64 `json:"fencingToken,omitempty"`
	// Preemption is a request of a claim of higher priority.
	// +optional
	Preemption *PreemptionRequest `json:"preemption,omitempty"`
	// Offer names the claim that the holder hands the device to. Only that
	// claim may bind against it, spec 14.
	// +optional
	Offer *ClaimRef `json:"offer,omitempty"`
	// Conditions hold the health of the device. The party that runs the
	// device sets them.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// LastRelease records the last clear of claimRef: who did it, when,
	// and which claim held the device, spec 8.5. The server sets it.
	// +optional
	LastRelease *Release `json:"lastRelease,omitempty"`
}

// PreemptionRequest asks the holder to give up the device.
type PreemptionRequest struct {
	// Claim names the claim that asks, as a claimRef does.
	Claim ClaimRef `json:"claim"`
	// RequestedAt is when the claim asked, by its own clock. For display.
	// +optional
	RequestedAt *metav1.Time `json:"requestedAt,omitempty"`
}

// ClaimRef names the claim that holds a device.
type ClaimRef struct {
	// Member is the name of the member cluster of the claim.
	Member string `json:"member"`
	// MemberUID is the UID of the Member when it wrote the ref.
	MemberUID types.UID `json:"memberUID"`
	// Namespace is the namespace of the claim.
	Namespace string `json:"namespace"`
	// Name is the name of the claim.
	Name string `json:"name"`
	// UID is the UID of the claim.
	UID types.UID `json:"uid"`
	// Priority is the priority of the claim.
	// +optional
	Priority int32 `json:"priority,omitempty"`
	// BoundAt is when the bind happened, by the binder's clock. For
	// display only.
	// +optional
	BoundAt *metav1.Time `json:"boundAt,omitempty"`
	// Protected means that the device can only be released, not
	// preempted, spec 10.10.
	// +optional
	Protected bool `json:"protected,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DeviceList is a list of devices.
type DeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Device `json:"items"`
}

// MemberPhase is the phase of a member cluster.
type MemberPhase string

// Member phases.
const (
	MemberActive   MemberPhase = "Active"
	MemberDraining MemberPhase = "Draining"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Member is one member cluster and its lease. Its name is the cluster ID.
type Member struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MemberSpec   `json:"spec,omitempty"`
	Status MemberStatus `json:"status,omitempty"`
}

// MemberSpec holds the lease settings of a member.
type MemberSpec struct {
	// LeaseDurationSeconds is the lease duration D. The default is 30.
	// +optional
	LeaseDurationSeconds int32 `json:"leaseDurationSeconds,omitempty"`
}

// MemberStatus holds the state of the lease.
type MemberStatus struct {
	// RenewTime is the time of the last renew, by the holder's clock.
	// +optional
	RenewTime *metav1.MicroTime `json:"renewTime,omitempty"`
	// Phase is Active or Draining.
	// +optional
	Phase MemberPhase `json:"phase,omitempty"`
	// MinFormat is the lowest format that the member supports, spec 11.4.
	// A member with no range supports format 1 only.
	// +optional
	MinFormat int32 `json:"minFormat,omitempty"`
	// MaxFormat is the highest format that the member supports.
	// +optional
	MaxFormat int32 `json:"maxFormat,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemberList is a list of members.
type MemberList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Member `json:"items"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StoreFormat holds the finalized format of the store, spec 11.1. The API
// does not serve it: solas finalize writes it at the storage level, and
// solas migrate copies it.
type StoreFormat struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Finalized is the finalized format. Zero reads as 1.
	// +optional
	Finalized int32 `json:"finalized,omitempty"`
	// Epoch is the epoch of the store, spec 13.1. A restore moves it up.
	// +optional
	Epoch int64 `json:"epoch,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StoreFormatList is a list of store formats. A store holds at most one.
type StoreFormatList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []StoreFormat `json:"items"`
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
	By string `json:"by"`
	// At is the time of the clear, by the clock of the API server.
	At metav1.Time `json:"at"`
	// Claim is the claimRef that the clear removed.
	Claim ClaimRef `json:"claim"`
}

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemberPolicy limits one member, spec 15.1. Its name is the cluster ID of
// the member. An operator writes it.
type MemberPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec MemberPolicySpec `json:"spec,omitempty"`
}

// MemberPolicySpec holds the limits of a member. A limit that is not set
// is no limit.
type MemberPolicySpec struct {
	// MaxDevices is the most devices that the member may hold.
	// +optional
	MaxDevices *int32 `json:"maxDevices,omitempty"`
	// BindsPerMinute is the rate of binds of the member, spec 15.2.
	// +optional
	BindsPerMinute *int32 `json:"bindsPerMinute,omitempty"`
	// PreemptionsPerMinute is the rate of preemption requests of the member.
	// +optional
	PreemptionsPerMinute *int32 `json:"preemptionsPerMinute,omitempty"`
	// AllowProtected lets the member mark its claims as protected, so
	// their devices cannot be preempted, spec 10.10.
	// +optional
	AllowProtected bool `json:"allowProtected,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemberPolicyList is a list of member policies.
type MemberPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MemberPolicy `json:"items"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemberUsage is the usage set of a limited member, spec 15.2: the devices
// that it holds or is binding. The API does not serve it; the server
// writes it at the storage level. Its name is the member name.
type MemberUsage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +listType=atomic
	Entries []UsageEntry `json:"entries,omitempty"`
	// Buckets are the token buckets of the rates, spec 15.2.
	// +listType=atomic
	Buckets []Bucket `json:"buckets,omitempty"`
}

// UsageEntry is one device in a usage set.
type UsageEntry struct {
	Device string `json:"device"`
	// Added is when the server added the entry, by its own clock.
	Added metav1.Time `json:"added"`
}

// Bucket is a token bucket of one rate: binds or preemptions.
type Bucket struct {
	Kind string `json:"kind"`
	// Tokens is the number of tokens left, in thousandths.
	Tokens int64 `json:"tokens"`
	// Last is when the bucket was last filled, by the server clock.
	Last metav1.Time `json:"last"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemberUsageList is a list of usage sets.
type MemberUsageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MemberUsage `json:"items"`
}
