// Package importer brings the versions of the old system into a charts repository
// as legacy entries. It reads the index and the archives of a checkout of the old
// system, works out what is not in the repository yet, and says what it would do.
package importer

import (
	"fmt"
	"strconv"
	"strings"

	repo "helm.sh/helm/v4/pkg/repo/v1"

	"github.com/rancher/cargueiro/internal/rules"
	"github.com/rancher/cargueiro/internal/version"
)

// Candidate is a version of the source that belongs to the family and the
// Rancher minor that were asked for.
type Candidate struct {
	Chart string
	Entry *repo.ChartVersion
}

// Select returns the versions in source of the charts of family whose chart
// major is the one of the Rancher minor, such as 2.12. Each chart comes in the
// order of the family, the main chart first, and its versions newest first.
//
// Only the minors of the old system can be imported: the charts of a Rancher that
// the new system makes charts for are released, not imported. A chart that the
// source does not have gives nothing.
func Select(cfg *rules.Config, source *repo.IndexFile, family, minor string) ([]Candidate, error) {
	fam, err := cfg.Family(family)
	if err != nil {
		return nil, err
	}
	versions := cfg.Versions()
	major, found := versions.Majors[minor]
	if !found {
		return nil, fmt.Errorf("the rules file has no chart major for Rancher %s", minor)
	}
	if version.SupportedRancher(minor) {
		return nil, fmt.Errorf("the charts of Rancher %s are made by the new system: only the charts of the old system are imported, which are the ones before 2.%d", minor, version.MinRancherMinor)
	}
	policy, err := version.NewPolicy(versions.Prereleases, versions.Majors)
	if err != nil {
		return nil, err
	}

	var selected []Candidate
	for _, name := range fam.Names() {
		rule, err := cfg.Find(name)
		if err != nil {
			return nil, err
		}
		byVersion := make(map[string]*repo.ChartVersion)
		var names []string
		for _, entry := range source.Entries[name] {
			if majorOf(entry.Version) != major {
				continue
			}
			byVersion[entry.Version] = entry
			names = append(names, entry.Version)
		}
		ordered, err := policy.SortNewestFirst(names, rule.PlainVersion)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, v := range ordered {
			selected = append(selected, Candidate{Chart: name, Entry: byVersion[v]})
		}
	}
	return selected, nil
}

// majorOf returns the first number of a version, or 0 when it does not start
// with one.
func majorOf(v string) int {
	first, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(first)
	if err != nil {
		return 0
	}
	return n
}
