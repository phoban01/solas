package claim

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// The reasons of the Events on a claim. Each Event names the device and
// the fencing token, so the history of a claim reads from its Events.
const (
	ReasonBound               = "Bound"
	ReasonResumed             = "Resumed"
	ReasonSuspended           = "Suspended"
	ReasonReleased            = "Released"
	ReasonPreemptionRequested = "PreemptionRequested"
	ReasonPreempted           = "Preempted"
	ReasonOffered             = "Offered"
	ReasonTransferred         = "Transferred"
	ReasonRecovered           = "Recovered"
	ReasonLost                = "Lost"
)

// event records an Event on a claim. The recorder may be nil, as in
// tests that do not look at Events; then it records nothing.
func (r *Reconciler) event(claim runtime.Object, related runtime.Object, warning bool, reason, note string, args ...any) {
	if r.Events == nil {
		return
	}
	kind := corev1.EventTypeNormal
	if warning {
		kind = corev1.EventTypeWarning
	}
	r.Events.Eventf(claim, related, kind, reason, reason, note, args...)
}
