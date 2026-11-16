//go:build e2e

package importing

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rancher/cargueiro/tests/internal/harness"
)

// git runs git in dir and returns what it printed.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// chartsRepo makes a git repository with one commit.
func chartsRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.name", "test")
	git(t, dir, "config", "user.email", "test@example.com")
	write(t, filepath.Join(dir, "README.md"), "charts")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "start")
	return dir
}

// runScript runs tools/scripts/legacy-import.sh with the binary under test.
func runScript(t *testing.T, source, repoDir, registry string, extra ...string) (string, error) {
	t.Helper()

	script := filepath.Join(harness.RepoRoot(t), "tools", "scripts", "legacy-import.sh")
	args := append([]string{script, "--source", source, "--repo", repoDir, "--registry", registry, "--plain-http", "--cargueiro", harness.Binary(t)}, extra...)
	out, err := exec.Command("bash", args...).CombinedOutput()
	return string(out), err
}

func TestLegacyImportScriptImportsEveryFamilyWithACommitEach(t *testing.T) {
	source := oldSystem(t,
		map[string][]string{
			"fleet":              {"105.0.1+up0.9.1", "110.0.1+up0.16.1", "110.0.2+up0.16.2"},
			"fleet-crd":          {"105.0.1+up0.9.1", "110.0.1+up0.16.1"},
			"rancher-backup":     {"106.0.1+up7.0.1", "110.0.3+up11.0.4"},
			"rancher-backup-crd": {"106.0.1+up7.0.1", "110.0.3+up11.0.4"},
		}, nil)
	repoDir := chartsRepo(t)
	registry := newRegistryPath(t)

	out, err := runScript(t, source, repoDir, registry)

	require.NoError(t, err, out)
	require.Equal(t, "legacy: regenerate the index\nlegacy: import rancher-backup\nlegacy: import fleet\nstart", git(t, repoDir, "log", "--format=%s"), "one commit for each family, then the index")
	body := git(t, repoDir, "log", "-1", "--format=%b", "HEAD~2")
	require.Contains(t, body, "2.10: 2 versions", "the message says the minors, with the CRD chart counted")
	require.Contains(t, body, "2.15: 3 versions")
	require.Equal(t, "", git(t, repoDir, "status", "--porcelain"), "nothing is left uncommitted")
	require.Contains(t, out, "done: 2 families imported")

	fleetFiles := git(t, repoDir, "show", "--format=", "--name-only", "HEAD~2")
	for _, want := range []string{"legacy/fleet/105.0.1+up0.9.1.yaml", "legacy/fleet/110.0.1+up0.16.1.yaml", "legacy/fleet/110.0.2+up0.16.2.yaml", "legacy/fleet-crd/110.0.1+up0.16.1.yaml"} {
		require.Contains(t, fleetFiles, want, "both minors of a family are in its one commit")
	}
	require.Equal(t, "index.yaml", git(t, repoDir, "show", "--format=", "--name-only", "HEAD"))
	require.Equal(t, 9, strings.Count(readFile(t, filepath.Join(repoDir, "index.yaml")), "\n    version: "), "every version is in the index")

	t.Run("audit leaves them out", func(t *testing.T) {
		res := harness.Cargueiro(t, t.TempDir(), "audit", "--repo", repoDir, "--registry", registry, "--plain-http")

		require.Zero(t, res.ExitCode, res.Stdout+res.Stderr)
	})

	t.Run("a second run does nothing", func(t *testing.T) {
		before := git(t, repoDir, "rev-parse", "HEAD")

		out, err := runScript(t, source, repoDir, registry)

		require.NoError(t, err, out)
		require.Equal(t, before, git(t, repoDir, "rev-parse", "HEAD"))
		require.Contains(t, out, "done: 0 families imported")
	})

	t.Run("a new version gets its family a commit", func(t *testing.T) {
		more := oldSystem(t, map[string][]string{
			"fleet":     {"105.0.1+up0.9.1", "110.0.1+up0.16.1", "110.0.2+up0.16.2", "110.0.3+up0.16.3"},
			"fleet-crd": {"105.0.1+up0.9.1", "110.0.1+up0.16.1"},
		}, nil)

		out, err := runScript(t, more, repoDir, registry, "--family", "fleet")

		require.NoError(t, err, out)
		require.Equal(t, "legacy: regenerate the index\nlegacy: import fleet", git(t, repoDir, "log", "--format=%s", "-2"))
	})
}

func TestLegacyImportScriptDryRunChangesNothing(t *testing.T) {
	source := oldSystem(t, map[string][]string{"fleet": {"110.0.1+up0.16.1"}}, nil)
	repoDir := chartsRepo(t)

	out, err := runScript(t, source, repoDir, newRegistryPath(t), "--dry-run")

	require.NoError(t, err, out)
	require.Contains(t, out, "fleet 2.15: 1 versions to import")
	require.Contains(t, out, "1 versions to import")
	require.Equal(t, "start", git(t, repoDir, "log", "--format=%s"))
	entries, err := os.ReadDir(repoDir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "README.md and .git only")
}
