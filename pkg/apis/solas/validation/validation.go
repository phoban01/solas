// Package validation checks Device and Member objects.
package validation

import (
	"fmt"

	genericvalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/phoban01/solas/pkg/apis/solas"
)

var (
	metaPath   = field.NewPath("metadata")
	specPath   = field.NewPath("spec")
	statusPath = field.NewPath("status")
)

// ValidateDevice checks a new device.
func ValidateDevice(d *solas.Device) field.ErrorList {
	errs := genericvalidation.ValidateObjectMeta(&d.ObjectMeta, false, genericvalidation.NameIsDNSSubdomain, metaPath)
	errs = append(errs, validateReclaim(&d.Spec, field.NewPath("spec"))...)
	errs = append(errs, validateParameters(d.Spec.Parameters, field.NewPath("spec", "parameters"))...)
	return append(errs, validateClaimRef(d.Status.ClaimRef, statusPath.Child("claimRef"))...)
}

// ValidateDeviceUpdate checks an update of a device.
func ValidateDeviceUpdate(d, old *solas.Device) field.ErrorList {
	errs := genericvalidation.ValidateObjectMetaUpdate(&d.ObjectMeta, &old.ObjectMeta, metaPath)
	return append(errs, ValidateDevice(d)...)
}

// ValidateDeviceStatusUpdate checks an update of the status of a device.
func ValidateDeviceStatusUpdate(d, old *solas.Device) field.ErrorList {
	return ValidateDeviceStatusUpdateWith(d, old, false)
}

// ValidateDeviceStatusUpdateWith checks an update of the status of a device.
// With recoveryChecked, it accepts a recovery, spec 14.5: the caller has
// checked that the old member UID is gone.
func ValidateDeviceStatusUpdateWith(d, old *solas.Device, recoveryChecked bool) field.ErrorList {
	errs := genericvalidation.ValidateObjectMetaUpdate(&d.ObjectMeta, &old.ObjectMeta, metaPath)
	path := statusPath.Child("claimRef")
	errs = append(errs, validateClaimRef(d.Status.ClaimRef, path)...)
	//= spec/solas.md#5-3-status-updates
	//# The server MUST reject a status update that changes `claimRef` from one
	//# holder to a different holder.

	//= spec/solas.md#5-3-status-updates
	//# A holder is different when any field of `claimRef` differs.

	//= spec/solas.md#5-3-status-updates
	//# To move a device, a client MUST first clear `claimRef` and then set it
	//# in a second update.

	//= spec/solas.md#14-2-bind-against-the-offer
	//# Only the claim that the offer names, with the member UID that it names,
	//# MAY bind against the offer.
	if old.Status.ClaimRef != nil && d.Status.ClaimRef != nil && !SameClaim(old.Status.ClaimRef, d.Status.ClaimRef) &&
		!IsTransferBind(d, old) && !(recoveryChecked && IsRecovery(d, old)) {
		errs = append(errs, field.Forbidden(path,
			"cannot change the holder of a bound device; clear claimRef first, then set it"))
	}
	errs = append(errs, validateOffer(d, old)...)
	return append(errs, validatePreemption(d, old)...)
}

//= spec/solas.md#5-3-status-updates
//# The server MUST reject a status update that sets a `claimRef` with an
//# empty field.

//= spec/solas.md#5-2-claim-reference
//# A `claimRef` MUST hold the member name, the member UID, the claim
//# namespace, the claim name, and the claim UID.

func validateClaimRef(ref *solas.ClaimRef, path *field.Path) field.ErrorList {
	if ref == nil {
		return nil
	}
	var errs field.ErrorList
	for name, value := range map[string]string{
		"member":    ref.Member,
		"memberUID": string(ref.MemberUID),
		"namespace": ref.Namespace,
		"name":      ref.Name,
		"uid":       string(ref.UID),
	} {
		if value == "" {
			errs = append(errs, field.Required(path.Child(name), ""))
		}
	}
	return errs
}

// ValidateMember checks a new member.
func ValidateMember(m *solas.Member) field.ErrorList {
	errs := genericvalidation.ValidateObjectMeta(&m.ObjectMeta, false, genericvalidation.NameIsDNSSubdomain, metaPath)
	//= spec/solas.md#7-1-resource
	//# `Member.spec.leaseDurationSeconds` MUST hold the lease duration `D`.
	if m.Spec.LeaseDurationSeconds <= 0 {
		errs = append(errs, field.Invalid(specPath.Child("leaseDurationSeconds"), m.Spec.LeaseDurationSeconds, "must be greater than 0"))
	}
	return append(errs, validateMemberStatus(m)...)
}

// ValidateMemberUpdate checks an update of a member.
func ValidateMemberUpdate(m, old *solas.Member) field.ErrorList {
	errs := genericvalidation.ValidateObjectMetaUpdate(&m.ObjectMeta, &old.ObjectMeta, metaPath)
	return append(errs, ValidateMember(m)...)
}

// ValidateMemberStatusUpdate checks an update of the status of a member.
func ValidateMemberStatusUpdate(m, old *solas.Member) field.ErrorList {
	errs := genericvalidation.ValidateObjectMetaUpdate(&m.ObjectMeta, &old.ObjectMeta, metaPath)
	return append(errs, validateMemberStatus(m)...)
}

func validateMemberStatus(m *solas.Member) field.ErrorList {
	//= spec/solas.md#7-1-resource
	//# `Member.status.phase` MUST be `Active` or `Draining`.
	switch m.Status.Phase {
	case solas.MemberActive, solas.MemberDraining:
		return nil
	}
	return field.ErrorList{field.NotSupported(statusPath.Child("phase"), m.Status.Phase,
		[]string{string(solas.MemberActive), string(solas.MemberDraining)})}
}

// SameClaim reports whether two refs name the same claim at the same
// priority. The bind time is for display and does not count.
func SameClaim(a, b *solas.ClaimRef) bool {
	return a.Member == b.Member && a.MemberUID == b.MemberUID && a.Namespace == b.Namespace &&
		a.Name == b.Name && a.UID == b.UID && a.Priority == b.Priority
}

// validatePreemption checks the preemption rules of spec 10.5 and 10.7.
func validatePreemption(d, old *solas.Device) field.ErrorList {
	path := statusPath.Child("preemption")
	var errs field.ErrorList
	req, oldReq := d.Status.Preemption, old.Status.Preemption
	if req != nil && (oldReq == nil || !SameClaim(&req.Claim, &oldReq.Claim)) {
		errs = append(errs, validateClaimRef(&req.Claim, path.Child("claim"))...)
		switch {
		case !d.Spec.Preemptible:
			errs = append(errs, field.Forbidden(path, "the device is not preemptible"))
		case old.Status.ClaimRef == nil:
			errs = append(errs, field.Forbidden(path, "the device is free; bind it instead"))
		//= spec/solas.md#14-1-offer
		//# The server MUST reject a preemption request while an offer stands.
		case old.Status.Offer != nil:
			errs = append(errs, field.Forbidden(path, "the holder offers the device to another claim"))
		//= spec/solas.md#10-10-protected-holders
		//# The server MUST reject a preemption request on a device whose holder is
		//# protected.
		case old.Status.ClaimRef.Protected:
			errs = append(errs, field.Forbidden(path, "the holder is protected; it can only release the device"))
		//= spec/solas.md#10-5-preemption-request
		//# The server MUST reject a request whose priority is not greater than the
		//# priority of the holder.
		case req.Claim.Priority <= old.Status.ClaimRef.Priority:
			errs = append(errs, field.Forbidden(path.Child("claim", "priority"),
				fmt.Sprintf("must be greater than the priority %d of the holder", old.Status.ClaimRef.Priority)))
		//= spec/solas.md#10-5-preemption-request
		//# The server MUST reject a request that replaces a request of equal or
		//# greater priority.
		case oldReq != nil && req.Claim.Priority <= oldReq.Claim.Priority:
			errs = append(errs, field.Forbidden(path.Child("claim", "priority"),
				fmt.Sprintf("must be greater than the priority %d of the current request", oldReq.Claim.Priority)))
		}
	}
	//= spec/solas.md#10-7-bind-by-the-preemptor
	//# While a device has a request, the server MUST reject a bind that does not
	//# name the requesting claim.
	if oldReq != nil && old.Status.ClaimRef == nil && d.Status.ClaimRef != nil && !SameIdentity(d.Status.ClaimRef, &oldReq.Claim) {
		errs = append(errs, field.Forbidden(statusPath.Child("claimRef"),
			fmt.Sprintf("the device is kept for claim %s/%s of member %s", oldReq.Claim.Namespace, oldReq.Claim.Name, oldReq.Claim.Member)))
	}
	return errs
}

// SameIdentity reports whether two refs name the same claim.
func SameIdentity(a, b *solas.ClaimRef) bool {
	return a.Member == b.Member && a.MemberUID == b.MemberUID && a.Namespace == b.Namespace &&
		a.Name == b.Name && a.UID == b.UID
}

//= spec/solas.md#8-5-reclaim-policy
//# A `Device` MAY set a reclaim policy in `spec.reclaimPolicy`: `Delete`,
//# `Delay`, or `Retain`, ADR 0016.

//= spec/solas.md#8-5-reclaim-policy
//# `R` is `spec.reclaimDelaySeconds` of the device.

// validateReclaim checks the reclaim policy and its delay. An empty policy
// is Delete; defaulting sets it on the way in.
func validateReclaim(spec *solas.DeviceSpec, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	switch spec.ReclaimPolicy {
	case "", solas.ReclaimDelete, solas.ReclaimRetain:
		if spec.ReclaimDelaySeconds != nil {
			errs = append(errs, field.Forbidden(path.Child("reclaimDelaySeconds"), "only the Delay policy has a reclaim time"))
		}
	case solas.ReclaimDelay:
		if spec.ReclaimDelaySeconds == nil {
			errs = append(errs, field.Required(path.Child("reclaimDelaySeconds"), "the Delay policy needs a reclaim time"))
		} else if *spec.ReclaimDelaySeconds < 0 {
			errs = append(errs, field.Invalid(path.Child("reclaimDelaySeconds"), *spec.ReclaimDelaySeconds, "must not be negative"))
		}
	default:
		errs = append(errs, field.NotSupported(path.Child("reclaimPolicy"), spec.ReclaimPolicy,
			[]solas.ReclaimPolicy{solas.ReclaimDelete, solas.ReclaimDelay, solas.ReclaimRetain}))
	}
	return errs
}

//= spec/solas.md#15-1-policy
//# A `MemberPolicy` MUST have the name of the member that it limits.

// ValidateMemberPolicy checks a member policy. Its name is a member name,
// and no limit is negative.
func ValidateMemberPolicy(p *solas.MemberPolicy) field.ErrorList {
	errs := genericvalidation.ValidateObjectMeta(&p.ObjectMeta, false, genericvalidation.NameIsDNSSubdomain, metaPath)
	spec := field.NewPath("spec")
	for name, v := range map[string]*int32{
		"maxDevices": p.Spec.MaxDevices, "bindsPerMinute": p.Spec.BindsPerMinute, "preemptionsPerMinute": p.Spec.PreemptionsPerMinute,
	} {
		if v != nil && *v < 0 {
			errs = append(errs, field.Invalid(spec.Child(name), *v, "must not be negative"))
		}
	}
	return errs
}

// MaxParameters is the largest spec.parameters in bytes. A DynamoDB item
// holds at most 400 KB, and an event item holds the object and the
// previous object.
const MaxParameters = 64 * 1024

//= spec/solas.md#5-1-resource
//# The server MUST store `spec.parameters` as written, and MUST reject it
//# when it is larger than 64 KiB.

func validateParameters(p *runtime.RawExtension, path *field.Path) field.ErrorList {
	if p != nil && len(p.Raw) > MaxParameters {
		return field.ErrorList{field.TooLong(path, len(p.Raw), MaxParameters)}
	}
	return nil
}

// IsTransferBind reports whether an update from old to d is a bind
// against the offer of old: claimRef moves from the holder to exactly the
// claim and member UID that the offer names, spec 14.2.
func IsTransferBind(d, old *solas.Device) bool {
	return old.Status.ClaimRef != nil && old.Status.Offer != nil && d.Status.ClaimRef != nil &&
		SameIdentity(d.Status.ClaimRef, old.Status.Offer)
}

// validateOffer checks the offer rules of spec 14.1.
func validateOffer(d, old *solas.Device) field.ErrorList {
	path := statusPath.Child("offer")
	offer, oldOffer := d.Status.Offer, old.Status.Offer
	if offer == nil || (oldOffer != nil && SameClaim(offer, oldOffer)) {
		return nil
	}
	errs := validateClaimRef(offer, path)
	switch {
	//= spec/solas.md#14-1-offer
	//# The server MUST reject an offer on a free device, and a second offer
	//# while one stands.
	case old.Status.ClaimRef == nil:
		errs = append(errs, field.Forbidden(path, "the device is free; bind it instead"))
	case oldOffer != nil:
		errs = append(errs, field.Forbidden(path, fmt.Sprintf("the device is offered to claim %s/%s of member %s; withdraw that offer first",
			oldOffer.Namespace, oldOffer.Name, oldOffer.Member)))
	//= spec/solas.md#14-1-offer
	//# The server MUST reject an offer while a preemption request stands.
	case old.Status.Preemption != nil:
		errs = append(errs, field.Forbidden(path, "a preemption request stands"))
	case SameIdentity(offer, old.Status.ClaimRef):
		errs = append(errs, field.Forbidden(path, "the offer names the holder"))
	}
	return errs
}

// IsRecovery reports whether an update from old to d has the shape of a
// recovery: claimRef keeps the member name and the claim, and moves to
// another member UID, spec 14.5. The status strategy also checks that the
// old member UID is gone.
func IsRecovery(d, old *solas.Device) bool {
	o, n := old.Status.ClaimRef, d.Status.ClaimRef
	return o != nil && n != nil && o.Member == n.Member && o.Namespace == n.Namespace &&
		o.Name == n.Name && o.UID == n.UID && o.MemberUID != n.MemberUID
}
