// Package memberpolicy holds the registry of MemberPolicy, spec 15.1.
package memberpolicy

import (
	"context"
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/registry/generic"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/names"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apis/solas/validation"
)

// REST is the store of member policies.
type REST struct {
	*genericregistry.Store
}

// NewREST returns the store of member policies. They live in the shared
// store, so every member reads the same limits.
func NewREST(scheme *runtime.Scheme, optsGetter generic.RESTOptionsGetter) (*REST, error) {
	s := strategy{scheme, names.SimpleNameGenerator}
	store := &genericregistry.Store{
		NewFunc:                   func() runtime.Object { return &solas.MemberPolicy{} },
		NewListFunc:               func() runtime.Object { return &solas.MemberPolicyList{} },
		PredicateFunc:             match,
		DefaultQualifiedResource:  solas.Resource("memberpolicies"),
		SingularQualifiedResource: solas.Resource("memberpolicy"),
		CreateStrategy:            s,
		UpdateStrategy:            s,
		DeleteStrategy:            s,
		TableConvertor:            tableConvertor{},
	}
	if err := store.CompleteWithOptions(&generic.StoreOptions{RESTOptions: optsGetter, AttrFunc: getAttrs}); err != nil {
		return nil, err
	}
	return &REST{store}, nil
}

type strategy struct {
	runtime.ObjectTyper
	names.NameGenerator
}

func (strategy) NamespaceScoped() bool                                            { return false }
func (strategy) PrepareForCreate(context.Context, runtime.Object)                 {}
func (strategy) PrepareForUpdate(context.Context, runtime.Object, runtime.Object) {}
func (strategy) Validate(_ context.Context, obj runtime.Object) field.ErrorList {
	return validation.ValidateMemberPolicy(obj.(*solas.MemberPolicy))
}
func (strategy) WarningsOnCreate(context.Context, runtime.Object) []string { return nil }
func (strategy) AllowCreateOnUpdate(context.Context) bool                  { return false }
func (strategy) AllowUnconditionalUpdate(context.Context) bool             { return true }
func (strategy) Canonicalize(runtime.Object)                               {}
func (strategy) ValidateUpdate(_ context.Context, obj, _ runtime.Object) field.ErrorList {
	return validation.ValidateMemberPolicy(obj.(*solas.MemberPolicy))
}
func (strategy) WarningsOnUpdate(context.Context, runtime.Object, runtime.Object) []string {
	return nil
}

func getAttrs(obj runtime.Object) (labels.Set, fields.Set, error) {
	p, ok := obj.(*solas.MemberPolicy)
	if !ok {
		return nil, nil, fmt.Errorf("given object is not a MemberPolicy")
	}
	return labels.Set(p.Labels), generic.ObjectMetaFieldsSet(&p.ObjectMeta, false), nil
}

func match(label labels.Selector, field fields.Selector) storage.SelectionPredicate {
	return storage.SelectionPredicate{Label: label, Field: field, GetAttrs: getAttrs}
}

// tableConvertor prints the columns of kubectl get memberpolicies.
type tableConvertor struct{}

var columns = []metav1.TableColumnDefinition{
	{Name: "Name", Type: "string", Format: "name"},
	{Name: "Max Devices", Type: "string"},
	{Name: "Binds/min", Type: "string"},
	{Name: "Preemptions/min", Type: "string"},
	{Name: "Protected", Type: "boolean", Description: "the member may protect its claims"},
	{Name: "Age", Type: "string"},
}

func (tableConvertor) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{ColumnDefinitions: columns}
	var items []solas.MemberPolicy
	switch o := obj.(type) {
	case *solas.MemberPolicy:
		items = []solas.MemberPolicy{*o}
	case *solas.MemberPolicyList:
		items = o.Items
		table.ResourceVersion, table.Continue, table.RemainingItemCount = o.ResourceVersion, o.Continue, o.RemainingItemCount
	default:
		return nil, fmt.Errorf("unexpected object %T", obj)
	}
	limit := func(v *int32) string {
		if v == nil {
			return "<none>"
		}
		return strconv.Itoa(int(*v))
	}
	for i := range items {
		p := &items[i]
		table.Rows = append(table.Rows, metav1.TableRow{
			Cells: []any{p.Name, limit(p.Spec.MaxDevices), limit(p.Spec.BindsPerMinute), limit(p.Spec.PreemptionsPerMinute),
				p.Spec.AllowProtected, duration.HumanDuration(metav1.Now().Sub(p.CreationTimestamp.Time))},
			Object: runtime.RawExtension{Object: p},
		})
	}
	return table, nil
}
