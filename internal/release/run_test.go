package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	repo "helm.sh/helm/v4/pkg/repo/v1"

	"github.com/rancher/cargueiro/internal/oci"
)

// fakeRegistry is the production registry: the manifest digest of each ref it has.
// A ref in copyFails fails to copy, a ref in lost is copied and then not found, and
// a ref in wrong is copied as another manifest. It is also the images: the ones of staging
// and of production by ref, with what makes them fail, and the order of what was done.
type fakeRegistry struct {
	has       map[string]string
	copyFails map[string]error
	lost      map[string]bool
	wrong     map[string]string
	copied    []string

	stagingImages    map[string]string
	images           map[string]string
	invalid          map[string]error
	imageCopyFails   map[string]error
	wrongImage       map[string]string
	stagingImageFail map[string]error
	// signatures is how many signatures staging has for each image, by its ref in production,
	// and signatureFails the images whose signatures cannot be copied.
	signatures     map[string]int
	signatureFails map[string]error
	calls          []string
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		has: map[string]string{}, copyFails: map[string]error{}, lost: map[string]bool{}, wrong: map[string]string{},
		stagingImages: map[string]string{}, images: map[string]string{}, invalid: map[string]error{},
		imageCopyFails: map[string]error{}, wrongImage: map[string]string{}, stagingImageFail: map[string]error{},
		signatures: map[string]int{}, signatureFails: map[string]error{},
	}
}

func (f *fakeRegistry) Inspect(_ context.Context, ref string) (oci.ManifestInfo, error) {
	digest, ok := f.has[ref]
	if !ok {
		return oci.ManifestInfo{}, fmt.Errorf("%s: %w", ref, oci.ErrNotFound)
	}
	return oci.ManifestInfo{ManifestDigest: digest}, nil
}

func (f *fakeRegistry) Copy(_ context.Context, item Item) error {
	ref := item.Destination.String()
	if err := f.copyFails[ref]; err != nil {
		return err
	}
	f.copied = append(f.copied, ref)
	f.calls = append(f.calls, "chart "+ref)
	switch {
	case f.lost[ref]:
	case f.wrong[ref] != "":
		f.has[ref] = f.wrong[ref]
	default:
		f.has[ref] = item.ManifestDigest
	}
	return nil
}

func (f *fakeRegistry) Validate(_ context.Context, item Item) error {
	f.calls = append(f.calls, "validate "+item.Chart)
	return f.invalid[item.Chart+" "+item.Entry.Version]
}

func (f *fakeRegistry) StagingImage(_ context.Context, image oci.ChartRef) (string, error) {
	if err := f.stagingImageFail[image.String()]; err != nil {
		return "", err
	}
	digest, ok := f.stagingImages[image.String()]
	if !ok {
		return "", fmt.Errorf("%s: %w", image, oci.ErrNotFound)
	}
	return digest, nil
}

func (f *fakeRegistry) ProductionImage(_ context.Context, image oci.ChartRef) (string, error) {
	digest, ok := f.images[image.String()]
	if !ok {
		return "", fmt.Errorf("%s: %w", image, oci.ErrNotFound)
	}
	return digest, nil
}

func (f *fakeRegistry) CopyImage(_ context.Context, image Image, digest string) error {
	ref := image.Destination.String()
	if err := f.imageCopyFails[ref]; err != nil {
		return err
	}
	f.calls = append(f.calls, "image "+ref)
	if wrong := f.wrongImage[ref]; wrong != "" {
		f.images[ref] = wrong
		return nil
	}
	f.images[ref] = digest
	return nil
}

func (f *fakeRegistry) CopyImageSignatures(_ context.Context, image Image, _ string) (int, error) {
	ref := image.Destination.String()
	f.calls = append(f.calls, "signatures "+ref)
	if err := f.signatureFails[ref]; err != nil {
		return 0, err
	}
	return f.signatures[ref], nil
}

// stage makes a staging repository with the legacy versions of fleet and a plan for
// it, and returns both and an empty production repository.
func stage(t *testing.T, versions ...string) (Options, Plan) {
	t.Helper()

	staging, production := t.TempDir(), t.TempDir()
	for _, v := range versions {
		tag := strings.ReplaceAll(v, "+", "_")
		writeLegacy(t, staging, "fleet", v, "", legacyURL("fleet", tag, "sha256:"+tag))
	}
	opts := Options{From: staging, Repo: production}
	plan, err := MakePlan(loadRules(t), opts)
	require.NoError(t, err)
	return opts, plan
}

func entryOf(t *testing.T, dir, version string) *repo.ChartVersion {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, "legacy", "fleet", version+".yaml"))
	require.NoError(t, err)
	var entry repo.ChartVersion
	require.NoError(t, yaml.Unmarshal(data, &entry))
	return &entry
}

func TestRunCopiesAndWritesTheEntryLast(t *testing.T) {
	opts, plan := stage(t, "110.0.3+up0.16.3", "110.0.4+up0.16.4")
	reg := newFakeRegistry()
	var report bytes.Buffer

	result, err := Run(context.Background(), opts, plan, reg, &report)

	require.NoError(t, err)
	require.True(t, result.Done())
	require.Len(t, result.Released, 2)
	require.Equal(t, "released fleet 110.0.3+up0.16.3\nreleased fleet 110.0.4+up0.16.4\n", report.String())
	require.Equal(t, "released 2 versions, adopted 0\n", result.Summary())

	entry := entryOf(t, opts.Repo, "110.0.3+up0.16.3")
	require.Equal(t, []string{"oci://registry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3@sha256:110.0.3_up0.16.3"}, entry.URLs, "the entry points to the production registry, with the same manifest")
	require.Equal(t, "abc", entry.Digest, "the rest of the entry is the one of staging")
}

func TestRunSecondRunHasNothingToDo(t *testing.T) {
	opts, plan := stage(t, "110.0.3+up0.16.3")
	reg := newFakeRegistry()
	_, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})
	require.NoError(t, err)

	again, err := MakePlan(loadRules(t), opts)

	require.NoError(t, err)
	require.Empty(t, again.Release, "the entry in production is what says it is released")
}

func TestRunAdoptsWhatWasCopiedByARunThatWasCutOff(t *testing.T) {
	opts, plan := stage(t, "110.0.3+up0.16.3")
	reg := newFakeRegistry()
	reg.has["registry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3"] = "sha256:110.0.3_up0.16.3"

	result, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})

	require.NoError(t, err)
	require.Empty(t, reg.copied, "nothing is copied again")
	require.Len(t, result.Adopted, 1)
	require.Equal(t, "released 0 versions, adopted 1\n", result.Summary())
	require.FileExists(t, filepath.Join(opts.Repo, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
}

func TestRunOneFailureDoesNotStopTheOthers(t *testing.T) {
	opts, plan := stage(t, "110.0.3+up0.16.3", "110.0.4+up0.16.4", "110.0.5+up0.16.5")
	reg := newFakeRegistry()
	reg.copyFails["registry.suse.com/rancher/catalog/fleet:110.0.4_up0.16.4"] = errors.New("the registry is down")
	var report bytes.Buffer

	result, err := Run(context.Background(), opts, plan, reg, &report)

	require.NoError(t, err)
	require.False(t, result.Done())
	require.Len(t, result.Released, 2, "the versions after the failure are released")
	require.Len(t, result.Failed, 1)
	require.Contains(t, report.String(), "failed fleet 110.0.4+up0.16.4: copy registry.suse.com/rancher/catalog/fleet:110.0.4_up0.16.4: the registry is down")
	require.FileExists(t, filepath.Join(opts.Repo, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
	require.NoFileExists(t, filepath.Join(opts.Repo, "legacy", "fleet", "110.0.4+up0.16.4.yaml"), "a version that failed has no entry")
	require.FileExists(t, filepath.Join(opts.Repo, "legacy", "fleet", "110.0.5+up0.16.5.yaml"))
	require.Contains(t, result.Summary(), "failed 1, in conflict 0")
	require.Contains(t, result.Summary(), "FAILED: 1 versions were not released. Run it again: what was done is kept.")

	t.Run("the next run finishes it", func(t *testing.T) {
		again, err := MakePlan(loadRules(t), opts)
		require.NoError(t, err)
		require.Equal(t, []string{"fleet 110.0.4+up0.16.4"}, itemNames(again))
		delete(reg.copyFails, "registry.suse.com/rancher/catalog/fleet:110.0.4_up0.16.4")

		result, err := Run(context.Background(), opts, again, reg, &bytes.Buffer{})

		require.NoError(t, err)
		require.True(t, result.Done())
	})
}

func TestRunAConflictIsLeftAsItIsAndReportedLoud(t *testing.T) {
	opts, plan := stage(t, "110.0.3+up0.16.3", "110.0.4+up0.16.4")
	reg := newFakeRegistry()
	reg.has["registry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3"] = "sha256:other"

	result, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})

	require.NoError(t, err)
	require.Len(t, result.Conflicts, 1)
	require.Len(t, result.Released, 1, "the other version goes on")
	require.Equal(t, "sha256:other", reg.has["registry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3"], "the tag is not touched")
	require.NoFileExists(t, filepath.Join(opts.Repo, "legacy", "fleet", "110.0.3+up0.16.3.yaml"))
	summary := result.Summary()
	require.Contains(t, summary, "CONFLICT: 1 versions are in the production registry with other content than staging's, and were not touched.")
	require.Contains(t, summary, "fleet 110.0.3+up0.16.3: registry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3 has the manifest sha256:other, and staging has sha256:110.0.3_up0.16.3")
}

func TestRunChecksWhatWasCopied(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(reg *fakeRegistry, ref string)
		wantErr string
	}{
		{"it is not there after the copy", func(reg *fakeRegistry, ref string) { reg.lost[ref] = true }, "read registry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3 after it was copied"},
		{"it is another manifest", func(reg *fakeRegistry, ref string) { reg.wrong[ref] = "sha256:bad" }, "has the manifest sha256:bad after the copy, and staging has sha256:110.0.3_up0.16.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, plan := stage(t, "110.0.3+up0.16.3")
			reg := newFakeRegistry()
			tt.arrange(reg, "registry.suse.com/rancher/catalog/fleet:110.0.3_up0.16.3")

			result, err := Run(context.Background(), opts, plan, reg, &bytes.Buffer{})

			require.NoError(t, err)
			require.Len(t, result.Failed, 1)
			require.ErrorContains(t, result.Failed[0].Err, tt.wantErr)
			require.NoFileExists(t, filepath.Join(opts.Repo, "legacy", "fleet", "110.0.3+up0.16.3.yaml"), "no entry for a copy that was not checked")
		})
	}
}

func TestRunCopiesTheLogosFirstAndNeverReplacesOne(t *testing.T) {
	staging, production := t.TempDir(), t.TempDir()
	writeLegacy(t, staging, "fleet", "110.0.3+up0.16.3", "file://assets/logos/fleet.svg", legacyURL("fleet", "t", "sha256:a"))
	writeFile(t, filepath.Join(staging, "assets", "logos", "fleet.svg"), "fleet logo")
	opts := Options{From: staging, Repo: production}
	plan, err := MakePlan(loadRules(t), opts)
	require.NoError(t, err)
	var report bytes.Buffer

	_, err = Run(context.Background(), opts, plan, newFakeRegistry(), &report)

	require.NoError(t, err)
	require.True(t, strings.HasPrefix(report.String(), "copied logo fleet.svg\n"))
	data, err := os.ReadFile(filepath.Join(production, "assets", "logos", "fleet.svg"))
	require.NoError(t, err)
	require.Equal(t, "fleet logo", string(data))

	writeFile(t, filepath.Join(production, "assets", "logos", "fleet.svg"), "another logo")
	err = copyLogo(production, plan.Logos[0])
	require.Error(t, err, "a logo that is there is never replaced")
}

func TestRunStopsWhenTheContextIsDone(t *testing.T) {
	opts, plan := stage(t, "110.0.3+up0.16.3")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Run(ctx, opts, plan, newFakeRegistry(), &bytes.Buffer{})

	require.ErrorIs(t, err, context.Canceled)
}
