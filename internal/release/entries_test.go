package release

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// stagedOptions are what writeStaged takes for the entry it writes.
type stagedOptions struct {
	withdrawn bool
	images    string
	icon      string
	url       string
}

// writeStaged writes the entry of a new version into dir, as ship does: pinned to a manifest
// of the staging registry, with the images it lists.
func writeStaged(t *testing.T, dir, chartName, version string, o stagedOptions) {
	t.Helper()

	tag := strings.ReplaceAll(version, "+", "_")
	url := o.url
	if url == "" {
		url = fmt.Sprintf("oci://stgregistry.suse.com/rancher/catalog/%s:%s@sha256:%s", chartName, tag, "a1")
	}
	var b strings.Builder
	b.WriteString("annotations:\n")
	if o.images != "" {
		b.WriteString("  helm.sh/images: |\n")
		for _, line := range strings.Split(strings.TrimRight(o.images, "\n"), "\n") {
			b.WriteString("    " + line + "\n")
		}
	}
	b.WriteString("  catalog.cattle.io/certified: rancher\n")
	fmt.Fprintf(&b, "apiVersion: v2\nname: %s\nversion: %s\ncreated: \"2026-11-16T23:50:17Z\"\n", chartName, version)
	if o.icon != "" {
		b.WriteString("icon: " + o.icon + "\n")
	}
	if o.withdrawn {
		b.WriteString("withdrawn: true\n")
	}
	fmt.Fprintf(&b, "urls:\n- %s\n", url)
	writeFile(t, filepath.Join(dir, "entries", chartName, version+".yaml"), b.String())
}

func skipped(plan Plan) []string {
	out := make([]string, 0, len(plan.Skipped))
	for _, s := range plan.Skipped {
		out = append(out, s.Chart+" "+s.Version+": "+s.Reason)
	}
	return out
}

func TestMakeEntriesPlan(t *testing.T) {
	staging, production := t.TempDir(), t.TempDir()
	images := "- name: fleet\n  image: rancher/fleet:v0.17.0\n- name: agent\n  image: rancher/fleet-agent:v0.17.0\n"
	writeStaged(t, staging, "fleet", "111.0.2+up0.17.0", stagedOptions{images: images})
	writeStaged(t, staging, "fleet-crd", "111.0.2+up0.17.0", stagedOptions{})
	writeStaged(t, staging, "fleet", "111.0.1+up0.17.0-rc.4", stagedOptions{})
	writeStaged(t, staging, "fleet", "111.0.0+up0.16.0-beta.1", stagedOptions{})
	writeStaged(t, staging, "fleet", "111.0.0+up0.15.0-alpha.3", stagedOptions{})
	writeStaged(t, staging, "fleet", "111.0.3+up0.17.1", stagedOptions{withdrawn: true})
	writeStaged(t, staging, "rancher-monitoring", "111.0.0+up80.9.1-rancher.23", stagedOptions{})
	cfg := loadRules(t)

	plan, err := MakeEntriesPlan(cfg, Options{From: staging, Repo: production})

	require.NoError(t, err)
	require.Equal(t, []string{"fleet 111.0.2+up0.17.0", "fleet-crd 111.0.2+up0.17.0", "rancher-monitoring 111.0.0+up80.9.1-rancher.23"}, itemNames(plan),
		"a flavor of the version that is -rancher.N is final, and is released")
	require.Equal(t, []string{
		"fleet 111.0.0+up0.15.0-alpha.3: release candidate",
		"fleet 111.0.0+up0.16.0-beta.1: release candidate",
		"fleet 111.0.1+up0.17.0-rc.4: release candidate",
		"fleet 111.0.3+up0.17.1: withdrawn",
	}, skipped(plan))

	first := plan.Release[0]
	require.Equal(t, "entries", first.Dir)
	require.True(t, first.Validate)
	require.Equal(t, "registry.suse.com/rancher/catalog/fleet:111.0.2_up0.17.0", first.Destination.String())
	require.Equal(t, "sha256:a1", first.ManifestDigest)
	require.NotEmpty(t, first.Raw, "the bytes of the staging entry are kept")
	require.Len(t, first.Images, 2)
	require.Equal(t, "stgregistry.suse.com/rancher/fleet:v0.17.0", first.Images[0].Source.String(), "an image is rancher/<name>:<tag>, and goes to the images registry")
	require.Equal(t, "registry.suse.com/rancher/fleet:v0.17.0", first.Images[0].Destination.String())
	require.Equal(t, "registry.suse.com/rancher/fleet-agent:v0.17.0", first.Images[1].Destination.String())

	report := plan.Report()
	require.Contains(t, report, "skip fleet 111.0.1+up0.17.0-rc.4: release candidate\n")
	require.Contains(t, report, "release fleet 111.0.2+up0.17.0\n  image stgregistry.suse.com/rancher/fleet:v0.17.0\n")

	t.Run("the registries given replace the ones of the rules", func(t *testing.T) {
		plan, err := MakeEntriesPlan(cfg, Options{From: staging, Repo: production, Registry: "127.0.0.1:5052/prod", ImageRegistry: "127.0.0.1:5052/img", StagingImageRegistry: "127.0.0.1:5050/stgimg"})

		require.NoError(t, err)
		require.Equal(t, "127.0.0.1:5052/prod/fleet:111.0.2_up0.17.0", plan.Release[0].Destination.String())
		require.Equal(t, "127.0.0.1:5050/stgimg/fleet:v0.17.0", plan.Release[0].Images[0].Source.String())
		require.Equal(t, "127.0.0.1:5052/img/fleet:v0.17.0", plan.Release[0].Images[0].Destination.String())
	})

	t.Run("only the families asked for", func(t *testing.T) {
		plan, err := MakeEntriesPlan(cfg, Options{From: staging, Repo: production, Families: []string{"rancher-monitoring"}})

		require.NoError(t, err)
		require.Equal(t, []string{"rancher-monitoring 111.0.0+up80.9.1-rancher.23"}, itemNames(plan))
		require.Empty(t, plan.Skipped, "what is not of the family is not even looked at")
	})

	t.Run("a family that rules.yaml does not list is an error", func(t *testing.T) {
		_, err := MakeEntriesPlan(cfg, Options{From: staging, Repo: production, Families: []string{"nope"}})

		require.ErrorContains(t, err, `family "nope" is not listed in the rules file`)
	})
}

func TestMakeEntriesPlanLeavesAloneWhatProductionHas(t *testing.T) {
	staging, production := t.TempDir(), t.TempDir()
	for _, v := range []string{"111.0.1+up0.17.1", "111.0.2+up0.17.2", "111.0.3+up0.17.3"} {
		writeStaged(t, staging, "fleet", v, stagedOptions{})
	}
	writeStaged(t, production, "fleet", "111.0.1+up0.17.1", stagedOptions{})
	// listed in the index of production, with no entry in this folder
	writeFile(t, filepath.Join(production, "index.yaml"), "apiVersion: v1\nentries:\n  fleet:\n  - name: fleet\n    version: 111.0.2+up0.17.2\n")
	cfg := loadRules(t)

	plan, err := MakeEntriesPlan(cfg, Options{From: staging, Repo: production})

	require.NoError(t, err)
	require.Equal(t, []string{"fleet 111.0.3+up0.17.3"}, itemNames(plan), "an entry of production and a line of its index both mean it is released")
}

func TestMakeEntriesPlanNeverLooksAtLegacy(t *testing.T) {
	staging, production := t.TempDir(), t.TempDir()
	writeStaged(t, staging, "fleet", "111.0.1+up0.17.1", stagedOptions{})
	// a legacy entry in staging is not released by this, and one in production does not make
	// a new version released
	writeLegacy(t, staging, "fleet", "110.0.0+up0.16.0", "", legacyURL("fleet", "t", "sha256:a"))
	writeLegacy(t, production, "fleet", "111.0.1+up0.17.1", "", legacyURL("fleet", "t", "sha256:a"))
	cfg := loadRules(t)

	plan, err := MakeEntriesPlan(cfg, Options{From: staging, Repo: production})

	require.NoError(t, err)
	require.Equal(t, []string{"fleet 111.0.1+up0.17.1"}, itemNames(plan))
}

func TestMakeEntriesPlanWithNothingStaged(t *testing.T) {
	plan, err := MakeEntriesPlan(loadRules(t), Options{From: t.TempDir(), Repo: t.TempDir()})

	require.NoError(t, err)
	require.Empty(t, plan.Release)
	require.Equal(t, "nothing to release\n", plan.Report())
}

func TestMakeEntriesPlanFails(t *testing.T) {
	cfg := loadRules(t)
	tests := []struct {
		name    string
		setup   func(t *testing.T, staging string)
		wantErr string
	}{
		{"an image that is not under rancher/", func(t *testing.T, s string) {
			writeStaged(t, s, "fleet", "111.0.1+up0.17.1", stagedOptions{images: "- name: x\n  image: quay.io/other/x:1\n"})
		}, "it is not under rancher/"},
		{"an image with no tag", func(t *testing.T, s string) {
			writeStaged(t, s, "fleet", "111.0.1+up0.17.1", stagedOptions{images: "- name: x\n  image: rancher/x\n"})
		}, "it has no tag"},
		{"an image pinned by digest", func(t *testing.T, s string) {
			writeStaged(t, s, "fleet", "111.0.1+up0.17.1", stagedOptions{images: "- name: x\n  image: rancher/x:1@sha256:abc\n"})
		}, "it is not of the form rancher/<name>:<tag>"},
		{"an annotation that is not a list", func(t *testing.T, s string) {
			writeStaged(t, s, "fleet", "111.0.1+up0.17.1", stagedOptions{images: "not: a list\n"})
		}, "the helm.sh/images annotation cannot be read"},
		{"a url that does not pin a manifest", func(t *testing.T, s string) {
			writeStaged(t, s, "fleet", "111.0.1+up0.17.1", stagedOptions{url: "oci://stgregistry.suse.com/rancher/catalog/fleet:111.0.1_up0.17.1"})
		}, "does not pin a manifest by its sha256 digest"},
		{"a chart that is not in the rules", func(t *testing.T, s string) {
			writeStaged(t, s, "not-a-chart", "1.0.0", stagedOptions{})
		}, `chart "not-a-chart" is not listed in the rules file`},
		{"an entry that is not of its file", func(t *testing.T, s string) {
			writeFile(t, filepath.Join(s, "entries", "fleet", "111.0.1+up0.17.1.yaml"), "name: fleet\nversion: 9.9.9\nurls: [x]\n")
		}, "is not of fleet 111.0.1+up0.17.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			staging := t.TempDir()
			tt.setup(t, staging)

			_, err := MakeEntriesPlan(cfg, Options{From: staging, Repo: t.TempDir()})

			require.ErrorContains(t, err, tt.wantErr)
		})
	}

	t.Run("every version at fault is named", func(t *testing.T) {
		staging := t.TempDir()
		writeStaged(t, staging, "fleet", "111.0.1+up0.17.1", stagedOptions{images: "- name: x\n  image: rancher/x\n"})
		writeStaged(t, staging, "fleet-crd", "111.0.1+up0.17.1", stagedOptions{images: "- name: y\n  image: other/y:1\n"})

		_, err := MakeEntriesPlan(cfg, Options{From: staging, Repo: t.TempDir()})

		require.ErrorContains(t, err, "fleet 111.0.1+up0.17.1")
		require.ErrorContains(t, err, "fleet-crd 111.0.1+up0.17.1")
	})
}

func TestMakeEntriesPlanKeepsTheLogosOfWhatItReleases(t *testing.T) {
	staging, production := t.TempDir(), t.TempDir()
	writeStaged(t, staging, "fleet", "111.0.1+up0.17.1", stagedOptions{icon: "file://assets/logos/fleet.svg"})
	writeFile(t, filepath.Join(staging, "assets", "logos", "fleet.svg"), "logo")

	plan, err := MakeEntriesPlan(loadRules(t), Options{From: staging, Repo: production})

	require.NoError(t, err)
	require.Equal(t, []Logo{{Name: "fleet.svg", From: filepath.Join(staging, "assets", "logos", "fleet.svg")}}, plan.Logos)
}
