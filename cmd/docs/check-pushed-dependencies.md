# check pushed dependencies

Check that the images of a chart are in the images registry, with the tag the chart names.

The argument is one chart directory with a `Chart.yaml`, the same that `stamp` and `check chart` take, after `stamp` has put the `helm.sh/images` annotation on it. Every image the annotation lists is looked for, the images of subcharts included, and nothing else of the chart is checked: `check chart` is what checks its form. A chart with no `helm.sh/images` has no images, so it passes and nothing is printed.

Each image is written `repository:tag`, or `repository@digest`, and is looked for in the images registry as `host/repository:tag`: the images carry their own path, so only the host of the registry is needed. Without `--registry` it is the host of the staging images registry of `rules.yaml`, which is compiled into the binary, and `--registry host[:port]` reads another one, such as a local registry. Only its existence is checked: the registry is asked for the manifest of the tag, or of the digest, and the image is not downloaded. An image that is listed twice is looked for once.

Credentials are optional. Without them the registry is read anonymously, which is right for a public path. To give them, use `--username` (or `CARGUEIRO_USERNAME`) and the password through `CARGUEIRO_PASSWORD`; otherwise the login saved by `cargueiro registry login` is used when it is for this registry. They are sent only to that registry, and over HTTP only with `--plain-http`. `--plain-http` talks HTTP to the registry instead of HTTPS, for local registries.

When a read fails because the network does, or because the registry is busy, it is tried again a few times. An image that the registry does not have is not tried again.

Every image is looked for before the result is given. When all are there, their references are printed to stdout, one on each line, in the order of the annotation, and the exit code is 0. When some are not, nothing is printed to stdout: stderr has the count and one line for each image that could not be found, sorted, with the reason, such as `not found` or that the registry granted no access, and the exit code is 1. The same exit code is given when the directory is not a chart, the annotation is not a list of images, or `--registry` is not a host.

Target-Audience: Chart Owners and Automation

## Examples

```
# the images of a stamped chart are in the staging images registry
cargueiro check-pushed-dependencies charts/fleet
```

```
# stop before the push when one is missing
cargueiro check-pushed-dependencies charts/fleet && cargueiro push fleet-111.0.3+up0.16.3.tgz --registry stgregistry.suse.com/rancher/catalog
```

```
# a local registry over HTTP
cargueiro check-pushed-dependencies charts/fleet --registry 127.0.0.1:5052 --plain-http
```
