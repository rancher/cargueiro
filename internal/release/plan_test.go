package release

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	repo "helm.sh/helm/v4/pkg/repo/v1"
	"sigs.k8s.io/yaml"

	"github.com/rancher/cargueiro/internal/rules"
)

const stagingHost = "stgregistry.suse.com/rancher/catalog"

func loadRules(t *testing.T) *rules.Config {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "rules.yaml"))
	require.NoError(t, err)
	cfg, err := rules.Parse(data)
	require.NoError(t, err)
	return cfg
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// legacyURL is the url of a legacy entry that was imported to the staging registry.
func legacyURL(chartName, tag, digest string) string {
	return "oci://" + stagingHost + "/" + chartName + ":" + tag + "@" + digest
}

// writeLegacy writes the legacy entry of a version into the repository dir, as an
// import does.
func writeLegacy(t *testing.T, dir, name, version, icon string, urls ...string) {
	t.Helper()

	entry := repo.ChartVersion{Metadata: &chart.Metadata{Name: name, Version: version, Icon: icon}, URLs: urls, Digest: "abc"}
	data, err := yaml.Marshal(&entry)
	require.NoError(t, err)
	writeFile(t, filepath.Join(dir, "legacy", name, version+".yaml"), string(data))
}

func itemNames(plan Plan) []string {
	out := make([]string, 0, len(plan.Release))
	for _, item := range plan.Release {
		out = append(out, item.Chart+" "+item.Entry.Version)
	}
	return out
}

func TestMakePlan(t *testing.T) {
	staging, production := t.TempDir(), t.TempDir()
	writeLegacy(t, staging, "fleet", "110.0.4+up0.16.4", "", legacyURL("fleet", "110.0.4_up0.16.4", "sha256:aaa"))
	writeLegacy(t, staging, "fleet", "110.0.3+up0.16.3", "", legacyURL("fleet", "110.0.3_up0.16.3", "sha256:bbb"))
	writeLegacy(t, staging, "fleet-crd", "110.0.3+up0.16.3", "", legacyURL("fleet-crd", "110.0.3_up0.16.3", "sha256:ccc"))
	cfg := loadRules(t)

	plan, err := MakePlan(cfg, Options{From: staging, Repo: production})

	require.NoError(t, err)
	require.Equal(t, []string{"fleet 110.0.3+up0.16.3", "fleet 110.0.4+up0.16.4", "fleet-crd 110.0.3+up0.16.3"}, itemNames(plan))
	first := plan.Release[0]
	require.Equal(t, "stgregistry.suse.com", first.Source.Host, "the source is what the entry says")
	require.Equal(t, "rancher/catalog/fleet", first.Source.Repository)
	require.Equal(t, "sha256:bbb", first.ManifestDigest)
	require.Equal(t, "registry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3", first.Destination.String(), "the production registry of the edition, with the same tag")
	require.Equal(t, "release fleet 110.0.3+up0.16.3\nrelease fleet 110.0.4+up0.16.4\nrelease fleet-crd 110.0.3+up0.16.3\n", plan.Report())

	t.Run("the registry given replaces the production one", func(t *testing.T) {
		plan, err := MakePlan(cfg, Options{From: staging, Repo: production, Registry: "127.0.0.1:5052/e2e"})

		require.NoError(t, err)
		require.Equal(t, "127.0.0.1:5052/e2e/fleet-crd:110.0.3_up0.16.3", plan.Release[2].Destination.String())
	})

	t.Run("a version that production has in legacy/ is left alone", func(t *testing.T) {
		writeLegacy(t, production, "fleet", "110.0.3+up0.16.3", "", legacyURL("fleet", "110.0.3_up0.16.3", "sha256:bbb"))

		plan, err := MakePlan(cfg, Options{From: staging, Repo: production})

		require.NoError(t, err)
		require.Equal(t, []string{"fleet 110.0.4+up0.16.4", "fleet-crd 110.0.3+up0.16.3"}, itemNames(plan))
	})

	t.Run("a version that production lists in its index.yaml is left alone", func(t *testing.T) {
		idx := &repo.IndexFile{APIVersion: "v1", Entries: map[string]repo.ChartVersions{
			"fleet-crd": {&repo.ChartVersion{Metadata: &chart.Metadata{Name: "fleet-crd", Version: "110.0.3+up0.16.3"}}},
		}}
		require.NoError(t, idx.WriteFile(filepath.Join(production, "index.yaml"), 0o600))

		plan, err := MakePlan(cfg, Options{From: staging, Repo: production})

		require.NoError(t, err)
		require.Equal(t, []string{"fleet 110.0.4+up0.16.4"}, itemNames(plan))
	})

	t.Run("everything released is nothing to do", func(t *testing.T) {
		writeLegacy(t, production, "fleet", "110.0.4+up0.16.4", "", legacyURL("fleet", "110.0.4_up0.16.4", "sha256:aaa"))

		plan, err := MakePlan(cfg, Options{From: staging, Repo: production})

		require.NoError(t, err)
		require.Empty(t, plan.Release)
		require.Equal(t, "nothing to release\n", plan.Report())
	})
}

func TestMakePlanWithoutLegacy(t *testing.T) {
	plan, err := MakePlan(loadRules(t), Options{From: t.TempDir(), Repo: t.TempDir()})

	require.NoError(t, err)
	require.Empty(t, plan.Release)
}

func TestMakePlanFails(t *testing.T) {
	cfg := loadRules(t)
	tests := []struct {
		name    string
		setup   func(t *testing.T, staging, production string)
		wantErr string
	}{
		{
			name: "the version is an entry of the new system in production",
			setup: func(t *testing.T, staging, production string) {
				writeLegacy(t, staging, "fleet", "110.0.3+up0.16.3", "", legacyURL("fleet", "110.0.3_up0.16.3", "sha256:bbb"))
				writeFile(t, filepath.Join(production, "entries", "fleet", "110.0.3+up0.16.3.yaml"), "x")
			},
			wantErr: "fleet 110.0.3+up0.16.3 is an entry of the new system in production already",
		},
		{
			name: "the url does not pin a manifest",
			setup: func(t *testing.T, staging, _ string) {
				writeLegacy(t, staging, "fleet", "110.0.3+up0.16.3", "", "oci://"+stagingHost+"/fleet:110.0.3_up0.16.3")
			},
			wantErr: "does not pin a manifest by its sha256 digest",
		},
		{
			name: "the entry has two urls",
			setup: func(t *testing.T, staging, _ string) {
				writeLegacy(t, staging, "fleet", "110.0.3+up0.16.3", "", legacyURL("fleet", "a", "sha256:a"), legacyURL("fleet", "b", "sha256:b"))
			},
			wantErr: "the entry has 2 urls, and it must have one",
		},
		{
			name: "the entry is of another version",
			setup: func(t *testing.T, staging, _ string) {
				writeFile(t, filepath.Join(staging, "legacy", "fleet", "110.0.3+up0.16.3.yaml"), "name: fleet\nversion: 110.0.9\nurls: [x]\n")
			},
			wantErr: "the legacy entry is not of fleet 110.0.3+up0.16.3",
		},
		{
			name: "the chart is not in the rules file",
			setup: func(t *testing.T, staging, _ string) {
				writeLegacy(t, staging, "not-a-chart", "1.0.0", "", legacyURL("not-a-chart", "1.0.0", "sha256:a"))
			},
			wantErr: `chart "not-a-chart" is not listed in the rules file`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			staging, production := t.TempDir(), t.TempDir()
			tt.setup(t, staging, production)

			_, err := MakePlan(cfg, Options{From: staging, Repo: production})

			require.ErrorContains(t, err, tt.wantErr)
		})
	}

	t.Run("every version at fault is named", func(t *testing.T) {
		staging, production := t.TempDir(), t.TempDir()
		writeLegacy(t, staging, "fleet", "110.0.3+up0.16.3", "", "oci://x/y")
		writeLegacy(t, staging, "fleet-crd", "110.0.3+up0.16.3", "", "oci://x/y")

		_, err := MakePlan(cfg, Options{From: staging, Repo: production})

		require.ErrorContains(t, err, "fleet 110.0.3+up0.16.3")
		require.ErrorContains(t, err, "fleet-crd 110.0.3+up0.16.3")
	})
}

func TestMakePlanLogos(t *testing.T) {
	staging, production := t.TempDir(), t.TempDir()
	writeLegacy(t, staging, "fleet", "110.0.3+up0.16.3", "file://assets/logos/fleet.svg", legacyURL("fleet", "t1", "sha256:a"))
	writeLegacy(t, staging, "fleet-crd", "110.0.3+up0.16.3", "file://assets/logos/there.svg", legacyURL("fleet-crd", "t2", "sha256:b"))
	writeLegacy(t, staging, "rancher-backup", "110.0.3+up11.0.4", "file://assets/logos/gone.svg", legacyURL("rancher-backup", "t3", "sha256:c"))
	writeLegacy(t, staging, "rancher-istio", "110.0.3+up1", "https://example.com/istio.svg", legacyURL("rancher-istio", "t4", "sha256:d"))
	writeFile(t, filepath.Join(staging, "assets", "logos", "fleet.svg"), "fleet logo")
	writeFile(t, filepath.Join(staging, "assets", "logos", "there.svg"), "staging logo")
	writeFile(t, filepath.Join(production, "assets", "logos", "there.svg"), "production logo")

	plan, err := MakePlan(loadRules(t), Options{From: staging, Repo: production})

	require.NoError(t, err)
	require.Equal(t, []Logo{{Name: "fleet.svg", From: filepath.Join(staging, "assets", "logos", "fleet.svg")}}, plan.Logos, "a logo that production has is not copied, and an icon on another address is left as it is")
	require.Equal(t, []string{"gone.svg"}, plan.MissingLogos)
	require.Contains(t, plan.Report(), "copy logo fleet.svg\nmissing logo gone.svg\n")
}

func TestMakePlanFamilies(t *testing.T) {
	staging, production := t.TempDir(), t.TempDir()
	writeLegacy(t, staging, "fleet", "110.0.3+up0.16.3", "", legacyURL("fleet", "t1", "sha256:a"))
	writeLegacy(t, staging, "fleet-crd", "110.0.3+up0.16.3", "", legacyURL("fleet-crd", "t2", "sha256:b"))
	writeLegacy(t, staging, "rancher-backup", "110.0.3+up11.0.4", "", legacyURL("rancher-backup", "t3", "sha256:c"))
	writeLegacy(t, staging, "rancher-istio", "110.0.3+up1", "", legacyURL("rancher-istio", "t4", "sha256:d"))
	cfg := loadRules(t)

	t.Run("a family is all its charts and no other", func(t *testing.T) {
		plan, err := MakePlan(cfg, Options{From: staging, Repo: production, Families: []string{"fleet"}})

		require.NoError(t, err)
		require.Equal(t, []string{"fleet 110.0.3+up0.16.3", "fleet-crd 110.0.3+up0.16.3"}, itemNames(plan))
	})

	t.Run("more than one family", func(t *testing.T) {
		plan, err := MakePlan(cfg, Options{From: staging, Repo: production, Families: []string{"rancher-istio", "rancher-backup"}})

		require.NoError(t, err)
		require.Equal(t, []string{"rancher-backup 110.0.3+up11.0.4", "rancher-istio 110.0.3+up1"}, itemNames(plan))
	})

	t.Run("none is every family", func(t *testing.T) {
		plan, err := MakePlan(cfg, Options{From: staging, Repo: production})

		require.NoError(t, err)
		require.Len(t, plan.Release, 4)
	})

	t.Run("a family with nothing in staging has nothing to release", func(t *testing.T) {
		plan, err := MakePlan(cfg, Options{From: staging, Repo: production, Families: []string{"longhorn"}})

		require.NoError(t, err)
		require.Empty(t, plan.Release)
		require.Equal(t, "nothing to release\n", plan.Report())
	})

	t.Run("a family that rules.yaml does not list is an error", func(t *testing.T) {
		_, err := MakePlan(cfg, Options{From: staging, Repo: production, Families: []string{"fleet", "not-a-family"}})

		require.ErrorContains(t, err, `family "not-a-family" is not listed in the rules file`)
	})
}
