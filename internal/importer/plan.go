package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	repo "helm.sh/helm/v4/pkg/repo/v1"

	"github.com/rancher/cargueiro/internal/index"
	"github.com/rancher/cargueiro/internal/rules"
	"github.com/rancher/cargueiro/internal/version"
)

// Options say where to import from and into.
type Options struct {
	// Source is a checkout of the old system: its index.yaml and assets/.
	Source string
	// Repo is the charts repository to import into.
	Repo string
	// Registry replaces the staging registry of the edition of each chart that the
	// archives are pushed to, as host[:port]/path. The name of the chart is added.
	Registry string
}

// Item is a version to import.
type Item struct {
	Chart string
	// Entry is the entry of the version in the index of the source.
	Entry *repo.ChartVersion
	// Archive is the file of the archive in the source.
	Archive string
	// Reference is where the archive is pushed, host/path/chart:tag.
	Reference string
}

// Logo is a logo of the source that the repository does not have.
type Logo struct {
	Name string
	// From is the file in the source.
	From string
}

// Plan is what an import would do.
type Plan struct {
	// Import are the versions that are not in legacy/ yet, by chart in the order of
	// the family and newest first.
	Import []Item
	// Logos are the logos of those versions that the source has and the repository
	// does not, sorted by name. A logo the repository has is never replaced.
	Logos []Logo
	// MissingLogos are the logos that those versions name and the source does not
	// have, sorted. They are as dead in the source as they will be here.
	MissingLogos []string
}

// Report says what the plan does, one line each: the versions to import, the logos
// to copy and the logos that are missing, or that there is nothing to do.
func (p Plan) Report() string {
	if len(p.Import) == 0 {
		return "nothing to import\n"
	}
	var report strings.Builder
	for _, item := range p.Import {
		fmt.Fprintf(&report, "import %s %s\n", item.Chart, item.Entry.Version)
	}
	for _, logo := range p.Logos {
		fmt.Fprintf(&report, "copy logo %s\n", logo.Name)
	}
	for _, name := range p.MissingLogos {
		fmt.Fprintf(&report, "missing logo %s\n", name)
	}
	return report.String()
}

// MakePlan works out what importing the versions of family for the Rancher minor
// would do. A version that is in legacy/ already is left alone, because it was
// imported before and a legacy entry never changes.
//
// It fails, naming every version at fault, when a version is also an entry of the
// new system, or when its archive in the source is not there or is not the one the
// index of the source says: the digest is what the registry will be checked
// against, so the archive must be what was published. Nothing is read from a
// registry and nothing is written.
func MakePlan(cfg *rules.Config, opts Options, family, minor string) (Plan, error) {
	source, err := index.ReadPublished(opts.Source)
	if err != nil {
		return Plan{}, fmt.Errorf("source: %w", err)
	}
	if source == nil {
		return Plan{}, fmt.Errorf("source: there is no %s in %s", index.File, opts.Source)
	}
	candidates, err := Select(cfg, source, family, minor)
	if err != nil {
		return Plan{}, err
	}
	legacy, err := index.LegacyRefs(opts.Repo)
	if err != nil {
		return Plan{}, err
	}
	imported := make(map[index.Ref]struct{}, len(legacy))
	for _, ref := range legacy {
		imported[ref] = struct{}{}
	}

	var plan Plan
	var problems []error
	for _, c := range candidates {
		ref := index.Ref{Chart: c.Chart, Version: c.Entry.Version}
		if _, done := imported[ref]; done {
			continue
		}
		item, err := makeItem(cfg, opts, c)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		plan.Import = append(plan.Import, item)
	}
	if len(problems) > 0 {
		return Plan{}, errors.Join(problems...)
	}
	plan.Logos, plan.MissingLogos, err = planLogos(opts, plan.Import)
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// logoName returns the name of the logo an icon points to, when it is one that is
// kept in a charts repository: a local file, or the address it was served from.
// An icon on any other address is left as it is.
func logoName(icon string) (string, bool) {
	prefixes := []string{
		"file://" + index.AssetsDir + "/logos/",
		"https://charts.rancher.io/" + index.AssetsDir + "/logos/",
	}
	for _, prefix := range prefixes {
		if name, found := strings.CutPrefix(icon, prefix); found {
			if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
				return "", false
			}
			return name, true
		}
	}
	return "", false
}

// planLogos works out which logos of the versions to import are to be copied
// from the source, and which ones the source does not have. A logo that the
// repository has already is not copied again.
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

		from := filepath.Join(opts.Source, index.AssetsDir, "logos", name)
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

// makeItem checks a version that is to be imported: it is not an entry of the new
// system, and its archive in the source is the one the index of the source says.
func makeItem(cfg *rules.Config, opts Options, c Candidate) (Item, error) {
	name := fmt.Sprintf("%s %s", c.Chart, c.Entry.Version)

	entry := filepath.Join(opts.Repo, index.EntriesDir, c.Chart, c.Entry.Version+".yaml")
	if _, err := os.Stat(entry); err == nil {
		return Item{}, fmt.Errorf("%s is an entry of the new system already: %s/%s/%s.yaml", name, index.EntriesDir, c.Chart, c.Entry.Version)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Item{}, fmt.Errorf("%s: %w", name, err)
	}

	archive, err := sourceArchive(opts.Source, c.Entry)
	if err != nil {
		return Item{}, fmt.Errorf("%s: %w", name, err)
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		return Item{}, fmt.Errorf("%s: read the archive: %w", name, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != c.Entry.Digest {
		return Item{}, fmt.Errorf("%s: the archive %s has the sha256 %s, but the index of the source says %s", name, archive, got, c.Entry.Digest)
	}

	rule, err := cfg.Find(c.Chart)
	if err != nil {
		return Item{}, err
	}
	repository, err := cfg.StagingRepository(rule, opts.Registry)
	if err != nil {
		return Item{}, fmt.Errorf("%s: %w", name, err)
	}
	return Item{
		Chart:     c.Chart,
		Entry:     c.Entry,
		Archive:   archive,
		Reference: repository + ":" + version.ToOCITag(c.Entry.Version),
	}, nil
}

// sourceArchive returns the file of the archive of an entry of the source: its
// only URL, a path inside assets/ of the source.
func sourceArchive(source string, entry *repo.ChartVersion) (string, error) {
	if len(entry.URLs) != 1 {
		return "", fmt.Errorf("the index of the source gives %d urls, and it must give one", len(entry.URLs))
	}
	clean := path.Clean(entry.URLs[0])
	if !strings.HasPrefix(clean, index.AssetsDir+"/") || strings.Contains(clean, "..") {
		return "", fmt.Errorf("the url %q is not a path inside %s/", entry.URLs[0], index.AssetsDir)
	}
	return filepath.Join(source, filepath.FromSlash(clean)), nil
}
