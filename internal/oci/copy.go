package oci

import (
	"context"
	"fmt"

	"oras.land/oras-go/v2"
)

// CopyOptions say how a chart is reached on each side of a copy.
type CopyOptions struct {
	// From is how the repository that is copied from is read, and To how the one
	// that is copied to is written. They have their own credentials.
	From, To FetchOptions
	// Retry is how the copy is tried again when the network fails. The zero value
	// does not.
	Retry Retry
}

// Copy copies the chart whose manifest has the digest manifestDigest, from the
// repository from of a registry to the tag of dst in another, or the same,
// registry. The manifest and the blobs it names are copied as they are, so the tag
// of dst names a manifest with the same digest, which is checked.
//
// It does not look at what dst has: whoever copies decides that a tag may be
// written.
func Copy(ctx context.Context, from ChartRepo, manifestDigest string, dst ChartRef, opts CopyOptions) error {
	source, err := newRepository(ChartRef{Host: from.Host, Repository: from.Repository}, opts.From)
	if err != nil {
		return err
	}
	target, err := newRepository(dst, opts.To)
	if err != nil {
		return err
	}
	err = opts.Retry.do(ctx, func() error {
		return copyManifest(ctx, source, manifestDigest, target, dst.Tag)
	}, nil)
	if err != nil {
		return fmt.Errorf("copy %s@%s to %s: %w", from, manifestDigest, dst, err)
	}
	return nil
}

// copyManifest copies the manifest with the digest manifestDigest of src, and the
// content it names, to dst, and tags it as tag there.
func copyManifest(ctx context.Context, src oras.ReadOnlyTarget, manifestDigest string, dst oras.Target, tag string) error {
	desc, err := oras.Copy(ctx, src, manifestDigest, dst, tag, oras.CopyOptions{})
	if err != nil {
		return err
	}
	if desc.Digest.String() != manifestDigest {
		return fmt.Errorf("the manifest that was copied has the digest %s, not %s", desc.Digest, manifestDigest)
	}
	return nil
}
