package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rancher/cargueiro/internal/annotations"
	"github.com/rancher/cargueiro/internal/chart"
	"github.com/rancher/cargueiro/internal/imagecheck"
	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/rules"
)

// resolveImageFunc says whether a registry has the image at ref: it returns nil
// when it does, an error that matches oci.ErrNotFound when it does not, and any
// other error when it could not tell.
type resolveImageFunc func(ctx context.Context, ref oci.ChartRef, opts oci.FetchOptions) error

// resolveImage is the production resolveImageFunc. It asks for the image again
// when the registry is busy or the network fails.
func resolveImage(ctx context.Context, ref oci.ChartRef, opts oci.FetchOptions) error {
	_, err := oci.ResolveDigestWithRetry(ctx, ref, opts, oci.DefaultRetry())
	return err
}

func newCheckPushedDependenciesCmd(opts *rootOptions, resolve resolveImageFunc) (*cobra.Command, error) {
	var registry string
	var plainHTTP bool
	var creds credentialFlags

	cmd := &cobra.Command{
		Use:  "check-pushed-dependencies <chart-dir>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return checkPushedDependencies(cmd, opts, resolve, args[0], registry, plainHTTP, &creds)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&registry, "registry", "", "images registry: host[:port] (default: the staging images registry of rules.yaml)")
	flags.BoolVar(&plainHTTP, "plain-http", false, "talk HTTP to the registry instead of HTTPS, for local registries")
	creds.addFlags(flags)
	if err := applyDoc(cmd, "check-pushed-dependencies"); err != nil {
		return nil, err
	}

	return cmd, nil
}

// checkPushedDependencies asks the images registry for every image that the
// helm.sh/images annotation of the chart in dir lists, and writes the reference
// of each, one to a line.
func checkPushedDependencies(cmd *cobra.Command, opts *rootOptions, resolve resolveImageFunc, dir, registry string, plainHTTP bool, creds *credentialFlags) error {
	host, err := imagesRegistryHost(opts.rules, registry)
	if err != nil {
		return err
	}

	files, err := chart.Load(dir)
	if err != nil {
		return err
	}
	value := files.Annotations[helmImagesAnnotation]
	if strings.TrimSpace(value) == "" {
		return nil
	}
	images, err := annotations.HelmImages(value)
	if err != nil {
		return err
	}
	opts.log.DebugContext(cmd.Context(), "checking images", "chart", files.Metadata.Name, "registry", host, "images", len(images))

	given := creds.read(opts)
	fetchOpts, err := given.credentialFunc(cmd.Context()).FetchOptions(host, plainHTTP)
	if err != nil {
		return err
	}
	checked, err := imagecheck.Check(cmd.Context(), host, images, func(ctx context.Context, ref oci.ChartRef) error {
		return resolve(ctx, ref, fetchOpts)
	})
	if err != nil {
		return err
	}

	for _, ref := range checked {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), ref); err != nil {
			return fmt.Errorf("write reference: %w", err)
		}
	}
	return nil
}

// helmImagesAnnotation is the annotation that lists the images of a chart.
const helmImagesAnnotation = "helm.sh/images"

// imagesRegistryHost is the host of the registry the images are looked for in:
// registry when it is given, otherwise the host of the staging images registry of
// the rules. The images carry their own path, so only the host is used.
func imagesRegistryHost(rulesData []byte, registry string) (string, error) {
	if registry == "" {
		cfg, err := rules.Parse(rulesData)
		if err != nil {
			return "", err
		}
		registry = cfg.Registries().Images.Staging
		target, err := oci.ParseTarget(registry)
		if err != nil {
			return "", fmt.Errorf("staging images registry of the rules: %w", err)
		}
		return target.Host, nil
	}

	target, err := oci.ParseTarget(registry)
	if err != nil {
		return "", fmt.Errorf("--registry: %w", err)
	}
	if target.Path != "" {
		return "", errors.New("--registry takes only host[:port]: the images carry their own path")
	}
	return target.Host, nil
}
