// Package index builds the index.yaml of a charts repository from its entries.
//
// An entry is the file entries/<chart>/<version>.yaml: the metadata of one chart
// version and the URL it is pulled from, as `ship` leaves it. The index is only
// a function of the entries and the archives in assets/, so it is the same bytes
// every time: the entries are put in a fixed order and no time of day is written.
//
// The versions of the old system are legacy/<chart>/<version>.yaml. They are
// imported as they were published, with the digest of their archive and a URL
// that points to the registry, so they have no archive in assets/ and nothing is
// checked or pulled for them. They are listed in the index with the entries.
package index

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	repo "helm.sh/helm/v4/pkg/repo/v1"
	"sigs.k8s.io/yaml"

	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/rules"
	"github.com/rancher/cargueiro/internal/version"
)

const (
	// EntriesDir is where the entries are, inside a charts repository.
	EntriesDir = "entries"
	// LegacyDir is where the entries of the legacy versions are.
	LegacyDir = "legacy"
	// File is the name of the index inside a charts repository.
	File = "index.yaml"
)

// Options say how to write the index.
type Options struct {
	// Registry replaces the staging registry of the chart's edition that the
	// URLs of new versions must point to, as host[:port]/path.
	Registry string
	// PlainHTTP talks HTTP to the registry the archives are pulled from.
	PlainHTTP bool
	// Credentials gives the credentials to read from a registry. Without it the
	// reads are anonymous.
	Credentials oci.CredentialFunc
	// Pull reads an archive from the registry. It defaults to oci.PullArchive.
	Pull PullFunc
	// DryRun makes Write check, and report what it would do, without pulling
	// or writing anything.
	DryRun bool
}

// Write builds the index of the charts repository in dir and writes it to
// dir/index.yaml. Hidden versions are not written to it. It returns what it did:
// the versions it added to the index and the ones it left out of it.
//
// Nothing is written unless every entry is valid, the entries, the index that is
// already there and assets/ agree, no published version or archive has changed,
// the URLs and icons of the new versions are right, and the archive of every new
// version has been pulled and is the chart its entry describes. The archives are
// written to assets/ before the index is, and are taken back if the index cannot
// be written.
//
// With Options.DryRun it does every check that needs no registry, pulls and
// writes nothing, and returns what it would do.
func Write(ctx context.Context, cfg *rules.Config, dir string, opts Options) (Plan, error) {
	entries, err := readEntries(cfg, dir)
	if err != nil {
		return Plan{}, err
	}
	published, err := ReadPublished(dir)
	if err != nil {
		return Plan{}, err
	}

	state, err := readState(dir, entries, published)
	if err != nil {
		return Plan{}, err
	}
	plan, err := state.Plan()
	if err != nil {
		return Plan{}, err
	}
	digests, err := archiveDigests(dir, entries.all)
	if err != nil {
		return Plan{}, err
	}
	if err := checkArchivesUnchanged(published, digests); err != nil {
		return Plan{}, err
	}
	setDigests(entries.all, digests)
	if err := checkUnchanged(published, entries.all); err != nil {
		return Plan{}, err
	}
	if err := errors.Join(
		checkURLs(cfg, opts.Registry, entries.all, plan.Add),
		checkIcons(cfg, dir, entries.all, plan.Add),
		checkCreated(entries.all, plan.Add),
	); err != nil {
		return Plan{}, err
	}
	if opts.DryRun {
		return plan, nil
	}

	archives, err := pullAdded(ctx, entries.all, plan.Add, opts)
	if err != nil {
		return Plan{}, err
	}
	pulled := make(map[Ref]string, len(archives))
	for ref, archive := range archives {
		pulled[ref] = digestOf(archive)
	}
	setDigests(entries.all, pulled)

	undo, err := writeArchives(dir, archives)
	if err != nil {
		return Plan{}, err
	}
	if err := entries.visible().WriteFile(filepath.Join(dir, File), 0o600); err != nil {
		return Plan{}, errors.Join(err, undo())
	}
	return plan, nil
}

// entries are the entries of a charts repository, as an index and as the
// versions among them that are hidden.
type entries struct {
	// all holds every entry, hidden or not, and the legacy ones.
	all    *repo.IndexFile
	hidden []Ref
	// fromEntries are the versions that have an entry in entries/, and legacy the
	// ones that have one in legacy/. A version in both is an error that the plan
	// reports.
	fromEntries []Ref
	legacy      []Ref
}

// visible returns the index to publish: all the entries but the hidden ones.
func (e entries) visible() *repo.IndexFile {
	hidden := refSet(e.hidden)

	idx := &repo.IndexFile{APIVersion: e.all.APIVersion, Entries: map[string]repo.ChartVersions{}}
	for name, versions := range e.all.Entries {
		for _, v := range versions {
			if _, isHidden := hidden[Ref{Chart: name, Version: v.Version}]; !isHidden {
				idx.Entries[name] = append(idx.Entries[name], v)
			}
		}
	}
	idx.Generated = newestCreated(idx)
	return idx
}

// readEntries reads the entries of the charts repository in dir, with the
// versions of each chart newest first.
func readEntries(cfg *rules.Config, dir string) (entries, error) {
	versions := cfg.Versions()
	policy, err := version.NewPolicy(versions.Prereleases, versions.Majors)
	if err != nil {
		return entries{}, err
	}

	// A repository that has only legacy versions has no entries/ folder: git does not
	// keep an empty one.
	charts, err := os.ReadDir(filepath.Join(dir, EntriesDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return entries{}, fmt.Errorf("read entries: %w", err)
	}

	// The time of day is left out: the zero time is what Rancher writes too.
	read := entries{all: &repo.IndexFile{APIVersion: "v1", Entries: map[string]repo.ChartVersions{}}}
	for _, c := range charts {
		if !c.IsDir() {
			return entries{}, fmt.Errorf("%s/%s is not a chart directory", EntriesDir, c.Name())
		}
		versions, hidden, err := readChart(cfg, policy, filepath.Join(dir, EntriesDir, c.Name()), c.Name())
		if err != nil {
			return entries{}, err
		}
		read.all.Entries[c.Name()] = versions
		read.hidden = append(read.hidden, hidden...)
	}
	read.fromEntries = indexRefs(read.all)

	legacy, err := readLegacy(cfg, dir)
	if err != nil {
		return entries{}, err
	}
	for name, added := range legacy {
		merged, err := mergeLegacy(cfg, policy, name, read.all.Entries[name], added)
		if err != nil {
			return entries{}, err
		}
		read.all.Entries[name] = merged
		for _, v := range added {
			read.legacy = append(read.legacy, Ref{Chart: name, Version: v.Version})
		}
	}
	return read, nil
}

// mergeLegacy puts the legacy versions of a chart among its entries, newest
// first. A version that is in both keeps its entry: the plan reports the clash.
func mergeLegacy(cfg *rules.Config, policy *version.Policy, name string, own, legacy repo.ChartVersions) (repo.ChartVersions, error) {
	rule, err := cfg.Find(name)
	if err != nil {
		return nil, err
	}
	byVersion := make(map[string]*repo.ChartVersion, len(own)+len(legacy))
	for _, v := range slices.Concat(legacy, own) {
		byVersion[v.Version] = v
	}
	ordered, err := policy.SortNewestFirst(slices.Collect(maps.Keys(byVersion)), rule.PlainVersion)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	merged := make(repo.ChartVersions, 0, len(ordered))
	for _, v := range ordered {
		merged = append(merged, byVersion[v])
	}
	return merged, nil
}

// readLegacy reads the legacy entries of the charts repository in dir, by chart.
// A repository without legacy/ has none.
func readLegacy(cfg *rules.Config, dir string) (map[string]repo.ChartVersions, error) {
	charts, err := os.ReadDir(filepath.Join(dir, LegacyDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", LegacyDir, err)
	}

	legacy := make(map[string]repo.ChartVersions, len(charts))
	var problems []error
	for _, c := range charts {
		if !c.IsDir() {
			problems = append(problems, fmt.Errorf("%s/%s is not a chart directory", LegacyDir, c.Name()))
			continue
		}
		if _, err := cfg.Find(c.Name()); err != nil {
			problems = append(problems, err)
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, LegacyDir, c.Name()))
		if err != nil {
			problems = append(problems, fmt.Errorf("read %s/%s: %w", LegacyDir, c.Name(), err))
			continue
		}
		for _, f := range files {
			entry, err := readLegacyEntry(filepath.Join(dir, LegacyDir, c.Name(), f.Name()), c.Name(), f.Name())
			if err != nil {
				problems = append(problems, err)
				continue
			}
			legacy[c.Name()] = append(legacy[c.Name()], &entry.ChartVersion)
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return legacy, nil
}

// archiveDigestForm is the sha256 of an archive, in hex, as the index lists it.
var archiveDigestForm = regexp.MustCompile(`^[0-9a-f]{64}$`)

// readLegacyEntry reads the legacy entry in path. It is an entry as the others,
// and it is also checked to have what the index needs to list it without an
// archive: the digest of the archive and the URL that points to the registry.
// Nothing else is checked.
func readLegacyEntry(path, name, file string) (*entryFile, error) {
	entry, err := readEntry(path, name, file)
	if err != nil {
		return nil, fmt.Errorf("legacy %w", err)
	}
	where := fmt.Sprintf("legacy entry %s/%s", name, file)
	switch {
	case entry.Hide:
		return nil, fmt.Errorf("%s cannot be hidden", where)
	case !archiveDigestForm.MatchString(entry.Digest):
		return nil, fmt.Errorf("%s has the digest %q, but it must be the sha256 of the archive in hex", where, entry.Digest)
	case len(entry.URLs) != 1 || !strings.HasPrefix(entry.URLs[0], "oci://"):
		return nil, fmt.Errorf("%s must have exactly one url, an oci:// reference", where)
	}
	return entry, nil
}

// readChart reads the entries of one chart and returns them newest first, and
// the versions among them that are hidden.
func readChart(cfg *rules.Config, policy *version.Policy, dir, name string) (repo.ChartVersions, []Ref, error) {
	rule, err := cfg.Find(name)
	if err != nil {
		return nil, nil, err
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("read entries of %s: %w", name, err)
	}

	byVersion := map[string]*repo.ChartVersion{}
	var hidden []Ref
	var problems []error
	for _, f := range files {
		file, err := readEntry(filepath.Join(dir, f.Name()), name, f.Name())
		if err != nil {
			problems = append(problems, err)
			continue
		}
		entry := &file.ChartVersion
		if _, found := byVersion[entry.Version]; found {
			problems = append(problems, fmt.Errorf("%s version %s is in more than one entry", name, entry.Version))
			continue
		}
		byVersion[entry.Version] = entry
		if file.Hide {
			hidden = append(hidden, Ref{Chart: name, Version: entry.Version})
		}
	}
	if len(problems) > 0 {
		return nil, nil, errors.Join(problems...)
	}

	ordered, err := policy.SortNewestFirst(slices.Collect(maps.Keys(byVersion)), rule.PlainVersion)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, err)
	}
	versions := make(repo.ChartVersions, 0, len(ordered))
	for _, v := range ordered {
		versions = append(versions, byVersion[v])
	}
	return versions, hidden, nil
}

// entryFile is an entry as it is written in entries/: the version as the index
// lists it, and whether the version is hidden.
type entryFile struct {
	repo.ChartVersion
	// Hide takes the version out of the index, without removing the entry or
	// the archive. It is not a field of the index.
	Hide bool `json:"hide,omitempty"`
}

// readEntry reads the entry in path, which must be the file of chart name.
func readEntry(path, name, file string) (*entryFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read entry: %w", err)
	}
	var entry entryFile
	if err := yaml.UnmarshalStrict(data, &entry); err != nil {
		return nil, fmt.Errorf("entry %s/%s: %w", name, file, err)
	}
	if entry.Metadata == nil {
		return nil, fmt.Errorf("entry %s/%s is empty", name, file)
	}
	if entry.Name != name {
		return nil, fmt.Errorf("entry %s/%s is of chart %q", name, file, entry.Name)
	}
	if want := entry.Version + ".yaml"; file != want {
		return nil, fmt.Errorf("entry %s/%s holds version %s, so the file must be called %s", name, file, entry.Version, want)
	}
	return &entry, nil
}
