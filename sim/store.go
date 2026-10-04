// Package sim runs the real solas controllers of several member clusters
// against in-memory stores, with pauses, restarts, and clock drift, and
// checks the safety properties of spec 8.4 after every step.
package sim

import (
	"context"
	"errors"
	"math/rand/v2"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/phoban01/solas/internal/fakekube"
	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

// newShared returns the shared store of Devices and Members, as the table
// is shared, spec 2.1.
func newShared(unconditional bool) client.Client {
	return fakekube.NewClientWithOptions(fakekube.Options{Unconditional: unconditional})
}

// router sends DeviceClaim calls to the store of one cluster, as claims
// live in each cluster's etcd (ADR 0002), and every other call to the
// shared store.
type router struct {
	client.Client
	local client.Client

	// rng and lag make a device list late: with probability lag, List
	// returns the device list of the call before. That is a list followed
	// by other steps before the caller acts on it, as in a real race
	// between a read and a write. Member lists are never late: the sweeper
	// must see devices before members, spec 8.3.
	rng  *rand.Rand
	lag  float64
	last *solasv1alpha1.DeviceList
	// outage points at the outage flag of the world. While it is true,
	// every call to the shared store fails.
	outage *bool
}

// newRouter returns the client that one cluster's controllers use.
func newRouter(shared client.Client, rng *rand.Rand, lag float64) *router {
	return &router{Client: shared, local: fakekube.NewClient(), rng: rng, lag: lag}
}

func (r *router) pick(o runtime.Object) client.Client {
	switch o.(type) {
	case *claimsv1alpha1.DeviceClaim, *claimsv1alpha1.DeviceClaimList:
		return r.local
	}
	if r.outage != nil && *r.outage {
		return down{}
	}
	return r.Client
}

func (r *router) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return r.pick(obj).Get(ctx, key, obj, opts...)
}

func (r *router) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, local := list.(*claimsv1alpha1.DeviceClaimList); !local && r.outage != nil && *r.outage {
		return errStoreDown
	}
	if dl, ok := list.(*solasv1alpha1.DeviceList); ok && r.rng != nil {
		if r.last != nil && r.rng.Float64() < r.lag {
			r.last.DeepCopyInto(dl)
			return nil
		}
		if err := r.Client.List(ctx, dl, opts...); err != nil {
			return err
		}
		r.last = dl.DeepCopy()
		return nil
	}
	return r.pick(list).List(ctx, list, opts...)
}

func (r *router) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return r.pick(obj).Create(ctx, obj, opts...)
}

func (r *router) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return r.pick(obj).Delete(ctx, obj, opts...)
}

func (r *router) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return r.pick(obj).Update(ctx, obj, opts...)
}

func (r *router) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return r.pick(obj).Patch(ctx, obj, patch, opts...)
}

func (r *router) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return r.pick(obj).DeleteAllOf(ctx, obj, opts...)
}

func (r *router) Status() client.SubResourceWriter { return r.SubResource("status") }

func (r *router) SubResource(name string) client.SubResourceClient {
	return &subRouter{r: r, name: name}
}

// subRouter routes subresource calls the same way.
type subRouter struct {
	r    *router
	name string
}

func (s *subRouter) Get(ctx context.Context, obj client.Object, sub client.Object, opts ...client.SubResourceGetOption) error {
	return s.r.pick(obj).SubResource(s.name).Get(ctx, obj, sub, opts...)
}

func (s *subRouter) Create(ctx context.Context, obj client.Object, sub client.Object, opts ...client.SubResourceCreateOption) error {
	return s.r.pick(obj).SubResource(s.name).Create(ctx, obj, sub, opts...)
}

func (s *subRouter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return s.r.pick(obj).SubResource(s.name).Update(ctx, obj, opts...)
}

func (s *subRouter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return s.r.pick(obj).SubResource(s.name).Patch(ctx, obj, patch, opts...)
}

func (s *subRouter) Apply(context.Context, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
	return errors.New("sim: apply is not supported")
}
