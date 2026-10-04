package device

import (
	"testing"

	"github.com/phoban01/solas/pkg/apis/solas"
)

// TestTransitionOffer checks the offer rules of the storage guard, which
// hold even when a write skips the status strategy, spec 14.
func TestTransitionOffer(t *testing.T) {
	target := *ref1
	target.Member, target.MemberUID, target.Name, target.UID = "m2", "mu2", "c2", "u2"
	held := dev("x", ref1)
	held.Status.FencingToken = 4
	offered := held.DeepCopy()
	offered.Status.Offer = &target

	bound := offered.DeepCopy()
	bound.Status.ClaimRef, bound.Status.Offer, bound.Status.FencingToken = &target, nil, 5
	keepOffer := bound.DeepCopy()
	keepOffer.Status.Offer = &target
	sameToken := bound.DeepCopy()
	sameToken.Status.FencingToken = 4
	other := *ref1
	other.Name, other.UID = "c3", "u3"
	byOther := bound.DeepCopy()
	byOther.Status.ClaimRef = &other
	freeWithOffer := offered.DeepCopy()
	freeWithOffer.Status.ClaimRef = nil
	second := offered.DeepCopy()
	second.Status.Offer = &other
	withdrawn := offered.DeepCopy()
	withdrawn.Status.Offer = nil

	for _, c := range []struct {
		name     string
		old, new *solas.Device
		ok       bool
	}{
		{"offer", held, offered, true},
		{"bind against the offer", offered, bound, true},
		{"withdraw", offered, withdrawn, true},
		{"bind that keeps the offer", offered, keepOffer, false},
		{"bind against the offer with the old token", offered, sameToken, false},
		{"bind by a claim that the offer does not name", offered, byOther, false},
		{"release that keeps the offer", offered, freeWithOffer, false},
		{"second offer", offered, second, false},
	} {
		if err := Transition(c.old, c.new); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok %v", c.name, err, c.ok)
		}
	}
}

// TestTransitionRecovery checks the shape and the token of a recovery in
// the storage guard, spec 14.5.
func TestTransitionRecovery(t *testing.T) {
	held := dev("x", ref1)
	held.Status.FencingToken = 4
	rec := held.DeepCopy()
	newRef := *ref1
	newRef.MemberUID = "mu-new"
	rec.Status.ClaimRef, rec.Status.FencingToken = &newRef, 5
	noRaise := rec.DeepCopy()
	noRaise.Status.FencingToken = 4
	otherName := rec.DeepCopy()
	otherName.Status.ClaimRef.Member = "b"
	otherClaim := rec.DeepCopy()
	otherClaim.Status.ClaimRef.UID = "u9"
	for _, c := range []struct {
		name string
		new  *solas.Device
		ok   bool
	}{
		{"recovery", rec, true},
		{"recovery with the old token", noRaise, false},
		{"another member name", otherName, false},
		{"another claim", otherClaim, false},
	} {
		if err := Transition(held, c.new); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok %v", c.name, err, c.ok)
		}
	}
}
