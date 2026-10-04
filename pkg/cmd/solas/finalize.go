package solas

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phoban01/solas/pkg/migrate"
)

// newFinalizeCommand returns solas finalize, spec 11.4.
func newFinalizeCommand() *cobra.Command {
	var url, prefix string
	var to int32
	cmd := &cobra.Command{
		Use:   "finalize --storage-url <url> --to <format>",
		Short: "Move the finalized format of the store up, when every Active member supports it",
		Long: "Move the finalized format of the store up to --to. Servers then write in that format. " +
			"The command refuses while an Active member supports less, and names those members. " +
			"After it, a release that supports less refuses to start, so a rollback below the format is no longer possible.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := migrate.Open(cmd.Context(), url, prefix)
			if err != nil {
				return fmt.Errorf("--storage-url: %w", err)
			}
			defer s.Close()
			if err := s.Finalize(cmd.Context(), to); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "finalized format %d in %s\n", to, s.URL)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&url, "storage-url", "", "Storage URL of the store.")
	f.StringVar(&prefix, "storage-prefix", "/registry", "Key prefix of the API servers in the store.")
	f.Int32Var(&to, "to", 0, "The format to finalize.")
	_ = cmd.MarkFlagRequired("storage-url")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}
