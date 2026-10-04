// Package claim binds DeviceClaims to Devices, spec section 6.
package claim

import (
	"context"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	claimsv1alpha1 "github.com/phoban01/solas/pkg/apis/claims/v1alpha1"
	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/controller/member"
)

// MemberState gives the reconciler the view of this member.
type MemberState interface {
	Status() member.Status
}

// Reconciler binds and releases the DeviceClaims of this cluster.
type Reconciler struct {
	// Client reads claims from the cache and writes claims and devices.
	Client client.Client
	// Reader reads with no cache, for consistent lists of devices.
	Reader    client.Reader
	ClusterID string
	Member    MemberState
	// Resync is how often a claim is checked again, for example D / 3.
	Resync time.Duration
	// Clock measures the grace period of preemption. Nil means the wall clock.
	Clock clock.PassiveClock
	// Rand picks among free devices. Nil means a random seed. The simulator
	// sets it so a run can be replayed.
	Rand *rand.Rand

	mu       sync.Mutex
	programs programs
	rand     *rand.Rand
}

var _ reconcile.Reconciler = &Reconciler{}

// SetupWithManager registers the reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&claimsv1alpha1.DeviceClaim{}).Complete(r)
}

// Reconcile moves one claim toward its phase.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var claim claimsv1alpha1.DeviceClaim
	if err := r.Client.Get(ctx, req.NamespacedName, &claim); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	st := r.Member.Status()

	//= spec/solas.md#6-3-bind
	//# The controller MUST NOT bind a claim that has a deletion timestamp.

	//= spec/solas.md#6-4-release
	//# When a claim has a deletion timestamp, the controller MUST release its
	//# device.
	if !claim.DeletionTimestamp.IsZero() {
		return r.release(ctx, &claim)
	}
	//= spec/solas.md#6-3-bind
	//# Before it binds a claim, the controller MUST add the finalizer
	//# `solas.dev/release` to the claim.
	if !controllerutil.ContainsFinalizer(&claim, claimsv1alpha1.ReleaseFinalizer) {
		controllerutil.AddFinalizer(&claim, claimsv1alpha1.ReleaseFinalizer)
		return reconcile.Result{Requeue: true}, ignoreConflict(r.Client.Update(ctx, &claim))
	}

	switch claim.Status.Phase {
	case "":
		//= spec/solas.md#6-5-phases
		//# A new claim MUST start in phase `Pending`.
		claim.Status.Phase = claimsv1alpha1.ClaimPending
		return reconcile.Result{Requeue: true}, ignoreConflict(r.Client.Status().Update(ctx, &claim))
	case claimsv1alpha1.ClaimLost:
		//= spec/solas.md#6-5-phases
		//# A `Lost` claim MUST NOT bind again, except by a recovery, section 14.5.
		return r.recover(ctx, &claim, st)
	case claimsv1alpha1.ClaimPending:
		return r.bind(ctx, &claim, st)
	default:
		return r.held(ctx, &claim, st)
	}
}

// bind adopts or binds a device for a Pending claim.
func (r *Reconciler) bind(ctx context.Context, claim *claimsv1alpha1.DeviceClaim, st member.Status) (reconcile.Result, error) {
	//= spec/solas.md#6-2-controller
	//# The controller MUST NOT bind while its member is not live by its own
	//# clock, as section 7.4 defines.

	//= spec/solas.md#6-2-controller
	//# The controller MUST NOT bind while its member is draining.

	//= spec/solas.md#7-6-leave
	//# A draining member MUST NOT bind.
	if st.UID == "" || !st.Live || st.Draining {
		return reconcile.Result{RequeueAfter: r.Resync}, nil
	}

	//= spec/solas.md#6-3-bind
	//# Before it picks a device, the controller MUST do a consistent list of
	//# devices.
	devices, err := r.listDevices(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}

	//= spec/solas.md#6-3-bind
	//# If a device in that list has a `claimRef` with the claim UID and the UID
	//# of the member's current `Member`, the controller MUST adopt that device
	//# and MUST NOT bind another.

	//= spec/solas.md#6-3-bind
	//# The controller MUST NOT adopt a device whose `claimRef` has an older
	//# member UID.
	for i := range devices {
		if ref := devices[i].Status.ClaimRef; ref != nil && ref.UID == claim.UID && ref.MemberUID == st.UID {
			return r.adopt(ctx, claim, devices[i].Name, st.UID)
		}
	}

	sel, err := r.newMatcher(claim)
	if err != nil {
		//= spec/solas.md#10-3-selection
		//# If the expression does not compile, the controller MUST NOT bind the
		//# claim and MUST set the condition `SelectorValid` to `False`.
		return reconcile.Result{}, r.setCondition(ctx, claim, "SelectorValid", metav1.ConditionFalse, "Invalid", err.Error())
	}
	//= spec/solas.md#6-3-bind
	//# The controller MUST pick a device from the free devices that match the
	//# selector.
	//= spec/solas.md#14-4-pre-bound-claims
	//# A claim that names a device MUST bind only that device: against an offer
	//# that names the claim, or as a normal bind when the device is free.
	named := claim.Spec.DeviceName
	var free, offered []*solasv1alpha1.Device
	for i := range devices {
		d := &devices[i]
		if named != "" && d.Name != named {
			continue
		}
		if offerFor(d, r.ClusterID, st.UID, claim) {
			offered = append(offered, d)
		}
		if d.Status.ClaimRef == nil && d.DeletionTimestamp.IsZero() && keptFor(d, claim) && sel.Matches(d) {
			free = append(free, d)
		}
	}
	if len(offered) > 0 {
		// A bind against an offer, spec 14.2. The server checks that the
		// offer still names this claim and member UID.
		free = offered[:1]
	}
	if len(free) == 0 {
		if named != "" {
			return reconcile.Result{RequeueAfter: r.Resync}, nil
		}
		return r.requestPreemption(ctx, claim, st, devices, sel)
	}

	//= spec/solas.md#6-3-bind
	//# The controller SHOULD pick at random among the matching devices.
	d := free[r.intN(len(free))].DeepCopy()

	//= spec/solas.md#6-3-bind
	//# The bind MUST be a status update of the device that sets `claimRef`.

	//= spec/solas.md#6-3-bind
	//# The bind MUST carry the resource version from the consistent list.

	//= spec/solas.md#6-3-bind
	//# The `claimRef` MUST hold the UID of the member's current `Member`.

	//= spec/solas.md#7-2-join
	//# The member MUST use the UID of its current `Member` in each `claimRef`
	//# that it writes.
	d.Status.ClaimRef = &solasv1alpha1.ClaimRef{
		Member:    r.ClusterID,
		MemberUID: st.UID,
		Namespace: claim.Namespace,
		Name:      claim.Name,
		UID:       claim.UID,
		Priority:  claim.Spec.Priority,
		Protected: claim.Spec.Protected,
		BoundAt:   ptrTime(r.now()),
	}
	if err := r.Client.Status().Update(ctx, d); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsInvalid(err) {
			//= spec/solas.md#6-3-bind
			//# If the bind fails with `409 Conflict`, the controller MUST list again
			//# and MUST NOT retry with the old version.
			log.FromContext(ctx).V(1).Info("lost the race for a device", "device", d.Name)
			return reconcile.Result{Requeue: true}, nil
		}
		return reconcile.Result{}, err
	}
	log.FromContext(ctx).Info("bound", "device", d.Name, "token", d.Status.FencingToken)
	//= spec/solas.md#6-3-bind
	//# The controller MUST NOT set a claim to `Bound` from a bind that carried
	//# an older member UID.
	//
	// The bind above carried st.UID, and setBound records the same UID.
	return r.setBound(ctx, claim, d, st.UID)
}

//= spec/solas.md#6-3-bind
//# Before it adopts a device, the controller MUST read the device again with
//# a consistent read.

//= spec/solas.md#6-3-bind
//# It MUST adopt the device only if that read still shows the same `claimRef`.

// adopt sets the claim Bound on a device that a list showed naming it. The
// list can be late, so adopt reads the device again first.
func (r *Reconciler) adopt(ctx context.Context, claim *claimsv1alpha1.DeviceClaim, name string, uid types.UID) (reconcile.Result, error) {
	var d solasv1alpha1.Device
	if err := r.Reader.Get(ctx, client.ObjectKey{Name: name}, &d); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{Requeue: true}, nil
		}
		return reconcile.Result{}, err
	}
	if ref := d.Status.ClaimRef; ref == nil || ref.UID != claim.UID || ref.MemberUID != uid {
		return reconcile.Result{Requeue: true}, nil
	}
	return r.setBound(ctx, claim, &d, uid)
}

func (r *Reconciler) setBound(ctx context.Context, claim *claimsv1alpha1.DeviceClaim, d *solasv1alpha1.Device, uid types.UID) (reconcile.Result, error) {
	//= spec/solas.md#6-3-bind
	//# After a successful bind, the controller MUST set the claim phase to
	//# `Bound`.

	//= spec/solas.md#6-3-bind
	//# It MUST also set `status.deviceName` and `status.memberUID`.

	//= spec/solas.md#6-3-bind
	//# It MUST also set `status.fencingToken` to the fencing token of the device.
	claim.Status.Phase = claimsv1alpha1.ClaimBound
	claim.Status.DeviceName = d.Name
	claim.Status.MemberUID = uid
	claim.Status.FencingToken = d.Status.FencingToken
	return reconcile.Result{RequeueAfter: r.Resync}, ignoreConflict(r.Client.Status().Update(ctx, claim))
}

//= spec/solas.md#6-3-bind
//# The controller MUST NOT move a claim to another device while the claim
//# is `Bound`, because a workload can be using the device.

// held handles a Bound or Suspended claim. It never binds a new device.
func (r *Reconciler) held(ctx context.Context, claim *claimsv1alpha1.DeviceClaim, st member.Status) (reconcile.Result, error) {
	if st.UID != "" && claim.Status.MemberUID != st.UID {
		//= spec/solas.md#6-5-phases
		//# When the controller finds that its member UID changed, it MUST set each
		//# claim with the old `status.memberUID` to `Lost`.
		setPhase(claim, claimsv1alpha1.ClaimLost)
		return reconcile.Result{}, ignoreConflict(r.Client.Status().Update(ctx, claim))
	}

	devices, err := r.listDevices(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}
	var own *solasv1alpha1.Device
	for i := range devices {
		d := &devices[i]
		ref := d.Status.ClaimRef
		if ref == nil || ref.UID != claim.UID {
			continue
		}
		if d.Name == claim.Status.DeviceName {
			own = d
			continue
		}
		//= spec/solas.md#6-3-bind
		//# If more than one device has a `claimRef` with the same claim UID, the
		//# controller MUST keep the device in `status.deviceName`.

		//= spec/solas.md#6-3-bind
		//# It MUST release the other devices as section 6.4 describes.
		if err := r.clear(ctx, d); err != nil {
			return reconcile.Result{}, err
		}
	}

	//= spec/solas.md#10-4-lease-display
	//# The controller SHOULD set `DeviceClaim.status.leaseExpiresAt` on each
	//# `Bound` claim to the end of its member's lease, `S + D - M`, as a wall
	//# time by the holder's clock.
	//= spec/solas.md#10-9-stale-requests
	//# The controller MUST clear a request that names its own member and a claim
	//# that does not exist, as it does for a `claimRef`, spec 6.4.
	//
	// A held claim needs no preemption, so it withdraws its requests too.
	if claim.Status.PreemptionTarget != "" {
		if err := r.withdraw(ctx, claim, devices); err != nil {
			return reconcile.Result{}, err
		}
		claim.Status.PreemptionTarget = ""
		return reconcile.Result{Requeue: true}, ignoreConflict(r.Client.Status().Update(ctx, claim))
	}

	if st.Live && (claim.Status.Phase == claimsv1alpha1.ClaimBound || claim.Status.Phase == claimsv1alpha1.ClaimPreempting) {
		if old := claim.Status.LeaseExpiresAt; old == nil || absDuration(old.Time.Sub(st.LeaseEnd)) >= time.Second {
			end := metav1.NewTime(st.LeaseEnd)
			claim.Status.LeaseExpiresAt = &end
			if err := r.Client.Status().Update(ctx, claim); err != nil {
				return reconcile.Result{}, ignoreConflict(err)
			}
		}
	}

	switch claim.Status.Phase {
	case claimsv1alpha1.ClaimBound, claimsv1alpha1.ClaimPreempting:
		//= spec/solas.md#6-5-phases
		//# When the controller's member is not live by its own clock, the
		//# controller MUST set each `Bound` claim to `Suspended`.

		//= spec/solas.md#7-6-leave
		//# The member MUST then set each `Bound` claim to `Suspended`.
		if !st.Live || st.Draining {
			setPhase(claim, claimsv1alpha1.ClaimSuspended)
			claim.Status.PreemptionSeenAt = nil
			return reconcile.Result{Requeue: true}, ignoreConflict(r.Client.Status().Update(ctx, claim))
		}
		if changed, err := r.startTransfer(ctx, claim, own); changed || err != nil {
			return reconcile.Result{Requeue: true}, err
		}
		if changed, err := r.preemptionStep(ctx, claim, own); changed || err != nil {
			return reconcile.Result{Requeue: true}, err
		}
	case claimsv1alpha1.ClaimTransferring:
		return r.transferStep(ctx, claim, own, st)
	case claimsv1alpha1.ClaimPreempted:
		if _, err := r.preemptionStep(ctx, claim, own); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{Requeue: true}, nil
	case claimsv1alpha1.ClaimSuspended:
		if st.Draining && own != nil {
			//= spec/solas.md#7-6-leave
			//# The member MUST then release each device that it holds, as section 6.4
			//# describes.

			//= spec/solas.md#7-6-leave
			//# The member MUST NOT release the device of a claim that is still `Bound`.
			return reconcile.Result{RequeueAfter: r.Resync}, r.clear(ctx, own)
		}
		//= spec/solas.md#6-5-phases
		//# When the member renews its lease, the controller MUST set each
		//# `Suspended` claim back to `Bound` if its device still names it.
		if st.Live && !st.Draining && own != nil && own.Status.ClaimRef.MemberUID == st.UID {
			claim.Status.Phase = claimsv1alpha1.ClaimBound
			return reconcile.Result{RequeueAfter: r.Resync}, ignoreConflict(r.Client.Status().Update(ctx, claim))
		}
	}
	return reconcile.Result{RequeueAfter: r.Resync}, nil
}

// release clears each device of a deleted claim, then removes the
// finalizer, spec 6.4.
func (r *Reconciler) release(ctx context.Context, claim *claimsv1alpha1.DeviceClaim) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(claim, claimsv1alpha1.ReleaseFinalizer) {
		return reconcile.Result{}, nil
	}
	//= spec/solas.md#6-4-release
	//# To release, the controller MUST do a consistent list of devices.
	devices, err := r.listDevices(ctx)
	if err != nil {
		return reconcile.Result{}, err
	}
	for i := range devices {
		//= spec/solas.md#6-4-release
		//# For each device whose `claimRef` has the claim UID, the controller MUST
		//# clear `claimRef` with a status update.

		//= spec/solas.md#6-4-release
		//# The controller MUST NOT change a device whose `claimRef` does not have
		//# the claim UID.
		if ref := devices[i].Status.ClaimRef; ref != nil && ref.UID == claim.UID {
			if err := r.clear(ctx, &devices[i]); err != nil {
				return reconcile.Result{}, err
			}
		}
	}
	if err := r.withdraw(ctx, claim, devices); err != nil {
		return reconcile.Result{}, err
	}
	//= spec/solas.md#6-4-release
	//# After the release, the controller MUST remove the finalizer.
	controllerutil.RemoveFinalizer(claim, claimsv1alpha1.ReleaseFinalizer)
	return reconcile.Result{}, ignoreConflict(r.Client.Update(ctx, claim))
}

// clear removes the claimRef of d with the resource version of the list.
func (r *Reconciler) clear(ctx context.Context, d *solasv1alpha1.Device) error {
	//= spec/solas.md#6-4-release
	//# That update MUST carry the resource version from the list.
	d = d.DeepCopy()
	d.Status.ClaimRef = nil
	err := r.Client.Status().Update(ctx, d)
	if apierrors.IsConflict(err) {
		//= spec/solas.md#6-4-release
		//# If the update fails with `409 Conflict`, the controller MUST list again.
		return errRetry
	}
	return client.IgnoreNotFound(err)
}

// listDevices lists devices with no cache, in name order.
func (r *Reconciler) listDevices(ctx context.Context) ([]solasv1alpha1.Device, error) {
	var list solasv1alpha1.DeviceList
	if err := r.Reader.List(ctx, &list); err != nil {
		return nil, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items, nil
}

func (r *Reconciler) intN(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rand == nil && r.Rand != nil {
		r.rand = r.Rand
	}
	if r.rand == nil {
		r.rand = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}
	return r.rand.IntN(n)
}

func ignoreConflict(err error) error {
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// setCondition sets one condition of the claim and saves the status.
func (r *Reconciler) setCondition(ctx context.Context, claim *claimsv1alpha1.DeviceClaim,
	typ string, status metav1.ConditionStatus, reason, msg string) error {
	if !meta.SetStatusCondition(&claim.Status.Conditions, metav1.Condition{
		Type: typ, Status: status, Reason: reason, Message: msg, ObservedGeneration: claim.Generation,
	}) {
		return nil
	}
	return ignoreConflict(r.Client.Status().Update(ctx, claim))
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func ptrTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}

//= spec/solas.md#10-4-lease-display
//# The controller SHOULD clear it when the claim becomes `Pending`,
//# `Suspended`, or `Lost`.

// setPhase sets the phase of a claim that leaves Bound or Preempting.
func setPhase(claim *claimsv1alpha1.DeviceClaim, p claimsv1alpha1.ClaimPhase) {
	claim.Status.Phase = p
	claim.Status.LeaseExpiresAt = nil
}
