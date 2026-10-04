package apiserver

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/phoban01/solas/pkg/apis/solas"
	"github.com/phoban01/solas/pkg/registry/solas/device"
	"github.com/phoban01/solas/pkg/registry/solas/member"
	"github.com/phoban01/solas/pkg/registry/solas/memberpolicy"
	"github.com/phoban01/solas/pkg/registry/solas/usage"
)

// Config is the configuration of the solas API server.
type Config struct {
	GenericConfig *genericapiserver.RecommendedConfig
}

// CompletedConfig is a Config after Complete.
type CompletedConfig struct {
	GenericConfig genericapiserver.CompletedConfig
}

// Server is the solas API server.
type Server struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
}

// Complete fills in the defaults of the configuration.
func (c *Config) Complete() CompletedConfig {
	return CompletedConfig{GenericConfig: c.GenericConfig.Complete()}
}

// New returns a server that serves the solas.dev group.
func (c CompletedConfig) New() (*Server, error) {
	generic, err := c.GenericConfig.New("solas-apiserver", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return nil, err
	}
	getter := c.GenericConfig.RESTOptionsGetter
	policies, err := memberpolicy.NewREST(Scheme, getter)
	if err != nil {
		return nil, err
	}
	lookup := func(ctx context.Context, member string) (*solas.MemberPolicy, error) {
		obj, err := policies.Get(ctx, member, &metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return obj.(*solas.MemberPolicy), nil
	}
	opts := device.Options{Policies: lookup}
	if g, ok := getter.(RESTOptionsGetter); ok {
		ro, err := g.GetRESTOptions(usage.Resource, nil)
		if err != nil {
			return nil, err
		}
		raw, _, err := g.RawStorage(ro.StorageConfig, ro.ResourcePrefix,
			func() runtime.Object { return &solas.MemberUsage{} }, func() runtime.Object { return &solas.MemberUsageList{} })
		if err != nil {
			return nil, err
		}
		opts.Usage = usage.New(raw)
	}
	devices, deviceStatus, err := device.NewRESTWithOptions(Scheme, getter, opts)
	if err != nil {
		return nil, err
	}
	members, memberStatus, err := member.NewREST(Scheme, getter)
	if err != nil {
		return nil, err
	}

	group := genericapiserver.NewDefaultAPIGroupInfo(solas.GroupName, Scheme, metav1.ParameterCodec, Codecs)
	group.VersionedResourcesStorageMap["v1alpha1"] = map[string]rest.Storage{
		"devices":        devices,
		"devices/status": deviceStatus,
		"members":        members,
		"members/status": memberStatus,
		"memberpolicies": policies,
	}
	if err := generic.InstallAPIGroup(&group); err != nil {
		return nil, err
	}
	return &Server{GenericAPIServer: generic}, nil
}
