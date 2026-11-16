package chart

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeChart writes the given files into a new directory and returns it. An
// empty content skips the file.
func writeChart(t *testing.T, chartYAML, valuesYAML string) string {
	t.Helper()
	dir := t.TempDir()
	if chartYAML != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(chartYAML), 0o600))
	}
	if valuesYAML != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "values.yaml"), []byte(valuesYAML), 0o600))
	}
	return dir
}

func TestLoadWithoutValues(t *testing.T) {
	dir := writeChart(t, "name: fleet-crd\nannotations:\n  catalog.cattle.io/hidden: \"true\"\n", "")

	got, err := Load(dir)

	require.NoError(t, err)
	require.Equal(t, "fleet-crd", got.Metadata.Name)
	require.Equal(t, map[string]string{"catalog.cattle.io/hidden": "true"}, got.Annotations)
	require.Empty(t, got.Values)
}

func TestLoad(t *testing.T) {
	dir := writeChart(t, `
name: fleet
version: 110.0.0
appVersion: 0.13.0
icon: https://example.com/icon.png
description: Fleet
dependencies:
  - name: sub
annotations:
  catalog.cattle.io/certified: rancher
  catalog.cattle.io/hidden: "true"
`, `
image:
  repository: rancher/fleet
  tag: v0.13.0
`)

	got, err := Load(dir)
	require.NoError(t, err)

	want := &Files{
		Metadata: Metadata{
			Name:        "fleet",
			Version:     "110.0.0",
			AppVersion:  "0.13.0",
			Icon:        "https://example.com/icon.png",
			Description: "Fleet",
		},
		Annotations: map[string]string{
			"catalog.cattle.io/certified": "rancher",
			"catalog.cattle.io/hidden":    "true",
		},
		Values: map[string]any{
			"image": map[string]any{
				"repository": "rancher/fleet",
				"tag":        "v0.13.0",
			},
		},
	}
	require.Equal(t, want, got)
}

func TestLoadAcceptsEmptyFiles(t *testing.T) {
	dir := writeChart(t, "name: a\n", "# nothing here\n")

	got, err := Load(dir)
	require.NoError(t, err)
	require.Equal(t, "a", got.Metadata.Name)
	require.Nil(t, got.Annotations)
	require.Nil(t, got.Values)
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name    string
		chart   string
		values  string
		wantErr string
	}{
		{
			name:    "no Chart.yaml",
			values:  "a: 1\n",
			wantErr: "Chart.yaml not found",
		},
		{
			name:    "Chart.yaml is not yaml",
			chart:   "name: [\n",
			values:  "a: 1\n",
			wantErr: "parse",
		},
		{
			name:    "values.yaml is not yaml",
			chart:   "name: a\n",
			values:  "a: [\n",
			wantErr: "parse",
		},
		{
			name:    "values.yaml is a list",
			chart:   "name: a\n",
			values:  "- a\n- b\n",
			wantErr: "parse",
		},
		{
			name:    "annotations is a list",
			chart:   "annotations:\n  - a\n",
			values:  "a: 1\n",
			wantErr: "parse",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(writeChart(t, tt.chart, tt.values))
			require.ErrorContains(t, err, tt.wantErr)
			require.Nil(t, got)
		})
	}
}

func TestLoadMissingDirectory(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "nope"))
	require.ErrorContains(t, err, "Chart.yaml not found")
	require.Nil(t, got)
}

func TestLoadWithNoChartsDirectoryHasNoDependencies(t *testing.T) {
	dir := writeChart(t, "name: a\n", "image:\n  repository: rancher/a\n  tag: v1\n")

	got, err := Load(dir)

	require.NoError(t, err)
	require.Nil(t, got.Dependencies)
}

func TestLoadWithAnEmptyChartsDirectoryHasNoDependencies(t *testing.T) {
	dir := writeChart(t, "name: a\n", "")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "charts"), 0o755))

	got, err := Load(dir)

	require.NoError(t, err)
	require.Nil(t, got.Dependencies)
}

func TestLoadReadsASubchartDirectory(t *testing.T) {
	dir := writeChart(t, "name: top\n", "image:\n  repository: rancher/top\n  tag: v1\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "charts", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "charts", "sub", "Chart.yaml"), []byte("name: sub\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "charts", "sub", "values.yaml"),
		[]byte("image:\n  repository: rancher/sub\n  tag: v2\n"), 0o600))

	got, err := Load(dir)

	require.NoError(t, err)
	require.Contains(t, got.Dependencies, "sub")
	require.Equal(t, "sub", got.Dependencies["sub"].Metadata.Name)
	require.Equal(t, map[string]any{
		"image": map[string]any{"repository": "rancher/sub", "tag": "v2"},
	}, got.Dependencies["sub"].Values)
}

func TestLoadKeysASubchartByItsOwnNameNotItsFolder(t *testing.T) {
	dir := writeChart(t, "name: top\n", "")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "charts", "folder"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "charts", "folder", "Chart.yaml"), []byte("name: sub\n"), 0o600))

	got, err := Load(dir)

	require.NoError(t, err)
	require.Contains(t, got.Dependencies, "sub")
	require.NotContains(t, got.Dependencies, "folder")
}

func TestLoadReadsASubchartNestedInsideASubchart(t *testing.T) {
	dir := writeChart(t, "name: top\n", "")
	nested := filepath.Join(dir, "charts", "sub", "charts", "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "charts", "sub", "Chart.yaml"), []byte("name: sub\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "Chart.yaml"), []byte("name: nested\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "values.yaml"),
		[]byte("image:\n  repository: rancher/nested\n  tag: v3\n"), 0o600))

	got, err := Load(dir)

	require.NoError(t, err)
	require.Contains(t, got.Dependencies, "sub")
	require.Contains(t, got.Dependencies["sub"].Dependencies, "nested")
	require.Equal(t, map[string]any{
		"image": map[string]any{"repository": "rancher/nested", "tag": "v3"},
	}, got.Dependencies["sub"].Dependencies["nested"].Values)
}

func TestLoadReadsAVendoredSubchartArchive(t *testing.T) {
	dir := writeChart(t, "name: top\n", "")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "charts"), 0o755))
	archive := buildArchive(t, []entry{
		{name: "sub/Chart.yaml", body: "name: sub\nversion: 0.1.0\n"},
		{name: "sub/values.yaml", body: "image:\n  repository: rancher/sub\n  tag: v2\n"},
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "charts", "sub-0.1.0.tgz"), archive, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "charts", "sub-0.1.0.tgz.prov"), []byte("a signature\n"), 0o600))

	got, err := Load(dir)

	require.NoError(t, err)
	require.Contains(t, got.Dependencies, "sub")
	require.Equal(t, map[string]any{
		"image": map[string]any{"repository": "rancher/sub", "tag": "v2"},
	}, got.Dependencies["sub"].Values)
}

func TestLoadRejectsABrokenSubchart(t *testing.T) {
	dir := writeChart(t, "name: top\n", "")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "charts", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "charts", "sub", "Chart.yaml"), []byte("name: [\n"), 0o600))

	got, err := Load(dir)

	require.ErrorContains(t, err, "sub")
	require.Nil(t, got)
}
