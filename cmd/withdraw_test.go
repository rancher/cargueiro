package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const withdrawEntry = `apiVersion: v2
name: fleet
urls:
- oci://stgregistry.suse.com/rancher/catalog/fleet:111.0.3_up0.17.3@sha256:1111111111111111111111111111111111111111111111111111111111111111
version: 111.0.3+up0.17.3
`

const withdrawIndex = `apiVersion: v1
entries:
  fleet:
  - name: fleet
    version: 111.0.3+up0.17.3
generated: "2026-11-11T17:33:45Z"
`

func runWithdraw(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer
	opts := &rootOptions{
		stderr:    &errOut,
		getenv:    func(string) string { return "" },
		openStore: openerFor(newTestStore(t)),
		prompt:    &fakePrompter{},
		rules:     []byte(upstreamRules),
	}
	root, err := newRootCmd(&out, &errOut, opts, dependencies{})
	require.NoError(t, err)
	root.SetArgs(append([]string{"withdraw"}, args...))

	err = root.Execute()
	return out.String(), err
}

func withdrawRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "entries", "fleet", "111.0.3+up0.17.3.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(withdrawEntry), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.yaml"), []byte(withdrawIndex), 0o600))
	return path
}

func TestWithdrawMarksThePublishedVersion(t *testing.T) {
	path := withdrawRepo(t)

	stdout, err := runWithdraw(t, path)

	require.NoError(t, err)
	require.Equal(t, "withdraw fleet 111.0.3+up0.17.3\n", stdout)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, withdrawEntry+"withdrawn: true\n", string(data))
}

func TestWithdrawDryRunWritesNothing(t *testing.T) {
	path := withdrawRepo(t)

	stdout, err := runWithdraw(t, "--dry-run", path)

	require.NoError(t, err)
	require.Equal(t, "withdraw fleet 111.0.3+up0.17.3\n", stdout)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, withdrawEntry, string(data))
}

func TestWithdrawRefusesWhatIsNotAnEntry(t *testing.T) {
	stdout, err := runWithdraw(t, filepath.Join(t.TempDir(), "legacy", "fleet", "110.0.0.yaml"))

	require.ErrorContains(t, err, "is not an entry")
	require.Empty(t, stdout)
}

func TestWithdrawNeedsOneEntry(t *testing.T) {
	_, err := runWithdraw(t)

	require.ErrorContains(t, err, "accepts 1 arg(s), received 0")
}

func withdrawFamilyRepo(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	index := "apiVersion: v1\nentries:\n"
	for _, name := range []string{"zeta", "zeta-crd"} {
		path := filepath.Join(dir, "entries", name, "111.0.3.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		entry := "apiVersion: v2\nname: " + name + "\nurls:\n- oci://stgregistry.suse.com/rancher/catalog/" + name + ":111.0.3@sha256:1111111111111111111111111111111111111111111111111111111111111111\nversion: 111.0.3\n"
		require.NoError(t, os.WriteFile(path, []byte(entry), 0o600))
		index += "  " + name + ":\n  - name: " + name + "\n    version: 111.0.3\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.yaml"), []byte(index+"generated: \"2026-11-11T17:33:45Z\"\n"), 0o600))
	return dir, filepath.Join(dir, "entries", "zeta-crd", "111.0.3.yaml")
}

func TestWithdrawFamilyWithdrawsEveryChartOfIt(t *testing.T) {
	dir, crd := withdrawFamilyRepo(t)

	stdout, err := runWithdraw(t, "--family", crd)

	require.NoError(t, err)
	require.Equal(t, "withdraw zeta 111.0.3\nwithdraw zeta-crd 111.0.3\n", stdout, "one line for each chart, the main chart first")
	for _, name := range []string{"zeta", "zeta-crd"} {
		data, err := os.ReadFile(filepath.Join(dir, "entries", name, "111.0.3.yaml"))
		require.NoError(t, err)
		require.Contains(t, string(data), "withdrawn: true\n")
	}
}

func TestWithdrawWithoutFamilyWithdrawsOnlyTheEntry(t *testing.T) {
	dir, crd := withdrawFamilyRepo(t)

	stdout, err := runWithdraw(t, crd)

	require.NoError(t, err)
	require.Equal(t, "withdraw zeta-crd 111.0.3\n", stdout)
	data, err := os.ReadFile(filepath.Join(dir, "entries", "zeta", "111.0.3.yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(data), "withdrawn")
}

func TestWithdrawFamilyDryRunWritesNothing(t *testing.T) {
	dir, crd := withdrawFamilyRepo(t)

	stdout, err := runWithdraw(t, "--family", "--dry-run", crd)

	require.NoError(t, err)
	require.Equal(t, "withdraw zeta 111.0.3\nwithdraw zeta-crd 111.0.3\n", stdout)
	data, err := os.ReadFile(filepath.Join(dir, "entries", "zeta", "111.0.3.yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(data), "withdrawn")
}

func TestWithdrawFamilyRefusesWholeWhenOneIsRefused(t *testing.T) {
	dir, crd := withdrawFamilyRepo(t)
	require.NoError(t, os.Remove(filepath.Join(dir, "entries", "zeta", "111.0.3.yaml")))

	stdout, err := runWithdraw(t, "--family", crd)

	require.ErrorContains(t, err, "the family zeta is not withdrawn, and nothing was written")
	require.Empty(t, stdout)
	data, err := os.ReadFile(crd)
	require.NoError(t, err)
	require.NotContains(t, string(data), "withdrawn")
}
