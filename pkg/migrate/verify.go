package migrate

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"

	"github.com/phoban01/solas/pkg/apis/solas"
)

//= spec/solas.md#12-2-copy
//# After the copy, the tool MUST verify that each object in the destination
//# equals its source, except for the resource version and the epoch.

// Verify compares every Device and Member of two stores. It ignores the
// resource version. It returns an error that names each missing object,
// each extra object, and each difference.
func Verify(ctx context.Context, src, dst *Store) error {
	var problems []string
	for _, r := range resources {
		want, err := byName(ctx, src, r)
		if err != nil {
			return err
		}
		got, err := byName(ctx, dst, r)
		if err != nil {
			return err
		}
		for name, w := range want {
			g, ok := got[name]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s/%s is missing", r.gr.Resource, name))
				continue
			}
			if !equality.Semantic.DeepEqual(w, g) {
				problems = append(problems, fmt.Sprintf("%s/%s differs:\n%s", r.gr.Resource, name, diff.Diff(w, g)))
			}
		}
		for name := range got {
			if _, ok := want[name]; !ok {
				problems = append(problems, fmt.Sprintf("%s/%s is only in the destination", r.gr.Resource, name))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the stores differ:\n%s", strings.Join(problems, "\n"))
	}
	return nil
}

// byName lists a resource and clears each resource version.
func byName(ctx context.Context, s *Store, r resource) (map[string]runtime.Object, error) {
	objs, err := s.list(ctx, r)
	if err != nil {
		return nil, err
	}
	out := map[string]runtime.Object{}
	for _, o := range objs {
		m, err := meta.Accessor(o)
		if err != nil {
			return nil, err
		}
		m.SetResourceVersion("")
		// The move raises the epoch of the destination, spec 12.2.
		if f, ok := o.(*solas.StoreFormat); ok {
			f.Epoch = 0
		}
		out[m.GetName()] = o
	}
	return out, nil
}

//= spec/solas.md#12-1-seal
//# The tool MUST NOT unseal a source when the destination holds an object
//# that is not in the source, or that differs from its source object except
//# in resource version.

// Untouched returns an error when dst holds an object that is not an exact
// copy of a source object, except for the resource version. Such an object
// shows that a cluster wrote to dst. A partial copy passes.
func Untouched(ctx context.Context, src, dst *Store) error {
	var problems []string
	for _, r := range resources {
		want, err := byName(ctx, src, r)
		if err != nil {
			return err
		}
		got, err := byName(ctx, dst, r)
		if err != nil {
			return err
		}
		for name, g := range got {
			w, ok := want[name]
			if !ok || !equality.Semantic.DeepEqual(w, g) {
				problems = append(problems, r.gr.Resource+"/"+name)
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("a cluster wrote to the destination: %s", strings.Join(problems, ", "))
	}
	return nil
}
