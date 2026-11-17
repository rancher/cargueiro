# release

Release the versions of the staging charts repository to the production one.

`--from` is a checkout of the staging charts repository and `--repo` is a checkout of the production one. There are two kinds of version, and one run releases one kind: the **new versions**, the ones in `entries/`, are the default, and `--legacy` releases the **legacy versions**, the ones in `legacy/` that `import` wrote, and nothing else. A run never reads the folder of the other kind, in staging or in production. A version is released once: what production already has is left alone, and what is in an entry never changes.

## New versions

The versions to release are the ones in the `entries/` of staging that production has neither in its `entries/` nor in its `index.yaml`. Two kinds are left out and said so, as `skip <chart> <version>: <reason>`: a hidden version (`hide: true`), and a release candidate, a version that ends in `-rc.N`, `-beta.N` or `-alpha.N`, which stay on staging. A version with a `-rancher.N` ending is a final version and is released.

Each version goes through the same steps, and the entry is written last, so a run that stops anywhere is run again to finish:

1. **The chart is checked.** It is read from the staging registry and the rules of the catalog are applied to it, as `check oci` does. A chart that is not valid is not released, and nothing of it is copied.
2. **Its images are copied.** The images in its `helm.sh/images` annotation are `rancher/<name>:<tag>`, and each goes from `<name>` of the staging images registry to the same name in the production one (`rancher` on `stgregistry.suse.com` and on `registry.suse.com`), by the digest of its manifest, and is read back. An image that production has already is left as it is, whatever it holds, and is not a failure. An image that is in neither registry makes the version fail, because the chart would be released without it. An image that is not under `rancher/` cannot be moved and is an error, found before anything is copied.
3. **The chart is copied** to the production registry of its edition, `registry.suse.com/rancher/catalog` for `rancher-charts`, with the manifest as it is, by its digest, and read back. A tag that production has with the same manifest is adopted and not copied again, and one with another manifest is a conflict and is never replaced.
4. **The entry is written** to the `entries/` of production: the file of staging, byte for byte, with only the registry of its URL changed. The logos the versions use are copied when production does not have them.

The command writes `entries/` and `assets/logos/`, not `index.yaml`: run `cargueiro index --registry registry.suse.com/rancher/catalog` after it, which pulls each archive from the production registry.

## Legacy versions

With `--legacy` the versions are the ones in the `legacy/` of staging that production has neither in its `legacy/` nor in its `index.yaml`. Each chart is copied to the production registry of its edition by the digest of its manifest, as above, and its entry is written to the `legacy/` of production, the entry of staging with the URL pointing to the manifest in the production registry. Nothing is checked and no image is copied: they were imported as they were published, and `--images-registry` and `--staging-images-registry` are refused.

## What a run says

A version that fails does not stop the others. One line is printed for each image and version as it is done, `image copied <ref>`, `image present <ref>`, `released <chart> <version>`, `adopted <chart> <version>`, `failed <chart> <version>: <reason>` or `conflict <chart> <version>: <what each registry has>`, and `copied logo <name>`, and at the end a summary. When something did not go through the summary says so apart: `CONFLICT` for the versions whose tag has other content in production, which are left as they are and need someone to look at them, and `FAILED` for the ones that a new run releases. The exit code is 0 when every version was released, and 1 otherwise.

`--family` limits the run to the charts of a family of `rules.yaml`, and it can be given more than once: a release of every family copies more than one run can finish, so it is done a family at a time. A family that `rules.yaml` does not list is an error.

`--dry-run` reads the two repositories only and prints what would be released: `skip <chart> <version>: <reason>`, `release <chart> <version>` with an `  image <ref>` line for each image of a new version, `copy logo <name>` and `missing logo <name>`, or `nothing to release`.

`--registry` replaces the production registry of the charts, as `host[:port]/path`, for a local registry. `--images-registry` and `--staging-images-registry` do the same for the production and the staging registry of the images of new versions.

The charts and the images are read with the credentials of `--username` (or `CARGUEIRO_USERNAME`) and the password in `CARGUEIRO_PASSWORD`, or the login saved by `cargueiro registry login` for that registry, or anonymously. They are written with their own credentials: `--production-username` (or `CARGUEIRO_PRODUCTION_USERNAME`) and the password in `CARGUEIRO_PRODUCTION_PASSWORD`, or the saved login for the production registry. A release works between one registry and one registry, the images included. The copy is guarded as any push is: on the SUSE registries a chart can only go to the repositories of its edition and an image only to an images repository. A copy is tried again when the network fails. Credentials are sent only to their registry, and over HTTP only with `--plain-http`, for local registries.

Target-Audience: Release Team and Automation

## Examples

```
# what would be released to production, and what is left out
cargueiro release --from ~/staging --repo ~/production --dry-run
```

```
# release the new versions of one family
cargueiro release --family rancher-monitoring --from ~/staging --repo ~/production
```

```
# release the legacy versions
cargueiro release --legacy --from ~/staging --repo ~/production
```

```
# release between local registries
cargueiro release --from ~/staging --repo ~/production --registry 127.0.0.1:5052/catalog --images-registry 127.0.0.1:5052/images --staging-images-registry 127.0.0.1:5050/images --plain-http
```
