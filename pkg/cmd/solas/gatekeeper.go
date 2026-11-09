package solas

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/controller/scheme"
	"github.com/phoban01/solas/pkg/gatekeeper"
)

// newGatekeeperCommand returns solas gatekeeper, spec 17.
func newGatekeeperCommand() *cobra.Command {
	var kubeconfig, upstream, listen, attribute, pattern string
	cmd := &cobra.Command{
		Use:   "gatekeeper --upstream <url> --device-attribute <name>",
		Short: "Proxy an actuator and check the fencing token of each change against the store",
		Long: "A gatekeeper sits in front of an actuator, such as a machine provisioning service, and forwards a request " +
			"that changes a machine only when the device that holds the machine ID names the claim of the request " +
			"and has its fencing token. Reads pass through. It reads devices through the solas API of its cluster.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := ctrl.GetConfig()
			if kubeconfig != "" {
				cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
			}
			if err != nil {
				return err
			}
			u, err := url.Parse(upstream)
			if err != nil || u.Host == "" {
				return fmt.Errorf("--upstream must be a URL with a host: %q", upstream)
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("--machine-id-pattern: %w", err)
			}
			return runGatekeeper(cmd.Context(), cfg, u, listen, attribute, re)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Empty means in-cluster or the default.")
	cmd.Flags().StringVar(&upstream, "upstream", "", "URL of the actuator's API (required).")
	cmd.Flags().StringVar(&listen, "listen", ":9443", "Address to listen on.")
	cmd.Flags().StringVar(&attribute, "device-attribute", "machineID", "Device attribute that holds the machine ID of the actuator.")
	cmd.Flags().StringVar(&pattern, "machine-id-pattern", `^/machines/([^/]+)`, "Regular expression on the request path; its first group is the machine ID.")
	_ = cmd.MarkFlagRequired("upstream")
	return cmd
}

// indexField is the cache index of devices by one attribute.
func indexField(attribute string) string { return "spec.attributes." + attribute }

// cacheIndex finds a device by its attribute through the cache's index.
type cacheIndex struct {
	cache cache.Cache
}

func (c cacheIndex) Lookup(ctx context.Context, attr, value string) (string, error) {
	var list solasv1alpha1.DeviceList
	if err := c.cache.List(ctx, &list, client.MatchingFields{indexField(attr): value}); err != nil {
		return "", err
	}
	if len(list.Items) == 0 {
		return "", nil
	}
	return list.Items[0].Name, nil
}

// runGatekeeper serves the gatekeeper until ctx ends. A cache of devices
// maps a machine ID to a device; the check itself reads the device with
// a consistent read, spec 17.2.
func runGatekeeper(ctx context.Context, cfg *rest.Config, upstream *url.URL, listen, attribute string, pattern *regexp.Regexp) error {
	sch := scheme.New()
	c, err := cache.New(cfg, cache.Options{Scheme: sch})
	if err != nil {
		return err
	}
	if err := c.IndexField(ctx, &solasv1alpha1.Device{}, indexField(attribute), func(o client.Object) []string {
		if v, ok := o.(*solasv1alpha1.Device).Spec.Attributes[attribute]; ok {
			return []string{v}
		}
		return nil
	}); err != nil {
		return err
	}
	direct, err := client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		return err
	}
	g, err := gatekeeper.New(gatekeeper.Options{Upstream: upstream, Attribute: attribute, Pattern: pattern,
		Index: cacheIndex{cache: c}, Reader: direct})
	if err != nil {
		return err
	}
	go func() {
		if err := c.Start(ctx); err != nil {
			ctrl.Log.Error(err, "the device cache stopped")
		}
	}()
	if !c.WaitForCacheSync(ctx) {
		return fmt.Errorf("the device cache did not sync")
	}
	srv := &http.Server{Addr: listen, Handler: g, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	ctrl.Log.Info("gatekeeper listening", "listen", listen, "upstream", upstream.String(), "attribute", attribute)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
