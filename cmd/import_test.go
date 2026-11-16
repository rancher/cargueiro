package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	repo "helm.sh/helm/v4/pkg/repo/v1"

	"github.com/rancher/cargueiro/internal/importer"
	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/version"
)

const importRules = `
kinds:
  main: {}
  crd: {}
families:
  - family: fleet
    charts:
      - { name: fleet, kind: main, edition: rancher-charts }
      - { name: fleet-crd, kind: crd, edition: rancher-charts }
registries:
  rancher-charts:
    charts-staging: stgregistry.suse.com/rancher/catalog
    charts-production: registry.suse.com/rancher/catalog
  prime-charts:
    charts-staging: stgregistry.suse.com/rancher/charts
    charts-production: registry.suse.com/rancher/charts
  images:
    staging: stgregistry.suse.com/rancher
    production: registry.suse.com/rancher
versions:
  prereleases: [rc]
  majors: {"2.14": 109, "2.15": 110, "2.16": 111}
`

// importSource writes a checkout of the old system with the given versions of
// the fleet charts, each with an archive the index has the digest of.
func importSource(t *testing.T, versions map[string][]string) string {
	t.Helper()

	dir := t.TempDir()
	idx := &repo.IndexFile{APIVersion: "v1", Entries: map[string]repo.ChartVersions{}}
	for name, vs := range versions {
		for _, v := range vs {
			rel := "assets/" + name + "/" + name + "-" + v + ".tgz"
			path := filepath.Join(dir, rel)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
			data := importArchive(t, name, v)
			require.NoError(t, os.WriteFile(path, data, 0o600))
			sum := sha256.Sum256(data)
			idx.Entries[name] = append(idx.Entries[name], &repo.ChartVersion{
				Metadata: &chart.Metadata{Name: name, Version: v},
				URLs:     []string{rel},
				Digest:   hex.EncodeToString(sum[:]),
			})
		}
	}
	require.NoError(t, idx.WriteFile(filepath.Join(dir, "index.yaml"), 0o600))
	return dir
}

func runImport(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer
	opts := &rootOptions{
		stderr:    &errOut,
		getenv:    func(string) string { return "" },
		openStore: openerFor(newTestStore(t)),
		prompt:    &fakePrompter{},
		rules:     []byte(importRules),
	}
	root, err := newRootCmd(&out, &errOut, opts, dependencies{})
	require.NoError(t, err)
	root.SetArgs(append([]string{"import"}, args...))

	err = root.Execute()
	return out.String(), err
}

func TestImportReportsWhatItWouldImport(t *testing.T) {
	source := importSource(t, map[string][]string{
		"fleet":     {"110.0.3+up0.16.3", "110.0.4+up0.16.4", "109.0.1+up0.15.1"},
		"fleet-crd": {"110.0.3+up0.16.3"},
	})
	repoDir := t.TempDir()

	stdout, err := runImport(t, "--source", source, "--repo", repoDir, "--family", "fleet", "--minor", "2.15", "--dry-run")

	require.NoError(t, err)
	require.Equal(t, "import fleet 110.0.4+up0.16.4\nimport fleet 110.0.3+up0.16.3\nimport fleet-crd 110.0.3+up0.16.3\n", stdout)
	entries, err := os.ReadDir(repoDir)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing is written")

	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "legacy", "fleet"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "legacy", "fleet", "110.0.4+up0.16.4.yaml"), []byte("x"), 0o600))
	stdout, err = runImport(t, "--source", source, "--repo", repoDir, "--family", "fleet", "--minor", "2.14", "--dry-run")
	require.NoError(t, err)
	require.Equal(t, "import fleet 109.0.1+up0.15.1\n", stdout, "another minor, and what is in legacy/ is not planned")
}

func TestImportFails(t *testing.T) {
	source := importSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}})
	repoDir := t.TempDir()
	base := []string{"--source", source, "--repo", repoDir, "--family", "fleet", "--minor", "2.15"}
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no source", args: []string{"--repo", repoDir, "--family", "fleet", "--minor", "2.15", "--dry-run"}, wantErr: "--source is required"},
		{name: "no repo", args: []string{"--source", source, "--family", "fleet", "--minor", "2.15", "--dry-run"}, wantErr: "--repo is required"},
		{name: "no family", args: []string{"--source", source, "--repo", repoDir, "--minor", "2.15", "--dry-run"}, wantErr: "--family is required"},
		{name: "no minor", args: []string{"--source", source, "--repo", repoDir, "--family", "fleet", "--dry-run"}, wantErr: "--minor is required"},
		{name: "a family that is not in the rules", args: []string{"--source", source, "--repo", repoDir, "--family", "nope", "--minor", "2.15", "--dry-run"}, wantErr: `family "nope" is not listed`},
		{name: "a minor of the new system", args: []string{"--source", source, "--repo", repoDir, "--family", "fleet", "--minor", "2.16", "--dry-run"}, wantErr: "the charts of Rancher 2.16 are made by the new system"},
		{name: "a source with no index", args: []string{"--source", t.TempDir(), "--repo", repoDir, "--family", "fleet", "--minor", "2.15", "--dry-run"}, wantErr: "source: there is no index.yaml"},
		{name: "an argument", args: append(append([]string{}, base...), "--dry-run", "extra"), wantErr: "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, err := runImport(t, tt.args...)

			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, stdout)
		})
	}
}

// importArchive returns a chart archive that holds a Chart.yaml of the chart.
func importArchive(t *testing.T, name, ver string) []byte {
	t.Helper()

	chartYAML := "apiVersion: v2\nname: " + name + "\nversion: " + ver + "\n"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: name + "/Chart.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(chartYAML))}))
	_, err := tw.Write([]byte(chartYAML))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// fakeImportRegistry keeps what is pushed to it, and records how it was made.
type fakeImportRegistry struct {
	target oci.Target
	opts   oci.ClientOptions
	guard  *oci.Guard
	stored map[string]oci.ManifestInfo
	pushed []string
	// failPush makes every push fail.
	failPush error
}

func (f *fakeImportRegistry) factory(target oci.Target, guard *oci.Guard, opts oci.ClientOptions) (importer.Registry, error) {
	f.target, f.guard, f.opts = target, guard, opts
	return f, nil
}

func (f *fakeImportRegistry) Inspect(_ context.Context, ref string) (oci.ManifestInfo, error) {
	info, found := f.stored[ref]
	if !found {
		return oci.ManifestInfo{}, fmt.Errorf("%s: %w", ref, oci.ErrNotFound)
	}
	return info, nil
}

func (f *fakeImportRegistry) Push(_ context.Context, archive string) error {
	if f.failPush != nil {
		return f.failPush
	}
	name, ver, err := oci.ReadIdentity(archive)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	ref := f.target.Ref(name, version.ToOCITag(ver))
	f.stored[ref] = oci.ManifestInfo{ManifestDigest: "sha256:" + hex.EncodeToString(sum[:]), ChartDigest: "sha256:" + hex.EncodeToString(sum[:])}
	f.pushed = append(f.pushed, name+" "+ver)
	return nil
}

func runImportWith(t *testing.T, fake *fakeImportRegistry, env map[string]string, args ...string) (stdout string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer
	opts := &rootOptions{
		stderr:    &errOut,
		getenv:    func(key string) string { return env[key] },
		openStore: openerFor(newTestStore(t)),
		prompt:    &fakePrompter{},
		rules:     []byte(importRules),
	}
	root, err := newRootCmd(&out, &errOut, opts, dependencies{newImportRegistry: fake.factory})
	require.NoError(t, err)
	root.SetArgs(append([]string{"import"}, args...))

	err = root.Execute()
	return out.String(), err
}

func TestImportPushesAndWritesTheEntries(t *testing.T) {
	source := importSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}, "fleet-crd": {"110.0.3+up0.16.3"}})
	repoDir := t.TempDir()
	fake := &fakeImportRegistry{stored: map[string]oci.ManifestInfo{}}

	stdout, err := runImportWith(t, fake, map[string]string{envPassword: "secret"},
		"--source", source, "--repo", repoDir, "--family", "fleet", "--minor", "2.15", "--username", "me", "--plain-http")

	require.NoError(t, err)
	require.Equal(t, "pushed fleet 110.0.3+up0.16.3\npushed fleet-crd 110.0.3+up0.16.3\n", stdout)
	require.Equal(t, oci.Target{Host: "stgregistry.suse.com", Path: "rancher/catalog"}, fake.target, "the staging registry of the edition")
	require.NotNil(t, fake.guard, "the push is guarded like any other")
	require.Equal(t, "me", fake.opts.Username)
	require.Equal(t, "secret", fake.opts.Password)
	require.True(t, fake.opts.PlainHTTP)
	require.FileExists(t, filepath.Join(repoDir, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
	require.FileExists(t, filepath.Join(repoDir, "legacy", "fleet-crd", "110.0.3+up0.16.3.yaml"))

	t.Run("a second run has nothing to do", func(t *testing.T) {
		stdout, err := runImportWith(t, fake, nil, "--source", source, "--repo", repoDir, "--family", "fleet", "--minor", "2.15")

		require.NoError(t, err)
		require.Equal(t, "nothing to import\n", stdout)
	})
}

func TestImportPushesToTheRegistryGiven(t *testing.T) {
	source := importSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}})
	fake := &fakeImportRegistry{stored: map[string]oci.ManifestInfo{}}

	_, err := runImportWith(t, fake, nil, "--source", source, "--repo", t.TempDir(), "--family", "fleet", "--minor", "2.15", "--registry", "127.0.0.1:5052/e2e")

	require.NoError(t, err)
	require.Equal(t, oci.Target{Host: "127.0.0.1:5052", Path: "e2e"}, fake.target)
	require.False(t, fake.opts.PlainHTTP)
	require.Empty(t, fake.opts.Username, "anonymous without credentials")
}

func TestImportDoesNotTouchTheRegistryOnADryRun(t *testing.T) {
	source := importSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}})
	fake := &fakeImportRegistry{stored: map[string]oci.ManifestInfo{}}

	stdout, err := runImportWith(t, fake, nil, "--source", source, "--repo", t.TempDir(), "--family", "fleet", "--minor", "2.15", "--dry-run")

	require.NoError(t, err)
	require.Equal(t, "import fleet 110.0.3+up0.16.3\n", stdout)
	require.Nil(t, fake.guard, "the registry was not even made")
	require.Empty(t, fake.pushed)
}

func TestImportFailureKeepsWhatWasDone(t *testing.T) {
	source := importSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}})
	repoDir := t.TempDir()
	fake := &fakeImportRegistry{stored: map[string]oci.ManifestInfo{}, failPush: errors.New("the registry is down")}

	stdout, err := runImportWith(t, fake, nil, "--source", source, "--repo", repoDir, "--family", "fleet", "--minor", "2.15")

	require.ErrorContains(t, err, "the registry is down")
	require.Empty(t, stdout)
	require.NoDirExists(t, filepath.Join(repoDir, "legacy"))
}
