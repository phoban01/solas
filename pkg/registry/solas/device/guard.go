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
	switch {
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
