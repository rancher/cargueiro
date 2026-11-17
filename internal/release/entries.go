package release

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	repo "helm.sh/helm/v4/pkg/repo/v1"

	"github.com/rancher/cargueiro/internal/index"
	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/rules"
)

// helmImages is the annotation that lists the images of a chart.
const helmImages = "helm.sh/images"

// stagedEntry is an entry of the staging repository: the version as the index lists
// it, and whether it is withdrawn. It is read as index reads it.
type stagedEntry struct {
	repo.ChartVersion
	Withdrawn bool `json:"withdrawn,omitempty"`
	raw       []byte
}

// MakeEntriesPlan works out what releasing the new versions would do: the ones in the
// entries/ of the staging repository that the production repository does not have, in its
// entries/ or in its index.yaml. It never looks at legacy/: those versions are released by
// MakePlan.
//
// A withdrawn version is not released, and neither is a release candidate: one whose upstream
// part ends in -rc.N, -beta.N or -alpha.N, which are indexed on staging only. Each is in
// Plan.Skipped with its reason. The rest are in Plan.Release, with the images their chart
// lists.
//
// It fails, naming every version at fault, when an entry cannot be read, does not pin a
// manifest in a registry, or lists an image that cannot be copied. Nothing is read from a
// registry and nothing is written.
func MakeEntriesPlan(cfg *rules.Config, opts Options) (Plan, error) {
	staged, err := readStagedEntries(opts.From)
	if err != nil {
		return Plan{}, fmt.Errorf("staging: %w", err)
	}
	have, err := productionHas(opts.Repo)
	if err != nil {
		return Plan{}, err
	}
	candidates, err := candidateSuffix(cfg)
	if err != nil {
		return Plan{}, err
	}
	families, err := familyCharts(cfg, opts.Families)
	if err != nil {
		return Plan{}, err
	}

	refs := make([]index.Ref, 0, len(staged))
	for ref := range staged {
		refs = append(refs, ref)
	}
	slices.SortFunc(refs, func(a, b index.Ref) int {
		return cmp.Or(cmp.Compare(a.Chart, b.Chart), cmp.Compare(a.Version, b.Version))
	})

	var plan Plan
	var problems []error
	for _, ref := range refs {
		if _, done := have[ref]; done {
			continue
		}
		if families != nil {
			if _, wanted := families[ref.Chart]; !wanted {
				continue
			}
		}
		entry := staged[ref]
		switch {
		case entry.Withdrawn:
			plan.Skipped = append(plan.Skipped, Skipped{ref.Chart, ref.Version, "withdrawn"})
			continue
		case candidates.MatchString(ref.Version):
			plan.Skipped = append(plan.Skipped, Skipped{ref.Chart, ref.Version, "release candidate"})
			continue
		}
		item, err := makeEntryItem(cfg, opts, ref, entry)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s %s: %w", ref.Chart, ref.Version, err))
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

// readStagedEntries reads every entry of entries/ in dir, strictly. A repository without
// entries/ has none.
func readStagedEntries(dir string) (map[index.Ref]*stagedEntry, error) {
	charts, err := os.ReadDir(filepath.Join(dir, index.EntriesDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", index.EntriesDir, err)
	}

	entries := make(map[index.Ref]*stagedEntry)
	var problems []error
	for _, c := range charts {
		if !c.IsDir() {
			problems = append(problems, fmt.Errorf("%s/%s is not a chart directory", index.EntriesDir, c.Name()))
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, index.EntriesDir, c.Name()))
		if err != nil {
			problems = append(problems, fmt.Errorf("read %s/%s: %w", index.EntriesDir, c.Name(), err))
			continue
		}
		for _, f := range files {
			version, ok := strings.CutSuffix(f.Name(), ".yaml")
			if f.IsDir() || !ok || version == "" {
				problems = append(problems, fmt.Errorf("%s/%s/%s is not an entry: it must be called <version>.yaml", index.EntriesDir, c.Name(), f.Name()))
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, index.EntriesDir, c.Name(), f.Name()))
			if err != nil {
				problems = append(problems, fmt.Errorf("read the entry of %s %s: %w", c.Name(), version, err))
				continue
			}
			var entry stagedEntry
			if err := yaml.UnmarshalStrict(data, &entry); err != nil {
				problems = append(problems, fmt.Errorf("entry %s/%s: %w", c.Name(), f.Name(), err))
				continue
			}
			if entry.Metadata == nil || entry.Name != c.Name() || entry.Version != version {
				problems = append(problems, fmt.Errorf("entry %s/%s is not of %s %s", c.Name(), f.Name(), c.Name(), version))
				continue
			}
			entry.raw = data
			entries[index.Ref{Chart: c.Name(), Version: version}] = &entry
		}
	}
	return entries, errors.Join(problems...)
}

// productionHas returns the versions the production repository has: the ones with an
// entry in its entries/ and the ones its index.yaml lists. legacy/ is not read: what is
// there is released by MakePlan.
func productionHas(dir string) (map[index.Ref]struct{}, error) {
	have := make(map[index.Ref]struct{})
	entries, err := readStagedEntries(dir)
	if err != nil {
		return nil, fmt.Errorf("production: %w", err)
	}
	for ref := range entries {
		have[ref] = struct{}{}
	}
	published, err := index.ReadPublished(dir)
	if err != nil {
		return nil, fmt.Errorf("production: %w", err)
	}
	if published != nil {
		for chart, versions := range published.Entries {
			for _, v := range versions {
				have[index.Ref{Chart: chart, Version: v.Version}] = struct{}{}
			}
		}
	}
	return have, nil
}

// candidateSuffix is the form of a release candidate: the version ends in -<name>.<number>,
// for the names of rules.yaml that are not "rancher", which is a flavor of a final version.
// It is the upstream part of the version for most charts and the whole version for a plain
// one, and in both the suffix is at the end.
func candidateSuffix(cfg *rules.Config) (*regexp.Regexp, error) {
	var names []string
	for _, name := range cfg.Versions().Prereleases {
		if name != "rancher" {
			names = append(names, regexp.QuoteMeta(name))
		}
	}
	if len(names) == 0 {
		return nil, errors.New("the rules file names no prerelease, so a release candidate cannot be told")
	}
	return regexp.MustCompile(`-(` + strings.Join(names, "|") + `)\.[0-9]+$`), nil
}

// familyCharts returns the charts of the families, or nil for no filter.
func familyCharts(cfg *rules.Config, families []string) (map[string]struct{}, error) {
	if len(families) == 0 {
		return nil, nil
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
	return charts, nil
}

// makeEntryItem checks a new version that is to be released and says where it and its
// images are copied from and to.
func makeEntryItem(cfg *rules.Config, opts Options, ref index.Ref, entry *stagedEntry) (Item, error) {
	chartVersion := entry.ChartVersion
	source, manifestDigest, err := pinnedManifest(&chartVersion)
	if err != nil {
		return Item{}, err
	}
	rule, err := cfg.Find(ref.Chart)
	if err != nil {
		return Item{}, err
	}
	repository, err := cfg.ProductionRepository(rule, opts.Registry)
	if err != nil {
		return Item{}, err
	}
	destination, err := oci.ParseChartRef(repository + ":" + tagOf(&chartVersion))
	if err != nil {
		return Item{}, err
	}
	images, err := imagesOf(cfg, opts, &chartVersion)
	if err != nil {
		return Item{}, err
	}
	return Item{
		Chart:          ref.Chart,
		Entry:          &chartVersion,
		Source:         source,
		ManifestDigest: manifestDigest,
		Destination:    destination,
		Dir:            index.EntriesDir,
		Raw:            entry.raw,
		Images:         images,
		Validate:       true,
	}, nil
}

// helmImage is an entry of the helm.sh/images annotation.
type helmImage struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// imagesOf returns the images that the chart of an entry lists, with where each is copied
// from and to. An image is rancher/<name>:<tag>, and its place in the registries is
// <registry>/<name>:<tag>, the registry being the images registry of rules.yaml (which
// ends in /rancher). Anything else cannot be moved, and is an error.
func imagesOf(cfg *rules.Config, opts Options, entry *repo.ChartVersion) ([]Image, error) {
	annotation := entry.Annotations[helmImages]
	if annotation == "" {
		return nil, nil
	}
	var listed []helmImage
	if err := yaml.Unmarshal([]byte(annotation), &listed); err != nil {
		return nil, fmt.Errorf("the %s annotation cannot be read: %w", helmImages, err)
	}

	registries := cfg.Registries().ImageRepositories()
	staging, production := registries[0], registries[1]
	if opts.StagingImageRegistry != "" {
		staging = opts.StagingImageRegistry
	}
	if opts.ImageRegistry != "" {
		production = opts.ImageRegistry
	}

	var images []Image
	seen := make(map[string]struct{})
	for _, l := range listed {
		name, tag, err := splitImage(l.Image)
		if err != nil {
			return nil, fmt.Errorf("image %q of %s: %w", l.Image, l.Name, err)
		}
		if _, dup := seen[name+":"+tag]; dup {
			continue
		}
		seen[name+":"+tag] = struct{}{}
		from, err := oci.ParseChartRef(strings.TrimRight(staging, "/") + "/" + name + ":" + tag)
		if err != nil {
			return nil, fmt.Errorf("image %q of %s: %w", l.Image, l.Name, err)
		}
		to, err := oci.ParseChartRef(strings.TrimRight(production, "/") + "/" + name + ":" + tag)
		if err != nil {
			return nil, fmt.Errorf("image %q of %s: %w", l.Image, l.Name, err)
		}
		images = append(images, Image{Name: l.Name, Source: from, Destination: to})
	}
	return images, nil
}

// splitImage splits rancher/<name>:<tag> into the name and the tag.
func splitImage(image string) (name, tag string, err error) {
	rest, ok := strings.CutPrefix(image, "rancher/")
	if !ok {
		return "", "", errors.New("it is not under rancher/, which is where the images of the catalog are")
	}
	name, tag, found := strings.Cut(rest, ":")
	switch {
	case !found || tag == "":
		return "", "", errors.New("it has no tag")
	case strings.ContainsAny(name, "@:") || strings.ContainsAny(tag, "@/"):
		return "", "", errors.New("it is not of the form rancher/<name>:<tag>")
	case name == "":
		return "", "", errors.New("it has no name")
	}
	return name, tag, nil
}
