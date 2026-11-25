package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// sigstoreBundleTypePrefix is the artifact type of the sigstore bundles that cosign v3
	// attaches to an image as OCI referrers.
	sigstoreBundleTypePrefix = "application/vnd.dev.sigstore.bundle"
	// cosignSignatureType is the artifact type of the OCI 1.1 signatures of cosign.
	cosignSignatureType = "application/vnd.dev.cosign.artifact.sig.v1+json"
	// ociEmptyMediaType is the media type of the empty config of an artifact manifest, which
	// the referrers that a registry keeps in a tag, and not in its API, carry instead of
	// their artifact type.
	ociEmptyMediaType = "application/vnd.oci.empty.v1+json"

	// The annotations of a sigstore bundle say whether it is a signature or an attestation.
	bundleContentAnnotation   = "dev.sigstore.bundle.content"
	bundlePredicateAnnotation = "dev.sigstore.bundle.predicateType"
	// cosignSignPredicateType is the predicate of the envelope that cosign v3 writes when it
	// signs an image: it is a signature, not an attestation.
	cosignSignPredicateType = "https://sigstore.dev/cosign/sign/v1"
)

// CountSignatures returns how many signatures the image whose manifest has the digest
// has in the repository of ref: the sigstore bundles and the cosign signatures that are
// attached to it as referrers, and the signature that older cosign stores under the tag
// sha256-<hex>.sig. SBOMs and attestations are not counted when the bundle says it is one.
// The tag of ref is not used.
func CountSignatures(ctx context.Context, ref ChartRef, manifestDigest string, opts FetchOptions) (int, error) {
	dgst, err := digest.Parse(manifestDigest)
	if err != nil {
		return 0, fmt.Errorf("the digest %q: %w", manifestDigest, err)
	}
	repo, err := newRepository(ref, opts)
	if err != nil {
		return 0, err
	}

	var referrers []ocispec.Descriptor
	err = repo.Referrers(ctx, ocispec.Descriptor{Digest: dgst}, "", func(page []ocispec.Descriptor) error {
		referrers = append(referrers, page...)
		return nil
	})
	if err != nil {
		return 0, fetchError(ref.String(), err, opts.authenticated())
	}

	count := 0
	for _, r := range referrers {
		isSignature, err := signatureReferrer(ctx, repo, r)
		if err != nil {
			return 0, fetchError(ref.String(), err, opts.authenticated())
		}
		if isSignature {
			count++
		}
	}

	legacy := fmt.Sprintf("sha256-%s.sig", dgst.Encoded())
	if _, err := repo.Resolve(ctx, legacy); err == nil {
		count++
	} else if fe := fetchError(ref.String(), err, opts.authenticated()); !errors.Is(fe, ErrNotFound) {
		return 0, fe
	}
	return count, nil
}

// signatureReferrer says whether the referrer is a signature. A registry that keeps the
// referrers in a tag gives neither the artifact type nor the annotations in its list, so
// they are read from the manifest of the referrer then.
func signatureReferrer(ctx context.Context, repo interface {
	Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error)
}, r ocispec.Descriptor) (bool, error) {
	artifactType, annotations := r.ArtifactType, r.Annotations
	if artifactType == "" || artifactType == ociEmptyMediaType {
		body, err := repo.Fetch(ctx, r)
		if err != nil {
			return false, err
		}
		defer func() { _ = body.Close() }()
		var m ocispec.Manifest
		if err := json.NewDecoder(body).Decode(&m); err != nil {
			return false, fmt.Errorf("read the manifest of the referrer %s: %w", r.Digest, err)
		}
		artifactType = m.ArtifactType
		if artifactType == "" || artifactType == ociEmptyMediaType {
			if len(m.Layers) == 1 {
				artifactType = m.Layers[0].MediaType
			} else {
				artifactType = m.Config.MediaType
			}
		}
		if len(annotations) == 0 {
			annotations = m.Annotations
		}
	}

	if artifactType == cosignSignatureType {
		return true, nil
	}
	if !strings.HasPrefix(artifactType, sigstoreBundleTypePrefix) {
		return false, nil
	}
	if predicate := annotations[bundlePredicateAnnotation]; predicate != "" {
		return predicate == cosignSignPredicateType, nil
	}
	// A bundle that does not say what it is counts as a signature.
	return true, nil
}
