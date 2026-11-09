// Package audit replays the history of the Devices of a store and checks
// the safety rules of spec 5.3 and 14 on every transition. The model and
// the simulator check these rules on generated runs; this package checks
// them on what a store recorded during a real run, such as the soak.
package audit

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apis/solas/validation"
)

// Violation is one transition that broke a rule.
type Violation struct {
	Device string
	RV     string
	Rule   string
	Detail string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s at version %s: %s: %s", v.Device, v.RV, v.Rule, v.Detail)
}

// Summary counts what the replay saw.
type Summary struct {
	Events     int
	Binds      int
	Releases   int
	OfferBinds int
	Recoveries int
	Deletes    int
	Violations []Violation
}

// Replay holds the last state of each device and the summary so far.
type Replay struct {
	Summary
	last map[string]*solas.Device
}

// New returns an empty replay.
func New() *Replay {
	return &Replay{last: map[string]*solas.Device{}}
}

// Apply feeds the next event of a device, in resource version order.
func (r *Replay) Apply(kind watch.EventType, d *solas.Device) {
	r.Events++
	old, seen := r.last[d.Name]
	if kind == watch.Deleted {
		r.Deletes++
		delete(r.last, d.Name)
		return
	}
	defer func() { r.last[d.Name] = d.DeepCopy() }()
	if !seen {
		// The first state of a device, from a create or from the oldest
		// event that the store still holds.
		return
	}
	r.check(old, d)
}

// check compares one transition with the rules.
func (r *Replay) check(old, d *solas.Device) {
	o, n := old.Status.ClaimRef, d.Status.ClaimRef
	ot, nt := old.Status.FencingToken, d.Status.FencingToken
	fail := func(rule, format string, args ...any) {
		r.Violations = append(r.Violations, Violation{Device: d.Name, RV: d.ResourceVersion, Rule: rule, Detail: fmt.Sprintf(format, args...)})
	}
	raise := func(what string) {
		//= spec/solas.md#5-3-status-updates
		//= type=test
		//# The token of a device never goes down, so a later bind always has a
		//# higher token.
		if nt <= ot {
			fail("token-raise", "%s kept token %d (before %d); a bind must raise it", what, nt, ot)
		}
	}
	keep := func(what string) {
		if nt != ot {
			fail("token-keep", "%s changed the token from %d to %d; only a bind changes it", what, ot, nt)
		}
	}
	switch {
	case o == nil && n != nil:
		r.Binds++
		raise("bind of " + holder(n))
	case o != nil && n == nil:
		r.Releases++
		keep("release of " + holder(o))
	case o != nil && n != nil && !validation.SameIdentity(o, n):
		switch {
		case old.Status.Offer != nil && validation.SameIdentity(old.Status.Offer, n):
			r.OfferBinds++
			raise("bind against the offer by " + holder(n))
		case validation.IsRecovery(d, old):
			r.Recoveries++
			raise("recovery by " + holder(n))
		default:
			//= spec/solas.md#5-3-status-updates
			//= type=test
			//# The server MUST reject a status update that changes `claimRef` from one
			//# holder to a different holder.
			fail("holder-change", "claimRef moved from %s to %s without a release, an offer, or a recovery", holder(o), holder(n))
		}
	default:
		keep("a write that is not a bind")
	}
}

func holder(ref *solas.ClaimRef) string {
	return fmt.Sprintf("%s/%s/%s (member UID %s)", ref.Member, ref.Namespace, ref.Name, ref.MemberUID)
}

// idle is how long ReadStore waits for a next event before it decides
// that the history is complete.
const idle = 5 * time.Second

// ReadStore replays the history that the store holds of the devices
// under prefix, from the oldest event to the current version. It fails
// with the error of the watch when the store has let the oldest events
// go, which on DynamoDB is the event retention.
func (r *Replay) ReadStore(ctx context.Context, s storage.Interface, prefix string) error {
	current, err := s.GetCurrentResourceVersion(ctx)
	if err != nil {
		return fmt.Errorf("read the current version: %w", err)
	}
	w, err := s.Watch(ctx, prefix, storage.ListOptions{ResourceVersion: "1", Recursive: true, Predicate: storage.Everything})
	if err != nil {
		return fmt.Errorf("watch from the first version: %w", err)
	}
	defer w.Stop()
	versioner := storage.APIObjectVersioner{}
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			// On etcd the revision is store-wide, so the last device event
			// can be below the current version. Quiet means complete.
			return nil
		case e, ok := <-w.ResultChan():
			if !ok {
				return nil
			}
			if e.Type == watch.Error {
				if st, ok := e.Object.(*metav1.Status); ok {
					return fmt.Errorf("the watch failed: %w", &apierrors.StatusError{ErrStatus: *st})
				}
				return fmt.Errorf("the watch failed: %v", e.Object)
			}
			d, ok := e.Object.(*solas.Device)
			if !ok {
				return fmt.Errorf("event holds a %T, want a Device", e.Object)
			}
			r.Apply(e.Type, d)
			rv, err := versioner.ObjectResourceVersion(d)
			if err != nil {
				return err
			}
			if rv >= current {
				return nil
			}
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(idle)
		}
	}
}
