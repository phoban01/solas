package pivot

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"

	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/storage/guard"
)

// Pivot moves the objects of an old Device CRD into solas.
type Pivot struct {
	// Old reads and patches the old resource.
	Old dynamic.Interface
	// Solas reads and writes solas Devices.
	Solas  client.Client
	Source Source
	// DryRun makes Copy report what it would do, and write nothing.
	DryRun bool
}

// Report says what a copy did, by Device name.
type Report struct {
	Created   []string
	Updated   []string
	Unchanged []string
	// Conflicts are Devices that exist but do not name the old object.
	Conflicts []string
}

func (p *Pivot) old() dynamic.NamespaceableResourceInterface {
	return p.Old.Resource(p.Source.Resource)
}

func (p *Pivot) list(ctx context.Context) ([]unstructured.Unstructured, error) {
	list, err := p.old().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", p.Source.Resource, err)
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
	return items, nil
}

// Copy creates or updates a Device for each old object.
func (p *Pivot) Copy(ctx context.Context) (Report, error) {
	var r Report
	items, err := p.list(ctx)
	if err != nil {
		return r, err
	}
	for i := range items {
		o := &items[i]
		result, err := p.upsert(ctx, o)
		if err != nil {
			return r, err
		}
		switch result {
		case created:
			r.Created = append(r.Created, o.GetName())
		case updated:
			r.Updated = append(r.Updated, o.GetName())
		case unchanged:
			r.Unchanged = append(r.Unchanged, o.GetName())
		case conflict:
			//= spec/solas.md#9-2-copy
			//# It MUST report each such Device as a conflict.
			r.Conflicts = append(r.Conflicts, o.GetName())
			continue
		}
		if err := p.markOld(ctx, o); err != nil {
			return r, err
		}
	}
	return r, nil
}

type outcome int

const (
	created outcome = iota
	updated
	unchanged
	conflict
)

//= spec/solas.md#9-2-copy
//# Running the copy twice MUST give the same result as running it once.

// upsert makes the Device of o match the mapping.
func (p *Pivot) upsert(ctx context.Context, o *unstructured.Unstructured) (outcome, error) {
	want, err := ToDevice(o, p.Source)
	if err != nil {
		return 0, err
	}
	var cur solasv1alpha1.Device
	err = p.Solas.Get(ctx, client.ObjectKey{Name: want.Name}, &cur)
	switch {
	case apierrors.IsNotFound(err):
		//= spec/solas.md#9-2-copy
		//# The copy MUST create the Device when it does not exist.
		if !p.DryRun {
			if err := p.Solas.Create(ctx, want); err != nil {
				return 0, fmt.Errorf("create device %s: %w", want.Name, err)
			}
		}
		return created, nil
	case err != nil:
		return 0, err
	}

	//= spec/solas.md#9-2-copy
	//# The copy MUST NOT change a Device whose `solas.dev/pivoted-from` is not
	//# set or names another object.
	if cur.Annotations[AnnotationPivotedFrom] != want.Annotations[AnnotationPivotedFrom] {
		return conflict, nil
	}
	// The storage guard stamps the format of each write. It is not from the
	// old object, so keep it, or every copy would update the Device.
	if f, ok := cur.Annotations[guard.FormatAnnotation]; ok {
		want.Annotations[guard.FormatAnnotation] = f
	}
	if same(&cur, want) {
		return unchanged, nil
	}
	//= spec/solas.md#9-2-copy
	//# The copy MUST update the Device when its `solas.dev/pivoted-from` names
	//# the old object.
	// Only the description and the parameters come from the old object;
	// keep the rest of the spec, such as attributes set in solas.
	cur.Labels, cur.Annotations, cur.Spec.Description = want.Labels, want.Annotations, want.Spec.Description
	cur.Spec.Parameters = want.Spec.Parameters
	if !p.DryRun {
		if err := p.Solas.Update(ctx, &cur); err != nil {
			return 0, fmt.Errorf("update device %s: %w", cur.Name, err)
		}
	}
	return updated, nil
}

// markOld adds solas.dev/pivoted-to to the old object.
func (p *Pivot) markOld(ctx context.Context, o *unstructured.Unstructured) error {
	if o.GetAnnotations()[AnnotationPivotedTo] == PivotedTo(o.GetName()) {
		return nil
	}
	//= spec/solas.md#9-2-copy
	//# With `--dry-run`, the copy MUST NOT write.
	if p.DryRun {
		return nil
	}
	patch, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"annotations": map[string]string{AnnotationPivotedTo: PivotedTo(o.GetName())}},
	})
	_, err := p.old().Namespace(o.GetNamespace()).Patch(ctx, o.GetName(), types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("annotate %s: %w", o.GetName(), err)
	}
	return nil
}

// Verify returns one problem for each old object whose Device is missing
// or different, spec 9.3.
func (p *Pivot) Verify(ctx context.Context) ([]string, error) {
	items, err := p.list(ctx)
	if err != nil {
		return nil, err
	}
	var problems []string
	for i := range items {
		o := &items[i]
		want, err := ToDevice(o, p.Source)
		if err != nil {
			return nil, err
		}
		var cur solasv1alpha1.Device
		//= spec/solas.md#9-3-verify
		//# The verify MUST check that each old object has a Device.
		if err := p.Solas.Get(ctx, client.ObjectKey{Name: want.Name}, &cur); err != nil {
			if apierrors.IsNotFound(err) {
				problems = append(problems, fmt.Sprintf("%s: no Device", o.GetName()))
				continue
			}
			return nil, err
		}
		//= spec/solas.md#9-3-verify
		//# It MUST check that the Device names the old object, and that the labels
		//# and the description match.

		//= spec/solas.md#9-3-verify
		//# It MUST check that `spec.parameters` holds the `spec` of the old object.
		switch {
		case cur.Annotations[AnnotationPivotedFrom] != want.Annotations[AnnotationPivotedFrom]:
			problems = append(problems, fmt.Sprintf("%s: Device names %q, not this object", o.GetName(), cur.Annotations[AnnotationPivotedFrom]))
		case !maps.Equal(cur.Labels, want.Labels):
			problems = append(problems, fmt.Sprintf("%s: labels differ", o.GetName()))
		case cur.Spec.Description != want.Spec.Description:
			problems = append(problems, fmt.Sprintf("%s: description differs", o.GetName()))
		case !SameParameters(cur.Spec.Parameters, want.Spec.Parameters):
			problems = append(problems, fmt.Sprintf("%s: parameters differ", o.GetName()))
		}
	}
	return problems, nil
}

func same(cur, want *solasv1alpha1.Device) bool {
	return maps.Equal(cur.Labels, want.Labels) &&
		maps.Equal(cur.Annotations, want.Annotations) &&
		cur.Spec.Description == want.Spec.Description &&
		SameParameters(cur.Spec.Parameters, want.Spec.Parameters)
}
