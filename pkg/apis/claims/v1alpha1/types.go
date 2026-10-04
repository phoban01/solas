package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

//= spec/solas.md#6-1-resource
//# `DeviceClaim.status.phase` MUST be one of `Pending`, `Bound`,
//# `Suspended`, or `Lost`.

// ClaimPhase is the phase of a claim, spec 6.5.
type ClaimPhase string

// Claim phases.
const (
	ClaimPending   ClaimPhase = "Pending"
	ClaimBound     ClaimPhase = "Bound"
	ClaimSuspended ClaimPhase = "Suspended"
	ClaimLost      ClaimPhase = "Lost"
	// Preempting: a claim of higher priority asked for the device. The claim
	// is still in effect until the grace period ends, spec 10.6.
	ClaimPreempting ClaimPhase = "Preempting"
	// Preempted: the grace period ended and the claim gives up the device.
	ClaimPreempted ClaimPhase = "Preempted"
)

// ReleaseFinalizer keeps a claim until its device is released, spec 6.3.
const ReleaseFinalizer = "solas.dev/release"

//= spec/solas.md#6-1-resource
//# `DeviceClaim` MUST be a namespaced CRD in the group `claims.solas.dev`,
//# version `v1alpha1`.

//= spec/solas.md#6-1-resource
//# Each member cluster MUST store its claims in its own etcd.

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DeviceClaim asks for one device. It lives in the etcd of its cluster.
type DeviceClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceClaimSpec   `json:"spec,omitempty"`
	Status DeviceClaimStatus `json:"status,omitempty"`
}

// DeviceClaimSpec says which devices the claim accepts.
type DeviceClaimSpec struct {
	//= spec/solas.md#6-1-resource
	//# `DeviceClaim.spec.selector` MUST be a label selector over `Device`
	//# labels.

	// Selector picks devices by label and by a CEL expression. An empty
	// selector matches every device.
	// +optional
	Selector *DeviceSelector `json:"selector,omitempty"`
	// Priority says how urgent the claim is. A higher number wins, and a
	// claim may preempt a preemptible device held at a lower priority.
	// +optional
	Priority int32 `json:"priority,omitempty"`
	// Protected asks that the device can only be released, not preempted.
	// The member needs allowProtected in its MemberPolicy, spec 10.10.
	// +optional
	Protected bool `json:"protected,omitempty"`
}

// DeviceSelector matches devices. Both parts must match, spec 10.3.
type DeviceSelector struct {
	metav1.LabelSelector `json:",inline"`
	// CEL is an expression over the variable device, which holds the whole
	// Device object. For example:
	//   device.spec.attributes.model == "A100"
	// +optional
	CEL string `json:"cel,omitempty"`
}

// DeviceClaimStatus shows the binding of the claim.
type DeviceClaimStatus struct {
	// Phase is Pending, Bound, Preempting, Preempted, Suspended, or Lost.
	// +optional
	Phase ClaimPhase `json:"phase,omitempty"`
	// DeviceName names the bound device.
	// +optional
	DeviceName string `json:"deviceName,omitempty"`
	// MemberUID is the UID of the Member that bound the claim.
	// +optional
	MemberUID types.UID `json:"memberUID,omitempty"`
	// FencingToken is the token of the bind. A workload presents it each
	// time it uses the device, spec 6.6.
	// +optional
	FencingToken int64 `json:"fencingToken,omitempty"`
	// LeaseExpiresAt is when the lease of the holding member ends, by that
	// member's clock. For display only, spec 10.4.
	// +optional
	LeaseExpiresAt *metav1.Time `json:"leaseExpiresAt,omitempty"`
	// PreemptionSeenAt is when the controller first saw a preemption
	// request on the device, by its clock. The grace period runs from here.
	// +optional
	PreemptionSeenAt *metav1.Time `json:"preemptionSeenAt,omitempty"`
	// PreemptionTarget names the device that this claim asked to preempt.
	// +optional
	PreemptionTarget string `json:"preemptionTarget,omitempty"`
	// Conditions give details of the phase.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DeviceClaimList is a list of claims.
type DeviceClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []DeviceClaim `json:"items"`
}
