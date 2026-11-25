# Manual test: `cargueiro release`, with the signatures of its images

Run it from the root of the repository, in bash or zsh, and paste the whole block. It needs Go, `git`, `jq`, `oras` (pinned in `mise.toml`), the local registries (`make registry-up`), and the network: the signed image is a real one, `rancher/rancher-webhook:v0.12.1`, which it reads, with its signatures, from the staging registry of SUSE (`stgregistry.suse.com`, no login), and writes to the local registries only.

The local registry without a password (`127.0.0.1:5052`) plays the staging registry, and the one with a password (`127.0.0.1:5050`) plays production. Everything goes to a path that was never used, so the test can be run again.

The real image has two things that refer to it: its **signature** (`https://sigstore.dev/cosign/sign/v1`) and its **SLSA provenance** (`https://slsa.dev/provenance/v1`). The release copies both, because `slsactl` copies the tag that lists what refers to the image, and creates that one tag in production.

For every step it prints a **green title**, an **orange line** with what to expect, the **command in blue** after a `$`, the output, and a bold `=> exit N`. A command is the text inside `run '…'`.

```bash
hdr()  { printf '\n\033[1;38;2;48;186;120m%s\033[0m\n' "$1"; }
want() { printf '\033[38;2;254;124;63mexpect: %s\033[0m\n' "$1"; }
run()  { printf '\033[38;2;0;117;168m$ %s\033[0m\n' "$1"; eval "$1"; printf '\033[1m=> exit %s\033[0m\n' "$?"; }

hdr "SETUP: build the binary of this checkout and put it first on the PATH of this shell"
run 'mkdir -p .tmp/manual-tests/bin && go build -o .tmp/manual-tests/bin/cargueiro .'
run 'PATH="$PWD/.tmp/manual-tests/bin:$PATH"'
run 'command -v cargueiro && cargueiro version'

hdr "SETUP: the paths of this run in the two local registries"
run 'BASE="manual-release-$(date +%s)"; STG="127.0.0.1:5052/$BASE/staging"; PROD="127.0.0.1:5050/$BASE/production"; echo "$STG"; echo "$PROD"'
run 'export CARGUEIRO_CONFIG_DIR="$PWD/.tmp/manual-tests/config"'
run 'mkdir -p .tmp/manual-tests && printf "{\"auths\":{\"127.0.0.1:5050\":{\"auth\":\"%s\"}}}" "$(printf admin:cargueiro-test | base64)" > .tmp/manual-tests/oras-auth.json'
run 'SIGNATURE=https://sigstore.dev/cosign/sign/v1; PRODAUTH="--registry-config .tmp/manual-tests/oras-auth.json"'

hdr "SETUP: copy the real, signed rancher-webhook:v0.12.1 and its signatures from the staging registry of SUSE to the local staging images"
want "a copy of the image and of what refers to it, ending 'Copied' and a digest; exit 0"
run 'oras copy --recursive --to-plain-http stgregistry.suse.com/rancher/rancher-webhook:v0.12.1 $STG/images/rancher-webhook:v0.12.1'

hdr "SETUP: the image is the same one, and it has the signatures that the real staging registry has"
want "the two descriptors are equal, 'same image'; and two things that refer to the image, by their predicate type: the provenance and the cosign signature; exit 0"
run 'diff <(oras manifest fetch --descriptor stgregistry.suse.com/rancher/rancher-webhook:v0.12.1) <(oras manifest fetch --descriptor --plain-http $STG/images/rancher-webhook:v0.12.1) && echo "same image"'
run 'oras discover --format json --plain-http $STG/images/rancher-webhook:v0.12.1 | jq -r ".referrers[] | .annotations[\"dev.sigstore.bundle.predicateType\"]"'

hdr "SETUP: a chart that lists that image, pushed to the local staging registry and shipped to a checkout like the staging branch of rancher/charts"
want "the chart is only a vehicle for the image, so it is named fleet, a chart the rules know; three lines of 'Pushed' and 'Digest' or so, and entries/fleet/111.0.2+up0.17.0.yaml; exit 0"
run 'export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com GIT_CONFIG_GLOBAL=/dev/null'
run 'git init -q --bare --initial-branch=staging .tmp/manual-tests/charts.git && git init -q --initial-branch=staging .tmp/manual-tests/seed && : > .tmp/manual-tests/seed/index.yaml && git -C .tmp/manual-tests/seed add index.yaml && git -C .tmp/manual-tests/seed commit -q -m "an empty index" && git -C .tmp/manual-tests/seed remote add origin "$PWD/.tmp/manual-tests/charts.git" && git -C .tmp/manual-tests/seed push -q origin staging && git clone -q --branch staging "$PWD/.tmp/manual-tests/charts.git" .tmp/manual-tests/charts'
run 'mkdir -p .tmp/manual-tests/vehicle/fleet .tmp/manual-tests/packed'
run 'printf "apiVersion: v2\nname: fleet\nversion: 111.0.2+up0.17.0\nappVersion: 0.17.0\nannotations:\n  catalog.cattle.io/certified: rancher\n  catalog.cattle.io/release-name: fleet\n  catalog.cattle.io/rancher-version: \">= 2.16.0-0 < 2.17.0-0\"\n  catalog.cattle.io/kube-version: \">= 1.34.0-0 < 1.37.0-0\"\n  catalog.cattle.io/permits-os: linux\n  helm.sh/images: |\n    - name: webhook\n      image: rancher/rancher-webhook:v0.12.1\n" > .tmp/manual-tests/vehicle/fleet/Chart.yaml'
run 'printf "image:\n  repository: rancher/rancher-webhook\n  tag: v0.12.1\n" > .tmp/manual-tests/vehicle/fleet/values.yaml'
run 'cargueiro package .tmp/manual-tests/vehicle/fleet --output-dir .tmp/manual-tests/packed'
run 'echo x | cargueiro registry login 127.0.0.1:5052 --username x --password-stdin --plain-http'
run 'cargueiro push .tmp/manual-tests/packed/fleet-111.0.2+up0.17.0.tgz --registry $STG/charts'
run 'cargueiro ship $STG/charts/fleet:111.0.2+up0.17.0 --repo "$PWD/.tmp/manual-tests/charts" --plain-http'
run 'cargueiro registry logout'

hdr "SETUP: the production credentials of the local registry, which are the only ones this release needs"
run 'export CARGUEIRO_PRODUCTION_PASSWORD=cargueiro-test'
run 'mkdir -p .tmp/manual-tests/production'
run 'REL="cargueiro release --from $PWD/.tmp/manual-tests/charts --repo $PWD/.tmp/manual-tests/production --registry $PROD/charts --images-registry $PROD/images --staging-images-registry $STG/images --plain-http --production-username admin"'

hdr "1. HAPPY: a dry run says what would be released, and reads and writes nothing"
want "release fleet 111.0.2+up0.17.0, and under it the image, $STG/images/rancher-webhook:v0.12.1; nothing is said about signatures, because a dry run reads no registry; exit 0; entries/ is not created in the production folder"
run '$REL --dry-run'
run 'ls .tmp/manual-tests/production'

hdr "2. HAPPY: the release copies the image, and then its signatures, and then the chart"
want "'image copied $PROD/images/rancher-webhook:v0.12.1', and right after it 'signatures copied $PROD/images/rancher-webhook:v0.12.1 (1)'; then 'released fleet 111.0.2+up0.17.0'; a summary 'released 1 versions, adopted 0' and 'images: copied 1, already in production 0, signatures copied for 1'; exit 0"
run '$REL'
run 'ls .tmp/manual-tests/production/entries/fleet'

hdr "3. CHECK: the image in production is the same image as in staging, with the same tag"
want "the same descriptor, so 'same image'; exit 0"
run 'diff <(oras manifest fetch --descriptor --plain-http $STG/images/rancher-webhook:v0.12.1) <(oras manifest fetch --descriptor --plain-http $PRODAUTH $PROD/images/rancher-webhook:v0.12.1) && echo "same image"'

hdr "4. CHECK: production has the same signature and the same provenance as staging, the very same manifests"
want "two referrers in production, with the predicate types of the cosign signature and of the SLSA provenance, and the same two digests as in staging: 'same signature and provenance'; exit 0"
run 'oras discover --format json --plain-http $PRODAUTH $PROD/images/rancher-webhook:v0.12.1 | jq -r ".referrers[] | .annotations[\"dev.sigstore.bundle.predicateType\"]"'
run 'diff <(oras discover --format json --plain-http $STG/images/rancher-webhook:v0.12.1 | jq -r ".referrers[].digest" | sort) <(oras discover --format json --plain-http $PRODAUTH $PROD/images/rancher-webhook:v0.12.1 | jq -r ".referrers[].digest" | sort) && echo "same signature and provenance"'

hdr "5. CHECK: the tags of the image in production, which are the tag of the image that was there before and one tag more"
want "v0.12.1, and sha256-7968d806…, the tag that lists what refers to the image, which the copy created: the registry has no Referrers API. The two sha256-12a2… and sha256-42fe… are the ones that the copy of the image itself makes, one for each platform of the image; exit 0"
run 'oras repo tags --plain-http $PRODAUTH $PROD/images/rancher-webhook'

hdr "6. HAPPY: a second release has nothing to release, and changes nothing"
want "'nothing to release'; exit 0; and still the two referrers in production"
run '$REL'
run 'oras discover --format json --plain-http $PRODAUTH $PROD/images/rancher-webhook:v0.12.1 | jq ".referrers | length"'

hdr "7. CHECK: an image that production already has is left as it is, unsigned: staging has it signed, and production has it with no signature"
want "'image present …/images/other:v1', and no 'signatures copied' line; production still has no signature for it (0); exit 0 each. Releasing the image does not sign what was there"
run 'oras copy --recursive --to-plain-http stgregistry.suse.com/rancher/rancher-webhook:v0.12.1 $STG/images/other:v1 >/dev/null && oras copy --from-plain-http --to-plain-http --to-registry-config .tmp/manual-tests/oras-auth.json $STG/images/other:v1 $PROD/images/other:v1 >/dev/null'
run 'oras discover --format json --plain-http $STG/images/other:v1 | jq ".referrers | length"'
run 'oras discover --format json --plain-http $PRODAUTH $PROD/images/other:v1 | jq ".referrers | length"'
run 'git -C .tmp/manual-tests/charts add -A && git -C .tmp/manual-tests/charts commit -q -m "ship 111.0.2" && git -C .tmp/manual-tests/charts push -q origin staging'
run 'sed -i "s/version: 111.0.2+up0.17.0/version: 111.0.3+up0.17.0/; s#rancher/rancher-webhook:v0.12.1#rancher/other:v1#" .tmp/manual-tests/vehicle/fleet/Chart.yaml && sed -i "s#rancher/rancher-webhook#rancher/other#; s#v0.12.1#v1#" .tmp/manual-tests/vehicle/fleet/values.yaml'
run 'cargueiro package .tmp/manual-tests/vehicle/fleet --output-dir .tmp/manual-tests/packed'
run 'echo x | cargueiro registry login 127.0.0.1:5052 --username x --password-stdin --plain-http >/dev/null 2>&1; cargueiro push .tmp/manual-tests/packed/fleet-111.0.3+up0.17.0.tgz --registry $STG/charts && cargueiro ship $STG/charts/fleet:111.0.3+up0.17.0 --repo "$PWD/.tmp/manual-tests/charts" --plain-http; cargueiro registry logout'
run '$REL'
run 'oras discover --format json --plain-http $PRODAUTH $PROD/images/other:v1 | jq ".referrers | length"'

hdr "8. CHECK: a tag that production has is never replaced: the destination has the tag that lists what refers to an image already, with something else in it"
want "the first four lines set it up; then 'error: the signatures of 1 of 1 images could not be copied:' and, indented, that the destination has 0 signatures after the copy, as before it; exit 1. The tag has the same digest before and after, and production still has only the thing it had"
run 'N=manual-clobber-$(date +%s); HEX=$(oras manifest fetch --descriptor stgregistry.suse.com/rancher/rancher-webhook:v0.12.1 | jq -r .digest | cut -d: -f2)'
run 'oras copy --recursive --to-plain-http stgregistry.suse.com/rancher/rancher-webhook:v0.12.1 127.0.0.1:5052/rancher/$N:v1 >/dev/null && oras copy --from-plain-http --to-plain-http --to-registry-config .tmp/manual-tests/oras-auth.json 127.0.0.1:5052/rancher/$N:v1 127.0.0.1:5050/rancher/$N:v1 >/dev/null'
run 'echo something > .tmp/manual-tests/something.txt && oras attach --disable-path-validation --plain-http $PRODAUTH --artifact-type application/vnd.example.not-a-signature 127.0.0.1:5050/rancher/$N:v1 .tmp/manual-tests/something.txt:text/plain | tail -1'
run 'oras manifest fetch --descriptor --plain-http $PRODAUTH 127.0.0.1:5050/rancher/$N:sha256-$HEX | jq -r .digest'
run 'cargueiro copy-image-signatures rancher/$N:v1 --from 127.0.0.1:5052 --to 127.0.0.1:5050 --production-username admin --plain-http'
run 'oras manifest fetch --descriptor --plain-http $PRODAUTH 127.0.0.1:5050/rancher/$N:sha256-$HEX | jq -r .digest'
run 'oras discover --format json --plain-http $PRODAUTH 127.0.0.1:5050/rancher/$N:v1 | jq -r ".referrers[].artifactType"'

hdr "CLEANUP: forget the settings, take the binary off the PATH and remove the copies"
run 'unset CARGUEIRO_CONFIG_DIR CARGUEIRO_PRODUCTION_PASSWORD REL BASE STG PROD SIGNATURE PRODAUTH N HEX GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL GIT_CONFIG_GLOBAL; PATH=${PATH#"$PWD/.tmp/manual-tests/bin:"}; rm -rf .tmp/manual-tests'
unset -f hdr want run
```
