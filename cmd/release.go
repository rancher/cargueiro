package cmd

import (
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/release"
	"github.com/rancher/cargueiro/internal/rules"
)

const (
	envProductionUsername = "CARGUEIRO_PRODUCTION_USERNAME"
	envProductionPassword = "CARGUEIRO_PRODUCTION_PASSWORD"
)

// releaseRegistryFactory builds the registry a release reads the charts from, with
// the options from, and copies them to, with the options to. The signatures of the images it
// copies are copied by copySignatures.
type releaseRegistryFactory func(cfg *rules.Config, guard *oci.Guard, from, to oci.FetchOptions, retry oci.Retry, copySignatures release.SignatureCopier) release.Registry

func newReleaseCmd(opts *rootOptions, newRegistry releaseRegistryFactory) (*cobra.Command, error) {
	var from, repoDir, registry, imageRegistry, stagingImageRegistry, productionUsername string
	var families []string
	var legacy, plainHTTP, dryRun bool
	var creds credentialFlags

	cmd := &cobra.Command{
		Use:  "release",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for flag, value := range map[string]string{"--from": from, "--repo": repoDir} {
				if value == "" {
					return fmt.Errorf("%s is required", flag)
				}
			}
			cfg, err := rules.Parse(opts.rules)
			if err != nil {
				return err
			}
			if legacy && (imageRegistry != "" || stagingImageRegistry != "") {
				return errors.New("--images-registry and --staging-images-registry are for new versions: a legacy version has no images to copy")
			}
			releaseOpts := release.Options{
				From: from, Repo: repoDir, Registry: registry, Families: families,
				ImageRegistry: imageRegistry, StagingImageRegistry: stagingImageRegistry,
			}
			opts.log.DebugContext(cmd.Context(), "releasing", "from", from, "repo", repoDir, "legacy", legacy, "dry-run", dryRun)

			makePlan := release.MakeEntriesPlan
			if legacy {
				makePlan = release.MakePlan
			}
			plan, err := makePlan(cfg, releaseOpts)
			if err != nil {
				return err
			}
			if dryRun || len(plan.Release) == 0 {
				if _, err := io.WriteString(cmd.OutOrStdout(), plan.Report()); err != nil {
					return fmt.Errorf("write result: %w", err)
				}
				return nil
			}

			reg, restore, err := openReleaseRegistry(cmd, opts, newRegistry, plan, creds, productionUsername, plainHTTP)
			if err != nil {
				return err
			}
			defer restore()
			result, err := release.Run(cmd.Context(), releaseOpts, plan, reg, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			if _, err := io.WriteString(cmd.OutOrStdout(), "\n"+result.Summary()); err != nil {
				return fmt.Errorf("write result: %w", err)
			}
			if !result.Done() {
				return fmt.Errorf("%d versions were not released and %d are in conflict: see above", len(result.Failed), len(result.Conflicts))
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&legacy, "legacy", false, "release the legacy versions, the ones in legacy/, and nothing else (default: the new versions, the ones in entries/)")
	flags.StringVar(&from, "from", "", "path of the checkout of the staging charts repository to release from")
	flags.StringVar(&repoDir, "repo", "", "path of the checkout of the production charts repository to release into")
	flags.StringSliceVar(&families, "family", nil, "release only the charts of this family, by its name in rules.yaml; it can be given more than once (default: every family)")
	flags.StringVar(&registry, "registry", "", "registry path the charts are copied to, host[:port]/path, instead of the production registry of the chart")
	flags.StringVar(&imageRegistry, "images-registry", "", "registry path the images are copied to, host[:port]/path, instead of the production images registry of the rules")
	flags.StringVar(&stagingImageRegistry, "staging-images-registry", "", "registry path the images are copied from, host[:port]/path, instead of the staging images registry of the rules")
	flags.StringVar(&productionUsername, "production-username", "", "username for the production registry (default: $"+envProductionUsername+")")
	flags.BoolVar(&dryRun, "dry-run", false, "report what would be released, and read and write nothing in a registry or in the repository")
	flags.BoolVar(&plainHTTP, "plain-http", false, "talk HTTP to the registries instead of HTTPS, for local registries")
	creds.addFlags(flags)
	if err := applyDoc(cmd, "release"); err != nil {
		return nil, err
	}

	return cmd, nil
}

// openReleaseRegistry works out the credentials of both sides of the release and
// builds the registry. The charts are read from the registry their entries say,
// which must be one, with the credentials given for it, or the saved login for it,
// or none. They are written to the production registry, which must be one too, with
// its own credentials: the username from --production-username or the environment,
// and the password from the environment only. It also returns the function that
// removes what the copy of the signatures needs, which is called when the release ends.
func openReleaseRegistry(cmd *cobra.Command, opts *rootOptions, newRegistry releaseRegistryFactory, plan release.Plan, creds credentialFlags, productionUsername string, plainHTTP bool) (release.Registry, func(), error) {
	var sources, destinations []string
	add := func(list []string, host string) []string {
		if slices.Contains(list, host) {
			return list
		}
		return append(list, host)
	}
	for _, item := range plan.Release {
		sources = add(sources, item.Source.Host)
		destinations = add(destinations, item.Destination.Host)
		for _, image := range item.Images {
			sources = add(sources, image.Source.Host)
			destinations = add(destinations, image.Destination.Host)
		}
	}
	if len(sources) != 1 || len(destinations) != 1 {
		return nil, nil, fmt.Errorf("the versions to release are read from %v and copied to %v: a release works between one registry and one registry", sources, destinations)
	}

	guard, err := newGuard(opts.rules)
	if err != nil {
		return nil, nil, err
	}
	ctx := cmd.Context()
	source, err := creds.read(opts).connection(ctx, sources[0])
	if err != nil {
		return nil, nil, err
	}
	if productionUsername == "" {
		productionUsername = opts.getenv(envProductionUsername)
	}
	production, err := resolveConnection(ctx, opts, destinations[0], productionUsername, opts.getenv(envProductionPassword))
	if err != nil {
		return nil, nil, err
	}
	cfg, err := rules.Parse(opts.rules)
	if err != nil {
		return nil, nil, err
	}

	// slsactl copies the signatures with the docker config, as it did for charts-build-scripts.
	// The credentials are the two pairs of this release, in a config of their own for this run.
	restore, err := useDockerConfig(map[string]credentials{sources[0]: source.credentials, destinations[0]: production.credentials})
	if err != nil {
		return nil, nil, err
	}
	return newRegistry(cfg, guard, fetchOptions(source, plainHTTP), fetchOptions(production, plainHTTP), oci.DefaultRetry(), copySignatures), restore, nil
}

// fetchOptions turns a connection into the options of a read or a write.
func fetchOptions(conn connection, plainHTTP bool) oci.FetchOptions {
	return oci.FetchOptions{Username: conn.Username, Password: conn.Password, PlainHTTP: conn.PlainHTTP || plainHTTP}
}
