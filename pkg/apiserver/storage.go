package apiserver

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/storage"
	cacherstorage "k8s.io/apiserver/pkg/storage/cacher"
	"k8s.io/apiserver/pkg/storage/storagebackend"
	"k8s.io/apiserver/pkg/storage/storagebackend/factory"
	"k8s.io/client-go/tools/cache"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/registry/solas/device"
	"github.com/phoban01/solas/pkg/storage/dynamo"
	"github.com/phoban01/solas/pkg/storage/guard"
)

// eventsHistoryWindow is the history of the watch cache. The cacher needs
// at least 75 seconds.
const eventsHistoryWindow = 5 * time.Minute

// RESTOptionsGetter gives each resource a store, behind its storage guard
// and the watch cache:
// the DynamoDB store, or the etcd store when EtcdServers is set, spec 2.6.
type RESTOptionsGetter struct {
	Dynamo dynamo.Config
	// EtcdServers holds the etcd client URLs. When it is set, the getter
	// picks the etcd store and ignores Dynamo.
	EtcdServers []string
	Codec       runtime.Codec
	// EncodeVersioner picks the version that objects are stored in.
	EncodeVersioner runtime.GroupVersioner
	// Prefix separates the data of different API servers in one table.
	Prefix string
}

var _ generic.RESTOptionsGetter = RESTOptionsGetter{}

// GetRESTOptions returns the options for one resource.
func (g RESTOptionsGetter) GetRESTOptions(resource schema.GroupResource, _ runtime.Object) (generic.RESTOptions, error) {
	if len(g.EtcdServers) > 0 {
		return g.etcdOptions(resource), nil
	}
	return generic.RESTOptions{
		StorageConfig: &storagebackend.ConfigForResource{
			Config: storagebackend.Config{
				Prefix:          g.Prefix,
				Codec:           g.Codec,
				EncodeVersioner: g.EncodeVersioner,
				// The watch cache keeps this much history. It must stay below
				// the event retention of the store.
				EventsHistoryWindow: eventsHistoryWindow,
			},
			GroupResource: resource,
		},
		Decorator:               g.decorator(),
		EnableGarbageCollection: true,
		DeleteCollectionWorkers: 1,
		ResourcePrefix:          "/" + resource.Group + "/" + resource.Resource,
		CountMetricPollPeriod:   time.Minute,
	}, nil
}

// guards holds the storage guard of each resource, below the registry.
var guards = map[schema.GroupResource]guard.Transition{
	solas.Resource("devices"): device.Transition,
}

// decorator builds the store of a resource, wraps it in its storage guard,
// and wraps that in the watch cache, as generic registry StorageWithCacher
// does. The guard checks each write on the stored objects, below the
// strategy, so servers of two releases enforce the same rules, spec 11.
func (g RESTOptionsGetter) decorator() generic.StorageDecorator {
	return func(
		config *storagebackend.ConfigForResource,
		resourcePrefix string,
		keyFunc func(obj runtime.Object) (string, error),
		newFunc func() runtime.Object,
		newListFunc func() runtime.Object,
		getAttrsFunc storage.AttrFunc,
		triggerFuncs storage.IndexerFuncs,
		indexers *cache.Indexers) (storage.Interface, factory.DestroyFunc, error) {
		raw, rawDestroy, err := g.RawStorage(config, resourcePrefix, newFunc, newListFunc)
		if err != nil {
			return nil, func() {}, err
		}
		s := guard.Wrap(raw, guards[config.GroupResource])
		//= spec/solas.md#4-2-poll
		//# The server SHOULD run one poller for each resource and share it between
		//# watchers.
		cacher, err := cacherstorage.NewCacherFromConfig(cacherstorage.Config{
			Storage:             s,
			EventsHistoryWindow: config.EventsHistoryWindow,
			Versioner:           storage.APIObjectVersioner{},
			GroupResource:       config.GroupResource,
			ResourcePrefix:      resourcePrefix,
			KeyFunc:             keyFunc,
			NewFunc:             newFunc,
			NewListFunc:         newListFunc,
			GetAttrsFunc:        getAttrsFunc,
			IndexerFuncs:        triggerFuncs,
			Indexers:            indexers,
			Codec:               config.Codec,
		})
		if err != nil {
			rawDestroy()
			return nil, func() {}, err
		}
		delegator := cacherstorage.NewCacheDelegator(cacher, s)
		var once sync.Once
		destroy := func() {
			once.Do(func() {
				delegator.Stop()
				cacher.Stop()
				rawDestroy()
			})
		}
		return delegator, destroy, nil
	}
}

//= spec/solas.md#2-7-etcd-store
//# The etcd store MUST use the etcd3 storage of `k8s.io/apiserver`.

//= spec/solas.md#2-7-etcd-store
//# The etcd store MUST keep each object under the storage prefix of the API
//# server.

//= spec/solas.md#2-7-etcd-store
//# Every read of the etcd store MUST be a linearizable read.

// etcdOptions returns the options of the etcd store for one resource: the
// etcd3 storage behind the watch cache, as the kube-apiserver uses it. The
// etcd3 storage reads with linearizable reads, the etcd default.
func (g RESTOptionsGetter) etcdOptions(resource schema.GroupResource) generic.RESTOptions {
	cfg := storagebackend.NewDefaultConfig(g.Prefix, g.Codec)
	cfg.Type = storagebackend.StorageTypeETCD3
	cfg.EncodeVersioner = g.EncodeVersioner
	cfg.Transport.ServerList = g.EtcdServers
	return generic.RESTOptions{
		StorageConfig:           cfg.ForResource(resource),
		Decorator:               g.decorator(),
		EnableGarbageCollection: true,
		DeleteCollectionWorkers: 1,
		ResourcePrefix:          "/" + resource.Group + "/" + resource.Resource,
		CountMetricPollPeriod:   time.Minute,
	}
}

// RawStorage builds the store of a resource with no guard and no watch
// cache. The decorator wraps it; tests use it to write below the guard.
func (g RESTOptionsGetter) RawStorage(config *storagebackend.ConfigForResource, resourcePrefix string,
	newFunc, newListFunc func() runtime.Object) (storage.Interface, factory.DestroyFunc, error) {
	if len(g.EtcdServers) > 0 {
		return generic.NewRawStorage(config, newFunc, newListFunc, resourcePrefix)
	}
	s, err := dynamo.New(g.Dynamo, config.Codec, newFunc, newListFunc, config.Prefix, resourcePrefix, config.GroupResource)
	return s, func() {}, err
}
