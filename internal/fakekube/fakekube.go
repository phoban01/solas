// Package fakekube gives controller tests a fake client that behaves like
// solas-apiserver where the controllers depend on it.
package fakekube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	"github.com/phoban01/solas/pkg/apis/solas"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/apiserver"
	"github.com/phoban01/solas/pkg/controller/scheme"
	"github.com/phoban01/solas/pkg/registry/solas/device"
)

// Options change how the fake client behaves.
type Options struct {
	// Unconditional drops the resource version check and the holder check
	// of Device status updates, to break spec 5.3 on purpose.
	Unconditional bool
}

// NewClient returns a fake client with the objects. It gives each created
// object a UID, and it applies the Device status rules of spec 5.3: a new
// holder needs a free device, and a bind raises the fencing token.
func NewClient(objs ...client.Object) client.WithWatch {
	return NewClientWithOptions(Options{}, objs...)
}

// NewClientWithOptions is NewClient with options.
func NewClientWithOptions(o Options, objs ...client.Object) client.WithWatch {
	return fake.NewClientBuilder().
		WithScheme(scheme.New()).
		WithObjects(objs...).
		WithStatusSubresource(&solasv1alpha1.Device{}, &solasv1alpha1.Member{}, &claimsv1alpha1.DeviceClaim{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if obj.GetUID() == "" {
					obj.SetUID(NewUID())
				}
				if m, ok := obj.(*solasv1alpha1.Member); ok && m.Status.Phase == "" {
					m.Status.Phase = solasv1alpha1.MemberActive
				}
				return c.Create(ctx, obj, opts...)
			},
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if d, ok := obj.(*solasv1alpha1.Device); ok && sub == "status" {
					if err := deviceStatus(ctx, c, d, o.Unconditional); err != nil {
						return err
					}
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()
}

// deviceStatus applies the real status strategy and validation of
// solas-apiserver to d, so the fake follows the same rules as the server.
func deviceStatus(ctx context.Context, c client.Client, d *solasv1alpha1.Device, unconditional bool) error {
	var old solasv1alpha1.Device
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), &old); err != nil {
		return err
	}
	if unconditional {
		d.ResourceVersion = old.ResourceVersion
		d.Status.FencingToken = old.Status.FencingToken
		if old.Status.ClaimRef == nil && d.Status.ClaimRef != nil {
			d.Status.FencingToken++
		}
		return nil
	}
	if old.ResourceVersion != d.ResourceVersion {
		return apierrors.NewConflict(schema.GroupResource{Group: "solas.dev", Resource: "devices"}, d.Name,
			fmt.Errorf("resource version %s is not %s", d.ResourceVersion, old.ResourceVersion))
	}
	var in, oldIn solas.Device
	if err := apiserver.Scheme.Convert(d, &in, nil); err != nil {
		return err
	}
	if err := apiserver.Scheme.Convert(&old, &oldIn, nil); err != nil {
		return err
	}
	strategy := device.NewStatusStrategy(device.NewStrategy(apiserver.Scheme)).WithMembers(
		func(ctx context.Context, name string) (*solas.Member, error) {
			var m solasv1alpha1.Member
			if err := c.Get(ctx, client.ObjectKey{Name: name}, &m); err != nil {
				return nil, client.IgnoreNotFound(err)
			}
			var out solas.Member
			return &out, apiserver.Scheme.Convert(&m, &out, nil)
		})
	strategy.PrepareForUpdate(ctx, &in, &oldIn)
	if errs := strategy.ValidateUpdate(ctx, &in, &oldIn); len(errs) > 0 {
		return apierrors.NewInvalid(schema.GroupKind{Group: "solas.dev", Kind: "Device"}, d.Name, errs)
	}
	return apiserver.Scheme.Convert(&in, d, nil)
}

// NewUID returns a random UID.
func NewUID() types.UID {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return types.UID(hex.EncodeToString(b))
}
