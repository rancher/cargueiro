//go:build e2e

// Package releasing holds the e2e tests of `cargueiro release`, run against the
// real binary and two registries: the staging one, that asks for no password, and
// the production one, that does. The legacy versions are imported to staging first.
package releasing

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

// stamp makes the paths of a test unique, so that a registry that was used before
// has nothing in them.
func stamp() int64 { return time.Now().UnixNano() }

func write(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// chartArchive returns a chart archive that Helm can push.
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

// oldSystem writes a checkout of the old system with the versions of each chart,
// an archive with the description given for each, and the logo fleet.svg.
func oldSystem(t *testing.T, versions map[string][]string, description string) string {
	t.Helper()

	dir := t.TempDir()
	idx := &repo.IndexFile{APIVersion: "v1", Entries: map[string]repo.ChartVersions{}}
	for name, vs := range versions {
		for _, v := range vs {
			rel := "assets/" + name + "/" + name + "-" + v + ".tgz"
			data := chartArchive(t, name, v, description)
			write(t, filepath.Join(dir, rel), string(data))
			sum := sha256.Sum256(data)
			idx.Entries[name] = append(idx.Entries[name], &repo.ChartVersion{
				Metadata: &chart.Metadata{Name: name, Version: v, Icon: "file://assets/logos/fleet.svg"},
				URLs:     []string{rel},
				Digest:   hex.EncodeToString(sum[:]),
			})
		}
	}
	require.NoError(t, idx.WriteFile(filepath.Join(dir, "index.yaml"), 0o600))
	write(t, filepath.Join(dir, "assets", "logos", "fleet.svg"), "logo")
	return dir
}

// stagingRepo imports the fleet versions of Rancher 2.15 into a new path of the
// staging registry and returns the checkout with their legacy entries, and the path.
func stagingRepo(t *testing.T, source string) (dir, registry string) {
	t.Helper()

	dir = t.TempDir()
	registry = fmt.Sprintf("%s/e2e/%d/staging", harness.OpenRegistry(t), stamp())
	res := harness.Cargueiro(t, t.TempDir(), "import", "--source", source, "--repo", dir, "--family", "fleet", "--minor", "2.15", "--registry", registry, "--plain-http")
	require.Zero(t, res.ExitCode, res.Stderr)
	return dir, registry
}

// productionPath returns a new path of the production registry, which asks for a
// password.
func productionPath(t *testing.T) string {
	t.Helper()

	return fmt.Sprintf("%s/e2e/%d/production", harness.HTTPRegistry(t), stamp())
}

func release(t *testing.T, staging, production, registry string, args ...string) harness.Result {
	t.Helper()

	all := append([]string{"release", "--legacy", "--from", staging, "--repo", production, "--registry", registry, "--plain-http", "--production-username", harness.RegistryUser}, args...)
	return harness.CargueiroWith(t, harness.Options{Dir: t.TempDir(), Env: []string{"CARGUEIRO_PRODUCTION_PASSWORD=" + harness.RegistryPassword}}, all...)
}

// manifestDigest asks the registry for the digest of the manifest of a tag, as the
// registry user when it asks for one.
func manifestDigest(t *testing.T, registry, chartName, tag string, authenticated bool) string {
	t.Helper()

	address, path, _ := strings.Cut(registry, "/")
	req, err := http.NewRequest(http.MethodHead, "http://"+address+"/v2/"+path+"/"+chartName+"/manifests/"+tag, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	if authenticated {
		req.SetBasicAuth(harness.RegistryUser, harness.RegistryPassword)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode, "%s:%s is in the registry", chartName, tag)
	return res.Header.Get("Docker-Content-Digest")
}

func TestReleaseCopiesBetweenRegistriesAndKeepsTheManifest(t *testing.T) {
	source := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4"}, "fleet-crd": {"110.0.3+up0.16.3"}}, "old")
	staging, stagingRegistry := stagingRepo(t, source)
	production, productionRegistry := t.TempDir(), productionPath(t)

	dry := release(t, staging, production, productionRegistry, "--dry-run")
	require.Zero(t, dry.ExitCode, dry.Stderr)
	require.Equal(t, "release fleet 110.0.3+up0.16.3\nrelease fleet 110.0.4+up0.16.4\nrelease fleet-crd 110.0.3+up0.16.3\ncopy logo fleet.svg\n", dry.Stdout)
	require.NoDirExists(t, filepath.Join(production, "legacy"), "a dry run writes nothing")

	res := release(t, staging, production, productionRegistry)

	require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	require.Equal(t, ""+
		"copied logo fleet.svg\n"+
		"released fleet 110.0.3+up0.16.3\n"+
		"released fleet 110.0.4+up0.16.4\n"+
		"released fleet-crd 110.0.3+up0.16.3\n"+
		"\nreleased 3 versions, adopted 0\n", res.Stdout)
	require.FileExists(t, filepath.Join(production, "assets", "logos", "fleet.svg"))

	staged := manifestDigest(t, stagingRegistry, "fleet", "110.0.3_up0.16.3", false)
	released := manifestDigest(t, productionRegistry, "fleet", "110.0.3_up0.16.3", true)
	require.Equal(t, staged, released, "the production tag names the manifest of staging")
	entry := readFile(t, filepath.Join(production, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
	require.Contains(t, entry, "oci://"+productionRegistry+"/fleet:110.0.3_up0.16.3@"+released, "the entry points to production")
	require.NotContains(t, entry, stagingRegistry)

	t.Run("index lists them", func(t *testing.T) {
		res := harness.Cargueiro(t, t.TempDir(), "index", "--repo", production)

		require.Zero(t, res.ExitCode, res.Stderr)
		require.Equal(t, "add legacy fleet 110.0.3+up0.16.3\nadd legacy fleet 110.0.4+up0.16.4\nadd legacy fleet-crd 110.0.3+up0.16.3\n", res.Stdout)
	})

	t.Run("a second run has nothing to release", func(t *testing.T) {
		res := release(t, staging, production, productionRegistry)

		require.Zero(t, res.ExitCode, res.Stderr)
		require.Equal(t, "nothing to release\n", res.Stdout)
	})
}

func TestReleaseAdoptsWhatWasCopiedByARunThatWasCutOff(t *testing.T) {
	source := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4"}}, "old")
	staging, _ := stagingRepo(t, source)
	production, productionRegistry := t.TempDir(), productionPath(t)
	require.Zero(t, release(t, staging, production, productionRegistry).ExitCode)
	before := manifestDigest(t, productionRegistry, "fleet", "110.0.3_up0.16.3", true)

	// The entries are lost, as if the run had stopped after it copied.
	require.NoError(t, os.RemoveAll(filepath.Join(production, "legacy")))
	res := release(t, staging, production, productionRegistry)

	require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	require.Contains(t, res.Stdout, "adopted fleet 110.0.3+up0.16.3\nadopted fleet 110.0.4+up0.16.4\n")
	require.Equal(t, before, manifestDigest(t, productionRegistry, "fleet", "110.0.3_up0.16.3", true), "nothing is copied again")
	require.Contains(t, readFile(t, filepath.Join(production, "legacy", "fleet", "110.0.3+up0.16.3.yaml")), "@"+before)
}

func TestReleaseReportsAConflictAndGoesOn(t *testing.T) {
	source := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4"}}, "old")
	staging, _ := stagingRepo(t, source)
	production, productionRegistry := t.TempDir(), productionPath(t)

	// Production has 110.0.3 with other content, put there by an import of another archive.
	other := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}}, "something else")
	imported := harness.CargueiroWith(t, harness.Options{Dir: t.TempDir(), Env: []string{"CARGUEIRO_PASSWORD=" + harness.RegistryPassword}},
		"import", "--source", other, "--repo", t.TempDir(), "--family", "fleet", "--minor", "2.15", "--registry", productionRegistry, "--plain-http", "--username", harness.RegistryUser)
	require.Zero(t, imported.ExitCode, imported.Stderr)
	foreign := manifestDigest(t, productionRegistry, "fleet", "110.0.3_up0.16.3", true)

	res := release(t, staging, production, productionRegistry)

	require.Equal(t, 1, res.ExitCode, "a conflict is not a success")
	require.Contains(t, res.Stdout, "conflict fleet 110.0.3+up0.16.3")
	require.Contains(t, res.Stdout, "released fleet 110.0.4+up0.16.4\n", "the other version goes on")
	require.Contains(t, res.Stdout, "CONFLICT: 1 versions are in the production registry with other content than staging's")
	require.Contains(t, res.Stdout, foreign)
	require.Equal(t, foreign, manifestDigest(t, productionRegistry, "fleet", "110.0.3_up0.16.3", true), "the tag was not touched")
	require.NoFileExists(t, filepath.Join(production, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
	require.FileExists(t, filepath.Join(production, "legacy", "fleet", "110.0.4+up0.16.4.yaml"))
}

func TestReleaseOneFailureDoesNotStopTheOthers(t *testing.T) {
	source := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3", "110.0.4+up0.16.4"}}, "old")
	staging, _ := stagingRepo(t, source)
	production, productionRegistry := t.TempDir(), productionPath(t)

	// The entry of 110.0.3 pins a manifest that staging does not have.
	entryPath := filepath.Join(staging, "legacy", "fleet", "110.0.3+up0.16.3.yaml")
	entry := readFile(t, entryPath)
	at := strings.Index(entry, "@sha256:")
	require.Positive(t, at)
	write(t, entryPath, entry[:at+len("@sha256:")]+strings.Repeat("0", 64)+entry[at+len("@sha256:")+64:])

	res := release(t, staging, production, productionRegistry)

	require.Equal(t, 1, res.ExitCode)
	require.Contains(t, res.Stdout, "failed fleet 110.0.3+up0.16.3")
	require.Contains(t, res.Stdout, "released fleet 110.0.4+up0.16.4\n")
	require.Contains(t, res.Stdout, "FAILED: 1 versions were not released. Run it again: what was done is kept.")
	require.NoFileExists(t, filepath.Join(production, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
	require.FileExists(t, filepath.Join(production, "legacy", "fleet", "110.0.4+up0.16.4.yaml"))
}

func TestReleaseOnlyTheFamilyGiven(t *testing.T) {
	source := oldSystem(t, map[string][]string{"fleet": {"110.0.3+up0.16.3"}, "rancher-backup": {"110.0.3+up11.0.4"}}, "old")
	staging, registry := t.TempDir(), fmt.Sprintf("%s/e2e/%d/staging", harness.OpenRegistry(t), stamp())
	for _, family := range []string{"fleet", "rancher-backup"} {
		res := harness.Cargueiro(t, t.TempDir(), "import", "--source", source, "--repo", staging, "--family", family, "--minor", "2.15", "--registry", registry, "--plain-http")
		require.Zero(t, res.ExitCode, res.Stderr)
	}
	production, productionRegistry := t.TempDir(), productionPath(t)

	res := release(t, staging, production, productionRegistry, "--family", "rancher-backup")

	require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	require.Contains(t, res.Stdout, "released rancher-backup 110.0.3+up11.0.4\n")
	require.NotContains(t, res.Stdout, "released fleet")
	require.FileExists(t, filepath.Join(production, "legacy", "rancher-backup", "110.0.3+up11.0.4.yaml"))
	require.NoDirExists(t, filepath.Join(production, "legacy", "fleet"), "the family that was not asked for is left alone")

	t.Run("the other family is released by its own run", func(t *testing.T) {
		res := release(t, staging, production, productionRegistry, "--family", "fleet")

		require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
		require.Contains(t, res.Stdout, "released fleet 110.0.3+up0.16.3\n")
		require.FileExists(t, filepath.Join(production, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
	})

	t.Run("a run with no family finds nothing left", func(t *testing.T) {
		res := release(t, staging, production, productionRegistry)

		require.Zero(t, res.ExitCode, res.Stderr)
		require.Equal(t, "nothing to release\n", res.Stdout)
	})
}
