package importer

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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	repo "helm.sh/helm/v4/pkg/repo/v1"
	"sigs.k8s.io/yaml"

	"github.com/rancher/cargueiro/internal/index"
	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/version"
)

const testRegistry = "registry.example.com/rancher/catalog"

// chartArchive returns a chart archive that holds a Chart.yaml of the chart.
func chartArchive(t *testing.T, name, ver string) []byte {
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

// realSource makes a checkout of the old system whose archives are real charts.
func realSource(t *testing.T, versions map[string][]string, icons map[string]string, logos ...string) string {
	t.Helper()

	dir := t.TempDir()
	idx := &repo.IndexFile{APIVersion: "v1", Entries: map[string]repo.ChartVersions{}}
	for name, vs := range versions {
		for _, v := range vs {
			rel := "assets/" + name + "/" + name + "-" + v + ".tgz"
			data := chartArchive(t, name, v)
			writeFile(t, filepath.Join(dir, rel), string(data))
			sum := sha256.Sum256(data)
			idx.Entries[name] = append(idx.Entries[name], &repo.ChartVersion{
				Metadata: &chart.Metadata{Name: name, Version: v, Icon: icons[name], Description: "old " + name},
				URLs:     []string{rel},
				Digest:   hex.EncodeToString(sum[:]),
			})
		}
	}
	require.NoError(t, idx.WriteFile(filepath.Join(dir, "index.yaml"), 0o600))
	for _, logo := range logos {
		writeFile(t, filepath.Join(dir, "assets", "logos", logo), "logo "+logo)
	}
	return dir
}

// fakeRegistry keeps the charts pushed to it in memory, and records what it was
// asked to do.
type fakeRegistry struct {
	stored   map[string]oci.ManifestInfo
	pushes   []string
	inspects []string
	// failPush makes the push of a chart and version, "chart version", fail.
	failPush map[string]error
	// wrongChart makes a pushed chart come back with another digest.
	wrongChart bool
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{stored: map[string]oci.ManifestInfo{}, failPush: map[string]error{}}
}

func (f *fakeRegistry) Inspect(_ context.Context, ref string) (oci.ManifestInfo, error) {
	f.inspects = append(f.inspects, ref)
	info, found := f.stored[ref]
	if !found {
		return oci.ManifestInfo{}, fmt.Errorf("%s was not found in the registry: %w", ref, oci.ErrNotFound)
	}
	return info, nil
}

func (f *fakeRegistry) Push(_ context.Context, archive string) error {
	name, ver, err := oci.ReadIdentity(archive)
	if err != nil {
		return err
	}
	if err := f.failPush[name+" "+ver]; err != nil {
		return err
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		return err
	}
	ref := testRegistry + "/" + name + ":" + version.ToOCITag(ver)
	sum := sha256.Sum256(data)
	chartDigest := "sha256:" + hex.EncodeToString(sum[:])
	if f.wrongChart {
		chartDigest = "sha256:" + strings.Repeat("0", 64)
	}
	manifest := sha256.Sum256([]byte("manifest of " + ref))
	f.stored[ref] = oci.ManifestInfo{ManifestDigest: "sha256:" + hex.EncodeToString(manifest[:]), ChartDigest: chartDigest}
	f.pushes = append(f.pushes, name+" "+ver)
	return nil
}

// charts repository for an import: it has an entries/ folder, as every one does.
func importRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "entries"), 0o750))
	return dir
}

func runImport(t *testing.T, source, repoDir string, reg Registry) (string, error) {
	t.Helper()

	var report strings.Builder
	err := Run(context.Background(), loadRules(t), Options{Source: source, Repo: repoDir, Registry: testRegistry}, "fleet", "2.15", reg, &report)
	return report.String(), err
}

func TestRunImportsEveryVersion(t *testing.T) {
	source := realSource(t,
		map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4"}, "fleet-crd": {"110.0.3+up0.16.3"}},
		map[string]string{"fleet": "file://assets/logos/fleet.svg"},
		"fleet.svg")
	repoDir := importRepo(t)
	reg := newFakeRegistry()

	report, err := runImport(t, source, repoDir, reg)

	require.NoError(t, err)
	require.Equal(t, ""+
		"copied logo fleet.svg\n"+
		"pushed fleet 110.0.4+up0.16.4\n"+
		"pushed fleet 110.0.3+up0.16.3\n"+
		"pushed fleet-crd 110.0.3+up0.16.3\n", report)
	require.Equal(t, []string{"fleet 110.0.4+up0.16.4", "fleet 110.0.3+up0.16.3", "fleet-crd 110.0.3+up0.16.3"}, reg.pushes)
	require.FileExists(t, filepath.Join(repoDir, "assets", "logos", "fleet.svg"))

	data, err := os.ReadFile(filepath.Join(repoDir, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
	require.NoError(t, err)
	var entry repo.ChartVersion
	require.NoError(t, yaml.UnmarshalStrict(data, &entry))
	manifest := reg.stored[testRegistry+"/fleet:110.0.3_up0.16.3"].ManifestDigest
	require.Equal(t, []string{"oci://" + testRegistry + "/fleet:110.0.3_up0.16.3@" + manifest}, entry.URLs, "the url points to the manifest that is in the registry")
	require.Equal(t, "old fleet", entry.Description, "the rest of the entry is as it was")
	require.NotEmpty(t, entry.Digest, "and it keeps the digest of the archive")

	t.Run("the entries are what index lists", func(t *testing.T) {
		plan, err := index.Write(context.Background(), loadRules(t), repoDir, index.Options{DryRun: true})

		require.NoError(t, err)
		require.Len(t, plan.AddLegacy, 3)
		require.Empty(t, plan.Add)
	})

	t.Run("a second run has nothing to do and does not touch the registry", func(t *testing.T) {
		reg.pushes, reg.inspects = nil, nil

		report, err := runImport(t, source, repoDir, reg)

		require.NoError(t, err)
		require.Equal(t, "nothing to import\n", report)
		require.Empty(t, reg.pushes)
		require.Empty(t, reg.inspects)
	})
}

func TestRunAdoptsWhatTheRegistryHas(t *testing.T) {
	source := realSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4"}}, nil)
	repoDir := importRepo(t)
	reg := newFakeRegistry()
	// A run that was cut off after it pushed the first version and before it wrote its entry.
	require.NoError(t, reg.Push(context.Background(), filepath.Join(source, "assets", "fleet", "fleet-110.0.3+up0.16.3.tgz")))
	reg.pushes = nil

	report, err := runImport(t, source, repoDir, reg)

	require.NoError(t, err)
	require.Equal(t, "pushed fleet 110.0.4+up0.16.4\nadopted fleet 110.0.3+up0.16.3\n", report)
	require.Equal(t, []string{"fleet 110.0.4+up0.16.4"}, reg.pushes, "the version that was there is not pushed again")
	require.FileExists(t, filepath.Join(repoDir, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
}

func TestRunRefusesATagWithOtherContentAndChangesNothing(t *testing.T) {
	source := realSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4"}}, nil, "fleet.svg")
	repoDir := importRepo(t)
	reg := newFakeRegistry()
	reg.stored[testRegistry+"/fleet:110.0.4_up0.16.4"] = oci.ManifestInfo{ManifestDigest: "sha256:" + strings.Repeat("1", 64), ChartDigest: "sha256:" + strings.Repeat("2", 64)}

	report, err := runImport(t, source, repoDir, reg)

	require.ErrorContains(t, err, "fleet 110.0.4+up0.16.4: "+testRegistry+"/fleet:110.0.4_up0.16.4 is in the registry already with other content")
	require.ErrorContains(t, err, "a version is never overwritten")
	require.Empty(t, report)
	require.Empty(t, reg.pushes, "nothing is pushed, not even the version that is fine")
	require.NoDirExists(t, filepath.Join(repoDir, "legacy"))
}

func TestRunRefusesAnArchiveThatIsAnotherChart(t *testing.T) {
	source := realSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}}, nil)
	// The archive holds fleet 110.0.9, with the digest the index of the source gives.
	other := chartArchive(t, "fleet", "110.0.9+up0.16.9")
	writeFile(t, filepath.Join(source, "assets", "fleet", "fleet-110.0.3+up0.16.3.tgz"), string(other))
	sum := sha256.Sum256(other)
	idx, err := index.ReadPublished(source)
	require.NoError(t, err)
	idx.Entries["fleet"][0].Digest = hex.EncodeToString(sum[:])
	require.NoError(t, idx.WriteFile(filepath.Join(source, "index.yaml"), 0o600))
	reg := newFakeRegistry()

	_, err = runImport(t, source, importRepo(t), reg)

	require.ErrorContains(t, err, "holds fleet 110.0.9+up0.16.9")
	require.Empty(t, reg.pushes)
}

func TestRunStopsAtAFailureAndResumes(t *testing.T) {
	source := realSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4", "110.0.5+up0.16.5"}}, nil)
	repoDir := importRepo(t)
	reg := newFakeRegistry()
	reg.failPush["fleet 110.0.4+up0.16.4"] = errors.New("the registry is down")

	report, err := runImport(t, source, repoDir, reg)

	require.ErrorContains(t, err, "push "+testRegistry+"/fleet:110.0.4_up0.16.4: the registry is down")
	require.Equal(t, "pushed fleet 110.0.5+up0.16.5\n", report, "what was done before is reported")
	require.FileExists(t, filepath.Join(repoDir, "legacy", "fleet", "110.0.5+up0.16.5.yaml"))
	require.NoFileExists(t, filepath.Join(repoDir, "legacy", "fleet", "110.0.4+up0.16.4.yaml"), "the entry of the version that failed is not written")

	reg.failPush = map[string]error{}
	reg.pushes = nil
	report, err = runImport(t, source, repoDir, reg)

	require.NoError(t, err)
	require.Equal(t, "pushed fleet 110.0.4+up0.16.4\npushed fleet 110.0.3+up0.16.3\n", report, "the next run finishes what is left")
	require.Equal(t, []string{"fleet 110.0.4+up0.16.4", "fleet 110.0.3+up0.16.3"}, reg.pushes, "and does not push the one that is done")
}

func TestRunWritesNoEntryWhenTheRegistryHasAnotherChart(t *testing.T) {
	source := realSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}}, nil)
	repoDir := importRepo(t)
	reg := newFakeRegistry()
	reg.wrongChart = true

	_, err := runImport(t, source, repoDir, reg)

	require.ErrorContains(t, err, "the chart in the registry has the digest sha256:0000")
	require.NoDirExists(t, filepath.Join(repoDir, "legacy"))
}

func TestRunNeverReplacesALogo(t *testing.T) {
	source := realSource(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}}, map[string]string{"fleet": "file://assets/logos/fleet.svg"}, "fleet.svg")
	repoDir := importRepo(t)
	writeFile(t, filepath.Join(repoDir, "assets", "logos", "fleet.svg"), "the logo that is there")

	report, err := runImport(t, source, repoDir, newFakeRegistry())

	require.NoError(t, err)
	require.NotContains(t, report, "copied logo")
	got, err := os.ReadFile(filepath.Join(repoDir, "assets", "logos", "fleet.svg"))
	require.NoError(t, err)
	require.Equal(t, "the logo that is there", string(got))
}
