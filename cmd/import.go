package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/rancher/cargueiro/internal/importer"
	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/rules"
)

// importRegistryFactory builds the registry an import reads and pushes to.
type importRegistryFactory func(target oci.Target, guard *oci.Guard, opts oci.ClientOptions) (importer.Registry, error)

func newImportCmd(opts *rootOptions, newRegistry importRegistryFactory) (*cobra.Command, error) {
	var source, repoDir, family, minor, registry string
	var plainHTTP, dryRun bool
	var creds credentialFlags

	cmd := &cobra.Command{
		Use:  "import",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for flag, value := range map[string]string{"--source": source, "--repo": repoDir, "--family": family, "--minor": minor} {
				if value == "" {
					return fmt.Errorf("%s is required", flag)
				}
			}
			cfg, err := rules.Parse(opts.rules)
			if err != nil {
				return err
			}
			importOpts := importer.Options{Source: source, Repo: repoDir, Registry: registry}
			opts.log.DebugContext(cmd.Context(), "importing", "source", source, "repo", repoDir, "family", family, "minor", minor, "dry-run", dryRun)

			if dryRun {
				plan, err := importer.MakePlan(cfg, importOpts, family, minor)
				if err != nil {
					return err
				}
				if _, err := io.WriteString(cmd.OutOrStdout(), plan.Report()); err != nil {
					return fmt.Errorf("write result: %w", err)
				}
				return nil
			}

			target, err := importTarget(cfg, family, registry)
			if err != nil {
				return err
			}
			guard, err := newGuard(opts.rules)
			if err != nil {
				return err
			}
			given := creds.read(opts)
			conn, err := given.connection(cmd.Context(), target.Host)
			if err != nil {
				return err
			}
			conn.PlainHTTP = conn.PlainHTTP || plainHTTP
			reg, err := newRegistry(target, guard, conn.clientOptions())
			if err != nil {
				return err
			}
			return importer.Run(cmd.Context(), cfg, importOpts, family, minor, reg, cmd.OutOrStdout())
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&source, "source", "", "path of the checkout of the old system to import from: its index.yaml and assets/")
	flags.StringVar(&repoDir, "repo", "", "path of the charts repository to import into")
	flags.StringVar(&family, "family", "", "the family to import, by its name in rules.yaml")
	flags.StringVar(&minor, "minor", "", "the Rancher minor whose charts are imported, such as 2.12")
	flags.StringVar(&registry, "registry", "", "registry path the archives are pushed to, host[:port]/path, instead of the staging registry of the chart")
	flags.BoolVar(&dryRun, "dry-run", false, "report what would be imported, and read and write nothing in a registry or in the repository")
	flags.BoolVar(&plainHTTP, "plain-http", false, "talk HTTP to the registry instead of HTTPS, for local registries")
	creds.addFlags(flags)
	if err := applyDoc(cmd, "import"); err != nil {
		return nil, err
	}

	return cmd, nil
}

// importTarget returns the registry the family is pushed to: the one given, or the
// staging registry of its edition.
func importTarget(cfg *rules.Config, family, registry string) (oci.Target, error) {
	if registry == "" {
		fam, err := cfg.Family(family)
		if err != nil {
			return oci.Target{}, err
		}
		repositories := cfg.Registries().ChartRepositories(fam.Edition)
		if len(repositories) == 0 {
			return oci.Target{}, errors.New("the rules file has no registry for the edition of the family")
		}
		registry = repositories[0]
	}
	return oci.ParseTarget(registry)
}
