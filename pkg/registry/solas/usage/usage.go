// Package usage keeps the usage set of each limited member, spec 15.2,
// at the storage level.
package usage

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/storage"

	"github.com/phoban01/solas/pkg/apis/solas"
)

// Resource is the storage resource of the usage sets.
var Resource = solas.Resource("memberusages")

// Prefix is the storage key prefix of the usage sets.
const Prefix = "/solas.dev/memberusages"

// Store reads and writes usage sets.
type Store struct {
	s   storage.Interface
	now func() time.Time
}

// New returns a usage store on s, the raw storage of the usage sets.
func New(s storage.Interface) *Store { return &Store{s: s, now: time.Now} }

func key(member string) string { return Prefix + "/" + member }

// update changes the usage set of member with a conditional write, and
// creates it when it does not exist.
func (u *Store) update(ctx context.Context, member string, change func(*solas.MemberUsage) error) error {
	return u.s.GuaranteedUpdate(ctx, key(member), &solas.MemberUsage{}, true, nil,
		func(in runtime.Object, _ storage.ResponseMeta) (runtime.Object, *uint64, error) {
			mu := in.(*solas.MemberUsage).DeepCopy()
			mu.Name = member
			if err := change(mu); err != nil {
				return nil, nil, err
			}
			return mu, nil, nil
		}, nil)
}

//= spec/solas.md#15-2-usage-set
//# Before the device write of a bind, the server MUST add the device to the
//# usage set with a conditional write, on condition that the set holds
//# fewer devices than `maxDevices`.

//= spec/solas.md#15-2-usage-set
//# A bind over the limit MUST fail with `403 Forbidden`.

// Reserve adds device to the usage set of member, on condition that the
// set holds fewer than max devices. A device already in the set passes.
func (u *Store) Reserve(ctx context.Context, member, device string, max int32) error {
	return u.update(ctx, member, func(mu *solas.MemberUsage) error {
		for _, e := range mu.Entries {
			if e.Device == device {
				return nil
			}
		}
		if int32(len(mu.Entries)) >= max {
			return apierrors.NewForbidden(solas.Resource("devices"), device,
				fmt.Errorf("member %s holds or is binding %d devices, and its limit is %d", member, len(mu.Entries), max))
		}
		mu.Entries = append(mu.Entries, solas.UsageEntry{Device: device, Added: metav1.NewTime(u.now())})
		return nil
	})
}

//= spec/solas.md#15-3-cleanup
//# A clear of a device MUST remove it from the usage set of its member.

// Remove takes device out of the usage set of member.
func (u *Store) Remove(ctx context.Context, member, device string) error {
	return u.RemoveIf(ctx, member, device, func(solas.UsageEntry) bool { return true })
}

// RemoveIf takes device out of the usage set of member when keep says so
// for its entry, in the same conditional write.
func (u *Store) RemoveIf(ctx context.Context, member, device string, ok func(solas.UsageEntry) bool) error {
	return u.update(ctx, member, func(mu *solas.MemberUsage) error {
		out := mu.Entries[:0]
		for _, e := range mu.Entries {
			if e.Device == device && ok(e) {
				continue
			}
			out = append(out, e)
		}
		mu.Entries = out
		return nil
	})
}

// Entries returns the usage set of member.
func (u *Store) Entries(ctx context.Context, member string) ([]solas.UsageEntry, error) {
	var mu solas.MemberUsage
	err := u.s.Get(ctx, key(member), storage.GetOptions{IgnoreNotFound: true}, &mu)
	return mu.Entries, err
}

// DefaultGrace is how long an entry with no held device stays: longer
// than the deadline of a request, spec 15.3.
const DefaultGrace = 2 * time.Minute

//= spec/solas.md#15-3-cleanup
//# The member's own server MAY remove an entry whose device the member does
//# not hold.

//= spec/solas.md#15-3-cleanup
//# It MUST NOT remove such an entry before a grace that is longer than the
//# deadline of a request has passed since the entry was added, by its own
//# clock.

// Cleanup removes each entry of member whose device the member does not
// hold, once the entry is older than grace by now. devices is the raw
// storage of the devices. It returns the devices it removed.
func (u *Store) Cleanup(ctx context.Context, devices storage.Interface, member string, grace time.Duration, now time.Time) ([]string, error) {
	entries, err := u.Entries(ctx, member)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	var list solas.DeviceList
	if err := devices.GetList(ctx, "/solas.dev/devices", storage.ListOptions{Recursive: true, Predicate: storage.Everything}, &list); err != nil {
		return nil, err
	}
	held := map[string]bool{}
	for _, d := range list.Items {
		if r := d.Status.ClaimRef; r != nil && r.Member == member {
			held[d.Name] = true
		}
	}
	var removed []string
	for _, e := range entries {
		if held[e.Device] || now.Sub(e.Added.Time) < grace {
			continue
		}
		added := e.Added
		if err := u.RemoveIf(ctx, member, e.Device, func(cur solas.UsageEntry) bool { return cur.Added.Equal(&added) }); err != nil {
			return removed, err
		}
		removed = append(removed, e.Device)
	}
	return removed, nil
}

// The kinds of rate, spec 15.2.
const (
	Binds       = "binds"
	Preemptions = "preemptions"
)

//= spec/solas.md#15-2-usage-set
//# A bind or a preemption request over a rate MUST fail with
//# `429 Too Many Requests`.

// take takes one token from the bucket kind of mu, which holds perMinute
// tokens and fills at perMinute a minute. It fails with 429 when the
// bucket is empty, with the seconds until the next token.
func take(mu *solas.MemberUsage, kind string, perMinute int32, now time.Time) error {
	const one = 1000
	capacity := int64(perMinute) * one
	i := -1
	for j := range mu.Buckets {
		if mu.Buckets[j].Kind == kind {
			i = j
		}
	}
	if i < 0 {
		mu.Buckets = append(mu.Buckets, solas.Bucket{Kind: kind, Tokens: capacity, Last: metav1.NewTime(now)})
		i = len(mu.Buckets) - 1
	}
	b := &mu.Buckets[i]
	if elapsed := now.Sub(b.Last.Time); elapsed > 0 {
		b.Tokens = min(capacity, b.Tokens+int64(elapsed)*int64(perMinute)*one/int64(time.Minute))
		b.Last = metav1.NewTime(now)
	}
	if b.Tokens < one || perMinute == 0 {
		wait := 60
		if perMinute > 0 {
			wait = int((one - b.Tokens) * 60 / (int64(perMinute) * one))
		}
		return apierrors.NewTooManyRequests(fmt.Sprintf("member %s is over its rate of %d %s a minute", mu.Name, perMinute, kind), max(wait, 1))
	}
	b.Tokens -= one
	return nil
}

// Admit admits a bind of device for member in one conditional write: it
// takes a token of the bind rate when rate is set, then reserves the
// device when max is set, spec 15.2.
func (u *Store) Admit(ctx context.Context, member, device string, max, rate *int32) error {
	now := u.now()
	return u.update(ctx, member, func(mu *solas.MemberUsage) error {
		if rate != nil {
			if err := take(mu, Binds, *rate, now); err != nil {
				return err
			}
		}
		if max == nil {
			return nil
		}
		for _, e := range mu.Entries {
			if e.Device == device {
				return nil
			}
		}
		if int32(len(mu.Entries)) >= *max {
			return apierrors.NewForbidden(solas.Resource("devices"), device,
				fmt.Errorf("member %s holds or is binding %d devices, and its limit is %d", member, len(mu.Entries), *max))
		}
		mu.Entries = append(mu.Entries, solas.UsageEntry{Device: device, Added: metav1.NewTime(now)})
		return nil
	})
}

// TakePreemption takes a token of the preemption rate of member.
func (u *Store) TakePreemption(ctx context.Context, member string, rate int32) error {
	now := u.now()
	return u.update(ctx, member, func(mu *solas.MemberUsage) error { return take(mu, Preemptions, rate, now) })
}

// WithClock returns u with another clock, for tests.
func (u *Store) WithClock(now func() time.Time) *Store { return &Store{s: u.s, now: now} }
