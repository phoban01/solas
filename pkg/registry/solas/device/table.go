package device

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/phoban01/solas/pkg/apis/solas"
)

// tableConvertor prints the columns of kubectl get devices, spec 10.2.
// Columns with Priority 1 show only with -o wide.
type tableConvertor struct{}

// none is what kubectl prints for a value that is not set.
const none = "<none>"

var columns = []metav1.TableColumnDefinition{
	{Name: "Name", Type: "string", Format: "name"},
	{Name: "Ready", Type: "string", Description: "status of the Ready condition"},
	{Name: "Member", Type: "string", Description: "member cluster of the claim that holds the device"},
	{Name: "Claim", Type: "string", Description: "namespace/name of the claim that holds the device"},
	{Name: "Age", Type: "string"},
	{Name: "Preemptible", Type: "boolean", Priority: 1},
	{Name: "Preemption", Type: "string", Priority: 1, Description: "member/namespace/name of the claim that asks for the device"},
	{Name: "Offer", Type: "string", Priority: 1, Description: "member/namespace/name of the claim that the holder offers the device to, spec 14"},
	{Name: "Reclaim", Type: "string", Priority: 1, Description: "reclaim policy, spec 8.5"},
}

func (tableConvertor) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	table := &metav1.Table{ColumnDefinitions: columns}
	var devices []solas.Device
	switch o := obj.(type) {
	case *solas.Device:
		devices = []solas.Device{*o}
	case *solas.DeviceList:
		devices = o.Items
		table.ResourceVersion, table.Continue, table.RemainingItemCount = o.ResourceVersion, o.Continue, o.RemainingItemCount
	default:
		return nil, fmt.Errorf("unexpected object %T", obj)
	}
	for i := range devices {
		d := &devices[i]
		member, claim := none, none
		if r := d.Status.ClaimRef; r != nil {
			member, claim = r.Member, r.Namespace+"/"+r.Name
		}
		preemption := none
		if p := d.Status.Preemption; p != nil {
			preemption = p.Claim.Member + "/" + p.Claim.Namespace + "/" + p.Claim.Name
		}
		offer := none
		if o := d.Status.Offer; o != nil {
			offer = o.Member + "/" + o.Namespace + "/" + o.Name
		}
		ready := "Unknown"
		if c := meta.FindStatusCondition(d.Status.Conditions, "Ready"); c != nil {
			ready = string(c.Status)
		}
		table.Rows = append(table.Rows, metav1.TableRow{
			Cells: []any{d.Name, ready, member, claim,
				duration.HumanDuration(metav1.Now().Sub(d.CreationTimestamp.Time)), d.Spec.Preemptible, preemption, offer, reclaimPolicy(d)},
			Object: runtime.RawExtension{Object: d},
		})
	}
	return table, nil
}

// reclaimPolicy returns the reclaim policy of a device, Delete when unset.
func reclaimPolicy(d *solas.Device) string {
	if d.Spec.ReclaimPolicy == "" {
		return string(solas.ReclaimDelete)
	}
	return string(d.Spec.ReclaimPolicy)
}
