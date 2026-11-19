// Package imagecheck checks that the images of a chart are in a registry.
package imagecheck

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/rancher/cargueiro/internal/oci"
)

// maxAtOnce is the most images that are asked for at the same time.
const maxAtOnce = 8

// Resolver says whether the registry has the image at ref: it returns nil when
// it does, an error that matches oci.ErrNotFound when it does not, and any other
// error when it could not tell.
type Resolver func(ctx context.Context, ref oci.ChartRef) error

// problem is why one image was not found.
type problem struct {
	image string
	text  string
}

// Check asks the registry host for every image, written "repository:tag" or
// "repository@digest", and returns their references in the order they were given,
// each once. When some are not there, nothing is returned and the error says
// which, all of them together and sorted, and why for each.
func Check(ctx context.Context, host string, images []string, resolve Resolver) ([]string, error) {
	distinct := make([]string, 0, len(images))
	seen := make(map[string]bool, len(images))
	for _, image := range images {
		if !seen[image] {
			seen[image] = true
			distinct = append(distinct, image)
		}
	}

	refs := make([]oci.ChartRef, len(distinct))
	problems := make([]string, len(distinct))
	var wg sync.WaitGroup
	slots := make(chan struct{}, maxAtOnce)
	for i, image := range distinct {
		ref, err := parse(host, image)
		if err != nil {
			problems[i] = err.Error()
			continue
		}
		refs[i] = ref

		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			if err := resolve(ctx, ref); err != nil {
				problems[i] = reason(err)
			}
		}()
	}
	wg.Wait()

	var failed []problem
	checked := make([]string, 0, len(distinct))
	for i, image := range distinct {
		if problems[i] != "" {
			failed = append(failed, problem{image: image, text: problems[i]})
			continue
		}
		checked = append(checked, refs[i].String())
	}
	if len(failed) > 0 {
		return nil, notFoundError(host, len(distinct), failed)
	}
	return checked, nil
}

// parse turns an image into its reference in the registry host.
func parse(host, image string) (oci.ChartRef, error) {
	errForm := errors.New(`must look like "repository:tag" or "repository@digest"`)
	if strings.ContainsAny(image, " \t\r\n") {
		return oci.ChartRef{}, errForm
	}

	repository, reference := image, ""
	if at := strings.LastIndex(image, "@"); at >= 0 {
		repository, reference = image[:at], image[at+1:]
		// A tag may come before the digest, which names the image alone.
		if colon := strings.LastIndex(repository, ":"); colon > strings.LastIndex(repository, "/") {
			repository = repository[:colon]
		}
		if !strings.Contains(reference, ":") {
			return oci.ChartRef{}, errForm
		}
	} else if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		repository, reference = image[:colon], image[colon+1:]
	}
	if repository == "" || reference == "" || strings.HasPrefix(repository, "/") || strings.HasSuffix(repository, "/") {
		return oci.ChartRef{}, errForm
	}
	return oci.ChartRef{Host: host, Repository: repository, Tag: reference}, nil
}

// reason says why the registry did not give an image.
func reason(err error) string {
	if errors.Is(err, oci.ErrNotFound) {
		return "not found"
	}
	return err.Error()
}

// notFoundError is the error for the images that were not found, sorted.
func notFoundError(host string, total int, failed []problem) error {
	sort.Slice(failed, func(i, j int) bool { return failed[i].image < failed[j].image })

	var b strings.Builder
	fmt.Fprintf(&b, "%d of %d images of the chart cannot be found in %s:", len(failed), total, host)
	for _, p := range failed {
		fmt.Fprintf(&b, "\n  %s: %s", p.image, p.text)
	}
	return errors.New(b.String())
}
