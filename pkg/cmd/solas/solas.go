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
	"github.com/phoban01/solas/pkg/registry/solas/usage"
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
	cmd.AddCommand(newMigrateCommand(), newUnsealCommand(), newFinalizeCommand(), newReleaseCommand(), newRestoreEpochCommand(), newGatekeeperCommand())
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		if err := errors.Join(o.Server.Validate(), o.Controller.Validate()); err != nil {
			return err
		}
		return o.Run(c.Context())
	}
	return cmd
}

//= spec/solas.md#16-1-the-server
//# The readiness of the server MUST NOT depend on the store.

// Run starts the API server part, waits until the cluster serves the
// solas API through it, then runs the controller part. It returns when
// either part stops.
//
// Nothing here waits for the store. The server serves discovery and
// reports ready while the store does not answer; the storage guard
// rejects writes until runStore has read the finalized format and the
// epoch, spec 16.1.
func (o *Options) Run(ctx context.Context) error {
	format.Forget()
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
	g.Go(func() error { return o.runStore(ctx) })
	g.Go(func() error {
		if err := waitForAPI(ctx, restConfig); err != nil {
			return err
		}
		return controller.Run(ctx, restConfig, o.Controller)
	})
	return g.Wait()
}

// storeRetry is how long runStore waits between two tries to reach a
// store that does not answer.
const storeRetry = 2 * time.Second

// runStore waits until the store answers, reads the finalized format and
// the epoch, and then keeps them and the usage set up to date. It returns
// an error, which stops the process, when the store is finalized above
// this release, spec 11.5.
func (o *Options) runStore(ctx context.Context) error {
	if err := o.Server.EnsureStore(ctx); err != nil {
		return err
	}
	log := ctrl.Log.WithName("store")
	var store *migrate.Store
	err := wait.PollUntilContextCancel(ctx, storeRetry, true, func(ctx context.Context) (bool, error) {
		s, err := openStore(ctx, o.Server.StorageURL, o.Server.Prefix)
		if err != nil {
			log.Info("waiting for the store", "err", err.Error())
			return false, nil
		}
		if err := checkFormat(ctx, s); err != nil {
			s.Close()
			var tooNew *tooNewError
			if errors.As(err, &tooNew) {
				return false, err
			}
			log.Info("waiting for the store", "err", err.Error())
			return false, nil
		}
		store = s
		return true, nil
	})
	if err != nil {
		return err
	}
	defer store.Close()
	log.Info("read the store", "finalizedFormat", format.Write(), "epoch", format.Epoch())

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return watchFormat(ctx, store) })
	g.Go(func() error { return cleanUsage(ctx, store, o.Controller.ClusterID) })
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

// tooNewError says that the store is finalized above this release.
type tooNewError struct{ finalized int32 }

func (e *tooNewError) Error() string {
	return fmt.Sprintf("the store is finalized at format %d, and this release supports formats %d to %d; run a newer release",
		e.finalized, format.Min, format.Max)
}

//= spec/solas.md#11-5-rollback
//# A server MUST refuse to start when the finalized format is above its
//# maximum format.

// checkFormat reads the finalized format of the store and writes in it
// from then on, spec 11.2. It fails with a tooNewError when the store is
// finalized above the maximum of this release; the process then exits,
// right after its start when the store answered at once.
func checkFormat(ctx context.Context, s *migrate.Store) error {
	f, err := s.Finalized(ctx)
	if err != nil {
		return fmt.Errorf("read the finalized format: %w", err)
	}
	if f > format.Max {
		return &tooNewError{finalized: f}
	}
	e, err := s.Epoch(ctx)
	if err != nil {
		return fmt.Errorf("read the store epoch: %w", err)
	}
	format.SetEpoch(e)
	format.SetFinalized(f)
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
		err := checkFormat(ctx, s)
		var tooNew *tooNewError
		switch {
		case errors.As(err, &tooNew):
			return err
		case err != nil:
			log.Info("cannot read the finalized format", "err", err.Error())
		}
	}
}

// usagePoll is how often the server cleans the usage set of its member.
const usagePoll = 30 * time.Second

// cleanUsage removes the stale entries of the usage set of this member,
// spec 15.3. Every replica may run it; each removal is conditional.
func cleanUsage(ctx context.Context, s *migrate.Store, member string) error {
	u := usage.New(s.Usages)
	log := ctrl.Log.WithName("usage")
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(usagePoll):
		}
		removed, err := u.Cleanup(ctx, s.Devices, member, usage.DefaultGrace, time.Now())
		if err != nil {
			log.Info("cannot clean the usage set", "member", member, "err", err.Error())
			continue
		}
		if len(removed) > 0 {
			log.Info("removed stale usage entries", "member", member, "devices", removed)
		}
	}
}

// openStore opens the store as migrate.Open does, but returns when ctx
// ends. The etcd3 storage waits until it reaches etcd and does not take
// a context, so an open of an etcd store that does not answer can run
// for a minute. The open goes on in the background, and its store is
// closed when it arrives.
func openStore(ctx context.Context, url, prefix string) (*migrate.Store, error) {
	type result struct {
		s   *migrate.Store
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := migrate.Open(ctx, url, prefix)
		done <- result{s, err}
	}()
	select {
	case r := <-done:
		return r.s, r.err
	case <-ctx.Done():
		go func() {
			if r := <-done; r.s != nil {
				r.s.Close()
			}
		}()
		return nil, ctx.Err()
	}
}
