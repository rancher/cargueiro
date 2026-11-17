package index

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Ref names one version of one chart.
type Ref struct {
	Chart   string
	Version string
}

// State is what a charts repository holds about its versions, seen from the
// three places that must agree.
type State struct {
	// Entries are the versions that have an entry file.
	Entries []Ref
	// Withdrawn are the entries that are marked withdrawn. They are a part of Entries.
	Withdrawn []Ref
	// Published are the versions listed in the index.yaml that is committed.
	Published []Ref
	// Archives are the versions that have a .tgz in assets/.
	Archives []Ref
	// Legacy are the versions that have an entry in legacy/: the ones of the old
	// system, imported as they are. They have no archive in assets/, and none of
	// them is also in Entries.
	Legacy []Ref
}

// Plan is what has to be done to the index so that it holds what the entries
// say: the versions to add to it, and the versions to take out of it.
type Plan struct {
	Add []Ref
	// AddLegacy are the legacy versions to add. Nothing is checked or pulled for
	// them, because they are written as they were published.
	AddLegacy []Ref
	Withdraw  []Ref
}

// Report says what the plan does, one version to a line: the ones to add, then
// the legacy ones to add, then the ones to withdraw. A plan that does nothing says so.
func (p Plan) Report() string {
	if len(p.Add) == 0 && len(p.AddLegacy) == 0 && len(p.Withdraw) == 0 {
		return "nothing to change\n"
	}

	var report strings.Builder
	for _, ref := range p.Add {
		fmt.Fprintf(&report, "add %s %s\n", ref.Chart, ref.Version)
	}
	for _, ref := range p.AddLegacy {
		fmt.Fprintf(&report, "add legacy %s %s\n", ref.Chart, ref.Version)
	}
	for _, ref := range p.Withdraw {
		fmt.Fprintf(&report, "withdraw %s %s\n", ref.Chart, ref.Version)
	}
	return report.String()
}

// Plan works out what to do, with each list sorted by chart and then by version.
//
// A legacy version is never withdrawn, has no archive, and is added when it is not in
// the index yet. It cannot also be an entry.
//
// A version is never removed once it is in the index, other than by withdrawing it,
// and the index and assets/ never hold what the entries do not. So Plan fails,
// naming every version at fault, when the three places disagree in any other
// way. Withdrawing is one way: a version that is out of the index but has an archive
// and an entry that is not withdrawn is a new entry that already has an archive.
func (s State) Plan() (Plan, error) {
	entries, withdrawn := refSet(s.Entries), refSet(s.Withdrawn)
	published, archives := refSet(s.Published), refSet(s.Archives)
	legacy := refSet(s.Legacy)

	all := slices.Concat(s.Entries, s.Legacy, s.Published, s.Archives)
	slices.SortFunc(all, compareRefs)
	all = slices.Compact(all)

	var plan Plan
	var problems []error
	for _, ref := range all {
		_, entry := entries[ref]
		_, isLegacy := legacy[ref]
		_, isWithdrawn := withdrawn[ref]
		_, indexed := published[ref]
		_, archived := archives[ref]

		switch {
		case isLegacy && entry:
			problems = append(problems, fmt.Errorf("%s %s is in entries/ and in legacy/", ref.Chart, ref.Version))
		case isLegacy && archived:
			problems = append(problems, fmt.Errorf("assets/ has an archive of %s %s, which is a legacy version: it has none", ref.Chart, ref.Version))
		case isLegacy && !indexed:
			plan.AddLegacy = append(plan.AddLegacy, ref)
		case isLegacy:
			// Published already, as it was imported.
		case indexed && !entry:
			problems = append(problems, fmt.Errorf("%s %s is in index.yaml but has no entry: entries are never removed", ref.Chart, ref.Version))
		case archived && !entry:
			problems = append(problems, fmt.Errorf("assets/ has an archive of %s %s but there is no entry for it", ref.Chart, ref.Version))
		case indexed && !archived:
			problems = append(problems, fmt.Errorf("%s %s is in index.yaml but has no archive in assets/", ref.Chart, ref.Version))
		case isWithdrawn && !indexed && !archived:
			problems = append(problems, fmt.Errorf("%s %s is withdrawn but was never published: it is not in index.yaml and has no archive in assets/", ref.Chart, ref.Version))
		case isWithdrawn && indexed:
			plan.Withdraw = append(plan.Withdraw, ref)
		case isWithdrawn:
			// Withdrawn already: out of the index, with its archive kept.
		case archived && !indexed:
			problems = append(problems, fmt.Errorf("%s %s is not in index.yaml yet but already has an archive in assets/", ref.Chart, ref.Version))
		case !indexed:
			plan.Add = append(plan.Add, ref)
		}
	}
	if len(problems) > 0 {
		return Plan{}, errors.Join(problems...)
	}
	return plan, nil
}

func compareRefs(a, b Ref) int {
	return cmp.Or(cmp.Compare(a.Chart, b.Chart), cmp.Compare(a.Version, b.Version))
}

func refSet(refs []Ref) map[Ref]struct{} {
	set := make(map[Ref]struct{}, len(refs))
	for _, ref := range refs {
		set[ref] = struct{}{}
	}
	return set
}
