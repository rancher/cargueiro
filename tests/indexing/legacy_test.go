//go:build e2e

package indexing

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rancher/cargueiro/tests/internal/harness"
)

const legacyEntry = `annotations:
  catalog.cattle.io/certified: rancher
apiVersion: v2
appVersion: 0.16.3
created: "2025-10-20T11:00:00Z"
description: Fleet Controller
digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
icon: file://assets/logos/fleet.svg
name: fleet
urls:
- oci://stgregistry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3@sha256:1111111111111111111111111111111111111111111111111111111111111111
version: 110.0.3+up0.16.3
`

// legacyOnlyRepo returns a charts repository whose only version is a legacy one.
func legacyOnlyRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "entries"), 0o750))
	path := filepath.Join(dir, "legacy", "fleet", "110.0.3+up0.16.3.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(legacyEntry), 0o600))
	return dir
}

func TestIndexListsALegacyVersionWithoutTheRegistry(t *testing.T) {
	repo := legacyOnlyRepo(t)

	res := harness.Cargueiro(t, t.TempDir(), "index", "--repo", repo)

	require.Zero(t, res.ExitCode, res.Stderr)
	require.Equal(t, "add legacy fleet 110.0.3+up0.16.3\n", res.Stdout)
	index := readIndex(t, repo)
	require.Contains(t, index, "digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n")
	require.Contains(t, index, "fleet:110.0.3_up0.16.3@sha256:1111")
	require.NoDirExists(t, filepath.Join(repo, "assets"), "a legacy version has no archive")

	res = harness.Cargueiro(t, t.TempDir(), "index", "--repo", repo)
	require.Zero(t, res.ExitCode, res.Stderr)
	require.Equal(t, "nothing to change\n", res.Stdout, "a second run changes nothing")
}

func TestAuditLeavesALegacyVersionOut(t *testing.T) {
	repo := legacyOnlyRepo(t)
	require.Zero(t, harness.Cargueiro(t, t.TempDir(), "index", "--repo", repo).ExitCode)

	// No registry is given, and the version would not be found in the staging
	// one: nothing is read for a legacy version.
	res := harness.Cargueiro(t, t.TempDir(), "audit", "--repo", repo)

	require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	require.NotContains(t, res.Stdout, "valid: false")
	require.Contains(t, res.Stdout, "0 versions")
}

func TestIndexRefusesALegacyVersionThatHasAnArchive(t *testing.T) {
	repo := legacyOnlyRepo(t)
	archive := filepath.Join(repo, "assets", "fleet", "fleet-110.0.3+up0.16.3.tgz")
	require.NoError(t, os.MkdirAll(filepath.Dir(archive), 0o750))
	require.NoError(t, os.WriteFile(archive, []byte("archive"), 0o600))

	res := harness.Cargueiro(t, t.TempDir(), "index", "--repo", repo)

	require.NotZero(t, res.ExitCode)
	require.Contains(t, res.Stderr, "which is a legacy version: it has none")
	require.NoFileExists(t, filepath.Join(repo, "index.yaml"))
}
