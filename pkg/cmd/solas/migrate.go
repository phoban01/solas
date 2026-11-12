package solas

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/utils/clock"

	"github.com/phoban01/solas/pkg/migrate"
)

// migrateOptions holds the flags of solas migrate and solas unseal.
type migrateOptions struct {
	from, to string
	prefix   string
	dryRun   bool
	// lease is D, how long the quiet check of an etcd source waits.
	lease time.Duration
	clock clock.Clock
}

func (o *migrateOptions) addFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&o.from, "from", "", "Storage URL of the source store.")
	f.StringVar(&o.to, "to", "", "Storage URL of the destination store.")
	f.StringVar(&o.prefix, "storage-prefix", "/registry", "Key prefix of the API servers in both stores.")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
}

func (o *migrateOptions) open(ctx context.Context) (src, dst *migrate.Store, err error) {
	if src, err = migrate.Open(ctx, o.from, o.prefix); err != nil {
		return nil, nil, fmt.Errorf("--from: %w", err)
	}
	if dst, err = migrate.Open(ctx, o.to, o.prefix); err != nil {
		src.Close()
		return nil, nil, fmt.Errorf("--to: %w", err)
	}
	return src, dst, nil
}

// newMigrateCommand returns solas migrate, spec section 12.
func newMigrateCommand() *cobra.Command {
	o := &migrateOptions{clock: clock.RealClock{}}
	cmd := &cobra.Command{
		Use:   "migrate --from <url> --to <url>",
		Short: "Move every Device and Member to another store: seal the source, copy, and verify",
		Long: "Seal the source, copy every Device and Member into the destination, and verify the copy. " +
			"Then change the storage URL of each member cluster within D. " +
			"An etcd source cannot be sealed: stop solas in every member cluster first, and the tool waits D to check that no Member renews.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return o.migrate(cmd.Context(), cmd.OutOrStdout())
		},
	}
	o.addFlags(cmd)
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Print what the move would copy. Seal nothing and write nothing.")
	cmd.Flags().DurationVar(&o.lease, "lease-duration", 30*time.Second, "Lease duration D of the members. The quiet check of an etcd source waits this long.")
	cmd.AddCommand(newVerifyCommand())
	return cmd
}

func (o *migrateOptions) migrate(ctx context.Context, out io.Writer) error {
	src, dst, err := o.open(ctx)
	if err != nil {
		return err
	}
	defer src.Close()
	defer dst.Close()
	if err := dst.Empty(ctx); err != nil {
		return err
	}
	if !dst.CanSeal() && !o.dryRun {
		// An etcd destination cannot raise its revision, spec 12.2; refuse
		// it before the copy, while it is still empty.
		if err := migrate.CheckRevision(ctx, src, dst); err != nil {
			return err
		}
	}

	//= spec/solas.md#12-2-copy
	//# The tool MUST seal the source before it reads it.
	switch {
	case o.dryRun:
		fmt.Fprintf(out, "dry run: would seal %s\n", src.URL)
	case src.CanSeal():
		if err := src.Seal(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "sealed %s: it now rejects every write\n", src.URL)
	default:
		fmt.Fprintf(out, "%s cannot be sealed; checking for %v that no Member renews and nothing changes\n", src.URL, o.lease)
		if err := migrate.Quiet(ctx, src.Versions, o.lease, o.clock); err != nil {
			return err
		}
	}

	rep, err := migrate.Copy(ctx, src, dst, o.dryRun, out)
	if err != nil {
		return err
	}
	if o.dryRun {
		fmt.Fprintf(out, "dry run: would copy %d devices and %d members to %s\n", rep.Devices, rep.Members, dst.URL)
		return nil
	}
	if err := migrate.Verify(ctx, src, dst); err != nil {
		return err
	}
	if dst.CanSeal() {
		e, err := migrate.MoveEpoch(ctx, src, dst, o.clock.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "moved %s to epoch %d: its versions and tokens are above every one of the source\n", dst.URL, e)
	}
	fmt.Fprintf(out, "copied and verified %d devices and %d members in %s\n", rep.Devices, rep.Members, dst.URL)
	fmt.Fprintf(out, "now set the storage URL of each member cluster to %s, within D\n", dst.URL)
	return nil
}

// newVerifyCommand returns solas migrate verify.
func newVerifyCommand() *cobra.Command {
	o := &migrateOptions{}
	cmd := &cobra.Command{
		Use:   "verify --from <url> --to <url>",
		Short: "Check that the destination holds an exact copy of every Device and Member of the source",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			src, dst, err := o.open(cmd.Context())
			if err != nil {
				return err
			}
			defer src.Close()
			defer dst.Close()
			if err := migrate.Verify(cmd.Context(), src, dst); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "the stores hold the same Devices and Members")
			return nil
		},
	}
	o.addFlags(cmd)
	return cmd
}

// newUnsealCommand returns solas unseal, which rolls a move back.
func newUnsealCommand() *cobra.Command {
	o := &migrateOptions{}
	cmd := &cobra.Command{
		Use:   "unseal --from <url> --to <url>",
		Short: "Roll a move back: unseal the source, when no cluster wrote to the destination",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			src, dst, err := o.open(ctx)
			if err != nil {
				return err
			}
			defer src.Close()
			defer dst.Close()
			if !src.CanSeal() {
				return fmt.Errorf("--from: %w, so there is nothing to unseal", migrate.ErrCannotSeal)
			}
			if err := migrate.Untouched(ctx, src, dst); err != nil {
				return fmt.Errorf("will not unseal: %w", err)
			}
			if err := src.Unseal(ctx); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "unsealed %s\n", src.URL)
			return nil
		},
	}
	o.addFlags(cmd)
	return cmd
}
