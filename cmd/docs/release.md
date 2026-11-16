# release

Release the legacy versions from the staging charts repository to the production one.

`--legacy` is required, because the legacy versions, the ones in `legacy/` that `import` wrote, are the only thing this command releases for now. `--from` is a checkout of the staging charts repository and `--repo` is a checkout of the production one. The versions to release are the ones that have an entry in the `legacy/` of staging and that production has neither in its `legacy/` nor in its `index.yaml`. The command never releases a version again, and a legacy entry never changes.

Each version is copied on its own, from the registry its entry says to the production registry of its edition, `registry.suse.com/rancher/catalog` for the charts of `rancher-charts`, and `--registry` changes it, as `host[:port]/path`, for a local registry. The copy keeps the manifest as it is, by its digest, so the production tag names the same manifest the entry pins. First the production registry is looked at: a tag that is not there is copied and read back, and its manifest must be the one of the entry; a tag that has the same manifest is adopted and not copied again; a tag that has another manifest is a conflict, and it is never replaced. Only then the entry is written to the `legacy/` of the production repository, the entry of staging as it is with the URL changed to point to the manifest in the production registry, and the logos the versions use are copied from staging when production does not have them. The entry is the last thing done for a version, so a run that stops anywhere is run again to finish: what is in `legacy/` is complete, and what was copied and has no entry is adopted.

A version that fails does not stop the others. One line is printed for each version as it is done, `released <chart> <version>`, `adopted <chart> <version>`, `failed <chart> <version>: <reason>` or `conflict <chart> <version>: <what each registry has>`, and `copied logo <name>`, and at the end a summary. When something did not go through the summary says so apart: `CONFLICT` for the versions whose tag has other content in production, which are left as they are and need someone to look at them, and `FAILED` for the ones that a new run releases. The exit code is 0 when every version was released, and 1 otherwise. The command writes `legacy/` and `assets/logos/`, not `index.yaml`: run `cargueiro index` after it.

`--dry-run` reads the two repositories only and prints `release <chart> <version>` for each version that would be released, `copy logo <name>` and `missing logo <name>`, or `nothing to release`.

The charts are read with the credentials of `--username` (or `CARGUEIRO_USERNAME`) and the password in `CARGUEIRO_PASSWORD`, or the login saved by `cargueiro registry login` for that registry, or anonymously. They are written with their own credentials: `--production-username` (or `CARGUEIRO_PRODUCTION_USERNAME`) and the password in `CARGUEIRO_PRODUCTION_PASSWORD`, or the saved login for the production registry. A release works between one registry and one registry. The copy is guarded as any push is: on the SUSE registries a chart can only go to the repositories of its edition. A copy is tried again when the network fails. Credentials are sent only to their registry, and over HTTP only with `--plain-http`, for local registries.

Target-Audience: Release Team and Automation

## Examples

```
# what would be released to production
cargueiro release --legacy --from ~/staging --repo ~/production --dry-run
```

```
# release it
cargueiro release --legacy --from ~/staging --repo ~/production
```

```
# release between local registries
cargueiro release --legacy --from ~/staging --repo ~/production --registry 127.0.0.1:5052/release --plain-http
```
