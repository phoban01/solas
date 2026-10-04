// Package member keeps the Member of this cluster: join, renew, the lease
// as the holder sees it, and graceful leave. Spec section 7.
package member

import (
	"context"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/format"
)

// Status is the view the rest of the controller has of this member.
type Status struct {
	// UID is the UID of the current Member, or empty when not joined.
	UID types.UID
	// Live is true while the lease is valid by this member's own clock.
	Live bool
	// Draining is true once the Member phase is Draining.
	Draining bool
	// Left is true after a graceful leave.
	Left bool
	// LeaseEnd is when the lease ends by this member's clock, S + D - M.
	LeaseEnd time.Time
}

// Manager keeps the Member of this cluster.
type Manager struct {
	// Client writes the Member. Reader reads it with no cache.
	Client client.Client
	Reader client.Reader

	ClusterID string
	Lease     time.Duration
	Margin    time.Duration
	Clock     clock.PassiveClock
	// MaxFormat is the highest format that this member reports. Zero means
	// the maximum of this release; the simulator sets it to run releases
	// side by side, spec 11.
	MaxFormat int32

	// Drained reports whether no device still names this member UID. The
	// manager deletes its Member on leave only after that, spec 7.6.
	Drained func(ctx context.Context, uid types.UID) (bool, error)

	// MarkLost sets each held claim with a member UID other than uid to Lost.
	// The manager calls it after a join and before it counts itself live,
	// spec 7.2. If it fails, the manager stays not live and tries again.
	MarkLost func(ctx context.Context, uid types.UID) error

	// BeforeRenew handles the preemption requests on the devices of this
	// member. The manager calls it before each renew and does not renew when
	// it fails, spec 10.8.
	BeforeRenew func(ctx context.Context, uid types.UID) error

	mu       sync.Mutex
	member   *solasv1alpha1.Member
	sentAt   time.Time
	draining bool
	left     bool
}

// Status returns the current view of this member.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.member == nil || m.left {
		return Status{Left: m.left}
	}
	return Status{
		UID: m.member.UID,
		//= spec/solas.md#7-4-lease-on-the-holder
		//# The member MUST treat itself as not live from local time `S + D - M`.
		Live:     m.Clock.Since(m.sentAt) < m.Lease-m.Margin,
		Draining: m.draining,
		LeaseEnd: m.sentAt.Add(m.Lease - m.Margin),
	}
}

// Start renews the lease every D / 3 until ctx ends.
func (m *Manager) Start(ctx context.Context) error {
	//= spec/solas.md#7-3-renew
	//# A member SHOULD renew every `D / 3`.
	t := time.NewTicker(m.Lease / 3)
	defer t.Stop()
	for {
		if err := m.Tick(ctx); err != nil {
			log.FromContext(ctx).Error(err, "member tick failed")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick does one step: join, renew, or finish a leave.
func (m *Manager) Tick(ctx context.Context) error {
	m.mu.Lock()
	joined, left, draining := m.member != nil, m.left, m.draining
	m.mu.Unlock()
	switch {
	case left:
		return nil
	case !joined:
		return m.join(ctx)
	case draining:
		done, err := m.finishLeave(ctx)
		if err != nil || done {
			return err
		}
		return m.renew(ctx)
	default:
		return m.renew(ctx)
	}
}

//= spec/solas.md#7-2-join
//# When the controller starts, it MUST try to renew its existing `Member`.

// join renews an existing Member or creates a new one.
func (m *Manager) join(ctx context.Context) error {
	send := m.Clock.Now()
	var mem solasv1alpha1.Member
	err := m.Reader.Get(ctx, client.ObjectKey{Name: m.ClusterID}, &mem)
	switch {
	case err == nil:
		//= spec/solas.md#7-2-join
		//# If the renew succeeds, the controller MUST keep the UID of that `Member`.
		mem.Status.RenewTime = ptrMicro(send)
		m.reportFormats(&mem.Status)
		if err := m.Client.Status().Update(ctx, &mem); err != nil {
			return client.IgnoreNotFound(ignoreConflict(err))
		}
		if err := m.markLost(ctx, mem.UID); err != nil {
			return err
		}
		m.set(&mem, send)
		return nil
	case apierrors.IsNotFound(err):
		//= spec/solas.md#7-2-join
		//# If the `Member` does not exist, the controller MUST create a new one.

		//= spec/solas.md#7-2-join
		//# A member cluster MUST create its `Member` before it binds any device.
		mem = solasv1alpha1.Member{
			ObjectMeta: metav1.ObjectMeta{Name: m.ClusterID},
			Spec:       solasv1alpha1.MemberSpec{LeaseDurationSeconds: int32(m.Lease / time.Second)},
			Status: solasv1alpha1.MemberStatus{
				Phase:     solasv1alpha1.MemberActive,
				RenewTime: ptrMicro(send),
				MinFormat: format.Min,
				MaxFormat: m.maxFormat(),
			},
		}
		if err := m.Client.Create(ctx, &mem); err != nil {
			return ignoreConflict(ignoreExists(err))
		}
		log.FromContext(ctx).Info("joined", "member", m.ClusterID, "uid", mem.UID)
		if err := m.markLost(ctx, mem.UID); err != nil {
			return err
		}
		m.set(&mem, send)
		return nil
	default:
		return err
	}
}

//= spec/solas.md#7-3-renew
//# A renew MUST be an update of `Member.status.renewTime`.

//= spec/solas.md#7-3-renew
//# The renew MUST carry the resource version that the member last read or
//# wrote.

// renew updates the renew time on condition of the last seen version.
func (m *Manager) renew(ctx context.Context) error {
	m.mu.Lock()
	mem := m.member.DeepCopy()
	m.mu.Unlock()

	//= spec/solas.md#10-8-holders-that-do-not-respond
	//# Before each renew, a member MUST handle each preemption request on the
	//# devices it holds, as section 10.6 describes.

	//= spec/solas.md#10-8-holders-that-do-not-respond
	//# If it cannot, it MUST NOT renew.
	if m.BeforeRenew != nil {
		if err := m.BeforeRenew(ctx, mem.UID); err != nil {
			return fmt.Errorf("handle preemption requests before the renew: %w", err)
		}
	}
	send := m.Clock.Now()
	mem.Status.RenewTime = ptrMicro(send)
	m.reportFormats(&mem.Status)
	err := m.Client.Status().Update(ctx, mem)
	switch {
	case err == nil:
		m.set(mem, send)
		return nil
	case apierrors.IsNotFound(err):
		//= spec/solas.md#7-3-renew
		//# After a `404 NotFound`, the member MUST join again as section 7.2
		//# describes.
		log.FromContext(ctx).Info("member was deleted by a sweeper; joining again", "old-uid", mem.UID)
		m.mu.Lock()
		m.member = nil
		m.mu.Unlock()
		return nil
	case apierrors.IsConflict(err):
		//= spec/solas.md#7-6-leave
		//# To leave, the member MUST first set its phase to `Draining`.
		//
		// An operator sets the phase; the member reads it here.
		// Another client changed the Member, for example to start a leave.
		// Read it again; the lease does not move.
		var cur solasv1alpha1.Member
		if err := m.Reader.Get(ctx, client.ObjectKey{Name: m.ClusterID}, &cur); err != nil {
			if apierrors.IsNotFound(err) {
				m.mu.Lock()
				m.member = nil
				m.mu.Unlock()
				return nil
			}
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if cur.UID != m.member.UID {
			m.member = nil
			return nil
		}
		m.member = &cur
		m.draining = cur.Status.Phase == solasv1alpha1.MemberDraining
		return nil
	default:
		return err
	}
}

// finishLeave deletes the Member once no device names it, spec 7.6.
func (m *Manager) finishLeave(ctx context.Context) (bool, error) {
	m.mu.Lock()
	mem := m.member.DeepCopy()
	m.mu.Unlock()
	if m.Drained == nil {
		return false, nil
	}
	drained, err := m.Drained(ctx, mem.UID)
	if err != nil || !drained {
		return false, err
	}
	//= spec/solas.md#7-6-leave
	//# The member MUST then delete its `Member`.
	err = m.Client.Delete(ctx, mem, client.Preconditions{UID: &mem.UID, ResourceVersion: &mem.ResourceVersion})
	if err != nil && !apierrors.IsNotFound(err) {
		return false, ignoreConflict(err)
	}
	log.FromContext(ctx).Info("left the mesh", "member", m.ClusterID)
	m.mu.Lock()
	m.left = true
	m.mu.Unlock()
	return true, nil
}

func (m *Manager) set(mem *solasv1alpha1.Member, send time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.member = mem
	//= spec/solas.md#7-4-lease-on-the-holder
	//# Let `S` be the local time at which the member sent its last successful
	//# renew.
	m.sentAt = send

	//= spec/solas.md#7-4-lease-on-the-holder
	//# The member MUST treat itself as live again only after a later renew
	//# succeeds.
	m.draining = mem.Status.Phase == solasv1alpha1.MemberDraining
}

func ptrMicro(t time.Time) *metav1.MicroTime {
	mt := metav1.NewMicroTime(t)
	return &mt
}

func ignoreConflict(err error) error {
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}

func ignoreExists(err error) error {
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

//= spec/solas.md#7-2-join
//# After a join, the controller MUST set each claim with another
//# `status.memberUID` to `Lost` before it treats itself as live.

// markLost runs the MarkLost hook, when there is one.
func (m *Manager) markLost(ctx context.Context, uid types.UID) error {
	if m.MarkLost == nil {
		return nil
	}
	return m.MarkLost(ctx, uid)
}

//= spec/solas.md#11-4-finalization
//# Each member MUST report the lowest and the highest format that it
//# supports in its `Member` status.

// reportFormats sets the format range of this release. A member reports it
// when it joins and with each renew, so an upgraded member updates its
// range with its first renew.
func (m *Manager) reportFormats(st *solasv1alpha1.MemberStatus) {
	st.MinFormat, st.MaxFormat = format.Min, m.maxFormat()
}

func (m *Manager) maxFormat() int32 {
	if m.MaxFormat != 0 {
		return m.MaxFormat
	}
	return format.Max
}
