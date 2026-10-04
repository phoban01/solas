package solas

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/phoban01/solas/pkg/migrate"
)

// newRestoreEpochCommand returns solas restore-epoch, spec 13.
func newRestoreEpochCommand() *cobra.Command {
	var url, prefix string
	var epoch int64
	cmd := &cobra.Command{
		Use:   "restore-epoch --storage-url <url> [--epoch N]",
		Short: "After a restore from a backup, move the store to a new epoch before any server starts",
		Long: "Move the store to an epoch above every epoch it had, so fencing tokens and resource versions never go back. " +
			"Run it after the restore and before any solas server starts on the store. " +
			"The default epoch is the time in Unix seconds, and at least the stored epoch plus 1.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := migrate.Open(cmd.Context(), url, prefix)
			if err != nil {
				return fmt.Errorf("--storage-url: %w", err)
			}
			defer s.Close()
			cur, err := s.Epoch(cmd.Context())
			if err != nil {
				return err
			}
			//= spec/solas.md#13-1-epoch
			//# The restore tool SHOULD use the time of the restore in Unix seconds, and
			//# at least the restored epoch plus 1.
			e := epoch
			if e == 0 {
				e = max(time.Now().Unix(), cur+1)
			}
			if err := s.SetEpoch(cmd.Context(), e); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "moved %s from epoch %d to epoch %d\n", s.URL, cur, e)
			if s.Dynamo == nil {
				//= spec/solas.md#13-3-resource-versions
				//# On the etcd store, the operator MUST restore with
				//# `etcdutl snapshot restore --bump-revision` and `--mark-compacted`.
				fmt.Fprintln(out, "on etcd, the snapshot must have been restored with etcdutl snapshot restore --bump-revision <large> --mark-compacted")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&url, "storage-url", "", "Storage URL of the restored store.")
	f.StringVar(&prefix, "storage-prefix", "/registry", "Key prefix of the API servers in the store.")
	f.Int64Var(&epoch, "epoch", 0, "The new epoch. 0 means the time in Unix seconds.")
	_ = cmd.MarkFlagRequired("storage-url")
	return cmd
}
