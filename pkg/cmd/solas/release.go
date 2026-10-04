package solas

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	solasv1alpha1 "github.com/phoban01/solas/pkg/apis/solas/v1alpha1"
	"github.com/phoban01/solas/pkg/controller/scheme"
)

// newReleaseCommand returns solas release, spec 8.5.
func newReleaseCommand() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "release <device>",
		Short: "Release a device whose holder's member is gone, for example a retained one",
		Long: "Clear the claimRef of a device whose holder's member is gone. The command refuses while the member exists. " +
			"It runs through the kube-apiserver with your kubeconfig, so RBAC decides who may release, " +
			"and the server records who released the device in status.lastRelease.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := ctrl.GetConfig()
			if kubeconfig != "" {
				cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
			}
			if err != nil {
				return err
			}
			c, err := client.New(cfg, client.Options{Scheme: scheme.New()})
			if err != nil {
				return err
			}
			return release(cmd.Context(), c, args[0], cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to a kubeconfig. Empty means the default.")
	return cmd
}

//= spec/solas.md#8-5-reclaim-policy
//# The release MUST fail while the holder's member UID is live.

//= spec/solas.md#8-5-reclaim-policy
//# The release MUST be a status update that clears `claimRef` and carries
//# the resource version that the operator read.

// release clears the claimRef of a device when no Member has the UID of
// its holder's member.
func release(ctx context.Context, c client.Client, name string, out io.Writer) error {
	var d solasv1alpha1.Device
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &d); err != nil {
		return err
	}
	ref := d.Status.ClaimRef
	if ref == nil {
		fmt.Fprintf(out, "device %s is free\n", name)
		return nil
	}
	var members solasv1alpha1.MemberList
	if err := c.List(ctx, &members); err != nil {
		return err
	}
	for _, m := range members.Items {
		if m.UID == ref.MemberUID {
			return fmt.Errorf("device %s is held by claim %s/%s of member %s, which is live (UID %s); its own controller releases it",
				name, ref.Namespace, ref.Name, m.Name, m.UID)
		}
	}
	cleared := d.DeepCopy()
	cleared.Status.ClaimRef = nil
	if err := c.Status().Update(ctx, cleared); err != nil {
		return fmt.Errorf("release device %s: %w", name, err)
	}
	fmt.Fprintf(out, "released device %s, held by claim %s/%s of gone member %s\n", name, ref.Namespace, ref.Name, ref.Member)
	if r := cleared.Status.LastRelease; r != nil {
		fmt.Fprintf(out, "recorded: released by %q at %s\n", r.By, r.At.UTC().Format("2006-01-02T15:04:05Z"))
	}
	return nil
}
