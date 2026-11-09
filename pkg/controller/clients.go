package controller

import (
	"time"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//= spec/solas.md#16-2-claims
//# The controller MUST bound each request to its API server to `D / 3`.

// RequestTimeout returns the bound of each request of the controller:
// D / 3, the renew interval. A request that waits for a store that does
// not answer fails within it, so the one worker is free to suspend the
// claims when the lease lapses, and a request never outlives the renew
// that follows it.
func RequestTimeout(lease time.Duration) time.Duration { return lease / 3 }

// clients returns the two clients of the controllers: one that reads
// claims from the manager's cache and writes directly, and one that
// reads directly, for the consistent lists of spec 6.3 and 8.3. Both
// bound each request to RequestTimeout. The manager's own clients keep
// no bound, because the informer watches must stay open.
func clients(mgr ctrl.Manager, cfg *rest.Config, lease time.Duration) (cached, direct client.Client, err error) {
	bounded := rest.CopyConfig(cfg)
	bounded.Timeout = RequestTimeout(lease)
	direct, err = client.New(bounded, client.Options{Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper()})
	if err != nil {
		return nil, nil, err
	}
	cached, err = client.New(bounded, client.Options{Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper(),
		Cache: &client.CacheOptions{Reader: mgr.GetCache()}})
	if err != nil {
		return nil, nil, err
	}
	return cached, direct, nil
}
