package holder_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/internal/ddbtest"
	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/format"
	"github.com/phoban01/solas/pkg/holder"
	"github.com/phoban01/solas/pkg/storage/dynamo"
	"github.com/phoban01/solas/pkg/storage/guard"
)

// Widget is a second kind with a holder, for this test alone: a type
// with the holder fields in its status that implements solas.Held, and
// nothing else. It goes through the real store and the guard with no
// change in pkg/storage, which is the proof that the holder is a
// primitive and not a part of Device.
type Widget struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	Status WidgetStatus
}

type WidgetStatus struct {
	solas.HolderStatus
	Note string
}

type WidgetList struct {
	metav1.TypeMeta
	metav1.ListMeta
	Items []Widget
}

func (w *Widget) HolderStatus() solas.HolderStatus     { return w.Status.HolderStatus }
func (w *Widget) SetHolderStatus(h solas.HolderStatus) { w.Status.HolderStatus = h }
func (w *Widget) DeepCopyObject() runtime.Object {
	c := *w
	c.ObjectMeta = *w.ObjectMeta.DeepCopy()
	c.Status.HolderStatus = *w.Status.HolderStatus.DeepCopy()
	return &c
}
func (l *WidgetList) DeepCopyObject() runtime.Object {
	c := *l
	c.Items = append([]Widget(nil), l.Items...)
	return &c
}

var _ solas.Held = &Widget{}

var widgetGV = schema.GroupVersion{Group: "test.solas.dev", Version: "v1"}

//= spec/solas.md#5-3-status-updates
//= type=test
//# The rules of this section, of section 6.6, and of section 14 MUST hold
//# for every object of a kind with a holder.

// TestSecondHeldKind runs a Widget through the DynamoDB store behind the
// holder guard: a create starts free, a bind takes the next token, a
// change of holder is refused, and a release keeps the token. The store
// and the guard know nothing of the kind.
func TestSecondHeldKind(t *testing.T) {
	ctx := context.Background()
	client := ddbtest.Client(t)
	table := ddbtest.TableName(t)
	ddbtest.DeleteTable(t, client, table)
	if err := dynamo.EnsureTable(ctx, client, table); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(widgetGV, &Widget{}, &WidgetList{})
	metav1.AddToGroupVersion(scheme, widgetGV)
	codec := serializer.NewCodecFactory(scheme).LegacyCodec(widgetGV)
	raw, err := dynamo.New(dynamo.Config{Client: client, Table: table, PollInterval: 20 * time.Millisecond}, codec,
		func() runtime.Object { return &Widget{} }, func() runtime.Object { return &WidgetList{} },
		"/registry", "/widgets/", schema.GroupResource{Group: widgetGV.Group, Resource: "widgets"})
	if err != nil {
		t.Fatal(err)
	}
	format.SetFinalized(1)
	format.SetEpoch(0)
	s := guard.Wrap(raw, holder.Guard)
	ref := &solas.ClaimRef{Member: "a", MemberUID: "mu", Namespace: "ns", Name: "c1", UID: "u1"}
	key := "/widgets/w1"

	held := &Widget{ObjectMeta: metav1.ObjectMeta{Name: "w1"}}
	held.SetHolderStatus(solas.HolderStatus{ClaimRef: ref, FencingToken: 1})
	if err := s.Create(ctx, key, held, &Widget{}, 0); !guard.IsRejected(err) {
		t.Fatalf("create of a held widget = %v, want the guard to reject it", err)
	}
	if err := s.Create(ctx, key, &Widget{ObjectMeta: metav1.ObjectMeta{Name: "w1"}}, &Widget{}, 0); err != nil {
		t.Fatal(err)
	}

	// write updates the widget as a status strategy would: the client's
	// change, then holder.Prepare for the fields the server sets.
	write := func(change func(h *solas.HolderStatus), prepare bool) error {
		return s.GuaranteedUpdate(ctx, key, &Widget{}, false, nil, func(in runtime.Object, _ storage.ResponseMeta) (runtime.Object, *uint64, error) {
			w := in.(*Widget).DeepCopyObject().(*Widget)
			old := w.HolderStatus()
			h := old
			change(&h)
			if prepare {
				h = holder.Prepare(old, h)
			}
			w.SetHolderStatus(h)
			return w, nil, nil
		}, nil)
	}
	if err := write(func(h *solas.HolderStatus) { h.ClaimRef = ref }, true); err != nil {
		t.Fatalf("bind: %v", err)
	}
	got := &Widget{}
	if err := s.Get(ctx, key, storage.GetOptions{}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.FencingToken != 1 || got.Status.ClaimRef == nil {
		t.Fatalf("after the bind: token %d, ref %v; want 1 and the ref", got.Status.FencingToken, got.Status.ClaimRef)
	}
	other := *ref
	other.Name, other.UID = "c2", "u2"
	if err := write(func(h *solas.HolderStatus) { h.ClaimRef = &other }, true); !guard.IsRejected(err) {
		t.Errorf("change of holder = %v, want the guard to reject it", err)
	}
	if err := write(func(h *solas.HolderStatus) { h.FencingToken = 9 }, false); !guard.IsRejected(err) {
		t.Errorf("a token a client sets = %v, want the guard to reject it", err)
	}
	if err := write(func(h *solas.HolderStatus) { h.ClaimRef = nil }, true); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := s.Get(ctx, key, storage.GetOptions{}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.FencingToken != 1 || got.Status.ClaimRef != nil {
		t.Errorf("after the release: token %d, ref %v; want 1 and no ref", got.Status.FencingToken, got.Status.ClaimRef)
	}
	if err := write(func(h *solas.HolderStatus) { h.ClaimRef = &other }, true); err != nil {
		t.Fatalf("second bind: %v", err)
	}
	if err := s.Get(ctx, key, storage.GetOptions{}, got); err != nil || got.Status.FencingToken != 2 {
		t.Errorf("after the second bind: token %d, %v; want 2", got.Status.FencingToken, err)
	}
}
