# Manual test: subchart images in helm.sh/images

Run it from the root of the repository, in bash or zsh, and paste the whole block. It needs Go, and no registry, because the version is given. The setup builds the binary of the current checkout into `.tmp/manual-tests/bin` and puts it first on the `PATH` of your shell, so every command is a plain `cargueiro …` with real paths. The chart it uses is a copy of the `rancher-compliance` fixture in `.tmp/manual-tests/compliance`, with two subcharts added under `charts/`, `grafana` and `hardened-exporter`, each with one image: the same repository, `rancher/mirrored-library-busybox`, at two different tags, the way two real subcharts of `rancher-monitoring` do. Everything the test creates is in `.tmp/manual-tests`, which git ignores, and the block removes it at the end.

For every step it prints a **green title**, an **orange line** with what to expect, the **command in blue** after a `$`, the output, and a bold `=> exit N`. A command is the text inside `run '…'`.

```bash
hdr()  { printf '\n\033[1;38;2;48;186;120m%s\033[0m\n' "$1"; }
want() { printf '\033[38;2;254;124;63mexpect: %s\033[0m\n' "$1"; }
run()  { printf '\033[38;2;0;117;168m$ %s\033[0m\n' "$1"; eval "$1"; printf '\033[1m=> exit %s\033[0m\n' "$?"; }

hdr "SETUP: build the binary of this checkout, put it first on the PATH of this shell, and add two subcharts to a copy of the chart"
run 'mkdir -p .tmp/manual-tests/bin && go build -o .tmp/manual-tests/bin/cargueiro .'
run 'PATH="$PWD/.tmp/manual-tests/bin:$PATH"'
run 'command -v cargueiro && cargueiro version'
run 'cp -r tests/data/charts/rancher-compliance-upstream/src/rancher-compliance .tmp/manual-tests/compliance'
run 'mkdir -p .tmp/manual-tests/compliance/charts/grafana .tmp/manual-tests/compliance/charts/hardened-exporter'
run 'printf "name: grafana\nversion: 1.0.0\n" > .tmp/manual-tests/compliance/charts/grafana/Chart.yaml'
run 'printf "image:\n  repository: rancher/mirrored-library-busybox\n  tag: \"1.31.1\"\n" > .tmp/manual-tests/compliance/charts/grafana/values.yaml'
run 'printf "name: hardened-exporter\nversion: 1.0.0\n" > .tmp/manual-tests/compliance/charts/hardened-exporter/Chart.yaml'
run 'printf "image:\n  repository: rancher/mirrored-library-busybox\n  tag: \"1.37.0\"\n" > .tmp/manual-tests/compliance/charts/hardened-exporter/values.yaml'

hdr "1. HAPPY: before stamping, check chart reports every image missing from helm.sh/images, including both subcharts'"
want "hip15-annotations: invalid, listing the 4 chart images and the 2 subchart images (charts/grafana/values.yaml, charts/hardened-exporter/values.yaml) as not listed; error: chart is not valid; exit 1"
run 'cargueiro check chart .tmp/manual-tests/compliance'

hdr "2. HAPPY: stamp the chart"
want "helm.sh/images: added kuberlr-kubectl, compliance-operator, security-scan, mirrored-sonobuoy-sonobuoy, mirrored-library-busybox, mirrored-library-busybox; exit 0"
run 'cargueiro stamp .tmp/manual-tests/compliance --version 111.0.0+up1.5.4'

hdr "3. HAPPY: the generated annotation lists both subchart images under the same name, each with its own dependency"
want "two entries named mirrored-library-busybox: one for tag 1.31.1 with dependency grafana, one for tag 1.37.0 with dependency hardened-exporter"
run 'grep -A2 "name: mirrored-library-busybox" .tmp/manual-tests/compliance/Chart.yaml'

hdr "4. HAPPY: check chart now passes"
want "valid: true; exit 0"
run 'cargueiro check chart .tmp/manual-tests/compliance'

hdr "5. HAPPY: packaging reads the subcharts from the archive too, and check archive agrees"
want "one archive path on stdout, then valid: true from check archive; exit 0 both times"
run 'cargueiro package .tmp/manual-tests/compliance --output-dir .tmp/manual-tests/out'
run 'cargueiro check archive .tmp/manual-tests/out/rancher-compliance-111.0.0+up1.5.4.tgz'

hdr "CLEANUP: take the binary off the PATH and remove the copies"
run 'PATH=${PATH#"$PWD/.tmp/manual-tests/bin:"}; rm -rf .tmp/manual-tests'
unset -f hdr want run
```
