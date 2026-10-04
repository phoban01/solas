package member

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/phoban01/solas/pkg/apis/solas"
)

// tableConvertor prints the columns of kubectl get members.
type tableConvertor struct{}

var columns = []metav1.TableColumnDefinition{
	{Name: "Name", Type: "string", Format: "name"},
	{Name: "Phase", Type: "string"},
	{Name: "Formats", Type: "string", Description: "lowest and highest format that the member supports, spec 11.4"},
	{Name: "Age", Type: "string"},
}

// FormatRange returns the format range of a member. A member with no
// range supports format 1 only.
func FormatRange(m *solas.Member) (lo, hi int32) {
	lo, hi = m.Status.MinFormat, m.Status.MaxFormat
	if lo == 0 {
		lo = 1
	}
	if hi == 0 {
		hi = 1
	}
	return lo, hi
}

func (tableConvertor) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{ColumnDefinitions: columns}
	var members []solas.Member
	switch o := obj.(type) {
	case *solas.Member:
		members = []solas.Member{*o}
	case *solas.MemberList:
		members = o.Items
		table.ResourceVersion, table.Continue, table.RemainingItemCount = o.ResourceVersion, o.Continue, o.RemainingItemCount
	default:
		return nil, fmt.Errorf("unexpected object %T", obj)
	}
	for i := range members {
		m := &members[i]
		lo, hi := FormatRange(m)
		table.Rows = append(table.Rows, metav1.TableRow{
			Cells: []any{m.Name, string(m.Status.Phase), fmt.Sprintf("%d-%d", lo, hi),
				duration.HumanDuration(metav1.Now().Sub(m.CreationTimestamp.Time))},
			Object: runtime.RawExtension{Object: m},
		})
	}
	return table, nil
}
