// Package apiserver builds the solas API server.
package apiserver

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apis/solas/install"
	"github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

var (
	// Scheme holds the types that the server serves.
	Scheme = runtime.NewScheme()
	// Codecs encodes and decodes the types of Scheme.
	Codecs = serializer.NewCodecFactory(Scheme)
)

func init() {
	install.Install(Scheme)
	metav1.AddToGroupVersion(Scheme, schema.GroupVersion{Version: "v1"})
	Scheme.AddUnversionedTypes(schema.GroupVersion{Group: "", Version: "v1"},
		&metav1.Status{},
		&metav1.APIVersions{},
		&metav1.APIGroupList{},
		&metav1.APIGroup{},
		&metav1.APIResourceList{},
	)
}

// StorageCodec encodes objects as v1alpha1 in the table.
func StorageCodec() runtime.Codec {
	return Codecs.LegacyCodec(v1alpha1.SchemeGroupVersion)
}

// StorageVersioner picks v1alpha1 for every kind of the group.
func StorageVersioner() runtime.GroupVersioner {
	return runtime.NewMultiGroupVersioner(v1alpha1.SchemeGroupVersion, schema.GroupKind{Group: solas.GroupName})
}

// StorageCodecFor returns the codec and the versioner that store objects
// in one served version of the group. A version that the scheme does not
// serve is an error. Decoding accepts every served version either way.
func StorageCodecFor(version string) (runtime.Codec, runtime.GroupVersioner, error) {
	gv := schema.GroupVersion{Group: solas.GroupName, Version: version}
	if !Scheme.IsVersionRegistered(gv) {
		return nil, nil, fmt.Errorf("storage version %q is not a version that this server serves", gv)
	}
	return Codecs.LegacyCodec(gv), runtime.NewMultiGroupVersioner(gv, schema.GroupKind{Group: solas.GroupName}), nil
}
