//go:build e2e

package releasing

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/rancher/cargueiro/tests/internal/harness"
)

const (
	// sigstoreBundleType and signPredicate are what cosign v3 gives the bundle that signs an
	// image: it is attached to the image as a referrer, and says by its annotation that it is a
	// signature and not an attestation. A bundle that says it is an attestation, as the SLSA
	// provenance of an image, has the predicate of slsaPredicate.
	sigstoreBundleType = "application/vnd.dev.sigstore.bundle.v0.3+json"
	signPredicate      = "https://sigstore.dev/cosign/sign/v1"
	slsaPredicate      = "https://slsa.dev/provenance/v1"
)

// registryRepository opens host/repository, as the registry user when withAuth says the
// registry asks for one.
func registryRepository(t *testing.T, host, repository string, withAuth bool) *remote.Repository {
	t.Helper()

	repo, err := remote.NewRepository(host + "/" + repository)
	require.NoError(t, err)
	repo.PlainHTTP = true
	// the registries of the tests cannot delete a manifest, so the index of referrers that a new
	// referrer replaces is left where it is
	repo.SkipReferrersGC = true
	if withAuth {
		repo.Client = &auth.Client{Credential: auth.StaticCredential(host, auth.Credential{Username: harness.RegistryUser, Password: harness.RegistryPassword})}
	}
	return repo
}

// signImage attaches a signature to the image at host/repository:tag, the way cosign v3 does:
// a sigstore bundle that refers to the manifest of the image. The content is different for
// each text. The registries of the tests keep the referrers in a tag, not in an API, as the
// SUSE registries do.
func signImage(t *testing.T, host, repository, tag, text string, withAuth bool) {
	t.Helper()
	attach(t, host, repository, tag, text, signPredicate, withAuth)
}

// attestImage attaches the SLSA provenance to the image, as a bundle that says it is not a
// signature.
func attestImage(t *testing.T, host, repository, tag, text string, withAuth bool) {
	t.Helper()
	attach(t, host, repository, tag, text, slsaPredicate, withAuth)
}

func attach(t *testing.T, host, repository, tag, text, predicate string, withAuth bool) {
	t.Helper()
	ctx := context.Background()
	repo := registryRepository(t, host, repository, withAuth)

	subject, err := repo.Resolve(ctx, tag)
	require.NoError(t, err)

	store := memory.New()
	bundle := []byte(`{"mediaType":"` + sigstoreBundleType + `","signed":"` + text + `"}`)
	layer := ocispec.Descriptor{MediaType: sigstoreBundleType, Digest: digest.FromBytes(bundle), Size: int64(len(bundle))}
	require.NoError(t, store.Push(ctx, layer, bytes.NewReader(bundle)))
	manifest, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, sigstoreBundleType, oras.PackManifestOptions{
		Subject: &subject,
		Layers:  []ocispec.Descriptor{layer},
		ManifestAnnotations: map[string]string{
			"dev.sigstore.bundle.content":       "dsse-envelope",
			"dev.sigstore.bundle.predicateType": predicate,
		},
	})
	require.NoError(t, err)
	require.NoError(t, oras.CopyGraph(ctx, store, repo, manifest, oras.DefaultCopyGraphOptions))
}

// signatureDigests lists the digests of the signatures that the image at host/repository:tag
// has, read with oras-go and not with cargueiro.
func signatureDigests(t *testing.T, host, repository, tag string, withAuth bool) []string {
	t.Helper()
	ctx := context.Background()
	repo := registryRepository(t, host, repository, withAuth)

	subject, err := repo.Resolve(ctx, tag)
	require.NoError(t, err)
	var found []string
	require.NoError(t, repo.Referrers(ctx, subject, sigstoreBundleType, func(referrers []ocispec.Descriptor) error {
		for _, r := range referrers {
			found = append(found, r.Digest.String())
		}
		return nil
	}))
	return found
}

func TestReleaseNewVersionsCopiesTheSignaturesOfTheImagesItCopies(t *testing.T) {
	s := stageNewVersion(t)
	production := t.TempDir()
	prod := productionPath(t)
	stagingHost, stagingPath, _ := strings.Cut(s.imagesStaging, "/")
	prodHost, prodPath, _ := strings.Cut(prod+"/images", "/")

	// staging has signed the controller, with its provenance next to the signature, and not the agent
	signImage(t, stagingHost, stagingPath+"/fleet", "v0.17.0", "controller", false)
	attestImage(t, stagingHost, stagingPath+"/fleet", "v0.17.0", "controller", false)
	staged := signatureDigests(t, stagingHost, stagingPath+"/fleet", "v0.17.0", false)
	require.Len(t, staged, 2)

	res := releaseNew(t, s, production, prod)

	require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	require.Contains(t, res.Stdout, "image copied "+prod+"/images/fleet:v0.17.0\nsignatures copied "+prod+"/images/fleet:v0.17.0 (1)\n")
	require.Contains(t, res.Stdout, "image copied "+prod+"/images/fleet-agent:v0.17.0\nno signatures to copy "+prod+"/images/fleet-agent:v0.17.0: staging has none\n")
	require.Contains(t, res.Stdout, "released 1 versions, adopted 0\nimages: copied 2, already in production 0, signatures copied for 1\n")

	// what production has is what staging has, byte for byte: slsactl copies the tag that lists
	// what refers to the image, so the provenance comes with the signature, which the count of
	// signatures in the line above leaves out. And the image is the one of staging, with its tag as it was
	require.Equal(t, staged, signatureDigests(t, prodHost, prodPath+"/fleet", "v0.17.0", true))
	require.Empty(t, signatureDigests(t, prodHost, prodPath+"/fleet-agent", "v0.17.0", true), "an image that staging did not sign is not given one")
	stagedImage := manifestDigest(t, harness.OpenRegistry(t)+"/"+stagingPath, "fleet", "v0.17.0", false)
	require.Equal(t, stagedImage, manifestDigest(t, prod+"/images", "fleet", "v0.17.0", true))

	t.Run("a second run has nothing to release and changes nothing", func(t *testing.T) {
		again := releaseNew(t, s, production, prod)

		require.Zero(t, again.ExitCode, again.Stdout+again.Stderr)
		require.Contains(t, again.Stdout, "nothing to release\n")
		require.Equal(t, staged, signatureDigests(t, prodHost, prodPath+"/fleet", "v0.17.0", true))
	})
}

func TestReleaseNewVersionsDoesNotSignAnImageThatIsThere(t *testing.T) {
	s := stageNewVersion(t)
	production := t.TempDir()
	prod := productionPath(t)
	stagingHost, stagingPath, _ := strings.Cut(s.imagesStaging, "/")
	prodHost, prodPath, _ := strings.Cut(prod+"/images", "/")

	// production has the very same controller as staging, with no signature, and staging has signed it
	pushImage(t, prodHost, prodPath+"/fleet", "v0.17.0", "controller", true)
	signImage(t, stagingHost, stagingPath+"/fleet", "v0.17.0", "controller", false)

	res := releaseNew(t, s, production, prod)

	require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	require.Contains(t, res.Stdout, "image present "+prod+"/images/fleet:v0.17.0\n")
	require.NotContains(t, res.Stdout, "signatures copied", "what production has stays as it is")
	require.Empty(t, signatureDigests(t, prodHost, prodPath+"/fleet", "v0.17.0", true))
}

// copyImageSignatures runs `copy-image-signatures` between the registry without a password, which
// has the signatures, and the one with a password, as the production one.
func copyImageSignatures(t *testing.T, image string, args ...string) harness.Result {
	t.Helper()

	all := append([]string{
		"copy-image-signatures", image, "--from", harness.OpenRegistry(t), "--to", harness.HTTPRegistry(t),
		"--plain-http", "--production-username", harness.RegistryUser,
	}, args...)
	return harness.CargueiroWith(t, harness.Options{Dir: t.TempDir(), Env: []string{"CARGUEIRO_PRODUCTION_PASSWORD=" + harness.RegistryPassword}}, all...)
}

// signedImageInBoth puts the same image in both registries, under a repository no other test
// uses, and signs it, with its provenance, in the one that has the signatures.
func signedImageInBoth(t *testing.T) (name string) {
	t.Helper()

	name = fmt.Sprintf("e2e-sign-%d", stamp())
	pushImage(t, harness.OpenRegistry(t), "rancher/"+name, "v1", "controller", false)
	pushImage(t, harness.HTTPRegistry(t), "rancher/"+name, "v1", "controller", true)
	signImage(t, harness.OpenRegistry(t), "rancher/"+name, "v1", "controller", false)
	attestImage(t, harness.OpenRegistry(t), "rancher/"+name, "v1", "controller", false)
	return name
}

func TestCopyImageSignaturesCopiesWhatReferToTheImage(t *testing.T) {
	name := signedImageInBoth(t)
	image := "rancher/" + name + ":v1"
	open, prod := harness.OpenRegistry(t), harness.HTTPRegistry(t)
	staged := signatureDigests(t, open, "rancher/"+name, "v1", false)
	require.Len(t, staged, 2, "the signature and the provenance")
	before := manifestDigest(t, prod+"/rancher", name, "v1", true)

	dry := copyImageSignatures(t, image, "--dry-run")
	require.Zero(t, dry.ExitCode, dry.Stdout+dry.Stderr)
	require.Equal(t, "would copy the signatures of "+image+" (source 1, destination 0)\n", dry.Stdout)
	require.Empty(t, signatureDigests(t, prod, "rancher/"+name, "v1", true), "a dry run writes nothing")

	res := copyImageSignatures(t, image)

	require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	require.Equal(t, "signatures copied "+image+" (source 1, destination before 0, after 1)\n", res.Stdout)
	require.Equal(t, staged, signatureDigests(t, prod, "rancher/"+name, "v1", true), "the same manifests as the source has")
	require.Equal(t, before, manifestDigest(t, prod+"/rancher", name, "v1", true), "the tag of the image is not written")

	again := copyImageSignatures(t, image)
	require.Zero(t, again.ExitCode, again.Stdout+again.Stderr)
	require.Equal(t, staged, signatureDigests(t, prod, "rancher/"+name, "v1", true), "a second run changes nothing")
}

func TestCopyImageSignaturesLeavesTheTagOfTheDestinationAsItIs(t *testing.T) {
	name := signedImageInBoth(t)
	image := "rancher/" + name + ":v1"
	prod := harness.HTTPRegistry(t)
	// the destination has signed the image itself, so it has the tag that lists what refers to it
	signImage(t, prod, "rancher/"+name, "v1", "its own signature", true)
	own := signatureDigests(t, prod, "rancher/"+name, "v1", true)
	require.Len(t, own, 1)

	res := copyImageSignatures(t, image)

	require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	require.Equal(t, own, signatureDigests(t, prod, "rancher/"+name, "v1", true), "what the destination has is not replaced, and the source's is not merged into it")
}

func TestCopyImageSignaturesRefusesWhatItDoesNotCopy(t *testing.T) {
	name := signedImageInBoth(t)

	t.Run("an image that the destination does not have is not copied", func(t *testing.T) {
		missing := fmt.Sprintf("rancher/e2e-nothere-%d:v1", stamp())
		pushImage(t, harness.OpenRegistry(t), strings.SplitN(missing, ":", 2)[0], "v1", "x", false)

		res := copyImageSignatures(t, missing)

		require.Equal(t, 1, res.ExitCode)
		require.Contains(t, res.Stderr, "the image is not in the destination registry")
		require.Empty(t, res.Stdout)
	})

	t.Run("a tag that names another image in the destination", func(t *testing.T) {
		other := fmt.Sprintf("e2e-other-%d", stamp())
		pushImage(t, harness.OpenRegistry(t), "rancher/"+other, "v1", "one", false)
		pushImage(t, harness.HTTPRegistry(t), "rancher/"+other, "v1", "another", true)

		res := copyImageSignatures(t, "rancher/"+other+":v1")

		require.Equal(t, 1, res.ExitCode)
		require.Contains(t, res.Stderr, "in the source registry and")
	})

	t.Run("an image that is not given as repository:tag", func(t *testing.T) {
		res := copyImageSignatures(t, name)

		require.Equal(t, 1, res.ExitCode)
		require.Contains(t, res.Stderr, "give the image as repository:tag")
	})
}
