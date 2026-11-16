package importer

import (
	"context"

	"github.com/rancher/cargueiro/internal/oci"
)

// ociRegistry is the Registry that talks to a real registry.
type ociRegistry struct {
	pusher *oci.Pusher
	fetch  oci.FetchOptions
}

// NewRegistry returns a Registry that reads and pushes charts of target, with
// the credentials in opts, where guard allows it.
func NewRegistry(target oci.Target, guard *oci.Guard, opts oci.ClientOptions) (Registry, error) {
	client, err := oci.NewClient(opts)
	if err != nil {
		return nil, err
	}
	return ociRegistry{
		pusher: oci.NewPusher(client, target, guard),
		fetch:  oci.FetchOptions{Username: opts.Username, Password: opts.Password, PlainHTTP: opts.PlainHTTP},
	}, nil
}

// Inspect reads the manifest of the chart at ref with one request.
func (r ociRegistry) Inspect(ctx context.Context, ref string) (oci.ManifestInfo, error) {
	chartRef, err := oci.ParseChartRef(ref)
	if err != nil {
		return oci.ManifestInfo{}, err
	}
	return oci.FetchManifestInfo(ctx, chartRef, r.fetch)
}

// Push uploads the archive.
func (r ociRegistry) Push(ctx context.Context, archive string) error {
	_, err := r.pusher.Push(ctx, archive)
	return err
}
