// Package gatekeeper is a proxy in front of an actuator that checks the
// fencing token of each request against the store, spec 17 and ADR 0017.
// The actuator, such as a machine provisioning service, checks nothing
// itself; the gatekeeper holds its credentials, so every change goes
// through the check.
package gatekeeper

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
)

// Headers of a request that changes a device, spec 17.1.
const (
	ClaimHeader = "Solas-Claim"
	TokenHeader = "Solas-Fencing-Token"
)

// Index finds the device whose attribute holds a value.
type Index interface {
	// Lookup returns the name of the device whose attribute attr equals
	// value, or "" when there is none.
	Lookup(ctx context.Context, attr, value string) (string, error)
}

// Options configure a Gatekeeper.
type Options struct {
	// Upstream is the actuator's API.
	Upstream *url.URL
	// Attribute is the device attribute that holds the machine ID of the
	// actuator.
	Attribute string
	// Pattern matches the request path; its first group is the machine ID.
	Pattern *regexp.Regexp
	// Index maps a machine ID to a device name.
	Index Index
	// Reader reads the device with a consistent read, spec 17.2.
	Reader client.Reader
}

// Gatekeeper proxies an actuator and checks each change.
type Gatekeeper struct {
	o     Options
	proxy http.Handler
	locks sync.Map // device name -> *sync.Mutex
}

// New returns a gatekeeper for the options.
func New(o Options) (*Gatekeeper, error) {
	switch {
	case o.Upstream == nil:
		return nil, fmt.Errorf("gatekeeper: no upstream")
	case o.Attribute == "":
		return nil, fmt.Errorf("gatekeeper: no device attribute")
	case o.Pattern == nil || o.Pattern.NumSubexp() < 1:
		return nil, fmt.Errorf("gatekeeper: the path pattern needs one group for the machine ID")
	case o.Index == nil || o.Reader == nil:
		return nil, fmt.Errorf("gatekeeper: no index or reader")
	}
	return &Gatekeeper{o: o, proxy: httputil.NewSingleHostReverseProxy(o.Upstream)}, nil
}

// ServeHTTP checks the request and forwards it, or rejects it.
func (g *Gatekeeper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	//= spec/solas.md#17-1-request
	//# A request with the method `GET`, `HEAD`, or `OPTIONS` reads and MUST
	//# pass through without a check.
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		g.proxy.ServeHTTP(w, r)
		return
	}
	reason, status := g.check(r)
	if reason != "" {
		http.Error(w, reason, status)
		return
	}
	g.proxy.ServeHTTP(w, r)
}

// check returns the reason to reject the request and its status, or an
// empty reason when the request may go through. It holds the lock of the
// device while it checks and, through r's context, while the actuator
// answers: the caller forwards within the same lock, see ServeHTTP.
func (g *Gatekeeper) check(r *http.Request) (string, int) {
	//= spec/solas.md#17-1-request
	//# A request that changes a device MUST carry the claim of its workload in
	//# the header `Solas-Claim`, as `member/namespace/name`.
	//= spec/solas.md#17-1-request
	//# It MUST carry the fencing token of the claim in the header
	//# `Solas-Fencing-Token`.
	parts := strings.Split(r.Header.Get(ClaimHeader), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "the request needs the header " + ClaimHeader + " as member/namespace/name", http.StatusForbidden
	}
	token, err := strconv.ParseInt(r.Header.Get(TokenHeader), 10, 64)
	if err != nil {
		return "the request needs the header " + TokenHeader + " with the fencing token of the claim", http.StatusForbidden
	}
	//= spec/solas.md#17-2-check
	//# The gatekeeper MUST map a request to a device by a value in the request
	//# path and a device attribute that holds that value.
	//= spec/solas.md#17-2-check
	//# A request whose path names no device MUST fail with `403 Forbidden`.
	m := g.o.Pattern.FindStringSubmatch(r.URL.Path)
	if m == nil {
		return "the request path names no machine", http.StatusForbidden
	}
	name, err := g.o.Index.Lookup(r.Context(), g.o.Attribute, m[1])
	if err != nil {
		return "cannot look up the device: " + err.Error(), http.StatusServiceUnavailable
	}
	if name == "" {
		return fmt.Sprintf("no device has %s=%s", g.o.Attribute, m[1]), http.StatusForbidden
	}
	//= spec/solas.md#17-3-lock
	//# The gatekeeper MUST hold a lock per device from the check until the
	//# actuator answers.
	mu, _ := g.locks.LoadOrStore(name, &sync.Mutex{})
	lock := mu.(*sync.Mutex)
	lock.Lock()
	context.AfterFunc(r.Context(), lock.Unlock)
	//= spec/solas.md#17-2-check
	//# The gatekeeper MUST read the device from the solas API of its cluster
	//# with a consistent read before it forwards a request.
	var d solasv1alpha1.Device
	if err := g.o.Reader.Get(r.Context(), client.ObjectKey{Name: name}, &d); err != nil {
		return "cannot read device " + name + ": " + err.Error(), http.StatusServiceUnavailable
	}
	//= spec/solas.md#17-2-check
	//# It MUST forward the request only when the `claimRef` of the device names
	//# the member, namespace, and name of the header, and the `fencingToken`
	//# of the device equals the token of the header.
	//= spec/solas.md#17-2-check
	//# It MUST reject every other request that changes a device with
	//# `403 Forbidden`, and say why.
	ref := d.Status.ClaimRef
	switch {
	case ref == nil:
		return fmt.Sprintf("device %s is free", name), http.StatusForbidden
	case ref.Member != parts[0] || ref.Namespace != parts[1] || ref.Name != parts[2]:
		return fmt.Sprintf("device %s is held by %s/%s/%s, not by %s", name, ref.Member, ref.Namespace, ref.Name, r.Header.Get(ClaimHeader)), http.StatusForbidden
	case d.Status.FencingToken != token:
		return fmt.Sprintf("device %s has token %d; the request has token %d", name, d.Status.FencingToken, token), http.StatusForbidden
	}
	return "", 0
}

// ListIndex finds a device by a list of all devices. It fits a small
// pool and the tests; the command uses a cache with an index.
type ListIndex struct{ Reader client.Reader }

// Lookup lists the devices and returns the one whose attribute matches.
func (l ListIndex) Lookup(ctx context.Context, attr, value string) (string, error) {
	var list solasv1alpha1.DeviceList
	if err := l.Reader.List(ctx, &list); err != nil {
		return "", err
	}
	for _, d := range list.Items {
		if d.Spec.Attributes[attr] == value {
			return d.Name, nil
		}
	}
	return "", nil
}
