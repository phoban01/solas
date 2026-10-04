package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// Preemptible lets a claim of higher priority take the device.
	// +optional
	Preemptible bool `json:"preemptible,omitempty"`
	// PreemptionGracePeriodSeconds is how long a holder may keep the device
	// after a preemption request. The default is 30.
	// +optional
	PreemptionGracePeriodSeconds *int32 `json:"preemptionGracePeriodSeconds,omitempty"`
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
	// Conditions hold the health of the device. The party that runs the
	// device sets them.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
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
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StoreFormatList is a list of store formats. A store holds at most one.
type StoreFormatList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []StoreFormat `json:"items"`
}
