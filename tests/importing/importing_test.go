//go:build e2e

// Package importing holds the e2e tests of `cargueiro import`, run against the
// real binary. The old system is a small checkout made in each test.
package importing

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	repo "helm.sh/helm/v4/pkg/repo/v1"

	"github.com/rancher/cargueiro/tests/internal/harness"
)

func TestMain(m *testing.M) {
	os.Exit(harness.Main(m))
}

// oldSystem writes a checkout of the old system: the versions of each chart, with
// an archive and its sha256 in the index, the icon of each chart, and the logos
// that are given.
func oldSystem(t *testing.T, versions map[string][]string, icons map[string]string, logos ...string) string {
	t.Helper()

	dir := t.TempDir()
	idx := &repo.IndexFile{APIVersion: "v1", Entries: map[string]repo.ChartVersions{}}
	for name, vs := range versions {
		for _, v := range vs {
			rel := "assets/" + name + "/" + name + "-" + v + ".tgz"
			data := []byte("archive of " + name + " " + v)
			write(t, filepath.Join(dir, rel), string(data))
			sum := sha256.Sum256(data)
			idx.Entries[name] = append(idx.Entries[name], &repo.ChartVersion{
				Metadata: &chart.Metadata{Name: name, Version: v, Icon: icons[name]},
				URLs:     []string{rel},
				Digest:   hex.EncodeToString(sum[:]),
			})
		}
	}
	require.NoError(t, idx.WriteFile(filepath.Join(dir, "index.yaml"), 0o600))
	for _, logo := range logos {
		write(t, filepath.Join(dir, "assets", "logos", logo), "logo "+logo)
	}
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func runImport(t *testing.T, source, repoDir, minor string) harness.Result {
	t.Helper()

	return harness.Cargueiro(t, t.TempDir(), "import", "--source", source, "--repo", repoDir, "--family", "fleet", "--minor", minor, "--dry-run")
}

func TestImportReportsWhatItWouldImportAndWritesNothing(t *testing.T) {
	source := oldSystem(t,
		map[string][]string{
			"fleet":     {"110.0.3+up0.16.3", "110.0.4+up0.16.4", "109.0.1+up0.15.1"},
			"fleet-crd": {"110.0.3+up0.16.3"},
		},
		map[string]string{"fleet": "file://assets/logos/fleet.svg", "fleet-crd": "https://charts.rancher.io/assets/logos/gone.svg"},
		"fleet.svg")
	repoDir := t.TempDir()

	res := runImport(t, source, repoDir, "2.15")

	require.Zero(t, res.ExitCode, res.Stderr)
	require.Equal(t, ""+
		"import fleet 110.0.4+up0.16.4\n"+
		"import fleet 110.0.3+up0.16.3\n"+
		"import fleet-crd 110.0.3+up0.16.3\n"+
		"copy logo fleet.svg\n"+
		"missing logo gone.svg\n", res.Stdout)
	entries, err := os.ReadDir(repoDir)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing is written")

	res = runImport(t, source, repoDir, "2.14")
	require.Zero(t, res.ExitCode, res.Stderr)
	require.Equal(t, "import fleet 109.0.1+up0.15.1\ncopy logo fleet.svg\n", res.Stdout, "another minor")
}

func TestImportLeavesWhatIsInLegacyAlone(t *testing.T) {
	source := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}}, nil)
	repoDir := t.TempDir()
	write(t, filepath.Join(repoDir, "legacy", "fleet", "110.0.3+up0.16.3.yaml"), "x")

	res := runImport(t, source, repoDir, "2.15")

	require.Zero(t, res.ExitCode, res.Stderr)
	require.Equal(t, "nothing to import\n", res.Stdout)
}

func TestImportRefusesAnArchiveThatIsNotTheOneOfTheIndex(t *testing.T) {
	source := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4"}}, nil)
	write(t, filepath.Join(source, "assets", "fleet", "fleet-110.0.4+up0.16.4.tgz"), "another archive")

	res := runImport(t, source, t.TempDir(), "2.15")

	require.Equal(t, 1, res.ExitCode)
	require.Empty(t, res.Stdout)
	require.Contains(t, res.Stderr, "fleet 110.0.4+up0.16.4: the archive")
	require.NotContains(t, res.Stderr, "fleet 110.0.3+up0.16.3")
}

func TestImportOnlyReportsForNow(t *testing.T) {
	source := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}}, nil)

	res := harness.Cargueiro(t, t.TempDir(), "import", "--source", source, "--repo", t.TempDir(), "--family", "fleet", "--minor", "2.15")

	require.Equal(t, 1, res.ExitCode)
	require.Contains(t, res.Stderr, "give --dry-run")
}
