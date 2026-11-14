# Runtime Container Images

forge-ci ships a family of OCI images that GitHub Actions workflows pull via
`container:`. Each image bundles the `forge-ci` binary at
`/usr/local/bin/forge-ci`, `templates/` at `/opt/forge-ci/templates/`, the
narrow bootstrap installer surface at `/opt/forge-ci/scripts/bootstrap/`, and
the toolchain a given workflow needs.

GitHub workflows and GitLab CI components use these images to select their
execution tools. An image digest pins image content for a selected platform;
it does not pin the runner environment or dependencies fetched during a job.

Replace the pins in the snippets with the release you install, as
[Which version to pin](README.md#documentation-index) describes.

## Image roster

Toolchain names identify their intended release line (e.g. `runtime-java-25`,
`runtime-node-24`). The current build targets and exact tool pins live in the
[`Containerfile`](../containers/runtime/Containerfile) and its bootstrap helpers;
the table below summarizes the shipped families.

| Image | Inherits from | Adds |
|---|---|---|
| `forge-ci-runtime-base` | `debian:13-slim` (Trixie) | `forge-ci`, `bash`, `ca-certificates`, `curl`, `git`, `gnupg`, `gzip`, `jq`, `openssh-client` (`ssh-keygen` verifies SSH-signed tags), `tar`, `unzip`, `xz`, `zip`, `yq`, `git-cliff`, `gh`, `glab`, `mise`, `publiccode-parser`, `cosign`, `syft`, `templates/`, bootstrap installers |
| `forge-ci-runtime` (full) | `runtime-base` | `trivy` |
| `forge-ci-runtime-rust-stable` | `runtime-base` | Pinned Rust stable release + `cargo-cyclonedx` |
| `forge-ci-runtime-go-1.27` | `runtime-base` | Go 1.27.4, `cyclonedx-gomod`, GCC, libc development headers and gettext |
| `forge-ci-runtime-java-25` | `runtime-base` | Eclipse Temurin JDK 25 LTS + Maven 3.10.x + Gradle 8.x |
| `forge-ci-runtime-android-35` | `runtime-java-25` | Android cmdline-tools + platform-tools + build-tools 35 + android-35, no NDK |
| `forge-ci-runtime-node-24` | `runtime-base` | Node 24 LTS + npm + corepack |

The current publication matrix targets `linux/amd64` and `linux/arm64` for these
families, except **Android, which is `linux/amd64` only** because the bundled
Linux SDK tools are x86_64 binaries. Select a matching runner platform.

## Layering

```text
debian:13-slim
└── runtime-base
    ├── runtime                  (security/SBOM tools)
    ├── runtime-rust-stable      (Rust toolchain)
    ├── runtime-go-1.27          (Go, cyclonedx-gomod, CGO/race tools, gettext)
    ├── runtime-java-25          (JDK 25 LTS + Maven + Gradle)
    │   └── runtime-android-35   (Android SDK platform 35, no NDK)
    └── runtime-node-24          (Node 24 LTS + npm + corepack)
```

These are internal Containerfile stages, not dependencies on separately
published runtime images. Android's `FROM java` shares the JDK version and
installation recipe. Layer reuse depends on matching platform, inputs and cache.
Two further stages, `go-toolchain` (the Go release, taken from the official
`golang` image pinned by tag and digest) and `go-tools` (yq and
publiccode-parser compiled with it), only feed `runtime-base` and
`runtime-go-1.27` and are not published.

## Users and caches

Every image runs as root by default: GitHub `container:` jobs and kaniko need
it. Each image also has the ordinary user `nonroot`, UID and GID 65532, home
`/home/nonroot`, for jobs whose tests need ordinary-user filesystem semantics;
on GitLab the check component's `job-user` and the release policy's
`job-user` select it. The toolchains under `/usr/local` and `/opt` are
read-only for that user, and every cache lives in the home of the user a job
runs as, at each tool's default: `~/.cargo` (no `CARGO_HOME` is set),
`~/go` (no `GOPATH`), `~/.cache/go-build`, `~/.m2`, `~/.gradle` and `~/.npm`.
The images set no `HOME`; the container runtime takes it from `/etc/passwd`,
and GitHub's `container:` jobs set `/github/home`.

What a job installs itself goes to `/opt/job-tools/bin`, on `PATH` ahead of
`/usr/local/bin` and writable for root and `nonroot`: `go install` through
`GOBIN`, `cargo install` through `CARGO_INSTALL_ROOT` and `npm install -g`
through `NPM_CONFIG_PREFIX`. A fixed directory rather than `~/go/bin` and
`~/.cargo/bin` keeps the command found whatever `HOME` the job has. Other UIDs
cannot write it. With `GOBIN` set, `go install` refuses to cross-compile; use
`go build -o`. The smoke test of each image builds and installs with its
toolchain as `nonroot`.

Two toolchains install more of themselves on demand, so their homes belong to
`nonroot` as well: rustup's `/usr/local/rustup` (`RUSTUP_HOME`) and the
Android SDK's `/opt/android-sdk` (`ANDROID_HOME`). The toolchain, components
and targets a `rust-toolchain.toml` pins, `rustup component add`, `rustup
target add`, `sdkmanager --install` and the Android Gradle Plugin's download
of a missing platform or build-tools work for root and `nonroot`; other UIDs
cannot write them. Corepack is enabled in the Node image, with its `yarn` and
`pnpm` shims in the read-only `/usr/local/bin`, so a job need not run
`corepack enable`; one that wants shims of its own runs `corepack enable
--install-directory /opt/job-tools/bin`. The package managers Corepack fetches
go to the job's home.

The Go, Cargo and Gradle workflows ask the image where its caches are (`go env
GOMODCACHE GOCACHE`, `CARGO_HOME`, `GRADLE_USER_HOME`, each defaulting under
`HOME`) before caching them, so a `runtime-image` older or newer than the
workflow still restores its caches. Maven's `~/.m2/repository` is Maven's
fixed default.

GitHub workflows and GitLab components take every runtime image as an input
pinned by digest (`image@sha256:<64 hex>`), with no default, and refuse a tag
before the job starts.

## Workflow → image mapping

| Workflow | Image | Notes |
|---|---|---|
| `lint-nanolinter` | nanolinter flavour image (external, `ghcr.io/itiquette/nanolinter:<flavour>`; verify publication and pin the served digest as described in [components](components.md#lint-nanolinteryml-default)) | Runs `nanolinter verify` against the consumer's `nanolinter.toml`, in the `nanolinter-image` the caller pins by digest. The image bakes nanolinter + its check toolchain (opengrep, osv-scanner, …). forge-ci is the canonical CLI of the run, received before the checkout (`consume-canonical-cli`, profile `host`), for the checkout, policy and SARIF commands |
| `lint-megalinter` | MegaLinter image (external, `oxsecurity/megalinter`) | Runs MegaLinter against the consumer's `.mega-linter.yml` on a standard runner; the alternative `lint-engine`. forge-ci is the canonical CLI of the run, received before the checkout, for the SARIF commands |
| Orchestrator/stage planning and summary jobs | `runtime-base` | Run the Go CLI's config, plan and report commands; dispatched build/publish jobs select their own toolchain images |
| `release-create-github`, `release-snapshot-publish-stage`'s `generate-snapshot-sboms` | `runtime` (full) | Needs syft for SBOM aggregation |
| `generate-changelog`, `validate-release-prerequisites` | `runtime-base` normally; `runtime-rust-stable` when Cargo artifacts are present | git-cliff / gh + gpg are in base; Cargo prerequisite validation also checks `cargo --version` |
| `build-cargo`, `sbom-cargo` | `runtime-rust-stable` | Needs cargo + cargo-cyclonedx; `build-cargo` additionally relies on `rustup target add aarch64-unknown-linux-gnu` + `gcc-aarch64-linux-gnu` baked into the image for the default `linux/amd64,linux/arm64` cross-compile matrix |
| `build-go`, `sbom-go` | `runtime-go-1.27` | Needs Go + cyclonedx-gomod |
| `build-maven`, `build-gradle-app`, `publish-maven-central` | `runtime-java-25` | Needs JDK + Maven/Gradle |
| `build-gradle-android` | `runtime-android-35` | JDK + Android SDK; amd64 only |
| `publish-google-play` | `runtime-base` | Upload action is JS-only; no Android tooling needed |
| `build-npm`, `publish-snapshot-npm` | `runtime-node-24` | Node + npm + corepack baked |
| `publish-maven-github` | `runtime-java-25` for maven, gradle and gradle-android; `runtime-node-24` for npm | The caller selects the image by package type; the publication runs with the image's JDK/Maven/Gradle or Node |
| `publish-container` | `runtime-base` (prep, publish-images, merge: cosign baked), `runtime` full (scan-layouts: Trivy; image-evidence: syft and cosign); host build legs | Buildah needs the host, so each build leg is a host job that receives the canonical CLI built in the run from the approved engine commit (`consume-canonical-cli`, profile `host`); the other jobs run in the admitted digests and install nothing |
| `snapshot-images` | `runtime-base` (plan, prepare-context, collect), `runtime` full (publish: Trivy and syft), the toolchain image of each producer's project type; host build legs | The same shape as `publish-container`: the Buildah legs receive the canonical CLI of the run (`consume-canonical-cli`, profile `host`), every other job runs the forge-ci baked into its image |
| `security-openssf-scorecard` | host + third-party Docker action (analysis); `runtime-base` (publish-sarif) | The Scorecard action needs a hosted runner; the SARIF upload runs in the admitted base image with its baked forge-ci |
| `slsa-attestor` | `runtime-base` | forge-ci and the pinned cosign are baked in; the caller-selected runner must run job containers |
| `lint-swift`, `build-xcode-ios`, `publish-apple-appstore` | macOS host; forge-ci built from the engine checkout | Canonical delivery builds Linux files in a Linux builder image with Linux tool profiles, so it serves no darwin job. Each macOS job checks the engine out at the full commit `forge-ci-binary-ref` names and runs `.github/actions/build-darwin-cli` from that checkout: the Go toolchain `go.mod` names through setup-go, then `go build ./cmd/forge-ci`. Nothing else is installed |

## Forgejo runtime publication

[`examples/forgejo/runtime-images.yml`](../examples/forgejo/runtime-images.yml)
publishes the runtime images from a Forge CI mirror on Forgejo, with the same
Containerfile and smoke checks as the GitHub publisher. It is opt-in: copy it
into the mirror's `.forgejo/workflows/`, and it runs on pushes to `main` that
touch the image sources, weekly and by manual dispatch. It does not run on
pull requests and publishes no CLI release.

Static tests check the workflow, but no live test has run it yet, so keep a
production publisher and its published pins until one has.

### Configuration

Replace the placeholders in the file: `OWNER` and `FORGE_CI_COMMIT` with the
mirror and its pinned commit, `REGISTRY` with the registry that holds the
signer and Go build images, `PIN_SIGNER_DIGEST` with the signer image,
`PIN_GO_IMAGE_DIGEST` with a Go build image matching the Go release in
`go.mod`, and `PIN_COSIGN_PUBLIC_KEY_SHA256` with the reviewed SHA-256 of the
organization's public key file. Then set:

| Setting | Purpose |
|---|---|
| `RUNTIME_IMAGE_NAMESPACE` | Destination, for example `ghcr.io/owner` |
| `CI_RUNNER_LABEL` | JSON labels for ordinary jobs, the signer and the amd64 smoke test |
| `CI_RUNNER_LABEL_ARM64` | The arm64 pool for the arm64 smoke test; without it the smoke test runs on `CI_RUNNER_LABEL_CONTAINER_ARM64` |
| `CI_RUNNER_LABEL_CONTAINER`, `CI_RUNNER_LABEL_CONTAINER_ARM64` | Optional build runners per architecture; otherwise the pools build |
| `RELEASE_PLATFORMS` | `linux/amd64,linux/arm64` by default, or one of them; Android is left out of an arm64-only run |
| `CI_IMAGE_BUILDER`, `CI_IMAGE_BUILDER_JOB_IMAGE` | `kaniko` and its job image on a pool without user namespaces; see [runners](runners.md#building-on-the-pool-with-kaniko) |
| `CI_RUNNER_LENDER`, `CI_RUNNER_LENDER_REPOSITORY` | Optional [runner lending](runners.md#runner-lending), one request per build leg |
| `RELEASE_REGISTRY_USER` | The registry account paired with the registry token |
| `COSIGN_PUBLIC_KEY` | The organization's public key |

Secrets: `RELEASE_REGISTRY_TOKEN` for registry writes,
`RELEASE_COSIGN_PRIVATE_KEY` and `RELEASE_COSIGN_PASSPHRASE` for the signer,
and optionally `CI_RUNNER_LENDER_TOKEN`. The signing key reaches only the
signer job. Private packages and a private source repository both work: the
registry token pulls the candidates, the forge's job token reads the source.

### What a run does

1. Rebuilds the CLI from the source commit and requires its bytes to match the
   recorded checksum.
2. Builds each runtime image per architecture natively and pushes it as a
   candidate.
3. Runs the smoke script in each candidate as a native job container.
4. After all smoke tests pass, extracts the CLI from each candidate and
   compares it with the recorded checksum, scans each image, generates its
   SBOM and assembles the index. Findings are reported; with the workflow's
   `enforce-vulnerabilities: true` a `CRITICAL` finding not approved in
   `.trivyignore.yaml` blocks, and a scanner failure always does.
5. Signs the index and its children with the organization's key, with no
   transparency log, and attests each SBOM and the SLSA provenance.
6. Verifies the signatures, refuses a `main` that has moved on, and tags the
   signed index `sha-<full-source-commit>`. Pin its digest.

A rebuild of the same commit can give a new digest, because distribution
packages move on. A failed run may leave candidate images behind; see
[GHCR package retention previews](forgejo.md#ghcr-package-retention-previews).

## Pinning convention

Exact versions are pinned in the Containerfile or bootstrap helper, with
Renovate updates made through source changes. The defaults below identify the
release lines, not a request to install the latest version on each rebuild.
`yq` and `publiccode-parser` are built from source with the image's Go
release, so a Go security fix reaches them with the next Go bump.

| Toolchain | Default in image | Selection/override |
|---|---|---|
| Rust | Pinned stable release | Use a compatible `runtime-image:` override |
| Go | 1.27.x | Use a compatible `runtime-image:` override |
| JDK | Eclipse Temurin 25 LTS | Use a compatible `runtime-image:` override |
| Maven | Pinned 3.x release, currently 3.10.x | The shipped adapter runs system `mvn`, not an automatically discovered `mvnw`; override the image to change it |
| Gradle | Pinned 8.x installed | The shipped adapter runs the project's `./gradlew` wrapper |
| Node | Node 24 LTS with bundled npm and Corepack by default | GitHub `build-npm` selects an explicit `node-version` before `npm`/`npx`; stable/snapshot stages forward `config.node-version`. Empty retains the runtime image's Node. `engines` or `packageManager` metadata alone does not select a different toolchain |
| Android cmdline-tools | Pinned archive version | Extend/override the image for additional SDK requirements |
| Android build-tools | 35.0.1 | Extend/override the image for additional SDK requirements |
| Android SDK platform | android-35 | Extend/override the image for additional SDK requirements |

A reusable workflow's runtime is selected inside its jobs. Tool setup performed
in a different caller job does not replace that runtime. Explicit setup actions
can be used in a job you own; otherwise select a compatible image containing
the required tools.

## Tag convention

The publication workflow emits the following tags when their event gates apply:

| Tag | When | Pinning scope |
|---|---|---|
| `:vX.Y.Z` | Successful version-tag publication | Version label; tag immutability is not enforced by the generic manifest writer |
| `:vX.Y` | Stable version-tag publication | Moving alias shared by patch releases |
| `:vX` | Stable version-tag publication | Moving alias shared by minor/patch releases |
| `:<branch>` | Eligible branch-context publication (e.g. `:main`) | Moving branch alias |
| `:v0.90.0-pre` | Eligible prerelease branch/manual publication | Moving prerelease alias |
| `:v0.90.0-pre-<YYYYMMDD-HHMMSS>` | Prerelease publication, using the commit timestamp | Commit-time label; reruns reuse it |
| `:weekly` | Successful scheduled publication | Moving scheduled-build alias |
| `:sha-<short-commit>` | Eligible non-scheduled branch/manual publication | Source-revision label; reruns can write the same tag |
| `@sha256:<digest>` | Per published image/index | Content pin; select the intended platform |

The source commit or timestamp alone does not fix every build input. Rebuilds
can resolve different packages or tool/build environments; a scheduled rebuild
does not itself update committed version pins. Use a verified image digest when
the same image bytes are required.

The workflows declare no default `runtime-image*` value, so the caller passes
each image by digest. Update those digests when you update the workflow ref;
they are not derived from it.

## OCI labels

Every image carries `org.opencontainers.image.{title, source, url, revision,
version, created}` (set at build time). They identify an image via `docker
inspect` or the GHCR UI even when tags have been pruned, and `source` links the
GHCR package back to this repository. `licenses` is intentionally left unset, because
a runtime image bundles many differently-licensed tools.

## CLI binary channels

Jobs never install a published CLI archive. Container jobs use the binary
baked into their image, Linux plain-runner jobs receive the CLI their run
builds from `forge-ci-binary-ref` ([engine delivery](engine-delivery.md)),
and macOS jobs build it from a checkout of that commit. The archives are for
people who download the CLI:

| Channel | Signed by | Use |
|---|---|---|
| `vX.Y.Z` release tarball | `release-binary.yml@refs/tags/release-request/vX.Y.Z` | Versioned production channel |
| `v0.90.0-pre` rolling pre-release from the configured prerelease branch | `build-cli.yml@refs/heads/…` | Moving test channel, not for production |

Both verify by SHA-256 and Sigstore. The pre-release channel has its own
signing identity and never relaxes the release identity pin. Images and CLI
archives are published in separate steps, so one can exist without the other.
There is no darwin/amd64 CLI.

## Advanced Pinning Recipe

After the required images have been published, pin a release caller. The
release orchestrator requires three of its inputs as exact IDs and admits them
in its first job, before any image starts: `forge-ci-binary-ref` is the full
engine commit the run builds its canonical CLI from, and `runtime-image` and
`canonical-builder-image` are `@sha256:` digests (a tag is refused). Every
other `runtime-image*` input is a digest too, admitted by the workflow that
runs it before the job starts; pass the ones the release's artifacts use. This
is an input fragment for an existing release caller; retain its
[permissions and secret bindings](reference.md):

Treat the workflow revision, runtime image digests, engine commit and
artifacts schema as one reviewed selection. A newer workflow does not update a
binary baked into an older image. Obtain the schema from that same selected
source revision, as described in [the schema guide](artifacts-reference.md#json-schema).
The placeholders below illustrate the selection; replace them with verified
coordinates for the revision you intend to run.

```yaml
# .github/workflows/release.yml in a consumer repo
jobs:
  release:
    uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@<full-commit> # vX.Y.Z
    with:
      branch: main
      artifacts-config: .forge-ci/artifacts.yml
      # The same full commit the workflow ref names: required.
      forge-ci-binary-ref: <full-commit>
      # Admitted by digest before any job runs: required.
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:<digest>
      canonical-builder-image: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:<digest>
      # Release assembly runs in the full image on every native release.
      runtime-image-full: ghcr.io/diggsweden/forge-ci-runtime@sha256:<digest>
      # The family image of each artifact type the release builds; leave out
      # the others.
      runtime-image-java: ghcr.io/diggsweden/forge-ci-runtime-java-25@sha256:<digest>
      runtime-image-node: ghcr.io/diggsweden/forge-ci-runtime-node-24@sha256:<digest>
      runtime-image-rust: ghcr.io/diggsweden/forge-ci-runtime-rust-stable@sha256:<digest>
      runtime-image-go: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:<digest>
      runtime-image-android: ghcr.io/diggsweden/forge-ci-runtime-android-35@sha256:<digest>
```

Read a published tag's digest with the registry client you have, for example
`docker buildx imagetools inspect ghcr.io/diggsweden/forge-ci-runtime-base:vX.Y.Z --format '{{.Manifest.Digest}}'`,
and the tag's commit with `git rev-parse vX.Y.Z^{commit}` in a forge-ci
checkout. Self Release pins the same way: it reads this commit's image digests
once the images exist and passes `github.sha` as the engine commit.

A workflow called on its own takes one `runtime-image:` input: the digest of
the matching family from the [roster](#image-roster), with the same
`forge-ci-binary-ref`. For example, a Java job passes:

```yaml
forge-ci-binary-ref: <full-commit>
runtime-image: ghcr.io/diggsweden/forge-ci-runtime-java-25@sha256:<digest>
```

Moving to another toolchain line means passing another image digest; the
workflow ref, the engine commit and the image digests move together.

### Renovate

If you override `runtime-image:` strings, they are Renovate-friendly. Consumers
using Renovate can pick up image bumps automatically. Example `renovate.json`:

```json
{
  "packageRules": [
    {
      "matchDatasources": ["docker"],
      "matchPackagePatterns": ["forge-ci-runtime"],
      "groupName": "forge-ci runtime images"
    }
  ]
}
```

Move the workflow commit, `forge-ci-binary-ref` and the image digests in the
same pull request.

## Branch Testing

Consumers pin a release. Maintainers test unreleased workflow changes with the
`sha-<short-commit>` runtime images that `Self Runtime Container`
(`.github/workflows/self-runtime-container.yml`) publishes for every push to
the configured `FORGE_CI_PRERELEASE_BRANCH` (default `main`), or when it is
dispatched there with `publish=true`; see [Tag convention](#tag-convention).
A workflow branch alone changes neither the images its caller pins nor the
commit the run builds its CLI from. A test from a consumer repository needs
three matching selections:

1. A reviewed workflow commit (`uses: diggsweden/forge-ci/.github/workflows/...@<sha>`).
2. The runtime images published for that commit, by digest.
3. The same full commit as `forge-ci-binary-ref`.

Read the digest each tag serves:

```bash
docker buildx imagetools inspect ghcr.io/diggsweden/forge-ci-runtime-base:sha-abcdef0 --format '{{.Manifest.Digest}}'
```

Then pass them as in the [Advanced Pinning Recipe](#advanced-pinning-recipe),
with that commit as the workflow ref and `forge-ci-binary-ref`. A rerun can
move a `sha-<short>` tag; the digest is the content pin.

## Testing consumer configuration

After copying an [example](../examples/README.md), validate the configuration
against the schema:

```bash
forge-ci config validate
```

That does not prove publication works. For a branch-only snapshot, create
`.github/workflows/release-snapshot-workflow.yml`:

```yaml
on:
  push:
    branches: [feat/test-config]

jobs:
  snapshot-release:
    uses: diggsweden/forge-ci/.github/workflows/release-snapshot-orchestrator.yml@PIN_FORGE_CI_SHA # v0.90.0
    with:
      branch: ${{ github.ref_name }}
      # The full commit the workflow ref above names; the snapshot route runs
      # the CLI built in this run from exactly this commit.
      forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
      # Pinned by digest and admitted before any job runs; replace FIXME with
      # the digests of the release you call (docs/runtime-images.md).
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      canonical-builder-image: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      runtime-image-full: ghcr.io/diggsweden/forge-ci-runtime@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      # Add the family image of each artifact type the snapshot builds, such
      # as runtime-image-java for Maven.
      artifacts-config: .forge-ci/artifacts.yml
    permissions:
      actions: read
      contents: read
      packages: write
    # GitHub Packages publication uses the automatic GITHUB_TOKEN; pass
    # registry-password explicitly only for another registry.
```

Push the selected test branch to trigger that configured snapshot workflow:

```bash
git checkout -b feat/test-config
git push origin feat/test-config
```

For the full signed release path, use a disposable stable version:

```bash
git tag -s release-request/v0.0.1 -m "Test configuration"
git push origin release-request/v0.0.1
```

The default release policy accepts stable requests only. Consumers selecting
`release-policy: stable-prerelease` can request a complete signed beta/RC
release. Use the unsigned branch-only snapshot when no permanent release is
wanted; see [release policy](workflows.md#stable-and-prerelease-policy).

## Releasing new image versions

A forge-ci release tag `vX.Y.Z` starts `self-runtime-container.yml`, which
builds the images from that commit natively for the platforms in the
[roster](#image-roster) and tags them `:vX.Y.Z`, and for a stable version
also `:vX.Y` and `:vX`. The tag push builds the images even when the tagged
commit did not touch the runtime files. The CLI archives are published by a
separate run, so check that both completed.

## GitLab compatibility building blocks

Every image lives on GHCR, which is reachable from GitLab CI runners. The GitLab
components under `templates/` have no default image: the caller passes one by
digest. For sovereign GitLab installations that must not pull from `ghcr.io`,
mirror the images to the consumer's own GitLab Container Registry and pass the
mirror's digest instead.
