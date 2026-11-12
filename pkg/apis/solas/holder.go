package solas

import "k8s.io/apimachinery/pkg/runtime"

//= spec/solas.md#5-3-status-updates
//# A kind with a holder MUST carry `claimRef`, `fencingToken`, `offer`, and
//# `preemption` in its status.

// HolderStatus is the holder part of the status of a kind with a holder:
// the fields that the rules of spec 5.3, 6.6, and 14 read and write. A
// kind with a holder keeps these fields in its status and gives them to
// the holder primitive through Held.
type HolderStatus struct {
	// ClaimRef names the holder. An object with no ClaimRef is free.
	ClaimRef *ClaimRef
	// FencingToken is the fencing token of the last bind, spec 5.3.
	FencingToken int64
	// Offer names the claim that the holder hands the object to, spec 14.1.
	Offer *ClaimRef
	// Preemption is a request of a claim of higher priority, spec 10.5.
	Preemption *PreemptionRequest
}

//= spec/solas.md#5-3-status-updates
//# The rules of this section, of section 6.6, and of section 14 MUST hold
//# for every object of a kind with a holder.

// Held is an object of a kind with a holder. The holder primitive,
// pkg/holder, reads the holder status of any Held object and writes it
// back, so the rules of a holder are the same for every kind.
type Held interface {
	runtime.Object
	// HolderStatus returns the holder fields of the status.
	HolderStatus() HolderStatus
	// SetHolderStatus stores the holder fields in the status.
	SetHolderStatus(HolderStatus)
}

//= spec/solas.md#5-3-status-updates
//# `Device` is the first kind with a holder.

// HolderStatus returns the holder fields of the device.
func (d *Device) HolderStatus() HolderStatus {
	return HolderStatus{ClaimRef: d.Status.ClaimRef, FencingToken: d.Status.FencingToken,
		Offer: d.Status.Offer, Preemption: d.Status.Preemption}
}

// SetHolderStatus stores the holder fields in the device.
func (d *Device) SetHolderStatus(h HolderStatus) {
	d.Status.ClaimRef, d.Status.FencingToken, d.Status.Offer, d.Status.Preemption = h.ClaimRef, h.FencingToken, h.Offer, h.Preemption
}

var _ Held = &Device{}
