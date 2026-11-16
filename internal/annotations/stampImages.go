package annotations

import (
	"fmt"
	"strings"

	"github.com/rancher/cargueiro/internal/chart"
)

// GenerateHelmImages returns the helm.sh/images annotation of a chart made from
// the images in its values.yaml and in every subchart's. values is the parsed
// values.yaml, and dependencies the subcharts under charts/, as chart.Load
// reads them.
//
// An annotation that exists is kept as it is and extended with the images it
// does not list yet, one entry each, named after the last part of the
// repository (rancher/fleet-agent gives fleet-agent). An image the annotation
// already lists is not added again, whatever its name. An image found in a
// subchart's values.yaml gets a "dependency" naming that subchart, the same way
// scanDependencies names it. A second image whose repository is the one a name
// already has, under a different tag, gets its own entry under that same name,
// the way HIP-15's own Kafka example names two tags of bitnami-shell "shell".
//
// It reports problems, and returns no annotation, when values.yaml or a
// subchart's has an image that is not written with a repository and a tag, or
// has the tag latest, when the annotation that exists is not valid, and when a
// name would be used by two different repositories. changed is false when
// there is nothing to add.
func GenerateHelmImages(annotations map[string]string, values map[string]any, dependencies map[string]*chart.Files) (value string, changed bool, problems []string) {
	images, valuesProblems := scanValues(values)
	depImages, depProblems := scanDependencies(dependencies)
	images = append(images, depImages...)
	problems = append(problems, valuesProblems...)
	problems = append(problems, depProblems...)
	problems = append(problems, checkDependenciesAnnotationStandard(annotations)...)
	if len(problems) > 0 {
		return "", false, problems
	}

	existing := annotations[imageDependenciesAnnotation]
	listed := make(map[string]bool)
	owners := make(map[string]struct{ repository, image string })
	if strings.TrimSpace(existing) != "" {
		entries, err := parseHelmImages(existing)
		if err != nil {
			return "", false, []string{fmt.Sprintf("%s: %v", imageDependenciesAnnotation, err)}
		}
		for _, entry := range entries {
			image := strings.TrimSpace(entry.Image)
			listed[image] = true
			name := strings.TrimSpace(entry.Name)
			if _, used := owners[name]; !used {
				owners[name] = struct{ repository, image string }{imageRepository(image), image}
			}
		}
	}

	var added strings.Builder
	for _, found := range images {
		if listed[found.Image] {
			continue
		}
		name := imageName(found.Image)
		repository := imageRepository(found.Image)
		if owner, used := owners[name]; used && owner.repository != repository {
			problems = append(problems, valuesProblem(found.source(), found.Path,
				"image %q would be named %q, which %q has already", found.Image, name, owner.image))
			continue
		} else if !used {
			owners[name] = struct{ repository, image string }{repository, found.Image}
		}
		listed[found.Image] = true
		if found.Dependency == "" {
			fmt.Fprintf(&added, "- name: %s\n  image: %s\n", name, found.Image)
		} else {
			fmt.Fprintf(&added, "- name: %s\n  image: %s\n  dependency: %s\n", name, found.Image, found.Dependency)
		}
	}
	if len(problems) > 0 {
		return "", false, problems
	}
	if added.Len() == 0 {
		return existing, false, nil
	}

	value = added.String()
	if strings.TrimSpace(existing) != "" {
		value = strings.TrimRight(existing, "\n") + "\n" + value
	}
	if after := checkDependenciesAnnotationStandard(map[string]string{imageDependenciesAnnotation: value}); len(after) > 0 {
		return "", false, after
	}
	return value, true, nil
}

// imageName is the last part of the repository of image, written
// repository:tag.
func imageName(image string) string {
	repository := imageRepository(image)
	if slash := strings.LastIndex(repository, "/"); slash >= 0 {
		return repository[slash+1:]
	}
	return repository
}

// imageRepository is the repository of image, written repository:tag.
func imageRepository(image string) string {
	if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		return image[:colon]
	}
	return image
}
