# Forgejo

How `forge-ci` is used on Forgejo, hosted or self-hosted: the reusable
workflows and composite actions under `.forgejo/` that check a pull request,
build, and prepare, sign and publish a release. A job that calls the binary
itself installs it as [Installing the binary in a
job](#installing-the-binary-in-a-job) says; how the CLI recognizes a Forgejo
runner, and what it does differently there, is in
[Providers and runners](providers.md#forgejo).

The three forge guides, [GitHub](github.md), [GitLab](gitlab.md) and
[Forgejo](forgejo.md), use the same section order. The authoritative
implementation, test and live-evidence status per forge is in
[`providers.md`](providers.md).

> **Release source and instance setup.** Pin the exact commit of an authenticated
> release, and serve that commit and its checksum-bound engine package on your
> Forgejo instance. The upstream release does not publish your instance's package
> or signer image. [Release readiness](#release-readiness) lists the checks needed
> for the selected instance and runner.

## What ships

A consumer's build job runs between two workflows of this layer, so signing
keys never enter the consumer's code and the consumer's code never runs in a
job that holds a key.

```text
immutable release request
  -> pinned prepare workflow (SSH signing key)
  -> consumer build job (no signing keys)
  -> digest-bound dist/ hand-off
  -> pinned signer workflow (explicitly enumerated secrets)
  -> signed release and provenance
```

| Path | Content |
|---|---|
| `.forgejo/workflows/` | Five reusable workflows; see [Reusable workflows](#reusable-workflows). |
| `.forgejo/actions/` | Nineteen composite actions; see [Composite actions](#composite-actions). |
| `.forgejo/bin/` | The resolver `lib-forge-ci.sh`, and the checksum of the `forge-ci` binary the layer runs. The binary is not in the tree: a job downloads it by that checksum. See [The binary in the layer](#the-binary-in-the-layer). |
| `.forgejo/scripts/verify-signer-toolchain.sh` | The check every privileged job runs first, and the signer image runs at build time. |
| `containers/signer/Containerfile` | The definition of the signer image. No image is published; see [The signer image](#the-signer-image). |
| `examples/<ecosystem>/forgejo/` | A pull-request and a release workflow to start from, for [Go CLI](../examples/go-cli/forgejo/), [Go service](../examples/go-service/forgejo/), [Cargo](../examples/cargo-app/forgejo/) and [Maven](../examples/maven-app/forgejo/). |
| `examples/forgejo/` | The workflow an organization publishes the layer's binary and builds its signer image with. |

Lint and build ship as actions, not as workflows: the pull-request jobs and
the release build job stay the consumer's, and an action is one step in them.
[Build and checks](#build-and-checks) says why; [Beside the other
layers](#beside-the-other-layers) compares the layer with the
[GitHub](github.md) and [GitLab](gitlab.md) ones.

The layer is flat on purpose. Forgejo resolves a relative `uses:` inside a
called workflow against the **calling** repository, so a workflow of this layer
never calls another one: composition is the consumer's workflow, or a composite
action.

### Beside the other layers

What each of the three layers has, capability by capability, is in
[Delivery layers side by side](providers.md#delivery-layers-side-by-side).

## Requirements

| Requirement | Value |
|---|---|
| Forgejo | v16 or later (reusable-workflow job expansion) |
| Runner | one reviewed, trusted runner, selected by the `CI_RUNNER_LABEL` variable; it must run job containers for the signer image. The layer assumes each job gets a fresh container and filesystem: on a runner that keeps state between jobs, a pull-request job can leave something that a later privileged job runs. |
| Runner architecture | **linux/amd64.** The binary every workflow and action runs is built and published for that platform only, so no job of the layer runs on an arm64 runner. Images and binaries the jobs produce can be for other platforms. |
| Runner job image | Bash, Git, curl and `sha256sum` for every job; Node as well for a job that uses `setup-toolchain`, whose cache and Go set-up steps are JavaScript actions. The signer jobs run in the signer image, which brings its own tools. |
| Instance | this repository, or a fork of it, hosted on the **same instance** as the consumer: a reusable workflow is called as `owner/repository/path@ref` |
| Serving repository | **Readable without credentials** by the runners, and so are the packages of its owner. Every job clones the layer over HTTPS with no token, so a repository that needs a sign-in fails the clone (`could not read Username`), and downloads the layer's binary from that owner's generic packages the same way. |
| The layer's binary | **Published on the instance for the pinned commit**, by the serving repository's owner, before a job runs; see [The binary in the layer](#the-binary-in-the-layer). |
| Transport | HTTPS between the runner and the instance; the helper checkout refuses anything else. A private certificate authority must be trusted by the runner's job images and by the signer image; the layer has no input for a CA bundle. |
| Release branch | `main` unless `prepare-release.yml` is given another `release-branch`; see [The request is the head of the release branch](#the-request-is-the-head-of-the-release-branch). |

### Network

Beside the instance itself (Git over HTTPS and SSH, the API, the package
registry and run artifacts), jobs reach these hosts. An instance with
restricted egress allows them or mirrors what they serve.

The layer's binary is no host of its own: every job that runs the CLI makes
one HTTPS request for it to the same instance, to the generic package registry
under `/api/packages/`, without credentials and without a redirect to another
host.

| Job | Host | For |
|---|---|---|
| `prepare-release.yml` | `github.com` and its release-asset host | The pinned changelog renderer archive. No API call, no token. |
| `prepare-release.yml` | The supplied signer-image registry and its token service | Anonymous manifest availability before consumer checkout or release mutation; private-image callers explicitly skip this lookup. |
| A job that uses `setup-toolchain` | `code.forgejo.org` | The Go set-up and cache actions. |
| | `github.com` and its release-asset host | The pinned mise archive. |
| | the Go download host and the Go module proxy | The Go toolchain and the consumer's modules. |
| | whatever the consumer's mise config names | Its tools. Tools from GitHub releases go through the GitHub API, which is why the action takes a rate-limit token. |
| Pull-request lint | whatever nanolinter's checks fetch | For example a rule registry for the SAST check. The `offline` input of `lint-nanolinter` is for a runner without that access. |
| An image build and scan | the registries of the base images; the Trivy database registry | Base images, and the vulnerability database of the scan. |
| The signing jobs, only with `transparency-log: public` | `rekor.sigstore.dev`, `tuf-repo-cdn.sigstore.dev` | The transparency log entry of each signature. With the default, `none`, the signing jobs reach no Sigstore service; see [The transparency log](#the-transparency-log). |
| The signer image build | `docker.io`, `github.com`, `code.forgejo.org` | The Debian base image, the pinned `jq`, `cosign` and `syft`, and the checkout action. |
| The binary job of the signer-image workflow | `code.forgejo.org`, the Go download host and the Go module proxy | The checkout and Go set-up actions, the Go release `go.mod` names, and the modules the CLI is built from. Only the organization that serves the layer runs it, once per pinned commit. |

A job that uses `setup-buildah` or `setup-toolchain` installs packages, and
reaches the Debian or Ubuntu archive its job image names. The layer fetches
from the official archive hosts (`deb.debian.org`, `security.debian.org`,
`archive.ubuntu.com`, `security.ubuntu.com`, `ports.ubuntu.com`) over HTTPS
when the job image has CA certificates (`/etc/ssl/certs/ca-certificates.crt`,
from the `ca-certificates` package), so a runner whose egress allows only TLS
can install them. The image's sources files are not changed, and packages are
verified by the archive signature as before. Without CA certificates, and for
any other host, the sources are used as the image names them: over HTTP when
that is what they say, with one log line that says so for the official hosts.

An image build's `RUN` steps have the network of the job they run in, and
resolve names with the job's own `/etc/resolv.conf`. The layer builds with
chroot isolation, which shares the job's network namespace, and states the
network as the host's (`buildah bud --isolation chroot --network host`), so a
step never asks a resolver the job does not. A runner whose egress allows DNS
only to its own resolver therefore needs nothing more for a build. A step can
reach what the job can reach, a service on the job's network included; that
is chroot isolation, with or without the flag.

## Getting started

### Calling it

Reusable workflows are called by path on the same instance, composite actions
by full URL. Both forms carry the same commit:

```yaml
jobs:
  prepare:
    uses: OWNER/forge-ci/.forgejo/workflows/prepare-release.yml@<commit>
    with:
      forge-ci-repository: OWNER/forge-ci
      forge-ci-sha: <commit>
      # ...

  build:
    steps:
      - uses: https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/setup-forge-ci@<commit>
```

**One commit, everywhere.** A called workflow runs in the caller's workspace,
which holds none of this repository's files. Each job therefore clones the
repository at `forge-ci-sha`, verifies that the checkout is that commit, and
runs the actions, the resolver and the binary from the clone. The `@<commit>`
of the `uses:` line pins the workflow file; `forge-ci-sha` pins everything
the workflow then runs. They must be the same commit, written out, and
`check-l3-isolation` refuses a pull request where they are not.

**The repository is named too.** `forge-ci-repository` is the
`owner/repository` of the `uses:` line, and it is where each job clones the
layer from. The layer has no default for it, because a default could only name
one organization's repository. Without the input a workflow reads the
`FORGE_CI_REPOSITORY` variable of the calling repository or its
organization; with neither, its first job stops and says so, before any
checkout. Prefer the input: it stands in reviewed code beside the `uses:` line
it repeats, and a variable is one more thing to forget and can be changed
without a commit. Neither is a trust input, since the clone must be exactly
`forge-ci-sha`.

### Quick start

1. **Copy the two examples** of your ecosystem, `pullrequest.yml` and
   `release.yml` from `examples/<ecosystem>/forgejo/` (see the
   [examples index](../examples/README.md)), at the commit you will pin, into
   a draft directory outside `.forgejo/workflows/`. Copy the example's
   `nanolinter.toml` to the repository root: the pull-request workflow's lint
   job runs `nanolinter verify` against it and requires at least the
   `required-lints` floor, default `secrets,sast`.
2. **Replace the placeholders**: `FORGE_HOST`, `OWNER`, `PIN_FORGE_CI_SHA`,
   `PIN_SIGNER_IMAGE_DIGEST` and `PIN_COSIGN_PUBLIC_KEY_SHA256`. The header of
   `release.yml` says what each one is. The tokens are the same in both files,
   so one command fills them:

   ```bash
   sed -i 's|FORGE_HOST|forgejo.example.org|g; s|OWNER|myorg|g; s|PIN_FORGE_CI_SHA|<commit>|g; s|PIN_SIGNER_IMAGE_DIGEST|<digest>|g; s|PIN_COSIGN_PUBLIC_KEY_SHA256|<sha256>|g' pullrequest.yml release.yml
   ```
3. **Fill in every `FIXME`**: the bot identity and the name of your binary in
   `release.yml`, and in the Go service example the image title. For an
   ecosystem without an example, keep the hand-off the build job ends with.
4. **Decide on request signatures.** The example sets
   `verify-release-request-signature: 'false'`. A production consumer reviews
   and commits `release-request-allowed-signers`, then enables the check: an
   annotated request tag does not authenticate its author, and tag protection
   alone leaves one control between a policy error and a signed release.
5. **Set the [variables](#variables) and [secrets](#secrets-and-tokens)**, among them
   `CI_JOB_IMAGE`, the image the pull-request jobs run in.
6. **Add the two files** as `.forgejo/workflows/pullrequest.yml` and
   `.forgejo/workflows/release.yml` through your protected pull-request path.
   Do not activate an incomplete template.
7. **Release** by pushing an immutable `release-request/vX.Y.Z` tag. The flow
   derives the final `vX.Y.Z` tag and creates it once. Never create, move or
   force-push the final tag yourself.

If a release fails partway, re-run the failed jobs of that run. Do not delete
the tag, the release or a published image: once the prepare job has pushed the
final tag, the pipeline retries forward and converges. A release is never
resumed from its final tag; if the prepare job itself fails after pushing
`vX.Y.Z`, release the next patch version with a new request tag.

## Inputs and variables

### Reusable workflows

Forgejo rejects declared `workflow_call.secrets`, so each workflow names the
secrets it reads in a `# workflow-call-secrets-contract:` header comment, and
the caller passes them by name. Never use `secrets: inherit`.

| Workflow | Does | Secrets |
|---|---|---|
| `prepare-release.yml` | Checks that every secret the release will need is configured, before any checkout; guards the request tag; requires the request at the head of the release branch; renders the changelog; with `version-bump`, bumps the project files in a key-free job and verifies that bump; makes the signed release commit and the final tag once. Outputs `release-tag` and `release-sha`. | `RELEASE_SSH_SIGNING_KEY`; presence only of the others; none in the version-bump job |
| `sign-and-publish-release.yml` | Four separately privileged jobs after a secret-free preflight: `sign` (checksums, SBOMs, provenance, images, distro packages), `promote` (final image tags), `publish` (the Forgejo release), `cleanup` (candidate tags). | Cosign, GPG, `RELEASE_TOKEN`, `RELEASE_REGISTRY_TOKEN`, each only in the job that needs it |
| `sign-container-images.yml` | Signs and attests a base-image ledger in one engine call. | Cosign, `RELEASE_REGISTRY_TOKEN` |
| `verify-base-container-images.yml` | Verifies existing base images against the trusted key, anonymously; a miss is not an error. | none |
| `promote-base-container-images.yml` | Verifies base-image signatures, then moves the final tags. | `RELEASE_REGISTRY_TOKEN` |

Each workflow's `inputs:` block is its reference: every input carries a
description. The inputs they share:

| Input | Required | Meaning |
|---|---|---|
| `runner-label` | yes | The runner every job of the workflow runs on. |
| `forge-ci-sha` | yes | The commit to check the layer's files out at: the same commit as the `uses:` pin. |
| `forge-ci-repository` | yes, unless the variable is set | The repository that serves the layer, as `owner/repository` on the instance or as an HTTPS Git URL: the one in the `uses:` line. Empty takes the `FORGE_CI_REPOSITORY` variable. With neither, the `coordinates` job fails. |
| `signer-image` | yes, in prepare and the four signer workflows | Your organization's signer image, pinned by digest; pass the same literal to preparation and signing. |
| `skip-signer-image-check` | no, in prepare | `'false'` by default. Set explicitly to `'true'` for a private signer image whose anonymous availability cannot be checked. |
| `cosign-public-key`, `cosign-public-key-sha256` | yes, in the four signer workflows | Your organization's Cosign public key, and its SHA-256 as `sha256sum cosign.pub` prints it. |
| `release-branch` | no, in prepare | The branch the release lands on, `main` by default. See [The request is the head of the release branch](#the-request-is-the-head-of-the-release-branch). |
| `transparency-log` | no, in the four signer workflows | `none` (the default) or `public`: whether signatures are also written to the public Sigstore transparency log. One value for every workflow of an organization; see [The transparency log](#the-transparency-log). |
| `release-asset-retention` | no, in `sign-and-publish-release.yml` | How many of the newest releases keep every file; older ones keep their checksums and signatures. Empty, the default, deletes nothing. See [Old releases' files](#old-releases-files). |

A secret-free `coordinates` job validates the repository and the image
coordinate before any container starts: no credentials, query or fragment in
the URL, and an image pinned by `sha256` digest.

### Composite actions

Every action reaches the binary through the resolver, never through `PATH`
alone. Each `action.yml` documents its own inputs and outputs.

| Action | Does |
|---|---|
| `setup-forge-ci` | Downloads the layer's binary, verifies it against the recorded checksum and puts it on the job `PATH`. The first step of a consumer job that calls `forge-ci` itself. |
| `check-l3-isolation` | The pull-request gate over the consumer's release workflow: no signing secret in the build job, enumerated signer secrets, the digest hand-off, one commit across every reference, and the signer image, the key digest and the transparency-log setting written out as literals. |
| `checkout-consumer` | Checks the consumer out without Node and without persisted credentials. |
| `setup-toolchain` | Installs the consumer's pinned tools with mise, verified and cache-aware. |
| `lint-nanolinter` | The pull-request lint gate: the repository's nanolinter plan with a mandated floor. |
| `build-go`, `build-cargo` | The build step of the consumer's job: tests, Build SBOM and one binary per platform. |
| `build-maven` | The build step of a Maven consumer's job: tests, the jar and the Build SBOM, as GitHub's `build-maven` workflow builds them, after checking that the POM declares the release version. |
| `build-release-image` | One release image from build to ledger: build and push, scan every platform, record. |
| `guard-release-tag`, `render-changelog`, `prepare-release` | The steps `prepare-release.yml` is made of, with rendering and signing in separate jobs. Call the workflow; see [The prepare job](#the-prepare-job). |
| `upload-dist`, `assemble-release-dist` | Hand `dist/` to the signer with its digest, from one producer or several. |
| `registry-login`, `setup-buildah`, `build-push-oci-image`, `record-release-image` | The single steps of an image build, for a release `build-release-image` does not fit. Such a job scans with `forge-ci container image-evidence` in a step of its own, the call `build-release-image` makes. |
| `prune-base-images` | Base-image maintenance on a schedule: deletes promoted base images that no supported release image's attestation names. A dry run unless told otherwise. |
| `verify-forgejo-token` | Checks a release token before it is needed. |

### Variables

Set these on the consumer repository or its organization. An operator supplies
each value through a trusted channel; none is derived during a release job.

| Variable | Value |
|---|---|
| `CI_RUNNER_LABEL` | The exact label of the trusted runner. |
| `CI_JOB_IMAGE` | The image the pull-request lint and build jobs run in, pinned by digest: a nanolinter image of your ecosystem's flavour, or your own image with nanolinter, its check tools and your toolchain. |
| `RELEASE_GIT_URL` | The exact write URL, as `ssh://git@host[:port]/owner/repository.git`. |
| `RELEASE_GIT_HOST_KEY_FINGERPRINT` | That endpoint's ed25519 host-key fingerprint. A fingerprint fetched from the host it is meant to verify verifies nothing. |
| `COSIGN_PUBLIC_KEY` | The PEM text of your Cosign public key, if you pass it from a variable as the example does. |
| `FORGE_CI_REPOSITORY` | `owner/repository` of the repository that serves the layer. Only read when a call does not pass `forge-ci-repository`, which the examples do. |

### Variables the layer sets

The workflows set these for their own steps. They are not inputs: a consumer
supplies values through workflow inputs and secrets. The layer's own names
(`HELPERS_*`, `SIGNER_*`) are none the CLI reads, so none of them can become a
CLI input by accident.

| Variable | Purpose |
|---|---|
| `FORGE_CI_REPOSITORY_URL` | The validated URL each job clones the layer from. The resolver reads it to find the layer's binary under that repository's owner. |
| `HELPERS_DIR` | The verified checkout of the layer in the job's private workspace. |
| `EXPECTED_HELPERS_SHA` | The commit that checkout must be, from `forge-ci-sha`. |
| `SIGNER_STRICT` | `1` in every signer job (not in the prepare job of `prepare-release.yml`): the toolchain check refuses interpreters and a checkout without its binary, an engine override must match the recorded checksum, and the resolver downloads only in the job's fetch step. |
| `SIGNER_WORKSPACE`, `CONSUMER_WORKSPACE` | The job's private scratch directory and the consumer checkout inside it, removed by the job's last step. |
| `COSIGN_KEY`, `COSIGN_PASSWORD`, `GPG_SIGNING_*`, `REGISTRY_TOKEN`, `REGISTRY_USER`, `FORGE_CI_PACKAGE_API_TOKEN` | Credentials, each set only on the step that uses it. `FORGE_CI_PACKAGE_API_TOKEN` selects the package-API credential for tag deletion. |

## Secrets and tokens

Every secret the layer reads is named `RELEASE_<what>`. The caller passes each
one by name; the workflows hand it to the CLI under the CLI's own variable
names, which are not these.

| Secret | Role | Used for | GitHub layer |
|---|---|---|---|
| `RELEASE_SSH_SIGNING_KEY` | SSH private key of the release bot | The signed changelog commit and the final release tag. | same name |
| `RELEASE_TOKEN` | Forge API token | Creating the release and uploading its assets. | same name |
| `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE` | OpenPGP signing key and its passphrase | `.sig` sidecars of `.deb`, `.rpm` and `.apk` packages, when `sign-distro-packages` is on. | same names |
| `RELEASE_GPG_FINGERPRINT` | Fingerprint the imported OpenPGP key must have | The same package signatures. | no such secret |
| `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE` | Cosign private key and its passphrase | Checksum, SBOM, provenance and image signatures and attestations. | no such secret: that layer signs keyless |
| `RELEASE_REGISTRY_TOKEN` | Token that may write packages of the owner, or, for images on another registry, a token for that registry | Signing, promoting and cleaning up container images, and, in the signer-image workflow, pushing the signer image and uploading the layer's binary. A release without an image ledger does not need it. | no such secret |

Four of these are needed by every release and the prepare workflow checks
them by name before any checkout: `RELEASE_SSH_SIGNING_KEY`, `RELEASE_TOKEN`,
`RELEASE_COSIGN_PRIVATE_KEY` and `RELEASE_COSIGN_PASSPHRASE`. The three
`RELEASE_GPG_*` secrets are required only with `sign-distro-packages: 'true'`,
and `RELEASE_REGISTRY_TOKEN` only with `publish-container-images: 'true'` or a
release that publishes images. The examples hand all eight over, configured
or not, so the workflow's own check is the one place that decides; an
unconfigured secret arrives empty and is named only if an enabled feature
needs it.

Where both layers have a secret, the name is the same, so an organization on
both forges configures one set of names.

A pull-request job receives no secret and no rate-limit token. It installs
nothing and uses the tools the runner already has.

## Images and the engine binary

The Forgejo layer has no runtime images. The engine's runtime images are
published on ghcr.io by this repository's GitHub workflows, which a Forgejo
instance may not reach, so every job runs in an image the job or the
organization provides:

| Job | Image | Tools |
|---|---|---|
| Pull-request lint and build | `CI_JOB_IMAGE` | nanolinter with its check tools, and your toolchain. Nothing is installed. |
| Release build | The runner's job image, with `setup-toolchain` | Go and the tools of the consumer's mise config, installed and verified. |
| `prepare-release.yml` | The runner's job image | Bash, Git, curl and `sha256sum`. |
| `prepare-release.yml`, version-bump job | `version-bump-image`, digest-pinned | Bash, Git, curl, `sha256sum` and the build tool: a JDK and Maven for `maven`. |
| Signing, promotion, publication | The signer image | The organization's, built from `containers/signer/Containerfile`. |

Every job that runs the CLI downloads the layer's own build of it, by the
checksum the pinned commit records. [Build and checks](#build-and-checks)
says where the build job's tools come from.

### The signer image

The four signer workflows run their privileged jobs in one image, which the
calling organization builds and publishes.
[`examples/forgejo/signer-image.yml`](../examples/forgejo/signer-image.yml) is
the workflow for it. Its first job publishes the layer's binary for the pinned
commit, which the other jobs then run; see
[The binary in the layer](#the-binary-in-the-layer). They check your copy of
this repository out at that commit, verify the checkout, build the image for
linux/amd64 and linux/arm64, push the manifest list to the instance's registry as
`<host>/<engine owner>/forge-ci-signer`, and print the
digest-pinned reference. The layer sets up no emulation: building the arm64
half on an amd64 runner depends on what the runner host provides.

`FORGE_CI_COMMIT_SHA` names the engine checkout and is passed as `--source-sha`;
the OCI source URL and revision therefore both identify that checkout.
`SIGNER_RECIPE_COMMIT_SHA` is the workflow repository's commit. Its first 40 hex
characters form the prefix `signer-<recipe>-`, followed by the full engine commit
and, for architecture tags, `-amd64` or `-arm64`. Bounding the recipe part keeps
tags within 128 characters even when both repositories use SHA-256 objects.
Consumers still pin the registry-served digest, not these descriptive tags.

`container multiarch-image build-arch` builds with Buildah, exports the result
once to an OCI layout, and publishes that layout through the Go registry adapter.
Push retries reuse the same layout; the served manifest must match its digest
before metadata is written. The temporary layout is then removed. `assemble`
checks that each admitted digest names a single image of the requested Linux
architecture, then merges directly through that same registry adapter. Its
read-back must match the constructed index digest. The assembly job needs no
Buildah installation or local image store. Both registry operations use the
explicit auth file.

The example selects `build-arch --stage` instead: each architecture job retains
its layout inside the metadata artifact and makes no container registry write.
`assemble --from-layouts` depends on the entire build matrix succeeding. Before
uploading any image it checks every metadata binding and the manifest, config
and compressed-layer SHA-256 values and sizes, plus the actual platform and OCI
revision label. Assembly then preserves the existing architecture tags, final
manifest tag and `image-ref`, `image-digest`, `image-tag` outputs. Any separately
required scans must run against the retained layout before its build job succeeds;
the record proves content identity, not that a scan ran. The example's existing
generic-package binary publisher remains the bootstrap prerequisite.

For ordinary builds the equivalent is `container build --mode oci --scan
--output-dir <fresh-directory>`, followed by `container layout record`. Transfer
the whole directory in the current run's artifact store. A dependent job downloads
one separate directory per platform and calls `container layout publish` with the
independently expected repository, full source SHA and complete platform set.
OCI staging disables registry-cache export even with scanning off; reads remain.

This handoff uses the existing artifact limits: 5 GiB per file, 10 GiB and 10,000
files per artifact/download set; instance quotas may be smaller. Allow disk for
all compressed layouts plus download/extraction staging. GitHub uploads use no
extra ZIP compression and layout retention is one day. Oversized or incomplete
handoffs fail before registry publication. Registry failure after admission can
leave unreferenced architecture images; retries reuse the same bytes and do not
delete registry data. Rebuild and verify image/index digests when adopting this
revision, and update pinned caller workflows together with their engine binary.

By hand, for one architecture:

```bash
buildah build --file containers/signer/Containerfile --tag <registry>/<owner>/<name> .
```

The build context is the repository root, cut down to the one file the image
copies by `containers/signer/Containerfile.containerignore`. Buildah and
Podman read that file because it is named after the Containerfile; a builder
that does not sends the whole checkout, Git history and a downloaded binary
included, which costs time and changes nothing in the image.

The image holds `jq`, `cosign`, `syft`, `skopeo`, `gpg` and Git on Debian, and
its build runs `verify-signer-toolchain.sh` in strict mode, which refuses
`sudo`, `gh`, `bun`, `python`, `python3`, `ruby`, `node`, `npm` and `mise`.
Debian's essential `perl-base` remains. Operating-system packages follow the
distribution at build time, which is why a consumer pins the resulting image
by digest and not by tag. Build it from the commit your consumers pin, and
rebuild and repin when that commit moves: the image carries the toolchain
check of its commit.

This repository publishes no signer image and names none, so `signer-image`
has no default: a missing one fails the `coordinates` job with a message that
says where the definition is. A reference that is well formed but names no
image is not caught there: the signing job fails when its container cannot
start, which is after the prepare job has pushed the final tag.

### The binary in the layer

The actions and workflows run one build of this repository's own CLI, for
linux/amd64. The tree does not hold it. It holds one line,
`.forgejo/bin/forge-ci.sha256`: the SHA-256 of the binary that belongs to
that commit. Every job gets the binary by that line.

- **From where.** The generic package registry of the instance that serves
  the layer, under the owner of the repository that serves it, with the
  digest as the version:

  ```text
  https://FORGE_HOST/api/packages/OWNER/generic/forge-ci/<sha256>/forge-ci-linux-amd64
  ```

  The owner is the layer's, never the consumer's. An action the runner
  fetched by URL takes the instance and the repository from the runner
  (`GITHUB_SERVER_URL`, `GITHUB_ACTION_REPOSITORY`). A workflow, which checks
  the layer out itself, passes the repository it cloned
  (`FORGE_CI_REPOSITORY_URL`).
- **How.** One HTTPS request without credentials, following a redirect only
  to HTTPS, refusing more than 128 MiB, into a temporary file beside the
  destination.
- **Verified before it exists.** The file is moved into place only when its
  SHA-256 is the recorded one. `lib-forge-ci.sh` checks it against the
  same line again before every use, a version probe included.
- **To where.** In a job that checked the layer out, beside the checksum in
  that checkout. In an action fetched by URL, whose directory the runner
  discards after the step, a directory named by the digest in the job's
  temporary directory (`RUNNER_TEMP`); `setup-forge-ci` puts it on `PATH`.
  A job downloads once, and later steps find the file.

The package registry is storage and not a trust anchor. Whoever can write the
owner's packages can make a job fail, and cannot make it run other bytes: a
job accepts only the build its pinned commit records. The checksum detects a
changed executable for a revision. It does not authenticate the revision:
that is what pinning an authenticated commit is for.

**A commit works once its binary is published.** Publishing is the first job
of the organization's
[signer-image workflow](../examples/forgejo/signer-image.yml), run at the
commit it pins. A job of a commit whose binary is not there fails in the
first step that needs the CLI, and the message names the address and that
workflow. A version of a generic package cannot be uploaded twice, so a
published binary stays what it is; a commit that changes no Go code names a
binary that is already there.

**A privileged job downloads in a step of its own**, "Fetch engine binary",
before the toolchain check and before any secret; see
[Signing boundary](#signing-boundary). Everywhere else the resolver downloads
on first use.

**The build is reproducible, within limits.**
[`scripts/ci/forgejo-binary.sh`](../scripts/ci/forgejo-binary.sh) is its one
definition: no cgo, trimmed paths, no VCS stamp, one constant version string,
modules from the local cache only. The bytes depend on the build inputs
(`cmd/`, `internal/`, `go.mod`, `go.sum`) and on the Go toolchain, and on no
commit, date, directory or user. The same sources gave the same SHA-256 at
two commits, in two directories with separate caches, and in a clean
`golang` container without network. The limits:

- The toolchain is exactly the Go release `go.mod` names, as the Go project
  distributes it. Another release writes other bytes, and a distributor's
  patched build of the same release may. The script and the guard refuse or
  report another release; neither can see a patch.
- The checksum in a commit was recorded on its author's machine, from that
  machine's module cache, with the module proxy and the checksum database
  switched off. That cache is therefore part of what the recorded line rests
  on. The binary job of the signer-image workflow is the independent
  rebuild: it downloads the modules itself, verified against `go.sum`,
  builds, and publishes only a build that is byte for byte the recorded one.
- Nothing is promised across platforms. The one build is linux/amd64.

`forge-ci --version` of this binary answers `forgejo-layer` at every
commit. `setup-forge-ci` prints the SHA-256 beside it, and the digest is
what identifies a build.

**Beside the GitHub and GitLab layers.** All three download a binary and
verify it before they run it. What the verification rests on differs, and the
three are not the same guarantee:

| Layer | Downloads | Verified against | Anchor |
|---|---|---|---|
| GitHub, GitLab | A tagged release from GitHub Releases, or the runtime image that carries it. | `checksums.txt` and its Sigstore bundle. | The identity of the workflow that built and signed the release. |
| Forgejo | The build a commit names, from the serving instance's packages. | The SHA-256 recorded in the pinned commit. | The commit pin. Nothing says who built the bytes; the recorded line says which bytes, and the rebuild above ties them to the sources. |

The upstream GitHub release has a Sigstore-verified binary installer,
`.github/actions/install-forge-ci`, for jobs that select that release. The
Forgejo layer uses its separately recorded build and instance-local package;
an upstream release archive does not replace that checksum-bound package.

`FORGE_CI_BIN` names another binary to run. It is a test and development
seam: under `SIGNER_STRICT=1`, which the signer jobs set, an override must
match the recorded checksum like the layer's own binary. The signer jobs are
those of `sign-and-publish-release.yml`, `sign-container-images.yml`,
`verify-base-container-images.yml` and `promote-base-container-images.yml`.
The prepare job of `prepare-release.yml` holds the SSH signing key without
`SIGNER_STRICT` or a signer container; it runs only the pinned layer's signing
action in a fresh job, receiving rendered data from a separate key-free job.

### Installing the binary in a job

A job of your own that calls `forge-ci` gets the binary through the layer, at
the commit the workflow already pins:

```yaml
- name: Set up forge-ci
  uses: https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/setup-forge-ci@<commit>
```

The action downloads the binary that commit records from the instance's
package registry, verifies it against the recorded checksum and adds it to
`PATH`; `expected-sha256` adds a pin of the caller's own. A standalone job can
instead install an authenticated upstream release with
`.github/actions/install-forge-ci`; see
[Installing `forge-ci`](providers.md#installing-forge-ci).

Run artifacts between such jobs are the current run's only: keep producing
and consuming jobs `needs:`-chained inside one workflow run, as the release
examples do with `upload-dist`.

## Pull-request gates and lint

The pull-request jobs are the consumer's own, and the layer offers two steps
for them; every `pullrequest.yml` in the examples, such as
[Go CLI](../examples/go-cli/forgejo/pullrequest.yml), wires both:

- **`lint-nanolinter`**: `nanolinter verify --require <floor>` against the
  repository's `nanolinter.toml`. The default floor is `secrets,sast`, as on
  the other forges; `disable-checks` leaves out checks another job owns, and
  `offline` is for a runner without network. It stops with a message when the
  job image has no `nanolinter`; it does not download one. The `commits`
  check, in every example plan, validates the pull request's commit messages
  with gommitlint against the base branch, which is why `checkout-consumer`
  takes `fetch-base`; every flavour image carries gommitlint.
  [Lint Workflows](components.md#lint-workflows) shows the same
  choice on each forge.
- **`check-l3-isolation`**: the release-isolation gate over the consumer's
  workflows: no signing secret in the build job, enumerated signer secrets,
  the digest hand-off, one commit across every reference, and the signer
  image, key digest and transparency-log setting written out as literals.
  [Signing boundary](#signing-boundary) says what it does not check.

nanolinter is the only lint engine: the layer has no MegaLinter action. Tests
run in the consumer's own job, for example through `build-go`, which runs
`go test ./...`. A pull-request job receives no secret and installs nothing.

The gate is the protected branch: require the pull-request jobs as status
checks. A direct push bypasses them.

## Builds

Container builds run their `RUN` steps on the job's own network (`--network host`), so they resolve and reach what the job's steps do, the host's loopback services included; a build is trusted like the job.

### Build and checks

The pull-request jobs and the release build job belong to the consumer. That
is the isolation the layer exists for: the consumer's code runs in the
consumer's job, which holds no signing key. So the layer offers the build and
the lint as actions, each one step of such a job, and the `forgejo/`
directory of each ecosystem example is the two workflows they add up to.

| Step | Action | Runs |
|---|---|---|
| Lint | `lint-nanolinter` | `nanolinter verify --require <floor>`, with `disable-checks` and `offline` for what another job or the runner's network decides. The default floor is `secrets,sast`, as on the other forges. |
| Release isolation | `check-l3-isolation` | `validate isolation` over the consumer's `release.yml`. |
| Build, Go | `build-go` | `build go run`: `go mod download`, `go test ./...`, the Build SBOM, one binary per platform. |
| Build, Cargo | `build-cargo` | `build cargo run`: `cargo fetch`, `cargo test --locked`, the Build SBOM, one binary per platform. |
| Build, Maven | `build-maven` | `build maven run --expect-version`: `mvn install`, a refusal unless the POM declares the release version, the app or library lifecycle with its tests, the Build SBOM, all in `target/`. |
| Hand-off | a `run:` step | `release files declare`, which writes the checksums file and `dist/release-files.json`; then `upload-dist`. |
| Image | `build-release-image` | `container build-push-oci-image`, `container image-evidence` for every pushed platform, `container ledger add`. |

**Where the tools come from.** The engine's runtime images are published on
ghcr.io by this repository's GitHub workflows, which a Forgejo instance may
not reach, so no action assumes one. Each runs in whatever the job provides:

- A pull-request job installs nothing and receives no secret. Its lint and
  build jobs run in the image the `CI_JOB_IMAGE` variable names, which carries
  nanolinter with its check tools and the toolchain. `lint-nanolinter` stops
  with a message when the job has no `nanolinter`; it does not download one.
- The release build job installs its toolchain with `setup-toolchain`: Go, and
  the tools of the consumer's mise config, among them `cyclonedx-gomod` or
  `cargo-cyclonedx` for the Build SBOM. `build-sbom: 'false'` drops that
  requirement. A Maven build needs the JDK and Maven from the mise config
  (or a job image); `build-maven` pins the `cyclonedx-maven-plugin` itself,
  in its `versions.env`, at the version GitHub and GitLab use.
  A job downloads Go once. When the mise config declares `go`, mise installs
  it, the release and checksum `mise.lock` records under `mise-locked: 'true'`,
  and the job runs with `GOTOOLCHAIN=local`, so `go.mod` asking for a newer Go
  fails the build instead of fetching one. Only when the mise config declares
  no `go` does the `setup-go` action install the version `go.mod` names.
  Every mise install, the Go download among them, is tried three times, and
  so is rustup's download of the toolchain `rust-toolchain.toml` pins.
  mise waits `mise-http-timeout-seconds` (default 120) for a connection or
  between two reads of a download, in place of its own 30 seconds, which a
  slow link to a download host exceeds; the whole download stays bounded by
  mise's `http_download_timeout`.

**Version and commit are inputs.** In a release the runner's ref is the
request tag and its commit the request commit, so `build-go` and
`build-cargo` require `version` and `commit`, and `build-maven` requires
`commit`; none takes them from the runner: pass the prepare job's
`release-tag` and `release-sha`. A pull request passes `build-maven` no
`version`, and builds the version its POM declares.

**A Maven release commit carries its version.** `prepare-release.yml` with
`version-bump: maven` and a digest-pinned `version-bump-image` bumps the POMs
in the signed release commit, as GitHub's and GitLab's release preparation
do, so the tagged source declares the version the jar carries.
`build-maven`, given the release's `version`, refuses a POM that declares
another. [The prepare job](#the-prepare-job) says how the bump is kept out
of the job that holds the signing key.

**Rendered release notes are ignored by Git.** The hand-off step of
`release.yml` reads a `release-notes.md` that an earlier step of the build job
rendered in the checkout root. List that file in `.gitignore`: an untracked
file that is not ignored makes the checkout dirty, and GoReleaser then stops
with `git is in a dirty state`.

Extract release notes from the prepared, signed changelog with
`forge-ci release notes --changelog-file CHANGELOG.md --release-version
vX.Y.Z --target-file release-notes.md`. It selects exactly that version's
level-two section, retains Markdown reference links, and refuses a missing
or duplicate section. Consumers may append their own installation and
verification links; no second changelog renderer or tag fetch is needed.

**An image is one step.** `build-release-image` takes the per-platform build
plan, pushes the manifest list, scans each platform of the pushed digest into
the path template `scan-output` names (`dist/trivy-results-<role>-{arch}.json`),
and records the image in `dist/release-images.json`. A scan that fails is run
again, three times in all, because Trivy's database download fails
transiently. The job logs in and sets
buildah up first, with `registry-login` and `setup-buildah`, and passes
`publish-container-images: 'true'` to the prepare workflow. A composite action
on Forgejo cannot call a sibling by relative path, so the action makes the
three engine calls itself; the single-step actions remain for a release that
stages a candidate tag or builds on a base image. The layer has one scan:
`container image-evidence`, with the retry above.

## Releases and promotion

A release starts when an immutable `release-request/vX.Y.Z` tag is pushed.
The consumer's `release.yml` calls the layer around its own build job:

1. **`prepare-release.yml`** checks that every secret the release needs is
   configured, guards the request tag, requires it at the head of the release branch,
   renders the changelog in a key-free job, and pushes the signed release
   commit and final `vX.Y.Z` tag once. It outputs `release-tag` and
   `release-sha`.
2. **The consumer's build job** builds at that tag without signing keys,
   declares the release files, records images in `dist/release-images.json`,
   and hands `dist/` on with its digest.
3. **`sign-and-publish-release.yml`** runs four separately privileged jobs:
   `sign`, `promote` (final image tags), `publish` (the Forgejo release) and
   `cleanup` (candidate tags).

**Promotion** is the `promote` job: after signing, it moves the final tags of
the images the build recorded. There is no `:dev`/`:staging` ladder. Base
images have their own lifecycle: `verify-base-container-images.yml`,
`sign-container-images.yml` and `promote-base-container-images.yml`, and the
`prune-base-images` action on a schedule.

Forgejo drops an untagged manifest as soon as its last tag moves away, so a
rollback can restore a moved tag only while another tag, in practice the
immutable version tag, still names the image; see
[Implementation status](providers.md#implementation-status).

If a release fails partway, re-run the failed jobs of that run, as
[Quick start](#quick-start) says; [Failed Release
Recovery](failed-release-recovery.md) covers the rest.

### Old releases' files

Each release adds its archives, binaries and packages, and a forge's storage
quota fills. With `release-asset-retention: <N>` on `sign-and-publish-release.yml` the release keeps every file
of the newest N published releases. After it publishes, each older release
keeps its checksum manifests, signatures, bundles and SBOMs, and loses the
rest; the releases, their notes and their tags stay. Empty, the default,
deletes nothing. With reproducible builds a deleted file can be rebuilt from
its tag and checked against the kept checksums and against its own
signature. Drafts are neither counted nor touched.
`forge-ci release asset-retention --repository <owner/repo> --keep <N>` is a
dry run that lists what a value would delete; `--keep-files` changes which
files old releases keep.
The job deletes with `RELEASE_TOKEN`.

### Images on another registry

The release, its files, signatures and the engine binary stay on Forgejo; the
images can live on another registry, such as GHCR, where storage does not
count against the forge's package quota. The caller names the repositories:

- `publication-image-repository` of `sign-and-publish-release.yml` is the
  release images' repository, for example `ghcr.io/owner/project`; the build
  job records its images there in `dist/release-images.json`.
- `base-repository` of the base-image workflows is the bases' repository, for
  example `ghcr.io/owner/project-base`.
- `registry-user` is the user `RELEASE_REGISTRY_TOKEN` logs in as there.
  Empty is the calling repository's owner, which is right for Forgejo's own
  registry.

A Forgejo instance serves its registry at its own host and port, so a
repository on any other host is external: the steps pass
`--external-registry` (`EXTERNAL_IMAGE_REGISTRY`) to the engine, which logs
in to that repository's registry, takes the repository as declared instead of
requiring it under the forge owner, and deletes tags through that registry.
The declaration is in the caller's reviewed workflow, every ledger image must
be in the declared repository, and the registry's own token decides what can be
written there.

GHCR deletes nothing through the registry API, so on `ghcr.io` the cleanup
deletes through the GitHub Packages API with the same registry token, which
then needs `delete:packages` (a classic token: fine-grained tokens cannot reach
GitHub Packages). GHCR deletes a version with all its tags, and a candidate or
staging tag usually shares its manifest with the promoted tag, so such a tag
stays and the cleanup says which tag keeps it; it costs no storage. A token
without `delete:packages` keeps every tag, reported, without failing a finished
release. Another registry that does not delete tags answers 405 or
`UNSUPPORTED`, and its tags stay reported the same way. Old versions are
removed with `forge-ci container package-retention`.

Base-image verification checks existing bases anonymously, so only public
bases are reused. GHCR creates packages private and refuses an anonymous read
of one, or of a package that does not exist yet, with DENIED: that counts as a
miss, the bases are built, and the log says to make the package public. GHCR
has no default visibility to change: the organization's package setting only
limits which visibilities members may choose, and linking a package to a
public repository does not make it public. Make each new package name public
once, in its package settings, at its first release: the bases' repository
and the release images' repository.

### The prepare job

Call `prepare-release.yml`; do not compose the prepare job from its actions.
What a consumer needs beside it is a job of its own that `prepare` waits for:

```yaml
jobs:
  preflight:            # yours: a token check, a full test run
    runs-on: ${{ vars.CI_RUNNER_LABEL }}
    steps:
      - uses: https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/verify-forgejo-token@<commit>
        with:
          token: ${{ secrets.RELEASE_TOKEN }}

  prepare:
    needs: preflight
    uses: OWNER/forge-ci/.forgejo/workflows/prepare-release.yml@<commit>
```

The changelog renderer is `changelog-creator`, `git-cliff` by default or
`git-chglog`, reading the consumer's `changelog-config` and `commit-body-config`,
by default `.forge-ci/gitcliff/keepachangelog.toml` and
`.forge-ci/gitcliff/minimal.toml`: copy the two files of this repository's
[`templates/gitcliff/`](../templates/gitcliff/) there to start, the same
defaults the GitLab `release` component has.

The workflow keeps the boundaries a hand-composed job has to get right itself:
every secret is checked for presence before any checkout, no token enters the
job that holds the SSH signing key, and the request is admitted only at the
head of the release branch. Project-controlled changelog configuration runs only in the
`render` job, which receives secret-presence booleans but no signing secret.
Git-cliff 2.10.0's `replace_command` hooks execute shell commands; unsetting a
key before rendering would not prevent a hook leaving code for a later step.
The fresh `prepare` job checks out both repositories again and downloads an
artifact outside them, copying only the regular `CHANGELOG.md` and
`commit-msg.txt` files. It runs no renderer or project script and validates
the request again before loading the SSH key. This relies on the fresh-job
runner requirement above: no shared filesystem or processes between jobs.
The request commit and the request tag are the event's,
`forgejo.sha` and `forgejo.ref_name`, in every job of the run, so they are not
outputs.

Preparation also takes the same `signer-image` literal as the signer workflow.
After fetching the checksum-verified engine and before checking out consumer
code, it reads the image manifest anonymously. A missing or unreadable image
fails before any release commit or final tag is created. The lookup uses
`container manifest digest --anonymous`: no auth file, ambient registry login
or credential helper is consulted. It checks availability, not the image's
toolchain or signing identity.

For a private image, pass `skip-signer-image-check: 'true'` explicitly and
configure image-pull access on the trusted runner. The digest-pinned coordinate
and the signer's own toolchain/key checks remain mandatory. A misspelled skip
value fails instead of silently bypassing the check.

**A version bump never runs where the key is.** A bump of project files runs
the project's build tool, and with it the project's plugins: `mvn
versions:set` for `version-bump: maven`. So it runs in a `version-bump` job of
its own, in the `version-bump-image` the caller pins by digest, and that job
references no secret at all, not even a presence check. It runs the engine's
`version bump`, the same verb GitHub's and GitLab's release preparation run,
and hands over only `git diff` of the result, uploaded as an artifact, with
the patch's sha256 as a job output.

The `prepare` job runs no build tool. `forge-ci version apply-bump` checks the
patch as text before Git applies it: it matches the reported digest, touches
only `pom.xml` files, creates, deletes, renames or re-modes nothing, and every
changed line is the root POM's current version replaced by the release
version and nothing else. After applying it, the root POM must declare the
release version. One limit: a line naming another artifact at exactly the
project's old version, such as a sibling module or a dependency released in
step, also passes, because `versions:set` legitimately moves reactor modules
the same way. The files it changed are committed beside `CHANGELOG.md`
in the signed release commit; any other file the bump job touched never
reaches it.

This is stricter than GitHub's `version-bump.yml`, which runs the bump in the
job that later signs and pushes the release commit. A POM whose version is a
property, such as `${revision}`, is refused: the substitution can only be
checked against a literal version. Leave `version-bump` empty and the release
commit carries the changelog alone, as before; the `version-bump` job still
runs, with every step skipped, in the label's default image. It cannot be
skipped as a job: Forgejo decides on the server, before any condition of the
dependant is read, that the dependant of a skipped job is skipped too, which
would take the signing job with it.

### The request is the head of the release branch

The release branch is the `release-branch` input, `main` by default; below it
is `main`. The prepare job commits the changelog on top of the request commit
and pushes `main`, without force, with the bot's key. So the workflow admits a
request only when its commit is the tip of `main`, and checks that before the
key is loaded:

- A request **ahead of** `main` would carry commits to `main` that no review
  admitted. It is refused.
- A request **behind** `main`, because `main` moved after the tag was pushed,
  cannot be pushed as a fast-forward. Without the check the job would fail at
  the push, after rendering and signing, with a Git error. It is refused
  early, with a message that says where the tag and the branch are.
- A request on **another line of history** is refused.

What this means for an operator:

- **Push the request tag at the tip of `main`**, and nothing else.
- **Freeze merges while a release is requested.** From the push of the
  request tag until the prepare job has pushed the release commit, nothing
  else may land on `main`: no merge, and no automerge of a dependency update.
  The window is the prepare job, minutes.
- **A moved branch spends the request.** The request tag is immutable and
  names one commit. If `main` moved first, that request cannot be released:
  cut a new request at the new tip, for the next patch version, or have an
  administrator delete the unused request tag and push it again at the tip.
  A failed run is not re-run into success here; the tag still names the old
  commit.

The rule is not an input. A switch to "any commit of `main`" would only bring
back the late failure, or, when the changelog comes out unchanged, tag an old
commit.

A consumer that tested the request before preparation can enforce its
changelog-only policy with `release validate-tag --expected-parent <request
commit> --changelog-only`, alongside `--release-sha` and `--tag`. It requires
one child commit changing exactly `CHANGELOG.md` and the local
`release-request/<tag>` still naming the tested request. A failed Git read,
an extra changed path, or a moved request is a refusal.

The branch is named, never read from the event or the environment: the
`coordinates` job admits the `release-branch` input as a branch name, and
every engine call that reads a branch is given it explicitly. The engine's
branch flags would otherwise fall back to `RELEASE_BRANCH` or `BRANCH`, so a
variable of either name could move the push target while the head check
stayed. Branch protection must let the bot's key push to that branch, without
force.

## Snapshots

The Forgejo layer has no snapshot flow: there is no counterpart of GitHub's
`release-snapshot-orchestrator.yml`. A branch build is the consumer's own
workflow, for example a push-triggered job with `build-go`.

## Signing

The layer signs with keys the organization holds:

| Object | Key | Secret |
|---|---|---|
| Release commit and final tag | SSH | `RELEASE_SSH_SIGNING_KEY` |
| Checksums, SBOMs, provenance, images and their attestations | Cosign key pair | `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE`, checked against `cosign-public-key` and its digest |
| `.deb`, `.rpm`, `.apk` packages, when `sign-distro-packages` is on | OpenPGP | `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE`, `RELEASE_GPG_FINGERPRINT` |

Keyless signing is not the layer's path: public Fulcio does not trust a
Forgejo issuer (see [forgejo](providers.md#forgejo)), and the workflows have
no KMS input. Whether signatures also go to the public transparency log is the
`transparency-log` input, `none` by default.

### Signing boundary

The runner that runs the sign job needs swap off: `release sign` refuses to
load a long-lived key while swap is active, and the signer container cannot
turn it off ([swap policy](verification.md#swap-policy)). Turn it off in the
runner host's setup, or sign with `sigstore` or `kms`.

These properties are the boundary. A change that leaves the public YAML
untouched can still alter every downstream release's provenance, so the
workflows, the signer tooling, the resolver, the binary and its checksum are
reviewed as one set.

- **Separately privileged jobs.** Signing keys never enter the promote or
  publish job, and the release token never enters the sign or promote job.
  The only hand-off between them is the signed `dist/`, carried with its
  digest.
- **Trust is established again in every job.** Each privileged job creates a
  private workspace, checks the layer out at `forge-ci-sha`, verifies the
  live checkout, downloads the binary that checkout names, and runs the
  toolchain check with `SIGNER_STRICT=1`, before any step that holds a secret
  or runs the engine. The repetition is visible on purpose. Do not collapse it
  into one job, a wrapper action or a bootstrap command.
- **The download is a step of its own.** "Fetch engine binary" holds no
  secret and comes before the toolchain check. With `SIGNER_STRICT=1` the
  resolver never downloads on the way to an engine call, since that call may
  hold a signing key, and the toolchain check refuses a checkout whose binary
  is not there. `test/forgejoguard` holds the order.
- **One executable.** The `forge-ci` binary is the one non-shell executable
  allowed in the signer, and only at the checksum recorded in the checkout. A static
  binary cannot be repurposed to run arbitrary code the way an interpreter
  can. Signing does not move back into shell, and not onto a runner without
  the signer container.
- **Two pins, two subjects.** `forge-ci-sha` identifies the revision;
  `.forgejo/bin/forge-ci.sha256` identifies the executable inside it. They
  are never merged.
- **The request is bound.** Each privileged job re-validates the release tag
  and, through `release-request-sha` from the caller's event context, its
  lineage: the release commit must be the single child of the commit the
  request authorized.
- **The hand-off is unsigned.** After the signer has verified the `dist/`
  digest, its first look at the files is `release files validate --unsigned`,
  which refuses a signature, a bundle or a provenance statement that arrived
  with the build. The signer writes those itself; one from the build would be
  published as the signer's.

**The Cosign trust root is the caller's.** The layer ships no key, and there
is no key file in the helper checkout to fall back to: `cosign-public-key` and
`cosign-public-key-sha256` are required, the step writes them to files in the
job's private workspace, and the engine refuses an empty key, an empty digest
and a key that does not hash to the digest. Half a pair is refused, not
completed.

A wrong key cannot yield a release that the organization's verifiers accept.
The signing workflows only compare: `validate signing-key` passes when the
private key in the job's secrets derives the trusted public key, so a wrong
public key fails the job, and whoever replaces both the key and the secret
signs with a key no verifier holds. The base-image workflows verify with the
key, so there a wrong key would accept bases signed by its private half; the
party able to pass it is the calling workflow, which already names the base
repository.

The digest is what a variable must not weaken. A key read from `vars` can be
changed by a repository or organization administrator without a reviewed
commit. A digest written as a literal in the calling workflow binds that
variable to the protected branch; a digest read from a variable does not, and
is then only an integrity check. The same holds for the signer image, which
runs with the signing keys.

`check-l3-isolation` enforces both. In every workflow file of the consumer, a
job that calls the layer and passes `signer-image` must write out an image
reference pinned by digest, and one that passes `cosign-public-key-sha256`
must write out 64 lowercase hex characters. An expression such as
`${{ vars.SIGNER_IMAGE }}` is refused, and so is a placeholder that was never
filled in. `cosign-public-key` itself may be an expression: the digest binds
it. The gate holds `transparency-log` the same way, to `none` or `public`; see
[The transparency log](#the-transparency-log).

### The transparency log

By default the layer writes **no** entry to the public Sigstore transparency
log, Rekor. Every Cosign signature is made with your own key and is verified
against that key alone. Writing to the public log is the caller's explicit
choice.

An entry in the public log holds the digest of what was signed, the
signature, the public key and a time. It does not hold the file or its name.
It is permanent and cannot be withdrawn. For a project that publishes its
releases that can be wanted: anyone can check that a signature existed at a
time. For an organization that does not want the existence and timing of its
releases in a public log, it is not, and one unintended release cannot be
taken back. That is why the public log is the choice that has to be written
down.

The four signer workflows and the `prune-base-images` action take one input,
`transparency-log`, with the same meaning in each:

| Value | Signing | Verification |
|---|---|---|
| `none` (the default) | Writes no entry and contacts no Sigstore service. | Checks the signature against the key alone. |
| `public` | Writes to Rekor. Needs `rekor.sigstore.dev` and `tuf-repo-cdn.sigstore.dev`; if they cannot be reached the signing step fails. | Requires the log entry. |

Anything else stops the workflow in its `coordinates` job, before a signer
container starts. The validated value is the job environment of every job
that runs in the signer image, as the CLI variable
`FORGE_CI_COSIGN_TRANSPARENCY`, so signing and the verification that
follows it in one release cannot disagree.

To opt in, pass the input wherever you call a signer workflow or the prune
action:

```yaml
    with:
      transparency-log: public
```

**Use one value everywhere.** A signature is verified the way it was made.
Images signed by `sign-container-images.yml` with one value are refused by
`verify-base-container-images.yml` and `promote-base-container-images.yml`
called with the other, and a release signed with `none` does not verify
under `public`. Changing the value later does not change the signatures
already made: those keep needing the verification that matches them.

**The value is a literal.** `check-l3-isolation` refuses a job that calls a
signer workflow and passes `transparency-log` as anything but `none` or
`public`, written out. The input does not decide which key or which signer
image is trusted, and a signature made under either value verifies only
against your key. But read from a variable, an administrator could send the
next release to the public log without a reviewed commit, and that entry
could not be taken back. A job that does not pass the input keeps the
default and is not reported. The gate reads the jobs that call a workflow; the
`transparency-log` input of a `prune-base-images` step is yours to write out.

**Whoever verifies your releases must know which it is.** A signature made
with `none` fails an ordinary `cosign verify`, which requires the log entry.
With the default, verification names the key and waives the log:

```shell
# A release file and its bundle
cosign verify-blob --key cosign.pub --insecure-ignore-tlog \
  --bundle checksums.txt.bundle --new-bundle-format checksums.txt

# A release image, by digest
cosign verify --key cosign.pub --insecure-ignore-tlog \
  forge.example/owner/app@sha256:<digest>
```

Cosign calls the flag insecure and warns on every use. What it gives up is
the public record of when the signature was made; the signature is still
checked against your key. With `public`, leave the flag out.

The CLI's own default is unchanged: outside this layer, an unset
`FORGE_CI_COSIGN_TRANSPARENCY` still means the public log, which keyless
signing needs.

**What the pull-request gate does not check.** `check-l3-isolation` checks the
declared workflow: it rejects mixed pins, a signing secret in the build job,
an inherited signer secret set, a broken digest hand-off, and a signer image,
key digest or transparency-log setting that is not a literal. For secrets it
reads the build job and the workflow-level `env` that job inherits, and looks
for the names in `signing-secrets` alone: another secret in the build job, or a
secret in a job it was not told about, is not looked at. It does not check
runner isolation, branch protection, or the provenance of a completed release.
A direct push bypasses it, so the protected branch is part of the boundary.

The action also checks every call to its own serving repository against the
action or `workflow_call.inputs` declarations in its pinned checkout. Unknown
inputs and missing declarations fail closed. An explicit foreign subject or
a local-path action keeps the generic checks; this checkout cannot supply
another repository's declarations. Standalone `validate isolation`
enables that check with `--contract-root <trusted checkout>` and
`--single-pin-subject`; it reads no network and does not authenticate a
checkout supplied by the caller. Consumer guards need not copy the layer's
input lists, but still own their reviewed pin, trust values and job policy.

## Code scanning (SARIF)

Forgejo ingests no SARIF report
([forgejo#3669](https://codeberg.org/forgejo/forgejo/issues/3669)).
`lint-nanolinter` gates on its exit status and its findings are in the step
log; it writes no SARIF file. `forge-ci security report upload-sarif` on a
Forgejo runner prints a notice and exits 0.

## Forks

### Using the layer from a fork

The layer names no organization. An organization serves it from its own copy
of this repository, under any owner and any repository name, and brings its
own signer image, Cosign key and bot identity. From nothing to a first
release:

1. **Put the repository on your instance.** Forgejo does not fork across
   instances, so create an empty repository and push this history to it.
   - Object format **SHA-1**: this history is SHA-1, and a SHA-256 repository
     cannot receive it.
   - **Readable without credentials** by your runners: public, or readable
     anonymously on an instance that allows it.
   - Protect the branch against force-push and deletion, and protect tags.
     Consumers pin commits, and a pinned commit must stay reachable from a
     branch or a tag: each job fetches the commit directly and falls back to
     the advertised branches and tags.
   - Nothing in it runs on its own. Every file under `.forgejo/workflows/`
     starts only when another workflow calls it, and Forgejo reads that
     directory in place of `.github/workflows/`.
   - Push upstream's commits unchanged if you can. A pushed commit keeps its
     ID, so a commit you authenticated upstream is the same commit in your
     copy.
2. **Choose the commit your consumers pin, and authenticate it.** Select an
   authenticated upstream release or an independently reviewed revision of your
   copy, and record its full commit ID and verification evidence. The layer
   verifies that a clone is the pinned commit; it cannot tell you the commit is
   the right one.
3. **If your copy changes Go code**, record the new build as
   [Development](DEVELOPMENT.md#the-forgejo-layers-engine-binary) describes,
   before you pin: a commit whose checksum is not a build of its tree is
   refused by the binary job of the next step. `just test-forgejo`, with the
   Go release `go.mod` names, rebuilds the CLI and compares it with the
   recorded checksum, so you can check a commit yourself.
4. **Publish the binary and build your signer image.** One workflow does
   both, in that order. Copy
   [`examples/forgejo/signer-image.yml`](../examples/forgejo/signer-image.yml)
   into one repository of your organization, set its two placeholders to your
   copy and the commit of step 2, and run it. It needs the `CI_RUNNER_LABEL`
   variable and two credentials: `RELEASE_REGISTRY_TOKEN` writes the engine
   owner's generic package on this forge; `SIGNER_REGISTRY_TOKEN`, with
   `SIGNER_REGISTRY_USER`, authenticates to the signer image registry.
   To host the signer on another registry, set the workflow's literal
   `SIGNER_IMAGE_REPOSITORY` to its full OCI repository without a tag or digest.
   Both architecture builds and assembly use that destination; source labels
   still identify the authenticated engine checkout.
   - Its first job builds the CLI of the pinned commit from modules it
     downloads itself, stops unless the build is the one the commit's
     `.forgejo/bin/forge-ci.sha256` names, and uploads it to the generic
     packages of the owner of your copy. Until that has run, every job of the
     layer at that commit fails in its first step that needs the CLI, with the
     address it asked for.
   - The owner's packages must be **readable without credentials**, as the
     repository is. The job ends by downloading the binary the way a consumer
     job does, so a private owner fails here and not in a release.
   - The other jobs build the signer image. Write down the digest-pinned
     reference they print. See [The signer image](#the-signer-image).
   - **Run it again for every commit you move the pin to.** A commit that
     changes no Go code names the same binary, and the upload then finds it
     there.
5. **Create a Cosign key pair.** `cosign generate-key-pair` writes
   `cosign.key` and `cosign.pub`. The private key and its passphrase become
   the secrets `RELEASE_COSIGN_PRIVATE_KEY` and `RELEASE_COSIGN_PASSPHRASE`.
   Keep `cosign.pub` where your verifiers find it, and record
   `sha256sum cosign.pub`: that digest is written into every consumer
   workflow, and the PEM text may be the organization variable
   `COSIGN_PUBLIC_KEY`. The text must be the file exactly, final newline
   included, or it does not hash to the digest.
6. **Create the release bot.** An account with an SSH key that may push to
   each consumer's `main` and push tags. The private key is the
   `RELEASE_SSH_SIGNING_KEY` secret; its name and e-mail are the `bot-name`
   and `bot-email` inputs, which have no default.
7. **Decide about the transparency log** before the first signature. The
   default writes nothing to the public log; see
   [The transparency log](#the-transparency-log).
8. **Set up each consumer** as in [Quick start](#quick-start): the two
   examples of its ecosystem with `OWNER` and the pin of step 2, the image
   reference of step 4 and the digest of step 5 as literals, the
   [variables](#variables) and the [secrets](#secrets-and-tokens) by the names
   listed there, and a `CI_JOB_IMAGE` for its pull-request jobs.

`check-l3-isolation` follows the fork without configuration: its pin subject
is the repository the runner fetched the action from. So does the binary: an
action downloads it from the owner of the repository the runner fetched the
action from, and a workflow from the owner of the repository it cloned.

The GitHub and GitLab lint workflows select Nanolinter images from
`ghcr.io/itiquette/nanolinter`. Verify the chosen image is published and readable
and pass its digest through `nanolinter-image`; see
[lint images](components.md#lint-nanolinteryml-default) for migration status.

What a copy cannot do yet, such as a non-amd64 runner, is listed under
[Limits](#limits).

### Try your repository against your copy, on a lab

Before you move your consumers' pin to a commit of your copy, you can run a
consumer against that commit without publishing either. From a checkout of
your copy at the commit, with a disposable lab that meets
[the live tier's contract](testing.md#running-it):

```text
just test-live-consumer /path/to/consumer pull-request CI_JOB_IMAGE=<image on the lab>
just test-live-consumer /path/to/consumer release
```

The command pushes HEAD of the consumer checkout to a scratch repository on
the lab, with every reference to the layer it pins replaced by the layer of
the checkout you run it from, sets the variables and throwaway keys a release
needs, and opens a pull request or pushes a release request. It reports the
run of the consumer's own workflow, job by job, and for a release verifies
the signed commit, the signed tag and the Cosign bundles from your machine.
The consumer checkout is only read. [Trying a consumer](testing.md#trying-a-consumer)
lists what is rewritten, what is set, and what you bring.

It shows that the consumer's workflows and the layer agree at that commit, on
that lab's runner. It does not show that your instance's runner, registry or
network behave as the lab's do.

### Release readiness

For 0.90.0, validate the chosen instance and runner with the live tier after
publishing the selected engine package and signer image. Cover checksum-bound
bootstrap, artifact handoff, missing-secret refusal, signed release preparation,
registry signing/promotion and the selected consumer workflow. Add base-image
verification/promotion and retention checks when those paths are used.

The local `test/forgejoguard` and `test/forgejobehavior` suites exercise the
layer contracts and shell against the built CLI. Real-runner acceptance also
checks workflow expansion, variable resolution and scheduling on the chosen
instance. Record the actual revision and selected scenarios with each result.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| A job's checkout of the layer fails with `could not read Username` | The serving repository needs credentials. It must be readable without them; see [Requirements](#requirements). |
| The first step that needs the CLI fails and prints an address | The binary for the pinned commit is not published. Run the [signer-image workflow](../examples/forgejo/signer-image.yml) at that commit. |
| The `coordinates` job fails | No `forge-ci-repository` input or `FORGE_CI_REPOSITORY` variable, a missing or tag-only `signer-image`, credentials or a query in a URL, or a `transparency-log` other than `none` or `public`. |
| `prepare-release.yml` fails before checkout | A secret the release needs is not configured, or the signer image cannot be read anonymously; pass `skip-signer-image-check: 'true'` for a private image. |
| The request is refused as ahead of or behind the release branch | See [The request is the head of the release branch](#the-request-is-the-head-of-the-release-branch). |
| GoReleaser stops with `git is in a dirty state` | `release-notes.md` is not in `.gitignore`. |
| `lint-nanolinter` stops: no `nanolinter` | `CI_JOB_IMAGE` does not carry nanolinter. |
| `check-l3-isolation` fails a pull request | Mixed pins, a signing secret in the build job, or a signer image, key digest or transparency-log setting that is not a literal. |
| A signing job cannot start its container, after the final tag exists | The signer image reference names no image. Publish the image it names and re-run the failed jobs. |
| A package publish answers 401 or 403 | The automatic token cannot write packages; supply one with `write:package`. See [forgejo](providers.md#forgejo). |
| Plain `cosign verify` fails on a release | Signatures made with `transparency-log: none` need `--insecure-ignore-tlog`; see [The transparency log](#the-transparency-log). |

`forge-ci doctor` in a job prints what the detected forge supports.

## Limits

Not supported yet, and known:

- A runner that is not linux/amd64. The binary is published for that
  platform alone; its file name carries the architecture, and nothing is
  promised about another one.
- A serving repository that needs credentials to read, or one whose owner's
  packages do. The download of the binary sends no token, and has no input
  for one. The lab has only shown the download for a public owner.
- A certificate authority the job images do not already trust.
- A pull-request job image from this repository: `CI_JOB_IMAGE` must carry
  nanolinter, its check tools and your toolchain, and nothing here builds one
  for a Forgejo instance.
- An authenticated release of the layer to pin.
- Rebuilding everything from a copy: `go.mod` replaces one module with a path
  outside the repository, so `go test ./...` and `go mod download` run only
  where that path exists. Building the CLI, publishing its binary and running
  `just test-forgejo` do not need it.

Not in the layer:

- A snapshot flow, a MegaLinter action, and SARIF ingestion.
- Builds other than Go, Cargo and Maven: no Gradle, npm or Xcode action.
- Publication to the forge's package registry.
- Committed version bumps for project files other than Maven's POMs.
- Plan-driven fan-out from `artifacts.yml`: the consumer's workflow is the plan.
- Keyless or KMS signing in the workflows.
