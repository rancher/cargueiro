#!/usr/bin/env bash
# Tests legacy-import.sh with a fake cargueiro, a rules.yaml of a few families and a
# real git repository, all throwaway.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/legacy-import.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failed=0
fail() {
	echo "FAIL $1" >&2
	failed=1
}
expect() {
	local name=$1 want=$2 got=$3
	[ "$want" = "$got" ] || fail "$name: want '$want', got '$got'"
}
expect_contains() {
	local name=$1 text=$2 got=$3
	case $got in *"$text"*) ;; *) fail "$name: '$text' is not in '$got'" ;; esac
}

# rules.yaml: two families of rancher-charts, one of prime-charts that is not
# imported, minors written out of order, and the ones of the new system.
cat >"$work/rules.yaml" <<'YAML'
# comment: edition: prime-charts, which is not a family
kinds:
  main: {}
families:
  - family: fleet
    charts:
      - { name: fleet, kind: main, edition: rancher-charts }
      - { name: fleet-crd, kind: crd, edition: rancher-charts }
  - family: harvester-cloud-provider
    deprecated: true
    charts:
      - { name: harvester-cloud-provider, kind: main, edition: rancher-charts }
  - family: rancher-ai-agent
    charts:
      - { name: rancher-ai-agent, kind: main, edition: prime-charts }
registries:
  rancher-charts:
    charts-staging: stgregistry.suse.com/rancher/catalog
versions:
  prereleases: [rc]
  majors:
    "2.15": 110
    "2.9": 104
    "2.10": 105
    "2.16": 111
    "2.17": 112
YAML

# The fake cargueiro. $FAKE_PLAN lists "family minor chart version" for what the old
# system has. It writes legacy/<chart>/<version>.yaml like the real one, and records
# its calls in $FAKE_CALLS. FAKE_FAIL="family minor" makes that import fail after it
# did its first version. FAKE_STUCK=1 makes it report versions it does not write.
mkdir "$work/bin"
cat >"$work/bin/cargueiro" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
echo "$*" >>"$FAKE_CALLS"
cmd=$1
shift
repo="" family="" minor="" dry=0
while [ $# -gt 0 ]; do
	case $1 in
	--repo) repo=$2; shift 2 ;;
	--family) family=$2; shift 2 ;;
	--minor) minor=$2; shift 2 ;;
	--dry-run) dry=1; shift ;;
	*) shift ;;
	esac
done
if [ "$cmd" = index ]; then
	find "$repo/legacy" -name '*.yaml' 2>/dev/null | sort >"$repo/index.yaml" || true
	echo "add legacy"
	exit 0
fi
pending=()
while read -r f m chart version; do
	[ "$f" = "$family" ] && [ "$m" = "$minor" ] || continue
	[ ! -e "$repo/legacy/$chart/$version.yaml" ] || continue
	pending+=("$chart $version")
done <"$FAKE_PLAN"
if [ ${#pending[@]} -eq 0 ]; then
	echo "nothing to import"
	exit 0
fi
first=1
for item in "${pending[@]}"; do
	read -r chart version <<<"$item"
	if [ "$dry" -eq 1 ]; then
		echo "import $chart $version"
		continue
	fi
	if [ "$first" -eq 0 ] && [ "${FAKE_FAIL:-}" = "$family $minor" ]; then
		echo "error: the registry is down" >&2
		exit 1
	fi
	first=0
	if [ "${FAKE_STUCK:-}" != 1 ]; then
		mkdir -p "$repo/legacy/$chart"
		echo "version: $version" >"$repo/legacy/$chart/$version.yaml"
	fi
	echo "pushed $chart $version"
done
FAKE
chmod +x "$work/bin/cargueiro"

# new_repo makes a git repository with one commit.
new_repo() {
	local dir=$1
	mkdir -p "$dir"
	git -C "$dir" init -q
	git -C "$dir" config user.name test
	git -C "$dir" config user.email test@example.com
	echo hello >"$dir/README.md"
	git -C "$dir" add -A
	git -C "$dir" commit -q -m "start"
}

source_dir="$work/source"
mkdir "$source_dir"
echo "apiVersion: v1" >"$source_dir/index.yaml"

export FAKE_CALLS="$work/calls"
export FAKE_PLAN="$work/plan"
run() { # run <repo> [options]: leaves the output in $out and the status in $status
	local repo=$1
	shift
	status=0
	out=$("$script" --source "$source_dir" --repo "$repo" --rules "$work/rules.yaml" --cargueiro "$work/bin/cargueiro" "$@" 2>&1) || status=$?
}
log() { git -C "$1" log --format=%s | paste -sd'|' -; }

cat >"$FAKE_PLAN" <<'PLAN'
fleet 2.10 fleet 105.0.1
fleet 2.10 fleet-crd 105.0.1
fleet 2.15 fleet 110.0.1
fleet 2.15 fleet-crd 110.0.1
harvester-cloud-provider 2.9 harvester-cloud-provider 104.0.1
rancher-ai-agent 2.15 rancher-ai-agent 110.0.1
PLAN

# --- a dry run: the families of rancher-charts and the minors of the old system, in order
repo="$work/dry"
new_repo "$repo"
: >"$FAKE_CALLS"
run "$repo" --dry-run
expect "dry run exit" 0 "$status"
expect_contains "dry run counts fleet 2.10" "fleet 2.10: 2 versions to import" "$out"
expect_contains "dry run counts fleet 2.15" "fleet 2.15: 2 versions to import" "$out"
expect_contains "dry run counts harvester" "harvester-cloud-provider 2.9: 1 versions to import" "$out"
expect_contains "dry run total" "5 versions to import" "$out"
case $out in *rancher-ai-agent*) fail "a prime family is not imported" ;; esac
expect "dry run commits nothing" "start" "$(log "$repo")"
case $(cat "$FAKE_CALLS") in *index*) fail "a dry run does not run index" ;; esac
minors_seen=$(grep -o ' --minor [0-9.]*' "$FAKE_CALLS" | awk '{print $2}' | head -3 | paste -sd' ' -)
expect "the minors, oldest first, and none of the new system" "2.9 2.10 2.15" "$minors_seen"

# --- an import: one commit for each family, and the index at the end
repo="$work/import"
new_repo "$repo"
: >"$FAKE_CALLS"
run "$repo"
expect "import exit" 0 "$status"
expect "commits" "legacy: regenerate the index|legacy: import harvester-cloud-provider|legacy: import fleet|start" "$(log "$repo")"
expect "a family has all its minors in one commit" "2 105.0.1|2 110.0.1" "$(git -C "$repo" show --format= --name-only HEAD~2 | sed -E 's|.*/([0-9.]+)\.yaml|\1|' | sort | uniq -c | sed -E 's/^ +//; s/^([0-9]+) (.*)$/\1 \2/' | paste -sd'|' -)"
expect_contains "the message says the minors" "2.10: 2 versions" "$(git -C "$repo" log -1 --format=%b HEAD~2)"
expect_contains "and the other minor" "2.15: 2 versions" "$(git -C "$repo" log -1 --format=%b HEAD~2)"
expect "the index commit has only index.yaml" "index.yaml" "$(git -C "$repo" show --format= --name-only HEAD)"
expect "nothing is left uncommitted" "" "$(git -C "$repo" status --porcelain)"
expect_contains "the summary" "done: 2 families imported" "$out"

# --- a second run does nothing
before=$(git -C "$repo" rev-parse HEAD)
run "$repo"
expect "second run exit" 0 "$status"
expect "second run makes no commit" "$before" "$(git -C "$repo" rev-parse HEAD)"
expect_contains "second run summary" "done: 0 families imported" "$out"

# --- a new version: only its family gets a commit
echo "fleet 2.15 fleet 110.0.2" >>"$FAKE_PLAN"
run "$repo"
expect "new version exit" 0 "$status"
expect "only fleet and the index" "legacy: regenerate the index|legacy: import fleet|legacy: regenerate the index" "$(git -C "$repo" log --format=%s -3 | paste -sd'|' -)"

# --- a failure: what was done is kept, nothing is committed for the family it was in,
# and the next run finishes
# harvester has two versions in 2.9, so the failure comes after the first one is done
echo "harvester-cloud-provider 2.9 harvester-cloud-provider 104.0.2" >>"$FAKE_PLAN"
repo="$work/failure"
new_repo "$repo"
: >"$FAKE_CALLS"
FAKE_FAIL="harvester-cloud-provider 2.9" run "$repo"
expect "a failure fails the script" 1 "$status"
expect_contains "it says where" "the import of harvester-cloud-provider 2.9 failed" "$out"
expect "the family before it was committed" "legacy: import fleet|start" "$(log "$repo")"
expect_contains "the file that was done is kept, uncommitted" "legacy/harvester-cloud-provider" "$(git -C "$repo" status --porcelain)"
run "$repo"
expect "the next run exit" 0 "$status"
expect "and finishes the family" "legacy: regenerate the index|legacy: import harvester-cloud-provider|legacy: import fleet|start" "$(log "$repo")"
expect "nothing is left uncommitted" "" "$(git -C "$repo" status --porcelain)"

# --- changes outside the work paths are refused, before anything is run
repo="$work/dirty"
new_repo "$repo"
echo changed >>"$repo/README.md"
: >"$FAKE_CALLS"
run "$repo"
expect "dirty exit" 1 "$status"
expect_contains "dirty says what" "README.md" "$out"
expect "nothing was run" 0 "$(wc -l <"$FAKE_CALLS" | tr -d ' ')"

# --- one family, and no index
repo="$work/family"
new_repo "$repo"
run "$repo" --family fleet --no-index
expect "family exit" 0 "$status"
expect "only that family, no index" "legacy: import fleet|start" "$(log "$repo")"
[ ! -e "$repo/index.yaml" ] || fail "--no-index writes no index"

# --- something that is still to import after the run is an error
repo="$work/stuck"
new_repo "$repo"
FAKE_STUCK=1 run "$repo" --family fleet --no-index
expect "stuck exit" 1 "$status"
expect_contains "stuck says what" "left to import: fleet 2.10" "$out"

# --- the old system is cloned when --source is not given, and kept up to date
# upstream stands for rancher/charts: the branch has index.yaml and assets/ and a big
# file that is not read, and another branch the script must not take
upstream="$work/upstream"
new_repo "$upstream"
git -C "$upstream" checkout -q -b release-v2.15
echo "apiVersion: v1" >"$upstream/index.yaml"
mkdir -p "$upstream/assets" "$upstream/packages"
echo archive >"$upstream/assets/a.tgz"
echo not-read >"$upstream/packages/big"
git -C "$upstream" add -A
git -C "$upstream" commit -q -m "release 1"
git -C "$upstream" checkout -q -b other
echo other >"$upstream/index.yaml"
git -C "$upstream" commit -q -am "other branch"
git -C "$upstream" checkout -q release-v2.15
cache="$work/cache"
repo="$work/cloned"
new_repo "$repo"
status=0
out=$("$script" --source-url "$upstream" --source-cache "$cache" --repo "$repo" --rules "$work/rules.yaml" --cargueiro "$work/bin/cargueiro" --dry-run 2>&1) || status=$?
expect "clone exit" 0 "$status"
expect_contains "it says the commit it read" "source: " "$out"
expect "it has the index and the assets" "apiVersion: v1 archive" "$(cat "$cache/index.yaml" "$cache/assets/a.tgz" | paste -sd' ' -)"
[ ! -e "$cache/packages" ] || fail "only index.yaml and assets/ are checked out"
expect "the branch is release-v2.15" "apiVersion: v1" "$(cat "$cache/index.yaml")"

# a new commit upstream is the one the next run reads
echo "apiVersion: v2" >"$upstream/index.yaml"
git -C "$upstream" commit -q -am "release 2"
status=0
"$script" --source-url "$upstream" --source-cache "$cache" --repo "$repo" --rules "$work/rules.yaml" --cargueiro "$work/bin/cargueiro" --dry-run >/dev/null 2>&1 || status=$?
expect "update exit" 0 "$status"
expect "the latest commit is read" "apiVersion: v2" "$(cat "$cache/index.yaml")"

# the branch is the one given
status=0
"$script" --source-url "$upstream" --source-branch other --source-cache "$work/cache-other" --repo "$repo" --rules "$work/rules.yaml" --cargueiro "$work/bin/cargueiro" --dry-run >/dev/null 2>&1 || status=$?
expect "other branch exit" 0 "$status"
expect "--source-branch is used" "other" "$(cat "$work/cache-other/index.yaml")"

# without --source-cache a workflow has RUNNER_TEMP, and a laptop has the cache folder
status=0
RUNNER_TEMP="$work/runner" "$script" --source-url "$upstream" --repo "$repo" --rules "$work/rules.yaml" --cargueiro "$work/bin/cargueiro" --dry-run >/dev/null 2>&1 || status=$?
expect "RUNNER_TEMP exit" 0 "$status"
[ -f "$work/runner/legacy-source/index.yaml" ] || fail "the clone is kept in RUNNER_TEMP"
status=0
env -u RUNNER_TEMP XDG_CACHE_HOME="$work/xdg" "$script" --source-url "$upstream" --repo "$repo" --rules "$work/rules.yaml" --cargueiro "$work/bin/cargueiro" --dry-run >/dev/null 2>&1 || status=$?
expect "XDG_CACHE_HOME exit" 0 "$status"
[ -f "$work/xdg/cargueiro/legacy-source/index.yaml" ] || fail "the clone is kept in the cache folder"

# --source is used as it is: nothing is cloned
status=0
out=$("$script" --source "$source_dir" --source-url "$work/nowhere" --repo "$repo" --rules "$work/rules.yaml" --cargueiro "$work/bin/cargueiro" --dry-run 2>&1) || status=$?
expect "--source exit" 0 "$status"
case $out in *fetching*) fail "--source does not clone" ;; esac

# --- arguments
status=0
out=$("$script" --source-url "$work/nowhere" --source-cache "$work/cache-bad" --repo "$repo" --rules "$work/rules.yaml" --cargueiro "$work/bin/cargueiro" 2>&1) || status=$?
expect "a source that cannot be cloned fails" 1 "$status"
expect_contains "and says which" "cannot fetch release-v2.15 of $work/nowhere" "$out"
status=0
"$script" --source "$work/nowhere" >/dev/null 2>&1 || status=$?
expect "a source without an index fails" 1 "$status"
status=0
"$script" --source "$source_dir" --bogus >/dev/null 2>&1 || status=$?
expect "an unknown option fails" 1 "$status"

exit "$failed"
