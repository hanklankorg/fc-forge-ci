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

For local CLI evaluation, use the reviewed
[source checkout](DEVELOPMENT.md#source-checkout). Consumer adoption below also
requires your instance's published components, engine package and job images.
`init` reads its examples and defaults from a release tag on that mirror; a
local build alone does not supply them.

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
| GitLab | 18.7 or later for `check`; `release` uses the same component context. CE/Free is enough: protected-environment approvals and paid pipeline-execution policies are not used. |
| Hosting | The components, and for checksum-bound components the engine project, on the **same instance** as the consumer. |
| Engine project | **Readable without credentials**, and its generic package downloadable anonymously. Downloads send no credential. |
| Runner architecture | **linux/amd64 or linux/arm64** for checksum-bound jobs: the engine is recorded for both, and each job runs the one of its runner's architecture. Catalog runtime images are linux/amd64 and linux/arm64, except Android (amd64 only). |
| Executor | Docker or Kubernetes. The components clear the entrypoint of `job-image`, `build-image`, `nanolinter-image` and `megalinter-image`, so an image that starts a tool of its own still runs the job's shell. |
| Job images | For `check`: Bash, Git, curl, `sha256sum` and the consumer's check tools, pinned by digest. For the lint components' SAST conversion: Bash, Git, curl and `sha256sum` in the lint image; Nanolinter's flavours and MegaLinter have them. For `release`: a `job-image` run as an ordinary user, a `build-image`, with `release-images` a `builder-job-image`, and the organization's `signer-image`; see [Images and the engine binary](#images-and-the-engine-binary). |
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

First get a green merge-request gate; then configure release publishing.
After pushing a request tag, follow its pipeline's prepare, build, sign and
publish jobs and inspect the final release. If it fails, use
[Failed Release Recovery](failed-release-recovery.md) rather than deleting final tags.

Two roles share the work. **The organization, once:** host the components
(steps 1 and 2), build the job, build and signer images, hold the Cosign key
pair and the release bot's deploy key, and own the policy project a release
runs from. **Each consumer, per project:** the merge-request gate in its
`.gitlab-ci.yml`, its `nanolinter.toml`, its project settings and the
protected variables (steps 3 to 6).

Init neither creates nor modifies Nanolinter or MegaLinter configuration files.
These are optional customizations: the engine selected by your lint component
uses its defaults without a config file, with the component's required lint
floor still applying. Keep your existing config or copy an example if you want
custom settings.

Use `forge-ci init <mirror URL> --dry-run` to preview the proposed files,
remaining placeholders, and all existing-file conflicts before generation.
It still fetches release templates into temporary storage, but writes no project
files. Conflicts return nonzero. Repeat without `--dry-run` using the reported
`--ref` and `--example` after reviewing the proposal.

Automatic release selection accepts both Catalog tags such as `0.90.0` and
prefixed tags such as `v0.90.0`, choosing the highest stable version.
Prereleases and build-metadata tags are excluded from automatic selection.
If both spellings exist at the highest version, the prefixed tag wins;
use `--ref 0.90.0` to choose the unprefixed tag explicitly.
Init resolves and reads the exact selected tag name.

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
| | `server-url` | This instance's URL as `CI_SERVER_URL` reads, `https://host[:port]` without a path, for example `https://gitlab.example.com`; required. Each job refuses to start when `CI_SERVER_URL` differs, and derives `CI_API_V4_URL` from it. See [The instance as a literal](#the-instance-as-a-literal). |
| | `job-image` | Digest-pinned image for the check job. |
| | `job-user` | UID or `UID:GID` the job runs as, for example `65532:65532` for the runtime images' `nonroot`. Empty, the default, keeps the image's user. See [The job's user](#the-jobs-user). |
| | `commands` | The consumer's check commands; a failure fails the job. `job-name` (default `check`) and `stage` (default `test`) name the job. |
| | `enforce-lint`, `enforce-vulnerabilities`, `enforce-application-tests`, `enforce-reproducible-builds` | The administrator's [check enforcement](admin-policy.md) for the gates the commands run through `forge-ci`; default `false`. |
| `release` | `forge-ci-repository`, `consumer-project` | The engine project, and the only project allowed to run this policy. |
| | `server-url` | This instance's URL as `CI_SERVER_URL` reads, `https://host[:port]` without a path, for example `https://gitlab.example.com`; required. Each job refuses to start when `CI_SERVER_URL` differs, and derives `CI_API_V4_URL` from it. See [The instance as a literal](#the-instance-as-a-literal). |
| | `registry` | This instance's container registry as `CI_REGISTRY` reads, `host[:port]`, for example `registry.gitlab.example.com`, or empty when the project has none; required. GitLab configures it apart from the web host, so it is stated, not derived. Each job refuses to start when `CI_REGISTRY` differs; the project's image repository is derived from it. |
| | `job-image`, `build-image`, `signer-image` | Digest-pinned images for key-free jobs, the build job, and the signing jobs. |
| | `job-user` | UID or `UID:GID` the `job-image` jobs run as, for example `65532:65532` with a runtime image. Empty, the default, keeps the image's user. |
| | `publication-project`, `publication-image-repository` | The separate project that owns final images, and the exact registry repository in it. |
| | `cosign-public-key-sha256` | SHA-256 of your Cosign public key, written out. |
| | `release-git-url`, `release-git-host-key-fingerprint` | The SSH write URL for the release commit and tag, and its independently supplied host key fingerprint, `SHA256:<base64>` alone. An empty URL is this project on the GitLab web host, port 22. |
| | `request-allowed-signers` | OpenSSH allowed-signers text for the people who may sign a request tag. |
| | `bot-name`, `bot-email` | The release bot identity; no default. |
| | `checks`, `build` | Consumer commands: checks on the request commit, and the build that writes `dist/release-files.json` and `release-notes.md`. |
| | `build-runner-tags` | Runner tags for the build job and, with Buildah, the amd64 image job, for example `[privileged]`. Empty, the default, leaves them untagged. |
| | `release-images`, `artifacts-config`, `release-platforms`, `labels` | `true` builds the `containers:` of `artifacts-config` (default `.forge-ci/artifacts.yml`; without it the root `Containerfile`) in the release's image jobs, narrowed to `release-platforms`, with `labels` beside the standard ones. Default `false`: no image jobs. See [Release images](#release-images). |
| | `image-builder`, `builder-job-image` | The image jobs' builder, `buildah` (default) or `kaniko`, and the digest-pinned job image they run in: the Buildah job image or the kaniko job image. |
| | `build-runner-tags-arm64` | Tags of a native arm64 runner. The arm64 image and export jobs exist only when they are set. |
| | `export-files`, `export` | `true` runs the `export` commands once per selected architecture, natively, in `builder-job-image` with `image-builder`, before the build job, and lays their `dist/` into its own. See [Files exported from a Containerfile](#files-exported-from-a-containerfile). |
| | `checks-runner-tags` | Runner tags for consumer checks after restricted signed-request authorization. Empty, the default, leaves checks untagged. Authorization and signing stay untagged. |
| | `changelog-creator`, `changelog-config`, `commit-body-config` | `git-cliff` (default) or `git-chglog`, and the consumer's configuration files, by default `.forge-ci/gitcliff/keepachangelog.toml` and `.forge-ci/gitcliff/minimal.toml`, copies of this repository's [`templates/gitcliff/`](../templates/gitcliff/) that `forge-ci init` writes. git-cliff renders offline, without remote metadata, and reads stable `vX.Y.Z` tags only, overriding any `tag_pattern` in the configuration. A SHA-256 repository needs `git-chglog`, since git-cliff cannot read one. |
| | `release-policy` | `stable` (default) or `stable-prerelease`, selected in the trusted external policy. GitLab has no native prerelease flag; see [release policy](workflows.md#stable-and-prerelease-policy). |
| | `release-branch` | The branch a release lands on; empty, the default, takes the project's default branch. The request must be its head. |
| | `transparency-log` | `none` (default) or `public`: whether Cosign signatures are also written to the public Sigstore transparency log. See [Signing](#signing). |
| | `sign-distro-packages` | Default `true`: GPG `.sig` sidecars for `.deb`, `.rpm` and `.apk`, which makes the GPG variables required. |
| | `release-asset-retention` | How many of the newest releases keep every file; older ones keep their checksums and signatures, upload and link both deleted. Empty, the default, deletes nothing. See [Old releases' files](#old-releases-files). |
| | `base-images` | Default `false`; see [Content-addressed base images](#content-addressed-base-images). |
| | `enforce-lint`, `enforce-vulnerabilities`, `enforce-application-tests`, `enforce-reproducible-builds` | The administrator's [check enforcement](admin-policy.md) for the checks and build jobs and the image scans; default `false`. |
| `nanolinter` | `forge-ci-repository`, `server-url`, `nanolinter-image`, `required-lints`, `enable-gitlab-sast` | The project serving the component and its engine package (required), the flavour image (required, pinned by digest, as the examples show), the mandated floor (default `secrets,sast`), and the SAST report. `enforce-lint` and `enforce-vulnerabilities`, default `false`, are the administrator's [check enforcement](admin-policy.md). |
| `megalinter` | `forge-ci-repository`, `server-url`, `megalinter-image`, `enable-gitlab-sast` | Required digest-pinned image; the rest as for `nanolinter`. |
| `build-*` | `working-directory`, `version`, `skip-tests`, `enable-build-sbom`, `runtime-image` | Common to every build component; `runtime-image` is required and pinned by `@sha256:`; `build-go` and `build-cargo` add `platforms` and `artifact-name`, `build-go` also `main-package` (relative to `working-directory`), `build-maven` `build-type` and `profile`, `build-gradle-android` the module, flavour and signing inputs. `enforce-application-tests`, default `false`, is the administrator's [check enforcement](admin-policy.md); the job's `runtime-image` must carry an engine from this release or later. |
| `publish-*` | `project-type`, `working-directory`, `runtime-image` | `runtime-image` as for `build-*`. `publish-maven-central` adds `profile` and `settings-path`; `publish-apple-appstore` takes none of these, only `platform`, `skip-validation`, `runner-tags` and `macos-image`. |

The standalone `publish-maven-central` component also accepts `release-policy`.
It binds the selected input before authorization; a project environment variable
cannot widen the default stable-only policy. Snapshot publishing is independent.

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

Set the project's **Minimum role to use pipeline variables** to **No one
allowed** (Settings > CI/CD > Variables, or the project API's
`ci_pipeline_variables_minimum_override_role: no_one_allowed`). A pipeline
variable, set when a pipeline is run by hand, triggered or created through the
API, outranks the job's own variables and GitLab's predefined ones, and so do
project and group variables. Whoever may set one could point `CI_SERVER_URL`,
`CI_API_V4_URL` or `CI_REGISTRY` at another host, where the release jobs would
send `RELEASE_TOKEN` and `RELEASE_REGISTRY_TOKEN`. The jobs no longer read
those variables for a token's destination (see
[The instance as a literal](#the-instance-as-a-literal)), so the setting is
defence in depth: it keeps every other predefined variable the jobs read as
GitLab set it.

### The instance as a literal

Every policy component (`check`, `nanolinter`, `megalinter`, `release` and
`snapshot-images`) takes the instance's URL as `server-url`, and `release`
and `snapshot-images` take its container registry as `registry`, from the
administrator's include. Each job's start assigns them read only and refuses
to run when `CI_SERVER_URL`, or `CI_REGISTRY`, is not the literal, before it
fetches anything or reads a token; it then sets `CI_API_V4_URL` to
`<server-url>/api/v4` and, for `release`, `CI_REGISTRY_IMAGE` to
`<registry>/<consumer-project>` in lower case, as GitLab derives them. A
variable that points the jobs elsewhere stops them instead of receiving the
job token or a release token. The URL is an origin, `https://host[:port]`: an
instance served under a relative URL path is not supported.

This binds the destination, not the runner's network trust. Token-bearing jobs
still use `CI_SERVER_TLS_CA_FILE` for instances with a private CA. GitLab can
override that predefined variable with a project, group or pipeline variable,
so the administrator must control it as well as `GIT_SSL_CAINFO`,
`CURL_CA_BUNDLE`, `SSL_CERT_FILE`, `SSL_CERT_DIR` and proxy settings. An
attacker-controlled CA combined with a redirected route can impersonate even
the literal server name. The components do not pin a CA digest or establish
the provenance of these environment values. Keep CA/proxy configuration in
trusted runner/admin configuration and prevent lower-trust overrides; setting
`server-url` alone does not enforce that boundary.

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

The build and publish components require `runtime-image`: a component cannot
tell which registry serves the images, so it names none. Pin it by digest to
the runtime images the GitHub workflows use, or, for an installation that must
not pull from `ghcr.io`, to your mirror of them. `forge-ci plan
gitlab-build-pipeline` takes the same images as `--runtime-image-java`,
`-node`, `-go`, `-rust` and `-android`, one per toolchain family the plan
builds. [Runtime Images](runtime-images.md) has the roster and pinning
conventions.

### The job's user

The runtime images run as root and carry the ordinary user `nonroot`,
65532:65532, whose caches live in its home. A check that needs ordinary-user
filesystem semantics runs a runtime image with `job-user: "65532:65532"`.
Give the group too: on Kubernetes a UID alone can run the job with GID 0. The
component sets both `image:docker:user` and `image:kubernetes:user`, since
the Kubernetes executor reads only the latter, and names a job variable there
rather than the input: GitLab refuses an empty image user, and the runner
reads an empty value after expansion as the image's own user on either
executor. The Kubernetes executor admits any UID but 0 unless its
`allowed_users` narrows that; the Docker executor admits any user unless the
runner configures `user` or `allowed_users`, which must then list it. Runner
settings that fix `runAsUser` take precedence. The Kubernetes executor sets
`umask 0000` while it fetches into the build volume, so the workspace is
writable for the job's UID; with `FF_DISABLE_UMASK_FOR_KUBERNETES_EXECUTOR` it
changes the files' owner to that UID instead. The image user needs GitLab
18.0 and GitLab Runner 17.11 or later on Kubernetes.

A project, group or instance variable outranks the job's own, so the job
does not trust the variable alone. At its start it refuses an `RC_JOB_USER`
that is not `job-user` as written, then a process whose UID, or `UID:GID`
when the input gives both, differs from it, before any checkout. Either way
it prints one line, `job user: uid=65532 gid=65532`, the user it runs as;
the job's log shows which user each executor gave it. In `release` only
the `job-image` jobs take `job-user`: the build, image build and signer jobs
hold the same variable to empty and run as their images' own users. Which
of these a job is follows from its role, a literal of the template, never
from its name, which a project variable can set too.
The images need `id`, as coreutils and BusyBox provide.

### Images of the release policy

The policy selects separate digest-pinned `job-image`, `build-image` and
`signer-image` inputs. Run checks and rendering as an ordinary user;
permission tests must run with ordinary-user filesystem semantics. A runtime
image with `job-user: "65532:65532"` does that, as [The job's user](#the-jobs-user)
describes; the jobs in the other images keep those images' users. Only the
build job selects `build-image`, the consumer's build tools. Pass the same
image for both inputs when the build needs no separate environment, and the
Buildah job image when the build runs `forge-ci container build` and nothing
else.

With `release-images`, the image build jobs run in `builder-job-image`, as
root, whichever the builder. For Buildah, the default, it is the image of
[containers/buildah-job/Containerfile](../containers/buildah-job/Containerfile),
built and pinned by digest like the signer image; it carries Buildah and its
helpers, and the job installs nothing. Buildah needs user namespaces. Where
only some runners allow them, tag those runners and name the tags in
`build-runner-tags`: the build job and the amd64 image job move there, and the
signing jobs stay on the untagged runners.

Where no runner allows them, set `image-builder: kaniko` and
`builder-job-image` to the kaniko job image of
[containers/kaniko-job/Containerfile](../containers/kaniko-job/Containerfile),
pinned by digest. The image build jobs then run in it, as root, on untagged
runners. Before a Containerfile is
built, the job admits the image as the snapshot builds do: its title must be
`forge-ci kaniko job image` and its `org.opencontainers.image.source` label
the engine project, `${CI_SERVER_URL}/<forge-ci-repository>`, read with the
job token. kaniko replaces the job's root filesystem with the image it
builds and deletes it after each build, so the job keeps its work, the engine
and the checkout in the workspace, and its temporary files in the job image's
`/kaniko/tmp`. See
[Building on the pool with kaniko](runners.md#building-on-the-pool-with-kaniko)
for what kaniko does not do.

Integration checks that execute Buildah need user namespaces too. Set
`checks-runner-tags` in the trusted release policy for those checks. The
restricted `Check: release request` must succeed before the selected runner
starts `Check: consumer checks`; that job freshly checks out the immutable
request commit and repeats signature and branch-head validation before running
`checks`. Both jobs refuse release credentials. Changelog and version
preparation wait for successful consumer checks. This selector does not route
merge-request checks or change the ordinary runner default.
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

The delivery binary has one build definition and one checksum per
architecture, shared with Forgejo. The established paths remain
`scripts/ci/engine-binary.sh` and
`.engine/bin/forge-ci-linux-<amd64|arm64>.sha256`; do not maintain a second
GitLab build or checksum copy. The script and directory are named for the engine,
shared by all delivery layers; its `build` and `refresh` know no forge, and `publish`
uploads to GitLab when `FORGE_CI_PACKAGE_PROVIDER=gitlab`, as shown below.

Checksum-bound check/release jobs use the **Linux** delivery binary of the
runner's architecture, amd64 or arm64, and refuse any other runner. Select a
compatible runner and job/signer image. There is no macOS engine; other components' runtime-image
platforms are described in the [runtime image roster](runtime-images.md#image-roster).

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

The engine repository must be on `CI_SERVER_URL`. The package must permit
anonymous downloads. Downloads and publication read-back send no credential;
the recorded SHA-256 is verified before the binary can run.

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

Replace both pins with reviewed immutable values. `forge-ci-repository` is the
project serving the component, including subgroups. The image must contain
Bash, Git, curl, sha256sum and the consumer's check tools. Optional input
`job-name` defaults to `check`; `stage` defaults to `test`; `job-user`, empty
by default, runs the job as another user. Include the component once per
toolchain to check a monorepo's parts in their own images, and give each
include its own `job-name`: GitLab merges two jobs of one name into one, so
two includes left at `check` run only one of the checks, silently.

The job starts with an empty workspace and no cache or artifact dependencies.
It uses the runner's custom CA when provided, keeps the helpers outside the
consumer checkout, and checksums the engine. It then writes a `forge-ci`
that runs that engine with the component's four
[enforcement inputs](admin-policy.md) as flags, puts it first on PATH and
names it in `FORGE_CI_BIN`, and prints the effective policy with
`forge-ci policy show` before the commands run; the release's checks and
build jobs do the same. A command that gives a setting again is refused. Code
the commands run can still reach the bare binary; the settings are the
administrator's only where the include is, see
[Who sets them](admin-policy.md#gitlab). `CI_OUTPUT` points to a temporary dotenv file for engine
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
`forge-ci platform checkout` also derives the format from the native pipeline
SHA when its requested repository and server both match the current project,
including snapshot checkouts of another branch. For another private project,
provide `--object-format sha1|sha256` or a credential that can read its metadata.
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
`dist/release-files.json` and `release-notes.md`, and, for images, leave
their build context in `image-context/`; the image ledger is the
[release image jobs'](#release-images). The flow checks that the hand-off is
unsigned.

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
      server-url: https://gitlab.example.com
      registry: registry.gitlab.example.com
      consumer-project: team/app
      job-image: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:PIN_DIGEST
      job-user: "65532:65532"
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
   without `build-runner-tags-arm64` (narrow `release-platforms` or name the
   runner), an empty `builder-job-image`, whichever the builder, or a
   `publication-project` that is not a separate project. An empty
   `builder-job-image` GitLab already refuses as it creates the pipeline,
   since the image jobs then name no image.
2. `Images: build amd64` and, with `build-runner-tags-arm64`, `Images: build
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
   staging images into the publication project as before.

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
`release-images`. Their SBOMs are generated when they are signed. Build secrets, binary extraction
and base images are not carried out by the image jobs; a binary extraction
is the export jobs' work.

#### Files exported from a Containerfile

A release file the Containerfile builds, such as the binary of a
`FROM scratch AS export-binary` stage, is exported by its own jobs, so that
it is built with the selected image builder on a runner of its own
architecture, never emulated. With `export-files: true`:

1. `Check: release request` refuses an export the jobs would not carry out:
   an empty `builder-job-image`, or a `release-platforms` that names
   linux/arm64 without `build-runner-tags-arm64`. Export commands without
   `export-files` would have no effect and are refused too.
2. `Build: export amd64` and, with `build-runner-tags-arm64`, `Build: export
   arm64` run the `export` commands on the release commit in
   `builder-job-image` with `image-builder`, as the image jobs do, for each
   architecture `release-platforms` selects; empty selects linux/amd64, the
   platform a container declares when it names none. A container that
   declares linux/arm64 then needs `release-platforms` named, or the request
   check refuses, so the images and the exports build the same
   architectures; `build-runner-tags-arm64` serves arm64 and never selects
   it. `EXPORT_PLATFORM` and `EXPORT_ARCH` name the
   job's architecture, and the job holds no credential of its own once the
   builder is admitted. The commands write `dist/`, regular files and
   directories only.
3. `Build: release files` lays every export's `dist/` into its own before
   the `build` commands run, refusing a path two of them hold, so the build
   declares the files with the rest.

The shell adapters pass literal policy inputs to `plan release-exports` for
admission and architecture selection. Invalid or duplicated platforms fail rather
than silently skipping an export. `release gather-exports` validates all local
producer trees and top-level collisions before copying, including hidden entries;
it rejects links/special files and never overwrites an existing top-level path.
This is a pre-manifest merge, not a signer handoff: the producer still declares
the final file manifest and the existing digest/signing boundary remains intact.

Release jobs reuse `container setup-image-builder --require-job-image` with
package installation disabled. The adapter owns only the job-token login against
the policy's registry, stdin secret transfer and isolated auth-file cleanup.

Release publication passes each native artifact root as `--layout-source
amd64=PATH` or `arm64=PATH`. The image owner admits the complete planned set and
each root's architecture before scanning or publishing; it reads those layouts
directly instead of trusting a shell merge. The existing `--layouts-dir` mode
remains available for callers that already have one combined artifact root.

`release validate-dist --digest-file RECORD --stage-dir dist --manifest-root .`
copies handoff files into private staging, hashes those copied bytes with the
existing handoff digest, and installs them only after verification. It refuses
an existing destination and cleans failed staging. Native GitLab handoff readers
use this mode rather than validating one tree and then separately copying it.
Base promotion accepts `--ledger` directly; ledger admission and the projection
to candidate/final metadata happen in Go before registry login.

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

The merge-request gate is the policy's, not the consumer's. A consumer file
that includes `nanolinter` or `check` itself can pass `enforce-lint: false`
and gets a green job, so those jobs are never the required gate. The policy's
own YAML includes the gate components, with the `enforce-*` inputs written
out, and takes from the consumer only the commands, as a script in its
repository.

Prefer a pipeline execution policy in inject mode (`inject_policy`) where the
tier allows it: its jobs run beside the project's own pipeline, apart from
its configuration, and a project maintainer cannot remove the policy. The
project's CI configuration path naming the policy file is weaker: GitLab then
runs only the policy file, merge requests included, but a project maintainer
can change the path, and every file the policy includes is merged into one
configuration. With that setting, the policy includes the consumer's file for
merge-request pipelines only, read at the merge request's commit, **before**
the gate components, and nothing of the consumer's for release tags:

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
  - component: gitlab.example.com/team/ci/forge-ci/release@PIN_FORGE_CI_SHA
    inputs:
      # as above
```

The include rules only decide when a file is read. The policy declares the
pipelines it runs in with its own `workflow: rules`, as the consumer's gate
file did before, admitting merge-request pipelines beside its release
pipelines. A consumer file merged before them can still add keys the gates do
not state, such as `rules`, `needs` or `variables`: keep the consumer file to
jobs of its own, with names of its own, and require the gate jobs by name.

Release-request tag pipelines stay policy-only. The live consumer-release trial checks both sides when the
consumer has `.gitlab/merge-request.yml`: it adds this include to the policy,
releases, then opens a merge request on the released main and requires the
gate's pipeline to pass. That trial still includes the gates from the
consumer's file, the shape this section replaces; it proves the pipeline, not
who set the settings.

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
| `Check: release request` | job | On the restricted runner, validates the signed request and that it is the head of the release branch; executes no consumer commands. |
| `Check: consumer checks` | job | After authorization, freshly checks out and revalidates the immutable request, then runs `checks` on `checks-runner-tags` (untagged by default). |
| `Prepare: changelog` | job | Renders the changelog and commit message. Consumer configuration runs only here and in `Prepare: version`. |
| `Prepare: version` | job | With `version-bump`, sets the release version in the project files and hands over the patch; holds no credential. |
| `Prepare: release commit and tag` | job | Verifies the version patch, commits it with the changelog and pushes the signed release commit and final tag with `RELEASE_SSH_SIGNING_KEY`. |
| `Build: release files` | build | Checks out the final tag and runs `build`; hands `dist/` on with its digest. |
| `Sign: files and images` | signer | Imports images into the publication project, signs files, packages, provenance and images. |
| `Publish: images` | signer | Moves final image tags, with a journal. |
| `Publish: release` | signer | Creates the GitLab release with notes and assets. |
| `Publish: tidy up` | signer | Removes candidate tags. |
| `Summary: release` | job | After a successful release, prints the release overview as a "Summary" section of its log; holds no release secret. |

Which job may hold which release secret is written in roles, in
`.gitlab/bin/release.sh` (`rc_release_context`). Each job's `before_script`
opens with its role, `readonly rc_job_role=…`, a literal of the template; the
role also decides the job's user, home and work directory. Nothing the policy
relies on reads `CI_JOB_NAME` or another variable, since a project, group or
instance variable outranks every other, the predefined ones included.

**The policy's values are literals of the job's script.** A project
maintainer can set project variables, and a project, group or instance
variable reaches every job and outranks the job's own `variables:`. So no
value the policy relies on is a job variable. Each job's start assigns the
policy's inputs as read-only shell variables from the inputs as written, the
same way it takes its role, and `.gitlab/bin/release.sh` reads only those:
the engine project and its fetch URL, `consumer-project`, the publication
project and image repository, the Cosign key digest, the release Git URL and
host key, the request's allowed signers, the bot identity, the changelog
settings, `sign-distro-packages`, `base-images`, `release-branch`,
`version-bump`, `release-asset-retention`, `transparency-log`, which the
engine reads as `FORGE_CI_COSIGN_TRANSPARENCY` and the start exports, and the
four [check enforcement](admin-policy.md) settings, which reach the engine as
flags. A
variable of the same name is replaced. The image builder and its job image
reach the image jobs as script arguments. Three runner settings must stay
job variables, because the runner reads them before the job's shell:
`RC_JOB_USER`, `GIT_STRATEGY` and `GIT_SUBMODULE_STRATEGY`. The start refuses
any of them that differs from its literal. Since the inputs are quoted in
the script, none of the free-text inputs may contain a single quote, and
their regexes refuse one before the pipeline starts.

The snapshot component works the same way: the engine project and commit,
the image builder and its job image, the branch, the image repository, the
job images, the context job, the runner tags and the enforcement settings
are literals of the job's script. The generated child jobs get their image, architecture and context
job as script arguments. The check component's engine project and enforcement settings and the
lint components' `required-lints`, `enable-gitlab-sast` and enforcement
settings are literals too. The
lint components' `forge-ci-repository` stays a variable so that it can name
`$CI_PROJECT_PATH`: what runs is bound by the component's commit and the
engine's recorded checksum, not by the project path.

Only GitLab knows some facts, and a variable can still shadow them:
`CI_PROJECT_PATH`, `CI_COMMIT_REF_PROTECTED`, `CI_PIPELINE_SOURCE`,
`CI_COMMIT_TAG`, `CI_COMMIT_SHA`, `CI_SERVER_URL` and the registry
coordinates. Where a literal exists, the jobs compare the fact with it
(`consumer-project`). The engine and the consumer checkout are bound by
commit, and the request by its signature and the literal allowed signers.
The jobs still send their tokens to the GitLab API and registry those
variables name. So the rest of the protection comes from GitLab: protect
the release-request tags and the release branch, scope each
secret to its environment as [Secrets and tokens](#secrets-and-tokens)
lists, and keep the secrets out of reach of the people who maintain the
consumer project. A secret a project maintainer can read or replace is
not protected by any template. Runner-level variables such as
`CI_DEBUG_TRACE` take effect before any script runs, so restrict them at
the instance or group.

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
`after_script`, which names that directory as a literal, makes only its
outgoing regular files and directories readable
by the artifact helper, including a failed promotion's journal. It follows no
symlinks and leaves private workspaces outside that operation. Dist handoffs
use content digests rooted at `.` so different runner paths remain compatible.

The component owns input defaults and forwards explicit values to the child.
Its default renderer is `git-cliff`, and its default configuration paths match
the Forgejo layer: `.forge-ci/gitcliff/keepachangelog.toml` for `CHANGELOG.md`
and `.forge-ci/gitcliff/minimal.toml` for the release commit body. `forge-ci
init` writes them from this repository's
[`templates/gitcliff/`](../templates/gitcliff/); by hand, copy the two files
there to start. Set `changelog-config` and `commit-body-config` to the consumer's
actual paths when using another layout or `git-chglog`; the renderer CLI
requires filenames, and refuses one that does not exist. A SHA-256 repository
needs `git-chglog`: git-cliff cannot read one, and the render refuses it.

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
| `publish-maven-central` | A Maven library through its profile, or exported Gradle JVM publications through the Central API | Signed release request and prepared SHA; publisher secrets including the GPG public key; sources and javadoc JARs |
| `publish-apple-appstore` | An IPA to App Store Connect with `xcrun altool` | A macOS runner with Xcode and `forge-ci`; the IPA under `artifacts/` |

Google Play and standalone release-container publishing have no GitLab component.
Development containers use the [snapshot component](#snapshots). See
[Publishing](publishing.md) for target setup, which is forge-neutral for
Maven Central and App Store Connect.

## Snapshots

`templates/snapshot-images.yml` is the native snapshot image component. Include
it from a trusted pinned policy with explicit `branch`, `image-repository`,
`forge-ci-repository`, digest-pinned `job-image` and `build-image`. The selected
branch is resolved once; the policy pipeline's ref is not the source identity.
`publish-containers` defaults to `true`; explicit `false` opts out.

The component generates a child pipeline with native amd64/arm64 builds,
publication after all builds and exact receipt collection. Set
`build-runner-tags` and `build-runner-tags-arm64` for the corresponding native
runners; arm64 builds require arm64 tags. `runner-tags` selects control-plane
runners. Buildah is the default; kaniko requires the admitted digest-pinned
`builder-job-image`, also used as `build-image`. Build execution receives no
registry publication credentials. Publishers use `SNAPSHOT_REGISTRY_USER` and
`SNAPSHOT_REGISTRY_PASSWORD`, falling back to GitLab registry credentials.
The collect job retains `container-images.json` as a child-pipeline artifact.

`plan gitlab-snapshot-pipeline` renders that graph in Go from the frozen snapshot
plan and explicit policy coordinates. It preserves typed enforcement inputs,
native runner routing, parent-pipeline context binding and the whole-build
publication barrier; the shell adapter only forwards inputs and output paths.

The default `context-job: 'Snapshot: plan'` enables automatic required
artifact-first producers in the child pipeline. They invoke the existing Go,
Cargo, Maven, Gradle JVM and npm builders using the frozen plan's `artifacts[]`,
source SHA and `source_date_epoch`, then gather and restore the bound inputs.
Optional `artifact-job-image-go`, `artifact-job-image-java`,
`artifact-job-image-node` and `artifact-job-image-rust` inputs must be
digest-pinned when provided; otherwise producers use the trusted `job-image`,
which must supply their toolchains.

To override automatic production, set `context-job` to a parent producer that
needs `Snapshot: plan` and builds from its frozen SHA. Use `gather-context` to
export `_snapshot-context/` containing `source-sha`, `snapshot-plan.json`,
`manifest.sha256` and `files/`, including `files/snapshot-dependencies.json` and
planned artifact destinations. The execute job waits for that producer before
creating the child pipeline; build jobs fetch the bundle with `needs:pipeline:job`.
This is an optional advanced prepared-context override. There is no package
snapshot orchestrator.

The shared Go `restore-context` command verifies the source SHA, exact plan,
manifest, dependency names, platforms, file sizes, digests and canonical string
modes `0644`/`0755`, then restores only confined planned inputs. Shell adapters
provide native transport, not context-validation business rules. Do not recreate
the producer inventory after transport. Npm inputs require one valid packaged
tarball at `<context>/.forge-ci/snapshot-inputs/<name>`. See the
[shared contract](workflows.md#snapshot-container-contract-all-three-forges) for
branch-hashed, final `-snapshot` tags, an always-on scan, independent SBOM policy and unsupported
build secrets, extraction and explicit snapshot OCI signing/provenance requests.
Snapshots do not advance release aliases or promotions.

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
   [Development](DEVELOPMENT.md#the-engine-binary) describes.
4. **Publish the binary** for that commit to your project's generic packages;
   see [Publish the recorded build](#publish-the-recorded-build).
5. **Build your signer image** and pin its digest; see
   [Images of the release policy](#images-of-the-release-policy).
6. **Create the Cosign key pair, the release bot's deploy key, and the
   publication project**, and set the [variables](#secrets-and-tokens).
7. **Publish the Catalog components** by pushing a semver tag, if consumers
   use them by version.

What does not follow the fork, and must be overridden:

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

For `sign.method: sigstore`, doctor checks `.gitlab-ci.yml` for a local job
declaring `id_tokens.SIGSTORE_ID_TOKEN` with a non-empty audience (a string or
list of strings). This declares a token GitLab creates at job runtime; never
put the token's actual value in a template or committed configuration.
Use the audience required by your signing service, commonly `sigstore`.

A separate `spec: inputs` header followed by `---` is supported: doctor
inspects the jobs in the following document. Input expressions (`$[[ ... ]]`)
in that pipeline produce a warning because doctor does not evaluate them.

The check reports a missing declaration as a failure for a directly readable
local pipeline. Includes, defaults, inheritance, child pipelines, custom token
variable names, or an unavailable default configuration file produce a warning
to inspect the effective signing job in GitLab instead. Doctor does not fetch
includes, evaluate the merged pipeline, inspect token values, or verify job
execution or audience suitability. Malformed local YAML, a header without its
pipeline, or unexpected extra documents are failures.

Doctor also fails when two forge-ci component includes in `.gitlab-ci.yml`
define the same job: GitLab merges jobs of one name without a warning, so the
second include rewrites the first. A component's job is its `job-name` input,
or the component's name by default; give each include of the same component
its own `job-name`. A `job-name` given as an expression is not checked.

Outside CI, doctor recognizes an unambiguous GitLab-only layout. For a repository
with multiple forge layouts, select GitLab explicitly with
`forge-ci --provider gitlab doctor`.

## Limits

- **No package snapshot orchestrator.** Snapshot image artifact-first producers
  run automatically for supported Go, Cargo, Maven, Gradle JVM and npm inputs.
- **No Google Play, container-publishing or Xcode-build components**; Gradle
  publishing is not wired in `publish-forge-packages`.
- **No `:dev`/`:staging` promotion ladder.**
- **Checksum-bound jobs run on linux/amd64 or linux/arm64 only.**
- **No run-artifact API**: files move between jobs through native artifacts.
- **SAST display needs GitLab Ultimate.**
- **The engine project and its packages must be readable anonymously.**
- **The native release is under acceptance**; validate it on your instance as
  in [GitLab live acceptance](testing.md#gitlab-live-acceptance).
