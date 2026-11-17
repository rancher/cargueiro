package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rancher/cargueiro/internal/index"
	"github.com/rancher/cargueiro/internal/rules"
)

func newWithdrawCmd(opts *rootOptions) (*cobra.Command, error) {
	var dryRun, family bool

	cmd := &cobra.Command{
		Use:  "withdraw <entries/chart/version.yaml>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.log.DebugContext(cmd.Context(), "withdrawing", "entry", args[0], "family", family, "dry-run", dryRun)
			refs, err := withdraw(opts, args[0], family, dryRun)
			if err != nil {
				return err
			}
			for _, ref := range refs {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "withdraw %s %s\n", ref.Chart, ref.Version); err != nil {
					return fmt.Errorf("write result: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&family, "family", false, "withdraw the same version of every chart of the family of the entry, or none of them")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "check the entry and print what would be withdrawn, and write nothing")
	if err := applyDoc(cmd, "withdraw"); err != nil {
		return nil, err
	}

	return cmd, nil
}

// withdraw withdraws the entry in path, and with family the same version of every chart of
// its family.
func withdraw(opts *rootOptions, path string, family, dryRun bool) ([]index.Ref, error) {
	if !family {
		ref, err := index.Withdraw(path, dryRun)
		if err != nil {
			return nil, err
		}
		return []index.Ref{ref}, nil
	}
	cfg, err := rules.Parse(opts.rules)
	if err != nil {
		return nil, err
	}
	return index.WithdrawFamily(cfg, path, dryRun)
}
