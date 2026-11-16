#!/usr/bin/env bash
# legacy-import.sh imports the versions of the old system into a charts
# repository, one commit for each family, and then regenerates the index.
#
#   legacy-import.sh [--repo <charts repository>] [--source <release-v2.15 checkout>]
#
# The old system is read from a checkout of release-v2.15 of rancher/charts: its
# index.yaml and assets/. Without --source the script clones that branch itself, with
# the latest commit of it at the time, into a cache folder that the next run updates,
# so the same command runs on a laptop and in a workflow.
#
# For each family of rules.yaml that is released from rancher-charts, and for each
# Rancher minor of the old system in it (2.10 to 2.15), it runs `cargueiro import`,
# which pushes what the registry does not have and writes legacy/<chart>/<version>.yaml.
# When a family got anything, it commits legacy/ and assets/logos/ as
# "legacy: import <family>". After the last family it runs `cargueiro index`, which
# needs no registry for legacy versions, and commits index.yaml. Then it checks that
# nothing is left to import.
#
# It can be run again at any point, after a failure or a month later: what is done is
# not done twice, a tag that the registry has is adopted, and only a family that has
# something new gets a commit. It stops at the first failure and leaves the files of
# the family it was in, uncommitted, for the next run to finish.
#
# Options:
#   --source DIR      a checkout of the old system that is already there: its index.yaml
#                     and assets/. Nothing is cloned or updated
#   --source-url URL  where to clone the old system from (default:
#                     https://github.com/rancher/charts.git)
#   --source-branch B the branch of it (default: release-v2.15)
#   --source-cache DIR where the clone is kept (default: legacy-source in $RUNNER_TEMP,
#                     which a workflow has, or else in $XDG_CACHE_HOME/cargueiro, or
#                     ~/.cache/cargueiro)
#   --repo DIR        the charts repository to import into, a git checkout (default: .)
#   --registry PATH   host[:port]/path the archives are pushed to, instead of the staging
#                     registry of each chart
#   --plain-http      talk HTTP to the registry, for local registries
#   --rules FILE      the rules.yaml to read the families and the minors from (default:
#                     the one of the repository this script is in)
#   --family NAME     import only this family; can be given more than once
#   --cargueiro BIN   the cargueiro to run (default: cargueiro)
#   --dry-run         report what would be imported and change nothing
#   --no-index        do not regenerate and commit index.yaml
#
# The registry credentials are the ones cargueiro reads: CARGUEIRO_USERNAME and
# CARGUEIRO_PASSWORD, or a saved login. The commits are made with the git identity
# that is configured, which a workflow sets.
set -euo pipefail

# The first Rancher minor that the new system makes charts for. The minors before it
# are the old system. It is MinRancherMinor in internal/version/rancher.go.
NEW_SYSTEM_FROM=16

source_dir=""
source_url="https://github.com/rancher/charts.git"
source_branch="release-v2.15"
source_cache=""
repo_dir="."
registry=""
plain_http=0
rules=""
cargueiro="cargueiro"
dry_run=0
index=1
families=()

die() {
	echo "legacy-import: $1" >&2
	exit 1
}

usage() {
	sed -n '2,/^set -euo/p' "$0" | sed -e '$d' -e 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
	case $1 in
	--source) source_dir=${2:?--source needs a directory}; shift 2 ;;
	--source-url) source_url=${2:?--source-url needs a URL}; shift 2 ;;
	--source-branch) source_branch=${2:?--source-branch needs a branch}; shift 2 ;;
	--source-cache) source_cache=${2:?--source-cache needs a directory}; shift 2 ;;
	--repo) repo_dir=${2:?--repo needs a directory}; shift 2 ;;
	--registry) registry=${2:?--registry needs a path}; shift 2 ;;
	--plain-http) plain_http=1; shift ;;
	--rules) rules=${2:?--rules needs a file}; shift 2 ;;
	--family) families+=("${2:?--family needs a name}"); shift 2 ;;
	--cargueiro) cargueiro=${2:?--cargueiro needs a binary}; shift 2 ;;
	--dry-run) dry_run=1; shift ;;
	--no-index) index=0; shift ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown option $1" ;;
	esac
done

command -v "$cargueiro" >/dev/null 2>&1 || [ -x "$cargueiro" ] || die "cargueiro is not installed: $cargueiro"
if [ -z "$rules" ]; then
	rules="$(cd "$(dirname "$0")/../.." && pwd)/rules.yaml"
fi
[ -f "$rules" ] || die "there is no rules file at $rules: give it with --rules"
git -C "$repo_dir" rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "$repo_dir is not a git repository"

# fetch_source clones the old system into the cache, or brings the clone that is there
# to the latest commit of the branch, and sets source_dir. It needs only index.yaml and
# assets/, so the rest of the tree is not checked out.
fetch_source() {
	local base
	if [ -z "$source_cache" ]; then
		if [ -n "${RUNNER_TEMP:-}" ]; then
			base=$RUNNER_TEMP
		else
			base=${XDG_CACHE_HOME:-$HOME/.cache}/cargueiro
		fi
		source_cache=$base/legacy-source
	fi
	if [ ! -d "$source_cache/.git" ]; then
		rm -rf "$source_cache"
		mkdir -p "$source_cache"
		git -C "$source_cache" init -q
		git -C "$source_cache" remote add origin "$source_url"
		git -C "$source_cache" sparse-checkout set --no-cone /index.yaml /assets
	fi
	git -C "$source_cache" remote set-url origin "$source_url"
	echo "fetching $source_branch of $source_url"
	git -C "$source_cache" fetch -q --depth 1 --no-tags origin "$source_branch" || die "cannot fetch $source_branch of $source_url"
	git -C "$source_cache" checkout -q --force FETCH_HEAD || die "cannot check out $source_branch of $source_url"
	echo "source: $(git -C "$source_cache" rev-parse --short HEAD) $(git -C "$source_cache" log -1 --format=%s | cut -c1-70)"
	source_dir=$source_cache
}

if [ -z "$source_dir" ]; then
	fetch_source
fi
[ -f "$source_dir/index.yaml" ] || die "there is no index.yaml in $source_dir"

# list_families prints the families of rules.yaml that are released from the
# rancher-charts edition. The edition of a family is the one of its first chart.
list_families() {
	awk '
		/^  - family:/ { name = $3; seen = 0; next }
		name != "" && !seen && /edition:/ {
			seen = 1
			if ($0 ~ /edition: *rancher-charts/) print name
		}
	' "$rules"
}

# list_minors prints the Rancher minors of rules.yaml that are of the old system,
# oldest first.
list_minors() {
	awk -v first="$NEW_SYSTEM_FROM" '
		/^  majors:/ { in_majors = 1; next }
		in_majors && /^    "2\.[0-9]+":/ {
			minor = $1; gsub(/[":]/, "", minor)
			split(minor, parts, ".")
			if (parts[2] + 0 < first) print parts[2] + 0, minor
			next
		}
		in_majors { in_majors = 0 }
	' "$rules" | sort -n | cut -d' ' -f2
}

if [ ${#families[@]} -eq 0 ]; then
	mapfile -t families < <(list_families)
fi
mapfile -t minors < <(list_minors)
[ ${#families[@]} -gt 0 ] || die "there is no family released from rancher-charts in $rules"
[ ${#minors[@]} -gt 0 ] || die "there is no Rancher minor of the old system in $rules"

# The work is only done in these paths, so that a commit has nothing but imported
# versions. A run that was cut off leaves its files here, uncommitted, and the next
# one finishes them.
work_paths=(legacy assets/logos index.yaml)

if [ "$dry_run" -eq 0 ]; then
	pathspec=(.)
	for path in "${work_paths[@]}"; do pathspec+=(":(exclude)$path"); done
	other=$(git -C "$repo_dir" status --porcelain --untracked-files=all -- "${pathspec[@]}")
	[ -z "$other" ] || die "the charts repository has changes outside ${work_paths[*]}, and a commit must have only imported versions:
$other"
fi

import_args=(--source "$source_dir" --repo "$repo_dir")
[ -z "$registry" ] || import_args+=(--registry "$registry")
[ "$plain_http" -eq 0 ] || import_args+=(--plain-http)

output=$(mktemp)
trap 'rm -f "$output"' EXIT

# run_import runs cargueiro import for a family and a minor, with the options given
# after them, leaves what it printed in $output, and shows it indented. It fails
# when cargueiro does.
run_import() {
	local family=$1 minor=$2 status=0
	shift 2
	"$cargueiro" import "${import_args[@]}" --family "$family" --minor "$minor" "$@" >"$output" || status=$?
	sed 's/^/  /' "$output"
	return "$status"
}

# commit_family commits what the family left in legacy/ and assets/logos/, with the
# number of versions of each minor in the message. It does nothing when there is
# nothing to commit.
commit_family() {
	local family=$1 summary=$2 path
	local paths=()
	for path in legacy assets/logos; do
		[ ! -e "$repo_dir/$path" ] || paths+=("$path")
	done
	[ ${#paths[@]} -gt 0 ] || return 0
	git -C "$repo_dir" add -- "${paths[@]}"
	[ -n "$(git -C "$repo_dir" status --porcelain -- "${paths[@]}")" ] || return 0
	# Only these paths are committed, whatever else is staged.
	git -C "$repo_dir" commit -q -m "legacy: import $family" -m "$summary" -- "${paths[@]}"
	echo "committed legacy: import $family"
}

changed=0
if [ "$dry_run" -eq 1 ]; then
	total=0
	for family in "${families[@]}"; do
		for minor in "${minors[@]}"; do
			run_import "$family" "$minor" --dry-run || die "the dry run of $family $minor failed"
			count=$(grep -c '^import ' "$output" || true)
			if [ "$count" -gt 0 ]; then
				echo "$family $minor: $count versions to import"
				total=$((total + count))
			fi
		done
	done
	echo "$total versions to import"
	exit 0
fi

for family in "${families[@]}"; do
	summary=""
	for minor in "${minors[@]}"; do
		run_import "$family" "$minor" || die "the import of $family $minor failed: run it again to finish, what was done is kept"
		count=$(grep -c -E '^(pushed|adopted) ' "$output" || true)
		if [ "$count" -gt 0 ]; then
			echo "$family $minor: $count versions"
			summary+="$minor: $count versions"$'\n'
		fi
	done
	if [ -n "$summary" ]; then
		commit_family "$family" "$summary"
		changed=$((changed + 1))
	fi
done

if [ "$index" -eq 1 ]; then
	"$cargueiro" index --repo "$repo_dir" >"$output" || die "cargueiro index failed"
	sed 's/^/  /' "$output"
	if [ -f "$repo_dir/index.yaml" ]; then
		git -C "$repo_dir" add -- index.yaml
		if [ -n "$(git -C "$repo_dir" status --porcelain -- index.yaml)" ]; then
			git -C "$repo_dir" commit -q -m "legacy: regenerate the index" -- index.yaml
			echo "committed legacy: regenerate the index"
		fi
	fi
fi

# Nothing is left to import.
left=0
for family in "${families[@]}"; do
	for minor in "${minors[@]}"; do
		run_import "$family" "$minor" --dry-run >/dev/null || die "the check of $family $minor failed"
		if ! grep -q '^nothing to import$' "$output"; then
			echo "left to import: $family $minor" >&2
			left=1
		fi
	done
done
[ "$left" -eq 0 ] || die "something is left to import"

echo "done: $changed families imported"
