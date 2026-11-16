package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	repo "helm.sh/helm/v4/pkg/repo/v1"
	"sigs.k8s.io/yaml"

	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/release"
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
}

func (f *fakeReleaseRegistry) factory(guard *oci.Guard, from, to oci.FetchOptions, _ oci.Retry) release.Registry {
	f.made, f.guard, f.from, f.to = true, guard, from, to
	return f
}

func (f *fakeReleaseRegistry) Inspect(_ context.Context, ref string) (oci.ManifestInfo, error) {
	digest, ok := f.has[ref]
	if !ok {
		return oci.ManifestInfo{}, fmt.Errorf("%s: %w", ref, oci.ErrNotFound)
	}
	return oci.ManifestInfo{ManifestDigest: digest}, nil
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
		{"without --legacy", []string{"--from", staging, "--repo", "x"}, "--legacy is required"},
		{"without --from", []string{"--legacy", "--repo", "x"}, "--from is required"},
		{"without --repo", []string{"--legacy", "--from", staging}, "--repo is required"},
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
