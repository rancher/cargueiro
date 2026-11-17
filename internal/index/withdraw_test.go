package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const publishedFleet = `apiVersion: v1
entries:
  fleet:
  - name: fleet
    version: 111.0.3+up0.17.3
generated: "2026-11-11T17:33:45Z"
`

// publishedRepo is a repository with one published version of fleet, and returns the path of
// its entry.
func publishedRepo(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	version := "111.0.3+up0.17.3"
	writeFiles(t, dir, map[string]string{
		"entries/fleet/" + version + ".yaml": entryText(version, pinnedOne),
		"index.yaml":                         publishedFleet,
	})
	return dir, filepath.Join(dir, "entries", "fleet", version+".yaml")
}

func readText(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestWithdraw(t *testing.T) {
	dir, path := publishedRepo(t)
	before := readText(t, path)
	index := readText(t, filepath.Join(dir, "index.yaml"))
	info, err := os.Stat(path)
	require.NoError(t, err)

	ref, err := Withdraw(path, false)

	require.NoError(t, err)
	require.Equal(t, Ref{Chart: "fleet", Version: "111.0.3+up0.17.3"}, ref)
	require.Equal(t, before+"withdrawn: true\n", readText(t, path), "the entry only got the mark")
	require.Equal(t, index, readText(t, filepath.Join(dir, "index.yaml")), "the index is the job of index")
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, info.Mode(), after.Mode())

	t.Run("a second time", func(t *testing.T) {
		_, err := Withdraw(path, false)

		require.ErrorContains(t, err, "fleet 111.0.3+up0.17.3 is withdrawn already")
		require.Equal(t, before+"withdrawn: true\n", readText(t, path))
	})
}

func TestWithdrawAnEntryWithoutANewlineAtTheEnd(t *testing.T) {
	_, path := publishedRepo(t)
	text := readText(t, path)
	require.NoError(t, os.WriteFile(path, []byte(text[:len(text)-1]), 0o600))

	_, err := Withdraw(path, false)

	require.NoError(t, err)
	require.Equal(t, text+"withdrawn: true\n", readText(t, path))
}

func TestWithdrawDryRunWritesNothing(t *testing.T) {
	_, path := publishedRepo(t)
	before := readText(t, path)

	ref, err := Withdraw(path, true)

	require.NoError(t, err)
	require.Equal(t, "fleet", ref.Chart)
	require.Equal(t, before, readText(t, path))
}

func TestWithdrawRefuses(t *testing.T) {
	tests := []struct {
		name    string
		path    func(t *testing.T, dir, path string) string
		wantErr string
	}{
		{"a path that is not an entry", func(_ *testing.T, dir, _ string) string {
			return filepath.Join(dir, "index.yaml")
		}, "is not an entry"},
		{"a legacy entry", func(t *testing.T, dir, _ string) string {
			path := filepath.Join(dir, "legacy", "fleet", "111.0.3+up0.17.3.yaml")
			writeFiles(t, dir, map[string]string{"legacy/fleet/111.0.3+up0.17.3.yaml": entryText("111.0.3+up0.17.3", pinnedOne)})
			return path
		}, "is not an entry"},
		{"an entry that is not there", func(_ *testing.T, dir, _ string) string {
			return filepath.Join(dir, "entries", "fleet", "111.0.9.yaml")
		}, "read entry"},
		{"an entry of another chart than its folder", func(t *testing.T, dir, _ string) string {
			writeFiles(t, dir, map[string]string{"entries/other/111.0.3+up0.17.3.yaml": entryText("111.0.3+up0.17.3", pinnedOne)})
			return filepath.Join(dir, "entries", "other", "111.0.3+up0.17.3.yaml")
		}, `is of chart "fleet"`},
		{"an entry that holds another version than its file", func(t *testing.T, dir, _ string) string {
			writeFiles(t, dir, map[string]string{"entries/fleet/111.0.4.yaml": entryText("111.0.3+up0.17.3", pinnedOne)})
			return filepath.Join(dir, "entries", "fleet", "111.0.4.yaml")
		}, "so the file must be called"},
		{"a version that is not published", func(t *testing.T, dir, _ string) string {
			writeFiles(t, dir, map[string]string{"entries/fleet/111.0.4+up0.17.4.yaml": entryText("111.0.4+up0.17.4", pinnedTwo)})
			return filepath.Join(dir, "entries", "fleet", "111.0.4+up0.17.4.yaml")
		}, "is not published"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, path := publishedRepo(t)
			target := tt.path(t, dir, path)

			_, err := Withdraw(target, false)

			require.ErrorContains(t, err, tt.wantErr)
			require.NotContains(t, readText(t, path), "withdrawn", "nothing is written")
		})
	}

	t.Run("a repository without an index", func(t *testing.T) {
		dir, path := publishedRepo(t)
		require.NoError(t, os.Remove(filepath.Join(dir, "index.yaml")))

		_, err := Withdraw(path, false)

		require.ErrorContains(t, err, "is not published")
	})

	t.Run("an entry with a field the index does not know", func(t *testing.T) {
		_, path := publishedRepo(t)
		require.NoError(t, os.WriteFile(path, []byte(readText(t, path)+"hide: true\n"), 0o600))

		_, err := Withdraw(path, false)

		require.ErrorContains(t, err, `unknown field "hide"`)
	})
}

const familyVersion = "111.0.3+up0.17.3"

// familyEntry is the entry of a chart of the fleet family at familyVersion.
func familyEntry(chart string) string {
	text := entryText(familyVersion, pinnedOne)
	text = strings.Replace(text, "name: fleet\n", "name: "+chart+"\n", 1)
	return strings.Replace(text, "/fleet:", "/"+chart+":", 1)
}

// familyRepo is a repository where the three charts of the fleet family have a published
// version, and returns the path of the entry of chart.
func familyRepo(t *testing.T, chart string) (string, func(chart string) string) {
	t.Helper()

	dir := t.TempDir()
	index := "apiVersion: v1\nentries:\n"
	files := map[string]string{}
	for _, name := range []string{"fleet", "fleet-agent", "fleet-crd"} {
		files["entries/"+name+"/"+familyVersion+".yaml"] = familyEntry(name)
		index += "  " + name + ":\n  - name: " + name + "\n    version: " + familyVersion + "\n"
	}
	files["index.yaml"] = index + "generated: \"2026-11-11T17:33:45Z\"\n"
	writeFiles(t, dir, files)
	entry := func(name string) string { return filepath.Join(dir, "entries", name, familyVersion+".yaml") }
	return entry(chart), entry
}

func TestWithdrawFamily(t *testing.T) {
	path, entry := familyRepo(t, "fleet-crd")

	refs, err := WithdrawFamily(loadRules(t), path, false)

	require.NoError(t, err)
	require.Equal(t, []Ref{
		{Chart: "fleet", Version: familyVersion},
		{Chart: "fleet-agent", Version: familyVersion},
		{Chart: "fleet-crd", Version: familyVersion},
	}, refs, "the main chart is first, whichever entry was given")
	for _, name := range []string{"fleet", "fleet-agent", "fleet-crd"} {
		require.Equal(t, familyEntry(name)+"withdrawn: true\n", readText(t, entry(name)))
	}
}

func TestWithdrawFamilyDryRunWritesNothing(t *testing.T) {
	path, entry := familyRepo(t, "fleet")

	refs, err := WithdrawFamily(loadRules(t), path, true)

	require.NoError(t, err)
	require.Len(t, refs, 3)
	for _, name := range []string{"fleet", "fleet-agent", "fleet-crd"} {
		require.Equal(t, familyEntry(name), readText(t, entry(name)))
	}
}

func TestWithdrawFamilyWritesNothingWhenOneIsRefused(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(t *testing.T, entry func(string) string)
		wantErr string
	}{
		{"a sibling with no entry", func(t *testing.T, entry func(string) string) {
			require.NoError(t, os.Remove(entry("fleet-agent")))
		}, "fleet-agent"},
		{"a sibling that is withdrawn already", func(t *testing.T, entry func(string) string) {
			require.NoError(t, os.WriteFile(entry("fleet-crd"), []byte(familyEntry("fleet-crd")+"withdrawn: true\n"), 0o600))
		}, "fleet-crd " + familyVersion + " is withdrawn already"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, entry := familyRepo(t, "fleet")
			tt.mutate(t, entry)

			_, err := WithdrawFamily(loadRules(t), path, false)

			require.ErrorContains(t, err, "the family fleet is not withdrawn, and nothing was written")
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, familyEntry("fleet"), readText(t, entry("fleet")), "the others are not written either")
		})
	}

	t.Run("a sibling that is not published", func(t *testing.T) {
		path, entry := familyRepo(t, "fleet")
		dir := filepath.Dir(filepath.Dir(filepath.Dir(path)))
		index := readText(t, filepath.Join(dir, "index.yaml"))
		index = strings.Replace(index, "  fleet-crd:\n  - name: fleet-crd\n    version: "+familyVersion+"\n", "", 1)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "index.yaml"), []byte(index), 0o600))

		_, err := WithdrawFamily(loadRules(t), path, false)

		require.ErrorContains(t, err, "fleet-crd "+familyVersion+" is not published")
		require.Equal(t, familyEntry("fleet"), readText(t, entry("fleet")))
	})
}

func TestWithdrawFamilyRefuses(t *testing.T) {
	t.Run("a chart that is not in the rules", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "entries", "nope", "1.0.0.yaml")

		_, err := WithdrawFamily(loadRules(t), path, false)

		require.ErrorContains(t, err, `chart "nope" is not listed in the rules file`)
	})

	t.Run("a path that is not an entry", func(t *testing.T) {
		_, err := WithdrawFamily(loadRules(t), filepath.Join(t.TempDir(), "index.yaml"), false)

		require.ErrorContains(t, err, "is not an entry")
	})
}
