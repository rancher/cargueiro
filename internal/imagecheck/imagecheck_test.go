package imagecheck

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rancher/cargueiro/internal/oci"
)

const host = "stgregistry.suse.com"

// fakeRegistry says which images it has, and records what it was asked for.
type fakeRegistry struct {
	mu      sync.Mutex
	has     map[string]bool
	failing map[string]error
	asked   []oci.ChartRef
}

func (f *fakeRegistry) resolve(_ context.Context, ref oci.ChartRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, ref)
	if err := f.failing[ref.Repository+":"+ref.Tag]; err != nil {
		return err
	}
	if !f.has[ref.Repository+":"+ref.Tag] {
		return fmt.Errorf("%s: %w", ref, oci.ErrNotFound)
	}
	return nil
}

func (f *fakeRegistry) askedFor() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var got []string
	for _, ref := range f.asked {
		got = append(got, ref.String())
	}
	sort.Strings(got)
	return got
}

func registryWith(images ...string) *fakeRegistry {
	f := &fakeRegistry{has: map[string]bool{}, failing: map[string]error{}}
	for _, image := range images {
		f.has[image] = true
	}
	return f
}

func TestCheck(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	tests := []struct {
		name        string
		registry    *fakeRegistry
		images      []string
		wantChecked []string
		wantAsked   []string
		wantErr     string
	}{
		{
			name:        "every image is there",
			registry:    registryWith("rancher/a:1", "rancher/b:2"),
			images:      []string{"rancher/a:1", "rancher/b:2"},
			wantChecked: []string{host + "/rancher/a:1", host + "/rancher/b:2"},
			wantAsked:   []string{host + "/rancher/a:1", host + "/rancher/b:2"},
		},
		{
			name:        "the images are returned in the order they were given",
			registry:    registryWith("rancher/b:2", "rancher/a:1"),
			images:      []string{"rancher/b:2", "rancher/a:1"},
			wantChecked: []string{host + "/rancher/b:2", host + "/rancher/a:1"},
			wantAsked:   []string{host + "/rancher/a:1", host + "/rancher/b:2"},
		},
		{
			name:     "one image is not there",
			registry: registryWith("rancher/a:1"),
			images:   []string{"rancher/a:1", "rancher/helm-controller:v0.17.10"},
			wantAsked: []string{
				host + "/rancher/a:1",
				host + "/rancher/helm-controller:v0.17.10",
			},
			wantErr: "1 of 2 images of the chart cannot be found in " + host + ":\n" +
				"  rancher/helm-controller:v0.17.10: not found",
		},
		{
			name:     "every image that is not there is reported, sorted",
			registry: registryWith("rancher/a:1"),
			images:   []string{"rancher/klipper-helm:v0.14.0", "rancher/a:1", "rancher/helm-controller:v0.17.10"},
			wantAsked: []string{
				host + "/rancher/a:1",
				host + "/rancher/helm-controller:v0.17.10",
				host + "/rancher/klipper-helm:v0.14.0",
			},
			wantErr: "2 of 3 images of the chart cannot be found in " + host + ":\n" +
				"  rancher/helm-controller:v0.17.10: not found\n" +
				"  rancher/klipper-helm:v0.14.0: not found",
		},
		{
			name:        "an image that is listed twice is asked for once",
			registry:    registryWith("rancher/a:1"),
			images:      []string{"rancher/a:1", "rancher/a:1"},
			wantChecked: []string{host + "/rancher/a:1"},
			wantAsked:   []string{host + "/rancher/a:1"},
		},
		{
			name:        "an image with a digest is asked for by its digest",
			registry:    registryWith("rancher/a:" + digest),
			images:      []string{"rancher/a@" + digest},
			wantChecked: []string{host + "/rancher/a:" + digest},
			wantAsked:   []string{host + "/rancher/a:" + digest},
		},
		{
			name: "an image the registry will not let read is reported with the reason",
			registry: func() *fakeRegistry {
				f := registryWith("rancher/a:1")
				f.failing["rancher/b:2"] = errors.New("the registry granted no access to b")
				return f
			}(),
			images:    []string{"rancher/a:1", "rancher/b:2"},
			wantAsked: []string{host + "/rancher/a:1", host + "/rancher/b:2"},
			wantErr: "1 of 2 images of the chart cannot be found in " + host + ":\n" +
				"  rancher/b:2: the registry granted no access to b",
		},
		{
			name:      "an image that is not written repository:tag is reported, and the others are still asked for",
			registry:  registryWith("rancher/a:1"),
			images:    []string{"rancher/a:1", "rancher/b"},
			wantAsked: []string{host + "/rancher/a:1"},
			wantErr: "1 of 2 images of the chart cannot be found in " + host + ":\n" +
				"  rancher/b: must look like \"repository:tag\" or \"repository@digest\"",
		},
		{
			name:        "a chart with no images has nothing to find",
			registry:    registryWith(),
			images:      nil,
			wantChecked: []string{},
			wantAsked:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Check(context.Background(), host, tt.images, tt.registry.resolve)

			require.Equal(t, tt.wantAsked, tt.registry.askedFor())
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantChecked, got)
		})
	}
}

func TestCheckAsksForEveryImageOnce(t *testing.T) {
	var images, want []string
	f := registryWith()
	for i := range 40 {
		image := fmt.Sprintf("rancher/image-%02d:v%d", i, i)
		images = append(images, image)
		f.has[image] = true
		want = append(want, host+"/"+image)
	}
	sort.Strings(want)

	got, err := Check(context.Background(), host, images, f.resolve)

	require.NoError(t, err)
	require.Len(t, got, 40)
	require.Equal(t, want, f.askedFor())
}
