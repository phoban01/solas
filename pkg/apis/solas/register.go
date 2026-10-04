package solas

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupName is the name of the API group, ADR 0006.
const GroupName = "solas.dev"

// SchemeGroupVersion is the internal version of the group.
var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: runtime.APIVersionInternal}

// Kind returns a group-qualified kind.
func Kind(kind string) schema.GroupKind {
	return SchemeGroupVersion.WithKind(kind).GroupKind()
}

// Resource returns a group-qualified resource.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

var (
	// SchemeBuilder adds the internal types to a scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// AddToScheme adds the internal types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&Device{},
		&DeviceList{},
		&Member{},
		&MemberList{},
		&StoreFormat{},
		&StoreFormatList{},
		&MemberPolicy{},
		&MemberPolicyList{},
		&MemberUsage{},
		&MemberUsageList{},
	)
	return nil
}
