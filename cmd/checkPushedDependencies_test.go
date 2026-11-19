package cmd

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rancher/cargueiro/internal/oci"
)

// A chart that lists three images, as one of the staged charts does.
const chartWithImagesYAML = `
name: prometheus-federator
version: 111.0.0+up8.0.0-rc.3
annotations:
  helm.sh/images: |
    - name: helm-controller
      image: rancher/helm-controller:v0.17.10
    - name: klipper-helm
      image: rancher/klipper-helm:v0.14.0-build20261117
    - name: prometheus-federator
      image: rancher/prometheus-federator:v8.0.0-rc.3
`

const chartWithoutImagesYAML = `
name: fleet-crd
version: 111.0.3+up0.16.3
`

const chartWithBadImagesYAML = `
name: fleet
version: 111.0.3+up0.16.3
annotations:
  helm.sh/images: just text
`

// imageCall is one call of the fake registry.
type imageCall struct {
	ref  oci.ChartRef
	opts oci.FetchOptions
}

// fakeImages says which images the registry does not have, and records its calls.
type fakeImages struct {
	missing map[string]bool
	mu      sync.Mutex
	calls   []imageCall
}

func (f *fakeImages) resolve(_ context.Context, ref oci.ChartRef, opts oci.FetchOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, imageCall{ref: ref, opts: opts})
	if f.missing[ref.Repository+":"+ref.Tag] {
		return fmt.Errorf("%s: %w", ref, oci.ErrNotFound)
	}
	return nil
}

// runCheckPushedDependencies executes check-pushed-dependencies with the rules in checkRules.
func runCheckPushedDependencies(t *testing.T, fake *fakeImages, env map[string]string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer
	opts := &rootOptions{
		stderr:    &errOut,
		getenv:    func(key string) string { return env[key] },
		openStore: openerFor(newTestStore(t)),
		prompt:    &fakePrompter{},
		rules:     []byte(checkRules),
	}
	root, err := newRootCmd(&out, &errOut, opts, dependencies{resolveImage: fake.resolve})
	require.NoError(t, err)
	root.SetArgs(append([]string{"check-pushed-dependencies"}, args...))

	err = root.Execute()
	return out.String(), errOut.String(), err
}

func TestCheckPushedDependenciesFound(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		wantStdout string
		wantHost   string
		wantPlain  bool
		wantUser   string
	}{
		{
			name: "every image is in the staging registry of the rules",
			wantStdout: "stgregistry.suse.com/rancher/helm-controller:v0.17.10\n" +
				"stgregistry.suse.com/rancher/klipper-helm:v0.14.0-build20261117\n" +
				"stgregistry.suse.com/rancher/prometheus-federator:v8.0.0-rc.3\n",
			wantHost: "stgregistry.suse.com",
		},
		{
			name: "another registry, over HTTP",
			args: []string{"--registry", "127.0.0.1:5052", "--plain-http"},
			wantStdout: "127.0.0.1:5052/rancher/helm-controller:v0.17.10\n" +
				"127.0.0.1:5052/rancher/klipper-helm:v0.14.0-build20261117\n" +
				"127.0.0.1:5052/rancher/prometheus-federator:v8.0.0-rc.3\n",
			wantHost:  "127.0.0.1:5052",
			wantPlain: true,
		},
		{
			name: "the credentials of the environment are used",
			env:  map[string]string{envUsername: "robot", envPassword: "secret"},
			wantStdout: "stgregistry.suse.com/rancher/helm-controller:v0.17.10\n" +
				"stgregistry.suse.com/rancher/klipper-helm:v0.14.0-build20261117\n" +
				"stgregistry.suse.com/rancher/prometheus-federator:v8.0.0-rc.3\n",
			wantHost: "stgregistry.suse.com",
			wantUser: "robot",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeImages{}
			dir := writeChartDir(t, chartWithImagesYAML, "")

			stdout, stderr, err := runCheckPushedDependencies(t, fake, tt.env, append([]string{dir}, tt.args...)...)

			require.NoError(t, err)
			require.Empty(t, stderr)
			require.Equal(t, tt.wantStdout, stdout)
			require.Len(t, fake.calls, 3)
			for _, call := range fake.calls {
				require.Equal(t, tt.wantHost, call.ref.Host)
				require.Equal(t, tt.wantPlain, call.opts.PlainHTTP)
				require.Equal(t, tt.wantUser, call.opts.Username)
			}
		})
	}
}

// TestCheckPushedDependenciesNotFound is the case that this command is for: a
// chart that was staged with images the registry does not have.
func TestCheckPushedDependenciesNotFound(t *testing.T) {
	fake := &fakeImages{missing: map[string]bool{
		"rancher/helm-controller:v0.17.10":           true,
		"rancher/klipper-helm:v0.14.0-build20261117": true,
	}}
	dir := writeChartDir(t, chartWithImagesYAML, "")

	stdout, _, err := runCheckPushedDependencies(t, fake, nil, dir)

	require.EqualError(t, err, "2 of 3 images of the chart cannot be found in stgregistry.suse.com:\n"+
		"  rancher/helm-controller:v0.17.10: not found\n"+
		"  rancher/klipper-helm:v0.14.0-build20261117: not found")
	require.Empty(t, stdout)
	require.Len(t, fake.calls, 3)
}

func TestCheckPushedDependenciesNothingToCheck(t *testing.T) {
	fake := &fakeImages{}
	dir := writeChartDir(t, chartWithoutImagesYAML, "")

	stdout, stderr, err := runCheckPushedDependencies(t, fake, nil, dir)

	require.NoError(t, err)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	require.Empty(t, fake.calls)
}

func TestCheckPushedDependenciesFails(t *testing.T) {
	tests := []struct {
		name    string
		chart   string
		args    []string
		noDir   bool
		wantErr string
	}{
		{
			name:    "a registry with a path",
			chart:   chartWithImagesYAML,
			args:    []string{"--registry", "stgregistry.suse.com/rancher"},
			wantErr: "--registry takes only host[:port]",
		},
		{
			name:    "a registry that is not a host",
			chart:   chartWithImagesYAML,
			args:    []string{"--registry", "not a host"},
			wantErr: "registry",
		},
		{
			name:    "a helm.sh/images that is not a list",
			chart:   chartWithBadImagesYAML,
			wantErr: "helm.sh/images: must be a YAML list",
		},
		{
			name:    "a directory that is not a chart",
			chart:   "",
			wantErr: "Chart.yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeImages{}
			dir := writeChartDir(t, tt.chart, "")

			stdout, _, err := runCheckPushedDependencies(t, fake, nil, append([]string{dir}, tt.args...)...)

			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, stdout)
			require.Empty(t, fake.calls)
		})
	}
}

func TestCheckPushedDependenciesNeedsOneChartDirectory(t *testing.T) {
	_, _, err := runCheckPushedDependencies(t, &fakeImages{}, nil)
	require.Error(t, err)

	_, _, err = runCheckPushedDependencies(t, &fakeImages{}, nil, "a", "b")
	require.Error(t, err)
}
