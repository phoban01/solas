package member

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/phoban01/solas/pkg/apis/solas"
)

func TestTableShowsTheFormatRange(t *testing.T) {
	old := solas.Member{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Status: solas.MemberStatus{Phase: solas.MemberActive}}
	upgraded := solas.Member{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Status: solas.MemberStatus{Phase: solas.MemberActive, MinFormat: 1, MaxFormat: 2}}
	table, err := tableConvertor{}.ConvertToTable(context.Background(), &solas.MemberList{Items: []solas.Member{old, upgraded}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := table.Rows[0].Cells[2]; got != "1-1" {
		t.Errorf("member with no range shows %v, want 1-1", got)
	}
	if got := table.Rows[1].Cells[2]; got != "1-2" {
		t.Errorf("upgraded member shows %v, want 1-2", got)
	}
}
