// Package migrate moves the Devices and Members of a mesh from one store
// to another, spec section 12 and ADR 0015.
package migrate

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sstorage "k8s.io/apiserver/pkg/storage"
	"k8s.io/apiserver/pkg/storage/storagebackend"
	"k8s.io/apiserver/pkg/storage/storagebackend/factory"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/apiserver"
	storageurl "github.com/phoban01/solas/pkg/storage"
	"github.com/phoban01/solas/pkg/storage/dynamo"
)

// A resource that the move copies.
type resource struct {
	gr          schema.GroupResource
	newFunc     func() runtime.Object
	newListFunc func() runtime.Object
}

func (r resource) prefix() string { return "/" + r.gr.Group + "/" + r.gr.Resource }

var (
	devices = resource{solas.Resource("devices"), func() runtime.Object { return &solas.Device{} },
		func() runtime.Object { return &solas.DeviceList{} }}
	members = resource{solas.Resource("members"), func() runtime.Object { return &solas.Member{} },
		func() runtime.Object { return &solas.MemberList{} }}
	formats = resource{solas.Resource("storeformats"), func() runtime.Object { return &solas.StoreFormat{} },
		func() runtime.Object { return &solas.StoreFormatList{} }}
	resources = []resource{devices, members, formats}
)

// ResourcePrefixes are the resource prefixes of the resources that a move
// copies and seals.
func ResourcePrefixes() []string {
	out := make([]string, len(resources))
	for i, r := range resources {
		out[i] = r.prefix()
	}
	return out
}

//= spec/solas.md#12-2-copy
//# The tool MUST copy at the storage level, not through the API server.

// Store is the raw storage of the Devices, Members, and format of one store, with
// no API server and no watch cache in front of it.
type Store struct {
	URL     storageurl.Config
	Prefix  string
	Devices k8sstorage.Interface
	Members k8sstorage.Interface
	// Formats holds the finalized format of the store, spec 11.1.
	Formats k8sstorage.Interface
	// Dynamo is set for a DynamoDB store, so the tool can seal it.
	Dynamo  *dynamo.Config
	destroy []factory.DestroyFunc
}

// Open opens the store that a storage URL names. prefix is the storage
// prefix of the API servers, /registry by default.
func Open(ctx context.Context, url, prefix string) (*Store, error) {
	sc, err := storageurl.Parse(url)
	if err != nil {
		return nil, err
	}
	s := &Store{URL: sc, Prefix: prefix}
	var open func(resource) (k8sstorage.Interface, error)
	switch sc.Kind {
	case storageurl.DynamoDB:
		client, err := storageurl.DynamoClient(ctx, sc)
		if err != nil {
			return nil, err
		}
		cfg := dynamo.Config{Client: client, Table: sc.Table}
		if sc.CreateTable {
			if err := dynamo.EnsureTable(ctx, client, sc.Table); err != nil {
				return nil, err
			}
		}
		s.Dynamo = &cfg
		open = func(r resource) (k8sstorage.Interface, error) {
			return dynamo.New(cfg, apiserver.StorageCodec(), r.newFunc, r.newListFunc, prefix, r.prefix(), r.gr)
		}
	case storageurl.Etcd:
		open = func(r resource) (k8sstorage.Interface, error) {
			cfg := storagebackend.NewDefaultConfig(prefix, apiserver.StorageCodec())
			cfg.Type = storagebackend.StorageTypeETCD3
			cfg.EncodeVersioner = apiserver.StorageVersioner()
			cfg.Transport.ServerList = sc.Endpoints
			st, destroy, err := factory.Create(*cfg.ForResource(r.gr), r.newFunc, r.newListFunc, r.prefix())
			if err == nil {
				s.destroy = append(s.destroy, destroy)
			}
			return st, err
		}
	default:
		return nil, fmt.Errorf("unknown store %q", sc.Kind)
	}
	if s.Devices, err = open(devices); err != nil {
		s.Close()
		return nil, err
	}
	if s.Formats, err = open(formats); err != nil {
		s.Close()
		return nil, err
	}
	if s.Members, err = open(members); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the store.
func (s *Store) Close() {
	for _, d := range s.destroy {
		d()
	}
}

// storageFor returns the storage of a resource.
func (s *Store) storageFor(r resource) k8sstorage.Interface {
	switch r.gr {
	case devices.gr:
		return s.Devices
	case members.gr:
		return s.Members
	}
	return s.Formats
}

// list returns every object of a resource.
func (s *Store) list(ctx context.Context, r resource) ([]runtime.Object, error) {
	list := r.newListFunc()
	opts := k8sstorage.ListOptions{Recursive: true, Predicate: k8sstorage.Everything}
	if err := s.storageFor(r).GetList(ctx, r.prefix(), opts, list); err != nil {
		return nil, fmt.Errorf("list %s: %w", r.gr, err)
	}
	var out []runtime.Object
	switch l := list.(type) {
	case *solas.DeviceList:
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case *solas.StoreFormatList:
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	case *solas.MemberList:
		for i := range l.Items {
			out = append(out, &l.Items[i])
		}
	}
	return out, nil
}
