// Package release moves the legacy versions of the staging charts repository to the
// production one: it copies each chart between the registries, by the digest of its
// manifest, and writes its entry in the production repository.
package release

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	repo "helm.sh/helm/v4/pkg/repo/v1"

	"github.com/rancher/cargueiro/internal/index"
	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/rules"
)

// Options say where to release from and to.
type Options struct {
	// From is a checkout of the staging charts repository.
	From string
	// Repo is the checkout of the production charts repository to release into.
	Repo string
	// Registry replaces the production registry of the edition of each chart that
	// the charts are copied to, as host[:port]/path. The name of the chart is added.
	Registry string
	// Families limits the release to the charts of these families, by their names
	// in rules.yaml. None means every legacy version staging has.
	Families []string
}

// Item is a legacy version to release.
type Item struct {
	Chart string
	// Entry is the legacy entry of the version in the staging repository.
	Entry *repo.ChartVersion
	// Source is where the chart is in the staging registry, which the URL of its
	// entry says.
	Source oci.ChartRepo
	// ManifestDigest is the digest of the manifest the URL of the entry pins, as
	// sha256:<hex>. The copy in the production registry must have the same one.
	ManifestDigest string
	// Destination is where the chart is copied to, host/path/chart:tag.
	Destination oci.ChartRef
}

// Logo is a logo of the staging repository that the production one does not have.
type Logo struct {
	Name string
	// From is the file in the staging repository.
	From string
}

// Plan is what a release would do.
type Plan struct {
	// Release are the versions that production does not have, by chart and version.
	Release []Item
	// Logos are the logos of those versions that staging has and production does
	// not, sorted by name. A logo production has is never replaced.
	Logos []Logo
	// MissingLogos are the logos that those versions name and staging does not
	// have, sorted.
	MissingLogos []string
}

// Report says what the plan does, one line each: the versions to release, the logos
// to copy and the logos that are missing, or that there is nothing to do.
func (p Plan) Report() string {
	if len(p.Release) == 0 {
		return "nothing to release\n"
	}
	var report strings.Builder
	for _, item := range p.Release {
		fmt.Fprintf(&report, "release %s %s\n", item.Chart, item.Entry.Version)
	}
	for _, logo := range p.Logos {
		fmt.Fprintf(&report, "copy logo %s\n", logo.Name)
	}
	for _, name := range p.MissingLogos {
		fmt.Fprintf(&report, "missing logo %s\n", name)
	}
	return report.String()
}

// MakePlan works out what releasing the legacy versions would do: the ones in the
// legacy/ of the staging repository that the production repository has neither in
// its legacy/ nor in its index.yaml. A version that production has is left alone,
// because a legacy entry never changes.
//
// It fails, naming every version at fault, when a version is also an entry of the
// new system in production, or when its entry cannot be read or does not pin a
// manifest in a registry. Nothing is read from a registry and nothing is written.
func MakePlan(cfg *rules.Config, opts Options) (Plan, error) {
	staged, err := index.LegacyRefs(opts.From)
	if err != nil {
		return Plan{}, fmt.Errorf("staging: %w", err)
	}
	if staged, err = inFamilies(cfg, staged, opts.Families); err != nil {
		return Plan{}, err
	}
	released, err := releasedRefs(opts.Repo)
	if err != nil {
		return Plan{}, err
	}

	slices.SortFunc(staged, func(a, b index.Ref) int {
		return cmp.Or(cmp.Compare(a.Chart, b.Chart), cmp.Compare(a.Version, b.Version))
	})
	var plan Plan
	var problems []error
	for _, ref := range staged {
		if _, done := released[ref]; done {
			continue
		}
		item, err := makeItem(cfg, opts, ref)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		plan.Release = append(plan.Release, item)
	}
	if len(problems) > 0 {
		return Plan{}, errors.Join(problems...)
	}
	plan.Logos, plan.MissingLogos, err = planLogos(opts, plan.Release)
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// inFamilies keeps the versions of the charts of the families. No families keeps
// them all, and a family that rules.yaml does not list is an error.
func inFamilies(cfg *rules.Config, refs []index.Ref, families []string) ([]index.Ref, error) {
	if len(families) == 0 {
		return refs, nil
	}
	charts := make(map[string]struct{})
	for _, name := range families {
		family, err := cfg.Family(name)
		if err != nil {
			return nil, err
		}
		for _, chart := range family.Names() {
			charts[chart] = struct{}{}
		}
	}
	return slices.DeleteFunc(slices.Clone(refs), func(ref index.Ref) bool {
		_, wanted := charts[ref.Chart]
		return !wanted
	}), nil
}

// releasedRefs returns the versions the production repository has: the ones with an
// entry in its legacy/ and the ones its index.yaml lists.
func releasedRefs(dir string) (map[index.Ref]struct{}, error) {
	legacy, err := index.LegacyRefs(dir)
	if err != nil {
		return nil, fmt.Errorf("production: %w", err)
	}
	published, err := index.ReadPublished(dir)
	if err != nil {
		return nil, fmt.Errorf("production: %w", err)
	}

	released := make(map[index.Ref]struct{}, len(legacy))
	for _, ref := range legacy {
		released[ref] = struct{}{}
	}
	if published != nil {
		for chart, versions := range published.Entries {
			for _, v := range versions {
				released[index.Ref{Chart: chart, Version: v.Version}] = struct{}{}
			}
		}
	}
	return released, nil
}

// makeItem checks a version that is to be released and says where it is copied from
// and to.
func makeItem(cfg *rules.Config, opts Options, ref index.Ref) (Item, error) {
	name := fmt.Sprintf("%s %s", ref.Chart, ref.Version)

	entry := filepath.Join(opts.Repo, index.EntriesDir, ref.Chart, ref.Version+".yaml")
	if _, err := os.Stat(entry); err == nil {
		return Item{}, fmt.Errorf("%s is an entry of the new system in production already: %s/%s/%s.yaml", name, index.EntriesDir, ref.Chart, ref.Version)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Item{}, fmt.Errorf("%s: %w", name, err)
	}

	chartVersion, err := readLegacyEntry(opts.From, ref)
	if err != nil {
		return Item{}, fmt.Errorf("%s: %w", name, err)
	}
	source, manifestDigest, err := pinnedManifest(chartVersion)
	if err != nil {
		return Item{}, fmt.Errorf("%s: %w", name, err)
	}

	rule, err := cfg.Find(ref.Chart)
	if err != nil {
		return Item{}, err
	}
	repository, err := productionRepository(cfg, rule, opts.Registry)
	if err != nil {
		return Item{}, fmt.Errorf("%s: %w", name, err)
	}
	destination, err := oci.ParseChartRef(repository + ":" + tagOf(chartVersion))
	if err != nil {
		return Item{}, fmt.Errorf("%s: %w", name, err)
	}
	return Item{Chart: ref.Chart, Entry: chartVersion, Source: source, ManifestDigest: manifestDigest, Destination: destination}, nil
}

// productionRepository returns the repository of a chart in the production registry:
// host/path/name. registry replaces the production registry of the edition of the
// chart when it is not empty.
func productionRepository(cfg *rules.Config, chart rules.Chart, registry string) (string, error) {
	if registry == "" {
		repositories := cfg.Registries().ChartRepositories(chart.Edition)
		if len(repositories) < 2 {
			return "", fmt.Errorf("the rules file has no production registry for the edition %q", chart.Edition)
		}
		registry = repositories[1]
	}
	return strings.TrimRight(registry, "/") + "/" + chart.Name, nil
}

// readLegacyEntry reads the legacy entry of ref in the repository dir.
func readLegacyEntry(dir string, ref index.Ref) (*repo.ChartVersion, error) {
	data, err := os.ReadFile(filepath.Join(dir, index.LegacyDir, ref.Chart, ref.Version+".yaml"))
	if err != nil {
		return nil, fmt.Errorf("read the legacy entry: %w", err)
	}
	var entry repo.ChartVersion
	if err := yaml.UnmarshalStrict(data, &entry); err != nil {
		return nil, fmt.Errorf("read the legacy entry: %w", err)
	}
	if entry.Metadata == nil || entry.Name != ref.Chart || entry.Version != ref.Version {
		return nil, fmt.Errorf("the legacy entry is not of %s %s", ref.Chart, ref.Version)
	}
	return &entry, nil
}

// pinnedManifest returns where the chart of a legacy entry is in the registry and the
// digest of its manifest, from its only URL: oci://host/path/chart:tag@sha256:<hex>.
func pinnedManifest(entry *repo.ChartVersion) (oci.ChartRepo, string, error) {
	if len(entry.URLs) != 1 {
		return oci.ChartRepo{}, "", fmt.Errorf("the entry has %d urls, and it must have one", len(entry.URLs))
	}
	address, manifestDigest, pinned := strings.Cut(entry.URLs[0], "@")
	if !pinned || !strings.HasPrefix(manifestDigest, "sha256:") {
		return oci.ChartRepo{}, "", fmt.Errorf("the url %q does not pin a manifest by its sha256 digest", entry.URLs[0])
	}
	ref, err := oci.ParseChartRef(address)
	if err != nil {
		return oci.ChartRepo{}, "", fmt.Errorf("the url %q: %w", entry.URLs[0], err)
	}
	return oci.ChartRepo{Host: ref.Host, Repository: ref.Repository}, manifestDigest, nil
}

// tagOf returns the tag the entry of a legacy version has in the registry, which its
// url says.
func tagOf(entry *repo.ChartVersion) string {
	address, _, _ := strings.Cut(entry.URLs[0], "@")
	ref, err := oci.ParseChartRef(address)
	if err != nil {
		return ""
	}
	return ref.Tag
}

// logoName returns the name of the logo an icon points to, when it is one that is
// kept in a charts repository. An icon on any other address is left as it is.
func logoName(icon string) (string, bool) {
	name, found := strings.CutPrefix(icon, "file://"+index.AssetsDir+"/logos/")
	if !found || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

// planLogos works out which logos of the versions to release are to be copied from
// the staging repository, and which ones it does not have. A logo that production
// has already is not copied again.
func planLogos(opts Options, items []Item) ([]Logo, []string, error) {
	names := make(map[string]struct{})
	for _, item := range items {
		if name, ok := logoName(item.Entry.Icon); ok {
			names[name] = struct{}{}
		}
	}

	var logos []Logo
	var missing []string
	for _, name := range slices.Sorted(maps.Keys(names)) {
		_, err := os.Lstat(filepath.Join(opts.Repo, index.AssetsDir, "logos", name))
		if err == nil {
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("logo %s: %w", name, err)
		}

		from := filepath.Join(opts.From, index.AssetsDir, "logos", name)
		info, err := os.Lstat(from)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			missing = append(missing, name)
		case err != nil:
			return nil, nil, fmt.Errorf("logo %s: %w", name, err)
		case !info.Mode().IsRegular():
			return nil, nil, fmt.Errorf("logo %s: %s is not a regular file", name, from)
		default:
			logos = append(logos, Logo{Name: name, From: from})
		}
	}
	return logos, missing, nil
}
