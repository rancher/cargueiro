# copy image signatures

Copy the signatures of images from the staging images registry to the production one.

`release` copies an image by the digest of its manifest, and a signature is not part of that manifest: it is another artifact that refers to the image. So an image that is released has no signature in the production registry, and this command copies it. The images are given as `repository:tag`, such as `rancher/fleet:v0.17.0`, and are looked for as `host/repository:tag` in both registries, so only the hosts are needed. Without `--from` and `--to` they are the hosts of the staging and production images registries of `rules.yaml`, which is compiled into the binary.

Only the signatures are copied, never an image. Each image has to be in both registries, and the tag has to name the same manifest in both: the signatures are of that manifest. An image that is not in the destination, or that has another manifest there, is an error that says so. The copy is [slsactl](https://github.com/rancherlabs/slsactl)'s, the one that `charts-build-scripts` used, and it never replaces what the destination has: the signatures are written by their own digest, and a tag that is there is left as it is. It copies the sigstore bundles and the cosign signatures, not SBOMs or attestations. An image whose source has no signature is said so and is not an error. After a copy, the signatures of the destination are counted, and it is an error when it has fewer than the source.

The source is read with `--username` (or `CARGUEIRO_USERNAME`) and the password in `CARGUEIRO_PASSWORD`, and the destination is written with `--production-username` (or `CARGUEIRO_PRODUCTION_USERNAME`) and the password in `CARGUEIRO_PRODUCTION_PASSWORD`, as `release` does, or with the login saved by `cargueiro registry login` for that registry. Each pair is only sent to its own registry. slsactl takes its credentials from the docker config, so for the copy they are written to a config of their own in a temporary folder, with only the registries that have credentials, and it is removed when the command ends. `--plain-http` talks HTTP to the registries instead of HTTPS, for local registries.

Every image is done before the result is given: a line is written to stdout for each one, and the exit code is 0 when all went through. Otherwise stderr says how many failed and why, and the exit code is 1. `--dry-run` reads both registries and says what would be copied, and copies nothing.

Target-Audience: Release Team and Automation

## Examples

```
# what would be copied for two images that were released
cargueiro copy-image-signatures rancher/fleet:v0.17.0 rancher/rancher-webhook:v0.12.1 --dry-run
```

```
# copy them
cargueiro copy-image-signatures rancher/fleet:v0.17.0 rancher/rancher-webhook:v0.12.1
```

```
# two local registries over HTTP
cargueiro copy-image-signatures rancher/fleet:v0.17.0 --from 127.0.0.1:5052 --to 127.0.0.1:5053 --plain-http
```
