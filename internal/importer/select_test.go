package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	repo "helm.sh/helm/v4/pkg/repo/v1"

	"github.com/rancher/cargueiro/internal/rules"
)

func loadRules(t *testing.T) *rules.Config {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "rules.yaml"))
	require.NoError(t, err)
	cfg, err := rules.Parse(data)
	require.NoError(t, err)
	return cfg
}

// sourceIndex returns an index of the old system with the versions of each chart.
func sourceIndex(versions map[string][]string) *repo.IndexFile {
	idx := &repo.IndexFile{APIVersion: "v1", Entries: map[string]repo.ChartVersions{}}
	for name, vs := range versions {
		for _, v := range vs {
			idx.Entries[name] = append(idx.Entries[name], &repo.ChartVersion{
				Metadata: &chart.Metadata{Name: name, Version: v},
				URLs:     []string{"assets/" + name + "/" + name + "-" + v + ".tgz"},
			})
		}
	}
	return idx
}

// names returns "chart version" for each candidate, in order.
func names(candidates []Candidate) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Chart+" "+c.Entry.Version)
	}
	return out
}

func TestSelect(t *testing.T) {
	source := sourceIndex(map[string][]string{
		"fleet":          {"109.0.1+up0.15.1", "110.0.3+up0.16.3", "110.0.10+up0.16.10", "110.0.4+up0.16.4", "111.0.0+up0.17.0"},
		"fleet-agent":    {"110.0.3+up0.16.3"},
		"fleet-crd":      {"110.0.3+up0.16.3", "109.0.1+up0.15.1"},
		"rancher-backup": {"110.0.3+up11.0.4"},
	})
	cfg := loadRules(t)

	got, err := Select(cfg, source, "fleet", "2.15")

	require.NoError(t, err)
	require.Equal(t, []string{
		"fleet 110.0.10+up0.16.10",
		"fleet 110.0.4+up0.16.4",
		"fleet 110.0.3+up0.16.3",
		"fleet-agent 110.0.3+up0.16.3",
		"fleet-crd 110.0.3+up0.16.3",
	}, names(got), "the charts of the family, the main chart first, newest first, and only the major of 2.15")

	got, err = Select(cfg, source, "fleet", "2.14")
	require.NoError(t, err)
	require.Equal(t, []string{"fleet 109.0.1+up0.15.1", "fleet-crd 109.0.1+up0.15.1"}, names(got), "a chart that has nothing for the minor gives nothing")

	got, err = Select(cfg, source, "fleet", "2.12")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestSelectIgnoresAVersionThatDoesNotStartWithANumber(t *testing.T) {
	source := sourceIndex(map[string][]string{"fleet": {"v1.2.3", "110.0.3+up0.16.3"}})

	got, err := Select(loadRules(t), source, "fleet", "2.15")

	require.NoError(t, err)
	require.Equal(t, []string{"fleet 110.0.3+up0.16.3"}, names(got))
}

func TestSelectFails(t *testing.T) {
	source := sourceIndex(map[string][]string{"fleet": {"110.0.3+up0.16.3"}})
	tests := []struct {
		name    string
		source  *repo.IndexFile
		family  string
		minor   string
		wantErr string
	}{
		{name: "a family that is not in the rules", source: source, family: "nope", minor: "2.15", wantErr: `family "nope" is not listed in the rules file`},
		{name: "a minor the rules do not know", source: source, family: "fleet", minor: "2.9", wantErr: "the rules file has no chart major for Rancher 2.9"},
		{name: "a minor the new system makes charts for", source: source, family: "fleet", minor: "2.16", wantErr: "the charts of Rancher 2.16 are made by the new system: only the charts of the old system are imported, which are the ones before 2.16"},
		{
			name:    "a version that does not follow the form of the chart",
			source:  sourceIndex(map[string][]string{"fleet": {"110.0.3"}}),
			family:  "fleet",
			minor:   "2.15",
			wantErr: "fleet: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Select(loadRules(t), tt.source, tt.family, tt.minor)

			require.ErrorContains(t, err, tt.wantErr)
			require.Nil(t, got)
		})
	}
}
