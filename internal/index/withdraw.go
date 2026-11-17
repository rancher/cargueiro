package index

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	repo "helm.sh/helm/v4/pkg/repo/v1"
)

// withdrawnLine is what Withdraw adds to an entry.
const withdrawnLine = "withdrawn: true\n"

// Withdraw marks the published version of the entry in path as withdrawn, so that the next
// Write takes it out of the index. path is entries/<chart>/<version>.yaml of a charts
// repository, which is the folder that holds entries/.
//
// It adds withdrawn: true to the entry as text and changes nothing else of it, and does not
// touch index.yaml or assets/. It refuses a path that is not the file of an entry, a legacy
// entry, an entry that does not hold the chart and the version its path says, an entry that
// is withdrawn already, and a version that is not in the index.yaml of the repository. With
// dryRun it checks all that and writes nothing. It returns the version it withdrew.
func Withdraw(path string, dryRun bool) (Ref, error) {
	repoDir, ref, err := entryPath(path)
	if err != nil {
		return Ref{}, err
	}
	entry, err := readEntry(path, ref.Chart, ref.Version+".yaml")
	if err != nil {
		return Ref{}, err
	}
	if entry.Withdrawn {
		return Ref{}, fmt.Errorf("%s %s is withdrawn already", ref.Chart, ref.Version)
	}

	published, err := ReadPublished(repoDir)
	if err != nil {
		return Ref{}, err
	}
	if published == nil || !isListed(published.Entries, ref) {
		return Ref{}, fmt.Errorf("%s %s is not published: it is not in %s of %s, and only a published version can be withdrawn", ref.Chart, ref.Version, File, repoDir)
	}
	if dryRun {
		return ref, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Ref{}, fmt.Errorf("read entry: %w", err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	if err := replaceFile(path, append(data, withdrawnLine...)); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

// entryPath splits entries/<chart>/<version>.yaml, with whatever folder holds entries/ in
// front of it, into that folder and the version it names.
func entryPath(path string) (string, Ref, error) {
	clean := filepath.Clean(path)
	version, ok := strings.CutSuffix(filepath.Base(clean), ".yaml")
	chartDir := filepath.Dir(clean)
	entriesDir := filepath.Dir(chartDir)
	if !ok || version == "" || filepath.Base(entriesDir) != EntriesDir {
		return "", Ref{}, fmt.Errorf("%s is not an entry: the path must be %s/<chart>/<version>.yaml", path, EntriesDir)
	}
	return filepath.Dir(entriesDir), Ref{Chart: filepath.Base(chartDir), Version: version}, nil
}

// isListed says whether the index lists the version.
func isListed(entries map[string]repo.ChartVersions, ref Ref) bool {
	for _, v := range entries[ref.Chart] {
		if v.Version == ref.Version {
			return true
		}
	}
	return false
}

// replaceFile replaces the file at path with data, whole or not at all, with the mode it has.
func replaceFile(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("write entry: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".withdraw-*")
	if err != nil {
		return fmt.Errorf("write entry: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write entry: %w", err), tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write entry: %w", err)
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return fmt.Errorf("write entry: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write entry: %w", err)
	}
	return nil
}
