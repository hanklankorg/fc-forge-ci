# GitHub

How `forge-ci` is used on GitHub: the reusable workflows under
`.github/workflows/` that check a pull request, build, publish, sign and release,
and the runtime images and CLI they run.

The three forge guides, [GitHub](github.md), [GitLab](gitlab.md) and
[Forgejo](forgejo.md), use the same section order. What each layer has beside
the others is in [Delivery layers side by side](providers.md#delivery-layers-side-by-side).

> **Unpublished 0.90.0.** `v0.90.0` and its runtime images do not exist yet.
> Treat `@v0.90.0` snippets as forward-looking; for pre-release testing, follow
> [Branch Testing](runtime-images.md#branch-testing) and do not substitute an
> unreviewed moving ref in production.

To inspect the CLI today, use the reviewed
[source checkout](DEVELOPMENT.md#source-checkout). The adoption steps below
require available release artifacts: `init` reads examples from a release tag,
and installing a local binary does not publish workflow images.

## What ships

| Path | Content |
|---|---|
| `.github/workflows/` | Three orchestrators a consumer calls, the stages they compose, and the build, publish, lint, security and release leaves. [Components Reference](components.md#workflow-inventory) is the complete inventory. |
| `.github/actions/install-forge-ci` | The composite installer for plain-runner jobs: verifies a released binary against its Sigstore bundle. See [Installing `forge-ci`](providers.md#installing-forge-ci). |
| `containers/runtime/Containerfile` | The runtime images the workflow jobs run in, published on `ghcr.io`. See [Runtime Images](runtime-images.md). |
| `examples/` | A pull-request, release and, where it applies, snapshot workflow per ecosystem under `<ecosystem>/github/`, and the signing backends. See [Examples](../examples/README.md). |

The orchestrators are the supported entry points:

| Orchestrator | Trigger | Does |
|---|---|---|
| `pullrequest-orchestrator.yml` | `pull_request`, push | The lint gate (nanolinter or MegaLinter) and Swift checks, with findings in Code Scanning. Tests stay in the caller's own workflow. |
| `release-orchestrator.yml` | A signed `release-request/vX.Y.Z` tag | Validates the request and prerequisites, prepares the signed version bump and final tag, builds every declared artifact, publishes it, builds and signs containers, and creates the GitHub release. |
| `release-snapshot-orchestrator.yml` | Branch pushes | A branch-suffixed npm snapshot and optional SBOMs; no signing, containers or release. |

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
| Runners | GitHub-hosted `ubuntu-26.04`; `ubuntu-26.04-arm` for the arm64 leg of a container build; macOS runners (the `macos-version` input) for Xcode and Swift jobs. |
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
steps 1 to 3 for the example its build files imply (`--example` picks
another): it writes the workflows, `.forge-ci/artifacts.yml` and
`nanolinter.toml`, pinned to the newest release, and never overwrites a
file. Review generated project-specific values before running; `doctor` checks
local configuration, not remote secrets or runner/publishing availability. By hand:

1. **Pick an example** from [`examples/`](../examples/README.md) for your
   ecosystem, and a signing backend from
   [`examples/signing/`](../examples/signing/).
2. **Copy** its `github/pullrequest-workflow.yml` and
   `github/release-workflow.yml` (and `github/release-snapshot-workflow.yml`
   if you want branch snapshots) into `.github/workflows/`, its
   `nanolinter.toml` into the repository root, and its `artifacts.yml` into
   `.forge-ci/` when you need one. The lint gate runs `nanolinter verify`
   against `nanolinter.toml` and requires at least the `required-lints`
   floor, default `secrets,sast`.
3. **Pin the references.** `uses: ...@v0.90.0`, `forge-ci-binary-ref` and any
   `runtime-image*` override move together; see
   [Advanced Pinning Recipe](runtime-images.md#advanced-pinning-recipe).
4. **Set the [secrets](#secrets-and-tokens)** your release plan selects, and
   [protect the release tags](#protect-the-release-tags).
5. **Commit the release allowlist** your signing policy needs; see
   [Release Authorisation](verification.md#release-authorisation).
6. **Release** by pushing a signed request tag:

   ```bash
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
   admitted. [Local Git signature setup](DEVELOPMENT.md#local-git-signature-setup)
   says how to install and inspect signing keys locally.

   If a release fails partway, re-run the failed jobs of that run. Do not
   delete the tag, the release or a published image: once the prepare job has
   pushed the final tag, the pipeline retries forward and converges. A release
   is never resumed from its final tag; if the prepare job itself fails after
   pushing `vX.Y.Z`, release the next patch version with a new request tag.
   [Failed Release Recovery](failed-release-recovery.md) covers the rest.

The [examples](../examples/README.md) show the caller workflows in full; the
snapshot caller is under [Snapshots](#snapshots). `forge-ci doctor` in a job reports what the detected forge supports.

## Inputs and variables

Each workflow's `on.workflow_call.inputs` block is its reference: every input
carries a description. The orchestrator inputs most callers set:

| Input | Workflow | Meaning |
|---|---|---|
| `project-type` | PR | `maven`, `npm`, `gradle`, `gradle-android`, `xcode-ios`, `cargo` or `go`; selects the nanolinter flavour. |
| `lint-engine` | PR | `nanolinter` (default), `megalinter` or `none`. |
| `required-lints` | PR | The mandated nanolinter floor, default `secrets,sast`. |
| `nanolinter-image`, `megalinter-image` | PR | Override the derived nanolinter flavour; the MegaLinter image, required and digest-pinned. A direct call of `lint-nanolinter.yml` defaults to the `core` tag; pin your flavour by digest as the examples show. |
| `branch` | release, snapshot | The branch to prepare and release from, or the snapshot branch. |
| `artifacts-config` | release, snapshot | Path to `artifacts.yml`, default `.forge-ci/artifacts.yml`. |
| `release-publisher` | release | `github-cli` creates the GitHub release; empty skips it. |
| `changelog-creator`, `changelog.config` | release | The changelog renderer (default `git-cliff`) and its configuration. git-cliff renders offline: a configuration that uses remote metadata, such as pull-request numbers, gets none. It reads stable `vX.Y.Z` tags only, overriding any `tag_pattern` in the configuration, so pre-release tags never split the changelog. |
| `release.sboms`, `release.signartifacts`, `release.requireallowlistedsigner`, `release.draft` | release | SBOM selection, artifact signing, the signer allowlist and draft releases. |
| `release-asset-retention` | release | How many of the newest releases keep every file; older ones keep their checksums and signatures. Empty, the default, deletes nothing. See [Old releases' files](#old-releases-files). |
| `promote-staging-environment`, `promote-release-environment`, `promote-release-*` | release | The GitHub Environments that gate the promotion ladder, and an optional separate release registry. |
| `forge-ci-binary-ref` | all | The CLI release plain-runner jobs install. |
| `runtime-image*` | all | The runtime image of each toolchain family. |

Configuration of what is built, published and signed lives in
`.forge-ci/artifacts.yml`, not in inputs; see the
[Artifacts Reference](artifacts-reference.md). Direct-call leaves are in
[Components Reference](components.md).

**Repository variables.** The workflows read these from the calling
repository; each falls back to the upstream value. A consumer of upstream sets
none. [Forks](#forks) says when to set them.

| Variable | Default | Used for |
|---|---|---|
| `FORGE_CI_REPOSITORY` | `diggsweden/forge-ci` | bootstrap checkouts and script downloads, binary installs, release URLs, trusted signing identities, and the repository allowed to publish the rolling pre-release CLI |
| `FORGE_CI_PRERELEASE_BRANCH` | `main` | the branch whose pushes publish runtime images and the pre-release CLI, and the branch the installer trusts for pre-release signatures |
| `RELEASE_PLATFORMS` | `linux/amd64` | the runtime images' platforms in Self Runtime Container, as a comma-separated subset of `linux/amd64,linux/arm64`; set both to publish arm64 images too. An undeclared, duplicated or malformed entry fails before anything is built. Consumers read the same variable for their images with the same meaning, unset building what their configuration declares |
| `RUNTIME_IMAGE_RETENTION` | unset: nothing is deleted | how many of the newest commit images (`sha-*`) of each runtime image Self Runtime Container keeps after it publishes; older versions are deleted through the GitHub Packages API with the job token, except release and pre-release tags and every platform manifest, signature and attestation a kept image needs. `forge-ci container package-retention --dry-run` shows what a value would delete |
| `FORGE_CI_IMAGE_NAMESPACE` | `ghcr.io/<repository owner>`, where Self Runtime Container publishes | runtime images used by Self Pull Request and Self Release; set it when the owner has capital letters or images live elsewhere |
| `FORGE_CI_TESTSUITE_REPOSITORY` | `diggsweden/forge-ci-blackbox-tests` | the companion black-box suite checked out by the PR and release gates |
| `RELEASE_BRANCH` | the branch whose head the release-request tag names | the fork's own Self Release: an explicit release branch when several branches share that head. A consumer names its branch with the orchestrator's `branch` input instead |

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
| `IOS_SIGNING_CERTIFICATE_PASSPHRASE`, `KEYCHAIN_PASSWORD`, `APP_STORE_CONNECT_API_KEY_ID`, `APP_STORE_CONNECT_ISSUER_ID` | Xcode signing and App Store Connect. |
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
Android). Each orchestrator has one `runtime-image*` input per family and
defaults to `ghcr.io/diggsweden/forge-ci-runtime-*:v0.90.0`.
[Runtime Images](runtime-images.md) has the roster, which workflow uses
which image, tag and pinning conventions, and mirroring. Android images are
linux/amd64 only.

Plain-runner jobs, the container build and the macOS jobs, install the
binary named by `forge-ci-binary-ref`. A release tag installs the archive
verified against `checksums.txt` and its Sigstore bundle; a branch or commit
ref builds from source. [CLI binary channels](runtime-images.md#cli-binary-channels)
and [Installing `forge-ci`](providers.md#installing-forge-ci) describe both.
Keep the workflow ref, `forge-ci-binary-ref` and the runtime images on one
version.

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
macOS, because nanolinter has no macOS binary. The orchestrator writes one aggregated step summary. Project tests are
not run by it: wire them in your own workflow beside it. [Lint
Workflows](components.md#lint-workflows) has the flavour table and direct-call
snippets, and [Linting](verification.md#linting) the caller/project policy
boundary.

The gate is your branch protection: require the orchestrator's jobs, and your
test jobs, as status checks. The jobs are named by stage, as on the other
forges: `Check: plan`, `Check: lint`, `Check: status` and so on. A required
check is matched by that name, so a rename of a job changes the check to
require. The GitHub layer has no release-isolation gate
like Forgejo's `check-l3-isolation`.

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
[Artifact and Image Flows](flows.md).
Container builds run their `RUN` steps on the job's own network (`--network host`), so they resolve and reach what the job's steps do, the host's loopback services included; a build is trusted like the job.

## Releases and promotion

A signed `release-request/vX.Y.Z` tag starts `release-orchestrator.yml`:

1. **Setup and validation**: parse the configuration, validate the request
   tag, its signature and allowlist, and every prerequisite the plan needs
   ([Prerequisites Check Matrix](reference.md#prerequisites-check-matrix)).
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

Production accepts stable `vMAJOR.MINOR.PATCH` requests only; prerelease and
build suffixes are refused before planning. [Workflow
Architecture](workflows.md#image-promotion-ladder) describes the ladder,
[Publishing](publishing.md) each target. When a release fails partway, follow
[Failed Release Recovery](failed-release-recovery.md): never delete or move a
published tag.

### Old releases' files

Each release adds its archives, binaries and packages, and a forge's storage
quota fills. With `release-asset-retention: <N>` on `release-orchestrator.yml` the release keeps every file
of the newest N published releases. After it publishes, each older release
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
recreates its tag at each publish. Once a final
tag exists it cannot move, which is what
[Failed Release Recovery](failed-release-recovery.md) relies on.

## Snapshots

`release-snapshot-orchestrator.yml` runs on the branches your caller selects
and publishes a content-addressed npm snapshot (`<version>-snapshot-<branch>-<sha>`,
dist-tag `snapshot`) to `npm-registry` (GitHub Packages by default), with SBOMs
when `sboms` asks for them.
It builds no containers, signs nothing and creates no release; a release-built
image reaches `:dev` through the promotion ladder instead. See
[Snapshot vs Production Release](components.md#snapshot-vs-production-release)
and the [snapshot trust model](verification.md#snapshot-release-trust-model).

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
    uses: diggsweden/forge-ci/.github/workflows/release-snapshot-orchestrator.yml@v0.90.0
    permissions:
      contents: read
      packages: write
    # An npm snapshot to GitHub Packages needs no secret. An Android or iOS
    # snapshot passes its signing secrets by name under `secrets:`.
    with:
      branch: ${{ github.ref_name }}
      forge-ci-binary-ref: v0.90.0
      artifacts-config: .forge-ci/artifacts.yml
```

## Signing

Release artifacts are signed with one method, `sign.method` in
`artifacts.yml`:

| Method | Key | On GitHub |
|---|---|---|
| `gpg` (default) | Long-lived OpenPGP key in `RELEASE_GPG_PRIVATE_KEY` | `.asc` sidecars, verified with `gpg --verify`. |
| `sigstore` | None: keyless, the workflow's OIDC identity through public Fulcio and Rekor | `.bundle` sidecars bound to the workflow identity. The recommended default; see the [example](../examples/signing/sigstore-keyless/). |
| `kms` | A KMS key reference (AWS, GCP, Azure, Vault/OpenBao, PKCS#11) | `.bundle` sidecars; the token exchange with the KMS is the caller's. See the [OpenBao example](../examples/signing/openbao-kms/). |
| `kms` with `key: env://COSIGN_KEY` | A Cosign key pair: the private key in `RELEASE_COSIGN_PRIVATE_KEY`, its passphrase in `RELEASE_COSIGN_PASSPHRASE` | `.bundle` sidecars, verified against the public key. The GitLab and Forgejo layers sign the same way. See the [example](../examples/signing/cosign-key/). |

Signing and publishing run in the release's plain-runner job with the
forge-ci binary pinned by the orchestrator, not in the runtime image, which
only assembles the files. GitHub-hosted runners have swap on, and
`release sign` refuses to load a long-lived key while it is
([swap policy](verification.md#swap-policy)), so that job turns swap off
first. A self-hosted runner needs swap off, or passwordless `sudo` for the
job to do it.

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
2. **Set the [repository variables](#inputs-and-variables)** in the
   repository that runs the workflows: `FORGE_CI_REPOSITORY` to the fork, and
   the others where they differ. Scheduling and provider-layer branch
   restrictions still apply.
3. **Publish runtime images** from the fork with Self Runtime Container on
   the pre-release branch, and have consumers override the `runtime-image*`
   inputs: released defaults still name `ghcr.io/diggsweden`.
4. **Provision the rolling pre-release** if the fork publishes the
   pre-release CLI (below).

`vars` resolve in the calling repository, so a consumer that sets
`FORGE_CI_REPOSITORY` to a fork runs that fork's bootstrap scripts and binary,
and trusts signatures made by its identity.

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
| Request tag refused | Unsigned or lightweight tag, a signer outside the allowlist, or a prerelease suffix. See [Release Authorisation](verification.md#release-authorisation). |
| Findings missing from Code Scanning | No `CODE_SCANNING_TOKEN` passed, or `upload-sarif: false`. The gate itself still ran. |
| A build succeeds but nothing is published | See [Configuration troubleshooting](artifacts-reference.md#configuration-troubleshooting). |
| A release failed after the final tag was pushed | Re-run forward; see [Failed Release Recovery](failed-release-recovery.md). |
| Plain-runner job cannot install the CLI | `forge-ci-binary-ref` names a release that does not exist yet, or a source ref the module restriction blocks; see [Branch Testing](runtime-images.md#branch-testing). |

`forge-ci doctor` in a job prints the detected forge, its capabilities and what
to do where it degrades.

## Limits

- **github.com only** for the release workflows; GitHub Enterprise Server is
  refused.
- **No container tag deletion.** `container release-images cleanup` and
  `rollback` need a forge registry API that deletes a tag and keeps the
  manifest; the GitHub adapter has none and refuses.
- **No release-isolation gate** over the consumer's workflow.
- **npm publishing targets GitHub Packages**; `NPM_TOKEN` is reserved and does
  not enable npmjs.org.
- **Snapshots are npm only.**
- **Android runtime image is linux/amd64 only.**
