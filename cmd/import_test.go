package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	repo "helm.sh/helm/v4/pkg/repo/v1"
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
			data := []byte("archive of " + name + " " + v)
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
		{name: "without --dry-run", args: base, wantErr: "give --dry-run"},
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
