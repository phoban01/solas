package sim

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

// check tests the safety properties of spec 8.4, the same ones that the
// Quint model checks.
func (w *World) check(ctx context.Context) error {
	if w.sweptRetained != nil {
		return w.sweptRetained
	}
	var devices solasv1alpha1.DeviceList
	if err := w.shared.List(ctx, &devices); err != nil {
		return err
	}
	byName := map[string]*solasv1alpha1.Device{}
	for i := range devices.Items {
		d := &devices.Items[i]
		// requestBeatsHolder: a request always beats the holder, spec 10.5.
		if p, h := d.Status.Preemption, d.Status.ClaimRef; p != nil && h != nil && p.Claim.Priority <= h.Priority {
			return fmt.Errorf("requestBeatsHolder: %s request priority %d, holder priority %d", d.Name, p.Claim.Priority, h.Priority)
		}
		byName[d.Name] = d
		ref := d.Status.ClaimRef
		if ref == nil {
			continue
		}
		// tokensUnique: each fencing token of a device belongs to one bind.
		held := w.bindings[d.Name]
		if prev, ok := held[d.Status.FencingToken]; ok && prev != string(ref.UID) {
			return fmt.Errorf("tokensUnique: %s token %d belongs to claim %s and to claim %s",
				d.Name, d.Status.FencingToken, prev, ref.UID)
		}
		held[d.Status.FencingToken] = string(ref.UID)
	}

	for name, g := range w.gates {
		// useMatchesBinding: the last accepted use comes from the claim that
		// got its token in a bind.
		if g.last.claim != "" && w.bindings[name][g.last.token] != g.last.claim {
			return fmt.Errorf("useMatchesBinding: %s accepted token %d from claim %s, but that token belongs to %q",
				name, g.last.token, g.last.claim, w.bindings[name][g.last.token])
		}
	}

	for _, c := range w.clusters {
		st := c.members.Status()
		// leaseSafety: a member that is live by its own clock has its Member.
		if st.Live {
			var m solasv1alpha1.Member
			err := w.shared.Get(ctx, client.ObjectKey{Name: c.name}, &m)
			if err != nil || m.UID != st.UID {
				return fmt.Errorf("leaseSafety: %s is live with uid %s, but its Member is %q (%v)", c.name, st.UID, m.UID, err)
			}
		}
		// claimMatchesDevice: a claim in effect is named by its device.
		var claims claimsv1alpha1.DeviceClaimList
		if err := c.client.List(ctx, &claims); err != nil {
			return err
		}
		for i := range claims.Items {
			cl := &claims.Items[i]
			if cl.Status.Phase == claimsv1alpha1.ClaimPreempted {
				w.sawPreempted = true
			}
			// A Preempting claim is in effect, as a Bound one is, spec 10.6.
			inEffect := cl.Status.Phase == claimsv1alpha1.ClaimBound || cl.Status.Phase == claimsv1alpha1.ClaimPreempting
			if !inEffect || !cl.DeletionTimestamp.IsZero() || !st.Live {
				continue
			}
			d := byName[cl.Status.DeviceName]
			if d == nil || d.Status.ClaimRef == nil || d.Status.ClaimRef.UID != cl.UID {
				return fmt.Errorf("claimMatchesDevice: %s claim %s is in effect on %s, but the device names %+v",
					c.name, cl.Name, cl.Status.DeviceName, refOf(d))
			}
		}
	}
	return nil
}

func refOf(d *solasv1alpha1.Device) any {
	if d == nil {
		return "no device"
	}
	return d.Status.ClaimRef
}
