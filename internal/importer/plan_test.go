package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	repo "helm.sh/helm/v4/pkg/repo/v1"
)

// writeSource makes a checkout of the old system with the versions of each chart:
// an archive for each, and an index that has the sha256 of the archive.
func writeSource(t *testing.T, versions map[string][]string) string {
	t.Helper()

	dir := t.TempDir()
	idx := &repo.IndexFile{APIVersion: "v1", Entries: map[string]repo.ChartVersions{}}
	for name, vs := range versions {
		for _, v := range vs {
			rel := "assets/" + name + "/" + name + "-" + v + ".tgz"
			data := []byte("archive of " + name + " " + v)
			writeFile(t, filepath.Join(dir, rel), string(data))
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// itemNames returns "chart version" for each item of the plan, in order.
func itemNames(plan Plan) []string {
	out := make([]string, 0, len(plan.Import))
	for _, item := range plan.Import {
		out = append(out, item.Chart+" "+item.Entry.Version)
	}
	return out
}

func TestMakePlan(t *testing.T) {
	source := writeSource(t, map[string][]string{
		"fleet":     {"110.0.3+up0.16.3", "110.0.4+up0.16.4"},
		"fleet-crd": {"110.0.3+up0.16.3"},
	})
	repoDir := t.TempDir()
	cfg := loadRules(t)

	plan, err := MakePlan(cfg, Options{Source: source, Repo: repoDir}, "fleet", "2.15")

	require.NoError(t, err)
	require.Equal(t, []string{"fleet 110.0.4+up0.16.4", "fleet 110.0.3+up0.16.3", "fleet-crd 110.0.3+up0.16.3"}, itemNames(plan))
	require.Equal(t, "stgregistry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3", plan.Import[1].Reference, "the staging registry of the edition, with the tag as the registry stores it")
	require.Equal(t, filepath.Join(source, "assets", "fleet", "fleet-110.0.3+up0.16.3.tgz"), plan.Import[1].Archive)
	require.Equal(t, "import fleet 110.0.4+up0.16.4\nimport fleet 110.0.3+up0.16.3\nimport fleet-crd 110.0.3+up0.16.3\n", plan.Report())

	t.Run("the registry given replaces the staging one", func(t *testing.T) {
		plan, err := MakePlan(cfg, Options{Source: source, Repo: repoDir, Registry: "127.0.0.1:5052/e2e"}, "fleet", "2.15")

		require.NoError(t, err)
		require.Equal(t, "127.0.0.1:5052/e2e/fleet-crd:110.0.3_up0.16.3", plan.Import[2].Reference)
	})

	t.Run("a version that is in legacy/ already is left alone", func(t *testing.T) {
		writeFile(t, filepath.Join(repoDir, "legacy", "fleet", "110.0.3+up0.16.3.yaml"), "x")
		writeFile(t, filepath.Join(repoDir, "legacy", "fleet-crd", "110.0.3+up0.16.3.yaml"), "x")

		plan, err := MakePlan(cfg, Options{Source: source, Repo: repoDir}, "fleet", "2.15")

		require.NoError(t, err)
		require.Equal(t, []string{"fleet 110.0.4+up0.16.4"}, itemNames(plan))
	})

	t.Run("and when everything is there, there is nothing to import", func(t *testing.T) {
		writeFile(t, filepath.Join(repoDir, "legacy", "fleet", "110.0.4+up0.16.4.yaml"), "x")

		plan, err := MakePlan(cfg, Options{Source: source, Repo: repoDir}, "fleet", "2.15")

		require.NoError(t, err)
		require.Empty(t, plan.Import)
		require.Equal(t, "nothing to import\n", plan.Report())
	})
}

func TestMakePlanFails(t *testing.T) {
	const version = "110.0.3+up0.16.3"
	good := func(t *testing.T) string { return writeSource(t, map[string][]string{"fleet": {version}}) }
	tests := []struct {
		name    string
		source  func(t *testing.T) string
		prepare func(t *testing.T, source, repoDir string)
		wantErr string
	}{
		{
			name:    "a source with no index",
			source:  func(t *testing.T) string { return t.TempDir() },
			wantErr: "source: there is no index.yaml in ",
		},
		{
			name:   "an archive that is missing",
			source: good,
			prepare: func(t *testing.T, source, _ string) {
				require.NoError(t, os.Remove(filepath.Join(source, "assets", "fleet", "fleet-"+version+".tgz")))
			},
			wantErr: "fleet " + version + ": read the archive",
		},
		{
			name:   "an archive that is not the one of the index",
			source: good,
			prepare: func(t *testing.T, source, _ string) {
				writeFile(t, filepath.Join(source, "assets", "fleet", "fleet-"+version+".tgz"), "another archive")
			},
			wantErr: "but the index of the source says",
		},
		{
			name:   "a version that is an entry of the new system already",
			source: good,
			prepare: func(t *testing.T, _, repoDir string) {
				writeFile(t, filepath.Join(repoDir, "entries", "fleet", version+".yaml"), "x")
			},
			wantErr: "fleet " + version + " is an entry of the new system already: entries/fleet/" + version + ".yaml",
		},
		{
			name: "a url that leaves assets/",
			source: func(t *testing.T) string {
				source := good(t)
				idx, err := os.ReadFile(filepath.Join(source, "index.yaml"))
				require.NoError(t, err)
				changed := []byte(strings.Replace(string(idx), "assets/fleet/fleet-"+version+".tgz", "../outside.tgz", 1))
				writeFile(t, filepath.Join(source, "index.yaml"), string(changed))
				return source
			},
			wantErr: `the url "../outside.tgz" is not a path inside assets/`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := tt.source(t)
			repoDir := t.TempDir()
			if tt.prepare != nil {
				tt.prepare(t, source, repoDir)
			}

			plan, err := MakePlan(loadRules(t), Options{Source: source, Repo: repoDir}, "fleet", "2.15")

			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, plan.Import)
		})
	}
}

func TestMakePlanNamesEveryVersionAtFault(t *testing.T) {
	source := writeSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4", "110.0.5+up0.16.5"}})
	writeFile(t, filepath.Join(source, "assets", "fleet", "fleet-110.0.3+up0.16.3.tgz"), "changed")
	writeFile(t, filepath.Join(source, "assets", "fleet", "fleet-110.0.5+up0.16.5.tgz"), "changed too")

	_, err := MakePlan(loadRules(t), Options{Source: source, Repo: t.TempDir()}, "fleet", "2.15")

	require.ErrorContains(t, err, "fleet 110.0.3+up0.16.3: the archive")
	require.ErrorContains(t, err, "fleet 110.0.5+up0.16.5: the archive")
	require.NotContains(t, err.Error(), "fleet 110.0.4+up0.16.4")
}
