package sim

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

// errStoreDown is the error of every call to the shared store during an
// outage.
var errStoreDown = errors.New("sim: the shared store is down")

// down is the shared store during an outage: every call fails.
type down struct{ client.Client }

func (down) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errStoreDown
}
func (down) List(context.Context, client.ObjectList, ...client.ListOption) error { return errStoreDown }
func (down) Create(context.Context, client.Object, ...client.CreateOption) error { return errStoreDown }
func (down) Delete(context.Context, client.Object, ...client.DeleteOption) error { return errStoreDown }
func (down) Update(context.Context, client.Object, ...client.UpdateOption) error { return errStoreDown }
func (down) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return errStoreDown
}
func (down) DeleteAllOf(context.Context, client.Object, ...client.DeleteAllOfOption) error {
	return errStoreDown
}
func (down) Status() client.SubResourceWriter            { return downSub{} }
func (down) SubResource(string) client.SubResourceClient { return downSub{} }

type downSub struct{}

func (downSub) Get(context.Context, client.Object, client.Object, ...client.SubResourceGetOption) error {
	return errStoreDown
}
func (downSub) Create(context.Context, client.Object, client.Object, ...client.SubResourceCreateOption) error {
	return errStoreDown
}
func (downSub) Update(context.Context, client.Object, ...client.SubResourceUpdateOption) error {
	return errStoreDown
}
func (downSub) Patch(context.Context, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	return errStoreDown
}
func (downSub) Apply(context.Context, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
	return errStoreDown
}

// sweep runs the sweeper of c, and records a violation of spec 8.5 when
// the sweep frees a retained device whose holder's member is gone.
func (w *World) sweep(ctx context.Context, c *cluster) {
	before := w.retainedOfGone(ctx)
	w.sawRetained = w.sawRetained || len(before) > 0
	w.log("%s sweep: %s", c.name, errText(c.sweeper.Run(ctx)))
	for name, uid := range before {
		var d solasv1alpha1.Device
		if err := w.shared.Get(ctx, client.ObjectKey{Name: name}, &d); err == nil && d.Status.ClaimRef == nil && w.sweptRetained == nil {
			w.sweptRetained = fmt.Errorf("retainNeverSwept: the sweep of %s freed retained device %s of gone member UID %s", c.name, name, uid)
		}
	}
}

// retainedOfGone returns the retained devices whose holder's member is
// gone, with that member UID.
func (w *World) retainedOfGone(ctx context.Context) map[string]string {
	var devices solasv1alpha1.DeviceList
	var members solasv1alpha1.MemberList
	if w.shared.List(ctx, &devices) != nil || w.shared.List(ctx, &members) != nil {
		return nil
	}
	live := map[string]bool{}
	for _, m := range members.Items {
		live[string(m.UID)] = true
	}
	out := map[string]string{}
	for _, d := range devices.Items {
		if r := d.Status.ClaimRef; r != nil && d.Spec.ReclaimPolicy == solasv1alpha1.ReclaimRetain && !live[string(r.MemberUID)] {
			out[d.Name] = string(r.MemberUID)
		}
	}
	return out
}
