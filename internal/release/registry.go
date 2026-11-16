package release

import (
	"context"
	"strings"

	"github.com/rancher/cargueiro/internal/oci"
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
}

// ociRegistry is the Registry that talks to the real registries.
type ociRegistry struct {
	guard *oci.Guard
	opts  oci.CopyOptions
}

// NewRegistry returns a Registry that reads the charts from the registry their
// entries say with the credentials in from, and writes them to production with the
// ones in to, where guard allows it. A failed copy is tried again as retry says.
func NewRegistry(guard *oci.Guard, from, to oci.FetchOptions, retry oci.Retry) Registry {
	return ociRegistry{guard: guard, opts: oci.CopyOptions{From: from, To: to, Retry: retry}}
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
