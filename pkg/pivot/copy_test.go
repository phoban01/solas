package pivot

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/phoban01/solas/internal/fakekube"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/storage/guard"
)

func newPivot(t *testing.T, old []runtime.Object, devices ...client.Object) *Pivot {
	t.Helper()
	src := testSource(t)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{src.Resource: "DeviceList"}, old...)
	return &Pivot{Old: dyn, Solas: fakekube.NewClient(devices...), Source: src}
}

func device(t *testing.T, p *Pivot, name string) *solasv1alpha1.Device {
	t.Helper()
	var d solasv1alpha1.Device
	if err := p.Solas.Get(context.Background(), client.ObjectKey{Name: name}, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

//= spec/solas.md#9-2-copy
//= type=test
//# Running the copy twice MUST give the same result as running it once.

//= spec/solas.md#9-2-copy
//= type=test
//# The copy MUST NOT change a Device whose `solas.dev/pivoted-from` is not
//# set or names another object.

func TestCopy(t *testing.T) {
	ctx := context.Background()
	other := &solasv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: "gpu-3", Labels: map[string]string{"x": "y"}}}
	p := newPivot(t, []runtime.Object{
		oldDevice("gpu-1", "u1", "first"),
		oldDevice("gpu-2", "u2", "second"),
		oldDevice("gpu-3", "u3", "clash"),
	}, other)

	r, err := p.Copy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Created) != 2 || len(r.Conflicts) != 1 || r.Conflicts[0] != "gpu-3" {
		t.Fatalf("first copy: %+v", r)
	}
	if d := device(t, p, "gpu-1"); d.Spec.Description != "first" {
		t.Errorf("gpu-1 description = %q", d.Spec.Description)
	}
	if d := device(t, p, "gpu-3"); d.Labels["x"] != "y" || d.Annotations[AnnotationPivotedFrom] != "" {
		t.Error("copy changed the Device of a conflict")
	}
	old, err := p.old().Get(ctx, "gpu-1", metav1.GetOptions{})
	if err != nil || old.GetAnnotations()[AnnotationPivotedTo] != "solas.dev/devices/gpu-1" {
		t.Errorf("old gpu-1 not annotated: %v %v", old.GetAnnotations(), err)
	}

	r, err = p.Copy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Created) != 0 || len(r.Updated) != 0 || len(r.Unchanged) != 2 {
		t.Errorf("second copy changed something: %+v", r)
	}

	// A change to an old object reaches its Device on the next copy.
	o, _ := p.old().Get(ctx, "gpu-2", metav1.GetOptions{})
	o.Object["spec"] = map[string]any{"description": "changed"}
	if _, err := p.old().Update(ctx, o, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if r, err = p.Copy(ctx); err != nil || len(r.Updated) != 1 {
		t.Fatalf("third copy: %+v, %v", r, err)
	}
	if d := device(t, p, "gpu-2"); d.Spec.Description != "changed" {
		t.Errorf("gpu-2 description = %q, want changed", d.Spec.Description)
	}
}

//= spec/solas.md#9-2-copy
//= type=test
//# With `--dry-run`, the copy MUST NOT write.

func TestCopyDryRun(t *testing.T) {
	ctx := context.Background()
	p := newPivot(t, []runtime.Object{oldDevice("gpu-1", "u1", "first")})
	p.DryRun = true
	r, err := p.Copy(ctx)
	if err != nil || len(r.Created) != 1 {
		t.Fatalf("dry run: %+v, %v", r, err)
	}
	var list solasv1alpha1.DeviceList
	if err := p.Solas.List(ctx, &list); err != nil || len(list.Items) != 0 {
		t.Errorf("dry run wrote %d Devices, %v", len(list.Items), err)
	}
	old, _ := p.old().Get(ctx, "gpu-1", metav1.GetOptions{})
	if _, ok := old.GetAnnotations()[AnnotationPivotedTo]; ok {
		t.Error("dry run annotated the old object")
	}
}

//= spec/solas.md#9-3-verify
//= type=test
//# It MUST check that the Device names the old object, and that the labels
//# and the description match.

func TestVerify(t *testing.T) {
	ctx := context.Background()
	p := newPivot(t, []runtime.Object{oldDevice("gpu-1", "u1", "first"), oldDevice("gpu-2", "u2", "second")})
	if problems, err := p.Verify(ctx); err != nil || len(problems) != 2 {
		t.Fatalf("before copy: %v, %v; want 2 problems", problems, err)
	}
	if _, err := p.Copy(ctx); err != nil {
		t.Fatal(err)
	}
	if problems, err := p.Verify(ctx); err != nil || len(problems) != 0 {
		t.Fatalf("after copy: %v, %v; want none", problems, err)
	}
	d := device(t, p, "gpu-2")
	d.Spec.Description = "edited in solas"
	if err := p.Solas.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	if problems, _ := p.Verify(ctx); len(problems) != 1 {
		t.Errorf("after an edit: %v; want 1 problem", problems)
	}
}

//= spec/solas.md#9-3-verify
//= type=test
//# It MUST check that `spec.parameters` holds the `spec` of the old object.

func TestVerifyParameters(t *testing.T) {
	ctx := context.Background()
	p := newPivot(t, []runtime.Object{oldDevice("gpu-1", "u1", "first")})
	if _, err := p.Copy(ctx); err != nil {
		t.Fatal(err)
	}
	d := device(t, p, "gpu-1")
	d.Spec.Parameters = &runtime.RawExtension{Raw: []byte(`{"description":"first","extra":true}`)}
	if err := p.Solas.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	if problems, _ := p.Verify(ctx); len(problems) != 1 || !strings.Contains(problems[0], "parameters differ") {
		t.Errorf("after an edit of the parameters: %v; want 1 problem", problems)
	}
	if _, err := p.Copy(ctx); err != nil {
		t.Fatal(err)
	}
	if problems, _ := p.Verify(ctx); len(problems) != 0 {
		t.Errorf("after a second copy: %v; want none", problems)
	}
}

// TestCopyKeepsFormat: the storage guard stamps solas.dev/format on each
// write. A second copy keeps it and leaves the Device unchanged.
func TestCopyKeepsFormat(t *testing.T) {
	ctx := context.Background()
	p := newPivot(t, []runtime.Object{oldDevice("gpu-1", "u1", "first")})
	if _, err := p.Copy(ctx); err != nil {
		t.Fatal(err)
	}
	d := device(t, p, "gpu-1")
	d.Annotations[guard.FormatAnnotation] = "1"
	if err := p.Solas.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	r, err := p.Copy(ctx)
	if err != nil || len(r.Unchanged) != 1 {
		t.Fatalf("second copy: %+v, %v; want gpu-1 unchanged", r, err)
	}
}
