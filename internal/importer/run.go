package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/rancher/cargueiro/internal/fsutil"
	"github.com/rancher/cargueiro/internal/index"
	"github.com/rancher/cargueiro/internal/oci"
	"github.com/rancher/cargueiro/internal/rules"
)

// Registry is what the import needs from a registry.
type Registry interface {
	// Inspect says what the registry has at ref, host/path/chart:tag. It fails with
	// an error that matches oci.ErrNotFound when there is nothing there.
	Inspect(ctx context.Context, ref string) (oci.ManifestInfo, error)
	// Push uploads the chart archive to the registry, to the tag of the version it
	// holds. It never replaces a version that is there.
	Push(ctx context.Context, archive string) error
}

// action is what an import does for a version, after looking at the registry.
type action int

const (
	pushIt action = iota
	adoptIt
)

// looked is what the registry has for a version that is to be imported.
type looked struct {
	action action
	// info is what the registry has, when the version is adopted.
	info oci.ManifestInfo
}

// Run imports the versions of family for the Rancher minor, as MakePlan says, and
// reports each one on report as it is done.
//
// Before anything is pushed or written, every version is looked at: its archive
// must hold the chart and the version its entry says, and the registry must not
// have the tag with other content. A tag that has the archive is adopted and not
// pushed again, so a run that was cut off after a push finds it where it left
// it. Then the logos are copied, and each version is pushed, if it was not there,
// checked in the registry, and only then written to legacy/: the entry is the
// last thing done for a version, so a run that stops anywhere is run again to
// finish, and what is in legacy/ is complete.
//
// The three digests that must be the same are the sha256 of the archive in the
// source, the digest of the index of the source, and the digest of the chart in
// the registry. The first two are checked by MakePlan and the last one here.
func Run(ctx context.Context, cfg *rules.Config, opts Options, family, minor string, reg Registry, report io.Writer) error {
	plan, err := MakePlan(cfg, opts, family, minor)
	if err != nil {
		return err
	}
	if len(plan.Import) == 0 {
		return say(report, "nothing to import\n")
	}

	seen, err := look(ctx, plan.Import, reg)
	if err != nil {
		return err
	}

	for _, logo := range plan.Logos {
		if err := copyLogo(opts.Repo, logo); err != nil {
			return err
		}
		if err := say(report, "copied logo %s\n", logo.Name); err != nil {
			return err
		}
	}
	for i, item := range plan.Import {
		done, err := importVersion(ctx, opts.Repo, item, seen[i], reg)
		if err != nil {
			return err
		}
		if err := say(report, "%s %s %s\n", done, item.Chart, item.Entry.Version); err != nil {
			return err
		}
	}
	return nil
}

// look checks every version to import against its archive and the registry,
// naming every version at fault, and returns what the registry has for each.
func look(ctx context.Context, items []Item, reg Registry) ([]looked, error) {
	seen := make([]looked, len(items))
	var problems []error
	for i, item := range items {
		name := fmt.Sprintf("%s %s", item.Chart, item.Entry.Version)
		chart, version, err := oci.ReadIdentity(item.Archive)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if chart != item.Chart || version != item.Entry.Version {
			problems = append(problems, fmt.Errorf("%s: the archive %s holds %s %s", name, item.Archive, chart, version))
			continue
		}

		info, err := reg.Inspect(ctx, item.Reference)
		switch {
		case errors.Is(err, oci.ErrNotFound):
			seen[i] = looked{action: pushIt}
		case err != nil:
			problems = append(problems, fmt.Errorf("%s: read %s: %w", name, item.Reference, err))
		case info.ChartDigest != wantDigest(item):
			problems = append(problems, fmt.Errorf("%s: %s is in the registry already with other content (digest %s, the archive has %s): a version is never overwritten",
				name, item.Reference, info.ChartDigest, wantDigest(item)))
		default:
			seen[i] = looked{action: adoptIt, info: info}
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return seen, nil
}

// wantDigest is the digest the chart layer of the version has in the registry:
// the sha256 of its archive.
func wantDigest(item Item) string {
	return "sha256:" + item.Entry.Digest
}

// importVersion pushes a version, unless the registry has it, checks what the
// registry has and writes its entry. It returns what it did, pushed or adopted.
func importVersion(ctx context.Context, repoDir string, item Item, seen looked, reg Registry) (string, error) {
	info, did := seen.info, "adopted"
	if seen.action == pushIt {
		if err := reg.Push(ctx, item.Archive); err != nil {
			return "", fmt.Errorf("push %s: %w", item.Reference, err)
		}
		var err error
		if info, err = reg.Inspect(ctx, item.Reference); err != nil {
			return "", fmt.Errorf("read %s after it was pushed: %w", item.Reference, err)
		}
		did = "pushed"
	}
	if info.ChartDigest != wantDigest(item) {
		return "", fmt.Errorf("%s: the chart in the registry has the digest %s, but the archive has %s", item.Reference, info.ChartDigest, wantDigest(item))
	}
	if err := writeEntry(repoDir, item, info.ManifestDigest); err != nil {
		return "", err
	}
	return did, nil
}

// writeEntry writes the legacy entry of a version: the entry of the index of the
// source, as it is, with the URL that points to the manifest in the registry. It
// never replaces a file.
func writeEntry(repoDir string, item Item, manifestDigest string) error {
	entry := *item.Entry
	entry.URLs = []string{"oci://" + item.Reference + "@" + manifestDigest}
	data, err := yaml.Marshal(&entry)
	if err != nil {
		return fmt.Errorf("%s %s: %w", item.Chart, item.Entry.Version, err)
	}

	var batch fsutil.Batch
	path := filepath.Join(repoDir, index.LegacyDir, item.Chart, item.Entry.Version+".yaml")
	if err := batch.Write(path, data); err != nil {
		return fmt.Errorf("write the entry of %s %s: %w", item.Chart, item.Entry.Version, err)
	}
	return nil
}

// copyLogo copies a logo of the source into the repository. It never replaces a
// file.
func copyLogo(repoDir string, logo Logo) error {
	data, err := os.ReadFile(logo.From)
	if err != nil {
		return fmt.Errorf("logo %s: %w", logo.Name, err)
	}
	var batch fsutil.Batch
	if err := batch.Write(filepath.Join(repoDir, index.AssetsDir, "logos", logo.Name), data); err != nil {
		return fmt.Errorf("copy the logo %s: %w", logo.Name, err)
	}
	return nil
}

// say writes a line of the report.
func say(w io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}
