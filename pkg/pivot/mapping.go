// Package pivot moves Devices from a CRD in a cluster's etcd into solas,
// spec section 9 and ADR 0010.
package pivot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

// Annotations that link an old object and its Device.
const (
	AnnotationPivotedFrom = "solas.dev/pivoted-from"
	AnnotationPivotedTo   = "solas.dev/pivoted-to"
	lastApplied           = "kubectl.kubernetes.io/last-applied-configuration"
)

// Source names the old resource and the field that holds the description.
type Source struct {
	Resource schema.GroupVersionResource
	// DescriptionPath is the field path of the description, for example
	// spec.description.
	DescriptionPath []string
}

// ParseSource parses group/version/resource and a dotted field path.
func ParseSource(gvr, descriptionPath string) (Source, error) {
	parts := strings.Split(gvr, "/")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return Source{}, fmt.Errorf("--from %q: want group/version/resource", gvr)
	}
	s := Source{Resource: schema.GroupVersionResource{Group: parts[0], Version: parts[1], Resource: parts[2]}}
	if descriptionPath != "" {
		s.DescriptionPath = strings.Split(descriptionPath, ".")
	}
	return s, nil
}

//= spec/solas.md#9-1-mapping
//# The Device MUST have the annotation `solas.dev/pivoted-from`, set to the
//# group, the resource, the namespace, and the name of the old object.

// Ref is the value of solas.dev/pivoted-from for an old object.
func (s Source) Ref(namespace, name string) string {
	return s.Resource.Group + "/" + s.Resource.Resource + "/" + namespace + "/" + name
}

//= spec/solas.md#9-1-mapping
//# The old object MUST have the annotation `solas.dev/pivoted-to`, set to
//# `solas.dev/devices/` and the name.

// PivotedTo is the value of solas.dev/pivoted-to for a Device name.
func PivotedTo(name string) string { return "solas.dev/devices/" + name }

// ToDevice maps an old object to the Device that solas should hold.
func ToDevice(old *unstructured.Unstructured, src Source) (*solasv1alpha1.Device, error) {
	d := &solasv1alpha1.Device{
		ObjectMeta: metav1.ObjectMeta{
			//= spec/solas.md#9-1-mapping
			//# The old object and its Device MUST have the same name.
			Name: old.GetName(),
			//= spec/solas.md#9-1-mapping
			//# The Device MUST get the labels of the old object.
			Labels:      copyMap(old.GetLabels()),
			Annotations: map[string]string{},
		},
	}
	//= spec/solas.md#9-1-mapping
	//# The Device MUST get the annotations of the old object, except
	//# `kubectl.kubernetes.io/last-applied-configuration`.
	for k, v := range old.GetAnnotations() {
		if k != lastApplied && k != AnnotationPivotedTo {
			d.Annotations[k] = v
		}
	}
	d.Annotations[AnnotationPivotedFrom] = src.Ref(old.GetNamespace(), old.GetName())

	//= spec/solas.md#9-1-mapping
	//# The Device MUST get its description from a field of the old object that
	//# the pivot names.
	if len(src.DescriptionPath) > 0 {
		desc, found, err := unstructured.NestedString(old.Object, src.DescriptionPath...)
		if err != nil {
			return nil, fmt.Errorf("%s: field %s: %w", old.GetName(), strings.Join(src.DescriptionPath, "."), err)
		}
		if found {
			d.Spec.Description = desc
		}
	}
	//= spec/solas.md#9-1-mapping
	//# The Device MUST get the whole `spec` of the old object in
	//# `spec.parameters`, so that no field of the old object is lost.
	spec, found, err := unstructured.NestedMap(old.Object, "spec")
	if err != nil {
		return nil, fmt.Errorf("%s: field spec: %w", old.GetName(), err)
	}
	if found {
		raw, err := json.Marshal(spec)
		if err != nil {
			return nil, fmt.Errorf("%s: field spec: %w", old.GetName(), err)
		}
		d.Spec.Parameters = &runtime.RawExtension{Raw: raw}
	}
	//= spec/solas.md#9-1-mapping
	//# The pivot MUST NOT copy the status of the old object.
	return d, nil
}

// SameParameters reports whether two parameters hold the same JSON. The
// server may store the bytes with other spacing or key order.
func SameParameters(a, b *runtime.RawExtension) bool {
	norm := func(r *runtime.RawExtension) []byte {
		if r == nil || len(r.Raw) == 0 {
			return nil
		}
		var v any
		if err := json.Unmarshal(r.Raw, &v); err != nil {
			return r.Raw
		}
		out, _ := json.Marshal(v)
		return out
	}
	return bytes.Equal(norm(a), norm(b))
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
