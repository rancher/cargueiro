# import

Import the versions of the old system into a charts repository, as legacy entries.

The old system is a checkout of the charts repository as it was, with the `index.yaml` of its versions and their archives in `assets/`. `--source` is that checkout and `--repo` is the charts repository to import into. One family and one Rancher minor are imported at a time: `--family` is a family of `rules.yaml` and `--minor` is a Rancher version such as `2.12`, whose charts are the ones with its chart major (105 for 2.10 up to 110 for 2.15). Only the minors of the old system can be imported: the charts of a Rancher that the new system makes charts for are released, not imported.

Each version of the family and the minor that is not in `legacy/` yet is checked against the registry before anything is pushed or written: its archive must hold the chart and the version its entry says, and the registry must not already have the tag with other content. A version is never overwritten, so a tag that has other content stops the run, naming every one, and nothing is pushed or written. A tag that has the archive is adopted and not pushed again. The logos that the versions use are copied from the source when the repository does not have them, and a logo the repository has is never replaced. Then each version is pushed if the registry did not have it, the chart in the registry is checked to be the archive, and the entry `legacy/<chart>/<version>.yaml` is written, the entry of the old index as it is with the URL that points to the manifest in the registry. The entry is the last thing done for a version, so a run that stops anywhere, because the registry fails or the command is cut off, is run again to finish: what is in `legacy/` is complete, and what was pushed and has no entry is adopted. One line is printed for each version as it is done, `pushed <chart> <version>` or `adopted <chart> <version>`, and `copied logo <name>` for each logo, or `nothing to import`. The exit code is 0, or 1 when a check fails or the registry does.

It fails before it pushes anything, naming every version at fault, when a version is also an entry of the new system, or when its archive in the source is missing or does not have the sha256 that the index of the source gives, because that digest is what the registry is checked against.

`--dry-run` reads the source and the repository only, and reads and writes nothing in a registry or in the repository. It prints `import <chart> <version>` for each version that would be imported, the charts of the family in order and the versions of each newest first, then `copy logo <name>` for each logo that would be copied and `missing logo <name>` for each logo that the versions name and the source does not have, which is as dead there as it will be here. An icon on another address is left as it is.

The archives are pushed to the staging registry of the edition of the family, `rancher/catalog` on `stgregistry.suse.com` for the charts of `rancher-charts`. `--registry` changes it, as `host[:port]/path`, for a local registry. The push is guarded as any other: on the SUSE registries a chart can only go to the repositories of its edition. Credentials are optional. Without them the registry is read anonymously, which does not push. To give them, use `--username` (or `CARGUEIRO_USERNAME`) and the password through `CARGUEIRO_PASSWORD`; otherwise the login saved by `cargueiro registry login` is used when it is for this registry. They are sent only to that registry, and over HTTP only with `--plain-http`, for local registries.

Target-Audience: Release Team and Automation

## Examples

```
# what would be imported of the fleet family for Rancher 2.15
cargueiro import --source ~/old-charts --repo ~/charts --family fleet --minor 2.15 --dry-run
```

```
# import it, to the staging registry
cargueiro import --source ~/old-charts --repo ~/charts --family fleet --minor 2.15
```

```
# import it to a local registry
cargueiro import --source ~/old-charts --repo ~/charts --family fleet --minor 2.15 --registry 127.0.0.1:5050/legacy --plain-http
```
