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

## How a workflow names its runners

Runner selection is a repository or organization variable, never a literal in
a workflow. Every value is a JSON array of labels that must all match one
runner; see [Variables](forgejo.md#variables).

| Variable | Selects |
|---|---|
| `CI_RUNNER_LABEL`, `CI_RUNNER_LABEL_ARM64` | the pool, per architecture: every job, image builds included |
| `CI_RUNNER_LABEL_CONTAINER`, `CI_RUNNER_LABEL_CONTAINER_ARM64` | optional overrides: where image builds run instead of the pool |

Architecture is always selected by variable name, never by a truthy fallback,
so a missing arm64 variable fails at scheduling instead of routing an arm64
build to amd64. The container-or-pool dimension does fall back, to the pool:
`fromJSON(vars[... 'CI_RUNNER_LABEL_CONTAINER_ARM64' || 'CI_RUNNER_LABEL_CONTAINER'] || vars[... 'CI_RUNNER_LABEL_ARM64' || 'CI_RUNNER_LABEL'])`.
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
A lender for GitLab CI or for a cloud VM API is the same script with another
way of starting the machine; the port has room for them.

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
build to an OCI layout: `container build` (push-by-digest and oci modes) and
`multiarch-image build-arch`. The image, the digest push, the scan, the ledger
and the signer are the same as with Buildah. `release-images.yml` reads the
same variables from the calling organization, so a consumer that builds its
release images there changes nothing to move between the two.

| Variable | Value |
|---|---|
| `CI_IMAGE_BUILDER` | `kaniko`; unset means `buildah`. Any other value is refused, naming the accepted types |
| `CI_IMAGE_BUILDER_JOB_IMAGE` | the image the build jobs run in: the kaniko job image, pinned by digest. See below |
| `CI_RUNNER_LABEL_CONTAINER*`, `CI_RUNNER_LENDER*` | unset: the builds run on the pool and no runner is borrowed |

What kaniko does not do, and the engine refuses by name instead of working
around:

- **No local image store.** `container build --mode load` and `--mode local`,
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

### The kaniko job image

kaniko replaces the job container's root filesystem with the image's, so a
kaniko job runs in a job image made for that:
[containers/kaniko-job/Containerfile](../containers/kaniko-job/Containerfile).
It keeps a static bash, a BusyBox toolset and kaniko's CA bundle under
`/kaniko`, which kaniko never touches, last on `PATH` so that they take over
only once Debian's tools are gone; the runner's workspace is a mount and
survives too, forge-ci with it. The steps after the build
(metadata upload, cleanup) use those and nothing else from the original image.
The adapter also names the layout, the context and the staged registry
credentials with `--ignore-path`.

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

#### Pinning the job image

The build jobs run in whatever `CI_IMAGE_BUILDER_JOB_IMAGE` names, as root,
with the registry token, so it is a reviewed digest set by a person, never a
tag and never written by a workflow.

1. Run the bootstrap. Its last step prints the line to set, for example
   `CI_IMAGE_BUILDER_JOB_IMAGE=ghcr.io/acme/forge-ci-kaniko-job@sha256:…`.
   That is the multi-arch index, not one of its `-amd64` or `-arm64` children.
2. After the first publication, make the package public: the build jobs pull
   it without credentials.
3. Set the variable on the organization, beside `CI_IMAGE_BUILDER`, to that
   value exactly, `@sha256:` included, so every repository reads the one
   value and a rebuild is one paste.

Rebuild when the kaniko release in the Containerfile or the Containerfile
itself moves, not when the engine pin does. A rebuild may produce a new digest even from
the same sources, because the Debian packages it installs move on; the
previous digest stays published and valid, so a variable not yet updated
keeps working. Delete superseded tags from the package when nothing pins
them.

Automating step 3 would need a token that may write the organization's
variables, kept as a secret of a workflow; whoever can change that workflow
could then rewrite any variable, the runner labels and this image included.
A paste per rebuild is the cheaper risk.

Before any build, the signer example's build job checks the variable: it
must be a `…@sha256:` reference, and the image must carry the title label of
the forge-ci kaniko job image and the source label of the engine repository,
for the job's architecture. A missing, tag-pinned or foreign image stops the
job there, naming `CI_IMAGE_BUILDER_JOB_IMAGE`; a private package fails
already when the job pulls its container.

Kaniko, like Buildah here, builds only for the architecture it runs on. A
job's `--custom-platform` must be its runner's, which `build-arch` enforces,
and every image, the job image included, is one native build per architecture
assembled into an index. No emulation, anywhere.

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

With the pinned Forgejo runner **13.2.0**, `--ephemeral` means deletion after
completion of one job. SIGINT stops an idle `one-job --wait` process but does
not delete its registration. The runner has no unregister command, and its
runner RPC protocol has no delete-self operation. The lending script therefore
fails idle expiry with **retirement unconfirmed**, rather than reporting that
the registration was released. Cancelling the hosted VM likewise does not prove
registration removal. A forge operator must verify and remove the exact unused
registration; do not delete registrations by name prefix.

This was reproduced in Forge CI's local live scenario (see
[Local runner-lending lifecycle](testing.md#local-runner-lending-lifecycle)).
The checked upstream sources are tag `v13.2.0`, commit
`df6b843fb929bb04b933b09c6bf208774d42480b`: `internal/app/cmd/cmd.go:42`
defines ephemeral as deletion after a job, and
`internal/app/poll/single.go:133–147` shuts down polling/tasks without revocation.
Its `actions-proto v0.8.0` dependency's `proto/runner/v1/services.proto` has no
retirement RPC.

Neither the registration token nor the per-runner credential is a Forgejo user
management token. In the local live test, the supported repository runner DELETE
endpoint rejected the per-runner credential with HTTP 401; its scoped owner
credential could perform exact-ID cleanup. Do not add an administrator PAT to the worker to work around
this. For automatic unused-runner retirement, the smallest compatible design
choice is an explicitly authorized **forge-side provisioner** retaining the
exact created runner ID and owning its deletion when the lend ends. That
changes the current registration-token provisioning contract and is not
implemented here. An upstream narrowly scoped self-retirement RPC would avoid
that extra management authority. Until one exists, idle/cancel retirement is
not supported by the current lender contract.
