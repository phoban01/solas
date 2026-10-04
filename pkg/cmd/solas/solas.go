// Package solas builds the solas command. One process runs the API server
// part on every replica and the controller part on the replica that holds
// the leader election lease, ADR 0012.
package solas

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/phoban01/solas/pkg/cmd/server"
	"github.com/phoban01/solas/pkg/controller"
	"github.com/phoban01/solas/pkg/format"
	"github.com/phoban01/solas/pkg/migrate"
)

// groupVersion is the API that the controller part reads through the
// cluster's kube-apiserver.
const groupVersion = "solas.dev/v1alpha1"

// Options holds the options of both parts.
type Options struct {
	Server     *server.Options
	Controller *controller.Options
}

// NewOptions returns the defaults of both parts.
func NewOptions() *Options {
	o := &Options{Server: server.NewOptions(), Controller: controller.NewOptions()}
	// The Pod probes the API server part, so the controller part serves no
	// probes of its own.
	o.Controller.ProbeAddr = "0"
	return o
}

// NewCommand returns the solas command, with the flags of both parts.
func NewCommand(ctx context.Context, o *Options) *cobra.Command {
	cmd := server.NewCommand(ctx, o.Server)
	cmd.Use = "solas"
	cmd.Short = "Serve the solas.dev API from DynamoDB and run the member controller"
	o.Controller.AddFlags(cmd.Flags())
	cmd.AddCommand(newMigrateCommand(), newUnsealCommand(), newFinalizeCommand(), newReleaseCommand(), newRestoreEpochCommand())
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		if err := errors.Join(o.Server.Validate(), o.Controller.Validate()); err != nil {
			return err
		}
		return o.Run(c.Context())
	}
	return cmd
}

// Run starts the API server part, waits until the cluster serves the
// solas API through it, then runs the controller part. It returns when
// either part stops.
func (o *Options) Run(ctx context.Context) error {
	store, err := migrate.Open(ctx, o.Server.StorageURL, o.Server.Prefix)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := checkFormat(ctx, store); err != nil {
		return err
	}

	config, err := o.Server.Config(ctx)
	if err != nil {
		return err
	}
	srv, err := config.Complete().New()
	if err != nil {
		return err
	}
	prepared := srv.GenericAPIServer.PrepareRun()
	restConfig, err := ctrl.GetConfig()
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return prepared.RunWithContext(ctx) })
	g.Go(func() error { return watchFormat(ctx, store) })
	g.Go(func() error {
		if err := waitForAPI(ctx, restConfig); err != nil {
			return err
		}
		return controller.Run(ctx, restConfig, o.Controller)
	})
	return g.Wait()
}

// waitForAPI waits until the kube-apiserver serves groupVersion. The
// controller part reads Devices and Members through it, so it starts only
// after the API server part is ready and the APIService is available.
func waitForAPI(ctx context.Context, cfg *rest.Config) error {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return err
	}
	log := ctrl.Log.WithName("solas")
	return wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(context.Context) (bool, error) {
		if _, err := dc.ServerResourcesForGroupVersion(groupVersion); err != nil {
			log.Info("waiting for the API", "groupVersion", groupVersion, "err", err.Error())
			return false, nil
		}
		return true, nil
	})
}

// formatPoll is how often a running server reads the finalized format.
const formatPoll = 30 * time.Second

//= spec/solas.md#11-5-rollback
//# A server MUST refuse to start when the finalized format is above its
//# maximum format.

// checkFormat reads the finalized format of the store and writes in it
// from then on, spec 11.2. It fails when the store is finalized above the
// maximum of this release.
func checkFormat(ctx context.Context, s *migrate.Store) error {
	f, err := s.Finalized(ctx)
	if err != nil {
		return fmt.Errorf("read the finalized format: %w", err)
	}
	if f > format.Max {
		return fmt.Errorf("the store is finalized at format %d, and this release supports formats %d to %d; run a newer release",
			f, format.Min, format.Max)
	}
	format.SetFinalized(f)
	e, err := s.Epoch(ctx)
	if err != nil {
		return fmt.Errorf("read the store epoch: %w", err)
	}
	format.SetEpoch(e)
	return nil
}

// watchFormat reads the finalized format on a slow poll. It ends the
// process when the store is finalized above this release, and ignores a
// failed read.
func watchFormat(ctx context.Context, s *migrate.Store) error {
	log := ctrl.Log.WithName("format")
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(formatPoll):
		}
		f, err := s.Finalized(ctx)
		if err != nil {
			log.Info("cannot read the finalized format", "err", err.Error())
			continue
		}
		if err := checkFormat(ctx, s); err != nil && f > format.Max {
			return err
		}
	}
}
