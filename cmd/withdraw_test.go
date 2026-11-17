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
