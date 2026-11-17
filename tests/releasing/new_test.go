//go:build e2e

package releasing

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	fleetVersion = "111.0.2+up0.17.0"
	fleetTag     = "111.0.2_up0.17.0"
)

// pushImage puts a small image in a registry at host/repository:tag, with content that is
// different for each text, and returns the digest of its manifest. withAuth says the
// registry asks for the password of the e2e registries.
func pushImage(t *testing.T, host, repository, tag, text string, withAuth bool) string {
	t.Helper()
	ctx := context.Background()

	store := memory.New()
	config := []byte(`{"architecture":"amd64","os":"linux","config":{}}`)
	configDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(config), Size: int64(len(config))}
	layer := []byte("layer of " + text)
	layerDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayer, Digest: digest.FromBytes(layer), Size: int64(len(layer))}
	require.NoError(t, store.Push(ctx, configDesc, bytes.NewReader(config)))
	require.NoError(t, store.Push(ctx, layerDesc, bytes.NewReader(layer)))
	manifest, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, "", oras.PackManifestOptions{
		ConfigDescriptor: &configDesc,
		Layers:           []ocispec.Descriptor{layerDesc},
	})
	require.NoError(t, err)
	require.NoError(t, store.Tag(ctx, manifest, tag))

	repo, err := remote.NewRepository(host + "/" + repository)
	require.NoError(t, err)
	repo.PlainHTTP = true
	if withAuth {
		repo.Client = &auth.Client{Credential: auth.StaticCredential(host, auth.Credential{Username: harness.RegistryUser, Password: harness.RegistryPassword})}
	}
	_, err = oras.Copy(ctx, store, tag, repo, tag, oras.DefaultCopyOptions)
	require.NoError(t, err)
	return manifest.Digest.String()
}

// fleetArchive is a chart that follows the rules of the catalog, and lists the two images.
func fleetArchive(t *testing.T) []byte {
	t.Helper()

	files := map[string]string{
		"fleet/Chart.yaml": `apiVersion: v2
name: fleet
version: ` + fleetVersion + `
appVersion: 0.17.0
annotations:
  catalog.cattle.io/certified: rancher
  catalog.cattle.io/release-name: fleet
  catalog.cattle.io/rancher-version: ">= 2.16.0-0 < 2.17.0-0"
  catalog.cattle.io/kube-version: ">= 1.34.0-0 < 1.37.0-0"
  catalog.cattle.io/permits-os: linux
  helm.sh/images: |
    - name: fleet
      image: rancher/fleet:v0.17.0
    - name: fleet-agent
      image: rancher/fleet-agent:v0.17.0
`,
		"fleet/values.yaml": "image:\n  repository: rancher/fleet\n  tag: v0.17.0\nagentImage:\n  repository: rancher/fleet-agent\n  tag: v0.17.0\n",
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"fleet/Chart.yaml", "fleet/values.yaml"} {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[name]))}))
		_, err := tw.Write([]byte(files[name]))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// newStaging prepares what a release of new versions needs: the chart and its two images in
// the staging registry, and a staging repository with its entry, a release candidate and a
// withdrawn version. It returns the repository and the paths in the registries.
type newStaging struct {
	repo                          string
	chartsRegistry, imagesStaging string
	chartDigest                   string
}

func stageNewVersion(t *testing.T) newStaging {
	t.Helper()

	host := harness.OpenRegistry(t)
	base := fmt.Sprintf("%s/e2e/%d", host, stamp())
	s := newStaging{repo: t.TempDir(), chartsRegistry: base + "/staging-charts", imagesStaging: base + "/staging-images"}

	archive := filepath.Join(t.TempDir(), "fleet-"+fleetVersion+".tgz")
	require.NoError(t, os.WriteFile(archive, fleetArchive(t), 0o600))
	session := harness.NewSession(t)
	login := session.Login(t, host, []string{"--plain-http"})
	require.Zero(t, login.ExitCode, login.Stderr)
	res := session.Push(t, s.chartsRegistry, archive)
	require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	s.chartDigest = manifestDigest(t, s.chartsRegistry, "fleet", fleetTag, false)

	_, imagesPath, _ := strings.Cut(s.imagesStaging, "/")
	pushImage(t, host, imagesPath+"/fleet", "v0.17.0", "controller", false)
	pushImage(t, host, imagesPath+"/fleet-agent", "v0.17.0", "agent", false)

	entry := func(version, extra string) {
		tag := strings.ReplaceAll(version, "+", "_")
		text := "annotations:\n" +
			"  catalog.cattle.io/certified: rancher\n" +
			"  catalog.cattle.io/release-name: fleet\n" +
			"  catalog.cattle.io/rancher-version: '>= 2.16.0-0 < 2.17.0-0'\n" +
			"  catalog.cattle.io/kube-version: '>= 1.34.0-0 < 1.37.0-0'\n" +
			"  catalog.cattle.io/permits-os: linux\n" +
			"  helm.sh/images: |\n    - name: fleet\n      image: rancher/fleet:v0.17.0\n    - name: fleet-agent\n      image: rancher/fleet-agent:v0.17.0\n" +
			"apiVersion: v2\nappVersion: 0.17.0\nname: fleet\nversion: " + version + "\ncreated: \"2026-11-17T10:00:00Z\"\n" + extra +
			"urls:\n- oci://" + s.chartsRegistry + "/fleet:" + tag + "@" + s.chartDigest + "\n"
		write(t, filepath.Join(s.repo, "entries", "fleet", version+".yaml"), text)
	}
	entry(fleetVersion, "")
	entry("111.0.1+up0.17.0-rc.4", "")
	entry("111.0.3+up0.17.1", "withdrawn: true\n")
	return s
}

// releaseNew runs `release` for the new versions into a production repository and the
// registries of one test.
func releaseNew(t *testing.T, s newStaging, production, prod string, args ...string) harness.Result {
	t.Helper()

	all := append([]string{
		"release", "--from", s.repo, "--repo", production,
		"--registry", prod + "/charts", "--images-registry", prod + "/images", "--staging-images-registry", s.imagesStaging,
		"--plain-http", "--production-username", harness.RegistryUser,
	}, args...)
	return harness.CargueiroWith(t, harness.Options{Dir: t.TempDir(), Env: []string{"CARGUEIRO_PRODUCTION_PASSWORD=" + harness.RegistryPassword}}, all...)
}

func TestReleaseNewVersionsCopiesTheChartAndItsImages(t *testing.T) {
	s := stageNewVersion(t)
	production := t.TempDir()
	prod := productionPath(t)

	dry := releaseNew(t, s, production, prod, "--dry-run")
	require.Zero(t, dry.ExitCode, dry.Stderr)
	require.Contains(t, dry.Stdout, "skip fleet 111.0.1+up0.17.0-rc.4: release candidate\n")
	require.Contains(t, dry.Stdout, "skip fleet 111.0.3+up0.17.1: withdrawn\n")
	require.Contains(t, dry.Stdout, "release fleet "+fleetVersion+"\n")
	require.NoDirExists(t, filepath.Join(production, "entries"), "a dry run writes nothing")

	res := releaseNew(t, s, production, prod)

	require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	require.Contains(t, res.Stdout, "image copied "+prod+"/images/fleet:v0.17.0\n")
	require.Contains(t, res.Stdout, "image copied "+prod+"/images/fleet-agent:v0.17.0\n")
	require.Contains(t, res.Stdout, "released fleet "+fleetVersion+"\n")
	require.Contains(t, res.Stdout, "released 1 versions, adopted 0\nimages: copied 2, already in production 0\n")
	require.NotContains(t, res.Stdout, "rc.4\n", "the release candidate is not released")

	// the manifests are the ones of staging, byte for byte
	require.Equal(t, s.chartDigest, manifestDigest(t, prod+"/charts", "fleet", fleetTag, true))
	_, imagesPath, _ := strings.Cut(s.imagesStaging, "/")
	stagedImage := manifestDigest(t, harness.OpenRegistry(t)+"/"+imagesPath, "fleet", "v0.17.0", false)
	require.Equal(t, stagedImage, manifestDigest(t, prod+"/images", "fleet", "v0.17.0", true), "the image has the manifest of staging")

	staged := readFile(t, filepath.Join(s.repo, "entries", "fleet", fleetVersion+".yaml"))
	got := readFile(t, filepath.Join(production, "entries", "fleet", fleetVersion+".yaml"))
	require.Equal(t, strings.Replace(staged, "oci://"+s.chartsRegistry, "oci://"+prod+"/charts", 1), got, "the entry only changed the registry of its url")
	require.NoFileExists(t, filepath.Join(production, "entries", "fleet", "111.0.1+up0.17.0-rc.4.yaml"))
	require.NoFileExists(t, filepath.Join(production, "entries", "fleet", "111.0.3+up0.17.1.yaml"))
	require.NoDirExists(t, filepath.Join(production, "legacy"))

	t.Run("index builds the production index from the production registry, and audit accepts it", func(t *testing.T) {
		env := harness.Options{Dir: t.TempDir(), Env: []string{"CARGUEIRO_PASSWORD=" + harness.RegistryPassword}}
		idx := harness.CargueiroWith(t, env, "index", "--repo", production, "--registry", prod+"/charts", "--plain-http", "--username", harness.RegistryUser)
		require.Zero(t, idx.ExitCode, idx.Stdout+idx.Stderr)
		require.Contains(t, idx.Stdout, "add fleet "+fleetVersion)

		audit := harness.CargueiroWith(t, env, "audit", "--repo", production, "--production", "--registry", prod+"/charts", "--plain-http", "--username", harness.RegistryUser)
		require.Zero(t, audit.ExitCode, audit.Stdout+audit.Stderr)
		require.Contains(t, audit.Stdout, "1 versions")
	})

	t.Run("a second run has nothing to release", func(t *testing.T) {
		again := releaseNew(t, s, production, prod)

		require.Zero(t, again.ExitCode, again.Stdout+again.Stderr)
		require.Contains(t, again.Stdout, "nothing to release\n")
	})
}

func TestReleaseNewVersionsLeavesAnImageThatIsThere(t *testing.T) {
	s := stageNewVersion(t)
	production := t.TempDir()
	prod := productionPath(t)
	host, imagesPath, _ := strings.Cut(prod+"/images", "/")
	// production has the controller already, with other content than staging has
	there := pushImage(t, host, imagesPath+"/fleet", "v0.17.0", "another controller", true)

	res := releaseNew(t, s, production, prod)

	require.Zero(t, res.ExitCode, "an image that is there is not a failure: "+res.Stdout+res.Stderr)
	require.Contains(t, res.Stdout, "image present "+prod+"/images/fleet:v0.17.0\n")
	require.Contains(t, res.Stdout, "image copied "+prod+"/images/fleet-agent:v0.17.0\n")
	require.Contains(t, res.Stdout, "images: copied 1, already in production 1")
	require.Equal(t, there, manifestDigest(t, prod+"/images", "fleet", "v0.17.0", true), "and it is not replaced")
	require.FileExists(t, filepath.Join(production, "entries", "fleet", fleetVersion+".yaml"))
}

func TestReleaseNewVersionsDoesNotReleaseAChartWhoseImageIsNowhere(t *testing.T) {
	s := stageNewVersion(t)
	production := t.TempDir()
	prod := productionPath(t)
	// the staging registry of the images is another path, where the agent is not
	s.imagesStaging += "-empty"
	host, imagesPath, _ := strings.Cut(s.imagesStaging, "/")
	pushImage(t, host, imagesPath+"/fleet", "v0.17.0", "controller", false)

	res := releaseNew(t, s, production, prod)

	require.Equal(t, 1, res.ExitCode)
	require.Contains(t, res.Stdout, "failed fleet "+fleetVersion)
	require.Contains(t, res.Stdout, "is in neither registry")
	require.Contains(t, res.Stdout, "FAILED: 1 versions were not released")
	require.NoFileExists(t, filepath.Join(production, "entries", "fleet", fleetVersion+".yaml"))
}
