# Runners and runner lending

Which runner a job needs, how a workflow names it, and how a job borrows a
machine when the pool it runs on cannot build container images.

## What a job needs

| Job kind | Needs | Examples |
|---|---|---|
| Plain | Linux, Bash, Git, curl, `sha256sum`; Node for a job that uses a JavaScript action such as `setup-toolchain`'s cache | lint, tests, release preparation, the engine binary build, SBOM composition, index assembly, release publication |
| Signing | the signer image as the job container, on a runner that gives each job a fresh container | the four signer workflows |
| Container build | native architecture, and either **user namespaces** for Buildah and Podman or, on a pool without them, the kaniko job image | jobs using `setup-buildah`, `setup-image-builder`, `release-images.yml` or the `forge-ci container build-push-oci-image` command; smoke tests with `podman run`, which need Buildah's side |

Rootless Buildah and Podman run as an ordinary user by creating a user
namespace. A runner pool whose job containers share the host's kernel between
tenants denies that (`unshare(CLONE_NEWUSER): Operation not permitted`),
because user namespaces expose the kernel's root attack surface to every
tenant. No storage driver or flag changes this; `setup-buildah` names the
runner as the cause when it sees it. Hosted Forgejo pools such as Codefloe
deny it; GitHub-hosted VMs, gitlab.com's per-job VMs and a machine of your own
with rootless Podman permit it.

`setup-buildah` provisions missing `buildah`, `fuse-overlayfs`, `netavark` and
`aardvark-dns` packages when package installation is enabled. The networking
helpers are required independently of the storage driver; switching to `vfs`
does not replace them. Helpers under `/usr/lib/podman` or `/usr/libexec/podman`
are recognized outside `PATH`. With `--install-packages=false`, provision these
runtime packages in the job image.

## Runner roles across the forges

Every forge layer chooses runners for the same roles. Each maps a role to its
native field: `tags:` on GitLab, an array input interpolated whole; `runs-on`
on Forgejo and GitHub, a JSON array of labels passed as text and decoded with
`fromJSON`. A cell without an input says what decides instead.

| Role | GitLab | Forgejo | GitHub |
|---|---|---|---|
| ordinary | `runner-tags` of `check`, `nanolinter`, `megalinter`, `build-*`, `publish-*`, `release` and `snapshot-images`; default `[]`, untagged | `runs-on` of every reusable workflow; required, no default; callers pass `vars.CI_RUNNER_LABEL` | `runs-on` of ordinary jobs; default `["ubuntu-26.04"]` |
| ordinary-arm64 | none: no component has an ordinary job bound to arm64 | `runs-on-linux-arm64` of `runtime-images.yml`, for the native arm64 smoke test of the image it built; required when arm64 is planned | none: the smoke test runs on the arm64 build runner |
| image-build | `build-runner-tags-linux-amd64` of `release` and `snapshot-images`, which the release's consumer checks take too; required, never empty | `build-runs-on-linux-amd64` of `release-images.yml`, `base-images.yml`, `snapshot-images.yml` and `runtime-images.yml`; required when amd64 is planned; callers pass `CI_RUNNER_LABEL_CONTAINER` or the pool | `build-runs-on-linux-amd64`, default `["ubuntu-26.04"]` |
| image-build-arm64 | `build-runner-tags-linux-arm64` of `release` and `snapshot-images`; required when arm64 is planned, never `[]` then | `build-runs-on-linux-arm64` of `release-images.yml`, `base-images.yml`, `snapshot-images.yml` and `runtime-images.yml`; required when arm64 is planned | `build-runs-on-linux-arm64`, default `["ubuntu-26.04-arm"]` |
| signer | `signer-runner-tags` of `release`; default `[]`; a literal of the separately pinned policy project | `signer-runs-on` of `sign-and-publish-release.yml`, `sign-container-images.yml` and `runtime-images.yml`; required, set by the caller workflow | none: the hosted `ubuntu-26.04` literal of the layer, which no caller input or variable changes; `slsa-attestor.yml` is the exception |
| macos | `runner-tags-darwin-arm64` of `publish-apple-appstore`; default `[saas-macos-medium-m1]` | none: the Forgejo layer has no Apple workflows | `runs-on-darwin-arm64` for unsigned Xcode and Swift lint, default `["macos-26"]`; signed Xcode and App Store publication stay on literal `macos-26` |

Each job takes its own role's input, and none falls back to another's. On
GitLab the kaniko image jobs take the build inputs as Buildah's do, and the
release's consumer checks, family builds and release files, which may run
Buildah, take `build-runner-tags-linux-amd64`; generated version jobs keep
the ordinary `runner-tags`. How the inputs are named is under
[How a workflow names its runners](#how-a-workflow-names-its-runners).

Where the names differ, each has a reason:

- GitLab names each role's input, `build-runner-tags-linux-amd64` beside `runner-tags`,
  because one component holds jobs of several roles. A Forgejo reusable
  workflow declares the inputs of the roles its jobs have, `runs-on` always,
  and the caller picks the pool or the container runners for each.
- GitLab takes arrays and Forgejo and GitHub JSON-array strings, because a
  GitLab input can be an array and a `workflow_call` input cannot.
- GitLab says tags and Forgejo labels: each forge's own word for the field.
- `runs-on-linux-arm64` exists on Forgejo's `runtime-images.yml` alone,
  because only that workflow runs the image it built as a job container, on
  the image's own architecture.

Who chooses the signer runner differs by forge. GitLab runs the signer jobs
from a component the policy project includes, so their tags are that
project's literals; a tag written as a `$VARIABLE` would let a project
variable choose the runner that sees the credentials, and the components take
tags only from inputs. GitHub fixes the hosted runner in the layer. Forgejo
has no policy project: the caller's release workflow passes
`signer-runs-on`, which only the job that reads the signing key runs on. The
examples pass the repository variable `CI_RUNNER_LABEL` there, so whoever may
set repository variables chooses that runner; an installation with a runner
kept for signing writes its labels in the workflow instead, where a change is
a reviewed commit.

GitHub threads its role-specific inputs through the orchestrators. Its
signing, trust-root and publication jobs retain hosted literals; see
[GitHub runners](#github-runners).

## How a workflow names its runners

Every runner input is named `[role-]<keyword>[-<os>-<arch>]`:

- the role: none for ordinary jobs, `build-` for privileged image builds,
  `signer-` for the jobs that hold the signing credentials;
- the keyword: `runner-tags` on GitLab, `runs-on` on Forgejo and GitHub;
- the platform, only on an input whose jobs run on that one platform: its
  `RELEASE_PLATFORMS` name with `/` turned into `-`, such as `linux-arm64` or
  `darwin-arm64`.

So `build-runner-tags-linux-arm64` chooses GitLab's arm64 image build runner
and `runs-on-darwin-arm64` the GitHub macOS runner. GitHub has no `signer-`
input: its signer jobs run on a hosted literal, with one exception named
under [GitHub runners](#github-runners).

On Forgejo, runner selection is a repository or organization variable that
the caller's workflow maps to the inputs. Every value is a JSON array of
labels that must all match one runner; see [Variables](forgejo.md#variables).

| Variable | Selects |
|---|---|
| `CI_RUNNER_LABEL`, `CI_RUNNER_LABEL_ARM64` | the pool, per architecture: every job, image builds included |
| `CI_RUNNER_LABEL_CONTAINER`, `CI_RUNNER_LABEL_CONTAINER_ARM64` | optional overrides: where image builds run instead of the pool |

The layer's workflows take one input per role and platform and never one in
place of another: an arm64 build runs on `build-runs-on-linux-arm64` or the
plan is refused. The caller's workflow maps the variables to the inputs, and
there the container-or-pool dimension does fall back, to the pool:
`build-runs-on-linux-amd64: ${{ vars.CI_RUNNER_LABEL_CONTAINER || vars.CI_RUNNER_LABEL }}`.
That fallback is never silent: on a pool that denies user namespaces the build
fails at `setup-buildah` or `setup-image-builder` with the hint naming the
override; where the pool permits them, or with kaniko, the pool is simply
right.

Where image builds run is the installation's choice:

| Installation | `CI_RUNNER_LABEL_CONTAINER*` | `CI_RUNNER_LENDER` |
|---|---|---|
| the pool permits user namespaces | unset | unset |
| the pool denies them; build with kaniko | unset | unset |
| your own runners with rootless Podman | their labels | unset |
| a hosted pool that denies them; borrow a runner | the labels the lender registers, e.g. `["gh-vm","arch=amd64"]` | `github` |

## A planned platform needs its runner

The planning commands take each runner input of the calling layer with
`--runner-input name=value`, as the layer passed it after its own defaults:
`forge-ci plan release`, `plan snapshot-release`,
`container release-images plan`, `container snapshot-images plan`,
`container platform-plan` and the base stage plans
(`container base-images root-base plan` and `child-base plan`). The layers
pass them for you; a consumer that calls `forge-ci plan release` itself
passes its runner inputs too, as the GitLab monorepo example does. The plan
is refused when one of its jobs has no runner:

- an image build for a platform the plan includes after `RELEASE_PLATFORMS`
  narrowed it, whose `build-` input of that platform has no value;
- an Xcode or App Store job, whose `darwin-arm64` input has no value.

The refusal names the input, the platform and the forge, and asks for a JSON
list that selects a runner of that platform. There is no fallback across
platforms or roles: an arm64 build never runs on the amd64 runner, and an
image build never on the ordinary one. A default counts as a value. An empty
value or `[]` counts as missing, except for GitLab's `runner-tags` and
`signer-runner-tags`, where `[]` means untagged. `release-platforms`, not
runner availability, selects the platforms; build inputs of platforms that
are not planned may stay empty.

`plan release`, `container snapshot-images plan` and the base stage plans
refuse a call with no `--runner-input` at all, except in a local run
(`--provider local`), which has no runners. The GitLab release builds a base
stage in one amd64 job, so an arm64 base is refused there.

This is a check of the inputs, not proof that a registered runner has the
promised capabilities.

## GitHub runners

The GitHub layer takes its runners as `workflow_call` inputs, not variables.
Each is a JSON array of labels passed as text and read with `fromJSON`, and
is named as [above](#how-a-workflow-names-its-runners). Each input defaults to
a GitHub-hosted label and never falls back to another input.

| Input | Default | Selects |
|---|---|---|
| `runs-on` | `["ubuntu-26.04"]` | ordinary jobs: builds, lint, plans, SBOMs, changelogs, summaries |
| `build-runs-on-linux-amd64` | `["ubuntu-26.04"]` | the linux/amd64 legs of `publish-container.yml` and `snapshot-images.yml`; each leg reads the input of its own arch, so a missing or empty one fails the leg instead of falling back |
| `build-runs-on-linux-arm64` | `["ubuntu-26.04-arm"]` | the linux/arm64 legs of the same image builds |
| `runs-on-darwin-arm64` | `["macos-26"]` | the unsigned Xcode build and the Swift lint jobs |

A reusable workflow declares only the inputs its own jobs or callees use, and
the orchestrators pass theirs on. An image build runner must permit the user
namespaces Buildah needs, as [above](#what-a-job-needs).

Jobs that sign, promote, publish or hold release credentials or an OIDC
token run on the GitHub-hosted `ubuntu-26.04` or `macos-26`, written as a
literal in the layer, and no input or variable moves them. The same holds for
the bootstrap jobs every release starts with, which admit the engine commit
and the runtime images and build the engine every later job runs. The one
exception is `slsa-attestor.yml`, a public `workflow_call` workflow that signs
with the operator's KMS key on the runner its caller names in `runs-on`. Hosted
runners are VMs that GitHub starts fresh for one job and destroys after it,
so no earlier job on the machine could have touched the signer; a runner the
caller chooses could be persistent or shared. Signed Android and Xcode builds
run as a separate `build-variants-signed` job on those hosted runners, while
unsigned and library builds take `runs-on` and `runs-on-darwin-arm64`.

## Runner lending

A lender starts one ephemeral runner on a machine that permits user
namespaces, registered to the orchestrating forge for exactly one job, with
the labels the waiting job requires. The workflow does not move; only that
job's machine does.

```text
pool job: request-runner ──dispatch──▶ lender ──boots a VM──▶ forgejo-runner --ephemeral
                                                                      │ registers with gh-vm,arch=amd64
container job (needs: request-runner, runs-on CI_RUNNER_LABEL_CONTAINER) ◀── takes the one job, VM is gone
```

The request is one step of a small job on the pool, after the engine binary
is available:

```yaml
  request-runner:
    needs: prepare
    runs-on: ${{ fromJSON(vars.CI_RUNNER_LABEL) }}
    env:
      CI_RUNNER_LENDER: ${{ vars.CI_RUNNER_LENDER }}
      CI_RUNNER_LENDER_REPOSITORY: ${{ vars.CI_RUNNER_LENDER_REPOSITORY }}
    steps:
      - uses: https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/setup-forge-ci@PIN_FORGE_CI_SHA
      - env:
          CI_RUNNER_LENDER_TOKEN: ${{ secrets.CI_RUNNER_LENDER_TOKEN }}
        run: |
          printf '%s' "${CI_RUNNER_LENDER_TOKEN}" | CI_RUNNER_LENDER_TOKEN='' \
            forge-ci platform request-runner --arch amd64 --token-file -
```

The container job then has `needs: [prepare, request-runner]` and runs on
`CI_RUNNER_LABEL_CONTAINER`, or on the pool when that is unset. One request
per architecture, as a matrix when the build is one. `forge-ci platform
request-runner` validates the architecture and the wait, asks the lender, and
prints what the next job waits for; with no lender configured it asks nothing
and says so. The token is read
from stdin and never from the runner's own credentials: those stay with the
forge that issued them. [examples/forgejo/signer-image.yml](../examples/forgejo/signer-image.yml)
is a complete single-build-job example. The go-service example instead calls
the multi-job `release-images.yml`, which needs persistent runner capacity
(or kaniko on the normal pool), not one borrowed ephemeral runner.

### Variables and secrets on the orchestrating side

| Name | Kind | Value |
|---|---|---|
| `CI_RUNNER_LENDER` | variable | `github`; unset or `none` when the container runners exist already |
| `CI_RUNNER_LENDER_REPOSITORY` | variable | github: `owner/repository` of the lender repository |
| `CI_RUNNER_LENDER_REF` | variable, optional | github: the branch `lend-runner.yml` is dispatched on; default `main` |
| `CI_RUNNER_LENDER_API_URL` | variable, optional | github: the API root of an enterprise server; the default is api.github.com, never the runner's own `$GITHUB_API_URL` |
| `CI_RUNNER_LENDER_TOKEN` | secret | github: a fine-grained token with Actions write on the lender repository and nothing else |

### Lenders

| Lender | Machine | How the request arrives | Lender-side artifact |
|---|---|---|---|
| `github` | a GitHub-hosted VM, `ubuntu-26.04` or `ubuntu-26.04-arm`, free for public repositories | `workflow_dispatch` of `lend-runner.yml` | a public repository with [examples/runner-lender/github-actions/lend-runner.yml](../examples/runner-lender/github-actions/lend-runner.yml) |
| `none` | the installation's own runners | no request | none |

Every lender runs the same script on the machine,
[scripts/runner-lender/lend-forgejo-runner.sh](../scripts/runner-lender/lend-forgejo-runner.sh):
install the pinned `forgejo-runner` by checksum, create an unprivileged user
with subordinate ids, register `--ephemeral` with the labels, run `one-job
--wait` under a clean environment until the job is done or the wait is over.

### Setting up the GitHub lender

1. Create a public repository in the lending organization, with the
   collaborators who may hold the instance's registration token and no one
   else, protected default branch, signed commits.
2. Copy the example to `.github/workflows/lend-runner.yml`, replacing
   `FORGE_HOST/OWNER/forge-ci` and `PIN_FORGE_CI_SHA` with the engine the
   orchestrating repositories pin.
3. Variable `FORGEJO_INSTANCE_URL`; secret `FORGEJO_RUNNER_REGISTRATION_TOKEN`,
   a runner registration token of the instance, organization level or per
   repository. The VM labels and the labels the runner registers with are
   literals in the file.
4. On the orchestrating side, `CI_RUNNER_LABEL_CONTAINER=["gh-vm","arch=amd64"]`,
   `CI_RUNNER_LABEL_CONTAINER_ARM64=["gh-vm","arch=arm64"]`,
   `CI_RUNNER_LENDER=github`, `CI_RUNNER_LENDER_REPOSITORY=<org>/<repo>`, and
   the secret `CI_RUNNER_LENDER_TOKEN`.
5. Dispatch `lend-runner.yml` once by hand and watch the runner appear and
   disappear in the instance's runner list.

## Building on the pool with kaniko

The other answer to a pool that denies user namespaces is to build without
them. [kaniko](https://github.com/osscontainertools/kaniko) runs the
Containerfile's steps as the job container's own root, in the job container's
own filesystem, and snapshots the difference: no daemon, no privileged
container, no mounts, no user namespace. It fits a pool whose rules forbid
exactly those, and it builds natively for whatever architecture the pool
offers.

It is selected per repository by the image builder type, for the verbs that
build to an OCI layout: `container build` (push-by-digest, oci and local
modes) and `multiarch-image build-arch`. A `FROM scratch` export stage in
local mode gives the same files under either builder. The image, the digest
push, the scan, the ledger and the signer are the same as with Buildah.
`release-images.yml` reads the same variables from the calling organization,
so a consumer that builds its release images there changes nothing to move
between the two.

| Variable | Value |
|---|---|
| `CI_IMAGE_BUILDER` | `kaniko`; unset means `buildah`. Any other value is refused, naming the accepted types |
| `CI_IMAGE_BUILDER_JOB_IMAGE` | the image the build jobs run in: the kaniko job image, pinned by digest. See below |
| `CI_RUNNER_LABEL_CONTAINER*`, `CI_RUNNER_LENDER*` | unset: the builds run on the pool and no runner is borrowed |

What kaniko does not do, and the engine refuses by name instead of working
around:

- **No local image store.** `container build --mode load`,
  `container build-push-oci-image`'s manifest-list build, `image push`, `manifest push`
  and local `image evidence` need Buildah. A smoke test with `podman run`
  cannot run on such a pool either; a consumer that smoke-tests records the
  test as not run, never silently.
- **No build secrets.** kaniko keeps a `--secret` recoverable by later `RUN`
  steps of the same build, so the adapter refuses them. A build that needs a
  secret uses Buildah.
- **Reproducible, not epoch-bound.** kaniko zeroes timestamps instead of
  setting `SOURCE_DATE_EPOCH`; a kaniko build matches another kaniko build of
  the same tree, not a Buildah build of it.

Kaniko itself reads only `.dockerignore` names. The engine gives it the same
ignore file Buildah builds with: the Containerfile's own
`<Containerfile>.containerignore` or `.dockerignore`, else the context's
`.containerignore` or `.dockerignore`.

### The kaniko job image

kaniko replaces the job container's root filesystem with the image's and
deletes it after each build, so a kaniko job runs in a job image made for
that: [containers/kaniko-job/Containerfile](../containers/kaniko-job/Containerfile).
It keeps the tools the steps after the build need under `/kaniko`, which
kaniko never touches; the runner's workspace is a mount and survives too,
forge-ci with it.

The job image is the one image no kaniko job can build. Its bootstrap,
[examples/forgejo/kaniko-job-image.yml](../examples/forgejo/kaniko-job-image.yml),
builds it once per architecture in kaniko's own debug image, on the pool's
native amd64 and arm64 runners, from the pinned engine commit as a git
context, and pushes each as a tag; a third job on the pool, with the engine,
admits each tag only if its architecture and revision label match, and merges
them into one index. Pin the index digest it prints as `CI_IMAGE_BUILDER_JOB_IMAGE`;
a job on either architecture then pulls its own manifest. It runs by dispatch
only, once the signer workflow has published the engine binary of its pin,
because its merge job runs that binary. Rebuild it when the Containerfile or
the kaniko release moves; Renovate tracks both digests. A moved engine pin
needs no rebuild: the image holds kaniko and its tools, and forge-ci comes
from the job's workspace. The previous digest keeps working until it is
replaced.

A job that builds images names no builder. Its container is
`${{ vars.CI_IMAGE_BUILDER_JOB_IMAGE }}` (empty for Buildah, so the job keeps
the runner's own environment), its environment passes `CI_IMAGE_BUILDER` and
`CI_IMAGE_BUILDER_JOB_IMAGE`, and one step, `setup-image-builder`, prepares
whichever is selected before `multiarch-image build-arch`. The signer example
is that job.

On GitLab the policy project selects the builder, not a project variable: the
`release` and `snapshot-images` components take `image-builder: kaniko` and
`builder-job-image`, the kaniko job image, which the snapshot components also
take as their `build-image`. The image jobs still need their per-platform
build inputs, which may name a pool without user namespaces. See
[Release images](gitlab.md#release-images).

#### Pinning the job image

The build jobs run in whatever `CI_IMAGE_BUILDER_JOB_IMAGE` names, as root,
with the registry token, so it is a reviewed digest set by a person, never a
tag and never written by a workflow.

1. Run the bootstrap. Its last step prints the line to set, for example
   `CI_IMAGE_BUILDER_JOB_IMAGE=ghcr.io/example-org/forge-ci-kaniko-job@sha256:…`.
   That is the multi-arch index, not one of its `-amd64` or `-arm64` children.
2. After the first publication, make the package public: the build jobs pull
   it without credentials.
3. Set the variable on the organization, beside `CI_IMAGE_BUILDER`, to that
   value exactly, `@sha256:` included, so every repository reads the one
   value and a rebuild is one paste.

A rebuild may give a new digest from the same sources, because the Debian
packages it installs move on. Delete superseded tags from the package when
nothing pins them.

Before any build, the signer example's build job checks the variable: it
must be a `…@sha256:` reference, and the image must carry the title label of
the forge-ci kaniko job image and the source label of the engine repository,
for the job's architecture. A missing, tag-pinned or foreign image stops the
job there, naming `CI_IMAGE_BUILDER_JOB_IMAGE`; a private package fails
already when the job pulls its container.

Kaniko, like Buildah here, builds only for the architecture it runs on, with
no emulation. Kaniko takes full snapshots of the filesystem, so a large base
image needs enough job memory; raise the runner's memory limit if a build
runs out.

### Choosing between lending and kaniko

| | Lending | kaniko |
|---|---|---|
| Build toolchain | Buildah and Podman unchanged | kaniko for layout builds, Buildah elsewhere |
| Smoke tests | yes, on the lent machine | not on the pool |
| Build secrets | yes | no |
| arm64 | from the lender | from the pool, if it has arm64 runners |
| Needs | a lender repository and two tokens | one job image and its bootstrap |
| Second forge | yes | no |

Both coexist, selected by variables; an installation may use kaniko for most
images and lend a runner for the one build that needs a secret or a smoke test.

## Trust

Two credentials, ranked. The **registration token** lets its holder register a
runner that receives the instance's container jobs and their secrets, such as
registry tokens; it is as sensitive as any runner you trust. It lives on the
lender's side, with the people who may do that, and is rotated when they
change. The **lender token** only starts lends: the lender decides the labels,
so a holder cannot make a runner advertise anything else.

A lent machine runs one job with that job's secrets and is destroyed. Signing
keys never reach it: signing runs on the pool in the signer image. The runner
binary is pinned by version and checksum, runs as a dedicated unprivileged
user, and starts with a clean environment so the credentials of whatever
started the machine are out of its reach.

A job whose lent runner never arrives waits in the forge's queue; its
`timeout-minutes` begins when it starts. Cancel it by hand, and read the
lender's run for the reason.

### Unused-runner retirement limitation

The pinned Forgejo runner 13.2.1 deletes an `--ephemeral` registration only
after it has run a job; it has no way to unregister itself. A lend that
expires idle, or whose VM is cancelled, leaves its registration behind, and
the lending script fails, saying that registration retirement is
unconfirmed. A forge operator removes that registration by its exact ID,
never by name prefix. Do not give the lent machine an administrator token to
work around this.
