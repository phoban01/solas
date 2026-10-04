package device

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/format"
)

// Transition holds the rules that the storage guard checks on each write
// of a Device, below the registry. They repeat the rules of the status
// strategy on the stored objects, and do not import the strategy. They
// change only with a format bump, spec 11.
func Transition(oldObj, newObj runtime.Object) error {
	n, ok := newObj.(*solas.Device)
	if !ok {
		return fmt.Errorf("unexpected object %T", newObj)
	}
	if oldObj == nil {
		//= spec/solas.md#5-3-status-updates
		//# When a status update sets `claimRef` on a free device, the server MUST
		//# set `status.fencingToken` to the old value plus 1.
		//
		// A new device starts free with token 0, so its first bind gets 1.
		if n.Status.ClaimRef != nil || n.Status.FencingToken != 0 {
			return errors.New("a new device must start with no claimRef and token 0")
		}
		return nil
	}
	o, ok := oldObj.(*solas.Device)
	if !ok {
		return fmt.Errorf("unexpected object %T", oldObj)
	}
	oldRef, newRef := o.Status.ClaimRef, n.Status.ClaimRef
	if err := offerTransition(o, n); err != nil {
		return err
	}
	switch {
	case recovery(o, n):
		//= spec/solas.md#14-5-recovery-of-a-lost-claim
		//# A recovery MUST be one status update that sets the `claimRef` to the
		//# member's current UID and raises `status.fencingToken`, spec 5.3 and 13.2.
		//
		// The guard checks the shape and the token. The status strategy checks
		// that the old member UID is gone, as the guard cannot read members.
		if want := format.NextToken(o.Status.FencingToken, format.Epoch()); n.Status.FencingToken != want {
			return fmt.Errorf("a recovery of device %s must set token %d, not %d", n.Name, want, n.Status.FencingToken)
		}
	case transferBind(o, n):
		//= spec/solas.md#14-2-bind-against-the-offer
		//# The bind MUST be one status update that sets `claimRef` to the named
		//# claim, sets `status.fencingToken` to the next token, spec 5.3 and 13.2,
		//# and clears `status.offer`.
		if n.Status.Offer != nil {
			return fmt.Errorf("a bind of device %s against its offer must clear the offer", n.Name)
		}
		if want := format.NextToken(o.Status.FencingToken, format.Epoch()); n.Status.FencingToken != want {
			return fmt.Errorf("a bind of device %s must set token %d, not %d", n.Name, want, n.Status.FencingToken)
		}
	case oldRef != nil && newRef != nil && !equality.Semantic.DeepEqual(oldRef, newRef):
		//= spec/solas.md#5-3-status-updates
		//# The server MUST reject a status update that changes `claimRef` from one
		//# holder to a different holder.
		return fmt.Errorf("device %s is held by %s/%s; clear claimRef first", n.Name, oldRef.Namespace, oldRef.Name)
	case oldRef == nil && newRef != nil:
		if want := format.NextToken(o.Status.FencingToken, format.Epoch()); n.Status.FencingToken != want {
			return fmt.Errorf("a bind of device %s must set token %d, not %d", n.Name, want, n.Status.FencingToken)
		}
	default:
		//= spec/solas.md#5-3-status-updates
		//# In every other status update, the server MUST keep the old token.
		if n.Status.FencingToken != o.Status.FencingToken {
			return fmt.Errorf("a write that does not bind device %s must keep token %d", n.Name, o.Status.FencingToken)
		}
	}
	return nil
}

// transferBind reports whether claimRef moves from the holder to exactly
// the claim and member UID of the old offer, spec 14.2.
func transferBind(o, n *solas.Device) bool {
	offer, ref := o.Status.Offer, n.Status.ClaimRef
	return o.Status.ClaimRef != nil && offer != nil && ref != nil &&
		ref.Member == offer.Member && ref.MemberUID == offer.MemberUID &&
		ref.Namespace == offer.Namespace && ref.Name == offer.Name && ref.UID == offer.UID
}

// offerTransition checks that an offer stands only on a held device, with
// no preemption request, and is not replaced by another offer, spec 14.1.
func offerTransition(o, n *solas.Device) error {
	offer := n.Status.Offer
	switch {
	case offer == nil:
		return nil
	case n.Status.ClaimRef == nil:
		return fmt.Errorf("device %s is free; it cannot have an offer", n.Name)
	case n.Status.Preemption != nil:
		return fmt.Errorf("device %s has a preemption request; it cannot have an offer", n.Name)
	case equality.Semantic.DeepEqual(offer, o.Status.Offer):
		return nil
	case o.Status.ClaimRef == nil:
		return fmt.Errorf("device %s is free; it cannot have an offer", n.Name)
	case o.Status.Offer != nil:
		return fmt.Errorf("device %s has an offer; withdraw it first", n.Name)
	case o.Status.Preemption != nil:
		return fmt.Errorf("device %s has a preemption request; it cannot have an offer", n.Name)
	}
	return nil
}

// recovery reports whether claimRef keeps the member name and the claim,
// and moves to another member UID, spec 14.5.
func recovery(o, n *solas.Device) bool {
	a, b := o.Status.ClaimRef, n.Status.ClaimRef
	return a != nil && b != nil && a.Member == b.Member && a.Namespace == b.Namespace &&
		a.Name == b.Name && a.UID == b.UID && a.MemberUID != b.MemberUID
}
