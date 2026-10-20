# Runtime Container Images

forge-ci ships a family of OCI images that GitHub Actions workflows pull via
`container:`. Each image bundles the `forge-ci` binary at
`/usr/local/bin/forge-ci`, `templates/` at `/opt/forge-ci/templates/`, the
narrow bootstrap installer surface at `/opt/forge-ci/scripts/bootstrap/`, and
the toolchain a given workflow needs.

GitHub workflows and GitLab CI components use these images to select their
execution tools. An image digest pins image content for a selected platform;
it does not pin the runner environment or dependencies fetched during a job.

> **Unpublished 0.90.0:** `@v0.90.0` and `:v0.90.0` examples below are prospective.
> Consumers wait for the tag and its images. Maintainers test unreleased
> changes with the `sha-*` images of the pre-release branch; see
> [Branch Testing](#branch-testing).

## Image roster

Toolchain names identify their intended release line (e.g. `runtime-java-25`,
`runtime-node-24`). The current build targets and exact tool pins live in the
[`Containerfile`](../containers/runtime/Containerfile) and its bootstrap helpers;
the table below summarizes the shipped families.

| Image | Inherits from | Adds |
|---|---|---|
| `forge-ci-runtime-base` | `debian:13-slim` (Trixie) | `forge-ci`, `bash`, `ca-certificates`, `curl`, `git`, `gnupg`, `gzip`, `jq`, `openssh-client` (`ssh-keygen` verifies SSH-signed tags), `tar`, `unzip`, `xz`, `zip`, `yq`, `git-cliff`, `gh`, `glab`, `mise`, `publiccode-parser`, `cosign`, `templates/`, bootstrap installers |
| `forge-ci-runtime` (full) | `runtime-base` | `syft`, `trivy` |
| `forge-ci-runtime-rust-stable` | `runtime-base` | Pinned Rust stable release + `cargo-cyclonedx` |
| `forge-ci-runtime-go-1.27` | `runtime-base` | Go 1.27 toolchain + `cyclonedx-gomod` |
| `forge-ci-runtime-java-25` | `runtime-base` | Eclipse Temurin JDK 25 LTS + Maven 3.9.x + Gradle 8.x |
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
    ├── runtime-go-1.27          (Go toolchain + cyclonedx-gomod)
    ├── runtime-java-25          (JDK 25 LTS + Maven + Gradle)
    │   └── runtime-android-35   (Android SDK platform 35, no NDK)
    └── runtime-node-24          (Node 24 LTS + npm + corepack)
```

These are internal Containerfile stages, not dependencies on separately
published runtime images. Android's `FROM java` shares the JDK version and
installation recipe. Layer reuse depends on matching platform, inputs and cache.

## Workflow → image mapping

| Workflow | Image | Notes |
|---|---|---|
| `lint-nanolinter` | nanolinter flavour image (external, `ghcr.io/itiquette/nanolinter:<flavour>`; verify publication and pin the served digest as described in [components](components.md#lint-nanolinteryml-default)) | Runs `nanolinter verify` against the consumer's `nanolinter.toml`; the PR orchestrator derives the flavour from `project-type`. The image bakes nanolinter + its check toolchain (opengrep, osv-scanner, …). forge-ci is installed at runtime, cosign-verified, for SARIF enrich/upload only |
| `lint-megalinter` | MegaLinter image (external, `oxsecurity/megalinter`) | Runs MegaLinter against the consumer's `.mega-linter.yml` on a standard runner; the alternative `lint-engine` |
| Orchestrator/stage planning and summary jobs | `runtime-base` | Run the Go CLI's config, plan and report commands; dispatched build/publish jobs select their own toolchain images |
| `release-create-github`, `release-snapshot-publish-stage`'s `generate-snapshot-sboms` | `runtime` (full) | Needs syft for SBOM aggregation |
| `generate-changelog`, `validate-release-prerequisites` | `runtime-base` normally; `runtime-rust-stable` when Cargo artifacts are present | git-cliff / gh + gpg are in base; Cargo prerequisite validation also checks `cargo --version` |
| `build-cargo`, `sbom-cargo` | `runtime-rust-stable` | Needs cargo + cargo-cyclonedx; `build-cargo` additionally relies on `rustup target add aarch64-unknown-linux-gnu` + `gcc-aarch64-linux-gnu` baked into the image for the default `linux/amd64,linux/arm64` cross-compile matrix |
| `build-go`, `sbom-go` | `runtime-go-1.27` | Needs Go + cyclonedx-gomod |
| `build-maven`, `build-gradle-app`, `publish-maven-central` | `runtime-java-25` | Needs JDK + Maven/Gradle |
| `build-gradle-android` | `runtime-android-35` | JDK + Android SDK; amd64 only |
| `publish-google-play` | `runtime-base` | Upload action is JS-only; no Android tooling needed |
| `build-npm`, `publish-snapshot-npm` | `runtime-node-24` | Node + npm + corepack baked |
| `publish-container` | host + Buildah and OCI registry adapter | The CLI builds and pushes on the host; this workflow does not use a runtime job container |
| `security-openssf-scorecard` | host + third-party Docker action | The workflow selects a hosted runner for the Scorecard action |
| `build-xcode-ios`, `publish-apple-appstore` | macOS host + bootstrap installer from `forge-ci-binary-ref` | Tagged releases use verified archives; branch/SHA refs use source installation, subject to the [current module restriction](DEVELOPMENT.md#source-checkout) |

## Why some images aren't built

- **`forge-ci-runtime-android-35-ndk`**: Not built today. NDK is ~3 GB and only needed for projects with native (JNI) code, the minority. The current `forge-ci-runtime-android-35` image covers typical Android library and app builds without NDK.
- **Multiple LTS lines simultaneously** (`runtime-java-21` *and* `runtime-java-25`, etc.): not built today.
  Adding or retaining another line requires explicit Containerfile and workflow
  matrix support. A versioned name identifies the targeted line; it does not
  establish a future maintenance or upstream-EOL commitment.

## Pinning convention

Exact versions are pinned in the Containerfile or bootstrap helper, with
Renovate updates made through source changes. The defaults below identify the
release lines, not a request to install the latest version on each rebuild.

| Toolchain | Default in image | Selection/override |
|---|---|---|
| Rust | Pinned stable release | Use a compatible `runtime-image:` override |
| Go | 1.27.x | Use a compatible `runtime-image:` override |
| JDK | Eclipse Temurin 25 LTS | Use a compatible `runtime-image:` override |
| Maven | Pinned 3.9.x | The shipped adapter runs system `mvn`, not an automatically discovered `mvnw`; override the image to change it |
| Gradle | Pinned 8.x installed | The shipped adapter runs the project's `./gradlew` wrapper |
| Node | Node 24 LTS with bundled npm and Corepack by default | GitHub `build-npm` selects an explicit `node-version` before `npm`/`npx`; stable/snapshot stages forward `config.node-version`. Empty retains the runtime image's Node. `engines` or `packageManager` metadata alone does not select a different toolchain |
| Android cmdline-tools | Pinned archive version | Extend/override the image for additional SDK requirements |
| Android build-tools | 35.0.0 | Extend/override the image for additional SDK requirements |
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

Consumers can omit `runtime-image*` inputs to use the defaults declared by the
pinned workflow. Check those values when updating workflow versions; they are
explicit configuration, not derived automatically from the caller's workflow ref.
The GitHub workflow guards check that declared runtime tags and binary-ref
defaults share a modeled version; they do not establish publication availability.

## OCI labels

Every image carries `org.opencontainers.image.{title, source, url, revision,
version, created}` (set at build time). They identify an image via `docker
inspect` or the GHCR UI even when tags have been pruned, and `source` links the
GHCR package back to this repository. `licenses` is intentionally left unset, because
a runtime image bundles many differently-licensed tools.

## CLI binary channels

Within a runtime-image workflow run, `build-cli.yml` builds the linux/darwin ×
amd64/arm64 archives and hands them to later jobs as `cli-dist`. Each image job
bakes the matching Linux architecture's binary and compares its bytes with that
same-run payload. The rolling CLI publisher uploads those archives after image
manifest publication succeeds. Stable release archives are built separately by
`release-binary.yml`; this is not one shared build across every release channel.

Container jobs use the baked binary. Plain-runner jobs (e.g. macOS swift/iOS)
select an archive with `forge-ci-binary-ref`:

| `forge-ci-binary-ref` | Channel | Use |
|---|---|---|
| `vX.Y.Z` | Release tarball, cosign-pinned to `release-binary.yml@refs/tags/release-request/vX.Y.Z` | Versioned production channel |
| `v0.90.0-pre` | Rolling pre-release from the configured prerelease branch, cosign-pinned to `build-cli.yml@refs/heads/…` | Moving test channel, not for production |

Both verify by SHA-256 + Sigstore. The pre-release channel is a separate, opt-in trust
domain and never relaxes the release identity pin. Images and CLI release assets
are published in separate steps, so their availability is not atomic.
For a release-semver ref, any asset download, checksum, or Sigstore failure terminates
the install. Source installation is used only for non-release refs or when
`FORGE_CI_USE_GO_INSTALL=1` is explicitly set.

## Advanced Pinning Recipe

After the required images have been published, use runtime overrides for digest
pinning, mirrors or branch testing. This is an input-override fragment for an
existing release caller; retain its [permissions and secret bindings](reference.md):

```yaml
# .github/workflows/release.yml in a consumer repo
jobs:
  release:
    uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@v0.90.0
    with:
      branch: main
      artifacts-config: .forge-ci/artifacts.yml
      # Optional: override every runtime image family used by the orchestrator.
      # Omit overrides to use the defaults declared by the pinned workflow.
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base:v0.90.0
      runtime-image-full: ghcr.io/diggsweden/forge-ci-runtime:v0.90.0
      runtime-image-java: ghcr.io/diggsweden/forge-ci-runtime-java-25:v0.90.0
      runtime-image-node: ghcr.io/diggsweden/forge-ci-runtime-node-24:v0.90.0
      runtime-image-rust: ghcr.io/diggsweden/forge-ci-runtime-rust-stable:v0.90.0
      runtime-image-go: ghcr.io/diggsweden/forge-ci-runtime-go-1.27:v0.90.0
      runtime-image-android: ghcr.io/diggsweden/forge-ci-runtime-android-35:v0.90.0
      forge-ci-binary-ref: v0.90.0
      # Renovate-friendly: bump the @v0.90.0, :v0.90.0, and binary ref in lockstep.
```

For direct workflow calls that take one `runtime-image:` input, choose the
matching family from the [roster](#image-roster). For example, a Java job uses:

```yaml
runtime-image: ghcr.io/diggsweden/forge-ci-runtime-java-25:v0.90.0
```

For a content pin, substitute the verified image digest:

```yaml
runtime-image: ghcr.io/diggsweden/forge-ci-runtime-java-25@sha256:<digest>
```

Moving to another toolchain line requires a reviewed workflow/default or image
override change. Pinning the workflow ref preserves its declared defaults;
pinning the image digest preserves the selected image content.

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

Renovate-driven bumps in the consumer repo + the workflow ref bump
(`uses: …@v0.90.1`) should land in the same PR for atomic version moves.

## Branch Testing

Consumers wait for the `v0.90.0` tag: its publication produces the runtime
images and the release CLI that the `@v0.90.0` pins in the examples name.
Maintainers test unreleased workflow changes with the `sha-<short-commit>`
runtime images that the `Self Runtime Container` workflow
(`.github/workflows/self-runtime-container.yml`) publishes for every push to
the configured `FORGE_CI_PRERELEASE_BRANCH` (default `main`); see
[Tag convention](#tag-convention). A workflow branch alone changes neither its
runtime-image defaults nor the CLI the plain-runner jobs install.

Testing unreleased forge-ci workflow changes from a consumer repository
requires coordinated selections:

1. A reviewed workflow commit (`uses: diggsweden/forge-ci/.github/workflows/...@<sha>`).
2. Published runtime images for that revision and the required platforms.
3. A matching CLI ref for plain-runner jobs.

For Linux containerized jobs, run `Self Runtime Container` with `publish=true`
on the configured `FORGE_CI_PRERELEASE_BRANCH` (default `main`). Manual
manifest/tag publication is gated to that branch; a dispatch on another branch
does not publish the merged tags even if per-architecture builds succeed.
Confirm the required tags/digests from the successful publication before using
them in a consumer. The fragment below uses source-revision labels for discovery;
replace them with verified digests when exact image content is required:

```yaml
# Input overrides for an existing release caller; retain its permissions/secrets.
jobs:
  release:
    uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@abcdef0123456789abcdef0123456789abcdef01
    with:
      branch: main
      artifacts-config: .forge-ci/artifacts.yml
      forge-ci-binary-ref: abcdef0123456789abcdef0123456789abcdef01
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base:sha-abcdef0
      runtime-image-full: ghcr.io/diggsweden/forge-ci-runtime:sha-abcdef0
      runtime-image-java: ghcr.io/diggsweden/forge-ci-runtime-java-25:sha-abcdef0
      runtime-image-node: ghcr.io/diggsweden/forge-ci-runtime-node-24:sha-abcdef0
      runtime-image-rust: ghcr.io/diggsweden/forge-ci-runtime-rust-stable:sha-abcdef0
      runtime-image-go: ghcr.io/diggsweden/forge-ci-runtime-go-1.27:sha-abcdef0
      runtime-image-android: ghcr.io/diggsweden/forge-ci-runtime-android-35:sha-abcdef0
```

The plain-runner jobs (container publish and macOS workflows) do not use the
runtime image. They install the binary selected by `forge-ci-binary-ref`, so
that value must point at the same forge-ci commit as the workflow and runtime
images being tested. Branch/SHA refs use source installation
(`go install …@<ref>` of a pushed commit; `go.mod` requires only published
modules). Commit SHAs avoid source-ref drift. The corresponding image labels
can still be rewritten by a rerun; a digest is the content pin.

## Testing consumer configuration

After copying an [example](../examples/README.md), check its YAML syntax:

```bash
pip install yamllint
yamllint .forge-ci/artifacts.yml
```

Syntax validation does not establish that the artifact configuration is valid
or that publication works. For a branch-only snapshot, create
`.github/workflows/release-snapshot-workflow.yml`:

```yaml
on:
  push:
    branches: [feat/test-config]

jobs:
  snapshot-release:
    uses: diggsweden/forge-ci/.github/workflows/release-snapshot-orchestrator.yml@v0.90.0
    with:
      branch: ${{ github.ref_name }}
      forge-ci-binary-ref: v0.90.0
      artifacts-config: .forge-ci/artifacts.yml
    permissions:
      actions: read
      contents: read
      packages: write
    # GitHub Packages publication uses the automatic GITHUB_TOKEN; pass
    # registry-password explicitly only for another registry.
```

The 0.90.0 references are prospective; before publication, apply the reviewed
workflow/binary/runtime-image pins from [Branch Testing](#branch-testing).
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

Production accepts stable `vMAJOR.MINOR.PATCH` requests only. Prerelease
requests such as `release-request/v0.0.1-rc.1` are rejected before release
planning or repository mutation. Use the unsigned branch-only snapshot when
no permanent release is wanted.

## Releasing new image versions

The image workflow is triggered by matching version-tag pushes, excluding the
rolling `*-pre` tag:

1. Cut a normal forge-ci release (`chore(release): vX.Y.Z`).
2. The release-orchestrator pushes the `vX.Y.Z` git tag.
3. `self-runtime-container.yml` attempts the configured runtime builds from that
   commit. Successful publication applies `:vX.Y.Z` and, for stable versions,
   the moving `:vX.Y` and `:vX` aliases.
4. Publish legs build natively for the platforms in the [roster](#image-roster),
   including the Android amd64-only exception.

This is a separate workflow run from stable CLI archive publication. Confirm
both completed and that the caller's explicit runtime/binary defaults select
the intended outputs; a shared source commit does not make publication atomic.

The path filter on the `push` trigger is intentionally absent for tag
pushes, so a matching tag schedules the image builds even if the tagged commit
didn't touch the runtime files. Prerequisite or build failures can still prevent
publication.

## Build pipeline

[`self-runtime-container.yml`](../.github/workflows/self-runtime-container.yml)
uses independent matrix legs over the internal Containerfile stages. Each target
publishes to its own image name under the repository owner's GHCR namespace
(`ghcr.io/diggsweden/` upstream). Stage inheritance determines what each build
includes; it is not a dependency on another matrix job publishing a base image.

The verify matrix builds and smoke-tests on an amd64 runner using Buildah and
Podman. Publication then builds the declared native architecture legs, pushes
by digest, and merges their digests into tagged indexes. The baked-binary check
also uses Docker to extract the CLI and compare it with the same-run archive.
The verify job is not a separate ARM64 smoke-test pass.

## GitLab compatibility building blocks

Every image lives on GHCR, which is reachable from GitLab CI runners. The GitLab
components under `templates/` use these images as their defaults. For sovereign
GitLab installations that must not pull from `ghcr.io`, mirror the images to the
consumer's own GitLab Container Registry and override the component's image
input with the mirror.

For macOS workflows, the equivalent ref input is `forge-ci-binary-ref`.
Those workflows run on a macOS VM instead of using the Linux runtime image.
Published semver tags install the cosign- and SHA-256-verified release archive;
branch/SHA refs, or an explicit `FORGE_CI_USE_GO_INSTALL=1` override, install
from source with `go install`, subject to the
[current module restriction](DEVELOPMENT.md#source-checkout).
