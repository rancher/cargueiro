//go:build e2e

// Package pusheddependencies holds the e2e tests of `cargueiro
// check-pushed-dependencies`, run against the real binary and the local registry
// that asks for no password.
package pusheddependencies

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/rancher/cargueiro/tests/internal/harness"
)

func TestMain(m *testing.M) {
	os.Exit(harness.Main(m))
}

// image is an image the test puts in the registry, or only names in a chart.
type image struct {
	// repository and tag as the chart writes them.
	repository string
	tag        string
	// dependency, when it is not empty, names the subchart the image is of.
	dependency string
}

func (i image) String() string { return i.repository + ":" + i.tag }

// uniqueRepository gives a repository that no other run used, because the
// registry keeps what the tests push.
func uniqueRepository(name string) string {
	return fmt.Sprintf("rancher/e2e-%s-%d", name, time.Now().UnixNano())
}

// push puts an image in the registry at address and returns the digest of its
// manifest.
func push(t *testing.T, address string, i image) string {
	t.Helper()

	ctx := context.Background()
	store := memory.New()
	desc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, "application/vnd.cargueiro.e2e.image", oras.PackManifestOptions{})
	require.NoError(t, err)
	require.NoError(t, store.Tag(ctx, desc, i.tag))

	repo, err := remote.NewRepository(address + "/" + i.repository)
	require.NoError(t, err)
	repo.PlainHTTP = true
	_, err = oras.Copy(ctx, store, i.tag, repo, i.tag, oras.DefaultCopyOptions)
	require.NoError(t, err)
	return desc.Digest.String()
}

// writeChart writes a chart directory whose helm.sh/images lists the images as
// they are given, and returns it.
func writeChart(t *testing.T, images []string, dependencies map[string]string) string {
	t.Helper()

	var b strings.Builder
	b.WriteString("name: prometheus-federator\nversion: 111.0.0+up8.0.0-rc.3\nannotations:\n  helm.sh/images: |\n")
	for _, image := range images {
		name := image[strings.LastIndex(image, "/")+1:]
		fmt.Fprintf(&b, "    - name: %s\n      image: %s\n", strings.NewReplacer(":", "-", "@", "-").Replace(name), image)
		if dependency := dependencies[image]; dependency != "" {
			fmt.Fprintf(&b, "      dependency: %s\n", dependency)
		}
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(b.String()), 0o600))
	return dir
}

// check runs the command on dir against the registry at address.
func check(t *testing.T, address, dir string) harness.Result {
	t.Helper()

	return harness.Cargueiro(t, t.TempDir(), "check-pushed-dependencies", dir, "--registry", address, "--plain-http")
}

func TestEveryImageIsThere(t *testing.T) {
	address := harness.OpenRegistry(t)
	a := image{repository: uniqueRepository("a"), tag: "v1"}
	b := image{repository: uniqueRepository("b"), tag: "v2-build20261117"}
	push(t, address, a)
	push(t, address, b)
	dir := writeChart(t, []string{a.String(), b.String()}, nil)

	res := check(t, address, dir)

	require.Zero(t, res.ExitCode, res.Stderr)
	require.Empty(t, res.Stderr)
	require.Equal(t, address+"/"+a.String()+"\n"+address+"/"+b.String()+"\n", res.Stdout)
}

// TestImagesNotPushedAreAllReported is the case the command is for: a chart
// that lists images, some of which were never pushed, the way a chart can reach
// the catalog before its images reach the registry.
func TestImagesNotPushedAreAllReported(t *testing.T) {
	address := harness.OpenRegistry(t)
	pushed := image{repository: uniqueRepository("prometheus-federator"), tag: "v8.0.0-rc.3"}
	helmController := image{repository: uniqueRepository("helm-controller"), tag: "v0.17.10"}
	klipper := image{repository: uniqueRepository("klipper-helm"), tag: "v0.14.0-build20261117"}
	push(t, address, pushed)
	dir := writeChart(t, []string{helmController.String(), klipper.String(), pushed.String()}, nil)

	res := check(t, address, dir)

	require.Equal(t, 1, res.ExitCode)
	require.Empty(t, res.Stdout)
	want := "2 of 3 images of the chart cannot be found in " + address + ":\n"
	for _, missing := range []string{helmController.String(), klipper.String()} {
		require.Contains(t, res.Stderr, "  "+missing+": not found\n")
	}
	require.Contains(t, res.Stderr, want)
	require.NotContains(t, res.Stderr, pushed.String())
}

func TestTheTagIsWhatIsLookedFor(t *testing.T) {
	address := harness.OpenRegistry(t)
	repository := uniqueRepository("tags")
	push(t, address, image{repository: repository, tag: "v1"})
	dir := writeChart(t, []string{repository + ":v2"}, nil)

	res := check(t, address, dir)

	require.Equal(t, 1, res.ExitCode)
	require.Empty(t, res.Stdout)
	require.Contains(t, res.Stderr, "  "+repository+":v2: not found")
}

func TestAnImageOfASubchartIsChecked(t *testing.T) {
	address := harness.OpenRegistry(t)
	there := image{repository: uniqueRepository("top"), tag: "v1"}
	missing := image{repository: uniqueRepository("sub"), tag: "v1", dependency: "sub"}
	push(t, address, there)
	dir := writeChart(t, []string{there.String(), missing.String()}, map[string]string{missing.String(): "sub"})

	res := check(t, address, dir)

	require.Equal(t, 1, res.ExitCode)
	require.Contains(t, res.Stderr, "1 of 2 images of the chart cannot be found")
	require.Contains(t, res.Stderr, "  "+missing.String()+": not found")
}

func TestAnImageWithADigest(t *testing.T) {
	address := harness.OpenRegistry(t)
	i := image{repository: uniqueRepository("pinned"), tag: "v1"}
	digest := push(t, address, i)

	t.Run("the digest is there", func(t *testing.T) {
		res := check(t, address, writeChart(t, []string{i.repository + "@" + digest}, nil))

		require.Zero(t, res.ExitCode, res.Stderr)
		require.Equal(t, address+"/"+i.repository+":"+digest+"\n", res.Stdout)
	})

	t.Run("the digest is not", func(t *testing.T) {
		wrong := "sha256:" + strings.Repeat("0", 64)
		res := check(t, address, writeChart(t, []string{i.repository + "@" + wrong}, nil))

		require.Equal(t, 1, res.ExitCode)
		require.Empty(t, res.Stdout)
		require.Contains(t, res.Stderr, "  "+i.repository+"@"+wrong+": not found")
	})
}

func TestTheChartIsNotChangedAndTheAnswerDoesNotChange(t *testing.T) {
	address := harness.OpenRegistry(t)
	i := image{repository: uniqueRepository("same"), tag: "v1"}
	push(t, address, i)
	dir := writeChart(t, []string{i.String(), uniqueRepository("never") + ":v1"}, nil)
	before, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	require.NoError(t, err)

	first := check(t, address, dir)
	second := check(t, address, dir)

	after, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "Chart.yaml was changed")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "something was written next to Chart.yaml")
	require.Equal(t, 1, first.ExitCode)
	require.Equal(t, first, second)
}

func TestFailuresPrintNothingToStdout(t *testing.T) {
	address := harness.OpenRegistry(t)

	t.Run("a directory that is not a chart", func(t *testing.T) {
		res := check(t, address, t.TempDir())

		require.Equal(t, 1, res.ExitCode)
		require.Empty(t, res.Stdout)
		require.Contains(t, res.Stderr, "Chart.yaml")
	})
}
