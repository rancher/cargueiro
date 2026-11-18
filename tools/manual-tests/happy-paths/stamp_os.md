# Manual test: the `os` of an image in helm.sh/images

Run it from the root of the repository, in bash or zsh, and paste the whole block. It needs Go, and no registry, because the version is given. The setup builds the binary of the current checkout into `.tmp/manual-tests/bin` and puts it first on the `PATH` of your shell, so every command is a plain `cargueiro …` with real paths. The chart it uses is a copy of the `fleet-agent` fixture, moved to Rancher 2.16, in `.tmp/manual-tests/fleet-agent`, whose `values.yaml` has `os: "windows,linux"` in its image block, without its `helm.sh/images` annotation, and with two subcharts under `charts/`: `with-os`, whose image has an `os`, and `no-os`, whose image has none, since `os` is optional. Everything the test creates is in `.tmp/manual-tests`, which git ignores, and the block removes it at the end.

For every step it prints a **green title**, an **orange line** with what to expect, the **command in blue** after a `$`, the output, and a bold `=> exit N`. A command is the text inside `run '…'`.

```bash
hdr()  { printf '\n\033[1;38;2;48;186;120m%s\033[0m\n' "$1"; }
want() { printf '\033[38;2;254;124;63mexpect: %s\033[0m\n' "$1"; }
run()  { printf '\033[38;2;0;117;168m$ %s\033[0m\n' "$1"; eval "$1"; printf '\033[1m=> exit %s\033[0m\n' "$?"; }

hdr "SETUP: build the binary of this checkout, put it first on the PATH of this shell, and prepare the chart"
run 'mkdir -p .tmp/manual-tests/bin && go build -o .tmp/manual-tests/bin/cargueiro .'
run 'PATH="$PWD/.tmp/manual-tests/bin:$PATH"'
run 'command -v cargueiro && cargueiro version'
run 'cp -r tests/data/charts/fleet/src/fleet-agent .tmp/manual-tests/fleet-agent'
run 'grep -v -e "- name: fleet-agent" -e "      image: " tests/data/charts/fleet/src/fleet-agent/Chart.yaml | sed "s/2.15.0-0 < 2.16.0-0/2.16.0-0 < 2.17.0-0/" > .tmp/manual-tests/fleet-agent/Chart.yaml'
run 'mkdir -p .tmp/manual-tests/fleet-agent/charts/with-os .tmp/manual-tests/fleet-agent/charts/no-os'
run 'printf "name: with-os\nversion: 1.0.0\n" > .tmp/manual-tests/fleet-agent/charts/with-os/Chart.yaml'
run 'printf "image:\n  repository: rancher/with-os\n  tag: v1\n  os: linux\n" > .tmp/manual-tests/fleet-agent/charts/with-os/values.yaml'
run 'printf "name: no-os\nversion: 1.0.0\n" > .tmp/manual-tests/fleet-agent/charts/no-os/Chart.yaml'
run 'printf "image:\n  repository: rancher/no-os\n  tag: v1\n" > .tmp/manual-tests/fleet-agent/charts/no-os/values.yaml'

hdr "1. HAPPY: stamp the chart"
want "helm.sh/images: added fleet-agent, no-os, with-os; exit 0"
run 'cargueiro stamp .tmp/manual-tests/fleet-agent --version 111.0.0+up0.16.3'

hdr "2. HAPPY: the os of each image block is copied to its entry, before the dependency"
want "fleet-agent has os: windows,linux; no-os has no os line, only dependency: no-os; with-os has os: linux and then dependency: with-os"
run 'grep -A9 "helm.sh/images" .tmp/manual-tests/fleet-agent/Chart.yaml'

hdr "3. HAPPY: stamping again changes nothing, and check chart passes"
want "'nothing to change' from stamp, then valid: true from check chart; exit 0 both times"
run 'cargueiro stamp .tmp/manual-tests/fleet-agent --version 111.0.0+up0.16.3'
run 'cargueiro check chart .tmp/manual-tests/fleet-agent'


hdr "4. HAPPY: an annotation that exists is kept as it is: the entry of fleet-agent, listed without os, stays so, and only the subcharts are added"
want "helm.sh/images: added no-os, with-os; the fleet-agent entry has no os line; exit 0"
run 'sed "s/2.15.0-0 < 2.16.0-0/2.16.0-0 < 2.17.0-0/" tests/data/charts/fleet/src/fleet-agent/Chart.yaml > .tmp/manual-tests/fleet-agent/Chart.yaml'
run 'cargueiro stamp .tmp/manual-tests/fleet-agent --version 111.0.0+up0.16.3'
run 'grep -A9 "helm.sh/images" .tmp/manual-tests/fleet-agent/Chart.yaml'

hdr "CLEANUP: take the binary off the PATH and remove the copies"
run 'PATH=${PATH#"$PWD/.tmp/manual-tests/bin:"}; rm -rf .tmp/manual-tests'
unset -f hdr want run
```
