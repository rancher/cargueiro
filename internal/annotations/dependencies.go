package annotations

import (
	"sort"

	"github.com/rancher/cargueiro/internal/chart"
)

// scanDependencies finds the images in the values.yaml of every subchart under
// dependencies, found recursively through their own Dependencies. An image's
// Dependency is the subchart's own name, or "parent/child" for one nested
// further in, which is how it should appear in the helm.sh/images annotation's
// "dependency" field. A problem is reported with the subchart's real path,
// such as "charts/parent/charts/child/values.yaml image: ...".
//
// Subcharts are visited in name order, and nested ones right after their
// parent, so the result is deterministic.
func scanDependencies(dependencies map[string]*chart.Files) ([]valuesImage, []string) {
	return scanDependenciesAt(dependencies, "", "")
}

// scanDependenciesAt is scanDependencies for the subcharts nested inside
// dependencyPrefix, found at pathPrefix on disk.
func scanDependenciesAt(dependencies map[string]*chart.Files, dependencyPrefix, pathPrefix string) ([]valuesImage, []string) {
	names := make([]string, 0, len(dependencies))
	for name := range dependencies {
		names = append(names, name)
	}
	sort.Strings(names)

	var images []valuesImage
	var problems []string
	for _, name := range names {
		sub := dependencies[name]
		dependency, path := name, pathPrefix+"charts/"+name+"/"
		if dependencyPrefix != "" {
			dependency = dependencyPrefix + "/" + name
		}

		subImages, subProblems := scanSource(sub.Values, path+"values.yaml", dependency)
		images = append(images, subImages...)
		problems = append(problems, subProblems...)

		nestedImages, nestedProblems := scanDependenciesAt(sub.Dependencies, dependency, path)
		images = append(images, nestedImages...)
		problems = append(problems, nestedProblems...)
	}
	return images, problems
}
