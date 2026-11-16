//go:build e2e

// Package importing holds the e2e tests of `cargueiro import`, run against the
// real binary. The old system is a small checkout made in each test.
package importing

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			data := chartArchive(t, name, v, "old "+name)
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

// chartArchive returns a chart archive that Helm can push: a Chart.yaml and a
// values file.
func chartArchive(t *testing.T, name, ver, description string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		name + "/Chart.yaml":  "apiVersion: v2\nname: " + name + "\nversion: " + ver + "\ndescription: " + description + "\n",
		name + "/values.yaml": "{}\n",
	}
	for _, file := range []string{name + "/Chart.yaml", name + "/values.yaml"} {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: file, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[file]))}))
		_, err := tw.Write([]byte(files[file]))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// newRegistryPath returns a path of the registry that asks for no password that
// nothing has used: the address and the path, such as 127.0.0.1:5052/e2e/123.
func newRegistryPath(t *testing.T) string {
	t.Helper()

	return fmt.Sprintf("%s/e2e/%d", harness.OpenRegistry(t), time.Now().UnixNano())
}

// importForReal imports the fleet family for Rancher 2.15 into a registry.
func importForReal(t *testing.T, source, repoDir, registry string) harness.Result {
	t.Helper()

	return harness.Cargueiro(t, t.TempDir(), "import", "--source", source, "--repo", repoDir, "--family", "fleet", "--minor", "2.15", "--registry", registry, "--plain-http")
}

// manifestDigest asks the registry for the digest of the manifest of a tag.
func manifestDigest(t *testing.T, registry, chart, tag string) string {
	t.Helper()

	address, path, _ := strings.Cut(registry, "/")
	req, err := http.NewRequest(http.MethodHead, "http://"+address+"/v2/"+path+"/"+chart+"/manifests/"+tag, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode, "%s:%s is in the registry", chart, tag)
	return res.Header.Get("Docker-Content-Digest")
}

func TestImportPushesToARegistryAndIndexAndAuditAcceptIt(t *testing.T) {
	source := oldSystem(t,
		map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4"}, "fleet-crd": {"110.0.3+up0.16.3"}},
		map[string]string{"fleet": "file://assets/logos/fleet.svg"}, "fleet.svg")
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "entries"), 0o750))
	registry := newRegistryPath(t)

	res := importForReal(t, source, repoDir, registry)

	require.Zero(t, res.ExitCode, res.Stderr)
	require.Equal(t, ""+
		"copied logo fleet.svg\n"+
		"pushed fleet 110.0.4+up0.16.4\n"+
		"pushed fleet 110.0.3+up0.16.3\n"+
		"pushed fleet-crd 110.0.3+up0.16.3\n", res.Stdout)
	require.FileExists(t, filepath.Join(repoDir, "assets", "logos", "fleet.svg"))

	entry, err := os.ReadFile(filepath.Join(repoDir, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
	require.NoError(t, err)
	manifest := manifestDigest(t, registry, "fleet", "110.0.3_up0.16.3")
	require.Contains(t, string(entry), "oci://"+registry+"/fleet:110.0.3_up0.16.3@"+manifest, "the entry pins the manifest that is in the registry")

	t.Run("index lists the legacy versions without the registry", func(t *testing.T) {
		res := harness.Cargueiro(t, t.TempDir(), "index", "--repo", repoDir)

		require.Zero(t, res.ExitCode, res.Stderr)
		require.Equal(t, "add legacy fleet 110.0.3+up0.16.3\nadd legacy fleet 110.0.4+up0.16.4\nadd legacy fleet-crd 110.0.3+up0.16.3\n", res.Stdout)
		require.Contains(t, readFile(t, filepath.Join(repoDir, "index.yaml")), "fleet:110.0.3_up0.16.3@"+manifest)
	})

	t.Run("audit leaves them out", func(t *testing.T) {
		res := harness.Cargueiro(t, t.TempDir(), "audit", "--repo", repoDir, "--registry", registry, "--plain-http")

		require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
		require.Contains(t, res.Stdout, "0 versions")
	})

	t.Run("a second run has nothing to import", func(t *testing.T) {
		res := importForReal(t, source, repoDir, registry)

		require.Zero(t, res.ExitCode, res.Stderr)
		require.Equal(t, "nothing to import\n", res.Stdout)
	})
}

func TestImportAdoptsWhatWasPushedByARunThatWasCutOff(t *testing.T) {
	source := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4"}}, nil)
	repoDir := t.TempDir()
	registry := newRegistryPath(t)
	require.Zero(t, importForReal(t, source, repoDir, registry).ExitCode)
	before := manifestDigest(t, registry, "fleet", "110.0.3_up0.16.3")

	// The entries are lost, as if the run had stopped after it pushed.
	require.NoError(t, os.RemoveAll(filepath.Join(repoDir, "legacy")))
	res := importForReal(t, source, repoDir, registry)

	require.Zero(t, res.ExitCode, res.Stderr)
	require.Equal(t, "adopted fleet 110.0.4+up0.16.4\nadopted fleet 110.0.3+up0.16.3\n", res.Stdout)
	require.Equal(t, before, manifestDigest(t, registry, "fleet", "110.0.3_up0.16.3"), "nothing is pushed again")
	require.Contains(t, readFile(t, filepath.Join(repoDir, "legacy", "fleet", "110.0.3+up0.16.3.yaml")), "@"+before)
}

func TestImportRefusesATagThatHasOtherContent(t *testing.T) {
	registry := newRegistryPath(t)
	first := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}}, nil)
	require.Zero(t, importForReal(t, first, t.TempDir(), registry).ExitCode)

	// Another archive for the same version, with the digest its own index gives.
	other := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}}, nil)
	archive := filepath.Join(other, "assets", "fleet", "fleet-110.0.3+up0.16.3.tgz")
	changed := chartArchive(t, "fleet", "110.0.3+up0.16.3", "another description")
	write(t, archive, string(changed))
	sum := sha256.Sum256(changed)
	index := readFile(t, filepath.Join(other, "index.yaml"))
	oldSum := sha256.Sum256(chartArchive(t, "fleet", "110.0.3+up0.16.3", "old fleet"))
	write(t, filepath.Join(other, "index.yaml"), strings.Replace(index, hex.EncodeToString(oldSum[:]), hex.EncodeToString(sum[:]), 1))
	repoDir := t.TempDir()

	res := importForReal(t, other, repoDir, registry)

	require.Equal(t, 1, res.ExitCode)
	require.Contains(t, res.Stderr, "is in the registry already with other content")
	require.Contains(t, res.Stderr, "a version is never overwritten")
	require.NoDirExists(t, filepath.Join(repoDir, "legacy"))
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
