package oci

import (
	"bytes"
	"context"
	"strconv"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

// storeChart puts a manifest with a config and a layer in s and returns the
// descriptor of the manifest.
func storeChart(t *testing.T, s *memory.Store, layer []byte) ocispec.Descriptor {
	t.Helper()
	ctx := context.Background()

	push := func(mediaType string, data []byte) ocispec.Descriptor {
		desc := content.NewDescriptorFromBytes(mediaType, data)
		require.NoError(t, s.Push(ctx, desc, bytes.NewReader(data)))
		return desc
	}
	config := push("application/vnd.cncf.helm.config.v1+json", []byte(`{"name":"a"}`))
	chartLayer := push("application/vnd.cncf.helm.chart.content.v1.tar+gzip", layer)
	manifestJSON := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"` +
		config.MediaType + `","digest":"` + config.Digest.String() + `","size":` + strconv.FormatInt(config.Size, 10) + `},"layers":[{"mediaType":"` +
		chartLayer.MediaType + `","digest":"` + chartLayer.Digest.String() + `","size":` + strconv.FormatInt(chartLayer.Size, 10) + `}]}`)
	manifest := push(ocispec.MediaTypeImageManifest, manifestJSON)
	// a memory store resolves a name it was given, and a registry resolves a digest
	require.NoError(t, s.Tag(ctx, manifest, manifest.Digest.String()))
	return manifest
}

func TestCopyManifestKeepsTheDigestAndTagsIt(t *testing.T) {
	ctx := context.Background()
	src, dst := memory.New(), memory.New()
	manifest := storeChart(t, src, []byte("the archive"))

	require.NoError(t, copyManifest(ctx, src, manifest.Digest.String(), dst, "1.0.0"))

	got, err := dst.Resolve(ctx, "1.0.0")
	require.NoError(t, err)
	require.Equal(t, manifest.Digest, got.Digest, "the tag names the manifest that was copied")
	exists, err := dst.Exists(ctx, ocispec.Descriptor{Digest: digest.FromBytes([]byte("the archive")), Size: int64(len("the archive")), MediaType: "application/vnd.cncf.helm.chart.content.v1.tar+gzip"})
	require.NoError(t, err)
	require.True(t, exists, "the chart layer is copied too")
}

func TestCopyManifestFailsWhenTheSourceDoesNotHaveIt(t *testing.T) {
	err := copyManifest(context.Background(), memory.New(), digest.FromString("nothing").String(), memory.New(), "1.0.0")

	require.Error(t, err)
}
