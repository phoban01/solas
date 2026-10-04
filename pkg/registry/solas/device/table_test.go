package device

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/phoban01/solas/pkg/apis/solas"
)

func TestTableColumns(t *testing.T) {
	free := &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d0"}}
	held := &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1"}, Spec: solas.DeviceSpec{Preemptible: true}}
	held.Status.ClaimRef = &solas.ClaimRef{Member: "a", Namespace: "work", Name: "train", Priority: 1}
	held.Status.FencingToken = 3
	held.Status.Preemption = &solas.PreemptionRequest{Claim: solas.ClaimRef{Member: "b", Namespace: "work", Name: "urgent", Priority: 5}}
	held.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}

	table, err := tableConvertor{}.ConvertToTable(context.Background(), &solas.DeviceList{Items: []solas.Device{*free, *held}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names, wide []string
	for _, c := range table.ColumnDefinitions {
		if c.Priority == 0 {
			names = append(names, c.Name)
		} else {
			wide = append(wide, c.Name)
		}
	}
	// The default columns show no priority and no fencing token.
	if want := []string{"Name", "Ready", "Member", "Claim", "Age"}; !reflect.DeepEqual(names, want) {
		t.Errorf("default columns = %v, want %v", names, want)
	}
	if want := []string{"Preemptible", "Preemption", "Reclaim"}; !reflect.DeepEqual(wide, want) {
		t.Errorf("wide columns = %v, want %v", wide, want)
	}
	cells := func(i int) []any { c := table.Rows[i].Cells; return []any{c[0], c[1], c[2], c[3], c[5], c[6]} }
	if got, want := cells(0), []any{"d0", "Unknown", "<none>", "<none>", false, "<none>"}; !reflect.DeepEqual(got, want) {
		t.Errorf("free row = %v, want %v", got, want)
	}
	if got, want := cells(1), []any{"d1", "True", "a", "work/train", true, "b/work/urgent"}; !reflect.DeepEqual(got, want) {
		t.Errorf("held row = %v, want %v", got, want)
	}
}
