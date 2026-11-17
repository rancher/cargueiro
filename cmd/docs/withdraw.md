# withdraw

Withdraw a published version from the catalog.

The argument is the entry of the version, `entries/<chart>/<version>.yaml` of a charts repository, which is the folder that holds `entries/`. Use it when a published version has a critical CVE or was published wrong and cannot be fixed at once. A withdrawn version is out of `index.yaml`, so no client can resolve it, and its entry and archive stay in the repository and the registry. It is not the `catalog.cattle.io/hidden` annotation: a hidden chart is not shown in the UI but is still in the index and can be installed.

The command adds `withdrawn: true` to the entry as text and changes nothing else in it. It does not touch `index.yaml` or `assets/`: the next `cargueiro index` run takes the version out of the index and says so. A version that is in the production repository is withdrawn in the staging one and in the production one, each by its own run.

It refuses, and writes nothing: a path that is not `entries/<chart>/<version>.yaml`, so a legacy version cannot be withdrawn, an entry that is not there, an entry that does not hold the chart and version its path says, an entry that is withdrawn already, and a version that is not in the `index.yaml` of the repository, because only a published version can be withdrawn. Withdrawing is one way: there is no command to undo it, and taking the mark off the entry does not publish the version again.

It prints `withdraw <chart> <version>`. `--dry-run` runs every check and prints the same line, and writes nothing. The exit code is 0, or 1 when the entry is refused.

Target-Audience: Release Team and Automation

## Examples

```
# withdraw a published version
cargueiro withdraw ~/charts/entries/rancher-monitoring/111.0.0+up80.9.1-rancher.23.yaml
```

```
# check what would be withdrawn, and change nothing
cargueiro withdraw --dry-run ~/charts/entries/fleet/111.0.3+up0.17.3.yaml
```
