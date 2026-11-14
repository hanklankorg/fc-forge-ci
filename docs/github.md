# GitHub

How `forge-ci` is used on GitHub: the reusable workflows under
`.github/workflows/` that check a pull request, build, publish, sign and release,
and the runtime images and CLI they run.

Replace the pins in the snippets with the release you install, as
[Which version to pin](README.md#documentation-index) describes. What each
forge layer has beside the others is in
[Delivery layers side by side](providers.md#delivery-layers-side-by-side).

## What ships

| Path | Content |
|---|---|
| `.github/workflows/` | Three orchestrators a consumer calls, the stages they compose, and the build, publish, lint, security and release leaves. [Components Reference](components.md#workflow-inventory) is the complete inventory. |
| `.github/actions/consume-canonical-cli`, `.github/actions/build-darwin-cli` | How a job that is not a runtime container gets the engine: Linux host jobs receive the canonical CLI built in the run from the pinned engine commit; macOS jobs build it from the engine checkout. See [Installing `forge-ci`](providers.md#installing-forge-ci). |
| `containers/runtime/Containerfile` | The runtime images the workflow jobs run in, published on `ghcr.io`. See [Runtime Images](runtime-images.md). |
| `examples/` | A pull-request, release and, where it applies, snapshot workflow per ecosystem under `<ecosystem>/github/`, and the signing backends. See [Examples](../examples/README.md). |

The orchestrators are the supported entry points:

| Orchestrator | Trigger | Does |
|---|---|---|
| `pullrequest-orchestrator.yml` | `pull_request`, push | The lint gate (nanolinter or MegaLinter) and Swift checks, with findings in Code Scanning. Tests stay in the caller's own workflow. |
| `release-orchestrator.yml` | A signed `release-request/vX.Y.Z` tag | Validates the request and prerequisites, prepares the signed version bump and final tag, builds every declared artifact, publishes it, builds and signs containers, and creates the GitHub release. |
| `release-snapshot-orchestrator.yml` | Branch pushes or manual selection | Package snapshots and default-on declared snapshot containers; no OCI signing or release. |

Leaf workflows can be called directly to compose your own pipeline; see
[Two ways to call it](components.md#two-ways-to-call-it). GitHub resolves a nested
`uses: ./.github/workflows/...` from the commit of the calling workflow, so the
orchestrators compose stages and leaves; [Workflow Architecture](workflows.md)
describes how.

## Requirements

**Organization prerequisites:** allow the reviewed reusable workflows and their
runtime images, and provide the runners below.

**Per repository:** select the example, review its build paths and names, add
your own test job, then configure release secrets and protected tags. A green
lint job alone does not establish that your project tests passed.

| Requirement | Value |
|---|---|
| Forge | **github.com.** The release workflows pin `actions/upload-artifact@v7`, which GitHub Enterprise Server does not support, so `release-orchestrator.yml` stops with exit 78 in its first preflight there. The CLI's own release API commands work on GHES. |
| Runners | GitHub-hosted by default: `ubuntu-26.04`, `ubuntu-26.04-arm` for the arm64 leg of an image build, `macos-26` for Xcode and Swift jobs. The orchestrators take `runs-on`, `build-runs-on-linux-amd64`, `build-runs-on-linux-arm64` and `runs-on-darwin-arm64` to choose others; signing, promotion and publication jobs stay on GitHub-hosted runners. See [GitHub runners](runners.md#github-runners). |
| Runtime images | Readable from `ghcr.io`, or mirrored and selected with the `runtime-image*` inputs. See [Images and the engine binary](#images-and-the-engine-binary). |
| Caller permissions | The `permissions:` each orchestrator declares; see the [permission matrix](reference.md#permission-requirements-matrix). |
| Release authorisation | Committed `.forge-ci/allowed_signers` (SSH) or `.forge-ci/allowed_gpg_keys.asc` (GPG) when allowlisting is on; see [Release Authorisation](verification.md#release-authorisation). |
| Default branch | Any; the release caller names it with the `branch` input. |

A single-manifest repository (one root `go.mod`, `Cargo.toml`, `pom.xml`,
`package.json` or Gradle build) needs no configuration file: the plan is
derived from the manifest. Add `.forge-ci/artifacts.yml` for anything more;
see the [Artifacts Reference](artifacts-reference.md).

## Getting started

`forge-ci init https://github.com/diggsweden/forge-ci` in the repository does
the CI-file setup for the example its build files imply (`--example` picks
another): it writes the workflows and `.forge-ci/artifacts.yml`, pinned to
the newest release, and never overwrites a file. It writes no Nanolinter or
MegaLinter configuration: without one, the lint engine runs with its defaults
and Nanolinter's required-check floor. Review the generated values before
running; `doctor` checks local configuration, not remote secrets or runners.
`--dry-run` previews the files without writing them and lists every conflict
with an existing file.

Then run `forge-ci setup status`. It lists the steps from here to a first
release, marks the ones the repository shows are done, and names the command
for each: `doctor` for the values left to fill in, `setup signer` for the
allowlist, `setup secrets` for the secrets, and `setup release-request` for the
signed request tag. It works the same on files copied by hand.

By hand:

1. **Pick an example** from [`examples/`](../examples/README.md) for your
   ecosystem, and a signing backend from
   [`examples/signing/`](../examples/signing/).
2. **Copy** its `github/pullrequest-workflow.yml` and
   `github/release-workflow.yml` (and `github/release-snapshot-workflow.yml`
   if you want branch snapshots) into `.github/workflows/`, and its
   `artifacts.yml` into `.forge-ci/` when you need one. Optionally copy the
   example's `nanolinter.toml` when you want custom lint policy. The lint gate
   runs `nanolinter verify`, which discovers root `nanolinter.toml`, then
   `development/nanolinter.toml`, then uses defaults. It requires at least the `required-lints`
   floor, default `secrets,sast`.
3. **Pin the references.** Replace the examples' `PIN_FORGE_CI_SHA` and
   `v0.90.0` with the commit and tag of the release you install (the commit
   is `git rev-parse vX.Y.Z^{commit}`, not the tag object):
   `forge-ci init` does this for you. The `uses: ...@<commit>` ref,
   `forge-ci-binary-ref` (the same full commit), the `runtime-image` and
   `canonical-builder-image` digests and any other `runtime-image*` override
   move together; see
   [Advanced Pinning Recipe](runtime-images.md#advanced-pinning-recipe).
4. **Set the [secrets](#secrets-and-tokens)** your release plan selects
   (`forge-ci setup secrets` lists them), and
   [protect the release tags](#protect-the-release-tags).
5. **Commit the release allowlist** your signing policy needs
   (`forge-ci setup signer` writes its lines); see
   [Release Authorisation](verification.md#release-authorisation).
6. **Release** by pushing a signed request tag. First check your signing
   setup; the command prints the two git commands below once it passes:

   ```bash
   forge-ci setup release-request v1.0.0
   git tag -s release-request/v1.0.0 -m "Release v1.0.0"
   git push origin release-request/v1.0.0
   ```

   The flow creates the final `v1.0.0` tag once, at the prepared branch head.
   Never create, move or force-push the final tag yourself.

   `git tag -s` signs with your own key, the one `user.signingkey` names on
   your machine: an SSH key with `gpg.format ssh`, or a GPG key. That key is
   what the committed allowlist must list, `.forge-ci/allowed_signers` for
   SSH or `.forge-ci/allowed_gpg_keys.asc` for GPG, with the principal you
   sign as. It is distinct from the bot's `RELEASE_*` secrets, which sign the
   release commit, the final tag and the artifacts after the request is
   admitted. `forge-ci setup release-request` names the step that is
   missing: the git signing settings, or the allowlist line, which
   `forge-ci setup signer` adds.

   If a release fails partway, follow
   [Failed Release Recovery](failed-release-recovery.md).

The [examples](../examples/README.md) show the caller workflows in full; the
snapshot caller is under [Snapshots](#snapshots).

## Inputs and variables

Each workflow's `on.workflow_call.inputs` block is its reference: every input
carries a description. The orchestrator inputs most callers set:

| Input | Workflow | Meaning |
|---|---|---|
| `project-type` | PR | `maven`, `npm`, `gradle`, `gradle-android`, `xcode-ios`, `cargo` or `go`; selects the nanolinter flavour. |
| `lint-engine` | PR | `nanolinter` (default), `megalinter` or `none`. |
| `required-lints` | PR | The mandated nanolinter floor, default `secrets,sast`. |
| `disable-checks` | PR | Nanolinter check ids to leave out of the run, comma-separated; empty by default. It cannot remove a check `required-lints` names. Name here each check the job cannot run, for example a scan another job owns; see [check enforcement](admin-policy.md). |
| `nanolinter-image`, `megalinter-image` | PR | The lint engine's image, pinned by digest: `nanolinter-image` is required with `lint-engine: nanolinter` and `megalinter-image` with `megalinter`. Neither has a default; pin your flavour as the examples show. |
| `branch` | release, snapshot | The branch to prepare and release from, or the snapshot branch. |
| `release-policy` | release | `stable` (default) or `stable-prerelease`; the signed tag determines classification. |
| `artifacts-config` | release, snapshot | Path to `artifacts.yml`, default `.forge-ci/artifacts.yml`. |
| `release-publisher` | release | `github-cli` creates the GitHub release; empty skips it. |
| `changelog-creator`, `changelog.config` | release | `git-cliff` (the default) or empty, which skips the changelog and the version bump; any other value is refused. git-cliff runs offline, so remote metadata such as pull-request numbers is not available, and it reads only stable `vX.Y.Z` tags. |
| `release.sboms`, `release.signartifacts`, `release.requireallowlistedsigner`, `release.draft` | release | SBOM selection, artifact signing, the signer allowlist and draft releases. |
| `release-asset-retention` | release | How many of the newest releases keep every file; older ones keep their checksums and signatures. Empty, the default, deletes nothing. See [Old releases' files](#old-releases-files). |
| `promote-staging-environment`, `promote-release-environment`, `promote-release-*` | release | The GitHub Environments that gate the promotion ladder, and an optional separate release registry. |
| `enforce-lint`, `enforce-vulnerabilities`, `enforce-application-tests`, `enforce-reproducible-builds` | PR, release, snapshot | [Check enforcement](admin-policy.md), default `false`, forwarded to every gate. Checks run and report findings; explicit `true` makes recognized findings block. Organisation-mandated values belong in its ruleset-required workflow. No workflow reads them from `vars` or the environment. |
| `forge-ci-binary-ref` | all | Required: the full engine commit the canonical CLI is built from in the run, for every plain-runner job of the PR, snapshot and release routes. The macOS lanes build the engine from a checkout of the same commit. |
| `runtime-image*` | all | The runtime image of each toolchain family. `runtime-image` is required on every orchestrator and admitted only as an `@sha256:` digest; it is the control-plane, release-notes, SARIF-upload and cosign signing container. |
| `canonical-builder-image` | all | Required: the Go runtime image digest the canonical CLI is built in. |
| `runs-on`, `runs-on-darwin-arm64` | PR, release, snapshot | JSON arrays of runner labels for the ordinary jobs, default `["ubuntu-26.04"]`, and the unsigned Xcode and the Swift jobs, default `["macos-26"]`; signed Android and Xcode builds stay on hosted runners. See [GitHub runners](runners.md#github-runners). |
| `build-runs-on-linux-amd64`, `build-runs-on-linux-arm64` | release, snapshot | JSON arrays of runner labels for the image builds per platform, default `["ubuntu-26.04"]` and `["ubuntu-26.04-arm"]`. Signing, promotion and publication jobs stay on GitHub-hosted runners. |

Configuration of what is built, published and signed lives in
`.forge-ci/artifacts.yml`, not in inputs; see the
[Artifacts Reference](artifacts-reference.md). Direct-call leaves are in
[Components Reference](components.md).

**Repository variables.** The workflows read these from the calling
repository; each falls back to the default below.

| Variable | Default | Used for |
|---|---|---|
| `FORGE_CI_REPOSITORY` | the repository serving the called workflow (`job.workflow_repository`) | the engine checkouts and images every job trusts, release URLs and trusted signing identities. GitHub Enterprise Server has no such context, so there it is required |
| `RELEASE_PLATFORMS` | what the configuration declares | narrows the platforms the release's images are built for; an undeclared, duplicated or malformed entry fails before anything is built |
| `CI_IMAGE_BUILDER` | `buildah` | the Linux container and snapshot image builder, `buildah` or `kaniko`; the image leaves' `image-builder` input takes precedence |
| `CI_IMAGE_BUILDER_JOB_IMAGE` | unset | the digest-pinned kaniko job image, required with kaniko; the `image-builder-job-image` input takes precedence |

A fork's own CI reads more; see [Forks](#forks).

## Secrets and tokens

The orchestrators declare their secrets in `workflow_call.secrets`; the caller
passes the ones its release uses with an explicit `secrets:` mapping. Which are
needed follows the selected signing methods and publishing targets.
`secrets: inherit` also works, but it hands the called workflow every secret,
and the default lint gate's SAST check rejects it.
[Secrets, Permissions, and Validation](reference.md) is the full matrix and
says how to create each one.

| Secret | Used for |
|---|---|
| `GITHUB_TOKEN` | Automatic. Checkout, run artifacts, GitHub Packages and GHCR; scoped by `permissions:`. |
| `RELEASE_TOKEN` | Fine-grained PAT with `contents: write`: pushing the prepared commit and final tag, creating the release. Classic PATs are refused. |
| `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE`, `RELEASE_GPG_PUBLIC_KEY` | GPG artifact and Git-object signing (the defaults), GPG-based publishers, and tag verification. |
| `RELEASE_SSH_SIGNING_KEY` | Git-object signing with `git-signing.method: ssh`. |
| `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE` | Cosign key signing with `sign.key: env://COSIGN_KEY`; see [Signing](#signing). |
| `CODE_SCANNING_TOKEN` | Optional SARIF upload; see [Code scanning](#code-scanning-sarif). |
| `MAVEN_CENTRAL_USERNAME`, `MAVEN_CENTRAL_PASSWORD` | Maven Central. |
| `ANDROID_KEYSTORE`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`, `GOOGLE_PLAY_SERVICE_ACCOUNT_JSON` | Android signing and Google Play. |
| `SECRETS_PROPERTIES_BASE64` | An Android `secrets.properties`. |
| `IOS_SIGNING_CERTIFICATE_BASE64`, `IOS_SIGNING_CERTIFICATE_PASSPHRASE`, `PROVISIONING_PROFILE_BASE64`, `XCCONFIG_BASE64` | Xcode signing. |
| `APP_STORE_CONNECT_API_KEY_ID`, `APP_STORE_CONNECT_ISSUER_ID`, `APP_STORE_CONNECT_API_PRIVATE_KEY_BASE64` | App Store Connect. |
| `FORGE_CI_BUILD_SECRETS_JSON` | Build secrets mounted into container builds; see [Build secrets](artifacts-reference.md#build-secrets). |
| `PROMOTE_RELEASE_REGISTRY_PASSWORD` | A separate release registry for the last promotion rung. |

Keyless Sigstore signing needs no secret: it uses the job's OIDC token
(`id-token: write`). `RELEASE_TOKEN`, `RELEASE_SSH_SIGNING_KEY`,
`RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE` and the two
`RELEASE_COSIGN_*` secrets have the same names on the GitLab and Forgejo
release paths, so an organization on several forges configures one set of
names.

## Images and the engine binary

Containerized jobs run in the runtime images, which carry the `forge-ci`
binary and the toolchain of one family (base, full, Java, Node, Rust, Go,
Android; Android is linux/amd64 only). Every orchestrator requires
`forge-ci-binary-ref` as the full engine commit and `runtime-image` and
`canonical-builder-image` as `@sha256:` digests. `runtime-image-full` and the
family images (`-java`, `-node`, `-go`, `-rust`, `-android`) are digests too:
pass the ones your artifacts use, and a native release always needs
`runtime-image-full`. A workflow refuses a tag or an empty image it needs
before its job starts, and none picks an image for its caller. Keep the
workflow ref, `forge-ci-binary-ref` and the image digests on one release; the
[Advanced Pinning Recipe](runtime-images.md#advanced-pinning-recipe) shows how
to read them. A leaf workflow called on its own takes the same
`forge-ci-binary-ref` and one `runtime-image` digest.

The first job builds the CLI from `forge-ci-binary-ref` in the builder image.
Linux jobs on a plain runner, and the planning jobs, receive that exact
build and check it before they run it; other container jobs, such as the
product builds, use the CLI baked into their image, and macOS jobs build it
from a checkout of the same commit. No workflow downloads a forge-ci binary
onto a host. [Engine delivery](engine-delivery.md) describes the checks, and
[Runtime Images](runtime-images.md) the roster and which workflow uses which
image.

### Linux image builders

`publish-container.yml` and `snapshot-images.yml` take `image-builder` and
`image-builder-job-image`; empty, they use the `CI_IMAGE_BUILDER` variables
above, with Buildah as the default. Buildah runs on the hosted runner. Kaniko
runs in a Docker container action on that runner, in an image built from
`containers/kaniko-job`, pinned by digest, readable by the runner and
labelled `org.opencontainers.image.source` (the engine's repository URL) and
`org.opencontainers.image.title` (`forge-ci kaniko job image`); the workflow
checks the labels and the architecture before building. Kaniko has no load
mode and no build secrets, so a nonempty `build-secrets` is refused. Context
and Containerfile paths must be relative to the checked-out source. Both
builders scan, publish and sign the same way, natively per architecture.

## Pull-request gates and lint

`pullrequest-orchestrator.yml` plans the quality stage from its inputs and
runs one general lint engine, chosen with `lint-engine`:

- **nanolinter** (default): `lint-nanolinter.yml` runs `nanolinter verify`
  against the project's `nanolinter.toml` in the flavour image for
  `project-type`, with `required-lints` as a floor the project cannot lower.
  It covers lint, licence/REUSE, OpenGrep SAST, OSV dependency scans, secrets
  and trivy-fs, and the pull request's commit messages through gommitlint,
  the `commits` check of the example plans; every flavour image carries
  gommitlint. The job runs on the runner and starts the image with
  `docker run` as the runner's unprivileged user, with no capabilities and the
  checkout mounted at `/workspace`; a self-hosted runner needs Docker.
- **MegaLinter**: `lint-megalinter.yml` runs a caller-selected, digest-pinned
  image against `.mega-linter.yml`. There is no default image.
- **none**: no general engine.

Swift checks (`linters.swiftlint`, `linters.swiftformat`) run separately on
macOS, because nanolinter has no macOS binary. The orchestrator writes one
aggregated step summary. Project tests are not run by it: wire them in your own workflow beside it. [Lint
Workflows](components.md#lint-workflows) has the flavour table and direct-call
snippets, and [Linting](verification.md#linting) the caller/project policy
boundary.

The gate is your branch protection: require the orchestrator's jobs, and your
test jobs, as status checks. The jobs are named by stage, as on the other
forges: `Check: plan`, `Check: lint`, `Check: status` and so on. A required
check is matched by that name, so a rename of a job changes the check to
require. The GitHub layer has no release-isolation gate like Forgejo's
`check-l3-isolation`.

**Who sets the enforcement settings.** The `enforce-*` inputs are only the
administrator's when they are written in a workflow the consumer cannot
change. Put that workflow in the administrator's repository and require it
with an organization ruleset; it calls the orchestrators with the settings as
literals. A consumer's own workflow that passes `enforce-lint: false` relaxes
only its own job, which must not be the required check. See
[Administrator check enforcement](admin-policy.md#github).

## Builds

`release-build-stage.yml` runs one build job per artifact in the plan: build
leaves for Maven, npm, Gradle, Gradle Android, Go, Cargo and Xcode. Each uploads
a typed run artifact for the publish stage. Go and Cargo default to
artifact-first (standalone binaries); `build-mode: container-first` lets the
Containerfile own the compile, with `sbom-go.yml` or `sbom-cargo.yml` for the
SBOM. [Ecosystem Support](ecosystems.md) has the per-ecosystem matrix,
[Build Workflows](components.md#build-workflows) each leaf's inputs.

Containers are declared under `containers[]` and built in the publish stage by
`publish-container.yml`: native multi-arch on split runners, Trivy gate, SBOM
and SLSA provenance attestations, and an entry in the release-image ledger.
See [Container configuration](artifacts-reference.md#containers-section) and
[Artifact and Image Flows](flows.md). Container builds run their `RUN` steps
on the job's own network (`--network host`), so they reach what the job's
steps reach, the host's loopback services included; trust a build as you
trust the job.

## Releases and promotion

A signed `release-request/vX.Y.Z` tag starts `release-orchestrator.yml`:

1. **Setup and validation**: parse the configuration, validate the request
   tag, its signature and allowlist, and every prerequisite the plan needs
   ([Prerequisites Check Matrix](reference.md#prerequisites-check-matrix)).
   The prerequisite job verifies with public keys only: it checks that the
   private GPG key and passphrase are set, and never imports them.
2. **Prepare**: version bump in project files, changelog, the signed release
   commit, and the final `vX.Y.Z` tag, created once.
3. **Build**: one job per artifact.
4. **Publish**: one job per target: GitHub Packages, Maven Central, Google
   Play, App Store Connect, and `publish-container` per container.
5. **Release**: `release-create-github.yml` creates the GitHub release with
   changelog, checksums, signatures, SBOMs and provenance.
6. **Promotion**: `promote-stage.yml` moves the same image digest through
   `:dev`, `:staging` and the release tags. Staging and release wait on the
   GitHub Environments named by `promote-staging-environment` and
   `promote-release-environment`. Promotion never rebuilds or re-signs.

The default `release-policy: stable` accepts stable requests only. Select
`release-policy: stable-prerelease` for complete signed beta/RC releases;
build metadata remains unsupported. See
[release policy](workflows.md#stable-and-prerelease-policy).
[Workflow Architecture](workflows.md#image-promotion-ladder) describes the ladder,
[Publishing](publishing.md) each target. When a release fails partway, follow
[Failed Release Recovery](failed-release-recovery.md): never delete or move a
published tag.

### Version preparation and signing

The version bump runs the project's own Maven, npm or Cargo tooling in a
separate job without keys. The signing job receives only the resulting
version patch, checks that it changes nothing but versions, and commits it
with the changelog; Gradle and Xcode versions are edited by forge-ci itself.
No project build tool runs beside the signing keys.

### The published release is read back

After `Publish: release` has published and the old releases are trimmed, its
last step reads the release back with `forge-ci release validate-published`
and compares it with the files the release assembly planned, signatures and
provenance included. The run fails, naming each file, when a planned file is
missing or has another size or SHA-256 digest, and when the tag has no
release, or two. A file the plan does not name is only a warning.

The step reads with the job's own `contents: read` token, or with
`RELEASE_TOKEN` for a draft (`release.draft: true`), which that token cannot
see otherwise.

### Old releases' files

Each release adds its archives, binaries and packages, and a forge's storage
quota fills. With `release-asset-retention: <N>` on `release-orchestrator.yml`
the release keeps every file of the newest N published releases. After it publishes, each older release
keeps its checksum manifests, signatures, bundles and SBOMs, and loses the
rest; the releases, their notes and their tags stay. Empty, the default,
deletes nothing. With reproducible builds a deleted file can be rebuilt from
its tag and checked against the kept checksums and against its own
signature. Drafts are neither counted nor touched.
`forge-ci release asset-retention --repository <owner/repo> --keep <N>` is a
dry run that lists what a value would delete; `--keep-files` changes which
files old releases keep.
A release GitHub marks immutable is counted and kept whole: GitHub does not
change its files. The step deletes with `RELEASE_TOKEN`.

### Protect the release tags

The `release-request/vX.Y.Z` tag is the human approval of a release, so only
release administrators may create it. In the repository, open *Settings →
Rules → Rulesets → New ruleset → New tag ruleset* and create two rulesets:

| Ruleset | Target pattern | Rules | Bypass list |
|---|---|---|---|
| `release-request` | `release-request/*` | Restrict creations, updates and deletions | the role or team that releases |
| `release` | `v*`, excluding `v*-pre` | Restrict updates and deletions | none |

Set *Enforcement status* to **Active** on both. Do not restrict creations of
`v*`: the flow creates the final tag itself with `RELEASE_TOKEN`. Exclude
`v*-pre` (*Exclude by pattern*), because the rolling pre-release channel
recreates its tag at each publish. Once a final tag exists it cannot move, which is what
[Failed Release Recovery](failed-release-recovery.md) relies on.

## Snapshots

`release-snapshot-orchestrator.yml` runs on the branches your caller selects
and publishes a content-addressed npm snapshot (`<version>-snapshot-<branch>-<sha>`,
dist-tag `snapshot`) to `npm-registry` (GitHub Packages by default), with SBOMs
when `sboms` asks for them.
Declared containers also publish by default from the same frozen source SHA;
`publish-containers: false` opts out. Containers use branch-hashed tags ending
in `-snapshot`. Snapshot images are always scanned, their SBOM policy is
independent of the package `sboms` input, and they are unsigned and never
advance release aliases or promotions; see
[what a snapshot takes from the declaration](workflows.md#what-a-snapshot-takes-from-the-declaration) and the
[snapshot container contract](workflows.md#snapshot-container-contract-all-three-forges)
and [manual branch-selection example](../examples/container-snapshot/).

`publish-files: true` also keeps the built files of the declared artifacts as
unsigned workflow artifacts of the run, named
`<artifact-name>-files-<artifact>-<snapshot version>` and kept for 7 days for
readers of the repository; see [snapshot files](workflows.md#snapshot-files). The
`build-files` job has read permissions only.

The orchestrator's snapshot images reuse the files its own build jobs
uploaded. `snapshot-images.yml` called on its own builds the Go, Cargo, Maven,
Gradle JVM and npm artifacts its images take first; see
[automatic artifact-first inputs](workflows.md#automatic-artifact-first-inputs).

A snapshot caller for feature branches:

```yaml
# .github/workflows/release-snapshot-workflow.yml
name: Snapshot Release
on:
  push:
    branches: ['dev/**', 'feat/**']
permissions:
  contents: read
jobs:
  snapshot-release:
    uses: diggsweden/forge-ci/.github/workflows/release-snapshot-orchestrator.yml@PIN_FORGE_CI_SHA # v0.90.0
    permissions:
      actions: read
      contents: read
      packages: write
    # An npm snapshot to GitHub Packages needs no secret. An Android or iOS
    # snapshot passes its signing secrets by name under `secrets:`.
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
      runtime-image-node: ghcr.io/diggsweden/forge-ci-runtime-node-24@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      artifacts-config: .forge-ci/artifacts.yml
```

## Signing

Release files and images are signed with `sign.method` in `artifacts.yml`,
unless `sign.files-method` gives the files their own (below):

| Method | Key | On GitHub |
|---|---|---|
| `gpg` (default) | Long-lived OpenPGP key in `RELEASE_GPG_PRIVATE_KEY` | `.asc` sidecars, verified with `gpg --verify`. |
| `sigstore` | None: keyless, the workflow's OIDC identity through public Fulcio and Rekor | `.bundle` sidecars bound to the workflow identity. The recommended default; see the [example](../examples/signing/sigstore-keyless/). |
| `kms` | A KMS key reference (AWS, GCP, Azure, Vault/OpenBao, PKCS#11) | `.bundle` sidecars; the token exchange with the KMS is the caller's. See the [OpenBao example](../examples/signing/openbao-kms/). |
| `kms` with `key: env://COSIGN_KEY` | A Cosign key pair: the private key in `RELEASE_COSIGN_PRIVATE_KEY`, its passphrase in `RELEASE_COSIGN_PASSPHRASE` | `.bundle` sidecars, verified against the public key. The GitLab and Forgejo layers sign the same way. See the [example](../examples/signing/cosign-key/). |

**GPG for files, Cosign for images.** `gpg` cannot sign an image or its SLSA
provenance, so a release with both files and images that wants `.asc`
signatures on its files adds `files-method: gpg` beside a Cosign method:

```yaml
sign:
  method: sigstore
  files-method: gpg
```

The release files are then signed in the `gpg` job below, with
`RELEASE_GPG_PRIVATE_KEY`, and nothing about them is written to the
transparency log. The images keep their Cosign signatures and provenance from
`method`, with its key, issuer and transparency settings. Without the field,
`method` signs both. See
[`sign.files-method`](artifacts-reference.md#signfiles-method).

Signing and publishing are separate jobs. `gpg` signs on a plain runner with
the canonical CLI delivered for the run: GitHub-hosted runners have swap on,
and `release sign` refuses to load a long-lived key while it is
([swap policy](verification.md#swap-policy)), so that job turns swap off
first; a self-hosted runner needs swap off, or passwordless `sudo` for the job
to do it. `sigstore` and `kms` sign inside the admitted `runtime-image`
digest, whose baked cosign does the work; no installer runs. The full runtime
image only assembles the files, and publication runs on a plain runner with
the same canonical CLI.

The publisher verifies the signer's checkpoint digest and the prepared tag,
then publishes those exact bytes without signing credentials or OIDC access.
Retry a failed publisher job rather than regenerating signatures; see
[failed release recovery](failed-release-recovery.md).

**Cosign key signing.** `sign.key: env://COSIGN_KEY` is the one environment
reference `artifacts.yml` accepts; another variable is refused, because Cosign
would receive it. Pass the two `RELEASE_COSIGN_*` secrets to the release
orchestrator, and its `cosign-public-key` and `cosign-public-key-sha256`
inputs: the public key from a variable, `${{ vars.COSIGN_PUBLIC_KEY }}`, and
its SHA-256, as `sha256sum cosign.pub` prints it, written out in the workflow.
The secret reaches only the steps that sign with it. Each job that holds it,
the release and the two container signing jobs, first checks that the key
derives that public key, and refuses to sign otherwise, so a changed variable
alone cannot swap the trusted key. Write `transparency: none` to keep the
signatures out of the public log, as the other forges do by default.

Containers are signed with Cosign and carry SBOM and SLSA provenance
attestations. `sign.transparency` decides whether signing writes to the public
Rekor log; keyless signing always does, and `kms` can opt out with `none`.
The release commit and tag are signed separately, with GPG or SSH
(`git-signing.method`). [Signing Methods](verification.md#signing-methods-for-release-artifacts),
[`sign.transparency`](verification.md#signtransparency-what-the-signing-run-publishes-about-itself)
and [Container image attestation](verification.md#container-image-attestation--verification)
cover each one and how to verify it.

## Code scanning (SARIF)

GitHub is the one forge that ingests SARIF. Both lint engines upload their
security findings to Code Scanning when `CODE_SCANNING_TOKEN` is passed and
`upload-sarif` is on; without the token the scan still runs and gates, and the
findings stay in the job. Categories are `nanolinter-security`,
`megalinter-security`, `scorecard` and `container-scan-<name>-<arch>`.
Scorecard and container-scan SARIF are also kept as workflow artifacts.
[CODE_SCANNING_TOKEN](reference.md#code_scanning_token) says how to create
the token (a GitHub App installation token is recommended) and lists the
artifact names. OpenSSF Scorecard (`security-openssf-scorecard.yml`) needs a
caller of its own with an `on.schedule`.

## Forks

A fork of this repository on GitHub serves the workflows to its own consumers.

1. **Fork or copy** the repository. Consumers pin your fork's commits or tags
   in `uses:`.
2. **Set the repository variables** below in the repository that runs the
   workflows, where they differ from the defaults. `FORGE_CI_REPOSITORY`
   already defaults to the repository the called workflow comes from, so the
   fork's workflows install the fork's engine.
3. **Publish runtime images** from the fork with Self Runtime Container on
   the pre-release branch, and have consumers override the `runtime-image*`
   inputs: released defaults still name `ghcr.io/diggsweden`.
4. **Provision the rolling pre-release** if the fork publishes the
   pre-release CLI (below).

The fork's own CI, Self Runtime Container, Self Pull Request and Self
Release, reads these variables as well:

| Variable | Default | Used for |
|---|---|---|
| `FORGE_CI_PRERELEASE_BRANCH` | `main` | the branch whose pushes publish runtime images and the pre-release CLI |
| `RELEASE_PLATFORMS` | `linux/amd64,linux/arm64` | the runtime images' platforms; an explicit value may narrow them |
| `FORGE_CI_IMAGE_NAMESPACE` | `ghcr.io/<repository owner>`, lowercased | where the runtime images are published and read from; a lowercase, tag-free GHCR path under the owner, such as `ghcr.io/owner/scratch-repo`, keeps a scratch engine's packages apart |
| `RUNTIME_IMAGE_RETENTION` | unset: nothing is deleted | how many of the newest `sha-*` images of each runtime image to keep after publishing; release and pre-release tags, and everything a kept image needs, stay. `forge-ci container package-retention --dry-run` shows what a value would delete |
| `FORGE_CI_COSIGN_TRANSPARENCY` | unset: `public` | `none` publishes the runtime images, their attestations and the CLI unsigned, and skips the rolling pre-release CLI, so the fork never appears in the public transparency log; any other value fails the run |
| `FORGE_CI_RUNTIME_VULNERABILITIES` | unset: report | `enforce` refuses a runtime image with a `CRITICAL` finding not approved in `.trivyignore.yaml`; unset, findings are reported. Consumer images take `enforce-vulnerabilities` from their caller instead |
| `FORGE_CI_TESTSUITE_REPOSITORY` | `diggsweden/forge-ci-blackbox-tests` | the external black-box suite the pull-request and release gates check out |
| `RELEASE_BRANCH` | the branch whose head the release-request tag names | an explicit release branch when several branches share that head |

`vars` resolve in the calling repository. A consumer that calls a fork's
workflows runs that fork's bootstrap scripts and binary, and trusts signatures
made by its identity; setting `FORGE_CI_REPOSITORY` overrides that choice.
A vendored copy called as `uses: ./.github/workflows/...` is served by the
consumer's own repository, so set `FORGE_CI_REPOSITORY` to the repository whose
engine and signer it should trust.

A fork that publishes the rolling pre-release CLI provisions its release once.
Self Runtime Container replaces the assets of an existing `v0.90.0-pre`
pre-release and moves its tag last, and refuses when there is none. Create it
on the pre-release branch, empty and not marked latest:

```bash
gh release create v0.90.0-pre --repo OWNER/forge-ci --target main \
  --prerelease --latest=false --title "forge-ci 0.90.0-pre (rolling pre-release)" --notes ""
```

Self Release pins runtime images to `sha-<first seven characters>` of the release
commit, which Self Runtime Container publishes when that commit lands on the
pre-release branch. The weekly image rebuild is a GitHub schedule and therefore
still runs from the default branch.

Before you move consumers to a fork's commit, test it from a consumer as in
[Branch Testing](runtime-images.md#branch-testing).
[Development](DEVELOPMENT.md#branch-testing-reusable-workflows) covers the
maintainer side.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Release stops in its first preflight with exit 78 | A configuration the forge cannot serve: GitHub Enterprise Server, or a capability the forge lacks. Exit 78 is never worth retrying; see the [capability matrix](providers.md#capability-matrix). |
| `RELEASE_TOKEN` refused in preflight | A classic PAT, or a fine-grained token without access to the repository. See [RELEASE_TOKEN](reference.md#release_token). |
| Request tag refused | Unsigned or lightweight tag, a signer outside the allowlist, or a prerelease suffix under the default `release-policy: stable`. Trusted `stable-prerelease` policy admits signed beta/RC requests; see [release policy](workflows.md#stable-and-prerelease-policy) and [Release Authorisation](verification.md#release-authorisation). |
| Findings missing from Code Scanning | No `CODE_SCANNING_TOKEN` passed, or `upload-sarif: false`. The gate itself still ran. |
| A build succeeds but nothing is published | See [Configuration troubleshooting](artifacts-reference.md#configuration-troubleshooting). |
| A release failed after the final tag was pushed | Re-run forward; see [Failed Release Recovery](failed-release-recovery.md). |
| A run stops in `canonical-cli` or before it | `forge-ci-binary-ref` is not a full 40-hex commit, or `runtime-image` / `canonical-builder-image` is a tag rather than an `@sha256:` digest; see [Advanced Pinning Recipe](runtime-images.md#advanced-pinning-recipe). |

`forge-ci doctor` in a job prints the detected forge, its capabilities and what
to do where it degrades.

## Limits

- **No container tag deletion.** `container release-images cleanup` and
  `rollback` need a forge registry API that deletes a tag and keeps the
  manifest; the GitHub adapter has none and refuses.
- **npm publishing targets GitHub Packages**; `NPM_TOKEN` is reserved and does
  not enable npmjs.org.
- **No distro package registry.** [GitHub Packages](https://docs.github.com/en/packages/learn-github-packages/introduction-to-github-packages)
  has no Debian, RPM or Alpine registry, so a declaration with
  `distro-packages` is refused when the release is planned; the packages
  stay release files.
- **Snapshot image features are limited.** Build secrets are unsupported; a
  snapshot builds the runtime image unsigned, whatever the declaration signs
  in a release, and runs no `extract.binary`.
- **Android runtime image is linux/amd64 only.**
