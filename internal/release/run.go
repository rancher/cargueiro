package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/rancher/cargueiro/internal/fsutil"
	"github.com/rancher/cargueiro/internal/index"
	"github.com/rancher/cargueiro/internal/oci"
)

// Failure is a version that was not released.
type Failure struct {
	Item Item
	// Err says why.
	Err error
}

// Conflict is a version whose tag in the production registry holds another manifest
// than the staging one. Nothing was changed there, because a tag is never replaced.
type Conflict struct {
	Item Item
	// Existing is the digest of the manifest production has at the tag.
	Existing string
}

// Result is what a release did.
type Result struct {
	// Released are the versions that were copied, and Adopted the ones that the
	// production registry had with the same manifest already.
	Released, Adopted []Item
	// Failed are the versions that could not be released, and Conflicts the ones
	// whose tag holds other content in production.
	Failed    []Failure
	Conflicts []Conflict
}

// Done says whether every version of the plan was released.
func (r Result) Done() bool {
	return len(r.Failed) == 0 && len(r.Conflicts) == 0
}

// Summary says what was done in a few lines. What did not go through comes last
// and loud: a conflict is a tag that holds other content in production, which needs
// a person, and a failure is run again.
func (r Result) Summary() string {
	var out strings.Builder
	fmt.Fprintf(&out, "released %d versions, adopted %d", len(r.Released), len(r.Adopted))
	if r.Done() {
		return out.String() + "\n"
	}
	fmt.Fprintf(&out, ", failed %d, in conflict %d\n", len(r.Failed), len(r.Conflicts))
	if len(r.Conflicts) > 0 {
		fmt.Fprintf(&out, "\nCONFLICT: %d versions are in the production registry with other content than staging's, and were not touched. A tag is never overwritten: someone has to look at each one.\n", len(r.Conflicts))
		for _, c := range r.Conflicts {
			fmt.Fprintf(&out, "  %s %s: %s has the manifest %s, and staging has %s\n", c.Item.Chart, c.Item.Entry.Version, c.Item.Destination, c.Existing, c.Item.ManifestDigest)
		}
	}
	if len(r.Failed) > 0 {
		fmt.Fprintf(&out, "\nFAILED: %d versions were not released. Run it again: what was done is kept.\n", len(r.Failed))
		for _, f := range r.Failed {
			fmt.Fprintf(&out, "  %s %s: %v\n", f.Item.Chart, f.Item.Entry.Version, f.Err)
		}
	}
	return out.String()
}

// Run releases the versions of plan, and reports each one on report as it is done.
//
// A version that fails does not stop the others: each is independent, and the
// entry of a version is the last thing done for it, so what is in the legacy/ of
// the repository is complete, and what was copied and has no entry is adopted by
// the next run. For each version the production registry is looked at: a tag that is
// not there is copied and read back, and its manifest must be the one the entry pins;
// a tag with the same manifest is adopted, and a tag with another one is a conflict
// and is left as it is. Only then the entry is written, with the URL that points to
// the manifest in the production registry. The logos come before the versions, and
// a logo production has is never replaced.
//
// The error is for what stops the whole run, such as a logo that cannot be copied;
// what happened to each version is in the Result.
func Run(ctx context.Context, opts Options, plan Plan, reg Registry, report io.Writer) (Result, error) {
	var result Result
	for _, logo := range plan.Logos {
		if err := copyLogo(opts.Repo, logo); err != nil {
			return result, err
		}
		if err := say(report, "copied logo %s\n", logo.Name); err != nil {
			return result, err
		}
	}
	for _, item := range plan.Release {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		line, err := releaseVersion(ctx, opts.Repo, item, reg, &result)
		if err != nil {
			return result, err
		}
		if err := say(report, "%s\n", line); err != nil {
			return result, err
		}
	}
	return result, nil
}

// releaseVersion releases a version, records what happened in result and returns the
// line that says it. The error is only for a failure to say it.
func releaseVersion(ctx context.Context, repoDir string, item Item, reg Registry, result *Result) (string, error) {
	name := fmt.Sprintf("%s %s", item.Chart, item.Entry.Version)
	fail := func(err error) (string, error) {
		result.Failed = append(result.Failed, Failure{Item: item, Err: err})
		return fmt.Sprintf("failed %s: %v", name, err), nil
	}

	did := "adopted"
	info, err := reg.Inspect(ctx, item.Destination.String())
	switch {
	case errors.Is(err, oci.ErrNotFound):
		if err := reg.Copy(ctx, item); err != nil {
			return fail(fmt.Errorf("copy %s: %w", item.Destination, err))
		}
		if info, err = reg.Inspect(ctx, item.Destination.String()); err != nil {
			return fail(fmt.Errorf("read %s after it was copied: %w", item.Destination, err))
		}
		did = "released"
	case err != nil:
		return fail(fmt.Errorf("read %s: %w", item.Destination, err))
	case info.ManifestDigest != item.ManifestDigest:
		result.Conflicts = append(result.Conflicts, Conflict{Item: item, Existing: info.ManifestDigest})
		return fmt.Sprintf("conflict %s: %s has the manifest %s, and staging has %s", name, item.Destination, info.ManifestDigest, item.ManifestDigest), nil
	}
	if info.ManifestDigest != item.ManifestDigest {
		return fail(fmt.Errorf("%s has the manifest %s after the copy, and staging has %s", item.Destination, info.ManifestDigest, item.ManifestDigest))
	}
	if err := writeEntry(repoDir, item); err != nil {
		return fail(err)
	}
	if did == "released" {
		result.Released = append(result.Released, item)
	} else {
		result.Adopted = append(result.Adopted, item)
	}
	return did + " " + name, nil
}

// writeEntry writes the legacy entry of a version: the entry of staging as it is,
// with the URL that points to the manifest in the production registry. It never
// replaces a file.
func writeEntry(repoDir string, item Item) error {
	entry := *item.Entry
	entry.URLs = []string{"oci://" + item.Destination.String() + "@" + item.ManifestDigest}
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

// copyLogo copies a logo of staging into the production repository. It never
// replaces a file.
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
