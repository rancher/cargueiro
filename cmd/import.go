package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/rancher/cargueiro/internal/importer"
	"github.com/rancher/cargueiro/internal/rules"
)

func newImportCmd(opts *rootOptions) (*cobra.Command, error) {
	var source, repoDir, family, minor, registry string
	var dryRun bool

	cmd := &cobra.Command{
		Use:  "import",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for flag, value := range map[string]string{"--source": source, "--repo": repoDir, "--family": family, "--minor": minor} {
				if value == "" {
					return fmt.Errorf("%s is required", flag)
				}
			}
			if !dryRun {
				return errors.New("the import only reports what it would do for now: give --dry-run")
			}
			cfg, err := rules.Parse(opts.rules)
			if err != nil {
				return err
			}
			opts.log.DebugContext(cmd.Context(), "planning import", "source", source, "repo", repoDir, "family", family, "minor", minor)
			plan, err := importer.MakePlan(cfg, importer.Options{Source: source, Repo: repoDir, Registry: registry}, family, minor)
			if err != nil {
				return err
			}
			if _, err := io.WriteString(cmd.OutOrStdout(), plan.Report()); err != nil {
				return fmt.Errorf("write result: %w", err)
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&source, "source", "", "path of the checkout of the old system to import from: its index.yaml and assets/")
	flags.StringVar(&repoDir, "repo", "", "path of the charts repository to import into")
	flags.StringVar(&family, "family", "", "the family to import, by its name in rules.yaml")
	flags.StringVar(&minor, "minor", "", "the Rancher minor whose charts are imported, such as 2.12")
	flags.StringVar(&registry, "registry", "", "registry path the archives are pushed to, host[:port]/path, instead of the staging registry of the chart")
	flags.BoolVar(&dryRun, "dry-run", false, "report what would be imported, and read and write nothing in a registry or in the repository")
	if err := applyDoc(cmd, "import"); err != nil {
		return nil, err
	}

	return cmd, nil
}
