# GitLab delivery

How `forge-ci` is used on GitLab CE/Free and above, hosted or self-hosted: the
CI/CD components under `templates/` that lint, check and build a merge request,
publish packages, and prepare, sign and publish a release. They call the same
engine commands as the other delivery layers.

Host the components, the engine package and the job images on your own
instance: an upstream release does not publish them for you. See
[Getting started](#getting-started), steps 1 and 2.

## What ships

Components must be hosted on the consumer's own GitLab instance: GitLab
includes a component only from a project on the same instance. There are two
families, which get the engine differently.

| Component | Family | Does |
|---|---|---|
| `check` | checksum-bound | Downloads the recorded engine, checks the consumer out at the pipeline commit and runs the consumer's check commands. |
| `release` (with `release-jobs`) | checksum-bound | The native release: request validation, changelog, signed release commit and final tag, key-free build, signing, image promotion, publication and cleanup, in separately scoped jobs. |
| `snapshot-images` (with `snapshot-image-jobs`) | checksum-bound | Snapshot images, and optionally snapshot files, of one branch, run from a policy project. See [Snapshots](#snapshots). |
| `nanolinter`, `megalinter` | Catalog | The merge-request lint gate, with findings published as a GitLab SAST report. |
| `build-go`, `build-cargo`, `build-maven`, `build-npm`, `build-gradle`, `build-gradle-android` | Catalog | One release build per artifact: `forge-ci build <ecosystem> run` in a runtime image. |
| `publish-forge-packages`, `publish-maven-central`, `publish-apple-appstore` | Catalog | Maven, npm and exported Gradle JVM packages to the GitLab Package Registry; Maven/Gradle to Central; App Store Connect. |

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
| GitLab | 18.7 or later. CE/Free is enough: protected-environment approvals and paid pipeline execution policies are not required. |
| Hosting | The components, and for checksum-bound components the engine project, on the **same instance** as the consumer. |
| Engine project | **Readable without credentials**, and its generic package downloadable anonymously. Downloads send no credential. |
| Runner architecture | **linux/amd64 or linux/arm64** for checksum-bound jobs: the engine is recorded for both, and each job runs the one of its runner's architecture. Catalog runtime images are linux/amd64 and linux/arm64, except Android (amd64 only). |
| Executor | Docker or Kubernetes. The components clear the entrypoint of `job-image`, `build-image`, the `runtime-image-<family>` images, `nanolinter-image` and `megalinter-image`, so an image that starts a tool of its own still runs the job's shell. |
| Job images | Digest-pinned; see [Images and the engine binary](#images-and-the-engine-binary). |
| Artifact size | Enough artifact capacity for the release hand-off; the GitLab default of 100 MB can be too small. See [Artifact size](#artifact-size). |
| Release branch | The project's default branch unless `release` is given another `release-branch`: the request must be its head. |
| macOS runner | With Xcode and a reviewed `forge-ci` on it, for `publish-apple-appstore` only. |
| SHA-256 repositories | Supported. Enable GitLab's `support_sha256_repositories` feature flag before creating the project; see [Troubleshooting](#troubleshooting). |

**Network.** Beside the instance itself, Catalog jobs pull their runtime images
from `ghcr.io` unless mirrored. The `release` component's render job
downloads the pinned changelog renderer archive. Checksum-bound jobs and the
lint components' SAST conversion fetch the engine binary from the instance's
own package registry.

## Getting started

First get a green merge-request gate; then configure release publishing.
After pushing a request tag, follow its pipeline's prepare, build, sign and
publish jobs and inspect the final release. If it fails, use
[Failed Release Recovery](failed-release-recovery.md#gitlab) rather than deleting final tags.

Two roles share the work. **The organization, once:** host the components
(steps 1 and 2), build the job, build and signer images, hold the Cosign key
pair and the release bot's deploy key, and own the policy project a release
runs from. **Each consumer, per project:** the merge-request gate in its
`.gitlab-ci.yml`, its `nanolinter.toml`, its project settings and the
protected variables (steps 3 to 6).

`forge-ci init` writes no Nanolinter or MegaLinter configuration: without
one, the lint engine uses its defaults and the component's required floor.
Preview the files with `forge-ci init <mirror URL> --dry-run`.

Then run `forge-ci setup status`: it lists the steps from here to a first
release, marks the ones the repository shows are done, and names the command
for each. GitLab takes the allowed signers as the `request-allowed-signers`
input, so run `forge-ci setup signer --dry-run` and paste the line it prints.

The organization's mirror can make `forge-ci init <mirror URL>` write a
consumer's files with every pin filled in. The operator commits
`.forge-ci/consumer-defaults.yml` to the mirror; init reads it at the release
tag it pins, never from a branch, and refuses unknown keys:

```yaml
forge: gitlab                       # required outside github.com
check-image: <registry>/<image>@sha256:<digest>   # the check component's job image
nanolinter-images:                                # by flavour, digest-pinned
  go: ghcr.io/itiquette/nanolinter:go@sha256:<digest>
runtime-images:                                   # build components' runtime-image,
  go: <registry>/forge-ci-runtime-go-1.27@sha256:<digest>   # by java, node, go, rust, android
```

A key left out leaves its placeholder, and init lists it. Consumers then run
`forge-ci init https://<host>/<owner>/forge-ci` in their repository; by hand,
the steps are these.

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
         server-url: https://gitlab.example.com
         nanolinter-image: "ghcr.io/itiquette/nanolinter:go@sha256:PIN_DIGEST"
     # check is checksum-bound: include it by the engine commit, never a version;
     # see Checksum-bound engine bootstrap in docs/gitlab.md.
     - component: gitlab.example.com/team/ci/forge-ci/check@PIN_FORGE_CI_SHA
       inputs:
         forge-ci-repository: team/ci/forge-ci
         server-url: https://gitlab.example.com
         job-image: registry.example.com/team/check-tools@sha256:PIN_IMAGE_DIGEST
         commands:
           - just test-unit
   ```

   Optionally copy the example's `nanolinter.toml` to the consumer's
   repository root. The lint component runs `nanolinter verify`, with the
   `required-lints` floor (default `secrets,sast`) applying either way.
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

Replace every pin with a reviewed digest; see
[lint images](components.md#lint-nanolinteryml-default). Each component's
Catalog page lists its inputs, from its `spec:`.

## Inputs and variables

Every input is declared in the component's `spec:` with its default and, for
images and pins, a validating regex. The inputs that are not obvious:

| Component | Input | Meaning |
|---|---|---|
| `check` | `forge-ci-repository` | The project serving the component and its engine package, including subgroups. |
| | `server-url` | This instance's URL as `CI_SERVER_URL` reads, `https://host[:port]` without a path, for example `https://gitlab.example.com`; required. Each job refuses to start when `CI_SERVER_URL` differs, and derives `CI_API_V4_URL` from it. See [The instance as a literal](#the-instance-as-a-literal). |
| | `job-image` | Digest-pinned image for the check job. |
| | `job-user` | UID or `UID:GID` the job runs as, for example `65532:65532` for the runtime images' `nonroot`. Empty, the default, keeps the image's user. See [The job's user](#the-jobs-user). |
| | `commands` | The consumer's check commands; a failure fails the job. `job-name` (default `check`) and `stage` (default `test`) name the job. |
| | `enforce-lint`, `enforce-vulnerabilities`, `enforce-application-tests`, `enforce-reproducible-builds` | The administrator's [check enforcement](admin-policy.md) for the gates the commands run through `forge-ci`; default `false`. |
| `release` | `forge-ci-repository`, `consumer-project` | The engine project, and the only project allowed to run this policy. |
| | `server-url` | As for `check`. |
| | `registry` | This instance's container registry as `CI_REGISTRY` reads, `host[:port]`, for example `registry.gitlab.example.com`, or empty when the project has none; required. GitLab configures it apart from the web host, so it is stated, not derived. Each job refuses to start when `CI_REGISTRY` differs; the project's image repository is derived from it. |
| | `job-image`, `signer-image` | Digest-pinned images for the key-free jobs, `Build: release files` among them, and for the signing jobs. |
| | `runtime-image-java`, `-node`, `-go`, `-rust`, `-android` | Digest-pinned runtime images of the toolchain families: the plan's version and build jobs of each family run in its image. Empty, the default, for a family the plan has none of; a planned family without one is refused at `Release: plan`. See [Plan-driven release jobs](#plan-driven-release-jobs). |
| | `job-user` | UID or `UID:GID` the `job-image` jobs run as, for example `65532:65532` with a runtime image. Empty, the default, keeps the image's user. |
| | `publication-project`, `publication-image-repository` | The separate project that owns final images, and the exact registry repository in it. |
| | `cosign-public-key-sha256` | SHA-256 of your Cosign public key, written out. |
| | `release-git-url`, `release-git-host-key-fingerprint` | The SSH write URL for the release commit and tag, and its independently supplied host key fingerprint, `SHA256:<base64>` alone. An empty URL is this project on the GitLab web host, port 22. |
| | `request-allowed-signers` | OpenSSH allowed-signers text for the people who may sign a request tag. |
| | `bot-name`, `bot-email` | The release bot identity; no default. |
| | `checks`, `build` | Consumer commands: checks on the request commit, and the build that writes `dist/release-files.json` and `release-notes.md`. |
| | `build-runner-tags-linux-amd64` | Required, never empty: runner tags of the consumer checks, family builds, release files and amd64 image and export jobs, whichever the image builder. Name a runner's own tags, such as `[privileged]` for a self-managed runner that permits user namespaces, or a `saas-linux-*` tag on GitLab.com. An untagged job could land on a runner of any platform, so there is no default. |
| | `release-images`, `artifacts-config`, `release-platforms`, `labels` | `true` builds the `containers:` of `artifacts-config` (default `.forge-ci/artifacts.yml`; without it the root `Containerfile`) in the release's image jobs, narrowed to `release-platforms`, with `labels` beside the standard ones. Default `false`: no image jobs. See [Release images](#release-images). |
| | `image-builder`, `builder-job-image` | The image jobs' builder, `buildah` (default) or `kaniko`, and the digest-pinned job image they run in: the Buildah job image or the kaniko job image. |
| | `build-runner-tags-linux-arm64` | Required: tags of a native arm64 runner for the arm64 image and export jobs, such as `[saas-linux-medium-arm64]` on GitLab.com, or `[]` to state there is none; the arm64 jobs exist only when it is set. |
| | `export-files`, `export` | `true` runs the `export` commands once per selected architecture, natively, in `builder-job-image` with `image-builder`, before the build job, and lays their `dist/` into its own. See [Files exported from a Containerfile](#files-exported-from-a-containerfile). |
| | `runner-tags`, `signer-runner-tags` | Runner tags of ordinary jobs, including request authorization, preparation and generated version jobs, and of the signer jobs, the ones in `signer-image` that hold the release credentials. Consumer build commands use the build input above. Empty, the default, leaves ordinary and signer jobs untagged. Write both as literals in this policy, never as a `$VARIABLE`: GitLab expands a variable in a tag, so a project could then choose the runner that sees the credentials. See [Runner roles across the forges](runners.md#runner-roles-across-the-forges). |
| | `changelog-creator`, `changelog-config`, `commit-body-config` | `git-cliff` (default) or `git-chglog`, and the consumer's configuration files, by default `.forge-ci/gitcliff/keepachangelog.toml` and `.forge-ci/gitcliff/minimal.toml`, copies of this repository's [`templates/gitcliff/`](../templates/gitcliff/) that `forge-ci init` writes. git-cliff renders offline, without remote metadata, and reads stable `vX.Y.Z` tags only, overriding any `tag_pattern` in the configuration. A SHA-256 repository needs `git-chglog`, since git-cliff cannot read one. |
| | `release-policy` | `stable` (default) or `stable-prerelease`, selected in the trusted external policy. GitLab has no native prerelease flag; see [release policy](workflows.md#stable-and-prerelease-policy). |
| | `release-branch` | The branch a release lands on; empty, the default, takes the project's default branch. The request must be its head. |
| | `transparency-log` | `none` (default) or `public`: whether Cosign signatures are also written to the public Sigstore transparency log. See [Signing](#signing). |
| | `sign-distro-packages` | Default `true`: GPG `.sig` sidecars for `.deb`, `.rpm` and `.apk`, which makes the GPG variables required. |
| | `release-asset-retention` | How many of the newest releases keep every file; older ones keep their checksums and signatures, upload and link both deleted. Empty, the default, deletes nothing. See [Old releases' files](#old-releases-files). |
| | `base-stages`, `base-plan` | The project's graph of base images and its planner; empty, the default, builds none. See [Base images](#base-images). |
| | `enforce-lint`, `enforce-vulnerabilities`, `enforce-application-tests`, `enforce-reproducible-builds` | The administrator's [check enforcement](admin-policy.md) for the checks and build jobs and the image scans, those of the base stages included; default `false`. |
| `nanolinter` | `forge-ci-repository`, `server-url`, `nanolinter-image`, `required-lints`, `disable-checks`, `enable-gitlab-sast` | The project serving the component and its engine package (required), the flavour image (required, pinned by digest, as the examples show), the mandated floor (default `secrets,sast`), the checks to leave out of the run (empty by default; it cannot remove one the floor names), and the SAST report. `enforce-lint` and `enforce-vulnerabilities`, default `false`, are the administrator's [check enforcement](admin-policy.md). |
| `megalinter` | `forge-ci-repository`, `server-url`, `megalinter-image`, `enable-gitlab-sast` | Required digest-pinned image; the rest as for `nanolinter`. |
| `check`, `nanolinter`, `megalinter`, `build-*`, `publish-*` | `runner-tags` | Runner tags of the component's job; empty, the default, leaves it untagged. `publish-apple-appstore` takes `runner-tags-darwin-arm64` instead, which defaults to a hosted macOS runner's. |
| `build-*` | `working-directory`, `skip-tests`, `enable-build-sbom`, `runtime-image` | Common to every build component; `runtime-image` is required and pinned by `@sha256:`. `build-go`, `build-cargo`, `build-gradle` and `build-gradle-android` also take `version`; `build-go` and `build-cargo` add `platforms` and `artifact-name`, `build-go` also `main-package` (relative to `working-directory`), `build-maven` `build-type` and `profile`, `build-gradle-android` the module, flavour and signing inputs. `enforce-application-tests`, default `false`, is the administrator's [check enforcement](admin-policy.md). |
| `publish-*` | `project-type`, `working-directory`, `runtime-image` | `runtime-image` as for `build-*`. `publish-maven-central` adds `profile` and `settings-path`; `publish-apple-appstore` takes none of these, only `platform`, `skip-validation`, `runner-tags-darwin-arm64` and `macos-image`. |

The standalone `publish-maven-central` component also accepts `release-policy`.
It binds the selected input before authorization; a project environment variable
cannot widen the default stable-only policy. Snapshot publishing is independent.

## Secrets and tokens

Use **project-level** protected variables with these environment scopes:

| Variable | Scope | Used for |
|---|---|---|
| `COSIGN_PUBLIC_KEY` | `*` (public value) | The PEM text of your Cosign public key; it must hash to `cosign-public-key-sha256`. |
| `RELEASE_SSH_SIGNING_KEY` | `release/prepare` | The signed release commit and final tag; a deploy key that may push to the project. |
| `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE` | `release/registry/signing` | Provenance and image signatures and attestations, and checksum and SBOM signatures unless `artifacts.yml` says `sign.method: gpg` or `sign.files-method: gpg`. |
| `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE`, `RELEASE_GPG_FINGERPRINT` | `release/registry/signing` | Distro-package signatures, when `sign-distro-packages` is on, and checksum and SBOM signatures, when `artifacts.yml` says `sign.method: gpg` or `sign.files-method: gpg`. |
| `RELEASE_REGISTRY_USER`, `RELEASE_REGISTRY_TOKEN` | `release/registry/*` | Importing, signing, promoting and cleaning up images in the publication project. |
| `RELEASE_TOKEN` | `release/publish` | Creating the GitLab release and its assets. |
| `RELEASE_PACKAGE_TOKEN` | `release/packages` | Publishing the release's npm packages: a project deploy token with `read_package_registry` and `write_package_registry` only. See [Package publication](#package-publication). |

Each release job refuses to start when a release secret it does not use is
visible to it (`is available to the wrong release job`), so a wrongly scoped
variable fails closed.

Set the project's **Minimum role to use pipeline variables** to **No one
allowed** (Settings > CI/CD > Variables, or the project API's
`ci_pipeline_variables_minimum_override_role: no_one_allowed`). A pipeline
variable outranks the job's own variables and GitLab's predefined ones, and so
do project and group variables. Whoever may set one could point
`CI_SERVER_URL`, `CI_API_V4_URL` or `CI_REGISTRY` at another host. The jobs
do not take a token's destination from those variables (see
[The instance as a literal](#the-instance-as-a-literal)), so the setting is
defence in depth: it keeps every other predefined variable as GitLab set it.

### The instance as a literal

Every policy component (`check`, `nanolinter`, `megalinter`, `release` and
`snapshot-images`) takes the instance's URL as `server-url`, and `release`
and `snapshot-images` take its container registry as `registry`, from the
administrator's include. Each job refuses to run when `CI_SERVER_URL`, or
`CI_REGISTRY`, is not the literal, before it fetches anything or reads a
token; it then sets `CI_API_V4_URL` to `<server-url>/api/v4` and, for
`release`, `CI_REGISTRY_IMAGE` to
`<registry>/<consumer-project>` in lower case, as GitLab derives them. A
variable that points the jobs elsewhere stops them instead of receiving the
job token or a release token. The URL is an origin, `https://host[:port]`: an
instance served under a relative URL path is not supported.

This binds the destination, not the runner's network trust. The jobs still
read the CA and proxy variables: `CI_SERVER_TLS_CA_FILE`, `GIT_SSL_CAINFO`,
`CURL_CA_BUNDLE`, `SSL_CERT_FILE`, `SSL_CERT_DIR`, `NODE_EXTRA_CA_CERTS` (the
release sets it from `CI_SERVER_TLS_CA_FILE` for npm) and the proxy settings.
A project or group variable can override each of them, and a CA of an
attacker's choice can impersonate even the literal server name. So whoever
may set project or group variables is trusted with what these jobs hold. Keep
CA and proxy settings in the runner's configuration, protect the
release-request tags and the release branch, scope each secret as the table
above lists, and keep the secrets out of reach of the consumer project's
maintainers. Runner-level variables such as `CI_DEBUG_TRACE` take effect
before any script runs, so restrict them at the instance or group.

**Lock the variables where you have pipeline execution policies.** In GitLab
18.1 or later, a pipeline execution policy can set `variables_override` with
`allowed: false`, so a project cannot override the variables of the policy's
jobs, the ones that hold the credentials. Name the variables a project may
still set as exceptions:

```yaml
variables_override:
  allowed: false
  exceptions: [MY_PROJECT_SETTING]
```

This keeps a project from setting CA and TLS variables such as
`SSL_CERT_FILE` and `CI_SERVER_TLS_CA_FILE` for those jobs. It is not on by
default, and it narrows the risk above without removing it.

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

The build and publish components require `runtime-image`: a component cannot
tell which registry serves the images, so it names none. Pin it by digest to
the runtime images the GitHub workflows use, or, for an installation that must
not pull from `ghcr.io`, to your mirror of them. `forge-ci plan
gitlab-build-pipeline` takes the same images as `--runtime-image-java`,
`-node`, `-go`, `-rust` and `-android`, one per toolchain family the plan
builds, and the `release` component as its `runtime-image-<family>` inputs. [Runtime Images](runtime-images.md) has the roster and pinning
conventions.

### The job's user

The runtime images run as root and carry the ordinary user `nonroot`,
65532:65532. A check that needs ordinary-user filesystem semantics runs a
runtime image with `job-user: "65532:65532"`. Give the group too: on
Kubernetes a UID alone can run the job with GID 0. The runner must admit the
user: on Docker, through its `user` or `allowed_users` settings when it sets
them; on Kubernetes, any UID but 0 unless `allowed_users` narrows it, and
runner settings that fix `runAsUser` win. Kubernetes needs GitLab 18.0 and
GitLab Runner 17.11 or later.

Each job refuses to run as any other user than `job-user` says, before any
checkout, and prints the user it runs as, `job user: uid=65532 gid=65532`.
In `release` only the `job-image` jobs take `job-user`; the family, image
build and signer jobs run as their images' own users. The images need `id`,
as coreutils and BusyBox provide.

### Images of the release policy

The policy selects separate digest-pinned `job-image`, `signer-image` and
`runtime-image-<family>` inputs. Run checks, rendering and the release files
job as an ordinary user; permission tests must run with ordinary-user
filesystem semantics. A runtime image with `job-user: "65532:65532"` does
that, as [The job's user](#the-jobs-user) describes; the jobs in the other
images keep those images' users. The plan's version and build jobs of a
toolchain family run in that family's `runtime-image-<family>`, the
consumer's build tools for it: `runtime-image-java` for Maven and Gradle,
`-node` for npm, `-go`, `-rust` for Cargo and `-android`.

With `release-images`, the image build jobs run in `builder-job-image`, as
root, whichever the builder. For Buildah, the default, it is the image of
[containers/buildah-job/Containerfile](../containers/buildah-job/Containerfile),
built and pinned by digest like the signer image; it carries Buildah and its
helpers, and the job installs nothing. Buildah needs user namespaces. Where
only some runners allow them, tag those runners and name the tags in
`build-runner-tags-linux-amd64`: the consumer checks, family builds, release
files and amd64 image and export jobs run there. Ordinary and version jobs
stay on `runner-tags`, and signing jobs on `signer-runner-tags`, untagged by
default.

Where no runner allows them, set `image-builder: kaniko` and
`builder-job-image` to the kaniko job image of
[containers/kaniko-job/Containerfile](../containers/kaniko-job/Containerfile),
pinned by digest. The image build jobs then run in it, as root, on the same
build runner inputs. Before a Containerfile is built, the job checks that the
image is the kaniko job image built from your engine project. See
[Building on the pool with kaniko](runners.md#building-on-the-pool-with-kaniko)
for what kaniko does not do.

The signer image is built from the same definition as the Forgejo layer's,
`containers/signer/Containerfile`, and every signing job runs the same strict
toolchain check (`.forgejo/scripts/verify-signer-toolchain.sh`) first. No
GitLab job builds it: build it from the commit you pin, push it to your
registry and pin it by digest. [The signer image](forgejo.md#the-signer-image)
describes its contents and a manual `buildah build`.

### Checksum-bound engine bootstrap

The engine binary has one build definition and one checksum per
architecture, shared with Forgejo: `scripts/ci/engine-binary.sh` and
`.engine/bin/forge-ci-linux-<amd64|arm64>.sha256`. `publish` uploads to
GitLab when `FORGE_CI_PACKAGE_PROVIDER=gitlab`, as shown below.

Set these coordinates for GitLab:

```bash
export FORGE_CI_PACKAGE_PROVIDER=gitlab
export CI_SERVER_URL=https://gitlab.example.com
export FORGE_CI_REPOSITORY_URL=https://gitlab.example.com/team/ci/forge-ci.git
```

The resolver uses the full project path, including subgroups:

```text
https://gitlab.example.com/api/v4/projects/team%2Fci%2Fforge-ci/packages/generic/forge-ci/<sha256>/forge-ci-linux-<amd64|arm64>
```

The engine repository must be on `CI_SERVER_URL`. The binary is verified
against the recorded SHA-256 before it runs.

#### Publish the recorded build

From a trusted checkout at the selected engine commit, with its pinned Go
toolchain and cached dependencies, for each architecture:

```bash
for arch in amd64 arm64; do
  bash scripts/ci/engine-binary.sh build "$arch" "/absolute/private/staging/forge-ci-$arch"
  bash scripts/ci/engine-binary.sh publish "$arch" "/absolute/private/staging/forge-ci-$arch"
done
```

Publication requires `FORGE_CI_PACKAGE_TOKEN` in the environment, supplied
by the CI secret mechanism rather than placed in a command argument or URL.
For GitLab, `FORGE_CI_PACKAGE_TOKEN_TYPE` selects the authentication header:

| Value | Header | Use |
|---|---|---|
| `private` (default) | `PRIVATE-TOKEN` | Access token authorized to publish to the engine project |
| `job` | `JOB-TOKEN` | Native `CI_JOB_TOKEN` with the required project package access |

For publication from the engine project's own job, once per architecture:

```bash
FORGE_CI_PACKAGE_TOKEN_TYPE=job \
  FORGE_CI_PACKAGE_TOKEN="${CI_JOB_TOKEN:?}" \
  bash scripts/ci/engine-binary.sh publish "$arch" "/absolute/private/staging/forge-ci-$arch"
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
  `javascript-typescript`, `rust` or `swift`; required, no default), with
  `required-lints` as a floor the project cannot lower. Pin the image by digest.
  In a merge-request pipeline it checks out the whole history (`GIT_DEPTH: 0`)
  and fetches the target branch as `origin/<target>`, with the job token, so
  secret scanning and commit checks see what the merge request adds.
- **`megalinter`** runs MegaLinter against `.mega-linter.yml`. Its image is
  required and must be pinned by `@sha256:`; a tag is refused before the job is
  created, since the job container receives the checkout, CI variables and the
  job token.

Both publish findings as a SAST report; see [Code scanning](#code-scanning-sarif).
The conversion needs Bash, Git, curl and `sha256sum` in the lint image;
Nanolinter's flavours and MegaLinter have them.
The ecosystem examples include `nanolinter` in their stack's flavour. To use
MegaLinter instead, include it in its place:

```yaml
include:
  - component: gitlab.example.com/team/ci/forge-ci/megalinter@0.90.0
    inputs:
      forge-ci-repository: team/ci/forge-ci
      server-url: https://gitlab.example.com
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
      server-url: https://gitlab.example.com
      job-image: registry.example.com/team/check-tools@sha256:PIN_IMAGE_DIGEST
      commands:
        - just test-unit
        - just lint
```

Replace both pins with reviewed digests. The image must contain Bash, Git,
curl, `sha256sum` and the consumer's check tools. To check a monorepo's parts
in their own images, include the component once per toolchain and give each
include its own `job-name`: GitLab merges two jobs of one name into one, so
two includes left at `check` run only one of the checks, silently.

The job puts a `forge-ci` on `PATH` that runs the checksummed engine with the
component's four [enforcement inputs](admin-policy.md) as flags, and prints
the effective policy before the commands run. The settings are the
administrator's only where the include is; see
[Who sets them](admin-policy.md#gitlab).

The component refuses known release secrets and `GITLAB_TOKEN` before
checkout, and refuses an include by tag or branch. The caller still owns the
pipeline rules, required checks and protected settings; for a required gate
the administrator controls, see
[The merge-request gate under a policy](#the-merge-request-gate-under-a-policy).
Checkout uses the job token, which works for public and private consumers
with SHA-1 or SHA-256 repositories; see
[Git operation credentials](providers.md#git-operation-credentials).

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
| `build-gradle` | `gradle-tasks` (default `assemble`) | `build/libs/` of the project and its subprojects, not of `buildSrc`; SBOM under `build/reports/` |
| `build-gradle-android` | APK and AAB, optionally signed | `<module>/build/outputs/`, SBOM under `build/reports/` |

GitLab has no run-artifact API, so a later job receives files through native
`artifacts:` and `needs:`, never `forge-ci artifact download`. GitLab also
cannot expand a matrix from a value computed at run time: for an
`artifacts.yml` with several artifacts, `forge-ci plan gitlab-build-pipeline`
generates a child pipeline with one build component per planned item, and a
trigger job runs it. It refuses a plan that names a component that does not
exist. See the [monorepo example](../examples/monorepo/gitlab/.gitlab-ci.yml).

In the native release, the build is the consumer's own `build` commands,
run without signing keys; see [Plan-driven release jobs](#plan-driven-release-jobs).

## Releases and promotion

### Native release policy

The `release` component runs on GitLab CE/Free. A project selects
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
      server-url: https://gitlab.example.com
      registry: registry.gitlab.example.com
      consumer-project: team/app
      job-image: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:PIN_DIGEST
      job-user: "65532:65532"
      runtime-image-go: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:PIN_DIGEST
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
      build-runner-tags-linux-amd64: [saas-linux-small-amd64]
      build-runner-tags-linux-arm64: []
      build:
        - bash .gitlab/build-release.sh
```

#### Release images

The release builds its container images itself, in jobs of its own between
the build and signing, as the Forgejo
[release-images workflow](forgejo.md#release-images) does. With `release-images: true`:

1. `Images: plan` plans the `containers:` of `artifacts-config` at the release
   commit, one build per container and architecture, each published to this
   project's registry, `CI_REGISTRY_IMAGE`, under `staging-<final tag>`.
   `Check: release request` makes the same plan at the request commit first,
   so a stage that would stop later stops the release before `Prepare`
   pushes the release commit and tag: a container that builds linux/arm64
   without `build-runner-tags-linux-arm64` (narrow `release-platforms` or name the
   runner), an empty `builder-job-image`, whichever the builder, or a
   `publication-project` that is not a separate project. An empty
   `builder-job-image` GitLab already refuses as it creates the pipeline,
   since the image jobs then name no image.
2. `Images: build amd64` and, with `build-runner-tags-linux-arm64`, `Images: build
   arm64` build every planned image of their architecture natively to OCI
   layouts in `builder-job-image`, with no registry write. The engine passes
   the build no credential of the job, but this is the trust `Build: release files` gives
   the consumer, not a boundary: under kaniko the Containerfile's `RUN` steps
   run in the job's own container and can read its job token. The build
   therefore pulls its `FROM` images anonymously: a base image in this
   project's registry must be public, and the dependency proxy, which always
   asks for credentials, cannot serve one. The job token is
   not handed to the builder for pulls because it is not pull-only: it can
   also push to this project's registry. The checkout
   is the release commit with the build's `image-context/` laid over it, for
   the files the Containerfiles copy that are not in the repository, such as
   the jar the build made.
3. `Images: publish` admits, verifies and scans the layouts of every image
   and, only when all of them pass the scan gate, pushes each image's index
   with the job token, records it in `release-images.json` with the scan
   results, and joins them to the build's `dist/` in a hand-off of its own,
   digested again. A finding in any image leaves the registry untouched.
4. `Sign: files and images` reads that hand-off, and only it, and imports the
   staging images into the publication project.

`Publish: tidy up` removes the candidate tags in the publication project,
not the `staging-<final tag>` tags in this project's registry. Set a bounded
registry retention for those tags yourself, keeping the ones active releases
and their recovery need. `Images: publish` needs Syft in `job-image`, which
every forge-ci runtime image carries. Base images are built by the
release's `base-stages` jobs instead; see [Base images](#base-images).

The build itself pushes no image: a `dist/release-images.json` from the
`build` commands stops its hand-off. The images carry the source, revision,
title, version, url, documentation, ref.name and created labels, and after
them the policy's `labels`, one `key=value` a line:

```yaml
release-images: true
labels: |
  org.opencontainers.image.licenses=EUPL-1.2
  org.opencontainers.image.vendor=Example Agency
```

`Check: release request` refuses a label that names one of the standard
eight, which describe where the image came from, a key that is not a
lowercase dotted name, a key named twice, and `labels` without
`release-images`. The image jobs take no build secrets; a binary extraction
is the export jobs' work.

#### Files exported from a Containerfile

A release file the Containerfile builds, such as the binary of a
`FROM scratch AS export-binary` stage, is exported by its own jobs, so that
it is built with the selected image builder on a runner of its own
architecture, never emulated. With `export-files: true`:

1. `Check: release request` refuses an export the jobs would not carry out:
   an empty `builder-job-image`, or a `release-platforms` that names
   linux/arm64 without `build-runner-tags-linux-arm64`. Export commands without
   `export-files` would have no effect and are refused too.
2. `Build: export amd64` and, with `build-runner-tags-linux-arm64`, `Build: export
   arm64` run the `export` commands on the release commit in
   `builder-job-image` with `image-builder`, as the image jobs do, for each
   architecture `release-platforms` selects; empty selects linux/amd64, the
   platform a container declares when it names none. A container that
   declares linux/arm64 then needs `release-platforms` named, or the request
   check refuses, so the images and the exports build the same
   architectures; `build-runner-tags-linux-arm64` serves arm64 and never selects
   it. `EXPORT_PLATFORM` and `EXPORT_ARCH` name the
   job's architecture, and the job holds no credential of its own once the
   builder is admitted. The commands write `dist/`, regular files and
   directories only.
3. `Build: release files` lays every export's `dist/` into its own before
   the `build` commands run, refusing a path two of them hold, so the build
   declares the files with the rest.

```yaml
export-files: true
export:
  - >-
    forge-ci container build --mode local --target export-binary
    --platform "$EXPORT_PLATFORM" --output-dir "dist/linux-$EXPORT_ARCH"
  - >-
    forge-ci container suffix-extracted-binaries --binaries-dir "dist/linux-$EXPORT_ARCH"
    --arch "$EXPORT_ARCH" --expected-names app
```

#### Plan-driven release jobs

The release child is one pipeline, as GitHub's release is one run: checks,
version, release commit, builds, images, signing and publication, its jobs
handing their files on through native `needs:`. `Release: plan`, in the
parent pipeline before the child starts, plans the request commit's
`artifacts-config` (empty is `.forge-ci/artifacts.yml`) with
`forge-ci plan gitlab-release-pipeline`, the planner GitHub's release uses,
and writes the child's plan-driven jobs. The child is two files: the policy's
`templates/release-jobs.yml`, included with the policy's inputs as written,
and that generated file, which only names jobs, each extending its family's
hidden job in `release-jobs.yml`. It holds no policy value and no consumer
string, so what a job may do stays in the pinned template. `Release: plan`
holds the job token alone and runs no consumer code.

| Job | When | Image | Does |
|---|---|---|---|
| `Prepare: version java`, `node`, `rust` | the plan bumps Maven, npm or Cargo projects | `runtime-image-<family>` | Runs that family's native version tool (`mvn versions:set`, `npm version`, Cargo) on the request for each of its planned projects, and hands over only the patch of their native version files and its digest; holds no credential. |
| `Build: java`, `node`, `go`, `rust`, `android` | the plan builds projects of the family | `runtime-image-<family>` | Runs `build` on the release commit with `FORGE_CI_BUILD_FAMILY` set to the family and `FORGE_CI_ARTIFACTS` to its planned artifacts, space-separated; hands on `dist/` and `image-context/` without a manifest. |
| `Publish: npm packages` | an npm project lists `forge-packages` under `publish-to` | `runtime-image-node` | After `Publish: release`, publishes the signed release's packed packages to this project's package registry; see [Package publication](#package-publication). Runs no consumer command, on the signer's runner, and holds `RELEASE_PACKAGE_TOKEN` alone. |

The families are java (Maven, Gradle), node (npm), go, rust (Cargo) and
android (Gradle Android); a family the plan uses needs its
`runtime-image-<family>`, and `Release: plan` refuses the release without it.
A Cargo or Go artifact built `container-first` is built by its Containerfile
(see [Files exported from a Containerfile](#files-exported-from-a-containerfile)),
not by a family job, though it is still version-bumped.

`Build: release files` then plans the release again, requires a hand-off
from exactly the planned build jobs, lays every family's and export job's
`dist/` and `image-context/` together, refusing a top-level path two of them
hold, and
runs `build` once more in `job-image` as `job-user`, with both variables
empty: that run writes the release notes and declares the files with
`forge-ci release files declare`. One script serves every run; a sketch for a
Maven backend and an npm frontend:

```bash
case "${FORGE_CI_BUILD_FAMILY}" in
java)
  forge-ci build maven run --working-dir services/backend --expect-version "${RELEASE_VERSION}"
  mkdir -p dist/backend && cp services/backend/target/backend-api-*.jar dist/backend/
  ;;
node)
  npm --prefix packages/frontend ci && npm --prefix packages/frontend run build
  mkdir -p dist/frontend && tar -C packages/frontend -czf dist/frontend/frontend-ui.tgz dist
  ;;
'')
  forge-ci release notes --changelog-file CHANGELOG.md --release-version "${RELEASE_TAG}" --target-file dist/release-notes.md
  forge-ci release files declare --asset 'dist/backend/*' --asset 'dist/frontend/*'
  ;;
esac
```

Without a declaration the release plans no family: `Build: release files`
runs `build` alone, in `job-image`.

#### Version bump

The signed release commit carries the release version in the version files
of every project the declaration plans, and a copy of the full changelog in
each project's directory. There is no `version-bump` input: a release without
a declaration, or whose projects have no version file (Go), carries the
changelog alone. Either changelog renderer bumps.

The `Prepare: version <family>` jobs run the native version tool in their
runtime image and hold no credential. `Prepare: release commit and tag`,
which holds the SSH signing key, runs no build tool: it admits their patches
only as version substitutions of the planned project files. Every later job
that checks the release commit out checks again that it holds the changelog,
its copies and the planned version changes, and nothing else.

#### The merge-request gate under a policy

The merge-request gate is the policy's, not the consumer's. A consumer file
that includes `nanolinter` or `check` itself can pass `enforce-lint: false`
and gets a green job, so those jobs are never the required gate. The policy's
own YAML includes the gate components, with the `enforce-*` inputs written
out, and takes from the consumer only the commands, as a script in its
repository.

Prefer a pipeline execution policy in inject mode (`inject_policy`) where the
tier allows it; [Who sets them](admin-policy.md#gitlab) explains why. With the
project's CI configuration path naming the policy file instead, the policy
includes the consumer's file for merge-request pipelines only, read at the
merge request's commit, **before** the gate components, and nothing of the
consumer's for release tags:

```yaml
include:
  # Optional jobs of the consumer's own, for merge requests only. Included
  # first: a job it defines with a gate's name is merged under the gate's,
  # which states allow_failure, before_script, script and after_script.
  - project: team/app
    ref: $CI_COMMIT_SHA
    file: .gitlab/merge-request.yml
    rules:
      - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
  # The gates, with the administrator's settings written out.
  - component: gitlab.example.com/team/ci/forge-ci/nanolinter@0.90.0
    inputs:
      forge-ci-repository: team/ci/forge-ci
      server-url: https://gitlab.example.com
      nanolinter-image: "ghcr.io/itiquette/nanolinter:go@sha256:PIN_DIGEST"
      enforce-lint: true
      enforce-vulnerabilities: true
    rules:
      - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
  - component: gitlab.example.com/team/ci/forge-ci/check@PIN_FORGE_CI_SHA
    inputs:
      forge-ci-repository: team/ci/forge-ci
      server-url: https://gitlab.example.com
      job-image: registry.example.com/team/check-tools@sha256:PIN_DIGEST
      commands:
        - bash .gitlab/check-release.sh
      enforce-lint: true
      enforce-vulnerabilities: true
      enforce-application-tests: true
      enforce-reproducible-builds: true
    rules:
      - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
  # The release, for tag pipelines only.
  - component: gitlab.example.com/team/ci/forge-ci/release@PIN_FORGE_CI_SHA
    inputs:
      # as above
    rules:
      - if: '$CI_COMMIT_TAG'
```

Keep that rule on the release. GitLab checks every job's stage when it
creates a pipeline, whatever the job's own rules: a consumer file that
declares `stages`, as the example gates do with `test` and `build`, leaves
out the release job's `deploy`, and without the rule every merge-request
pipeline is refused as a configuration error.

The include rules only decide when a file is read. The policy declares the
pipelines it runs in with its own `workflow: rules`, admitting merge-request pipelines beside its release
pipelines. A consumer file merged before them can still add keys the gates do
not state, such as `rules`, `needs` or `variables`: keep the consumer file to
jobs of its own, with names of its own, and require the gate jobs by name.

Release-request tag pipelines stay policy-only.

A merge request from a fork gets its pipeline in the fork by default, where
the gate compares with the fork's copy of the target branch and the fork's job
token cannot read a private project. Turn on the project's setting to run
pipelines in the parent project for merge requests from forks: the gate then
runs here, against the real target, once a maintainer starts it.

#### The release jobs

The parent trigger holds a resource group through a mirrored child pipeline,
serializing the entire release. The child uses native `needs` and artifacts;
artifact files are never sourced or declared as dotenv reports. Request tags
must be protected and SSH-signed against the allowed-signers text in the
trusted policy. Checks, changelog rendering and builds run without signing keys.

The child runs these jobs, in six stages: check, prepare, build, images, sign
and publish.

| Job | Image | Does |
|---|---|---|
| `Check: signing access`, `Check: publishing access`, `Check: registry access` | signer | The toolchain check, the Cosign key against the trusted public key, the GPG variables when `sign-distro-packages` or the declaration's `gpg` needs them, and the release and registry tokens, before anything else. Registry access is scheduled only when the protected `release-images` input is true or `base-stages` names a stage; archive/export-only releases need no publication-registry token. |
| `Check: release request` | job | On `runner-tags`, validates the signed request and that it is the head of the release branch; executes no consumer commands. |
| `Check: consumer checks` | job | After authorization, freshly checks out and revalidates the immutable request, then runs `checks` on `build-runner-tags-linux-amd64`. |
| `Prepare: changelog` | job | After authorization, beside the consumer checks, renders the changelog and commit message. Consumer configuration runs only here and in the version jobs. |
| `Prepare: version <family>` | runtime | One per family the plan bumps with a native tool; sets the release version in the family's project files and hands over the patch; holds no credential. See [Plan-driven release jobs](#plan-driven-release-jobs). |
| `Prepare: release commit and tag` | job | Verifies the version patches, applies the plan's bump, commits it with the changelog and pushes the signed release commit and final tag with `RELEASE_SSH_SIGNING_KEY`. |
| `Build: <family>` | runtime | One per family the plan builds; checks out the final tag and runs `build` for the family's artifacts on `build-runner-tags-linux-amd64`; hands `dist/` and `image-context/` on. |
| `Base: …` | see [Base images](#base-images) | One set per stage of `base-stages`: plans, builds, signs and promotes the project's base images before the release's files are built. |
| `Build: export amd64`, `Build: export arm64` | builder | With `export-files: true`, run the `export` commands per architecture. See [Files exported from a Containerfile](#files-exported-from-a-containerfile). |
| `Build: release files` | job | On `build-runner-tags-linux-amd64`, checks out the final tag, lays the family and export files together and runs `build` to declare them; hands `dist/` on with its digest. |
| `Images: plan`, `Images: build amd64`, `Images: build arm64`, `Images: publish` | job; builder for the builds | With `release-images: true`, plan, build and push the release's candidate images. See [Release images](#release-images). |
| `Sign: files and images` | signer | Imports images into the publication project, signs files, packages, provenance and images. |
| `Publish: images` | signer | Moves final image tags, with a journal. |
| `Publish: release` | signer | Creates the GitLab release with notes and assets. |
| `Publish: tidy up` | signer | Removes candidate tags. |
| `Summary: release` | job | After a successful release, prints the release overview as a "Summary" section of its log; holds no release secret. |

Each job's role is fixed in the template and decides which secret it may
see, its user and its work directory; no job trusts `CI_JOB_NAME`.

**The policy's values are literals of the job's script.** A project, group
or instance variable reaches every job and outranks the job's own
`variables:`, so no value the policy relies on is a job variable: each job
assigns the policy's inputs as read-only shell variables, and a variable of
the same name cannot change them. The same holds in `snapshot-images`,
`check` and the lint components, except the lint components'
`forge-ci-repository`, which may name `$CI_PROJECT_PATH`. The runner
settings `RC_JOB_USER`, `GIT_STRATEGY` and `GIT_SUBMODULE_STRATEGY` must
stay job variables, and a job refuses any that differs from its literal.
Free-text inputs may not contain a single quote.

**The request is the head of the release branch.** As on Forgejo, the release
commit is pushed on top of the request commit without force, so a request that
is not the tip of the release branch is refused. The branch is the
`release-branch` input, else the project's default branch. The deploy key of
`release-git-url` must be allowed to push to that protected branch. Push the
request tag at the tip and freeze merges until `Prepare: release commit and
tag` has pushed; a request whose branch moved first is spent.
[The request is the head of the release
branch](forgejo.md#the-request-is-the-head-of-the-release-branch) gives the operator rules.

#### Artifact size

The release hands its files between jobs as artifacts, and GitLab's default
archive limit, 100 MB, can be too small for a multi-platform release. Only the
GitLab administrator can raise it: measure the largest hand-off and set a
bounded limit with some headroom.

### The published release is read back

After `Publish: release`, the job reads the release back with the job token
and compares its files with the planned ones, signatures and provenance
included. GitLab's release links carry no size, so only names are compared.
A planned file that is missing fails the job; a file the plan does not name
is a warning. The general check is in
[verification](verification.md).

### Old releases' files

With `release-asset-retention: <N>`, the newest N releases keep every file;
each older release keeps its checksum manifests, signatures, bundles and
SBOMs, and loses the rest. The releases, their notes and their tags stay.
Empty, the default, deletes nothing. `forge-ci release asset-retention
--repository <owner/repo> --keep <N>` lists what a value would delete. A
GitLab release file is a project upload behind a link, so the upload is
deleted with its link; a link to anything else, such as a package file,
loses only the link. Deleting an upload needs GitLab 17.2 or later and
`RELEASE_TOKEN` with the Maintainer role.

### Serialization and promotion recovery

The parent resource group covers the child pipeline from the checks to the
last publish job, so a second release waits until the first has finished.

Promotion moves final tags in the publication project; there is no
`:dev`/`:staging` ladder on GitLab. Each `Publish: images` attempt keeps its
promotion journal as an artifact, also when it fails; keep the first failed
attempt's journal if you may need to roll back. A failed pipeline rolls
nothing back by itself. See
[Failed Release Recovery](failed-release-recovery.md#gitlab).

### Base images

A project whose product is a graph of base images, a root and the flavors
built on it, names the graph in the release policy. The policy then builds,
signs and promotes each stage of it before the release's files are built.
See the [base-images example](../examples/base-images/README.md).

| Input | Value |
|---|---|
| `base-stages` | The stages, separated by spaces and in build order: a flavor for a root, `flavor:parent` for a child of a parent that an earlier stage builds, and `flavor+flavor:parent` for siblings on one parent. `base tools+debug:base` is a root and two flavors on it. Empty, the default: no base stage. |
| `base-plan` | The project's planner, for example `bash base-plan.sh`. Its words are split at spaces and run without a shell. |

A release with `base-stages` needs `base-plan` and `builder-job-image`, and a
child stage needs `image-builder: buildah`: its build job holds no registry
credential, and kaniko has no local store to load the verified parent into.
`Release: plan` refuses the release otherwise, before anything runs.

Each stage is these jobs, named after it, in the `build` stage:

| Job | Image, runner | Holds | Does |
|---|---|---|---|
| `Base: plan <stage>` | job image, `runner-tags` | no release secret | Runs `base-plan` at the release commit and admits what it prints as one stage. A child's plan takes its parent from the hand-off of the parent's promotion. |
| `Base: parent <stage>` (child only) | signer | `RELEASE_REGISTRY_TOKEN` | Verifies the promoted parent digest again against the pinned Cosign key, then fetches it for the build. Runs nothing of the project's. |
| `Base: build <stage>` | `builder-job-image`, `build-runner-tags-linux-amd64` | no release secret | Builds every image of the stage to an OCI layout, a child from the bytes the parent job fetched. |
| `Base: publish <stage>` | job image, `signer-runner-tags` | `RELEASE_REGISTRY_TOKEN` | Scans the layouts and pushes unsigned `staging-` candidates. Runs nothing of the project's. The job image must carry Syft. |
| `Base: sign and promote <stage>` | signer | the Cosign key, `RELEASE_REGISTRY_TOKEN` | Signs the candidates and their SBOM and provenance, verifies each against the pinned key and the declared inputs, then moves the final tags. Hands each flavor's `{"ref":...,"input_id":...}` on. |

`Base: all promoted` gathers those hand-offs, and `Build: release files`
waits for it.

**The planner.** The plan job calls

```text
<base-plan> <flavor>[,<flavor>...] <repository> [<parent-ref> <parent-input-id>]
```

and reads the stage's plan from its output, in the shape
[`base-images.yml`](../.forgejo/workflows/base-images.yml) describes. The
graph, the input IDs and the content-addressed tags are the project's. The
policy holds a plan to one complete stage in the bases repository: exactly
the flavors `base-stages` names for the stage, each under the final tag
`<base-input-id>-<flavor>`, and, for a child, on the one parent it names.
Every job of the stage checks this again before it builds, pushes or signs
anything.

**Where the images live.** In `<publication image repository>/bases`. The
jobs that reach it log in with `RELEASE_REGISTRY_USER` and
`RELEASE_REGISTRY_TOKEN` of the `release/registry/promote` and
`release/registry/signing` environments. The job token is never used for the
registry: every job that runs the project's code holds one.

**What the project's code can reach.** `Base: plan` and `Base: build` run
the planner and the Containerfiles, the project's own code. They hold no
release secret, but code in their container can read the job's own job
token. The bases repository takes the release's registry credential alone,
and what the two jobs hand on is checked again by the jobs that hold it. Do
not add the release project to the publication project's job-token
allowlist.

**What a child builds on.** A child's plan job needs its parent's
`Base: sign and promote`, and its build needs its own `Base: parent`. A child
is therefore planned and built only on a digest that a signer job verified
against the pinned key, after the parent's promotion.

**What the build receives.** `Build: release files` runs `build` with
`FORGE_CI_BASE_IMAGES_DIR` set to a directory of `<flavor>.json`, one per
flavor of the policy, each the promoted `{"ref":...,"input_id":...}` with the
ref pinned by digest. A `dist/base-images.json` from the build is refused:
the base stage is the only source of base images.

**A release that reuses a stage.** Final base tags are immutable. When every final tag of a
stage already exists and verifies against the pinned key and the stage's
declared inputs, `Base: sign and promote` signs nothing and hands the
existing images on. The build and publish jobs of a reused stage still run.
Bases are not rolled back with a failed release.

**A promotion that stopped halfway.** Promotion moves a stage's final tags
one by one. When `Base: sign and promote` fails between two of them, retry
the job: it finds some final tags, signs nothing, and promotes again.
Promotion verifies every candidate's signature and lineage and requires each
existing final tag to be the digest this stage staged before it moves a tag,
so the retry completes exactly what the first attempt signed. A stage whose
existing tags are another digest, or whose candidates carry no signature
that verifies, is refused with nothing moved: its flavors changed apart, so
give a flavor that changes on its own a stage of its own.

**Limits.** A stage builds for `linux/amd64` alone.

### Package publication

The native release publishes npm packages itself. An npm project that lists
`forge-packages` under `publish-to` in the release's `artifacts.yml` adds the
plan-driven job `Publish: npm packages` to the release child. It runs after
`Publish: release`, in `runtime-image-node`, and publishes the packed
tarballs of the signed hand-off, the bytes the release offers as files, to
this project's package registry with `forge-ci publish forge-packages deploy`
under the tag `release-<version>`, which the engine holds to the packed
version. So one release publishes files, a package and images together.

- The `*.tgz` files at the root of `dist/` must be exactly the planned
  projects' packages, one each: every tarball packs the `name` and `version`
  of its project's `package.json` at the release commit, and no other
  `*.tgz` lies there. `forge-ci publish forge-packages npm-tarballs` checks
  it in `Build: release files`, before anything is signed, and in the
  package job again, on the signed hand-off and a fresh checkout of the
  release commit. Keep other archives in a subdirectory or under another
  suffix.
- The credential is `RELEASE_PACKAGE_TOKEN`: create a project deploy token
  with the scopes `read_package_registry` and `write_package_registry`, and
  store it as a masked, protected project variable with the environment
  scope `release/packages`. Do not give the job token package write: the
  jobs that run the project's code hold one too. Keep its self entry
  read-only; see
  [Signing and publication are separate authorities](#signing-and-publication-are-separate-authorities).
- The job runs on the signer's runner (`signer-runner-tags`) and runs no
  consumer command. Every other release job refuses to start when it sees
  `RELEASE_PACKAGE_TOKEN`, and the package job when it sees any other
  release secret. `npm publish` runs with `--ignore-scripts`, and a project
  variable cannot send the package elsewhere or load code into npm.
- A registry takes a package version once. A retry of the job finds a
  package an earlier run published, by the release's tag, and leaves it when
  the registry's digest of that version is the tarball's; it then publishes
  the rest. The same version with other content fails the job.
- Maven and Gradle packages are not published by the release: a release
  whose `artifacts.yml` sends a Maven library or a Gradle project to
  `forge-packages` is refused when it is planned. They, and a project
  without the release policy, use the Catalog components below.

The Catalog publish components are separate from the native release and run
in the consumer's own pipeline. Each expects its input files from an earlier
job through native artifacts.

| Component | Publishes | Needs |
|---|---|---|
| `publish-forge-packages` | Maven (`mvn deploy`) or npm to the project's GitLab Package Registry | `CI_JOB_TOKEN`; JARs under `*/target/` or the packed `*.tgz` |
| `publish-maven-central` | A Maven library through its profile, or exported Gradle JVM publications through the Central API | Signed release request and prepared SHA; publisher secrets including the GPG public key; sources and javadoc JARs |
| `publish-apple-appstore` | An IPA to App Store Connect with `xcrun altool` | A macOS runner with Xcode and `forge-ci`; the IPA under `artifacts/` |

Development containers use the [snapshot component](#snapshots). See
[Publishing](publishing.md) for target setup, which is forge-neutral for
Maven Central and App Store Connect.

## Snapshots

`templates/snapshot-images.yml` is the native snapshot image component. Include
it from a trusted pinned policy, by full commit SHA. Required inputs, with no
default: `branch`, `forge-ci-repository`, `server-url`, `registry`, the
digest-pinned `job-image` and `build-image`, and
`build-runner-tags-linux-amd64` and `build-runner-tags-linux-arm64` (`[]` when
there is no arm64 runner). `image-repository` is needed when the declaration
has a container. The selected
branch is resolved once; the policy pipeline's ref is not the source identity.
`publish-containers` defaults to `true`; explicit `false` opts out.

The component generates a child pipeline with native amd64/arm64 builds,
publication after all builds and exact receipt collection. Set
`build-runner-tags-linux-amd64` and `build-runner-tags-linux-arm64` for the corresponding native
runners; arm64 builds require arm64 tags. `runner-tags` selects control-plane
runners. Buildah is the default; kaniko requires the admitted digest-pinned
`builder-job-image`, also used as `build-image`. Build execution receives no
registry publication credentials. Publishers use `SNAPSHOT_REGISTRY_USER` and
`SNAPSHOT_REGISTRY_PASSWORD`, falling back to GitLab registry credentials.
The collect job retains `container-images.json` as a child-pipeline artifact.

`publish-files: true` adds one job per declared file artifact to the child
pipeline, `Snapshot: files <artifact>`. It builds the artifact in its
toolchain image (`artifact-job-image-*`, else `job-image`) and keeps
`snapshot-files/<artifact>/` as an unsigned job artifact, named
`<artifact>-<snapshot version>`, for 7 days; see
[snapshot files](workflows.md#snapshot-files). The job reads no protected
variable and no registry credential.
`examples/go-cli/gitlab/snapshot-files-policy.yml` is such an include, for the
policy project.

With the default `context-job: 'Snapshot: plan'`, the child pipeline builds
the Go, Cargo, Maven, Gradle JVM and npm artifacts the images need from the
planned commit. They run in the digest-pinned `artifact-job-image-go`,
`-java`, `-node` or `-rust` image when given, else in `job-image`, which must
then carry the toolchains. To build them yourself instead, set `context-job`
to a job of the parent pipeline that needs `Snapshot: plan` and exports
`_snapshot-context/` with `forge-ci` `gather-context`, as
[workflows](workflows.md#snapshot-container-contract-all-three-forges)
describes.

An npm package is laid over the build context, where the release's image
jobs lay `image-context/`. See the
[shared contract](workflows.md#snapshot-container-contract-all-three-forges) for
branch-hashed, final `-snapshot` tags, an always-on scan, independent SBOM policy, unsupported
build secrets, and no `extract.binary`. The snapshot reads the release's
declaration and leaves its signing and provenance out, see
[what a snapshot takes from the declaration](workflows.md#what-a-snapshot-takes-from-the-declaration).
Snapshots do not advance release aliases or promotions.

## Signing

GitLab signs release files and images with the Cosign key
(`env://COSIGN_KEY`), set from `RELEASE_COSIGN_PRIVATE_KEY` and
`RELEASE_COSIGN_PASSPHRASE`, unless `artifacts.yml` asks for GPG for the
files, as below. A method other than `kms` or `gpg`, or a KMS key other than
`env://COSIGN_KEY`, in `artifacts.yml` is not used, and `forge-ci doctor` and
the release plan warn about it.

A declaration that says `sign.method: gpg`, or `sign.files-method: gpg`
beside a Cosign method, signs the checksums and SBOMs with the OpenPGP key:
each gets an armored `.asc` beside it on the release page, and no Cosign
bundle. `Check: signing access` reads the declaration before the release
commit exists and requires `RELEASE_GPG_PRIVATE_KEY`,
`RELEASE_GPG_PASSPHRASE` and `RELEASE_GPG_FINGERPRINT` for it, also with
`sign-distro-packages: false`. `Sign: files and images` signs them with that
key, dated at the release commit, in a subshell whose environment holds the
GPG key, its passphrase and its fingerprint and none of the job's other
signing and publishing secrets or its job token, and refuses a key whose
fingerprint is not `RELEASE_GPG_FINGERPRINT`. The provenance statement, the
evidence and the images are signed with the Cosign key all the same, so the
Cosign variables stay required. Any other method, and a declaration without a
`sign` section, signs the checksums and SBOMs with the Cosign key. With the
release's public OpenPGP key imported, the checksums verify and then the
files they list:

```bash
gpg --verify checksums.txt.asc checksums.txt
sha256sum --check --ignore-missing checksums.txt
```

With `sign.files-method: gpg`, GPG for the files beside Cosign for the
images, the files are signed with GPG as above and the images with the
Cosign key, whatever `sign.method` names
([`sign.files-method`](artifacts-reference.md#signfiles-method)).

Swap matters for OpenPGP keys: the [swap policy](verification.md#swap-policy)
keeps a GPG key off disk. Cosign reads its key in its own process. If you sign
distro packages with `sign-distro-packages`, or the checksums and SBOMs with
GPG, keep swap off on the signer runner, or run its job containers with swap
disabled: with the Docker executor, set `memory` and `memory_swap` to the same
value under `[runners.docker]` in the runner's `config.toml`. The container
limit leaves `gitlab-runner`'s copy of the key swappable, so prefer host swap
off on production signer runners
([residual risk](verification.md#swap-policy)).

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
`Sign: files and images` copies the admitted images into it by digest.

### What is signed, and with what

| Object | Key | Variable |
|---|---|---|
| Release commit and final tag | SSH | `RELEASE_SSH_SIGNING_KEY` |
| Checksums and SBOMs | Cosign key pair, or OpenPGP when `artifacts.yml` says `sign.method: gpg` or `sign.files-method: gpg` | `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE`; with `gpg`, `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE`, `RELEASE_GPG_FINGERPRINT` |
| SLSA provenance, images and their attestations | Cosign key pair | `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE`, checked against `COSIGN_PUBLIC_KEY` and `cosign-public-key-sha256` |
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
value, because a signature is verified the way it was made. The input
decides, not `artifacts.yml`: a declaration that writes `sign.transparency`
is not read, and `forge-ci doctor` and `Release: plan` warn about it.
[The transparency log](forgejo.md#the-transparency-log) explains what each
value gives up.

**Keyless and KMS.** The `release` component does not use them. The CLI
supports keyless Sigstore signing on GitLab, with `https://gitlab.com` or
your `CI_SERVER_URL` as the OIDC issuer; public Fulcio trusts only gitlab.com.
A job of your own can run `forge-ci release sign --method=sigstore` or
`--method=kms`. See
[Signing Methods](verification.md#signing-methods-for-release-artifacts).

## Code scanning (SARIF)

GitLab does not ingest SARIF. The lint components convert their SARIF to the
GitLab SAST schema with `forge-ci security report to-gitlab-sast` and publish
it as `artifacts:reports:sast`, so findings appear on the merge request's
Security tab. Producing the report works on every tier; displaying it needs
GitLab Ultimate. `enable-gitlab-sast: false` keeps the SARIF as a plain
artifact only.

The conversion runs the engine the way `check` does, from the
component's commit, so a component included by version needs the engine of
that version's commit published; see
[Publish the recorded build](#publish-the-recorded-build). A failed
conversion fails the job after the gate has printed its verdict, and the
SARIF is kept. `forge-ci security report upload-sarif` on GitLab prints a
notice and exits 0.

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
   [Development](DEVELOPMENT.md#the-engine-binary) describes.
4. **Publish the binary** for that commit to your project's generic packages;
   see [Publish the recorded build](#publish-the-recorded-build).
5. **Build your signer image** and pin its digest; see
   [Images of the release policy](#images-of-the-release-policy).
6. **Create the Cosign key pair, the release bot's deploy key, and the
   publication project**, and set the [variables](#secrets-and-tokens).
7. **Publish the Catalog components** by pushing a semver tag, if consumers
   use them by version.

`forge-ci-repository` and `consumer-project` are the fork's and the consumer's
paths; nothing else in the components names an owner. The lint images have no
default: each consumer passes its own digest-pinned `nanolinter-image` or
`megalinter-image`.

## A job that found a dependency unavailable

GitLab runs a job once more when the engine ended it with exit code 69: a
download, registry or resolver that did not answer
([what 69 means](providers.md#capability-matrix)). No other failure is
retried, and no job more than once: `retry: {max: 1, exit_codes: 69}`, a
literal of the components, with no input or variable to widen it. It is on
the jobs that only read, build and hand on files: the `check`, lint and
`build-*` components, the release's plan, check, version, build, export,
base plan, parent and build, image plan and build and summary jobs, and every
snapshot job but the publishing one.

Every job that pushes an image, signs, tags, moves a tag, publishes,
deletes, or starts the pipeline of such jobs says `retry: 0`, so a
`default: retry` of the including configuration does not reach it. That
also switches off GitLab's retry of a runner failure. These jobs fail once,
and a person decides whether to run them again: `Prepare: release commit
and tag`, `Base: publish`, `Base: sign and promote`, `Images: publish`,
`Sign: files and images`, `Publish: images`, `Publish: release`,
`Publish: tidy up`, `Publish: npm packages`, the snapshot's publishing job,
the two triggers and the `publish-*` components.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| A check or release job refuses before checkout: mutable reference | The component was included by tag or branch. Include checksum-bound components by full commit SHA. |
| A check job refuses: release secret present | A signing or publication variable reaches the check job. Scope it to its release environment. |
| `is available to the wrong release job` | A release variable is scoped too broadly. Use the scopes in [Secrets and tokens](#secrets-and-tokens). |
| A release job fails in its first lines | The component is not included by full commit SHA, the project is not `consumer-project`, the tag is not protected, or the pipeline is not the policy's child. |
| The engine download fails | The binary for that commit is not published, or the engine project or its packages need credentials. |
| `Check: release request` refuses the request | The tag is not SSH-signed by an allowed signer, or it is not the head of the release branch. |
| A release job fails uploading artifacts | The hand-off is over the instance's artifact limit; see [Artifact size](#artifact-size). |
| An image build step ends with `exec format error` | A `RUN` step for another architecture on an unprivileged runner; emulation on the host does not reach a nested build. Build on a runner of that architecture. |
| A project requested as SHA-256 is SHA-1 | The `support_sha256_repositories` feature flag was not enabled for the account when the project was created. |
| `Base: sign and promote` refuses a stage as half promoted | Some final tags of the stage exist, and they are not what this stage signed. A stage is promoted or reused whole; see [Base images](#base-images). |
| Findings do not appear on the Security tab | Display needs GitLab Ultimate, or the conversion was skipped; the job log says so. |

`forge-ci doctor` in a job prints what the detected forge supports.

Doctor fails when two forge-ci component includes in `.gitlab-ci.yml`
define the same job: GitLab merges jobs of one name without a warning. Give
each include of the same component its own `job-name`.

Outside CI, doctor recognizes an unambiguous GitLab-only layout. For a repository
with multiple forge layouts, select GitLab explicitly with
`forge-ci --provider gitlab doctor`.

## Limits

- **No Google Play or Xcode-build components**, and `publish-forge-packages`
  does not run Gradle: with `project-type: gradle` or `gradle-android` it
  uploads a Maven publication the build has already exported. Container
  images are built and published by `release` and `snapshot-images`.
- **No `:dev`/`:staging` promotion ladder.**
- **No distro package registry.** GitLab has no RPM or Alpine registry, and
  its [Debian registry](https://docs.gitlab.com/user/packages/debian_repository/)
  is an experiment behind a feature flag that is off by default. A
  declaration with `distro-packages` is refused when the release is planned;
  the packages stay release files.
- **Checksum-bound jobs run on linux/amd64 or linux/arm64 only.**
- **No run-artifact API**: files move between jobs through native artifacts.
- **SAST display needs GitLab Ultimate.**
- **The engine project and its packages must be readable anonymously.**
