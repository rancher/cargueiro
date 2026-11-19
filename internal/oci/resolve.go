package oci

import (
	"context"
)

// ResolveDigest returns the digest of the manifest that the tag of ref names. It
// does not read the manifest: it is how a tag is turned into the digest that pins
// it. Reads are anonymous.
func ResolveDigest(ctx context.Context, ref ChartRef, opts FetchOptions) (string, error) {
	repo, err := newRepository(ref, opts)
	if err != nil {
		return "", err
	}

	desc, err := repo.Resolve(ctx, ref.Tag)
	if err != nil {
		return "", fetchError(ref.String(), err, opts.authenticated())
	}
	return desc.Digest.String(), nil
}

// ResolveDigestWithRetry is ResolveDigest, run again by retry when the read fails
// in a way that can pass, such as a network error or a registry that is busy. A
// tag that is not there and a refusal are not tried again.
func ResolveDigestWithRetry(ctx context.Context, ref ChartRef, opts FetchOptions, retry Retry) (string, error) {
	var digest string
	err := retry.do(ctx, func() error {
		var err error
		digest, err = ResolveDigest(ctx, ref, opts)
		return err
	}, nil)
	if err != nil {
		return "", err
	}
	return digest, nil
}
