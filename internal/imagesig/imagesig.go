// Package imagesig copies the signatures of images between two registries.
//
// A release copies an image by the digest of its manifest, and that does not copy what
// is attached to it: the signature is another artifact that refers to the image. This
// package copies the signatures of images that are already in both registries, and checks
// that they are there afterwards. It never copies an image.
package imagesig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rancher/cargueiro/internal/oci"
)

// Image is an image of the images registries: rancher/<name>:<tag>.
type Image struct {
	Repository string
	Tag        string
}

func (i Image) String() string { return i.Repository + ":" + i.Tag }

// Parse reads repository:tag. An image by digest is refused: the signatures are found by
// the digest of the manifest that the tag names in each registry, and the tag must name the
// same manifest in both.
func Parse(s string) (Image, error) {
	if strings.Contains(s, "@") {
		return Image{}, fmt.Errorf("%q: give the image as repository:tag", s)
	}
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 || strings.Contains(s[i:], "/") || !strings.Contains(s[:i], "/") {
		return Image{}, fmt.Errorf("%q: give the image as repository:tag, such as rancher/fleet:v0.17.0", s)
	}
	return Image{Repository: s[:i], Tag: s[i+1:]}, nil
}

// Side says which registry is meant.
type Side int

const (
	// Source is the registry the signatures are copied from.
	Source Side = iota
	// Destination is the registry they are copied to.
	Destination
)

// Backend is what the copy needs from the registries.
type Backend interface {
	// Digest returns the digest of the manifest that the tag of the image names in the
	// registry. It fails with an error that matches oci.ErrNotFound when there is none.
	Digest(ctx context.Context, side Side, image Image) (string, error)
	// Signatures counts the signatures of the image whose manifest has the digest.
	Signatures(ctx context.Context, side Side, image Image, digest string) (int, error)
	// CopySignatures copies the signatures of the image from the source to the
	// destination, without replacing anything that is there.
	CopySignatures(ctx context.Context, image Image) error
}

// Run copies the signatures of every image, and writes one line for each to report. An
// image that fails does not stop the others: every image is done before the error, which
// says all the images that failed, is returned. With dryRun nothing is copied.
func Run(ctx context.Context, images []Image, b Backend, dryRun bool, report io.Writer) error {
	var failed []string
	for _, image := range images {
		line, err := one(ctx, image, b, dryRun)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", image, err))
			continue
		}
		if _, err := fmt.Fprintln(report, line); err != nil {
			return err
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("the signatures of %d of %d images could not be copied:\n  %s", len(failed), len(images), strings.Join(failed, "\n  "))
	}
	return nil
}

func one(ctx context.Context, image Image, b Backend, dryRun bool) (string, error) {
	src, err := b.Digest(ctx, Source, image)
	if errors.Is(err, oci.ErrNotFound) {
		return "", errors.New("the image is not in the source registry")
	}
	if err != nil {
		return "", fmt.Errorf("read the image in the source registry: %w", err)
	}
	dst, err := b.Digest(ctx, Destination, image)
	if errors.Is(err, oci.ErrNotFound) {
		return "", errors.New("the image is not in the destination registry: only the signatures are copied here, so the image has to be released first")
	}
	if err != nil {
		return "", fmt.Errorf("read the image in the destination registry: %w", err)
	}
	if src != dst {
		return "", fmt.Errorf("the tag names %s in the source registry and %s in the destination one: the signatures are of the first", src, dst)
	}

	have, err := b.Signatures(ctx, Source, image, src)
	if err != nil {
		return "", fmt.Errorf("read the signatures in the source registry: %w", err)
	}
	before, err := b.Signatures(ctx, Destination, image, dst)
	if err != nil {
		return "", fmt.Errorf("read the signatures in the destination registry: %w", err)
	}
	if have == 0 {
		return fmt.Sprintf("no signatures to copy %s: the source has none", image), nil
	}
	if dryRun {
		return fmt.Sprintf("would copy the signatures of %s (source %d, destination %d)", image, have, before), nil
	}

	if err := b.CopySignatures(ctx, image); err != nil {
		return "", fmt.Errorf("copy the signatures: %w", err)
	}
	after, err := b.Signatures(ctx, Destination, image, dst)
	if err != nil {
		return "", fmt.Errorf("read the signatures in the destination registry after the copy: %w", err)
	}
	// The copy is slsactl's: it creates the tag that lists what refers to the image, and leaves
	// it as it is when the destination has it already. So it is a failure when it added nothing
	// and the destination still has fewer signatures than the source.
	if after <= before && after < have {
		return "", fmt.Errorf("the destination has %d signatures after the copy, as before it, and the source has %d", after, have)
	}
	return fmt.Sprintf("signatures copied %s (source %d, destination before %d, after %d)", image, have, before, after), nil
}
