package audit

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/phoban01/solas/pkg/apis/solas"
)

func ref(member, claim, uid string) *solas.ClaimRef {
	return &solas.ClaimRef{Member: member, MemberUID: types.UID("uid-" + member + "-" + uid), Namespace: "ns", Name: claim, UID: types.UID("claim-" + claim)}
}

func state(rv string, token int64, holder, offer *solas.ClaimRef) *solas.Device {
	d := &solas.Device{ObjectMeta: metav1.ObjectMeta{Name: "d1", ResourceVersion: rv}}
	d.Status.FencingToken = token
	d.Status.ClaimRef = holder
	d.Status.Offer = offer
	return d
}

// TestReplayAcceptsTheRules: a bind that raises the token, a release that
// keeps it, a bind against an offer, and a recovery pass.
func TestReplayAcceptsTheRules(t *testing.T) {
	a, b := ref("a", "c1", "1"), ref("b", "c2", "1")
	r := New()
	r.Apply(watch.Added, state("2", 0, nil, nil))
	r.Apply(watch.Modified, state("3", 1, a, nil))
	r.Apply(watch.Modified, state("4", 1, nil, nil))
	r.Apply(watch.Modified, state("5", 2, a, nil))
	r.Apply(watch.Modified, state("6", 2, a, b))
	r.Apply(watch.Modified, state("7", 3, b, nil))
	r.Apply(watch.Modified, state("8", 4, ref("b", "c2", "2"), nil))
	r.Apply(watch.Deleted, state("9", 4, nil, nil))
	if len(r.Violations) != 0 {
		t.Fatalf("violations: %v", r.Violations)
	}
	want := Summary{Events: 8, Binds: 2, Releases: 1, OfferBinds: 1, Recoveries: 1, Deletes: 1}
	if got := r.Summary; got.Events != want.Events || got.Binds != want.Binds || got.Releases != want.Releases ||
		got.OfferBinds != want.OfferBinds || got.Recoveries != want.Recoveries || got.Deletes != want.Deletes {
		t.Errorf("summary = %+v, want %+v", got, want)
	}
}

// TestReplayFindsViolations: a bind without a token raise, a release that
// changes the token, and a holder change without an offer are reported.
func TestReplayFindsViolations(t *testing.T) {
	a, b := ref("a", "c1", "1"), ref("b", "c2", "1")
	r := New()
	r.Apply(watch.Added, state("2", 0, nil, nil))
	r.Apply(watch.Modified, state("3", 0, a, nil))
	r.Apply(watch.Modified, state("4", 1, nil, nil))
	r.Apply(watch.Modified, state("5", 2, a, nil))
	r.Apply(watch.Modified, state("6", 3, b, nil))
	rules := map[string]int{}
	for _, v := range r.Violations {
		rules[v.Rule]++
	}
	if rules["token-raise"] != 1 || rules["token-keep"] != 1 || rules["holder-change"] != 1 || len(r.Violations) != 3 {
		t.Errorf("violations = %v, want one token-raise, one token-keep, one holder-change", r.Violations)
	}
}
