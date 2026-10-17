# GitLab delivery

How `forge-ci` is used on GitLab CE/Free and above, hosted or self-hosted: the
CI/CD components under `templates/` that lint, check and build a merge request,
publish packages, and prepare, sign and publish a release. They call the same
engine commands as the other delivery layers.

The three forge guides, [GitHub](github.md), [GitLab](gitlab.md) and
[Forgejo](forgejo.md), use the same section order. What each layer has beside
the others is in [Delivery layers side by side](providers.md#delivery-layers-side-by-side).

> **Release source and instance setup.** Pin an authenticated engine commit and
> serve its components and checksum-bound package on your GitLab instance.
> An upstream GitHub release does not publish your GitLab Catalog or packages.
> Validate the selected instance and runner through
> [GitLab live acceptance](testing.md#gitlab-live-acceptance).

## What ships

Components must be hosted on the consumer's own GitLab instance: GitLab
includes a component only from a project on the same instance. There are two
families, which get the engine differently.

| Component | Family | Does |
|---|---|---|
| `check` | checksum-bound | Downloads the recorded engine, checks the consumer out at the pipeline commit and runs the consumer's check commands. |
| `release` (with `release-jobs`) | checksum-bound | The native release: request validation, changelog, signed release commit and final tag, key-free build, signing, image promotion, publication and cleanup, in separately scoped jobs. |
| `nanolinter`, `megalinter` | Catalog | The merge-request lint gate, with findings published as a GitLab SAST report. |
| `build-go`, `build-cargo`, `build-maven`, `build-npm`, `build-gradle`, `build-gradle-android` | Catalog | One release build per artifact: `forge-ci build <ecosystem> run` in a runtime image. |
| `publish-forge-packages`, `publish-maven-central`, `publish-apple-appstore` | Catalog | Maven or npm to the GitLab Package Registry, Maven Central, and App Store Connect. |

**Checksum-bound** components declare `spec: component: [sha, reference]` and
must be included by full commit SHA. Every job clones the engine project at
that commit and runs the `forge-ci` build the commit records, verified by
SHA-256; see [Checksum-bound engine bootstrap](#checksum-bound-engine-bootstrap).

**Catalog** components run in a `forge-ci` runtime image that already carries
the binary, and can be included by version. This repository's own
`.gitlab-ci.yml` publishes them to the CI/CD Catalog when a semver tag
(`X.Y.Z`) is pushed; mark the hosting project as a Catalog project first.

Every ecosystem the build components cover has a consumer pipeline in
[`examples/`](../examples/README.md), as `<ecosystem>/gitlab/.gitlab-ci.yml`:
the lint gate, `check` and the build component, and for the
[monorepo](../examples/monorepo/gitlab/.gitlab-ci.yml) the plan-driven build
fan-out.

## Requirements

| Requirement | Value |
|---|---|
| GitLab | 18.7 or later for `check`; `release` uses the same component context. CE/Free is enough: protected-environment approvals and paid pipeline-execution policies are not used. |
| Hosting | The components, and for checksum-bound components the engine project, on the **same instance** as the consumer. |
| Engine project | **Readable without credentials**, and its generic package downloadable anonymously. Downloads send no credential. |
| Runner architecture | **linux/amd64** for checksum-bound jobs: the recorded engine is built for that platform only. Catalog runtime images are linux/amd64 and linux/arm64, except Android (amd64 only). |
| Executor | Docker or Kubernetes. The components clear the entrypoint of `job-image`, `build-image`, `nanolinter-image` and `megalinter-image`, so an image that starts a tool of its own still runs the job's shell. |
| Job images | For `check`: Bash, Git, curl, `sha256sum` and the consumer's check tools, pinned by digest. For the lint components' SAST conversion: Bash, Git, curl and `sha256sum` in the lint image; Nanolinter's flavours and MegaLinter have them. For `release`: a non-root `job-image`, a `build-image`, and the organization's `signer-image`; see [Images and the engine binary](#images-and-the-engine-binary). |
| Artifact size | Enough native artifact capacity for the release handoff; the GitLab default of 100 MB can be too small. See [Releases and promotion](#releases-and-promotion). |
| Release branch | The project's default branch unless `release` is given another `release-branch`: the request must be its head. |
| macOS runner | With Xcode and a reviewed `forge-ci` on it, for `publish-apple-appstore` only. |
| SHA-256 repositories | Supported. Enable GitLab's `support_sha256_repositories` feature flag before creating the project; see [Troubleshooting](#troubleshooting). |

**Network.** Beside the instance itself, Catalog jobs pull their runtime images
from `ghcr.io` unless mirrored. The `release` component's render job
downloads the pinned changelog renderer archive. Checksum-bound jobs and the
lint components' SAST conversion fetch the engine binary from the instance's
own package registry.

## Getting started

1. **Host the components.** Put this repository on your instance (see
   [Forks](#forks)), and choose and authenticate the engine commit you will pin.
2. **Publish the engine binary** for that commit; see
   [Publish the recorded build](#publish-the-recorded-build).
3. **Add the merge-request gate** to the consumer's `.gitlab-ci.yml`: a lint
   component and the `check` component.

   ```yaml
   workflow:
     rules:
       - if: $CI_PIPELINE_SOURCE == "merge_request_event"
       - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH

   include:
     - component: gitlab.example.com/team/ci/forge-ci/nanolinter@0.90.0
       inputs:
         forge-ci-repository: team/ci/forge-ci
         nanolinter-image: "ghcr.io/itiquette/nanolinter:go@sha256:PIN_DIGEST"
     # check is checksum-bound: include it by the engine commit, never a version;
     # see Checksum-bound engine bootstrap in docs/gitlab.md.
     - component: gitlab.example.com/team/ci/forge-ci/check@PIN_FORGE_CI_SHA
       inputs:
         forge-ci-repository: team/ci/forge-ci
         job-image: registry.example.com/team/check-tools@sha256:PIN_IMAGE_DIGEST
         commands:
           - just test-unit
   ```

   Copy the example's `nanolinter.toml` to the consumer's repository root.
   The lint component runs `nanolinter verify` against it and requires at
   least the `required-lints` floor, default `secrets,sast`.
4. **Configure the project** to require successful pipelines and refuse
   merging on skipped pipelines, and protect the target branch.
5. **For releases**, create an independently controlled policy project that
   includes `release` and your merge-request gate (see
   [The merge-request gate under a policy](#the-merge-request-gate-under-a-policy)),
   point the consumer's CI configuration path at it, and set
   the [protected variables](#secrets-and-tokens); see
   [Releases and promotion](#releases-and-promotion).
6. **Release** by pushing a protected, SSH-signed `release-request/vX.Y.Z` tag
   at the head of the release branch. The flow creates the final `vX.Y.Z` tag once.

Replace every pin with a reviewed immutable value. The Nanolinter GHCR
coordinates are the migration destination: confirm the image is published and
readable, then use its served digest or an available mirror. See
[lint images](components.md#lint-nanolinteryml-default).
Component inputs are
documented on each component's Catalog page, which GitLab renders from its
`spec:`.

## Inputs and variables

Every input is declared in the component's `spec:` with its default and, for
images and pins, a validating regex. The inputs that are not obvious:

| Component | Input | Meaning |
|---|---|---|
| `check` | `forge-ci-repository` | The project serving the component and its engine package, including subgroups. |
| | `job-image` | Digest-pinned image for the check job. |
| | `commands` | The consumer's check commands; a failure fails the job. `job-name` (default `check`) and `stage` (default `test`) name the job. |
| `release` | `forge-ci-repository`, `consumer-project` | The engine project, and the only project allowed to run this policy. |
| | `job-image`, `build-image`, `signer-image` | Digest-pinned images for key-free jobs, the build job, and the signing jobs. |
| | `publication-project`, `publication-image-repository` | The separate project that owns final images, and the exact registry repository in it. |
| | `cosign-public-key-sha256` | SHA-256 of your Cosign public key, written out. |
| | `release-git-url`, `release-git-host-key-fingerprint` | The SSH write URL for the release commit and tag, and its independently supplied host key fingerprint. |
| | `request-allowed-signers` | OpenSSH allowed-signers text for the people who may sign a request tag. |
| | `bot-name`, `bot-email` | The release bot identity; no default. |
| | `checks`, `build` | Consumer commands: checks on the request commit, and the build that writes `dist/release-files.json` and `release-notes.md`. |
| | `changelog-creator`, `changelog-config`, `commit-body-config` | `git-cliff` (default) or `git-chglog`, and the consumer's configuration files, by default `.forge-ci/gitcliff/keepachangelog.toml` and `.forge-ci/gitcliff/minimal.toml`, copies of this repository's [`templates/gitcliff/`](../templates/gitcliff/). git-cliff renders offline, without remote metadata, and reads stable `vX.Y.Z` tags only, overriding any `tag_pattern` in the configuration. |
| | `release-branch` | The branch a release lands on; empty, the default, takes the project's default branch. The request must be its head. |
| | `transparency-log` | `none` (default) or `public`: whether Cosign signatures are also written to the public Sigstore transparency log. See [Signing](#signing). |
| | `sign-distro-packages` | Default `true`: GPG `.sig` sidecars for `.deb`, `.rpm` and `.apk`, which makes the GPG variables required. |
| | `release-asset-retention` | How many of the newest releases keep every file; older ones keep their checksums and signatures, upload and link both deleted. Empty, the default, deletes nothing. See [Old releases' files](#old-releases-files). |
| | `base-images` | Default `false`; see [Content-addressed base images](#content-addressed-base-images). |
| `nanolinter` | `forge-ci-repository`, `nanolinter-image`, `required-lints`, `enable-gitlab-sast` | The project serving the component and its engine package (required), the flavour image (default the `core` tag; pin your flavour by digest as the examples show), the mandated floor (default `secrets,sast`), and the SAST report. |
| `megalinter` | `forge-ci-repository`, `megalinter-image`, `enable-gitlab-sast` | Required digest-pinned image; the rest as for `nanolinter`. |
| `build-*` | `working-directory`, `version`, `skip-tests`, `enable-build-sbom`, `runtime-image` | Common to every build component; `build-go` and `build-cargo` add `platforms` and `artifact-name`, `build-maven` `build-type` and `profile`, `build-gradle-android` the module, flavour and signing inputs. |
| `publish-*` | `project-type`, `working-directory`, `runtime-image` | `publish-maven-central` adds `profile` and `settings-path`; `publish-apple-appstore` adds `platform`, `runner-tags` and `macos-image`. |

**Variables.** Project-level variables the components read:

| Variable | Value |
|---|---|
| `COSIGN_PUBLIC_KEY` | The PEM text of your Cosign public key; it must hash to `cosign-public-key-sha256`. |

Engine commands write scalar outputs to the dotenv file named by `CI_OUTPUT`,
with upper-snake names (`VERSION`, not `version`); see
[Scalar step outputs](providers.md#gitlab).

## Secrets and tokens

Use **project-level** protected variables with these environment scopes:

| Variables | Scope |
|---|---|
| `COSIGN_PUBLIC_KEY` | `*` (public value) |
| `RELEASE_SSH_SIGNING_KEY` | `release/prepare` |
| `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE`, `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE`, `RELEASE_GPG_FINGERPRINT` | `release/registry/signing` |
| `RELEASE_REGISTRY_USER`, `RELEASE_REGISTRY_TOKEN` | `release/registry/*` |
| `RELEASE_TOKEN` | `release/publish` |

These are ordinary environment names and variable scopes; protected-environment
approvals and paid pipeline-execution policies are not required. Each release
job also refuses to start when a release secret it does not use is visible to
it (`is available to the wrong release job`), so a wrongly scoped variable fails
closed.

| Secret | Used for |
|---|---|
| `RELEASE_SSH_SIGNING_KEY` | The signed release commit and final tag; a deploy key that may push to the project. |
| `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE` | Checksum, SBOM, provenance and image signatures and attestations. |
| `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE`, `RELEASE_GPG_FINGERPRINT` | Distro-package signatures, when `sign-distro-packages` is on. |
| `RELEASE_REGISTRY_USER`, `RELEASE_REGISTRY_TOKEN` | Importing, signing, promoting and cleaning up images in the publication project. |
| `RELEASE_TOKEN` | Creating the GitLab release and its assets. |

The Catalog publish components read their own masked, protected variables:
`MAVEN_CENTRAL_USERNAME`, `MAVEN_CENTRAL_PASSWORD`,
`RELEASE_GPG_PRIVATE_KEY` and `RELEASE_GPG_PASSPHRASE` for Maven Central, the
names GitHub uses;
`APP_STORE_CONNECT_API_KEY_ID`, `APP_STORE_CONNECT_ISSUER_ID` and
`APP_STORE_CONNECT_API_PRIVATE_KEY_BASE64` for App Store Connect; and
`ANDROID_KEYSTORE_BASE64`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`,
`ANDROID_KEY_PASSWORD` and `SECRETS_PROPERTIES_BASE64` for a signed Android
build. `publish-forge-packages` uses the job's own `CI_JOB_TOKEN`, which
carries package-write for its project.

**`CI_JOB_TOKEN`.** Checkout uses GitLab's short-lived job token when no
explicit token overrides it, bound to the issuing `CI_SERVER_URL`; see
[Git operation credentials](providers.md#git-operation-credentials). Removing
long-lived secrets from a job does not remove its job token; restrict it as
[Signing and publication are separate authorities](#signing-and-publication-are-separate-authorities)
describes. Source-check jobs refuse `GITLAB_TOKEN`.

## Images and the engine binary

### Runtime images

Catalog components default to `ghcr.io/diggsweden/forge-ci-runtime-*:v0.90.0`,
the same images the GitHub workflows use; set each component's `runtime-image`
to a digest. For an installation that must not pull from `ghcr.io`, mirror the
images to your own registry and point `runtime-image` at the mirror. [Runtime
Images](runtime-images.md) has the roster and pinning conventions.

`build-cargo` defaults to
`ghcr.io/diggsweden/forge-ci-runtime-rust-stable:v0.90.0`.

### Images of the release policy

The policy selects separate digest-pinned `job-image`, `build-image` and
`signer-image` inputs. Use a non-root `job-image` for checks and rendering;
permission tests must run with ordinary-user filesystem semantics. Only the
build job selects `build-image`, which can supply a root Buildah environment.
Pass the same image for both inputs when the build needs no separate environment.
The components clear the entrypoint of `job-image` and `build-image`, here and
in `check`, and of the lint images in `nanolinter` and `megalinter`, so an
image that starts a tool of its own still runs the job's shell on a Docker
executor as it does on a Kubernetes one. The signer image
is the organization's and declares none.

The signer image is built from the same definition as the Forgejo layer's,
`containers/signer/Containerfile`, and every signing job runs the same strict
toolchain check (`.forgejo/scripts/verify-signer-toolchain.sh`) first. No
GitLab job builds it: build it from the commit you pin, push it to your
registry and pin it by digest. [The signer image](forgejo.md#the-signer-image)
describes its contents and a manual `buildah build`.

### Checksum-bound engine bootstrap

The delivery binary has one build definition and one checksum, shared with
Forgejo. The established paths remain `scripts/ci/forgejo-binary.sh` and
`.forgejo/bin/forge-ci.sha256`; do not maintain a second GitLab build or
checksum copy.

Checksum-bound check/release jobs use the **Linux/amd64** delivery binary.
Select a compatible runner and job/signer image. This package coordinate does
not select a native ARM64 or macOS engine; other components' runtime-image
platforms are described in the [runtime image roster](runtime-images.md#image-roster).

Set these coordinates for GitLab:

```bash
export FORGE_CI_PACKAGE_PROVIDER=gitlab
export CI_SERVER_URL=https://gitlab.example.com
export FORGE_CI_REPOSITORY_URL=https://gitlab.example.com/team/ci/forge-ci.git
```

The resolver uses the full project path, including subgroups:

```text
https://gitlab.example.com/api/v4/projects/team%2Fci%2Fforge-ci/packages/generic/forge-ci/<sha256>/forge-ci-linux-amd64
```

The engine repository must be on `CI_SERVER_URL`. The package must permit
anonymous downloads. Downloads and publication read-back send no credential;
the recorded SHA-256 is verified before the binary can run.

#### Publish the recorded build

From a trusted checkout at the selected engine commit, with its pinned Go
toolchain and cached dependencies:

```bash
bash scripts/ci/forgejo-binary.sh build /absolute/private/staging/forge-ci
bash scripts/ci/forgejo-binary.sh publish /absolute/private/staging/forge-ci
```

Publication requires `FORGE_CI_PACKAGE_TOKEN` in the environment, supplied
by the CI secret mechanism rather than placed in a command argument or URL.
For GitLab, `FORGE_CI_PACKAGE_TOKEN_TYPE` selects the authentication header:

| Value | Header | Use |
|---|---|---|
| `private` (default) | `PRIVATE-TOKEN` | Access token authorized to publish to the engine project |
| `job` | `JOB-TOKEN` | Native `CI_JOB_TOKEN` with the required project package access |

For publication from the engine project's own job:

```bash
FORGE_CI_PACKAGE_TOKEN_TYPE=job \
  FORGE_CI_PACKAGE_TOKEN="${CI_JOB_TOKEN:?}" \
  bash scripts/ci/forgejo-binary.sh publish /absolute/private/staging/forge-ci
```

The publisher rejects a binary that differs from the checkout's recorded
checksum before uploading. After upload it downloads the published bytes
anonymously and checks the same checksum. Storage is not a trust root.
[The binary in the layer](forgejo.md#the-binary-in-the-layer) describes how
the build is reproduced and what the checksum does and does not prove; the
same holds here.

## Pull-request gates and lint

A merge-request pipeline is a static graph: the consumer's `.gitlab-ci.yml`
includes the components and owns `workflow:rules` and stages. The **pipeline
status is the gate**: GitLab fails the pipeline, and the merge request, when any
job fails. The ecosystem examples, such as
[Go CLI](../examples/go-cli/gitlab/.gitlab-ci.yml), show the shape: `workflow:
rules:` scoped to merge requests and the default branch, the lint component,
`check`, and a build component.

### Lint components

Pick one engine; [Lint Workflows](components.md#lint-workflows) shows how the
same choice reads on GitHub and Forgejo:

- **`nanolinter`** runs `nanolinter verify` against the project's
  `nanolinter.toml` in a flavour image (`core`, `go`, `java`, `kotlin`,
  `javascript-typescript`, `rust` or `swift`; default `core`), with
  `required-lints` as a floor the project cannot lower. Pin the image by digest.
  In a merge-request pipeline it checks out the whole history (`GIT_DEPTH: 0`)
  and fetches the target branch as `origin/<target>`, with the job token, so
  secret scanning and commit checks see what the merge request adds.
- **`megalinter`** runs MegaLinter against `.mega-linter.yml`. Its image is
  required and must be pinned by `@sha256:`; a tag is refused before the job is
  created, since the job container receives the checkout, CI variables and the
  job token.

Both publish findings as a SAST report; see [Code scanning](#code-scanning-sarif).
The ecosystem examples include `nanolinter` in their stack's flavour. To use
MegaLinter instead, include it in its place:

```yaml
include:
  - component: gitlab.example.com/team/ci/forge-ci/megalinter@0.90.0
    inputs:
      forge-ci-repository: team/ci/forge-ci
      megalinter-image: "REPLACE_WITH_REVIEWED_IMAGE_AT_SHA256_DIGEST"
```

Replace the placeholder with a reviewed MegaLinter flavour reference ending in
`@sha256:<64 lowercase hex digits>`.

### Source-check component

`templates/check.yml` is a GitLab 18.7+ component. It downloads the recorded
engine, checks out the consumer at
`CI_COMMIT_SHA`, fetches the MR target branch (or the default branch), then
runs the consumer's check commands. Include it by the engine's full commit SHA
and publish that commit's binary first:

```yaml
include:
  # check is checksum-bound: include it by the engine commit, never a version;
  # see Checksum-bound engine bootstrap in docs/gitlab.md.
  - component: gitlab.example.com/team/ci/forge-ci/check@PIN_FORGE_CI_SHA
    inputs:
      forge-ci-repository: team/ci/forge-ci
      job-image: registry.example.com/team/check-tools@sha256:PIN_IMAGE_DIGEST
      commands:
        - just test-unit
        - just lint
```

Replace both pins with reviewed immutable values. `forge-ci-repository` is the
project serving the component, including subgroups. The image must contain
Bash, Git, curl, sha256sum and the consumer's check tools. Optional input
`job-name` defaults to `check`; `stage` defaults to `test`.

The job starts with an empty workspace and no cache or artifact dependencies.
It uses the runner's custom CA when provided, keeps the helpers outside the
consumer checkout, and exposes the verified engine as `FORGE_CI_BIN` and
`forge-ci` on PATH. `CI_OUTPUT` points to a temporary dotenv file for engine
commands within the job; no cross-job output artifact is published.

Signing and publication credentials must be environment-scoped away from check
jobs. The component refuses known release secrets before checkout and refuses
a component included through a mutable reference. GitLab merges components
into the consumer's configuration: the caller still owns pipeline rules,
required checks and protected settings. This source-check primitive does not
yet establish a complete MR/release trust policy.

Consumer checkout selects GitLab's short-lived `CI_JOB_TOKEN` when no explicit
token overrides it. The credential stays bound to the issuing `CI_SERVER_URL`
and uses GitLab's required `gitlab-ci-token` HTTPS username. Git receives an
origin-scoped transient header; neither the token nor that header is written
to the checkout's remote URL or configuration. The component derives the
consumer's object format from the full pipeline SHA, avoiding a metadata API
request that job tokens cannot make for private projects. The engine
source/package must still be publicly readable. The checkout component supports
public and private consumers with SHA-1 or SHA-256 repositories.
Source-check jobs refuse `GITLAB_TOKEN`; scope long-lived API credentials to
their publication jobs rather than supplying them for checkout.

### Required MR gate

For a required MR gate, configure the GitLab project to require successful
pipelines and disallow merging on skipped pipelines. Protect the target branch
and scope protected release credentials to their signing environment. These
are project settings, not guarantees supplied by an included YAML component.

GitLab has no release-isolation gate like Forgejo's `check-l3-isolation`: the
signing policy lives in a separate, pinned policy project instead, which the
consumer's own YAML cannot change.

A GitLab job cannot read a sibling job's status, so an aggregated summary is
built from per-job records. Each job records its own outcome in
`after_script`, where `$CI_JOB_STATUS` holds the final status, and keeps the
record as an artifact even when it fails; a summary job that `needs:` them
collects the records:

```yaml
.records-outcome:
  image: ghcr.io/diggsweden/forge-ci-runtime-base:v0.90.0
  after_script:
    - forge-ci report job-result --name "$CI_JOB_NAME" --status "$CI_JOB_STATUS"
  artifacts:
    when: always
    paths:
      - .ci-results/jobs/

quality-summary:
  image: ghcr.io/diggsweden/forge-ci-runtime-base:v0.90.0
  stage: summary
  when: always
  needs: [lint, scan]
  variables:
    STAGE_PLAN_JSON: '{"version":1,"stage":"quality","targets":{"lint":{"runs":true},"scan":{"runs":true}}}'
    CI_OUTPUT: stage.env
  script:
    - forge-ci report stage-result
  artifacts:
    when: always
    paths:
      - .ci-results/
    reports:
      dotenv: stage.env
```

`lint` and `scan` are jobs that `extends: .records-outcome`; their names match
the plan's targets. With no `--job-results`, `report stage-result` collects
`$CI_RESULTS_DIR/jobs/*.json` (default `.ci-results`) and aggregates them
against `STAGE_PLAN_JSON` with the same rule GitHub and Forgejo feed from
`toJson(needs)`. A job cancelled before its `after_script` leaves no record,
and its target then counts as failed. `report pr` renders the panel from the
`<stage>-result.json` it writes.

## Builds

Each build component is one job running `forge-ci build <ecosystem> run` in its
runtime image, the same sequence the GitHub build workflows run. GitLab clones
the repository for the job; binaries, packages and the Build SBOM are kept as
native artifacts. Container builds run their `RUN` steps on the job's own network (`--network host`), so they resolve and reach what the job's steps do, the host's loopback services included; a build is trusted like the job.

| Component | Builds | Output |
|---|---|---|
| `build-go` | Go binaries per `platforms` | `dist/`, Build SBOM under `.forge-ci/go-build-sbom/` |
| `build-cargo` | Rust binaries per `platforms` | `dist/`, `bom.json` |
| `build-maven` | `build-type: app` or `lib` | `target/`, `target/bom.json` |
| `build-npm` | Packed tarball | `*.tgz`, `bom.json` |
| `build-gradle` | `gradle-tasks` (default `assemble`) | `build/libs/`, SBOM under `build/reports/` |
| `build-gradle-android` | APK and AAB, optionally signed | `<module>/build/outputs/`, SBOM under `build/reports/` |

There is no Xcode build component.

GitLab has no run-artifact API, so a later job receives files through native
`artifacts:` and `needs:`, never `forge-ci artifact download`. GitLab also
cannot expand a matrix from a value computed at run time: for an
`artifacts.yml` with several artifacts, `forge-ci plan gitlab-build-pipeline`
generates a child pipeline with one build component per planned item, and a
trigger job runs it. It refuses a plan that names a component that does not
exist. See the [monorepo example](../examples/monorepo/gitlab/.gitlab-ci.yml).

In the native release, the build is the consumer's own `build` commands,
running in `build-image` without signing keys. They must write
`dist/release-files.json` and `release-notes.md`, and, for images, the image
ledger. The flow checks that the hand-off is unsigned.

## Releases and promotion

### Native release policy (under acceptance)

The `release` component is designed for GitLab CE/Free. A project selects
a configuration from an independently controlled project using
`policy.yml@namespace/policy-project:<full commit SHA>` as its CI configuration
path. That policy includes `release` at the independently authenticated engine
commit. Consumer-owned `.gitlab-ci.yml` is not part of the signing policy.

A policy file includes the component with the [inputs above](#inputs-and-variables):

```yaml
include:
  - component: gitlab.example.com/team/ci/forge-ci/release@PIN_FORGE_CI_SHA
    inputs:
      forge-ci-repository: team/ci/forge-ci
      consumer-project: team/app
      job-image: registry.example.com/team/check-tools@sha256:PIN_DIGEST
      build-image: registry.example.com/team/build-tools@sha256:PIN_DIGEST
      signer-image: registry.example.com/team/forge-ci-signer@sha256:PIN_DIGEST
      publication-project: team/app-releases
      publication-image-repository: registry.example.com/team/app-releases/app
      cosign-public-key-sha256: PIN_COSIGN_PUBLIC_KEY_SHA256
      release-git-url: ssh://git@gitlab.example.com/team/app.git
      release-git-host-key-fingerprint: PIN_HOST_KEY_FINGERPRINT
      request-allowed-signers: |
        PIN_ALLOWED_SIGNERS
      bot-name: FIXME
      bot-email: FIXME
      checks:
        - bash .gitlab/check-release.sh
      build:
        - bash .gitlab/build-release.sh
```

#### Version bump

With `version-bump: maven` in the policy's `release` inputs, the signed
release commit carries the release version in the project's POMs, as it does
on GitHub and on Forgejo. The `Prepare: version` job runs `mvn versions:set`
in `job-image`, so that image needs the JDK and Maven, and holds no credential
(`rc_release_context` refuses every release secret there). It hands over only
the patch and its digest. `Prepare: release commit and tag`, which holds the
SSH signing key, runs no build tool: `forge-ci version apply-bump` checks the patch as text,
the files of `pom.xml` only, each changed line the version or the
reproducible-build timestamp and nothing else, then commits it beside
`CHANGELOG.md`. The build then finds the release version already declared.
Empty, the default, leaves the release commit to the changelog.

#### The merge-request gate under a policy

Once the project's CI configuration path names the policy, GitLab runs the
policy file for every pipeline and ignores the project's own `.gitlab-ci.yml`,
merge requests included. The policy therefore carries the gate too: it
includes the consumer's gate file for merge-request pipelines only, read at
the merge request's commit, and nothing of the consumer's for release tags.

```yaml
include:
  # The consumer's own gate, for merge requests only. It runs without the
  # protected signing variables and never on a release-request tag.
  - project: team/app
    ref: $CI_COMMIT_SHA
    file: .gitlab/merge-request.yml
    rules:
      - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
  - component: gitlab.example.com/team/ci/forge-ci/release@PIN_FORGE_CI_SHA
    inputs:
      # as above
```

Keep the gate in its own file, such as `.gitlab/merge-request.yml` with the
`nanolinter` and `check` components from [Getting started](#getting-started),
so the policy names exactly what it admits. The include's rule only decides
when the file is read; GitLab still adds no job without rules to a
merge-request pipeline, so the gate file declares the pipelines it runs in:

```yaml
# .gitlab/merge-request.yml
workflow:
  rules:
    - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
include:
  # check is checksum-bound: include it by the engine commit, never a version;
  # see Checksum-bound engine bootstrap in docs/gitlab.md.
  - component: $CI_SERVER_FQDN/team/ci/forge-ci/check@PIN_FORGE_CI_SHA
    inputs:
      forge-ci-repository: team/ci/forge-ci
      job-image: registry.example.com/team/check-tools@sha256:PIN_DIGEST
      commands:
        - bash .gitlab/check-release.sh
```

Release-request tag pipelines stay policy-only. The live consumer-release trial checks both sides when the
consumer has `.gitlab/merge-request.yml`: it adds this include to the policy,
releases, then opens a merge request on the released main and requires the
gate's pipeline to pass.

A merge request from a fork gets its pipeline in the fork by default, where
the gate compares with the fork's copy of the target branch and the fork's job
token cannot read a private project. Turn on the project's setting to run
pipelines in the parent project for merge requests from forks: the gate then
runs here, against the real target, once a maintainer starts it.

The parent trigger holds a resource group through a mirrored child pipeline,
serializing the entire release. The child uses native `needs` and artifacts;
artifact files are never sourced or declared as dotenv reports. Request tags
must be protected and SSH-signed against the allowed-signers text in the
trusted policy. Checks, changelog rendering and builds run without signing keys.

The child runs these jobs, in five stages that read the same on every forge
(check, prepare, build, sign, publish):

| Job | Image | Does |
|---|---|---|
| `Check: signing access`, `Check: publishing access`, `Check: registry access` | signer | The toolchain check, the Cosign key against the trusted public key, and the release and registry tokens, before anything else. |
| `Check: release request` | job | Validates the signed request and that it is the head of the release branch, then runs `checks`. |
| `Prepare: changelog` | job | Renders the changelog and commit message. Consumer configuration runs only here and in `Prepare: version`. |
| `Prepare: version` | job | With `version-bump`, sets the release version in the project files and hands over the patch; holds no credential. |
| `Prepare: release commit and tag` | job | Verifies the version patch, commits it with the changelog and pushes the signed release commit and final tag with `RELEASE_SSH_SIGNING_KEY`. |
| `Build: release files` | build | Checks out the final tag and runs `build`; hands `dist/` on with its digest. |
| `Sign: files and images` | signer | Imports images into the publication project, signs files, packages, provenance and images. |
| `Publish: images` | signer | Moves final image tags, with a journal. |
| `Publish: release` | signer | Creates the GitLab release with notes and assets. |
| `Publish: tidy up` | signer | Removes candidate tags. |

Which job may hold which release secret is written in roles, in
`.gitlab/bin/release.sh` (`rc_job_role`); a job's name maps to its role there.

**The request is the head of the release branch.** As on Forgejo, the release
commit is pushed on top of the request commit without force, so a request that
is not the tip of the release branch is refused. The branch is the
`release-branch` input, else the project's default branch, and every engine
call that reads a branch is given it explicitly, never through `RELEASE_BRANCH`
or `BRANCH`. The deploy key of `release-git-url` must be allowed to push to
that protected branch. Push the request tag at the tip and freeze merges until
`Prepare: release commit and tag` has pushed; a request whose branch moved first is spent.
[The request is the head of the release
branch](forgejo.md#the-request-is-the-head-of-the-release-branch) gives the operator rules.

Provision enough native artifact capacity for the full release handoff. GitLab's
default archive limit is 100 MB; a multi-platform package release can exceed it
even after raw build directories are pruned. The GitLab administrator owns this
capacity setting. A project owner's API request can be silently filtered when
it lacks `update_max_artifacts_size`; successful HTTP status alone is not proof
that the limit changed. Measure the largest native handoff for the selected
consumer and provision a bounded limit with appropriate headroom.
Forge Lab provides `just k3s-gitlab-artifact-limit 512` for this explicit admin
operation, with locality checks and verified read-back; the consumer account
keeps its ordinary permissions.

Each producer uses its own top-level artifact directory (`_release-render`,
`_release-build`, `_release-sign` or `_release-promote`). This avoids writing
inside a directory restored under another container's UID. The job's native
`after_script` makes only its outgoing regular files and directories readable
by the artifact helper, including a failed promotion's journal. It follows no
symlinks and leaves private workspaces outside that operation. Dist handoffs
use content digests rooted at `.` so different runner paths remain compatible.

The component owns input defaults and forwards explicit values to the child.
Its default renderer is `git-cliff`, and its default configuration paths match
the Forgejo layer: `.forge-ci/gitcliff/keepachangelog.toml` for `CHANGELOG.md`
and `.forge-ci/gitcliff/minimal.toml` for the release commit body. Copy the two
files of this repository's [`templates/gitcliff/`](../templates/gitcliff/) there
to start. Set `changelog-config` and `commit-body-config` to the consumer's
actual paths when using another layout or `git-chglog`; the renderer CLI
requires filenames.

### Old releases' files

Each release adds its archives, binaries and packages, and a forge's storage
quota fills. With `release-asset-retention: <N>` on the `release` component the release keeps every file
of the newest N published releases. After it publishes, each older release
keeps its checksum manifests, signatures, bundles and SBOMs, and loses the
rest; the releases, their notes and their tags stay. Empty, the default,
deletes nothing. With reproducible builds a deleted file can be rebuilt from
its tag and checked against the kept checksums and against its own
signature. Drafts are neither counted nor touched.
`forge-ci release asset-retention --repository <owner/repo> --keep <N>` is a
dry run that lists what a value would delete; `--keep-files` changes which
files old releases keep.
A GitLab release file is a project upload behind a link, so the upload is
deleted with its link: the link alone would keep the storage. A link to
anything else, such as a package file, loses only the link. Deleting an
upload needs GitLab 17.2 or later and `RELEASE_TOKEN` with the Maintainer
role. Publishing no longer leaves replaced or removed files behind either.

### Serialization and promotion recovery

The parent resource group covers the mirrored child through preparation, signing,
promotion and publication. A second release must wait before creating its child.
`TestGitLabRelease_ResourceGroupSpansTheChild` exercises that native scheduling
contract with a blocking child job.

Promotion journals belong to their job attempts. Keep the original failed
attempt's artifact if rollback is needed; a clean retry writes a journal for
the state it finds. Pipeline failure does not trigger automatic rollback.
`TestGitLabRelease_PromotionRetryAfterRegistryWrite` exercises a failure after
a registry write, blocked downstream jobs, retained evidence and a clean retry.

Promotion moves final tags in the publication project; there is no
`:dev`/`:staging` ladder on GitLab. GitLab keeps an untagged manifest reachable
by digest, so a rollback can restore a moved tag; see
[Implementation status](providers.md#implementation-status). For a failure
after the final tag exists, re-run forward and follow
[Failed Release Recovery](failed-release-recovery.md).

### Content-addressed base images

The independent policy can opt into base publication with `base-images: true`.
The build image must expose the pinned Cosign executable on PATH, including
for warm-only builds. Verification runs in an isolated environment; a Mise shim
requiring its configuration variables is insufficient. Resolve the installed
tool while preparing the reviewed image, for example
`install -m 0755 "$(mise which cosign)" /usr/local/bin/cosign`, then verify
`env -i HOME=/tmp PATH=/usr/local/bin:/usr/bin:/bin cosign version` works.

The consumer's key-free build owns its base graph, input IDs and cache decision:

- Verify warm bases with `container base-images validate`, the policy's pinned
  public key, exact source/workflow and expected flavor/input IDs. Publication
  bases must be anonymously readable. An existing tag with invalid evidence is
  an error, not a cache miss that may be silently rebuilt.
- Push newly built base digests only below `${CI_REGISTRY_IMAGE}/bases` using
  the native job token. Emit `dist/base-images.json` as a base-only ledger with
  `image_kind: base`, flavor, input ID, digest, content-addressed final tag
  `<base-input-id>-<flavor>` and its `staging-` candidate. Include any pinned
  SBOM files in the handoff. Warm-only builds omit this ledger.
- The trusted signer imports those digests into
  `${RC_PUBLICATION_IMAGE_REPOSITORY}/bases`, preserving identities, SBOM pins
  and build lineage. It refuses producer-supplied publication ledgers, mixed
  image kinds, moving tags, conflicting candidates and out-of-scope refs.
  It then signs the imported bases and their CycloneDX/SLSA attestations.
- The separate promotion job verifies that evidence before publishing immutable
  base tags, then promotes release images. Release-image base references use
  the publication base repository. Base candidates are retained; retention is
  a separate base-image cleanup/prune operation.

`container base-images import` is distinct from `container release-images
import`: release-only import continues to reject base entries. Neither import
copies producer signatures or publishes final tags. A failure after base
promotion can leave a valid immutable cache entry for the next request; bases
are not rolled back with release tags.

### Package publication

The Catalog publish components are separate from the native release and run
in the consumer's own pipeline. Each expects its input files from an earlier
job through native artifacts.

| Component | Publishes | Needs |
|---|---|---|
| `publish-forge-packages` | Maven (`mvn deploy`) or npm to the project's GitLab Package Registry | `CI_JOB_TOKEN`; JARs under `*/target/` or the packed `*.tgz` |
| `publish-maven-central` | A Maven library to Maven Central, GPG-signed through the Maven profile | The variables in [Secrets and tokens](#secrets-and-tokens); sources and javadoc JARs |
| `publish-apple-appstore` | An IPA to App Store Connect with `xcrun altool` | A macOS runner with Xcode and `forge-ci`; the IPA under `artifacts/` |

Google Play and container publishing have no GitLab component. See
[Publishing](publishing.md) for target setup, which is forge-neutral for
Maven Central and App Store Connect.

## Snapshots

The GitLab layer has no snapshot flow: there is no counterpart of GitHub's
`release-snapshot-orchestrator.yml`. A branch build is the consumer's own
pipeline, for example with a build component and `publish-forge-packages`.

## Signing

The runner that runs the signing jobs needs swap off: `release sign` refuses
to load a long-lived key while swap is active, and a job image cannot turn
it off ([swap policy](verification.md#swap-policy)). Turn it off in the
runner host's setup, or sign with `sigstore` or `kms`.

### Signing and publication are separate authorities

Removing long-lived secrets from a build does not remove its `CI_JOB_TOKEN`.
Configure an explicit read-only **self entry** in the project's job-token
allowlist, with default permissions disabled and only the required read policies.
The absence of a self entry preserves GitLab's broader same-project access.
Keep job-token Git push disabled and pipeline-variable overrides set to
`no_one_allowed`.

Fine-grained REST permissions do not constrain every registry operation.
Final container images therefore belong to a separate publication project,
while the source project's registry holds untrusted staging images. Only
trusted registry jobs receive credentials for the publication project.
`container release-images import` copies the admitted source digests into that
project and writes a publication ledger; source provenance and base-image
dependencies remain unchanged. The producer ledger is preserved.

### What is signed, and with what

| Object | Key | Variable |
|---|---|---|
| Release commit and final tag | SSH | `RELEASE_SSH_SIGNING_KEY` |
| Checksums, SBOMs, SLSA provenance, images and their attestations | Cosign key pair | `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE`, checked against `COSIGN_PUBLIC_KEY` and `cosign-public-key-sha256` |
| `.deb`, `.rpm`, `.apk` packages | OpenPGP | `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE`, `RELEASE_GPG_FINGERPRINT` |
| Request tag (verified, not made) | The signer's SSH key | `request-allowed-signers` |

The signing preflight passes only when the private key derives the trusted
public key whose digest the policy writes out, so a replaced key signs
nothing a verifier accepts. Create the pair with `cosign generate-key-pair` and
keep `cosign.pub` where your verifiers find it.

**The transparency log.** `transparency-log` is `none` by default, as on
Forgejo: signatures are checked against your key alone and nothing is written
to Rekor. With `none`, verify with the key and waive the log:

```shell
cosign verify-blob --key cosign.pub --insecure-ignore-tlog \
  --bundle checksums.txt.bundle --new-bundle-format checksums.txt
cosign verify --key cosign.pub --insecure-ignore-tlog \
  registry.example.com/team/app-releases/app@sha256:<digest>
```

`public` writes every signature to the public log, Rekor, permanently, and the
signer jobs then need `rekor.sigstore.dev` and `tuf-repo-cdn.sigstore.dev`;
verify without the flag. Every release of one organization takes the same
value, because a signature is verified the way it was made.
[The transparency log](forgejo.md#the-transparency-log) explains what each
value gives up.

**Keyless and KMS.** The CLI supports keyless Sigstore signing on GitLab: the
OIDC issuer is `https://gitlab.com` or your `CI_SERVER_URL`, and public Fulcio
trusts only gitlab.com. No shipped component uses it; a caller-owned job can run
`forge-ci release sign --method=sigstore` or `--method=kms`. See
[Signing Methods](verification.md#signing-methods-for-release-artifacts).

## Code scanning (SARIF)

GitLab does not ingest SARIF. The lint components convert their SARIF to the
GitLab SAST schema with `forge-ci security report to-gitlab-sast` and publish
it as `artifacts:reports:sast`, so findings appear on the merge request's
Security tab. Producing the report works on every tier; displaying it needs
GitLab Ultimate. `enable-gitlab-sast: false` keeps the SARIF as a plain
artifact only.

The conversion runs the engine the way `check` does: it checks out
`forge-ci-repository` at the component's commit and downloads the binary that
commit records from the project's generic packages, refusing any other
bytes. A component included by version therefore needs the engine of that
version's commit published; see
[Publish the recorded build](#publish-the-recorded-build). It is
best-effort: when the checkout, download or conversion fails, for example
because the binary is not published, the job logs `GitLab-SAST conversion skipped` and keeps the SARIF; the lint
gate itself is unaffected. `forge-ci security report upload-sarif` on GitLab
prints a notice and exits 0.

## Forks

The components name no organization in their trust inputs: an organization
serves them from its own copy on its own instance, and brings its own engine
package, images, keys and bot.

1. **Put the repository on your instance**, under any group and name.
   The history is SHA-1, so create a SHA-1 project. It must be readable
   without credentials, and its generic packages downloadable anonymously.
   Protect the branch and tags so pinned commits stay reachable.
2. **Choose and authenticate the commit** your consumers pin, and pin it in
   each releasing project's policy file. Review of that file on the protected
   branch approves the pin, and only release administrators push the
   protected `release-request/v*` tag that starts a release from it.
3. **If your copy changes Go code**, record the new build as
   [Development](DEVELOPMENT.md#the-forgejo-layers-engine-binary) describes.
4. **Publish the binary** for that commit to your project's generic packages;
   see [Publish the recorded build](#publish-the-recorded-build).
5. **Build your signer image** and pin its digest; see
   [Images of the release policy](#images-of-the-release-policy).
6. **Create the Cosign key pair, the release bot's deploy key, and the
   publication project**, and set the [variables](#secrets-and-tokens).
7. **Publish the Catalog components** by pushing a semver tag, if consumers
   use them by version.

What does not follow the fork, and must be overridden:

- Catalog runtime-image defaults name `ghcr.io/diggsweden`; set
  `runtime-image`.
- The lint components select `ghcr.io/itiquette/nanolinter`; pass a verified,
  available digest through `nanolinter-image`, including for a mirror.

`forge-ci-repository` and `consumer-project` are the fork's and the consumer's
paths; nothing else in the checksum-bound components or the lint components'
SAST conversion names an owner.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| A check or release job refuses before checkout: mutable reference | The component was included by tag or branch. Include checksum-bound components by full commit SHA. |
| A check job refuses: release secret present | A signing or publication variable reaches the check job. Scope it to its release environment. |
| `is available to the wrong release job` | A release variable is scoped too broadly. Use the scopes in [Secrets and tokens](#secrets-and-tokens). |
| A release job fails in its first lines | The component is not included at its `engine-sha`, the project is not `consumer-project`, the tag is not protected, or the pipeline is not the policy's child. |
| The engine download fails | The binary for that commit is not published, or the engine project or its packages need credentials. |
| `Check: release request` refuses the request | The tag is not SSH-signed by an allowed signer, or it is not the head of the release branch. |
| A release job fails uploading artifacts | The handoff is over the instance's artifact limit (100 MB by default); have the administrator raise it. |
| An image build step ends with `exec format error` | A `RUN` step for another architecture on an unprivileged runner; emulation on the host does not reach a nested build. Build on a runner of that architecture. |
| A project requested as SHA-256 is SHA-1 | The `support_sha256_repositories` feature flag was not enabled for the account when the project was created. |
| Warm base verification fails with a Cosign shim error | The build image exposes Cosign through a Mise shim; install the binary on `PATH` as in [Content-addressed base images](#content-addressed-base-images). |
| Findings do not appear on the Security tab | Display needs GitLab Ultimate, or the conversion was skipped; the job log says so. |

`forge-ci doctor` in a job prints what the detected forge supports.

## Limits

- **No snapshot flow.**
- **No Google Play, container-publishing or Xcode-build components**; Gradle
  publishing is not wired in `publish-forge-packages`.
- **No `:dev`/`:staging` promotion ladder.**
- **Checksum-bound jobs run on linux/amd64 only.**
- **No run-artifact API**: files move between jobs through native artifacts.
- **SAST display needs GitLab Ultimate.**
- **The engine project and its packages must be readable anonymously.**
- **The native release is under acceptance**; validate it on your instance as
  in [GitLab live acceptance](testing.md#gitlab-live-acceptance).
