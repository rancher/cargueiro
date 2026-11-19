package annotations

import (
	"errors"
	"fmt"
)

// HelmImages returns the images of the value of a helm.sh/images annotation, as
// they are written and in the order of the list, the ones of subcharts included.
// It only needs every entry to have an image: whether an image is well formed is
// what ValidateHelmImages says.
func HelmImages(value string) ([]string, error) {
	entries, err := parseHelmImages(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", imageDependenciesAnnotation, err)
	}

	images := make([]string, 0, len(entries))
	for i, entry := range entries {
		if entry == nil || entry.Image == "" {
			return nil, errors.New(entryProblem(i+1, "has no image"))
		}
		images = append(images, entry.Image)
	}
	return images, nil
}
