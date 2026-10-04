package member

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/generic"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"

	"github.com/phoban01/solas/pkg/apis/solas"
)

// REST is the store of members.
type REST struct {
	*genericregistry.Store
}

// StatusREST is the store of the status subresource of members.
type StatusREST struct {
	store *genericregistry.Store
}

// NewREST returns the stores of members and their status.
func NewREST(scheme *runtime.Scheme, optsGetter generic.RESTOptionsGetter) (*REST, *StatusREST, error) {
	strategy := NewStrategy(scheme)
	store := &genericregistry.Store{
		NewFunc:                   func() runtime.Object { return &solas.Member{} },
		NewListFunc:               func() runtime.Object { return &solas.MemberList{} },
		PredicateFunc:             Match,
		DefaultQualifiedResource:  solas.Resource("members"),
		SingularQualifiedResource: solas.Resource("member"),

		CreateStrategy:      strategy,
		UpdateStrategy:      strategy,
		DeleteStrategy:      strategy,
		ResetFieldsStrategy: strategy,

		TableConvertor: tableConvertor{},
	}
	//= spec/solas.md#7-1-resource
	//# The solas API server MUST serve `Member` from the table.
	options := &generic.StoreOptions{RESTOptions: optsGetter, AttrFunc: GetAttrs}
	if err := store.CompleteWithOptions(options); err != nil {
		return nil, nil, err
	}
	statusStore := *store
	status := NewStatusStrategy(strategy)
	statusStore.UpdateStrategy = status
	statusStore.ResetFieldsStrategy = status
	return &REST{store}, &StatusREST{store: &statusStore}, nil
}

// New returns an empty member.
func (r *StatusREST) New() runtime.Object { return &solas.Member{} }

// Destroy does nothing; the main store owns the storage.
func (r *StatusREST) Destroy() {}

// Get returns a member, so that clients can read before they update status.
func (r *StatusREST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	return r.store.Get(ctx, name, options)
}

// Update updates the status of a member. It is how a member renews its
// lease, spec 7.3.
func (r *StatusREST) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo,
	createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc,
	forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	return r.store.Update(ctx, name, objInfo, createValidation, updateValidation, false, options)
}

// GetResetFields returns the fields that the status subresource resets.
func (r *StatusREST) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return r.store.GetResetFields()
}
