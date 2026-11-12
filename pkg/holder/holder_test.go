package holder

import (
	"testing"

	"github.com/phoban01/solas/pkg/apis/solas"
)

var ref1 = &solas.ClaimRef{Member: "a", MemberUID: "mu", Namespace: "ns", Name: "c1", UID: "u1"}

func held(ref *solas.ClaimRef, token int64) solas.HolderStatus {
	return solas.HolderStatus{ClaimRef: ref, FencingToken: token}
}

// TestTransitionOffer checks the offer rules of the guard, which hold
// even when a write skips the status strategy, spec 14.
func TestTransitionOffer(t *testing.T) {
	target := *ref1
	target.Member, target.MemberUID, target.Name, target.UID = "m2", "mu2", "c2", "u2"
	h := held(ref1, 4)
	offered := h
	offered.Offer = &target

	bound := offered
	bound.ClaimRef, bound.Offer, bound.FencingToken = &target, nil, 5
	keepOffer := bound
	keepOffer.Offer = &target
	sameToken := bound
	sameToken.FencingToken = 4
	other := *ref1
	other.Name, other.UID = "c3", "u3"
	byOther := bound
	byOther.ClaimRef = &other
	freeWithOffer := offered
	freeWithOffer.ClaimRef = nil
	second := offered
	second.Offer = &other
	withdrawn := offered
	withdrawn.Offer = nil

	for _, c := range []struct {
		name     string
		old, new solas.HolderStatus
		ok       bool
	}{
		{"offer", h, offered, true},
		{"bind against the offer", offered, bound, true},
		{"withdraw", offered, withdrawn, true},
		{"bind that keeps the offer", offered, keepOffer, false},
		{"bind against the offer with the old token", offered, sameToken, false},
		{"bind by a claim that the offer does not name", offered, byOther, false},
		{"release that keeps the offer", offered, freeWithOffer, false},
		{"second offer", offered, second, false},
	} {
		old := c.old
		if err := Transition("x", &old, c.new); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok %v", c.name, err, c.ok)
		}
	}
}

// TestTransitionRecovery checks the shape and the token of a recovery in
// the guard, spec 14.5.
func TestTransitionRecovery(t *testing.T) {
	h := held(ref1, 4)
	newRef := *ref1
	newRef.MemberUID = "mu-new"
	rec := held(&newRef, 5)
	noRaise := held(&newRef, 4)
	otherName := *(&newRef)
	otherName.Member = "b"
	otherClaim := newRef
	otherClaim.UID = "u9"
	for _, c := range []struct {
		name string
		new  solas.HolderStatus
		ok   bool
	}{
		{"recovery", rec, true},
		{"recovery with the old token", noRaise, false},
		{"another member name", held(&otherName, 5), false},
		{"another claim", held(&otherClaim, 5), false},
	} {
		if err := Transition("x", &h, c.new); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok %v", c.name, err, c.ok)
		}
	}
}

// TestTransitionBindAndHold checks the plain rules of spec 5.3: a create
// starts free, a bind takes the next token, a change of holder is
// refused, and every other update keeps the token.
func TestTransitionBindAndHold(t *testing.T) {
	free := held(nil, 0)
	other := *ref1
	other.Name, other.UID = "c2", "u2"
	for _, c := range []struct {
		name string
		old  *solas.HolderStatus
		new  solas.HolderStatus
		ok   bool
	}{
		{"create free", nil, free, true},
		{"create held", nil, held(ref1, 1), false},
		{"create with a token", nil, held(nil, 3), false},
		{"bind", &free, held(ref1, 1), true},
		{"bind with the old token", &free, held(ref1, 0), false},
		{"release keeps the token", ptr(held(ref1, 1)), held(nil, 1), true},
		{"release with a new token", ptr(held(ref1, 1)), held(nil, 2), false},
		{"another holder", ptr(held(ref1, 1)), held(&other, 2), false},
		{"same holder keeps the token", ptr(held(ref1, 1)), held(ref1, 1), true},
	} {
		if err := Transition("x", c.old, c.new); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok %v", c.name, err, c.ok)
		}
	}
}

func ptr(h solas.HolderStatus) *solas.HolderStatus { return &h }

// TestPrepare checks what the server stores: the token, the offer, and
// the preemption request after each kind of status update.
func TestPrepare(t *testing.T) {
	target := *ref1
	target.Member, target.MemberUID, target.Name, target.UID = "m2", "mu2", "c2", "u2"
	offered := held(ref1, 4)
	offered.Offer = &target
	req := &solas.PreemptionRequest{Claim: target}
	kept := held(nil, 4)
	kept.Preemption = req

	for _, c := range []struct {
		name      string
		old, new  solas.HolderStatus
		token     int64
		offer     bool
		preempted bool
	}{
		{"bind of a free object", held(nil, 4), held(ref1, 99), 5, false, false},
		{"release clears the offer", offered, solas.HolderStatus{Offer: &target, FencingToken: 4}, 4, false, false},
		{"bind against the offer", offered, solas.HolderStatus{ClaimRef: &target, Offer: &target}, 5, false, false},
		{"withdraw keeps the token", offered, held(ref1, 4), 4, false, false},
		{"the preemptor binds and clears the request", kept, solas.HolderStatus{ClaimRef: &target, Preemption: req}, 5, false, false},
		{"another claim binds and the request stays", kept, solas.HolderStatus{ClaimRef: ref1, Preemption: req}, 5, false, true},
	} {
		got := Prepare(c.old, c.new)
		if got.FencingToken != c.token || (got.Offer != nil) != c.offer || (got.Preemption != nil) != c.preempted {
			t.Errorf("%s: got token %d, offer %v, preemption %v; want %d, %v, %v", c.name,
				got.FencingToken, got.Offer != nil, got.Preemption != nil, c.token, c.offer, c.preempted)
		}
	}
}
