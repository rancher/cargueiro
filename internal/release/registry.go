package release

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/rancher/cargueiro/internal/check"
	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/rules"
	"github.com/rancher/cargueiro/internal/version"
)

// Registry is what a release needs from the registries.
type Registry interface {
	// Inspect says what the production registry has at ref, host/path/chart:tag. It
	// fails with an error that matches oci.ErrNotFound when there is nothing there.
	Inspect(ctx context.Context, ref string) (oci.ManifestInfo, error)
	// Copy copies the chart of the item from where its entry says to its
	// destination in the production registry, with the manifest as it is. It never
	// replaces a tag that is there.
	Copy(ctx context.Context, item Item) error
	// Validate checks the chart of the item as it is in the staging registry against the
	// rules of the catalog, as check oci does. A chart that is not valid is an error that
	// says why.
	Validate(ctx context.Context, item Item) error
	// StagingImage and ProductionImage return the digest of the manifest that the tag of an
	// image names in the staging and in the production registry. They fail with an error
	// that matches oci.ErrNotFound when there is nothing there.
	StagingImage(ctx context.Context, image oci.ChartRef) (string, error)
	ProductionImage(ctx context.Context, image oci.ChartRef) (string, error)
	// CopyImage copies the image, the manifest with the digest given, from the staging
	// registry to its place in the production one. It never replaces a tag that is there.
	CopyImage(ctx context.Context, image Image, digest string) error
}

// ociRegistry is the Registry that talks to the real registries.
type ociRegistry struct {
	cfg   *rules.Config
	guard *oci.Guard
	opts  oci.CopyOptions
}

// NewRegistry returns a Registry that reads the charts from the registry their
// entries say with the credentials in from, and writes them to production with the
// ones in to, where guard allows it. A failed copy is tried again as retry says.
func NewRegistry(cfg *rules.Config, guard *oci.Guard, from, to oci.FetchOptions, retry oci.Retry) Registry {
	return ociRegistry{cfg: cfg, guard: guard, opts: oci.CopyOptions{From: from, To: to, Retry: retry}}
}

// Inspect reads the manifest of the chart at ref with one request.
func (r ociRegistry) Inspect(ctx context.Context, ref string) (oci.ManifestInfo, error) {
	chartRef, err := oci.ParseChartRef(ref)
	if err != nil {
		return oci.ManifestInfo{}, err
	}
	return oci.FetchManifestInfo(ctx, chartRef, r.opts.To)
}

// Copy copies the chart of the item to its destination.
func (r ociRegistry) Copy(ctx context.Context, item Item) error {
	target := oci.Target{
		Host: item.Destination.Host,
		Path: strings.TrimSuffix(item.Destination.Repository, "/"+item.Chart),
	}
	if err := r.guard.Check(target, item.Chart); err != nil {
		return err
	}
	return oci.Copy(ctx, item.Source, item.ManifestDigest, item.Destination, r.opts)
}

// Validate reads the chart of the item from the staging registry and applies the rules of
// the catalog to it, as check oci does.
func (r ociRegistry) Validate(ctx context.Context, item Item) error {
	ref := oci.ChartRef{Host: item.Source.Host, Repository: item.Source.Repository, Tag: item.Destination.Tag}
	files, err := oci.FetchChart(ctx, ref, r.opts.From)
	if err != nil {
		return err
	}
	result, err := check.Chart(r.cfg, files, version.FromOCITag(ref.Tag))
	if err != nil {
		return err
	}
	if !result.Fails(false) {
		return nil
	}
	var problems []error
	for _, section := range []check.Section{result.Annotations, result.HIP15Annotations, result.Version} {
		for _, e := range section.Errors {
			problems = append(problems, errors.New(e))
		}
	}
	return fmt.Errorf("the chart in %s is not valid: %w", ref, errors.Join(problems...))
}

// StagingImage returns the digest of the manifest that the tag of an image names in the
// staging registry.
func (r ociRegistry) StagingImage(ctx context.Context, image oci.ChartRef) (string, error) {
	return oci.ResolveDigest(ctx, image, r.opts.From)
}

// ProductionImage returns the digest of the manifest that the tag of an image names in the
// production registry.
func (r ociRegistry) ProductionImage(ctx context.Context, image oci.ChartRef) (string, error) {
	return oci.ResolveDigest(ctx, image, r.opts.To)
}

// CopyImage copies the image to its destination, where the guard allows an image to go.
func (r ociRegistry) CopyImage(ctx context.Context, image Image, digest string) error {
	target := oci.Target{Host: image.Destination.Host, Path: path.Dir(image.Destination.Repository)}
	if err := r.guard.CheckImageRepository(target); err != nil {
		return err
	}
	from := oci.ChartRepo{Host: image.Source.Host, Repository: image.Source.Repository}
	return oci.Copy(ctx, from, digest, image.Destination, r.opts)
}
