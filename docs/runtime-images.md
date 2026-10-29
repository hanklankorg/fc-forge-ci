# Runtime Container Images

forge-ci ships a family of OCI images that GitHub Actions workflows pull via
`container:`. Each image bundles the `forge-ci` binary at
`/usr/local/bin/forge-ci`, `templates/` at `/opt/forge-ci/templates/`, the
narrow bootstrap installer surface at `/opt/forge-ci/scripts/bootstrap/`, and
the toolchain a given workflow needs.

GitHub workflows and GitLab CI components use these images to select their
execution tools. An image digest pins image content for a selected platform;
it does not pin the runner environment or dependencies fetched during a job.

> **Current source versus release pins:** `v0.90.0` examples identify a historical
> baseline, not the current source revision. Confirm matching workflows, CLI
> assets and runtime images before adoption. Maintainers test unreleased changes
> with reviewed source and its published images; see [Branch Testing](#branch-testing).

## Image roster

For a configurable Forgejo-to-GHCR example, see
[Forgejo runtime publication](#forgejo-runtime-publication). The GitHub
workflow below is the upstream publication path; another forge does not execute
`.github/workflows/`.

Toolchain names identify their intended release line (e.g. `runtime-java-25`,
`runtime-node-24`). The current build targets and exact tool pins live in the
[`Containerfile`](../containers/runtime/Containerfile) and its bootstrap helpers;
the table below summarizes the shipped families.

| Image | Inherits from | Adds |
|---|---|---|
| `forge-ci-runtime-base` | `debian:13-slim` (Trixie) | `forge-ci`, `bash`, `ca-certificates`, `curl`, `git`, `gnupg`, `gzip`, `jq`, `openssh-client` (`ssh-keygen` verifies SSH-signed tags), `tar`, `unzip`, `xz`, `zip`, `yq`, `git-cliff`, `gh`, `glab`, `mise`, `publiccode-parser`, `cosign`, `templates/`, bootstrap installers |
| `forge-ci-runtime` (full) | `runtime-base` | `syft`, `trivy` |
| `forge-ci-runtime-rust-stable` | `runtime-base` | Pinned Rust stable release + `cargo-cyclonedx` |
| `forge-ci-runtime-go-1.27` | `runtime-base` | Go 1.27.3, `cyclonedx-gomod`, GCC, libc development headers and gettext |
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
    ├── runtime-go-1.27          (Go, cyclonedx-gomod, CGO/race tools, gettext)
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
| `build-xcode-ios`, `publish-apple-appstore` | macOS host + bootstrap installer from `forge-ci-binary-ref` | Tagged releases use verified archives; branch/full commit refs build from a [detached source checkout](DEVELOPMENT.md#source-checkout) |

## Forgejo runtime publication

The opt-in [`runtime-images.yml`](../examples/forgejo/runtime-images.yml) example
uses the same Containerfile and smoke checks as the GitHub publisher. Copy it
into a Forge CI mirror's `.forgejo/workflows/`, replace the `forge.example/owner`
URL and `FORGE_CI_COMMIT` with a published immutable layer revision, and configure
the settings below. Registry location does not determine the source forge.
Once installed, it runs on relevant `main` pushes, a weekly schedule and manual
dispatch from `main`. It does not run on pull requests or publish a CLI release.

### Configuration

| Setting | Purpose |
|---|---|
| `RUNTIME_IMAGE_NAMESPACE` | Reviewed GHCR destination, for example `ghcr.io/owner` |
| `CI_SIGNER_IMAGE` | Published trusted signer image pinned by digest; never a candidate |
| `CI_GO_BUILD_IMAGE` | Digest-pinned Go build image matching the release in `go.mod` |
| `CI_RUNNER_LABEL` | JSON labels for ordinary jobs and native amd64 smoke jobs |
| `CI_RUNNER_LABEL_ARM64` | Preferred ordinary native arm64 pool; when absent, smoke jobs use `CI_RUNNER_LABEL_CONTAINER_ARM64` and request fresh lent runners if lending is configured |
| `RELEASE_PLATFORMS` | Both `linux/amd64,linux/arm64` by default; optionally select just `linux/amd64` or `linux/arm64`. The existing engine planner validates the selection |
| `CI_IMAGE_BUILDER` | Existing builder selector: `buildah` or `kaniko` |
| `CI_IMAGE_BUILDER_JOB_IMAGE` | Digest-pinned kaniko job image on restricted pools |
| `CI_RUNNER_LABEL_CONTAINER`, `CI_RUNNER_LABEL_CONTAINER_ARM64` | Optional architecture-specific build pools; otherwise use the ordinary native pools |
| `CI_RUNNER_LENDER`, `CI_RUNNER_LENDER_REPOSITORY` | Optional existing runner-lending configuration; one request per native build leg |
| `RELEASE_REGISTRY_USER` | GHCR account paired with the registry token |
| `COSIGN_PUBLIC_KEY`, `COSIGN_PUBLIC_KEY_SHA256` | Organization public key and independently reviewed SHA-256 digest of its newline-terminated file |

Secrets: `RELEASE_REGISTRY_TOKEN` for GHCR writes; `RELEASE_COSIGN_PRIVATE_KEY`
and `RELEASE_COSIGN_PASSPHRASE` for the isolated signer; optionally
`CI_RUNNER_LENDER_TOKEN` for runner lending. The lender token reaches only
the requester, and signing keys reach only the signer. GHCR pull credentials
in the smoke job's `container.credentials` belong to the runner, not the
candidate's process environment. Keep the forge's automatic job token
read-default. Forks change their namespace, helper pin, signer digest and public
key digest in the self-workflow, not in the reusable layer or runtime scripts.

Private runtime packages are supported: candidate pulls and publication
verification use the registry credential. Source checkouts and the final
current-branch lookup use the forge's own job token, so the source repository
can be private too. These credentials remain separate and are not placed in
Git URLs or command arguments.

The shared planner selects each architecture separately. The runtime roster
omits Android for arm64-only runs because Android's Linux SDK is amd64-only.
The build runner is native to its leg. Buildah capability, kaniko selection
and runner lending remain independent of the platform selection. A kaniko
build does not imply that the runner can execute nested Podman tests.

### Publication sequence and evidence

1. Rebuild the CLI from the exact source commit with the Go release in
   `go.mod`, fresh module/build caches and the existing engine-binary recipe.
   Require each architecture's bytes to match its recorded checksum.
   These are the Forgejo layer's `engine-layer` binaries, identified by their
   checksums; runtime publication does not create versioned CLI release archives.
2. Build each selected runtime/platform through the existing builder adapter.
   Publish uniquely run/attempt-scoped architecture candidates to GHCR and
   transfer only their digest metadata through Forgejo artifacts.
3. Start each exact candidate digest as a native job container and run the
   shared smoke script. Its installed-CLI checksum check is diagnostic only.
   This needs no nested container engine or user namespaces.
   If ARM64 uses only a container/lender pool, request new single-use smoke
   runners after the builds; the build runners have already been consumed.
4. After **all** smoke jobs pass, pull each child digest into an OCI layout
   in the trusted utility job, extract its CLI with the existing engine
   extractor and compare its bytes with the recorded source checksum. No
   candidate executable or hashing tool participates in this independent gate.
   Scan every child digest, generate its CycloneDX SBOM and assemble the
   complete index. Vulnerability findings
   remain advisory, matching the existing GitHub runtime workflow; scanner
   failures block. Retain the reports with the native digest metadata.
5. In the pinned trusted signer, with no source checkout or tool installation,
   verify index/platform/source binding and the organization's signing key.
   Sign the index and children, attest each child's SBOM and attest the
   index's SLSA provenance. Forgejo uses the organization's key with no public
   transparency log, rather than GitHub's OIDC identity.
6. After **all** signing jobs pass, independently verify signatures and
   attestations, reject a stale `main`, and copy the unchanged signed index to
   `sha-<full-source-commit>`. Record the digest reference consumers must pin.

The final availability check reads the promoted tag with the configured
registry credential and requires its exact manifest digest to match the signed
candidate. This works for both private and public packages. Private consumers
need registry read access; for anonymous distribution, make the packages public
and additionally verify anonymous access. Visibility changes are an operator
choice, not a prerequisite for smoke testing. Candidate tags are not consumer pins.

Assembly reads each uploaded evidence bundle back into a fresh temporary
directory and verifies the file inventory and SHA-256 checksums against the
local originals. A successful upload response alone is insufficient: unreadable,
missing, changed or extra files fail assembly before signing. This adds one
evidence download per flavour; it does not repair server storage or guarantee
that the artifact remains available later.

The Forgejo artifact client opts GET/HEAD requests into bounded HTTP 500 retries
using the shared transport: at most four attempts with exponential backoff and
jitter, within the existing request timeout and delay budgets. Upload and
finalization requests are not automatically replayed. A recovered read must
still pass the same content checks; persistent errors fail the handoff with
artifact/file context. This client behavior requires a published engine
containing the retry change and a helper pin selecting that engine.

Each successful smoke job uploads a `runtime-smoke-<flavour>-<architecture>`
receipt for its exact container digest and source commit. Assembly checks these
receipts against all architecture candidates before scanning or creating an
index. Rebuilding a candidate does not reuse a green smoke result for different
bytes: rerun collection and the smoke jobs for the rebuilt digests. A receipt
is current-run workflow evidence, not a replacement for isolated signing or
independent inspection of the candidate's embedded CLI.

The final digest is authoritative: rebuilding the same source can incorporate
updated distribution packages and change `sha-<commit>`. The example does not
move upstream's rolling CLI release or manufacture semver runtime tags before
a corresponding release exists. Product releases, checksum-addressed engine
packages and trusted signer publication require their own configured workflows.
Signing/promotion is gated across the matrix, but multiple registry tag writes
are not an atomic transaction. Failed runs may leave staging images. Registry
retention is a separate reviewed operation; protect pinned digests and their
signatures/evidence when using the existing package-retention tooling.

The opt-in [`package-retention.yml`](../examples/forgejo/package-retention.yml)
example runs a weekly/manual **preview only** after being installed and configured.
Replace its forge URL and engine revision, set `RUNTIME_IMAGE_NAMESPACE`, and
provide `PACKAGE_RETENTION_MATRIX` as an `include` matrix of package names and
reviewed consumer digests.
The preview uses the pinned published engine; it needs no Forge CI source
checkout or Go compiler in the consumer repository.
Its reviewed matrix is the retention policy: release tags remain, the ten newest
commit-tagged versions remain, and each listed consumer digest remains regardless
of age, together with its platforms and signing evidence. Update that matrix
when supported consumers move pins. The preview reuses the publisher's existing
`RELEASE_REGISTRY_USER` and `RELEASE_REGISTRY_TOKEN`; that token must be able to
read package versions and manifests. The workflow never applies deletion.

After reviewing a complete pin set and the preview, an operator can apply the
same rule with the existing command:

```bash
forge-ci container package-retention --repository ghcr.io/owner/runtime \
  --keep-digest sha256:REVIEWED_CONSUMER_DIGEST --recent-count 10 --max-delete 100 \
  --registry-username PUBLISHER_USER
```

Repeat `--keep-digest` for every supported consumer pin. A missing protected
digest stops the pass. The command is dry-run by default; `--dry-run=false`
applies the reviewed rule with package-deletion credentials. Release-file and
base-image retention keep their existing separate commands and policies.

Local guard and orchestration tests establish structure, refusal paths and
platform selection. Activation still requires a successful destination-forge publication
run, native smoke results, verified GHCR signatures and consumer access with
the intended credentials (or anonymously for public distribution).
See [Forge migration review](forge-migration.md) for the cross-provider checks.

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

Treat the workflow revision, runtime image digests, plain-runner CLI ref and
artifacts schema as one reviewed selection. A newer workflow does not update a
binary baked into an older image. Obtain the schema from that same selected
source revision, as described in [the schema guide](artifacts-reference.md#json-schema).
The version labels below illustrate the selection; replace them with verified
coordinates for the revision you intend to run.

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
images being tested. Branch/full commit refs use a detached Git checkout and
`go install ./cmd/forge-ci`, without a version-suffixed module query. Both
40-character SHA-1 and 64-character SHA-256 commit IDs are accepted. Host jobs
bind the build to their bootstrap checkout; PR and snapshot orchestrators
resolve source branches once. Commit IDs avoid source-ref drift across runs.
The corresponding image labels
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

The default release policy accepts stable requests only. Consumers selecting
`release-policy: stable-prerelease` can request a complete signed beta/RC
release. Use the unsigned branch-only snapshot when no permanent release is
wanted; see [release policy](workflows.md#stable-and-prerelease-policy).

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
from a detached Git checkout with `go install ./cmd/forge-ci`; see the
[source checkout requirements](DEVELOPMENT.md#source-checkout).
