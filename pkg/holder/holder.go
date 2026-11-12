// Package holder is the holder primitive: the rules of a kind that one
// claim at a time holds, spec 5.3, 6.6, and 14. A kind opts in by keeping
// the fields of solas.HolderStatus in its status and implementing
// solas.Held; the server then registers Guard for it, and its status
// strategy calls Prepare. Device is the first such kind. The rules name
// no kind, and the storage layer names neither the rules nor the kinds.
package holder

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/format"
)

//= spec/solas.md#5-3-status-updates
//# The server MUST check the rules of a kind with a holder below the
//# registry, on the stored objects, so that servers of two releases
//# enforce them alike, section 11.

// Guard is the storage guard of a kind with a holder: the transition
// check that the server registers for the kind, below the registry. It
// repeats the rules of the status strategy on the stored objects and does
// not depend on it, so servers of two releases enforce the same rules.
// old is nil for a create.
func Guard(oldObj, newObj runtime.Object) error {
	n, ok := newObj.(solas.Held)
	if !ok {
		return fmt.Errorf("%T has no holder", newObj)
	}
	name := objectName(newObj)
	if oldObj == nil {
		return Transition(name, nil, n.HolderStatus())
	}
	o, ok := oldObj.(solas.Held)
	if !ok {
		return fmt.Errorf("%T has no holder", oldObj)
	}
	old := o.HolderStatus()
	return Transition(name, &old, n.HolderStatus())
}

// objectName returns the name of the object, for error messages.
func objectName(obj runtime.Object) string {
	if m, ok := obj.(interface{ GetName() string }); ok {
		return m.GetName()
	}
	return "?"
}

// Transition checks a write of the holder status of one object from old
// to n. old is nil for a create. name is for error messages.
func Transition(name string, old *solas.HolderStatus, n solas.HolderStatus) error {
	if old == nil {
		//= spec/solas.md#5-3-status-updates
		//# When a status update sets `claimRef` on a free device, the server MUST
		//# set `status.fencingToken` to the old value plus 1.
		//
		// A new object starts free with token 0, so its first bind gets 1.
		if n.ClaimRef != nil || n.FencingToken != 0 {
			return errors.New("a new object must start with no claimRef and token 0")
		}
		return nil
	}
	o := *old
	oldRef, newRef := o.ClaimRef, n.ClaimRef
	if err := offerTransition(name, o, n); err != nil {
		return err
	}
	switch {
	case IsRecovery(o, n):
		//= spec/solas.md#14-5-recovery-of-a-lost-claim
		//# A recovery MUST be one status update that sets the `claimRef` to the
		//# member's current UID and raises `status.fencingToken`, spec 5.3 and 13.2.
		//
		// The guard checks the shape and the token. The status strategy checks
		// that the old member UID is gone, as the guard cannot read members.
		if want := format.NextToken(o.FencingToken, format.Epoch()); n.FencingToken != want {
			return fmt.Errorf("a recovery of %s must set token %d, not %d", name, want, n.FencingToken)
		}
	case IsTransferBind(o, n):
		//= spec/solas.md#14-2-bind-against-the-offer
		//# The bind MUST be one status update that sets `claimRef` to the named
		//# claim, sets `status.fencingToken` to the next token, spec 5.3 and 13.2,
		//# and clears `status.offer`.
		if n.Offer != nil {
			return fmt.Errorf("a bind of %s against its offer must clear the offer", name)
		}
		if want := format.NextToken(o.FencingToken, format.Epoch()); n.FencingToken != want {
			return fmt.Errorf("a bind of %s must set token %d, not %d", name, want, n.FencingToken)
		}
	case oldRef != nil && newRef != nil && !equality.Semantic.DeepEqual(oldRef, newRef):
		//= spec/solas.md#5-3-status-updates
		//# The server MUST reject a status update that changes `claimRef` from one
		//# holder to a different holder.
		return fmt.Errorf("%s is held by %s/%s; clear claimRef first", name, oldRef.Namespace, oldRef.Name)
	case oldRef == nil && newRef != nil:
		if want := format.NextToken(o.FencingToken, format.Epoch()); n.FencingToken != want {
			return fmt.Errorf("a bind of %s must set token %d, not %d", name, want, n.FencingToken)
		}
	default:
		//= spec/solas.md#5-3-status-updates
		//# In every other status update, the server MUST keep the old token.
		if n.FencingToken != o.FencingToken {
			return fmt.Errorf("a write that does not bind %s must keep token %d", name, o.FencingToken)
		}
	}
	return nil
}

// offerTransition checks that an offer stands only on a held object, with
// no preemption request, and is not replaced by another offer, spec 14.1.
func offerTransition(name string, o, n solas.HolderStatus) error {
	offer := n.Offer
	switch {
	case offer == nil:
		return nil
	case n.ClaimRef == nil:
		return fmt.Errorf("%s is free; it cannot have an offer", name)
	case n.Preemption != nil:
		return fmt.Errorf("%s has a preemption request; it cannot have an offer", name)
	case equality.Semantic.DeepEqual(offer, o.Offer):
		return nil
	case o.ClaimRef == nil:
		return fmt.Errorf("%s is free; it cannot have an offer", name)
	case o.Offer != nil:
		return fmt.Errorf("%s has an offer; withdraw it first", name)
	case o.Preemption != nil:
		return fmt.Errorf("%s has a preemption request; it cannot have an offer", name)
	}
	return nil
}

//= spec/solas.md#5-3-status-updates
//# When a status update sets `claimRef` on a free device, the server MUST
//# set `status.fencingToken` to the old value plus 1.

//= spec/solas.md#5-3-status-updates
//# The server MUST ignore the `status.fencingToken` that a client sends.

//= spec/solas.md#5-3-status-updates
//# In every other status update, the server MUST keep the old token.

//= spec/solas.md#10-7-bind-by-the-preemptor
//# The bind of the requesting claim MUST clear `status.preemption`.

//= spec/solas.md#14-2-bind-against-the-offer
//# The bind MUST be one status update that sets `claimRef` to the named
//# claim, sets `status.fencingToken` to the next token, spec 5.3 and 13.2,
//# and clears `status.offer`.

//= spec/solas.md#14-1-offer
//# When a status update clears `claimRef`, the server MUST clear
//# `status.offer`.

//= spec/solas.md#14-5-recovery-of-a-lost-claim
//# A recovery MUST clear `status.offer`, because the old identity made it.

// Prepare returns the holder status that the server stores for a status
// update from old to n: the fencing token that the server sets, the
// offer cleared by a release, a bind against it, or a recovery, and the
// preemption request cleared by the bind of the claim that made it. The
// status strategy of a kind with a holder calls it in PrepareForUpdate.
func Prepare(old, n solas.HolderStatus) solas.HolderStatus {
	n.FencingToken = NextToken(old, n)
	if req := old.Preemption; req != nil && old.ClaimRef == nil && n.ClaimRef != nil && SameIdentity(n.ClaimRef, &req.Claim) {
		n.Preemption = nil
	}
	if (old.ClaimRef != nil && n.ClaimRef == nil) || IsTransferBind(old, n) || IsRecovery(old, n) {
		n.Offer = nil
	}
	return n
}

// NextToken returns the fencing token that the server stores for a status
// update from old to n: the next token on a bind of a free object, a bind
// against an offer, or a recovery, and the old token in every other case.
func NextToken(old, n solas.HolderStatus) int64 {
	if (old.ClaimRef == nil && n.ClaimRef != nil) || IsTransferBind(old, n) || IsRecovery(old, n) {
		return format.NextToken(old.FencingToken, format.Epoch())
	}
	return old.FencingToken
}

// SameIdentity reports whether two refs name the same claim.
func SameIdentity(a, b *solas.ClaimRef) bool {
	return a.Member == b.Member && a.MemberUID == b.MemberUID && a.Namespace == b.Namespace &&
		a.Name == b.Name && a.UID == b.UID
}

// IsTransferBind reports whether an update from old to n is a bind
// against the offer of old: claimRef moves from the holder to exactly the
// claim and member UID that the offer names, spec 14.2.
func IsTransferBind(old, n solas.HolderStatus) bool {
	return old.ClaimRef != nil && old.Offer != nil && n.ClaimRef != nil && SameIdentity(n.ClaimRef, old.Offer)
}

// IsRecovery reports whether an update from old to n has the shape of a
// recovery: claimRef keeps the member name and the claim, and moves to
// another member UID, spec 14.5. The status strategy also checks that the
// old member UID is gone.
func IsRecovery(old, n solas.HolderStatus) bool {
	o, c := old.ClaimRef, n.ClaimRef
	return o != nil && c != nil && o.Member == c.Member && o.Namespace == c.Namespace &&
		o.Name == c.Name && o.UID == c.UID && o.MemberUID != c.MemberUID
}
