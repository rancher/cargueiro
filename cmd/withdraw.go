package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rancher/cargueiro/internal/index"
)

func newWithdrawCmd(opts *rootOptions) (*cobra.Command, error) {
	var dryRun bool

	cmd := &cobra.Command{
		Use:  "withdraw <entries/chart/version.yaml>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.log.DebugContext(cmd.Context(), "withdrawing", "entry", args[0], "dry-run", dryRun)
			ref, err := index.Withdraw(args[0], dryRun)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "withdraw %s %s\n", ref.Chart, ref.Version); err != nil {
				return fmt.Errorf("write result: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "check the entry and print what would be withdrawn, and write nothing")
	if err := applyDoc(cmd, "withdraw"); err != nil {
		return nil, err
	}

	return cmd, nil
}
