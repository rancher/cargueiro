package annotations

import (
	"fmt"
	"sort"
	"strings"
)

// valuesImage is an image found in values.yaml.
type valuesImage struct {
	// Path locates the image in values.yaml, such as "image.controller".
	Path string
	// Image is "repository:tag".
	Image string
	// Dependency names the subchart this image belongs to, the way it should
	// appear in the helm.sh/images annotation's "dependency" field: the
	// subchart's own name, or "parent/child" for one nested further in. Empty
	// for an image found in the chart's own values.yaml.
	Dependency string
}

// source is where image was found, as a problem about it should name it:
// "values.yaml" for the chart's own, "charts/<name>/values.yaml" for a
// subchart's.
func (img valuesImage) source() string {
	if img.Dependency == "" {
		return "values.yaml"
	}
	return "charts/" + img.Dependency + "/values.yaml"
}

// scanValues finds the images in values.yaml and reports the ones that are not
// written with a repository and a tag. An image is a map with a "repository"
// key. A map with an empty repository and an empty tag has no image configured
// and is skipped. Other keys, including one called "image" that holds a
// string, are not images and are ignored.
//
// HIP-15 says nothing about values.yaml: reading images from it is Rancher
// policy.
func scanValues(values map[string]any) ([]valuesImage, []string) {
	return scanSource(values, "values.yaml", "")
}

// scanSource is scanValues for a values.yaml read from source, such as
// "charts/sub/values.yaml" for a subchart's, tagging every image it finds with
// dependency, as scanDependencies needs.
func scanSource(values map[string]any, source, dependency string) ([]valuesImage, []string) {
	scan := valuesScan{source: source, dependency: dependency}
	scan.walk(values, "")
	return scan.images, scan.problems
}

type valuesScan struct {
	// source and dependency are copied onto every image and problem found, as
	// scanSource documents.
	source, dependency string

	images   []valuesImage
	problems []string
}

// walk visits node and everything under it, in key order.
func (s *valuesScan) walk(node any, path string) {
	switch n := node.(type) {
	case map[string]any:
		s.walkMap(n, path)
	case map[any]any:
		// The YAML decoder returns this for a mapping with a key that is not
		// text, such as a port number.
		s.walkMap(textKeys(n), path)
	case []any:
		for i, item := range n {
			s.walk(item, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

// textKeys returns m with its keys written as text.
func textKeys(m map[any]any) map[string]any {
	keys := make(map[string]any, len(m))
	for key, value := range m {
		keys[fmt.Sprint(key)] = value
	}
	return keys
}

func (s *valuesScan) walkMap(m map[string]any, path string) {
	if _, isImage := m["repository"]; isImage {
		s.imageBlock(m, path)
	}

	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		childPath := key
		if path != "" {
			childPath = path + "." + key
		}
		s.walk(m[key], childPath)
	}
}

// imageBlock reads a map that has a "repository" key.
func (s *valuesScan) imageBlock(block map[string]any, path string) {
	repository, tag := block["repository"], block["tag"]

	switch {
	case notString(repository) || notString(tag):
		s.problems = append(s.problems, valuesProblem(s.source, path, `"repository" and "tag" must be strings`))
	case blank(repository) && blank(tag):
		// No image configured.
	case blank(tag):
		s.problems = append(s.problems, valuesProblem(s.source, path, `"repository" is set but "tag" is empty`))
	case blank(repository):
		s.problems = append(s.problems, valuesProblem(s.source, path, `"tag" is set but "repository" is empty`))
	case tag == latestTag:
		s.problems = append(s.problems, valuesProblem(s.source, path, `"tag" must not be %q`, latestTag))
	default:
		repositoryText, _ := repository.(string)
		tagText, _ := tag.(string)
		s.images = append(s.images, valuesImage{Path: path, Image: repositoryText + ":" + tagText, Dependency: s.dependency})
	}
}

func valuesProblem(source, path, format string, args ...any) string {
	if path == "" {
		path = "."
	}
	return fmt.Sprintf("%s %s: %s", source, path, fmt.Sprintf(format, args...))
}

// notString reports a value that is set but is not text, such as a number.
func notString(v any) bool {
	_, isString := v.(string)
	return v != nil && !isString
}

// blank reports a missing, null or empty value.
func blank(v any) bool {
	s, _ := v.(string)
	return strings.TrimSpace(s) == ""
}
