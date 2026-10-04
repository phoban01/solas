// Package device holds the registry of the Device resource.
package device

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/names"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apis/solas/validation"
	"github.com/phoban01/solas/pkg/format"
)

// strategy handles creates, updates, and deletes of devices.
type strategy struct {
	runtime.ObjectTyper
	names.NameGenerator
}

// PolicyLookup returns the MemberPolicy of a member, or nil when it has
// none, spec 15.1.
type PolicyLookup func(ctx context.Context, member string) (*solas.MemberPolicy, error)

// MemberLookup returns the Member with a name, or nil when there is none.
type MemberLookup func(ctx context.Context, name string) (*solas.Member, error)

// statusStrategy handles updates of the status subresource.
type statusStrategy struct {
	strategy
	// policies finds the MemberPolicy of a member. Nil means that no
	// member has one.
	policies PolicyLookup
	// members finds a Member by name. Nil means that the server rejects
	// every recovery.
	members MemberLookup
}

// NewStrategy returns the strategy for devices.
func NewStrategy(typer runtime.ObjectTyper) strategy {
	return strategy{typer, names.SimpleNameGenerator}
}

// NewStatusStrategy returns the strategy for the status of devices.
func NewStatusStrategy(s strategy) statusStrategy {
	return statusStrategy{strategy: s}
}

//= spec/solas.md#5-1-resource
//# `Device` MUST be a cluster-scoped resource in the group `solas.dev`,
//# version `v1alpha1`.

func (strategy) NamespaceScoped() bool { return false }

// PrepareForCreate drops the status, so a device starts free.
func (strategy) PrepareForCreate(ctx context.Context, obj runtime.Object) {
	obj.(*solas.Device).Status = solas.DeviceStatus{}
}

//= spec/solas.md#5-3-status-updates
//# A spec update MUST NOT change `status`.

func (strategy) PrepareForUpdate(ctx context.Context, obj, old runtime.Object) {
	obj.(*solas.Device).Status = old.(*solas.Device).Status
}

func (strategy) Validate(ctx context.Context, obj runtime.Object) field.ErrorList {
	return validation.ValidateDevice(obj.(*solas.Device))
}

func (strategy) WarningsOnCreate(ctx context.Context, obj runtime.Object) []string { return nil }

func (strategy) AllowCreateOnUpdate(context.Context) bool { return false }

//= spec/solas.md#5-3-status-updates
//# The server MUST NOT accept an unconditional update of `Device` status.

//= spec/solas.md#5-3-status-updates
//# Each status update MUST carry the resource version that the client read.

func (strategy) AllowUnconditionalUpdate(context.Context) bool { return false }

func (strategy) Canonicalize(obj runtime.Object) {}

func (strategy) ValidateUpdate(ctx context.Context, obj, old runtime.Object) field.ErrorList {
	return validation.ValidateDeviceUpdate(obj.(*solas.Device), old.(*solas.Device))
}

func (strategy) WarningsOnUpdate(ctx context.Context, obj, old runtime.Object) []string { return nil }

// GetResetFields tells server-side apply that the main resource does not
// own the status.
func (strategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"solas.dev/v1alpha1": fieldpath.NewSet(fieldpath.MakePathOrDie("status")),
	}
}

//= spec/solas.md#5-3-status-updates
//# A status update MUST NOT change `spec`.

func (statusStrategy) PrepareForUpdate(ctx context.Context, obj, old runtime.Object) {
	d, o := obj.(*solas.Device), old.(*solas.Device)
	d.Spec = o.Spec
	// Labels and annotations belong to the main resource.
	d.Labels, d.Annotations = o.Labels, o.Annotations
	d.Status.FencingToken = nextToken(o, d)
	d.Status.LastRelease = lastRelease(ctx, o, d)
	//= spec/solas.md#10-7-bind-by-the-preemptor
	//# The bind of the requesting claim MUST clear `status.preemption`.
	if req := o.Status.Preemption; req != nil && o.Status.ClaimRef == nil && d.Status.ClaimRef != nil &&
		validation.SameIdentity(d.Status.ClaimRef, &req.Claim) {
		d.Status.Preemption = nil
	}
	//= spec/solas.md#14-2-bind-against-the-offer
	//# The bind MUST be one status update that sets `claimRef` to the named
	//# claim, sets `status.fencingToken` to the next token, spec 5.3 and 13.2,
	//# and clears `status.offer`.

	//= spec/solas.md#14-1-offer
	//# When a status update clears `claimRef`, the server MUST clear
	//# `status.offer`.

	//= spec/solas.md#14-5-recovery-of-a-lost-claim
	//# A recovery MUST clear `status.offer`, because the old identity made it.
	if (o.Status.ClaimRef != nil && d.Status.ClaimRef == nil) || validation.IsTransferBind(d, o) || validation.IsRecovery(d, o) {
		d.Status.Offer = nil
	}
}

//= spec/solas.md#5-3-status-updates
//# When a status update sets `claimRef` on a free device, the server MUST
//# set `status.fencingToken` to the old value plus 1.

//= spec/solas.md#5-3-status-updates
//# The server MUST ignore the `status.fencingToken` that a client sends.

//= spec/solas.md#5-3-status-updates
//# In every other status update, the server MUST keep the old token.

// nextToken returns the fencing token that the server stores for a status
// update from old to d.
func nextToken(old, d *solas.Device) int64 {
	if (old.Status.ClaimRef == nil && d.Status.ClaimRef != nil) || validation.IsTransferBind(d, old) || validation.IsRecovery(d, old) {
		return format.NextToken(old.Status.FencingToken, format.Epoch())
	}
	return old.Status.FencingToken
}

func (s statusStrategy) ValidateUpdate(ctx context.Context, obj, old runtime.Object) field.ErrorList {
	d, o := obj.(*solas.Device), old.(*solas.Device)
	recErrs := s.validateRecovery(ctx, d, o)
	errs := validation.ValidateDeviceStatusUpdateWith(d, o, len(recErrs) == 0)
	errs = append(errs, recErrs...)
	return append(errs, s.validateProtected(ctx, d, o)...)
}

func (statusStrategy) WarningsOnUpdate(ctx context.Context, obj, old runtime.Object) []string {
	return nil
}

// GetResetFields tells server-side apply that the status subresource does
// not own the spec or the metadata.
func (statusStrategy) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return map[fieldpath.APIVersion]*fieldpath.Set{
		"solas.dev/v1alpha1": fieldpath.NewSet(
			fieldpath.MakePathOrDie("spec"),
			fieldpath.MakePathOrDie("metadata", "labels"),
			fieldpath.MakePathOrDie("metadata", "annotations"),
		),
	}
}

// GetAttrs returns the labels and fields that selectors match.
func GetAttrs(obj runtime.Object) (labels.Set, fields.Set, error) {
	d, ok := obj.(*solas.Device)
	if !ok {
		return nil, nil, fmt.Errorf("given object is not a Device")
	}
	return labels.Set(d.Labels), generic.ObjectMetaFieldsSet(&d.ObjectMeta, false), nil
}

// Match returns a predicate for devices.
func Match(label labels.Selector, field fields.Selector) storage.SelectionPredicate {
	return storage.SelectionPredicate{Label: label, Field: field, GetAttrs: GetAttrs}
}

//= spec/solas.md#8-5-reclaim-policy
//# The release MUST record who released the device.

// lastRelease returns the release record that the server stores. A clear
// of claimRef records the user of the request, the time, and the claim
// that held the device. Every other update keeps the old record; the
// server ignores a record that a client sends.
func lastRelease(ctx context.Context, old, d *solas.Device) *solas.Release {
	if old.Status.ClaimRef == nil || d.Status.ClaimRef != nil {
		return old.Status.LastRelease
	}
	by := ""
	if u, ok := genericapirequest.UserFrom(ctx); ok {
		by = u.GetName()
	}
	return &solas.Release{By: by, At: metav1.Now(), Claim: *old.Status.ClaimRef}
}

//= spec/solas.md#10-10-protected-holders
//# The server MUST reject a `claimRef` with `protected: true` unless the
//# `MemberPolicy` of its member sets `allowProtected`, spec 15.1.

// validateProtected rejects a new protected claimRef of a member whose
// policy does not allow protection.
func (s statusStrategy) validateProtected(ctx context.Context, d, old *solas.Device) field.ErrorList {
	ref := d.Status.ClaimRef
	if ref == nil || !ref.Protected || (old.Status.ClaimRef != nil && old.Status.ClaimRef.Protected) {
		return nil
	}
	if s.policies != nil {
		p, err := s.policies(ctx, ref.Member)
		if err != nil {
			return field.ErrorList{field.InternalError(field.NewPath("status", "claimRef", "protected"), err)}
		}
		if p != nil && p.Spec.AllowProtected {
			return nil
		}
	}
	return field.ErrorList{field.Forbidden(field.NewPath("status", "claimRef", "protected"),
		fmt.Sprintf("member %s may not protect its claims; an operator sets allowProtected in its MemberPolicy", ref.Member))}
}

// WithMembers returns the strategy with a lookup of members, which a
// recovery needs, spec 14.5.
func (s statusStrategy) WithMembers(members MemberLookup) statusStrategy {
	s.members = members
	return s
}

//= spec/solas.md#14-5-recovery-of-a-lost-claim
//# The server MUST reject a recovery unless the member name and the claim
//# UID are the same, and no `Member` has the old member UID.

// validateRecovery checks that the old member UID of a recovery is gone.
// The old UID was the UID of the Member with the name of the ref, and a
// UID belongs to one object only, so only that Member can have it.
func (s statusStrategy) validateRecovery(ctx context.Context, d, old *solas.Device) field.ErrorList {
	if !validation.IsRecovery(d, old) {
		return nil
	}
	path := field.NewPath("status", "claimRef", "memberUID")
	ref := d.Status.ClaimRef
	if s.members == nil {
		return field.ErrorList{field.Forbidden(path, "this server cannot check members, so it cannot accept a recovery")}
	}
	m, err := s.members(ctx, ref.Member)
	if err != nil {
		return field.ErrorList{field.InternalError(path, err)}
	}
	if m != nil && m.UID == old.Status.ClaimRef.MemberUID {
		return field.ErrorList{field.Forbidden(path,
			fmt.Sprintf("member %s still has the old UID %s", ref.Member, m.UID))}
	}
	return nil
}
