# Manual test: `cargueiro check-pushed-dependencies`

Run it from the root of the repository, in bash or zsh, and paste the whole block. It needs Go, `oras` (pinned in `mise.toml`), `curl` and `tar`, the local registries (`make registry-up`), and the network: it reads, anonymously, the charts of the staging catalog from `raw.githubusercontent.com` and the staging images registry (`stgregistry.suse.com`). The local registry it uses is the one without a password, on `127.0.0.1:5052`. The setup builds the binary of the current checkout into `.tmp/manual-tests/bin` and puts it first on the `PATH` of your shell, and the last step takes it off again.

For every step it prints a **green title**, an **orange line** with what to expect, the **command in blue** after a `$`, the output, and a bold `=> exit N`. A command is the text inside `run '…'`.

```bash
hdr()  { printf '\n\033[1;38;2;48;186;120m%s\033[0m\n' "$1"; }
want() { printf '\033[38;2;254;124;63mexpect: %s\033[0m\n' "$1"; }
run()  { printf '\033[38;2;0;117;168m$ %s\033[0m\n' "$1"; eval "$1"; printf '\033[1m=> exit %s\033[0m\n' "$?"; }

hdr "SETUP: build the binary of this checkout and put it first on the PATH of this shell"
run 'mkdir -p .tmp/manual-tests/bin && go build -o .tmp/manual-tests/bin/cargueiro .'
run 'PATH="$PWD/.tmp/manual-tests/bin:$PATH"'
run 'command -v cargueiro && cargueiro version'

hdr "1. HAPPY: a real chart of this repository, against the real staging images registry"
want "two references, stgregistry.suse.com/rancher/fleet:v0.16.3 and stgregistry.suse.com/rancher/fleet-agent:v0.16.3, one on each line; exit 0"
run 'cargueiro check-pushed-dependencies tests/data/charts/fleet/src/fleet'

hdr "2. HAPPY: a chart that has no images, a CRD chart"
want "nothing printed at all, because there is nothing to look for; exit 0"
run 'cargueiro check-pushed-dependencies tests/data/charts/fleet/src/fleet-crd'

hdr "3. HAPPY: a chart staged in the real catalog, whose four images are in the registry (prometheus-federator, once its images were pushed)"
want "four references, in the order of the annotation: helm-controller, kuberlr-kubectl, klipper-helm and prometheus-federator, all with stgregistry.suse.com in front; exit 0"
run 'mkdir -p .tmp/manual-tests/pf && curl -sfL "https://raw.githubusercontent.com/rancher/charts/staging/assets/prometheus-federator/prometheus-federator-111.0.0%2Bup8.0.0-rc.3.tgz" | tar -xz -C .tmp/manual-tests/pf'
run 'cargueiro check-pushed-dependencies .tmp/manual-tests/pf/prometheus-federator'

hdr "4. FAILURE: a chart staged in the real catalog that lists an image the registry does not have (rancher-turtles rc.7)"
want "stdout empty; stderr 'error: 1 of 2 images of the chart cannot be found in stgregistry.suse.com:' and, indented, 'rancher/cluster-api-controller:v1.14.4_turtles.1: not found'; exit 1"
run 'mkdir -p .tmp/manual-tests/turtles && curl -sfL "https://raw.githubusercontent.com/rancher/charts/staging/assets/rancher-turtles/rancher-turtles-111.0.0%2Bup0.28.0-rc.7.tgz" | tar -xz -C .tmp/manual-tests/turtles'
run 'cargueiro check-pushed-dependencies .tmp/manual-tests/turtles/rancher-turtles'

hdr "5. FAILURE: every image that is not there is reported together, sorted, and the one that is there is not"
want "error: 2 of 3 images of the chart cannot be found in stgregistry.suse.com, then rancher/fleet-agent:v0.0.0-not-there and rancher/fleet:v0.0.0-not-there, each ': not found'; exit 1"
run 'mkdir -p .tmp/manual-tests/missing && printf "name: example\nversion: 111.0.0+up1.0.0\nannotations:\n  helm.sh/images: |\n    - name: fleet\n      image: rancher/fleet:v0.16.3\n    - name: gone\n      image: rancher/fleet:v0.0.0-not-there\n    - name: gone-agent\n      image: rancher/fleet-agent:v0.0.0-not-there\n" > .tmp/manual-tests/missing/Chart.yaml'
run 'cargueiro check-pushed-dependencies .tmp/manual-tests/missing'

hdr "6. FAILURE: nothing is printed to stdout, so a script that chains the push after it never pushes"
want "the first line prints 0, no byte on stdout; the second prints only 'not pushed', because the command that follows the && never runs"
run 'cargueiro check-pushed-dependencies .tmp/manual-tests/missing 2>/dev/null | wc -c'
run 'cargueiro check-pushed-dependencies .tmp/manual-tests/missing 2>/dev/null && echo "would push" || echo "not pushed"'

hdr "7. LOCAL: push two images to the local registry without a password, and check a chart that lists them"
want "(the images stay in that registry, and pushing them again does no harm) two 'Pushed' lines; then the two references 127.0.0.1:5052/rancher/manual-test-a:v1 and 127.0.0.1:5052/rancher/manual-test-b:v1; exit 0"
run 'for i in a b; do oras push --plain-http --artifact-type application/vnd.cargueiro.manual.image 127.0.0.1:5052/rancher/manual-test-$i:v1 | grep Pushed; done'
run 'mkdir -p .tmp/manual-tests/local && printf "name: example\nversion: 111.0.0+up1.0.0\nannotations:\n  helm.sh/images: |\n    - name: a\n      image: rancher/manual-test-a:v1\n    - name: b\n      image: rancher/manual-test-b:v1\n" > .tmp/manual-tests/local/Chart.yaml'
run 'cargueiro check-pushed-dependencies .tmp/manual-tests/local --registry 127.0.0.1:5052 --plain-http'

hdr "8. LOCAL FAILURE: the same chart with a tag that was never pushed"
want "stdout empty; 'error: 1 of 2 images of the chart cannot be found in 127.0.0.1:5052:' and 'rancher/manual-test-b:v2: not found'; exit 1"
run 'sed -i "s/manual-test-b:v1/manual-test-b:v2/" .tmp/manual-tests/local/Chart.yaml'
run 'cargueiro check-pushed-dependencies .tmp/manual-tests/local --registry 127.0.0.1:5052 --plain-http'

hdr "9. FAILURE: --registry takes only the host, because the images carry their own path"
want "error: --registry takes only host[:port]: the images carry their own path; exit 1, and nothing is looked for"
run 'cargueiro check-pushed-dependencies .tmp/manual-tests/local --registry 127.0.0.1:5052/rancher --plain-http'

hdr "10. FAILURE: a directory that is not a chart"
want "stdout empty; an error that says Chart.yaml; exit 1"
run 'cargueiro check-pushed-dependencies .tmp/manual-tests/bin'

hdr "CLEANUP: take the binary off the PATH and remove the copies"
run 'PATH=${PATH#"$PWD/.tmp/manual-tests/bin:"}; rm -rf .tmp/manual-tests'
unset -f hdr want run
```
