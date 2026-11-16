# import

Report the versions of the old system that are to be imported into a charts repository.

The old system is a checkout of the charts repository as it was, with the `index.yaml` of its versions and their archives in `assets/`. `--source` is that checkout and `--repo` is the charts repository to import into. One family and one Rancher minor are imported at a time: `--family` is a family of `rules.yaml` and `--minor` is a Rancher version such as `2.12`, whose charts are the ones with its chart major (105 for 2.10 up to 110 for 2.15). Only the minors of the old system can be imported: the charts of a Rancher that the new system makes charts for are released, not imported.

For now it only reports, and `--dry-run` is required. It reads the source and the repository, and nothing else: no registry is read and nothing is written. It prints `import <chart> <version>` for each version of the family and the minor that is not in `legacy/` yet, the charts of the family in order and the versions of each newest first, then `copy logo <name>` for each logo that those versions use and the source has but the repository does not, and `missing logo <name>` for each one that the source does not have either. A logo the repository has is never replaced, and an icon on another address is left as it is. When there is nothing to do it says `nothing to import`. The exit code is 0, or 1 when a check fails.

It fails, naming every version at fault, when a version to import is also an entry of the new system, or when its archive in the source is missing or does not have the sha256 that the index of the source gives, because that digest is what the registry will be checked against. `--registry` changes the registry the archives would be pushed to, as `host[:port]/path`, instead of the staging registry of the chart.

Target-Audience: Release Team and Automation

## Examples

```
# what would be imported of the fleet family for Rancher 2.15
cargueiro import --source ~/old-charts --repo ~/charts --family fleet --minor 2.15 --dry-run
```
