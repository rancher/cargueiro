package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// legacyVersion is a version of the fleet chart for Rancher 2.15, from the
	// old system.
	legacyVersion = "110.0.3+up0.16.3"
	legacyDigest  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	legacyURL     = "oci://registry.example.com/rancher/catalog/fleet:110.0.3_up0.16.3@" + pinnedOne
)

// legacyText returns the legacy entry of the fleet chart in a version: the entry
// of the old index with the url of the registry.
func legacyText(version, digest, url string) string {
	return `annotations:
  catalog.cattle.io/certified: rancher
apiVersion: v2
appVersion: 0.16.3
created: "2025-10-20T11:00:00Z"
description: Fleet Controller
digest: ` + digest + `
icon: file://assets/logos/fleet.svg
name: fleet
urls:
- ` + url + `
version: ` + version + `
`
}

// legacyRepo returns a charts repository with the legacy version and, when new
// is not empty, a new version in entries/.
func legacyRepo(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "entries"), 0o750))
	writeFiles(t, dir, files)
	return dir
}

func TestWriteLegacyOnly(t *testing.T) {
	fake := &fakePull{}
	dir := legacyRepo(t, map[string]string{"legacy/fleet/" + legacyVersion + ".yaml": legacyText(legacyVersion, legacyDigest, legacyURL)})

	plan, err := Write(context.Background(), loadRules(t), dir, Options{Registry: testRegistry, Pull: fake.pull})

	require.NoError(t, err)
	require.Equal(t, Plan{AddLegacy: []Ref{{Chart: "fleet", Version: legacyVersion}}}, plan)
	require.Equal(t, "add legacy fleet "+legacyVersion+"\n", plan.Report())
	require.Empty(t, fake.calls, "nothing is pulled for a legacy version")
	require.NoDirExists(t, filepath.Join(dir, "assets"), "and it has no archive")
	index, err := os.ReadFile(filepath.Join(dir, "index.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(index), "digest: "+legacyDigest+"\n", "the digest is the one of the entry")
	require.Contains(t, string(index), "- "+legacyURL+"\n")
	require.Contains(t, string(index), "created: \"2025-10-20T11:00:00Z\"\n", "with the date it had")
	require.Contains(t, string(index), "version: "+legacyVersion+"\n", "and its +")
}

func TestWriteLegacyNextToANewVersion(t *testing.T) {
	const second = "111.0.4+up0.17.4"
	fake := &fakePull{archives: map[string][]byte{pinnedTwo: archiveOf(t, chartYAMLOf(second))}}
	dir := legacyRepo(t, map[string]string{
		"legacy/fleet/" + legacyVersion + ".yaml": legacyText(legacyVersion, legacyDigest, legacyURL),
		"entries/fleet/" + second + ".yaml":       entryText(second, pinnedTwo),
	})
	cfg := loadRules(t)

	plan, err := Write(context.Background(), cfg, dir, Options{Registry: testRegistry, Pull: fake.pull})

	require.NoError(t, err)
	require.Equal(t, Plan{Add: []Ref{{Chart: "fleet", Version: second}}, AddLegacy: []Ref{{Chart: "fleet", Version: legacyVersion}}}, plan)
	require.Len(t, fake.calls, 1, "only the new version is pulled")
	require.Equal(t, pinnedTwo, fake.calls[0].digest)
	require.FileExists(t, filepath.Join(dir, "assets", "fleet", "fleet-"+second+".tgz"))
	require.NoFileExists(t, filepath.Join(dir, "assets", "fleet", "fleet-"+legacyVersion+".tgz"))
	index, err := os.ReadFile(filepath.Join(dir, "index.yaml"))
	require.NoError(t, err)
	require.Less(t, strings.Index(string(index), "version: "+second), strings.Index(string(index), "version: "+legacyVersion), "newest first")

	t.Run("a second run changes nothing", func(t *testing.T) {
		before, err := os.ReadFile(filepath.Join(dir, "index.yaml"))
		require.NoError(t, err)
		fake.calls = nil

		plan, err := Write(context.Background(), cfg, dir, Options{Registry: testRegistry, Pull: fake.pull})

		require.NoError(t, err)
		require.Equal(t, Plan{}, plan)
		require.Empty(t, fake.calls)
		after, err := os.ReadFile(filepath.Join(dir, "index.yaml"))
		require.NoError(t, err)
		require.Equal(t, string(before), string(after))
	})

	t.Run("a published legacy entry that changed is refused", func(t *testing.T) {
		writeFiles(t, dir, map[string]string{"legacy/fleet/" + legacyVersion + ".yaml": legacyText(legacyVersion, strings.Repeat("a", 64), legacyURL)})

		_, err := Write(context.Background(), cfg, dir, Options{Registry: testRegistry, Pull: fake.pull})

		require.ErrorContains(t, err, "fleet "+legacyVersion+" is in index.yaml and its entry no longer matches it: a published version never changes")
	})
}

func TestWriteLegacyDryRun(t *testing.T) {
	dir := legacyRepo(t, map[string]string{"legacy/fleet/" + legacyVersion + ".yaml": legacyText(legacyVersion, legacyDigest, legacyURL)})

	plan, err := Write(context.Background(), loadRules(t), dir, Options{Registry: testRegistry, DryRun: true})

	require.NoError(t, err)
	require.Equal(t, "add legacy fleet "+legacyVersion+"\n", plan.Report())
	require.NoFileExists(t, filepath.Join(dir, "index.yaml"))
}

func TestWriteLegacyFails(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{
			name: "the same version in entries/ and in legacy/",
			files: map[string]string{
				"legacy/fleet/" + legacyVersion + ".yaml":  legacyText(legacyVersion, legacyDigest, legacyURL),
				"entries/fleet/" + legacyVersion + ".yaml": entryText(legacyVersion, pinnedOne),
			},
			wantErr: "fleet " + legacyVersion + " is in entries/ and in legacy/",
		},
		{
			name: "a legacy version that has an archive",
			files: map[string]string{
				"legacy/fleet/" + legacyVersion + ".yaml":      legacyText(legacyVersion, legacyDigest, legacyURL),
				"assets/fleet/fleet-" + legacyVersion + ".tgz": "archive",
			},
			wantErr: "which is a legacy version: it has none",
		},
		{
			name:    "a hidden legacy entry",
			files:   map[string]string{"legacy/fleet/" + legacyVersion + ".yaml": legacyText(legacyVersion, legacyDigest, legacyURL) + "hide: true\n"},
			wantErr: "legacy entry fleet/" + legacyVersion + ".yaml cannot be hidden",
		},
		{
			name:    "a digest that is not the sha256 of an archive",
			files:   map[string]string{"legacy/fleet/" + legacyVersion + ".yaml": legacyText(legacyVersion, "sha256:"+legacyDigest, legacyURL)},
			wantErr: "must be the sha256 of the archive in hex",
		},
		{
			name:    "no digest",
			files:   map[string]string{"legacy/fleet/" + legacyVersion + ".yaml": strings.Replace(legacyText(legacyVersion, legacyDigest, legacyURL), "digest: "+legacyDigest+"\n", "", 1)},
			wantErr: `has the digest ""`,
		},
		{
			name:    "a url that is not an oci reference",
			files:   map[string]string{"legacy/fleet/" + legacyVersion + ".yaml": legacyText(legacyVersion, legacyDigest, "assets/fleet/fleet-110.0.3.tgz")},
			wantErr: "must have exactly one url, an oci:// reference",
		},
		{
			name:    "a file that is not called after its version",
			files:   map[string]string{"legacy/fleet/other.yaml": legacyText(legacyVersion, legacyDigest, legacyURL)},
			wantErr: "legacy entry fleet/other.yaml holds version " + legacyVersion + ", so the file must be called " + legacyVersion + ".yaml",
		},
		{
			name:    "a chart that is not in the rules",
			files:   map[string]string{"legacy/unknown-chart/1.0.0.yaml": "name: unknown-chart\n"},
			wantErr: `chart "unknown-chart" is not listed in the rules file`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := legacyRepo(t, tt.files)

			_, err := Write(context.Background(), loadRules(t), dir, Options{Registry: testRegistry, Pull: (&fakePull{}).pull})

			require.ErrorContains(t, err, tt.wantErr)
			require.NoFileExists(t, filepath.Join(dir, "index.yaml"), "nothing is written")
		})
	}
}

func TestWriteWithoutAnEntriesFolder(t *testing.T) {
	// A repository that has only legacy versions has no entries/: git does not
	// keep an empty folder.
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"legacy/fleet/" + legacyVersion + ".yaml": legacyText(legacyVersion, legacyDigest, legacyURL)})

	plan, err := Write(context.Background(), loadRules(t), dir, Options{Registry: testRegistry, Pull: (&fakePull{}).pull})

	require.NoError(t, err)
	require.Equal(t, Plan{AddLegacy: []Ref{{Chart: "fleet", Version: legacyVersion}}}, plan)
	require.FileExists(t, filepath.Join(dir, "index.yaml"))
	require.NoDirExists(t, filepath.Join(dir, "entries"), "and none is made")
}

func TestWriteWithNothingAtAll(t *testing.T) {
	dir := t.TempDir()

	plan, err := Write(context.Background(), loadRules(t), dir, Options{Registry: testRegistry, Pull: (&fakePull{}).pull})

	require.NoError(t, err)
	require.Equal(t, Plan{}, plan)
}
