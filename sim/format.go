package sim

import (
	"context"
	"fmt"
	"strings"

	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/format"
)

//= spec/solas.md#11-5-rollback
//# A member MAY roll back to a release whose maximum format is at or above
//# the finalized format.

// setRelease restarts a cluster on a release with the highest format max.
// A release that does not support the finalized format refuses to start,
// spec 11.5, so the cluster stays down.
func (w *World) setRelease(c *cluster, max int32) {
	c.maxFormat = max
	c.restart()
	c.down = max < w.finalized
	state := "started"
	if c.down {
		state = "refused to start"
	}
	w.log("%s release with highest format %d: %s", c.name, max, state)
}

// finalize moves the finalized format to n, as solas finalize does, with
// the same rule, spec 11.4.
func (w *World) finalize(ctx context.Context, n int32) error {
	var list solasv1alpha1.MemberList
	if err := w.shared.List(ctx, &list); err != nil {
		return err
	}
	var ms []format.Member
	for _, m := range list.Items {
		ms = append(ms, format.Member{Name: m.Name, Active: m.Status.Phase == solasv1alpha1.MemberActive, Max: m.Status.MaxFormat})
	}
	if behind := format.Behind(ms, n); len(behind) > 0 {
		w.log("finalize %d: refused, %s", n, strings.Join(behind, ", "))
		return fmt.Errorf("cannot finalize format %d: %s", n, strings.Join(behind, ", "))
	}
	if n < w.finalized {
		return fmt.Errorf("the finalized format only moves up: it is %d", w.finalized)
	}
	w.finalized = n
	w.log("finalize %d: done", n)
	return nil
}
