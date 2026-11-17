package release

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	chartRef      = "registry.suse.com/rancher/catalog/fleet:111.0.2_up0.17.0"
	agentRef      = "registry.suse.com/rancher/fleet-agent:v0.17.0"
	controllerRef = "registry.suse.com/rancher/fleet:v0.17.0"
)

// stageNew makes a staging repository with one new version of fleet that lists two images, and
// returns the plan to release it with a registry that has the images in staging.
func stageNew(t *testing.T) (Options, Plan, *fakeRegistry) {
	t.Helper()

	staging, production := t.TempDir(), t.TempDir()
	writeStaged(t, staging, "fleet", "111.0.2+up0.17.0", stagedOptions{
		images: "- name: fleet\n  image: rancher/fleet:v0.17.0\n- name: agent\n  image: rancher/fleet-agent:v0.17.0\n",
		icon:   "file://assets/logos/fleet.svg",
	})
	writeFile(t, filepath.Join(staging, "assets", "logos", "fleet.svg"), "logo")
	opts := Options{From: staging, Repo: production}
	plan, err := MakeEntriesPlan(loadRules(t), opts)
	require.NoError(t, err)

	reg := newFakeRegistry()
	reg.stagingImages["stgregistry.suse.com/rancher/fleet:v0.17.0"] = "sha256:img1"
	reg.stagingImages["stgregistry.suse.com/rancher/fleet-agent:v0.17.0"] = "sha256:img2"
	return opts, plan, reg
}

func TestRunReleasesNewVersionsInOrder(t *testing.T) {
	opts, plan, reg := stageNew(t)
	var report bytes.Buffer

	result, err := Run(context.Background(), opts, plan, reg, &report)

	require.NoError(t, err)
	require.True(t, result.Done())
	require.Equal(t, []string{"validate fleet", "image " + controllerRef, "image " + agentRef, "chart " + chartRef}, reg.calls,
		"the chart is checked, then its images are copied, then the chart: the chart is never in production without them")
	require.Equal(t, "sha256:img1", reg.images[controllerRef], "the image has the manifest of staging")
	require.Equal(t, 2, result.ImagesCopied)
	require.Equal(t, "copied logo fleet.svg\nimage copied "+controllerRef+"\nimage copied "+agentRef+"\nreleased fleet 111.0.2+up0.17.0\n", report.String())
	require.Equal(t, "released 1 versions, adopted 0\nimages: copied 2, already in production 0\n", result.Summary())

	staged, err := os.ReadFile(filepath.Join(opts.From, "entries", "fleet", "111.0.2+up0.17.0.yaml"))
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(opts.Repo, "entries", "fleet", "111.0.2+up0.17.0.yaml"))
	require.NoError(t, err)
	want := strings.Replace(string(staged), "oci://stgregistry.suse.com/rancher/catalog/fleet:111.0.2_up0.17.0@sha256:a1", "oci://registry.suse.com/rancher/catalog/fleet:111.0.2_up0.17.0@sha256:a1", 1)
	require.Equal(t, want, string(got), "the entry is the one of staging, byte for byte, with only the registry of its url changed")
	require.FileExists(t, filepath.Join(opts.Repo, "assets", "logos", "fleet.svg"))
	require.NoDirExists(t, filepath.Join(opts.Repo, "legacy"), "legacy is not touched")

	t.Run("a second run has nothing to release", func(t *testing.T) {
		again, err := MakeEntriesPlan(loadRules(t), opts)

		require.NoError(t, err)
		require.Empty(t, again.Release)
	})
}

func TestRunLeavesAnImageThatIsThereAlone(t *testing.T) {
	opts, plan, reg := stageNew(t)
	// production has the controller already, with another digest than staging has
	reg.images[controllerRef] = "sha256:other"
	var report bytes.Buffer

	result, err := Run(context.Background(), opts, plan, reg, &report)

	require.NoError(t, err)
	require.True(t, result.Done(), "an image that is there is not a failure")
	require.Equal(t, "sha256:other", reg.images[controllerRef], "and it is not replaced")
	require.Equal(t, []string{"validate fleet", "image " + agentRef, "chart " + chartRef}, reg.calls, "only the other image is copied")
	require.Contains(t, report.String(), "image present "+controllerRef)
	require.Equal(t, 1, result.ImagesPresent)
	require.Equal(t, 1, result.ImagesCopied)
	require.Contains(t, result.Summary(), "images: copied 1, already in production 1")
}

func TestRunDoesNotReleaseWhatIsMissingAnImage(t *testing.T) {
	opts, plan, reg := stageNew(t)
	delete(reg.stagingImages, "stgregistry.suse.com/rancher/fleet-agent:v0.17.0")

	result, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})

	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	require.ErrorContains(t, result.Failed[0].Err, "is in neither registry")
	require.NotContains(t, reg.calls, "chart "+chartRef, "the chart is not copied without its images")
	require.NoFileExists(t, filepath.Join(opts.Repo, "entries", "fleet", "111.0.2+up0.17.0.yaml"))
}

func TestRunDoesNotCopyAnythingOfAnInvalidChart(t *testing.T) {
	opts, plan, reg := stageNew(t)
	reg.invalid["fleet 111.0.2+up0.17.0"] = errors.New("the chart is not valid: kube-version has no upper bound")

	result, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})

	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	require.ErrorContains(t, result.Failed[0].Err, "kube-version has no upper bound")
	require.Equal(t, []string{"validate fleet"}, reg.calls, "not an image, not the chart")
	require.NoFileExists(t, filepath.Join(opts.Repo, "entries", "fleet", "111.0.2+up0.17.0.yaml"))
}

func TestRunChecksAnImageAfterItWasCopied(t *testing.T) {
	opts, plan, reg := stageNew(t)
	reg.wrongImage[controllerRef] = "sha256:bad"

	result, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})

	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	require.ErrorContains(t, result.Failed[0].Err, "has the manifest sha256:bad after the copy, and staging has sha256:img1")
	require.NotContains(t, reg.calls, "chart "+chartRef)
}

func TestRunReportsAnImageThatCannotBeRead(t *testing.T) {
	opts, plan, reg := stageNew(t)
	reg.stagingImageFail["stgregistry.suse.com/rancher/fleet:v0.17.0"] = errors.New("the registry is down")

	result, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})

	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	require.ErrorContains(t, result.Failed[0].Err, "the registry is down")
	require.Contains(t, result.Summary(), "FAILED: 1 versions were not released")
}

func TestRunLeavesAConflictOfANewVersionAsItIs(t *testing.T) {
	opts, plan, reg := stageNew(t)
	reg.has[chartRef] = "sha256:other"

	result, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})

	require.NoError(t, err)
	require.Len(t, result.Conflicts, 1)
	require.Equal(t, "sha256:other", reg.has[chartRef], "the tag is not touched")
	require.NoFileExists(t, filepath.Join(opts.Repo, "entries", "fleet", "111.0.2+up0.17.0.yaml"))
	require.Contains(t, result.Summary(), "CONFLICT: 1 versions")
}

func TestRunAdoptsANewVersionThatWasCopiedByARunThatWasCutOff(t *testing.T) {
	opts, plan, reg := stageNew(t)
	reg.has[chartRef] = "sha256:a1"
	reg.images[controllerRef] = "sha256:img1"
	reg.images[agentRef] = "sha256:img2"

	result, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})

	require.NoError(t, err)
	require.Len(t, result.Adopted, 1)
	require.Equal(t, []string{"validate fleet"}, reg.calls, "nothing is copied again")
	require.FileExists(t, filepath.Join(opts.Repo, "entries", "fleet", "111.0.2+up0.17.0.yaml"))
}

func TestRunRefusesAnEntryWhoseUrlIsNotInItsFileOnce(t *testing.T) {
	opts, plan, reg := stageNew(t)
	plan.Release[0].Raw = []byte("name: fleet\nversion: 111.0.2+up0.17.0\n")

	result, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})

	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	require.ErrorContains(t, result.Failed[0].Err, "does not have its url once, so it cannot be moved")
}
