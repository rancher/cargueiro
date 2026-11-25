package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	repo "helm.sh/helm/v4/pkg/repo/v1"
	"sigs.k8s.io/yaml"

	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/release"
	"github.com/rancher/cargueiro/internal/rules"
)

// releaseStaging writes a staging repository with the legacy entries of the given
// fleet versions, each pinned to the manifest "sha256:<tag>".
func releaseStaging(t *testing.T, versions ...string) string {
	t.Helper()

	dir := t.TempDir()
	for _, v := range versions {
		tag := v
		entry := repo.ChartVersion{
			Metadata: &chart.Metadata{Name: "fleet", Version: v},
			URLs:     []string{"oci://stgregistry.suse.com/rancher/catalog/fleet:" + tag + "@sha256:" + tag},
			Digest:   "abc",
		}
		data, err := yaml.Marshal(&entry)
		require.NoError(t, err)
		path := filepath.Join(dir, "legacy", "fleet", v+".yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, data, 0o600))
	}
	return dir
}

// fakeReleaseRegistry is the production registry, and records how it was made.
type fakeReleaseRegistry struct {
	made     bool
	guard    *oci.Guard
	from, to oci.FetchOptions
	has      map[string]string
	copied   []string
	failCopy map[string]bool
	// the images of staging and of production, by ref, and the ones that were copied
	stagingImages map[string]string
	images        map[string]string
	imagesCopied  []string
	// the images that were asked for their signatures, and the copier the factory was given
	signed         []string
	copySignatures release.SignatureCopier
	// the docker config that was in use when the signatures were copied
	dockerDir, dockerConfig string
}

func (f *fakeReleaseRegistry) factory(_ *rules.Config, guard *oci.Guard, from, to oci.FetchOptions, _ oci.Retry, copySignatures release.SignatureCopier) release.Registry {
	f.made, f.guard, f.from, f.to, f.copySignatures = true, guard, from, to, copySignatures
	return f
}

func (f *fakeReleaseRegistry) CopyImageSignatures(_ context.Context, image release.Image, _ string) (int, error) {
	f.signed = append(f.signed, image.Destination.String())
	// what slsactl would find while the signatures are copied
	f.dockerDir = os.Getenv("DOCKER_CONFIG")
	if data, err := os.ReadFile(filepath.Join(f.dockerDir, "config.json")); err == nil {
		f.dockerConfig = string(data)
	}
	return 0, nil
}

func (f *fakeReleaseRegistry) Inspect(_ context.Context, ref string) (oci.ManifestInfo, error) {
	digest, ok := f.has[ref]
	if !ok {
		return oci.ManifestInfo{}, fmt.Errorf("%s: %w", ref, oci.ErrNotFound)
	}
	return oci.ManifestInfo{ManifestDigest: digest}, nil
}

func (f *fakeReleaseRegistry) Validate(context.Context, release.Item) error { return nil }

func (f *fakeReleaseRegistry) StagingImage(_ context.Context, image oci.ChartRef) (string, error) {
	digest, ok := f.stagingImages[image.String()]
	if !ok {
		return "", fmt.Errorf("%s: %w", image, oci.ErrNotFound)
	}
	return digest, nil
}

func (f *fakeReleaseRegistry) ProductionImage(_ context.Context, image oci.ChartRef) (string, error) {
	digest, ok := f.images[image.String()]
	if !ok {
		return "", fmt.Errorf("%s: %w", image, oci.ErrNotFound)
	}
	return digest, nil
}

func (f *fakeReleaseRegistry) CopyImage(_ context.Context, image release.Image, digest string) error {
	f.images[image.Destination.String()] = digest
	f.imagesCopied = append(f.imagesCopied, image.Destination.String())
	return nil
}

func (f *fakeReleaseRegistry) Copy(_ context.Context, item release.Item) error {
	ref := item.Destination.String()
	if f.failCopy[ref] {
		return fmt.Errorf("the registry is down")
	}
	f.has[ref] = item.ManifestDigest
	f.copied = append(f.copied, ref)
	return nil
}

func runReleaseWith(t *testing.T, fake *fakeReleaseRegistry, env map[string]string, args ...string) (stdout string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer
	opts := &rootOptions{
		stderr:    &errOut,
		getenv:    func(key string) string { return env[key] },
		openStore: openerFor(newTestStore(t)),
		prompt:    &fakePrompter{},
		rules:     []byte(importRules),
	}
	root, err := newRootCmd(&out, &errOut, opts, dependencies{newReleaseRegistry: fake.factory})
	require.NoError(t, err)
	root.SetArgs(append([]string{"release"}, args...))

	err = root.Execute()
	return out.String(), err
}

func TestReleaseCopiesAndWritesTheEntries(t *testing.T) {
	staging, production := releaseStaging(t, "110.0.3_up0.16.3", "110.0.4_up0.16.4"), t.TempDir()
	fake := &fakeReleaseRegistry{has: map[string]string{}}

	stdout, err := runReleaseWith(t, fake, map[string]string{envPassword: "read", envProductionPassword: "write"},
		"--legacy", "--from", staging, "--repo", production, "--username", "reader", "--production-username", "writer", "--plain-http")

	require.NoError(t, err)
	require.Contains(t, stdout, "released fleet 110.0.3_up0.16.3\nreleased fleet 110.0.4_up0.16.4\n")
	require.Contains(t, stdout, "released 2 versions, adopted 0\n")
	require.NotNil(t, fake.guard, "the copy is guarded like a push")
	require.Equal(t, oci.FetchOptions{Username: "reader", Password: "read", PlainHTTP: true}, fake.from)
	require.Equal(t, oci.FetchOptions{Username: "writer", Password: "write", PlainHTTP: true}, fake.to)
	require.FileExists(t, filepath.Join(production, "legacy", "fleet", "110.0.3_up0.16.3.yaml"))

	t.Run("a second run has nothing to do and makes no registry", func(t *testing.T) {
		again := &fakeReleaseRegistry{has: fake.has}

		stdout, err := runReleaseWith(t, again, nil, "--legacy", "--from", staging, "--repo", production)

		require.NoError(t, err)
		require.Equal(t, "nothing to release\n", stdout)
		require.False(t, again.made)
	})
}

func TestReleaseTakesTheProductionCredentialsFromTheEnvironment(t *testing.T) {
	staging := releaseStaging(t, "110.0.3_up0.16.3")
	fake := &fakeReleaseRegistry{has: map[string]string{}}

	_, err := runReleaseWith(t, fake, map[string]string{envProductionUsername: "writer", envProductionPassword: "write"},
		"--legacy", "--from", staging, "--repo", t.TempDir())

	require.NoError(t, err)
	require.Equal(t, oci.FetchOptions{Username: "writer", Password: "write"}, fake.to)
	require.Equal(t, oci.FetchOptions{}, fake.from, "the staging registry is read anonymously without credentials")
}

func TestReleaseCopiesToTheRegistryGiven(t *testing.T) {
	staging := releaseStaging(t, "110.0.3_up0.16.3")
	fake := &fakeReleaseRegistry{has: map[string]string{}}

	_, err := runReleaseWith(t, fake, nil, "--legacy", "--from", staging, "--repo", t.TempDir(), "--registry", "127.0.0.1:5052/e2e")

	require.NoError(t, err)
	require.Equal(t, []string{"127.0.0.1:5052/e2e/fleet:110.0.3_up0.16.3"}, fake.copied)
}

func TestReleaseDryRunDoesNotTouchAnything(t *testing.T) {
	staging, production := releaseStaging(t, "110.0.3_up0.16.3"), t.TempDir()
	fake := &fakeReleaseRegistry{has: map[string]string{}}

	stdout, err := runReleaseWith(t, fake, nil, "--legacy", "--from", staging, "--repo", production, "--dry-run")

	require.NoError(t, err)
	require.Equal(t, "release fleet 110.0.3_up0.16.3\n", stdout)
	require.False(t, fake.made, "the registry was not even made")
	require.NoDirExists(t, filepath.Join(production, "legacy"))
}

func TestReleaseSaysLoudWhatDidNotGoThrough(t *testing.T) {
	staging, production := releaseStaging(t, "110.0.3_up0.16.3", "110.0.4_up0.16.4", "110.0.5_up0.16.5"), t.TempDir()
	fake := &fakeReleaseRegistry{
		has:      map[string]string{"registry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3": "sha256:other"},
		failCopy: map[string]bool{"registry.suse.com/rancher/catalog/fleet:110.0.4_up0.16.4": true},
	}

	stdout, err := runReleaseWith(t, fake, nil, "--legacy", "--from", staging, "--repo", production)

	require.ErrorContains(t, err, "1 versions were not released and 1 are in conflict")
	require.Contains(t, stdout, "released fleet 110.0.5_up0.16.5\n", "the version after the problems is released")
	require.Contains(t, stdout, "CONFLICT: 1 versions")
	require.Contains(t, stdout, "FAILED: 1 versions")
}

func TestReleaseNeedsWhatItReleases(t *testing.T) {
	staging := releaseStaging(t, "110.0.3_up0.16.3")
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"without --from", []string{"--repo", "x"}, "--from is required"},
		{"without --repo", []string{"--from", staging}, "--repo is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runReleaseWith(t, &fakeReleaseRegistry{}, nil, tt.args...)

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestReleaseWorksBetweenOneRegistryAndOneRegistry(t *testing.T) {
	staging := releaseStaging(t, "110.0.3_up0.16.3")
	other := releaseStaging(t, "110.0.4_up0.16.4")
	data, err := os.ReadFile(filepath.Join(other, "legacy", "fleet", "110.0.4_up0.16.4.yaml"))
	require.NoError(t, err)
	moved := bytes.ReplaceAll(data, []byte("stgregistry.suse.com"), []byte("other.example.com"))
	require.NoError(t, os.WriteFile(filepath.Join(staging, "legacy", "fleet", "110.0.4_up0.16.4.yaml"), moved, 0o600))

	_, err = runReleaseWith(t, &fakeReleaseRegistry{has: map[string]string{}}, nil, "--legacy", "--from", staging, "--repo", t.TempDir())

	require.ErrorContains(t, err, "a release works between one registry and one registry")
}

func TestReleaseOnlyTheFamiliesGiven(t *testing.T) {
	staging := releaseStaging(t, "110.0.3_up0.16.3")
	other := releaseStaging(t, "110.0.4_up11.0.4")
	data, err := os.ReadFile(filepath.Join(other, "legacy", "fleet", "110.0.4_up11.0.4.yaml"))
	require.NoError(t, err)
	backup := bytes.ReplaceAll(bytes.ReplaceAll(data, []byte("name: fleet"), []byte("name: rancher-backup")), []byte("catalog/fleet:"), []byte("catalog/rancher-backup:"))
	require.NoError(t, os.MkdirAll(filepath.Join(staging, "legacy", "rancher-backup"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(staging, "legacy", "rancher-backup", "110.0.4_up11.0.4.yaml"), backup, 0o600))

	stdout, err := runReleaseWith(t, &fakeReleaseRegistry{has: map[string]string{}}, nil,
		"--legacy", "--from", staging, "--repo", t.TempDir(), "--family", "fleet", "--dry-run")

	require.NoError(t, err)
	require.Equal(t, "release fleet 110.0.3_up0.16.3\n", stdout)

	t.Run("an unknown family is an error", func(t *testing.T) {
		_, err := runReleaseWith(t, &fakeReleaseRegistry{has: map[string]string{}}, nil,
			"--legacy", "--from", staging, "--repo", t.TempDir(), "--family", "nope", "--dry-run")

		require.ErrorContains(t, err, `family "nope" is not listed in the rules file`)
	})
}

// releaseStagingNew writes a staging repository with new versions of fleet: a final one that
// lists two images, a release candidate, and a withdrawn one.
func releaseStagingNew(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	write := func(version, extra string) {
		tag := strings.ReplaceAll(version, "+", "_")
		text := "annotations:\n  helm.sh/images: |\n    - name: fleet\n      image: rancher/fleet:v0.17.0\n    - name: agent\n      image: rancher/fleet-agent:v0.17.0\n" +
			"apiVersion: v2\nname: fleet\nversion: " + version + "\n" + extra +
			"urls:\n- oci://stgregistry.suse.com/rancher/catalog/fleet:" + tag + "@sha256:a1\n"
		path := filepath.Join(dir, "entries", "fleet", version+".yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	}
	write("111.0.2+up0.17.0", "")
	write("111.0.1+up0.17.0-rc.4", "")
	write("111.0.3+up0.17.1", "withdrawn: true\n")
	return dir
}

func newReleaseFake() *fakeReleaseRegistry {
	return &fakeReleaseRegistry{
		has: map[string]string{}, images: map[string]string{},
		stagingImages: map[string]string{
			"stgregistry.suse.com/rancher/fleet:v0.17.0":       "sha256:img1",
			"stgregistry.suse.com/rancher/fleet-agent:v0.17.0": "sha256:img2",
		},
	}
}

func TestReleaseNewVersionsWithoutLegacy(t *testing.T) {
	staging, production := releaseStagingNew(t), t.TempDir()
	fake := newReleaseFake()

	stdout, err := runReleaseWith(t, fake, nil, "--from", staging, "--repo", production)

	require.NoError(t, err)
	require.Contains(t, stdout, "image copied registry.suse.com/rancher/fleet:v0.17.0\n")
	require.Contains(t, stdout, "released fleet 111.0.2+up0.17.0\n")
	require.Contains(t, stdout, "released 1 versions, adopted 0\nimages: copied 2, already in production 0\n")
	require.NotContains(t, stdout, "111.0.1+up0.17.0-rc.4", "a release candidate is not released")
	require.Equal(t, []string{"registry.suse.com/rancher/fleet:v0.17.0", "registry.suse.com/rancher/fleet-agent:v0.17.0"}, fake.imagesCopied)
	require.FileExists(t, filepath.Join(production, "entries", "fleet", "111.0.2+up0.17.0.yaml"))
	require.NoDirExists(t, filepath.Join(production, "legacy"))
	require.NoFileExists(t, filepath.Join(production, "entries", "fleet", "111.0.1+up0.17.0-rc.4.yaml"))
}

func TestReleaseNewVersionsCopiesTheSignaturesOfTheImagesItCopies(t *testing.T) {
	staging, production := releaseStagingNew(t), t.TempDir()
	fake := newReleaseFake()
	t.Setenv("DOCKER_CONFIG", "/the/config/of/the/user")

	_, err := runReleaseWith(t, fake, map[string]string{envPassword: "read", envProductionPassword: "write"},
		"--from", staging, "--repo", production, "--username", "reader", "--production-username", "writer")

	require.NoError(t, err)
	require.NotNil(t, fake.copySignatures, "the registry is given the copy of slsactl")
	require.Equal(t, []string{"registry.suse.com/rancher/fleet:v0.17.0", "registry.suse.com/rancher/fleet-agent:v0.17.0"}, fake.signed,
		"every image that was copied is asked for its signatures")

	// slsactl reads the docker config, so each registry has its own pair in a config for this run
	var config struct {
		Auths map[string]struct{ Auth string }
	}
	require.NoError(t, json.Unmarshal([]byte(fake.dockerConfig), &config))
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("reader:read")), config.Auths["stgregistry.suse.com"].Auth)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("writer:write")), config.Auths["registry.suse.com"].Auth)
	require.NotEqual(t, "/the/config/of/the/user", fake.dockerDir)

	// and what was there is put back, and the config with the passwords is removed
	require.Equal(t, "/the/config/of/the/user", os.Getenv("DOCKER_CONFIG"))
	require.NoDirExists(t, fake.dockerDir)
}

func TestReleaseNewVersionsDryRunSaysWhatIsLeftOut(t *testing.T) {
	staging, production := releaseStagingNew(t), t.TempDir()
	fake := newReleaseFake()

	stdout, err := runReleaseWith(t, fake, nil, "--from", staging, "--repo", production, "--dry-run")

	require.NoError(t, err)
	require.Equal(t, ""+
		"skip fleet 111.0.1+up0.17.0-rc.4: release candidate\n"+
		"skip fleet 111.0.3+up0.17.1: withdrawn\n"+
		"release fleet 111.0.2+up0.17.0\n"+
		"  image stgregistry.suse.com/rancher/fleet:v0.17.0\n"+
		"  image stgregistry.suse.com/rancher/fleet-agent:v0.17.0\n", stdout)
	require.False(t, fake.made, "no registry is made, and nothing is written")
	require.NoDirExists(t, filepath.Join(production, "entries"))
}

func TestReleaseImagesFromAnotherRegistryThanTheChartsAreRefused(t *testing.T) {
	staging := releaseStagingNew(t)
	fake := newReleaseFake()
	fake.stagingImages = map[string]string{
		"127.0.0.1:5050/stg/fleet:v0.17.0":       "sha256:img1",
		"127.0.0.1:5050/stg/fleet-agent:v0.17.0": "sha256:img2",
	}

	_, err := runReleaseWith(t, fake, nil, "--from", staging, "--repo", t.TempDir(), "--plain-http",
		"--registry", "127.0.0.1:5052/charts", "--images-registry", "127.0.0.1:5052/images", "--staging-images-registry", "127.0.0.1:5050/stg")

	require.Error(t, err, "the charts and the images are read from different registries than they are written to, and a release works between one and one")
	require.ErrorContains(t, err, "a release works between one registry and one registry")
}

func TestReleaseImageRegistriesAreNotForLegacy(t *testing.T) {
	_, err := runReleaseWith(t, newReleaseFake(), nil, "--legacy", "--from", t.TempDir(), "--repo", t.TempDir(), "--images-registry", "x.io/images")

	require.ErrorContains(t, err, "are for new versions")
}

func TestReleaseWithNothingNewSaysSo(t *testing.T) {
	staging := t.TempDir()
	fake := newReleaseFake()

	stdout, err := runReleaseWith(t, fake, nil, "--from", staging, "--repo", t.TempDir())

	require.NoError(t, err)
	require.Equal(t, "nothing to release\n", stdout)
	require.False(t, fake.made)
}
