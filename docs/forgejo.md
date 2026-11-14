# Forgejo

How `forge-ci` is used on Forgejo, hosted or self-hosted: the reusable
workflows and composite actions under `.forgejo/` that check a pull request,
build, and prepare, sign and publish a release. A job that calls the binary
itself installs it as [Installing the binary in a
job](#installing-the-binary-in-a-job) says; how the CLI recognizes a Forgejo
runner, and what it does differently there, is in
[Providers and runners](providers.md#forgejo).

Pin a reviewed commit of the layer, and serve that commit, its engine binary
and the signer image on your Forgejo instance: the upstream project publishes
neither the binary package nor the signer image for your instance. `init`
reads its examples and defaults from a release tag of the serving repository.

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
| `.forgejo/workflows/` | Reusable workflows; see [Reusable workflows](#reusable-workflows). |
| `.forgejo/actions/` | Composite actions; see [Composite actions](#composite-actions). |
| `.engine/bin/` | The resolver `lib-forge-ci.sh` and the engine binary checksums, shared with the GitLab layer. The binary is not in the tree: a job downloads it by its checksum. See [The binary in the layer](#the-binary-in-the-layer). |
| `.forgejo/scripts/verify-signer-toolchain.sh` | The check every privileged job runs first, and the signer image runs at build time. |
| `containers/signer/Containerfile` | The definition of the signer image. No image is published; see [The signer image](#the-signer-image). |
| `examples/<ecosystem>/forgejo/` | A pull-request and a release workflow to start from, for [Go CLI](../examples/go-cli/forgejo/), [Go service](../examples/go-service/forgejo/), [Cargo](../examples/cargo-app/forgejo/) and [Maven](../examples/maven-app/forgejo/). |
| `examples/forgejo/` | Organization publication callers, including runtime images and package-retention preview. |

Lint and build ship as actions, not as workflows: the pull-request jobs and
the release build job stay the consumer's, and an action is one step in them.
[Build and checks](#build-and-checks) says why;
[Delivery layers side by side](providers.md#delivery-layers-side-by-side)
compares the layer with the [GitHub](github.md) and [GitLab](gitlab.md) ones.

The layer is flat on purpose. Forgejo resolves a relative `uses:` inside a
called workflow against the **calling** repository, so a workflow of this layer
never calls another one: composition is the consumer's workflow, or a composite
action.

## Requirements

**Owner prerequisites (an organization or a user):** host the reviewed layer
and its published binary, provide fresh job containers on a trusted runner,
and publish the reviewed job and signer images with their trusted
signing-key information; see [Under a user account](#under-a-user-account).

**Per repository:** fill the example's pins and project values, select the
runner and job image, configure release secrets, and protect branches and
release-request tags. Get the pull-request checks green before requesting a
release; follow its workflow run to the final release, or use
[Failed Release Recovery](failed-release-recovery.md) on failure.

| Requirement | Value |
|---|---|
| Forgejo | v16 or later (reusable-workflow job expansion) |
| Runner | one reviewed, trusted runner, selected by the `CI_RUNNER_LABEL` variable; it must run job containers for the signer image. The layer assumes each job gets a fresh container and filesystem: on a runner that keeps state between jobs, a pull-request job can leave something that a later privileged job runs. |
| Runner architecture | **linux/amd64 or linux/arm64.** The binary every workflow and action runs is built and published for both, and each job runs the one of its runner's architecture natively. A runner of another architecture is refused. Images and binaries the jobs produce can be for other platforms. |
| Runner job image | Bash, Git, curl and `sha256sum` for every job; Node as well for a job that uses `setup-toolchain`, whose cache and Go set-up steps are JavaScript actions. The signer jobs run in the signer image, which brings its own tools. |
| Instance | this repository, or a fork of it, hosted on the **same instance** as the consumer: a reusable workflow is called as `owner/repository/path@ref` |
| Serving repository | **Readable without credentials** by the runners, and so are the packages of its owner. Every job clones the layer over HTTPS with no token, so a repository that needs a sign-in fails the clone (`could not read Username`), and downloads the layer's binary from that owner's generic packages the same way. |
| The layer's binary | **Published on the instance for the pinned commit**, by the serving repository's owner, before a job runs; see [The binary in the layer](#the-binary-in-the-layer). |
| Transport | HTTPS between the runner and the instance; the helper checkout refuses anything else. A private certificate authority must be trusted by the runner's job images and by the signer image; the layer has no input for a CA bundle. |
| Release branch | `main` unless `prepare-release.yml` is given another `release-branch`; see [The request is the head of the release branch](#the-request-is-the-head-of-the-release-branch). |

### Under a user account

The owner of the layer and of its consumers can be a Forgejo user account
as well as an organization. The setup maps directly:

- The layer is called as
  `uses: <user>/<repository>/.forgejo/workflows/<workflow>@<sha>`.
- Variables and secrets go on each consumer repository, or on the user's own
  Actions variables and secrets.
- Runners register at user or instance level.
- The layer's binary, the signer image and the release packages live under
  the user.

The rule that the serving repository and its owner's packages are readable
without credentials still applies. Forgejo lets an anonymous client read a
user's packages only when the user's visibility is public; a limited or
private user's packages need a sign-in, so such a user cannot serve the
layer. A layer under a user therefore needs a public user, on an instance
that does not require a sign-in to view. A public user's packages are
readable by anyone, those linked to its private repositories too, so a
consumer that wants private packages belongs under another owner, one that
is private.

A user account leaves residual risk that an organization can reduce:

- One person controls the layer, the pins and the signing keys, so there is
  no separation of duties.
- With no teams, required approvals must name collaborators.
- Only the user can write the user's packages, so the package and registry
  tokens are that person's personal tokens.

Use an organization for production, and a user account for personal or
small projects. The Forgejo live rows run with a user account as owner; see
[Live runs](live-runs.md).

### Authorization and job tokens

Forgejo ignores GitHub's `permissions:` declarations, so the layer and its
examples leave them out. The automatic job token (`forgejo.token`) is scoped
to the calling repository, but scoped does not mean read-only: in Forgejo 16 and 17
it can write to the repository in every run except a fork's pull request.
Build jobs hold no signing key, but they do hold that token; a workload that
needs a hard read-only boundary needs isolation from write credentials that
the workflow file cannot give. Issue explicit release and registry tokens
with the narrowest scopes, such as `read:repository`, `write:repository` and
`write:package`, and pass them only to the steps that need them. The layer's
workflows request no OIDC token.

See Forgejo's [workflow differences](https://forgejo.org/docs/v17.0/user/actions/github-actions/)
and [access-token scopes](https://forgejo.org/docs/v17.0/user/authentication/token-scope/).

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

A job that uses `setup-buildah` or `setup-toolchain` installs packages from
the Debian or Ubuntu archive its job image names. When the image has CA
certificates, the official archive hosts (`deb.debian.org`,
`security.debian.org`, `archive.ubuntu.com`, `security.ubuntu.com`,
`ports.ubuntu.com`) are reached over HTTPS, so egress that allows only TLS is
enough; other hosts are used as the image's sources name them.

An image build's `RUN` steps share the job's network and its
`/etc/resolv.conf`, so they reach and resolve what the job does, a service on
the job's network included.

## Getting started

### Calling it

Reusable workflows are called by path on the same instance, composite actions
by full URL. Both forms carry the same commit:

```yaml
jobs:
  prepare:
    uses: OWNER/forge-ci/.forgejo/workflows/prepare-release.yml@PIN_FORGE_CI_SHA
    with:
      forge-ci-repository: OWNER/forge-ci
      forge-ci-sha: PIN_FORGE_CI_SHA
      # ...

  build:
    steps:
      - uses: https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/setup-forge-ci@PIN_FORGE_CI_SHA
```

A called workflow runs in the caller's workspace, which holds none of this
repository's files, so each job clones the layer at `forge-ci-sha`, checks
that the checkout is that commit, and runs the actions and the binary from
it. The commit of the `uses:` line and `forge-ci-sha` must be the same
commit, written out; `check-l3-isolation` refuses a pull request where they
are not.

`forge-ci-repository` is the `owner/repository` of the `uses:` line, where
each job clones the layer from. Without it a workflow reads the
`FORGE_CI_REPOSITORY` variable of the calling repository or organization, and
with neither its first job stops. Prefer the input: it is reviewed code
beside the `uses:` line, and a variable can change without a commit.

### Quick start

`forge-ci init <mirror URL> --dry-run` previews the files, the placeholders
left and every conflict with an existing file, without writing anything.

Then run `forge-ci setup status`: it lists the steps from here to a first
release, marks the ones the repository shows are done, and names the command
for each. In a repository with only Forgejo workflows, `forge-ci setup signer`
writes `.forgejo/release-allowed-signers`.

The organization's mirror can make `forge-ci init <mirror URL>` write a
consumer's files with every pin filled in. The operator commits
`.forge-ci/consumer-defaults.yml` to the mirror; init reads it at the release
tag it pins, never from a branch, and refuses unknown keys:

```yaml
forge: forgejo                       # required outside github.com
signer-image: <host>/<owner>/forge-ci-signer@sha256:<digest>
cosign-public-key-sha256: <sha256 of the public key>
```

A key left out leaves its placeholder, and init lists it. Consumers then run
`forge-ci init https://<host>/<owner>/forge-ci` in their repository; by hand,
the steps are these.

1. **Copy the two examples** of your ecosystem, `pullrequest.yml` and
   `release.yml` from `examples/<ecosystem>/forgejo/` (see the
   [examples index](../examples/README.md)), at the commit you will pin, into
   a draft directory outside `.forgejo/workflows/`. Optionally copy the example's
   `nanolinter.toml` if you want custom lint policy: the pull-request workflow's lint
   job runs `nanolinter verify` with normal configuration discovery and requires at least the
   `required-lints` floor, default `secrets,sast`.
2. **Replace the placeholders**: `FORGE_HOST`, `OWNER`, `PIN_FORGE_CI_SHA`,
   `PIN_SIGNER_IMAGE_DIGEST` and `PIN_COSIGN_PUBLIC_KEY_SHA256`. The header of
   `release.yml` says what each one is. The tokens are the same in both files,
   so one command fills them:

   ```bash
   sed -i \
     -e 's|FORGE_HOST|forge.example|g' \
     -e 's|OWNER|myorg|g' \
     -e 's|PIN_FORGE_CI_SHA|<commit>|g' \
     -e 's|PIN_SIGNER_IMAGE_DIGEST|<digest>|g' \
     -e 's|PIN_COSIGN_PUBLIC_KEY_SHA256|<sha256>|g' \
     pullrequest.yml release.yml
   ```

3. **Fill in every `FIXME`**: the bot identity and the name of your binary in
   `release.yml`, and in the Go service example the image title. For an
   ecosystem without an example, keep the hand-off the build job ends with.
4. **Decide on request signatures.** The example sets
   `verify-release-request-signature: 'false'`. A production consumer reviews
   and commits `release-request-allowed-signers`, then enables the check: an
   annotated request tag does not authenticate its author, and tag protection
   alone leaves one control between a policy error and a signed release.
   `forge-ci setup signer` writes the allowlist, and `forge-ci doctor` warns
   while the check is still off.
5. **Set the [variables](#variables) and [secrets](#secrets-and-tokens)**, among them
   `CI_JOB_IMAGE`, the image the pull-request jobs run in.
6. **Add the two files** as `.forgejo/workflows/pullrequest.yml` and
   `.forgejo/workflows/release.yml` through your protected pull-request path,
   with the changelog configuration the release reads: `keepachangelog.toml`
   and `minimal.toml` of [`templates/gitcliff/`](../templates/gitcliff/) as
   `.forge-ci/gitcliff/`. Do not activate an incomplete template.
7. **Release** by pushing an immutable `release-request/vX.Y.Z` tag. The flow
   derives the final `vX.Y.Z` tag and creates it once. Never create, move or
   force-push the final tag yourself.

If a release fails or is cancelled partway, do not delete the tag, the
release or a published image. Once the prepare job has pushed `vX.Y.Z`, a new
request for that version is refused: re-run the failed jobs of that run, or
release the next patch version from a new commit. On Forgejo 17 or later,
`POST /api/v1/repos/{owner}/{repo}/actions/jobs/{job}/rerun` on the first
cancelled job reruns it and the jobs that need it in the same run, for a run
cancelled between the unsigned hand-off and signing. That is held by
`TestForgejoRelease_RerunAfterInterruption` (Forgejo 17 or later; not yet
live-proven, see [Testing matrices](testing-matrices.md)); a re-run from the
web interface is held by no test. If
the prepare job itself failed after pushing the tag, only the next patch
version recovers. See [Failed Release Recovery](failed-release-recovery.md).

## Inputs and variables

### Reusable workflows

Forgejo rejects declared `workflow_call.secrets`, so each workflow names the
secrets it reads in a `# workflow-call-secrets-contract:` header comment, and
the caller passes them by name. Never use `secrets: inherit`.

| Workflow | Does | Secrets |
|---|---|---|
| `prepare-release.yml` | Checks that every secret the release will need is configured, before its own checkout; guards the request tag; requires the request at the head of the release branch; renders the changelog; bumps the version files of the projects `artifacts.yml` plans, in one key-free job per toolchain family, and verifies that bump; makes the signed release commit and the final tag once. Outputs `release-tag`, `release-sha` and the `artifacts-config` it planned from. | `RELEASE_SSH_SIGNING_KEY`; presence only of the others; none in the version-plan and version jobs |
| `sign-and-publish-release.yml` | Five separately privileged jobs after a secret-free preflight: `sign` (checksums, SBOMs, provenance, images, distro packages), `promote` (final image tags), `publish` (the Forgejo release), `packages` (distro packages to the owner's registries; it starts only when they are declared), `cleanup` (candidate tags). | Cosign, GPG, `RELEASE_TOKEN`, `RELEASE_REGISTRY_TOKEN`, each only in the job that needs it |
| `base-images.yml` | Builds one stage of the caller's own graph of base images, a root or the children of one authenticated parent, one child or all its siblings: one native job per image and architecture without a secret, then one job that scans every layout and stages the unsigned candidates. See [Base images](#base-images). | `RELEASE_REGISTRY_TOKEN`, in the parent fetch and publish jobs only |
| `sign-container-images.yml` | Signs and attests a base-image ledger in one engine call. | Cosign, `RELEASE_REGISTRY_TOKEN` |
| `verify-base-container-images.yml` | Verifies existing base images against the trusted key; optional explicit registry authentication supports private bases. Only missing images (or anonymous access refusal) are cache misses. With the token, `parent-bases-json` names every found base as a parent. With `expected-ref` it authenticates exactly one published base as a parent instead, and a missing base is a failure. | optional `RELEASE_REGISTRY_TOKEN`, `registry-user` for external registries; required with `expected-ref` |
| `promote-base-container-images.yml` | Verifies base-image signatures, then moves the final tags. | `RELEASE_REGISTRY_TOKEN` |
| `release-images.yml` | Builds the release images `artifacts.yml` declares, one native job per container and architecture, with the organization's image builder; scans each, publishes it under its staging tag and records it for the signer. See [Release images](#release-images). | `RELEASE_REGISTRY_TOKEN`, in the publish job only |
| `snapshot-images.yml` | Builds and publishes the unsigned snapshot images of a branch. See [Snapshots](#snapshots). | `SNAPSHOT_REGISTRY_PASSWORD` |
| `package-retention.yml` | Previews which old package versions a retention rule would delete; it never deletes. See [GHCR package retention previews](#ghcr-package-retention-previews). | `RELEASE_REGISTRY_TOKEN` |
| `runtime-images.yml` | Publishes the runtime images from a mirror. See [Forgejo runtime publication](runtime-images.md#forgejo-runtime-publication). | `RELEASE_REGISTRY_TOKEN`, Cosign, optional `CI_RUNNER_LENDER_TOKEN` |

Each workflow's `inputs:` block is its reference: every input carries a
description. The inputs they share:

| Input | Required | Meaning |
|---|---|---|
| `runs-on` | yes | JSON array of runner labels, e.g. `["ci-builder"]`. A job requires all labels. A workflow with image builds or a signing job also takes `build-runs-on-linux-amd64`, `build-runs-on-linux-arm64` or `signer-runs-on`; see [runners](runners.md#runner-roles-across-the-forges). |
| `forge-ci-sha` | yes | The commit to check the layer's files out at: the same commit as the `uses:` pin. |
| `forge-ci-repository` | yes, unless the variable is set | The repository that serves the layer, as `owner/repository` on the instance or as an HTTPS Git URL: the one in the `uses:` line. Empty takes the `FORGE_CI_REPOSITORY` variable. With neither, the `coordinates` job fails. |
| `signer-image` | yes, in prepare and the four signer workflows | Your organization's signer image, pinned by digest; pass the same literal to preparation and signing. |
| `skip-signer-image-check` | no, in prepare | `'false'` by default. Set explicitly to `'true'` for a private signer image whose anonymous availability cannot be checked. |
| `cosign-public-key`, `cosign-public-key-sha256` | yes, in the four signer workflows | Your organization's Cosign public key, and its SHA-256 as `sha256sum cosign.pub` prints it. |
| `release-branch` | no, in prepare | The branch the release lands on, `main` by default. See [The request is the head of the release branch](#the-request-is-the-head-of-the-release-branch). |
| `artifacts-config` | no, in prepare; from prepare in sign/publish | The `artifacts.yml` whose release plan decides the version bump, `.forge-ci/artifacts.yml` by default. Give it to `prepare-release.yml`; pass `sign-and-publish-release.yml` the prepare job's `artifacts-config` output, as `release-tag` and `release-sha`, which `check-l3-isolation` requires. See [The prepare job](#the-prepare-job). |
| `runtime-image-java`, `-node`, `-rust` | no, in prepare | Digest-pinned images the plan's version job of each family runs in; a planned family without one is refused at `Prepare: version plan`. |
| `release-policy` | no, in prepare and sign/publish | `stable` (default) or `stable-prerelease`; pass the same value to both workflows. See [release policy](workflows.md#stable-and-prerelease-policy). |
| `transparency-log` | no, in the four signer workflows | `none` (the default) or `public`: whether signatures are also written to the public Sigstore transparency log. One value for every workflow of an organization; see [The transparency log](#the-transparency-log). |
| `release-asset-retention` | no, in `sign-and-publish-release.yml` | How many of the newest releases keep every file; older ones keep their checksums and signatures. Empty, the default, deletes nothing. See [Old releases' files](#old-releases-files). |
| `enforce-vulnerabilities`, `enforce-application-tests` | no, in `release-images.yml`, `base-images.yml` and `runtime-images.yml` (vulnerabilities) and `snapshot-images.yml` (both) | [Check enforcement](admin-policy.md), default `false`; checks still run. Write mandated values as literals in a protected, code-owner-reviewed workflow; no workflow reads them from a variable. |

A secret-free `coordinates` job validates the repository and the image
coordinate before any container starts: no credentials, query or fragment in
the URL, and an image pinned by `sha256` digest.

### Composite actions

Every action reaches the binary through the resolver, never through `PATH`
alone. Each `action.yml` documents its own inputs and outputs.

| Action | Does |
|---|---|
| `setup-forge-ci` | Downloads the layer's binary, verifies it against the recorded checksum and puts it on the job `PATH`. The first step of a consumer job that calls `forge-ci` itself. |
| `check-l3-isolation` | The pull-request gate over the consumer's release workflow: no signing secret in the build job, enumerated signer secrets, a prepare job that calls `prepare-release.yml`, its `release-tag`, `release-sha` and `artifacts-config` passed to the signer as bare `needs` expressions, the digest hand-off, one commit across every reference, and the signer image, the key digest and the transparency-log setting written out as literals. |
| `checkout-consumer` | Checks the consumer out without Node and without persisted credentials. |
| `setup-toolchain` | Installs the consumer's pinned tools with mise, verified and cache-aware. |
| `lint-nanolinter` | The pull-request lint gate: the repository's nanolinter plan with a mandated floor. |
| `build-go`, `build-cargo` | The build step of the consumer's job: tests, Build SBOM and one binary per platform. |
| `build-goreleaser` | The build step of a GoReleaser release: packages the prepared release unsigned and imports its manifest. See [Unsigned GoReleaser handoff](#unsigned-goreleaser-handoff). |
| `build-maven` | The build step of a Maven consumer's job: tests, the jar and the Build SBOM, as GitHub's `build-maven` workflow builds them, after checking that the POM declares the release version. |
| `guard-release-tag`, `render-changelog`, `prepare-release` | The steps `prepare-release.yml` is made of, with rendering and signing in separate jobs. Call the workflow; see [The prepare job](#the-prepare-job). |
| `upload-dist`, `assemble-release-dist` | Hand `dist/` to the signer with its digest, from one producer or several. |
| `registry-login`, `setup-buildah`, `record-release-image` | Registry login, Buildah preparation and ledger recording for a custom image job that builds with `forge-ci container build-push-oci-image`, Buildah only. That command scans as it publishes, so it needs `--scan-output-template` and Trivy on the runner. |
| `setup-image-builder` | Prepares Buildah, or checks the kaniko job image, before `multiarch-image build-arch` or `container build`; the job's `container:` is `CI_IMAGE_BUILDER_JOB_IMAGE`, empty for Buildah. |
| `prune-base-images` | Base-image maintenance on a schedule: deletes promoted base images that no supported release image's attestation names. A dry run unless told otherwise. |
| `verify-forgejo-token` | Checks a release token before it is needed. |

The `lint-nanolinter` action takes `enforce-lint` and `enforce-vulnerabilities`,
and `build-go`, `build-cargo` and `build-maven` take `enforce-application-tests`:
the administrator's [check enforcement](admin-policy.md), default `'false'`,
passed to the engine as flags. They are the administrator's only in a
protected, code-owner-reviewed workflow file, written out as literals, which
`check-l3-isolation` requires; the actions never read them from a variable or
the environment.

`registry-login` and `release-images.yml` name the registry user by one rule:
the user given (`username`, `registry-user` or the `RELEASE_REGISTRY_USER`
variable), else the repository's owner on this forge's own registry, whom its
tokens log in as. Another registry, such as GHCR, has users of its own: name
the user there, or the login stops before it sends a token.

For release images, prefer `release-images.yml`, which builds each
architecture natively and hands its checked ledger to the signer.

### Variables

Set these on the consumer repository or its owner, an organization or a
user. An operator supplies each value through a trusted channel; a release
job derives none of them, except the default push URL when `RELEASE_GIT_URL`
is unset.

| Variable | Value |
|---|---|
| `CI_RUNNER_LABEL`, `CI_RUNNER_LABEL_ARM64` | JSON array of trusted runner labels, e.g. `["ci-builder"]` or `["linux","amd64"]`, of the amd64 and the arm64 pool. All labels must match. Plain and signing jobs run on `CI_RUNNER_LABEL`; native arm64 jobs, such as the signer image's arm64 build, on `CI_RUNNER_LABEL_ARM64`. |
| `CI_RUNNER_LABEL_CONTAINER`, `CI_RUNNER_LABEL_CONTAINER_ARM64` | Optional, the same format: where jobs that build container images run instead of the pool, because they need user namespaces a shared pool may deny; see [Runners and runner lending](runners.md). |
| `CI_RUNNER_LENDER`, `CI_RUNNER_LENDER_REPOSITORY` | Optional: the lender a request-runner job asks for an ephemeral runner, with the secret `CI_RUNNER_LENDER_TOKEN`; unset, nothing is borrowed. See [Runner lending](runners.md#runner-lending). |
| `CI_IMAGE_BUILDER`, `CI_IMAGE_BUILDER_JOB_IMAGE` | The image builder type, `buildah` when unset, or `kaniko` to build on a pool that denies user namespaces, with the digest-pinned image the build jobs then run in. Set both once on the organization: the job image changes only when kaniko or its Containerfile does, not with the engine pin. See [Building on the pool with kaniko](runners.md#building-on-the-pool-with-kaniko). |
| `CI_JOB_IMAGE` | The image the pull-request lint and build jobs run in, pinned by digest: a nanolinter image of your ecosystem's flavour, or your own image with nanolinter, its check tools and your toolchain. |
| `RELEASE_GIT_URL` | Optional. The exact write URL, as `ssh://git@host[:port]/owner/repository.git`; unset, the release pushes to `ssh://git@<web host>/<owner>/<repository>.git`, port 22. Set it where SSH runs on another host or port. |
| `RELEASE_GIT_HOST_KEY_FINGERPRINT` | That endpoint's ed25519 host-key fingerprint alone, `SHA256:<base64>` as `ssh-keygen -l` prints it, without host or key type. A fingerprint fetched from the host it is meant to verify verifies nothing. |
| `COSIGN_PUBLIC_KEY` | The PEM text of your Cosign public key, if you pass it from a variable as the example does. |
| `FORGE_CI_REPOSITORY` | `owner/repository` of the repository that serves the layer. Only read when a call does not pass `forge-ci-repository`, which the examples do. |

The runner variables are JSON arrays stored as text, all of whose labels one
runner must match. A job decodes one with
`runs-on: ${{ fromJSON(vars.CI_RUNNER_LABEL) }}`; a reusable-workflow caller
passes the JSON text as `runs-on` and the workflow decodes it. The signer-image
example builds each architecture on `CI_RUNNER_LABEL_CONTAINER` or
`CI_RUNNER_LABEL_CONTAINER_ARM64` when set, else on `CI_RUNNER_LABEL` or
`CI_RUNNER_LABEL_ARM64`. [Runner roles across the forges](runners.md#runner-roles-across-the-forges)
lists each role's input on all three forges.

## Secrets and tokens

[Set up release signing secrets](quickstart-setup-secrets.md) says how to
create the keys.

Every secret the layer reads is named `RELEASE_<what>`. The caller passes each
one by name; the workflows hand it to the CLI under the CLI's own variable
names, which are not these.

| Secret | Role | Used for | GitHub layer |
|---|---|---|---|
| `RELEASE_SSH_SIGNING_KEY` | SSH private key of the release bot | The signed changelog commit and the final release tag. | same name |
| `RELEASE_TOKEN` | Forge API token | Creating the release and uploading its assets. | same name |
| `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE` | OpenPGP signing key and its passphrase | `.sig` sidecars of `.deb`, `.rpm` and `.apk` packages, when `sign-distro-packages` is on, and `.asc` sidecars of the checksums and SBOMs, when `artifacts.yml` says `sign.method: gpg`. | same names |
| `RELEASE_GPG_FINGERPRINT` | Fingerprint the OpenPGP key must have | The same package and release file signatures: a key with another fingerprint is refused. | no such secret |
| `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE` | Cosign private key and its passphrase | Provenance, evidence and image signatures and attestations, and checksum and SBOM signatures unless `artifacts.yml` says `sign.method: gpg`. | same names, for `sign.key: env://COSIGN_KEY` |
| `RELEASE_REGISTRY_TOKEN` | Token that may write packages of the owner, or, for images on another registry, a token for that registry | Signing, promoting and cleaning up container images, and, in the signer-image workflow, pushing the signer image and uploading the layer's binary; and publishing `.deb`, `.rpm` and `.apk` packages to the owner's registries, when `artifacts.yml` declares `distro-packages`. A release with neither an image ledger nor that block does not need it. | no such secret |

Every release needs `RELEASE_SSH_SIGNING_KEY`, `RELEASE_TOKEN`,
`RELEASE_COSIGN_PRIVATE_KEY` and `RELEASE_COSIGN_PASSPHRASE`, and the prepare
workflow checks them before its own checkout. The `RELEASE_GPG_*` secrets are
needed only with `sign-distro-packages: 'true'` or `sign.method: gpg`, and
`RELEASE_REGISTRY_TOKEN` only when the release publishes images or declares
`distro-packages`; the prepare workflow checks those before the release
commit. The examples pass all eight, configured or not: an empty secret is
named only when an enabled feature needs it.

A pull-request job receives no secret and no rate-limit token. It installs
nothing and uses the tools the runner already has.

### The dependency token of your own jobs

Your build jobs install your mise config's tools with `setup-toolchain`, and
tools from GitHub releases go through the GitHub API, which rate-limits
anonymous downloads. Pass a read-only GitHub token as `mise-github-token`,
named `DEPENDENCY_GITHUB_TOKEN` as in the examples:

| Secret | Role | Used for |
|---|---|---|
| `DEPENDENCY_GITHUB_TOKEN` | A GitHub token with no scopes and no repository access (a fine-grained token on public repositories, read-only) | Downloading your tools from GitHub releases within the authenticated rate limit. |

Set it once as an organization secret: every repository's build jobs then
pass the same name, and no write or delete scope ever reaches a job. The
layer's own workflows read no `DEPENDENCY_*` secret. Without the token,
`mise-locked: 'true'` installs from a committed `mise.lock`, which must
record a download URL for every tool on every runner platform. mise records
none for tools from a `forgejo:` backend, so a config that uses one cannot
install locked: pass the GitHub token instead, and for those tools
`mise-forgejo-token` (read-only) where their Forgejo rate-limits anonymous
downloads.

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
| `prepare-release.yml`, `Prepare: version <family>` jobs | `runtime-image-java`, `-node`, `-rust`, digest-pinned | Bash, Git, curl, `sha256sum` and the family's version tool: a JDK and Maven, Node and npm, or Cargo. |
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
digest-pinned reference. Each architecture builds on a native runner of its
own architecture (`CI_RUNNER_LABEL` for amd64, `CI_RUNNER_LABEL_ARM64` for
arm64), with that architecture's engine binary. The layer sets up no
emulation, and the engine refuses to build an architecture other than its
host's.

The image's OCI source and revision labels name the engine checkout it was
built from. Consumers pin the digest it prints, not its descriptive tags.

By hand, for one architecture:

```bash
buildah build --file containers/signer/Containerfile \
  --tag <registry>/<owner>/<name> .
```

The build context is the repository root, cut down to the one file the image
copies by `containers/signer/Containerfile.containerignore`; forge-ci applies
that file with either builder, as
[Building on the pool with kaniko](runners.md#building-on-the-pool-with-kaniko)
describes.

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

The actions and workflows run one build of this repository's own CLI per
architecture, linux/amd64 and linux/arm64. The tree does not hold them. It
holds one line per architecture, `.engine/bin/forge-ci-linux-amd64.sha256`
and `.engine/bin/forge-ci-linux-arm64.sha256`: the SHA-256 of the binary
that belongs to that commit. Every job gets the binary of its host's
architecture by that architecture's line.

- **Which.** `lib-forge-ci.sh` reads `uname -m` and takes `x86_64` or
  `amd64` as amd64 and `aarch64` or `arm64` as arm64. It refuses any other
  host rather than run a binary under emulation.
- **From where.** The generic package registry of the instance that serves
  the layer, under the owner of the repository that serves it, with the
  digest as the version:

  ```text
  https://FORGE_HOST/api/packages/OWNER/generic/forge-ci/<sha256>/forge-ci-linux-<amd64|arm64>
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
  SHA-256 is the recorded one for the host's architecture. `lib-forge-ci.sh`
  checks it against the same line before the first use in a shell, a version
  probe included, and caches successful verification for that binary path
  within the shell.
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

**A commit works once its binaries are published.** Publishing is the first
job of the organization's
[signer-image workflow](../examples/forgejo/signer-image.yml), run at the
commit it pins; it publishes both architectures. A job of a commit whose
binary is not there fails in the first step that needs the CLI, and the
message names the address and that workflow. A version of a generic package
cannot be uploaded twice, so a published binary stays what it is; a commit
that changes no Go code names binaries that are already there.

**The build is reproducible** from the Go release `go.mod` names, by
[`scripts/ci/engine-binary.sh`](../scripts/ci/engine-binary.sh). The binary
job of the signer-image workflow rebuilds it from the sources and publishes
only bytes that match the recorded checksums; see
[DEVELOPMENT](DEVELOPMENT.md#the-engine-binary). `forge-ci --version` of this
binary answers `engine-layer` at every commit; the digest identifies a build.
How the GitHub and GitLab layers get the engine is in
[Installing `forge-ci`](providers.md#installing-forge-ci).

`FORGE_CI_BIN` names a prebuilt binary to run instead, for example in an
offline job; in the signer jobs it must match the recorded checksum.

### Installing the binary in a job

A job of your own that calls `forge-ci` gets the binary through the layer, at
the commit the workflow already pins:

```yaml
- name: Set up forge-ci
  uses: https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/setup-forge-ci@PIN_FORGE_CI_SHA
```

The action downloads the binary that commit records from the instance's
package registry, verifies it against the recorded checksum and adds it to
`PATH`; `expected-sha256` adds a pin of the caller's own.

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
  a prepare job that calls `prepare-release.yml`, its release identity and
  declaration passed to the signer as bare `needs` expressions,
  the digest hand-off, one commit across every reference, and the signer
  image, key digest, transparency-log setting and every `enforce-*` setting
  written out as literals.
  [Signing boundary](#signing-boundary) says what it does not check.

The `enforce-*` inputs are the administrator's only where
`.forgejo/workflows/` is on a protected branch whose changes need the
administrator's code owners; see
[Administrator check enforcement](admin-policy.md#forgejo). Tests run in the
consumer's own job, for example through `build-go`, which runs
`go test ./...`.

The gate is the protected branch: require the pull-request jobs as status
checks. A direct push bypasses them.

## Builds

### Build and checks

The pull-request jobs and the release build job belong to the consumer, so
the consumer's code never runs in a job that holds a signing key. Each step
below is one step of such a job; the `forgejo/` directory of each ecosystem
example shows the two workflows they add up to.

| Step | Action | Runs |
|---|---|---|
| Lint | `lint-nanolinter` | `nanolinter verify --require <floor>`, with `disable-checks` and `offline` for what another job or the runner's network decides. The default floor is `secrets,sast`, as on the other forges. |
| Release isolation | `check-l3-isolation` | `validate isolation` over the consumer's `release.yml`. |
| Build, Go | `build-go` | `build go run`: `go mod download`, `go test ./...`, the Build SBOM, one binary per platform. |
| Build, Cargo | `build-cargo` | `build cargo run`: `cargo fetch`, `cargo test --locked`, the Build SBOM, one binary per platform. |
| Build, Maven | `build-maven` | `build maven run --expect-version`: `mvn install`, a refusal unless the POM declares the release version, the app or library lifecycle with its tests, the Build SBOM, all in `target/`. |
| Hand-off | a `run:` step | `release files declare`, which writes the checksums file and `dist/release-files.json`; then `upload-dist`. |
| Image | `release-images.yml` reusable workflow | Native per-architecture builds, scan-before-publication assembly and a release image ledger. |

**Where the tools come from.** No action assumes a runtime image; each runs
in whatever the job provides:

- A pull-request job installs no toolchain and receives no secret. Its lint
  and build jobs run in the `CI_JOB_IMAGE` image, which carries nanolinter,
  its check tools and the toolchain. It fetches only the checksum-pinned
  forge-ci engine; an offline job provides that build through `FORGE_CI_BIN`.
- The release build job installs Go and the tools of the consumer's mise
  config with `setup-toolchain`. `build-go` and `build-cargo` fetch their
  pinned, checksum-verified SBOM generator unless one is on `PATH` or
  `enable-build-sbom: 'false'` skips the SBOM; `build-cargo`'s is a `.tar.xz`,
  so the runner needs `xz`. A Maven build needs the JDK and Maven from the
  mise config or the job image.
- When the mise config declares `go`, the job runs with `GOTOOLCHAIN=local`,
  so a `go.mod` asking for a newer Go fails instead of fetching one; without
  it, `setup-go` installs the version `go.mod` names. A `tools:` subset of a
  Go repository must then include `go`; `setup-toolchain` refuses one that
  leaves it out. On a slow link, raise `mise-http-timeout-seconds` (default
  120).

**Version and commit are inputs.** In a release the runner's ref is the
request tag and its commit the request commit, so `build-go` and
`build-cargo` require `version` and `commit`, and `build-maven` requires
`commit`; none takes them from the runner: pass the prepare job's
`release-tag` and `release-sha`. A pull request passes `build-maven` no
`version`, and builds the version its POM declares.

**A Maven release commit carries its version.** `prepare-release.yml` bumps
the POMs of the Maven projects `artifacts.yml` declares, in the
`runtime-image-java` the caller pins by digest, in the signed release
commit, as GitHub's and GitLab's release preparation do, so the tagged source
declares the version the jar carries.
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
or duplicate section.

`forge-ci container build-push-oci-image` builds every requested platform on
one runner, so it avoids emulation only when the target stages copy files and
any `RUN` is in a `FROM --platform=$BUILDPLATFORM` stage. For native builds
per architecture use `release-images.yml`; see [Release images](#release-images).

### Unsigned GoReleaser handoff

`.forgejo/actions/build-goreleaser` packages an already checked-out prepared
release with GoReleaser, without signing, publishing or announcing, and
imports its unsigned release manifest. It takes `release-tag`, `release-sha`,
a `release-notes` file outside the dist directory, `config` (default
`.goreleaser.yml`) and `dist-directory` (default `dist`, matching the
GoReleaser configuration). The job brings GoReleaser and any SBOM tools its
configuration uses; the action installs nothing and refuses a checkout that
is not the prepared commit. Follow it with `upload-dist` and pass that
action's digest to `sign-and-publish-release.yml`.
[`examples/go-cli/forgejo/release-goreleaser.yml`](../examples/go-cli/forgejo/release-goreleaser.yml)
is the complete caller: copy it as `release.yml`, beside the example's
`pullrequest.yml`. Evidence that arrives after packaging is added with
`release files manifest --unsigned --evidence-file <file>`.

Other ecosystems keep their own build tools and end their build job with
`release files declare` and `upload-dist`. A custom Cargo producer can fetch
locked dependencies with retries, without compiling, through
`build cargo fetch --attempts 5 --retry-delay 10s`.

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
   and hands `dist/` on with its digest. `release-images.yml` can build the
   images instead; see [Release images](#release-images).
3. **`sign-and-publish-release.yml`** runs five separately privileged jobs:
   `sign`, `promote` (final image tags), `publish` (the Forgejo release),
   `packages` ([distro packages](#distro-packages-in-the-owners-registries),
   when declared) and `cleanup` (candidate tags).

**Promotion** is the `promote` job: after signing, it moves the final tags of
the images the build recorded. There is no `:dev`/`:staging` ladder. Base
images have their own lifecycle, [Base images](#base-images):
`base-images.yml`, `sign-container-images.yml`,
`promote-base-container-images.yml` and `verify-base-container-images.yml`,
and the `prune-base-images` action on a schedule.

Forgejo drops an untagged manifest as soon as its last tag moves away, so a
rollback can restore a moved tag only while another tag, in practice the
immutable version tag, still names the image; see
[Untagged manifests and rollback](providers.md#untagged-manifests-and-rollback).

Before it moves a tag, the `promote` job records what each tag served in the
run artifact `release-promote-<attempt>`; see
[Failed Release Recovery](failed-release-recovery.md#container-promotion-partially-completed).

If a release fails partway, release the next patch version or re-run the
failed jobs of that run, as [Quick start](#quick-start) says; [Failed Release
Recovery](failed-release-recovery.md) covers the rest.

### Release images

`release-images.yml` builds a repository's release images at the prepared
release commit. Without an `artifacts.yml` it builds one: the repository's own
image, from the root `Containerfile`, for `linux/amd64`. Declare `containers:`
in `.forge-ci/artifacts.yml` for more images, other platforms or tag variants.

```yaml
  release-images:
    needs: [prepare, build]
    uses: OWNER/forge-ci/.forgejo/workflows/release-images.yml@PIN_FORGE_CI_SHA
    with:
      runs-on: ${{ vars.CI_RUNNER_LABEL }}
      build-runs-on-linux-amd64: ${{ vars.CI_RUNNER_LABEL_CONTAINER || vars.CI_RUNNER_LABEL }}
      build-runs-on-linux-arm64: ${{ vars.CI_RUNNER_LABEL_CONTAINER_ARM64 || vars.CI_RUNNER_LABEL_ARM64 }}
      forge-ci-sha: PIN_FORGE_CI_SHA
      release-tag: ${{ needs.prepare.outputs.release-tag }}
      release-sha: ${{ needs.prepare.outputs.release-sha }}
      context-artifact: image-context
      # Empty publishes to this forge's own registry.
      image-repository: ghcr.io/OWNER/APP
    secrets:
      RELEASE_REGISTRY_TOKEN: ${{ secrets.RELEASE_REGISTRY_TOKEN }}
```

Buildah builds need user namespaces, so their runner labels follow the
container runners where an installation names them. Kaniko builds use the
admitted builder job image on the ordinary runner pool instead.

- **plan** reads the declaration and stops the run before any build on what
  the workflow does not do: `build-secrets`, `extract`, a `scan-severity`
  other than its default `CRITICAL` gate, a platform other than `linux/amd64` and
  `linux/arm64`, two containers publishing the same tag, and a build
  of a platform whose `build-runs-on-linux-<arch>` input is empty.
- **build** runs once per container and architecture, on a runner of that
  architecture. It checks the release commit out, unpacks `context-artifact`
  over it when one is named (binaries built in an earlier job, for example),
  and builds to an OCI layout with `build-arch --stage`, labelled with the
  source, revision, title, version, url, documentation, ref.name (the final
  tag) and created (the release commit's time), then the labels admitted by
  the plan. Custom labels cannot replace those provenance labels. It
  writes nothing to a
  registry and receives no secret, so the Containerfile never runs beside a
  token. Its pulls are anonymous, whatever login the runner keeps, so the
  bases must be public. It hands the layout to publish as run artifacts
  through the namespace's blob store, below.
- **publish** runs once per container. It verifies every staged architecture
  against its record and scans each with the layer's pinned Trivy. Findings
  are reported; with `enforce-vulnerabilities: true` a `CRITICAL` finding not
  approved in the release commit's `.trivyignore.yaml` blocks publication (see
  [Administrator check enforcement](admin-policy.md)). Then it uploads the
  index under `staging-<final tag>` and records it in the ledger.
- **collect** merges the ledgers into one artifact, `release-images`: a
  `release-images.json` and the Trivy results. Unpack it into the `dist/` the
  consumer hands to `sign-and-publish-release.yml`, which signs the images,
  generates their SBOMs and promotes the staging tags to the final ones.

The layouts travel between the jobs as run artifacts, which Forgejo counts
against the repository owner's storage quota for a day. A layer shared by
several images is stored once per run. An upload refused with HTTP 413 means
the quota is spent.

Images go to `image-repository`, for example `ghcr.io/owner/app`, which you
pass to the signer too, or to this forge's own registry,
`<host>/<owner>/<repository>`, when it is empty. Each container publishes
`<release-tag><tag-suffix>` there, so containers are tag variants told apart
by `tag-suffix`; the plan refuses an `image-name`:

```yaml
containers:
  - name: app
    container-file: Containerfile.alpine
    platforms: linux/amd64,linux/arm64
  - name: app-distroless
    tag-suffix: -distroless
    platforms: linux/amd64,linux/arm64
```

The registry credential is a pair the layer reads by name: the secret
`RELEASE_REGISTRY_TOKEN` and the variable `RELEASE_REGISTRY_USER`, the user
it logs in as. The `registry-user` input overrides the variable. With
neither, this forge's registry takes the calling repository's owner; on
another registry the plan stops before any build until one names the user
there.

The builder is the organization's, read from its variables, and the caller
names none: `CI_IMAGE_BUILDER` is `buildah`, the default, or `kaniko`, and
with kaniko `CI_IMAGE_BUILDER_JOB_IMAGE` is the kaniko job image pinned by
digest. The `coordinates` job checks both before a build job starts in that
image; [Runners](runners.md) covers when a pool needs kaniko. Kaniko runs the
Containerfile's steps in the job container's own filesystem, so a `RUN` step
that changes the directory the runner mounts the workspace at, such as
`/workspace` on some runners, fails there.

When files and images come from separate jobs, `assemble-release-dist` with
`artifact-names` listing `release-files` and `release-images` downloads both,
merges the ledger and uploads `dist/`; its `digest` output feeds the signer
as `upload-dist`'s does. See
[`examples/go-service/forgejo/release.yml`](../examples/go-service/forgejo/release.yml).
[Checked multi-architecture images](multiarch-images.md) covers images whose
job graph you write yourself.

#### Resolved consumer image policy

A consumer that computes its image selection or content-addressed base IDs
itself passes `image-plan-json` instead of `artifacts-config`: the
`builds`/`images` JSON that `container release-images plan` emits. Each build
names its image, native architecture, Containerfile, context, target, build
arguments and optional labels; each image its destination, staging and final
tags, architectures and optional ledger fields (kind, flavor, base ref and
input ID, moving tag, `required-labels`). The engine refuses unknown fields,
foreign destinations, conflicting tags, incomplete architecture sets and paths
outside the checkout before anything is scheduled. The plan cannot choose the
builder, add build secrets, replace the provenance labels or turn the scan
off. [`docs/schemas/release-images.schema.json`](schemas/release-images.schema.json)
is the full contract.

Use a distinct `artifact-name` for each call in one run; the `ledger-json`
output returns the published images to your policy gate. Building a base
image here neither signs nor promotes it; that is the
[base image](#base-images) lifecycle.

A private parent image is declared in each build's `registry-inputs`, for
example `{"BASE_IMAGE":"ghcr.io/owner/base@sha256:..."}`, matching a build
argument with that digest-pinned value. A separate job fetches it with the
registry credential, so no token enters the build job. Private parents need
Buildah; kaniko refuses them.

### Base images

A project that ships base images owns their graph: which image starts from
which, what an image's input ID covers, and its content-addressed tags. The
layer builds one stage of that graph at a time: a root, or the children of
one parent. A stage goes through four workflows, in this order:

1. **`base-images.yml`** takes the plan of the stage, `image-plan-json`, in
   the shape of a [resolved consumer image policy](#resolved-consumer-image-policy):
   images of `image-kind: base` only, with no `base-ref`, moving tag or
   architecture cache; a root stage is exactly one image. It admits the plan
   before any build starts, builds each image and architecture natively in a
   job with no secret, scans every layout and only then pushes the unsigned
   `staging-<final tag>` candidates. Its outputs are the
   stage's `ledger-json` and `base-inputs-json`, the input map the build
   declares; the SBOMs the ledger pins are the artifact
   `<artifact-name>-sboms`.
2. **`sign-container-images.yml`** signs and attests that ledger, with the
   SBOM artifact as `sbom-artifact`, in the one job that holds the key.
3. **`promote-base-container-images.yml`** takes the same `ledger-json` and
   `base-inputs-json`, verifies the signature, the SBOM and the lineage
   against your key, and only then moves the final tag. `base-inputs-json`
   is required of every caller: a JSON array that names each promoted flavor
   once, with its `base_input_id` and, for a child, its `parent_base`
   `{"ref","input_id"}`. A caller that does not build with `base-images.yml`
   passes the map it gives `verify-base-container-images.yml`.
4. **`verify-base-container-images.yml`** with `expected-ref`, the promoted
   digest, authenticates that one image as a parent. Its `parent-base-json`
   output, `{"ref","input_id"}`, is the only value a child is planned from.

A base that is already published is not built again, and is a parent on the
same terms. Called without `expected-ref` and with `RELEASE_REGISTRY_TOKEN`,
`verify-base-container-images.yml` checks every flavor of `flavors-file` and
returns `parent-bases-json`, one `{"ref","input_id"}` per flavor it found:
the digest the final tag resolved to, with the signature, SBOM and lineage
verified on that digest against your key, source, workflow, flavor and input
ID. One call covers every reused parent of the graph:

```yaml
parent-base-json: ${{ toJSON(fromJSON(needs.existing.outputs.parent-bases-json).base) }}
```

A flavor that is missing has no entry, and without the token the map is
empty; `base-images.yml` refuses the child planned from either.

A child is the same four steps with `parent-base-json` passed to
`base-images.yml`. Its plan must name that parent three times, and the layer
refuses a plan that names anything else: the one registry input of every
build, the build argument that carries it, and `parent_base` in the image's
`provenance-json`. The parent is fetched by digest in a job that holds the
registry token and runs nothing of yours, and the build job starts from
those bytes. The child's signed lineage then names its parent's digest and
input ID, and promotion holds the image's own statement to it.

A child builds with Buildah only, from the parent layout imported after it
is verified against the pinned digest; kaniko would need a registry
credential in the job that runs your Containerfile. With
`CI_IMAGE_BUILDER=kaniko` the plan refuses the child. A root builds with
either builder. Where your runners are kaniko-only, set `CI_IMAGE_BUILDER` to
`buildah` on the repository and borrow a runner; see
[Runner lending](runners.md#runner-lending).

Siblings share a stage. A plan that lists several images, with the builds of
each, is one call of each of the four workflows for all of them: every image
is held to `parent-base-json` on its own, one image that names another
parent refuses the whole stage, and the ledger and `base-inputs-json` carry
one entry per flavor. A root and seven flavors built on it are two stages.

A graph is these stages in order, one `needs:` after the other. An image with
several children is authenticated once. The layer does not walk the graph
and has no recursive mode. A build runs on the runner its
architecture's input names, `build-runs-on-linux-amd64` or
`build-runs-on-linux-arm64`, and the plan is refused when it builds an
architecture whose input is empty.

[`examples/base-images`](../examples/base-images/) is a root and two sibling
flavors built on it, two stages released as one workflow.

The signing job trusts the ledger it receives from the same run, bound to the
SBOM bytes by digest; Forgejo cannot prove which job wrote an artifact. See
[Signing boundary](#signing-boundary).

### The published release is read back

After the `Publish: release` job of `sign-and-publish-release.yml` has
published and the old releases are trimmed, its last step reads the release
back with `forge-ci release validate-published` and compares it with the
assets of `dist/release-files.json`, signatures and provenance included. The
run fails, naming each file, when a planned file is missing or has another
size, and when the tag has no release. A file the plan does not name is only
a warning. The step reads with `forgejo.token`, or with `RELEASE_TOKEN` for a
draft (any `release-draft` but `false`).

### Distro packages in the owner's registries

A release whose `artifacts.yml` has a `distro-packages` block publishes its
packages to the Debian, RPM and Alpine registries of the repository's
owner, so a user installs them with `apt`, `dnf` or `apk`. When the block is
present, every `.deb`, `.rpm` and `.apk` file of the release is published.
The block is the opt-in and holds only the places, so an empty one is
enough:

```yaml
distro-packages: {}
```

The places default to distribution `stable` and component `main` for Debian,
group `stable` for RPM, and branch `stable` and repository `main` for Alpine;
[the reference](artifacts-reference.md#distro-packages-section) has the fields, and
[examples/go-cli/artifacts-forgejo-packages.yml](../examples/go-cli/artifacts-forgejo-packages.yml)
is a declaration to copy. Without the block nothing is published to a
registry and the packages stay release files.

The job `Publish: packages` of `sign-and-publish-release.yml` does it, after
`Publish: release`, with `RELEASE_REGISTRY_TOKEN` as its one secret. It runs
no consumer command: it takes the signed `dist/` by its digest, verifies the
release's checksums against the files, and runs
`forge-ci publish forge-packages distro-packages`. Nothing is built again, so
what the registry gets is the file the release built, checksummed and signed.
A release without the block does not start the job: no container, no secret.

The packages are published after the release is public, so what would stop
them is found before:

- the prepare workflow refuses a declaration with the block and no
  `RELEASE_REGISTRY_TOKEN`, before it makes the release commit;
- the `sign` job checks, before it signs anything, that the token is
  configured, the instance is reached over HTTPS, the release holds a
  package, and each is publishable (see [What is refused](#what-is-refused)).

What is left to the packages job is the registry's own answer: a token
without the right to write the owner's packages, a registry that is down,
and the conflicts below.

Each file is sent once, unchanged, and the registry is then read back the way
a package manager reads it: the Debian `Packages` index, the RPM `primary`
metadata, or the Alpine file itself has to carry the file's SHA-256 at the
declared place. That makes a rerun safe:

| The registry | Result |
|---|---|
| takes the file and serves its digest | published |
| refuses the file as a version it holds, and serves this digest | left as it is; the job succeeds |
| refuses it, and serves the Alpine package of that name with another digest | the job fails with "other content"; nothing is replaced |
| refuses it, and lists no such digest within 30 seconds | the job fails and says both things it can be; nothing is replaced |
| takes the file and serves another digest | the job fails: the registry changed the package |

The fourth row is usually two runs publishing one version at once: rerun,
and if it fails the same way, the Debian or RPM registry holds other content.

The job never deletes or overwrites. A version published with other content
stays until its owner removes it; release the next version.

The upload follows no redirect, so the token and the package go to the
instance alone. A read follows one HTTPS redirect without the token, as from
an instance that serves packages from object storage (`SERVE_DIRECT`).

#### Installing, and what each signature proves

Forgejo signs the metadata of each owner's registry with a key it generates
and keeps for that owner: Debian's `Release` file, RPM's `repomd.xml`,
Alpine's `APKINDEX`. The metadata lists the digests of the packages, so a
package manager that checks it installs the bytes the registry lists. That
signature proves that the instance serves this package to this owner's
registry. It does not prove who built the package: whoever may write the
owner's packages, or runs the instance, can publish under it. The registry
key is not a release key.

The release's own signatures are on the release page: the checksums file
with its Cosign bundle, or its OpenPGP `.asc` when `artifacts.yml` says
`sign.method: gpg`, and, with `sign-distro-packages: 'true'`, an OpenPGP
`<package>.sig` for each package, made with `RELEASE_GPG_PRIVATE_KEY`. The
published package is that file byte for byte, so a downloaded package is
verified against them:

```bash
apt-get download hello                # or: dnf download hello, apk fetch hello
sha256sum hello_1.2.3_amd64.deb       # the line in the release's checksums file
gpg --verify hello_1.2.3_amd64.deb.sig hello_1.2.3_amd64.deb
```

[Verification](verification.md) has the Cosign command for the checksums
file.

```bash
# Debian and derivatives
curl -fsSL https://FORGE_HOST/api/packages/OWNER/debian/repository.key \
  -o /etc/apt/keyrings/forgejo-OWNER.asc
echo "deb [signed-by=/etc/apt/keyrings/forgejo-OWNER.asc] https://FORGE_HOST/api/packages/OWNER/debian stable main" \
  > /etc/apt/sources.list.d/OWNER.list

# Alpine
curl -fsSL -J -O --output-dir /etc/apk/keys https://FORGE_HOST/api/packages/OWNER/alpine/key
echo "https://FORGE_HOST/api/packages/OWNER/alpine/stable/main" >> /etc/apk/repositories
```

For RPM, write the repository file yourself. The one Forgejo generates at
`rpm/<group>.repo` sets `gpgcheck=1`, which wants a signature inside each
RPM, and the release's RPM has none unless your build put one there; it does
not set `repo_gpgcheck`, which is the check of the signed metadata:

```ini
# /etc/yum.repos.d/OWNER.repo
[OWNER]
name=OWNER
baseurl=https://FORGE_HOST/api/packages/OWNER/rpm/stable
enabled=1
repo_gpgcheck=1
gpgcheck=0
gpgkey=https://FORGE_HOST/api/packages/OWNER/rpm/repository.key
```

Upstream: Forgejo's [Debian](https://forgejo.org/docs/latest/user/packages/debian/),
[RPM](https://forgejo.org/docs/latest/user/packages/rpm/) and
[Alpine](https://forgejo.org/docs/latest/user/packages/alpine/) registries.

#### What is refused

- **GitHub and GitLab.** GitHub Packages has no Debian, RPM or Alpine
  registry. GitLab has no RPM or Alpine registry, and its Debian registry is
  an experiment behind a feature flag that is off by default. A declaration
  with `distro-packages` is refused when either release is planned.
- **An Alpine package for `noarch`.** The registry files it under
  architectures of its own choosing, so it has no one place to be read back
  from. Build a package for each architecture.
- **An Android `.apk` in the same release.** Every `.apk` release file is
  taken for an Alpine package, and one without a `.PKGINFO` stops the `sign`
  job.
- **A release with the block and no package.** The `sign` job stops.
- **A missing token.** The prepare workflow and the `sign` job stop.
- **An instance that signs RPMs on upload** (`DEFAULT_RPM_SIGN_ENABLED`). It
  stores other bytes than the release signed. This is found only after the
  upload: the packages job fails on the read-back and the stored package
  stays, on every rerun, until the instance stops signing or the block is
  removed. Nothing the publisher may read says beforehand that an instance
  signs.
- **A redirect of the upload, a second redirect of a read, or one that is
  not HTTPS.**
- **An instance that is not reached over HTTPS.** The `sign` job stops.

The packages of one release go in manifest order, after every one of them
was found publishable; a failure partway leaves the earlier ones published,
and the rerun leaves them as they are.

### Old releases' files

Each release adds its archives, binaries and packages, and a forge's storage
quota fills. With `release-asset-retention: <N>` on
`sign-and-publish-release.yml` the release keeps every file
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

- `image-repository` is the release images' repository, for example
  `ghcr.io/owner/project`. Pass the same value to `release-images.yml`, which
  publishes the images there, and to `sign-and-publish-release.yml`, which
  signs, promotes and cleans up only there.
- The bases' repository, for example `ghcr.io/owner/project-base`, is the
  `image-repository` of `base-images.yml` and the `base-repository` of
  `sign-container-images.yml`, `verify-base-container-images.yml` and
  `promote-base-container-images.yml`.
- The registry user is named as under [Release images](#release-images).

Every ledger image must be in that one repository, and its token decides what
can be written there.

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
    runs-on: ${{ fromJSON(vars.CI_RUNNER_LABEL) }}
    steps:
      - uses: https://FORGE_HOST/OWNER/forge-ci/.forgejo/actions/verify-forgejo-token@PIN_FORGE_CI_SHA
        with:
          token: ${{ secrets.RELEASE_TOKEN }}

  prepare:
    needs: preflight
    uses: OWNER/forge-ci/.forgejo/workflows/prepare-release.yml@PIN_FORGE_CI_SHA
```

The changelog renderer is `changelog-creator`, `git-cliff` by default or
`git-chglog`, reading the consumer's `changelog-config` and `commit-body-config`,
by default `.forge-ci/gitcliff/keepachangelog.toml` and
`.forge-ci/gitcliff/minimal.toml`: `forge-ci init` writes them from this
repository's [`templates/gitcliff/`](../templates/gitcliff/), or copy the two
files there by hand to start, the same
defaults the GitLab `release` component has. Both config files must exist; a
missing one stops the render instead of letting git-cliff fall back to its
built-in config. A SHA-256 repository needs `git-chglog` with its own configs:
git-cliff cannot read one, and the render refuses it by name.

The workflow keeps the boundaries a hand-composed job has to get right itself:
every secret is checked for presence first, no token enters the job that
holds the SSH signing key, and the request is admitted only at the head of
the release branch. The project's changelog configuration runs only in a
key-free `render` job; the `prepare` job copies back only `CHANGELOG.md` and
the commit message, validates the request again, and only then loads the SSH
key. This relies on runners that give each job a fresh filesystem.

Preparation also takes the same `signer-image` literal as the signer
workflow, and reads its manifest anonymously before checking out consumer
code, so a missing image fails before any release commit. It checks that the
image exists, not its toolchain or signing identity.

For a private image, pass `skip-signer-image-check: 'true'` explicitly and
configure image-pull access on the trusted runner. The digest-pinned coordinate
and the signer's own toolchain/key checks remain mandatory. A misspelled skip
value fails instead of silently bypassing the check.

**A version bump never runs where the key is.** The release plan of the
consumer's `artifacts.yml` (the `artifacts-config` input,
`.forge-ci/artifacts.yml` by default) decides the bump, as on GitLab; there
is no `version-bump` input. A release without a declaration, or whose
projects have no version file (Go), carries the changelog alone.

A bump runs the project's own tools and their plugins (`mvn versions:set`,
`npm version`, Cargo), so it runs in a job of its own per toolchain family,
`java`, `node` or `rust`, in the `runtime-image-<family>` you pin by digest,
with no secret at all. A planned family without its image stops at
`Prepare: version plan`. The `prepare` job runs no build tool: it applies the
version changes of the planned project files only, writes the Gradle and Xcode
versions itself, and commits them beside `CHANGELOG.md`. Any other file a
version job touched never reaches the release commit.

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
one child commit changing exactly `CHANGELOG.md`, a regular file at its
parent's mode, and the local
`release-request/<tag>` still naming the tested request. A failed Git read,
an extra changed path, or a moved request is a refusal.

Branch protection must let the bot's key push to `release-branch` without
force.

## Snapshots

`.forgejo/workflows/snapshot-images.yml` publishes declared development containers
using the [shared snapshot contract](workflows.md#snapshot-container-contract-all-three-forges).
`publish-containers` defaults to `true`; set it explicitly to `false` to opt out.
Pass `branch` explicitly for push or manual developer-branch selection. The layer
freezes that branch to one SHA, uses native amd64/arm64 runners, stages without
registry publication credentials, waits for all builds and collects the exact
image receipt set. Tags always include the original branch hash and finish with
lower-case `-snapshot`; no stable aliases, promotions or signing are performed.

Call the reusable workflow from the trusted, SHA-pinned layer repository. Supply
matching `forge-ci-sha`, `forge-ci-repository`, `image-repository`, `registry-user`
and JSON-encoded `runs-on` and `build-runs-on-linux-amd64` (plus
`build-runs-on-linux-arm64` for arm64). Pass only
`SNAPSHOT_REGISTRY_PASSWORD` as the publication secret. Builder selection follows
the organization's `CI_IMAGE_BUILDER` and admitted kaniko job-image policy.
Outputs are `source-sha` and `container-images-json`.

`publish-files: true` also keeps the built files of the declared artifacts as
unsigned run artifacts, named `<artifact-name>-files-<artifact>-<snapshot version>`
and kept for 7 days for readers of the run, see
[snapshot files](workflows.md#snapshot-files). A declaration with
artifacts alone needs neither `image-repository`, `registry-user` nor
`SNAPSHOT_REGISTRY_PASSWORD`; all three are required as soon as the
declaration has a container and `publish-containers` is not `false`: the
plan step refuses an image without `image-repository` or `registry-user`
before anything builds. The
`files` job builds in the same toolchain image as an image's producer
(`artifact-job-image-*`) and receives no secret.

Declared artifacts that the images take are built first, automatically, with
the Go, Cargo, Maven, Gradle JVM and npm builders. Go and Cargo producers fetch
the same pinned SBOM generator as `build-go` and `build-cargo`, so their image
needs `curl`, `tar` and `sha256sum` (and `xz` for Cargo).

Optional `artifact-job-image-go`, `artifact-job-image-java`,
`artifact-job-image-node` and `artifact-job-image-rust` inputs select trusted
digest-pinned producer toolchain images. They fall back to repository variables
`SNAPSHOT_ARTIFACT_JOB_IMAGE_GO`, `SNAPSHOT_ARTIFACT_JOB_IMAGE_JAVA`,
`SNAPSHOT_ARTIFACT_JOB_IMAGE_NODE` and `SNAPSHOT_ARTIFACT_JOB_IMAGE_RUST`,
respectively. When unset, producers use the existing amd64 host tools.

`context-artifact` lets a job of the same run prepare the build context
itself instead; it must build at the frozen commit and emit its bundle with
`gather-context`. See the
[shared input contract](workflows.md#automatic-artifact-first-inputs).
The snapshot reads the release's declaration and leaves its signing and
provenance out, see
[what a snapshot takes from the declaration](workflows.md#what-a-snapshot-takes-from-the-declaration).

Callers to copy:
[`examples/go-service/forgejo/snapshot.yml`](../examples/go-service/forgejo/snapshot.yml)
publishes container-only snapshot images,
[`examples/maven-app/forgejo/snapshot.yml`](../examples/maven-app/forgejo/snapshot.yml)
builds the jar first in the Java image named by `artifact-job-image-java`,
[`examples/go-cli/forgejo/snapshot-files.yml`](../examples/go-cli/forgejo/snapshot-files.yml)
keeps a branch's built files with `publish-files: true`, and
[`examples/go-cli/forgejo/snapshot.yml`](../examples/go-cli/forgejo/snapshot.yml)
only builds and tests with `build-go`, publishing nothing.
Build secrets remain unsupported; `extract.binary` is a release's output,
which a snapshot does not run. Scan severity and image SBOM policy are
independent.

## Signing

The layer signs with keys the organization holds:

| Object | Key | Secret |
|---|---|---|
| Release commit and final tag | SSH | `RELEASE_SSH_SIGNING_KEY` |
| Checksums and SBOMs | Cosign key pair, or OpenPGP when `artifacts.yml` says `sign.method: gpg` or `sign.files-method: gpg` | `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE`; with `gpg`, `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE`, `RELEASE_GPG_FINGERPRINT` |
| Provenance, evidence, images and their attestations | Cosign key pair | `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE`, checked against `cosign-public-key` and its digest |
| `.deb`, `.rpm`, `.apk` packages, when `sign-distro-packages` is on | OpenPGP | `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE`, `RELEASE_GPG_FINGERPRINT` |

Keyless signing is not the layer's path: public Fulcio does not trust a
Forgejo issuer (see [forgejo](providers.md#forgejo)), and the workflows have
no KMS input. Whether signatures also go to the public transparency log is the
`transparency-log` input, `none` by default. A `sign.transparency` in
`artifacts.yml` is not used, and `forge-ci doctor` and the release plan warn
about it.

A declaration that says `sign.method: gpg`, or `sign.files-method: gpg`
beside a Cosign method, signs the checksums and SBOMs with
the OpenPGP key: each gets an armored `.asc` beside it on the release page,
and no Cosign bundle. The sign job reads the methods from the declaration at
the release commit, signs in its own step with only the three
`RELEASE_GPG_*` secrets, and refuses a key whose fingerprint is not
`RELEASE_GPG_FINGERPRINT`. The provenance statement, the evidence and the
images are signed with the Cosign key all the same, so the Cosign secrets
stay required. Any other method, and a declaration without a `sign`
section, signs the checksums and SBOMs with the Cosign key. A different
`sign.method` or signing key in `artifacts.yml` is not used, and `forge-ci
doctor` and the release plan warn about it. With the
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

### Signing boundary

With `sign.method: gpg`, the sign job must run without swap: the engine
signs in process with OpenPGP and refuses to read the key while its memory
can be swapped out, and the signer container cannot turn swap off
([swap policy](verification.md#swap-policy)). Turn swap off in the runner
host's setup, or run the job containers with swap disabled: set
`--memory-swap` equal to `--memory` in `container.options` of the runner's
`config.yml`, for example `--memory 4g --memory-swap 4g`. The container limit
leaves the runner daemon's copy of the key swappable, so prefer host swap off
on production signer runners
([residual risk](verification.md#swap-policy)). Cosign signs in a
subprocess and is exempt.

The boundary is these properties together:

- **Separately privileged jobs.** Signing keys never enter the promote or
  publish job, and the release token never enters the sign or promote job.
  The only hand-off between them is the signed `dist/`, carried with its
  digest.
- **Each job states the success it waits for.** A job of the layer's
  workflows that needs another runs only on
  `needs.<job>.result == 'success'` for each need, written in its own `if`.
  Forgejo 17 does not apply the implicit success of a job's needs inside a
  called workflow, so without it a refused signing would still start
  promotion, publication and the packages.
- **Trust is established again in every job.** Each privileged job creates a
  private workspace, checks the layer out at `forge-ci-sha`, verifies the
  checkout, downloads the binary that checkout names in a step of its own,
  "Fetch engine binary", which holds no secret, and runs the toolchain check
  before any step that holds a secret or runs the engine.
- **One executable.** The `forge-ci` binary, at the checksum recorded in the
  checkout, is the one non-shell executable the signer image allows.
- **The release commit is checked.** Each privileged job checks the release
  tag, that the release commit is the single child of the commit the request
  named, and that it changes only `CHANGELOG.md` and the planned version
  bump; a commit that edits the declaration itself is refused.
- **The caller passes the prepare job's outputs.** Pass the signer
  `${{ needs.prepare.outputs.artifacts-config }}`, `release-tag` and
  `release-sha` as bare expressions; `check-l3-isolation` refuses anything
  else.
- **The hand-off is unsigned.** The signer refuses a signature, bundle or
  provenance statement that arrives with the build's `dist/`; it writes those
  itself.

**The Cosign trust root is the caller's.** The layer ships no key, and there
is no key file in the helper checkout to fall back to: `cosign-public-key` and
`cosign-public-key-sha256` are required, the step writes them to files in the
job's private workspace, and the engine refuses an empty key, an empty digest
and a key that does not hash to the digest. Half a pair is refused, not
completed.

The signing workflows only compare: they sign when the private key in the
job's secrets derives the trusted public key, so a wrong public key fails the
job. The base-image workflows verify with the key, so there a wrong key would
accept bases signed by its private half.

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
container starts.

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

`check-l3-isolation` checks that the jobs calling a signer workflow write
the value out; the `transparency-log` input of a `prune-base-images` step is
yours to write out.

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

**What the pull-request gate does not check.** `check-l3-isolation` checks the
declared workflow: it rejects mixed pins, a signing secret in the build job,
an inherited signer secret set, a broken digest hand-off, and a signer image,
key digest or transparency-log setting that is not a literal. For secrets it
reads the build job and the workflow-level `env` that job inherits, and looks
for the names in `signing-secrets` alone: another secret in the build job, or a
secret in a job it was not told about, is not looked at. It does not check
runner isolation, branch protection, or the provenance of a completed release.
A direct push bypasses it, so the protected branch is part of the boundary.

It also refuses unknown inputs to the layer's actions and workflows.

## Code scanning (SARIF)

Forgejo ingests no SARIF report
([forgejo#3669](https://codeberg.org/forgejo/forgejo/issues/3669)).
`lint-nanolinter` gates on its exit status and its findings are in the step
log; it writes no SARIF file. `forge-ci security report upload-sarif` on a
Forgejo runner prints a notice and exits 0.

## GHCR package retention previews

`.forgejo/workflows/package-retention.yml` previews which old versions of
GHCR packages a retention rule would delete; it never deletes.
[`examples/forgejo/package-retention.yml`](../examples/forgejo/package-retention.yml)
is the caller and owns the schedule. It takes `runs-on`, a digest-pinned
`job-image` holding the CLI, that CLI's `engine-sha256`, `registry-user`,
the `RELEASE_REGISTRY_TOKEN` secret, and `packages-json` listing the consumer
pins to protect, which the example reads from the `PACKAGE_RETENTION_MATRIX`
variable:
`{"include":[{"repository":"ghcr.io/owner/name","digest":"sha256:..."}]}`.
Release tags (`v1.2.3`) are always kept. Every listed digest is kept with its
signatures and manifests, as are the `recent-count` newest versions
(default 10); `max-delete` (default 100) caps the preview. A listed digest
that the package does not hold stops the pass.

Apply the rule yourself once you have reviewed the preview, with a classic
token holding `read:packages` and `delete:packages`:

```bash
forge-ci container package-retention --dry-run=false \
  --packages-json "$PACKAGE_RETENTION_MATRIX" \
  --registry-username USER --token-file /path/to/token
```

For one package, `--repository ghcr.io/owner/name --keep-digest sha256:...`
replaces `--packages-json`.

## Forks

### Using the layer from a fork

The layer names no organization. An owner, an organization or a user, serves
it from its own copy of this repository, under any repository name, and
brings its own signer image, Cosign key and bot identity. From nothing to a
first release:

1. **Put the repository on your instance.** Forgejo does not fork across
   instances, so create an empty repository and push this history to it.
   - Object format **SHA-1**: this history is SHA-1, and a SHA-256 repository
     cannot receive it. Pins are full commit IDs, 40 hex characters for SHA-1
     or 64 for SHA-256.
   - **Readable without credentials** by your runners: public, or readable
     anonymously on an instance that allows it.
   - Protect the branch against force-push and deletion, and protect tags.
     Consumers pin commits, and a pinned commit must stay reachable from a
     branch or a tag: each job fetches the commit directly and falls back to
     the advertised branches and tags.
   - In an unchanged upstream copy, nothing runs on its own. Every file under
     `.forgejo/workflows/` starts only when another workflow calls it, and
     Forgejo reads that
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
   [Development](DEVELOPMENT.md#the-engine-binary) describes,
   before you pin: a commit whose checksum is not a build of its tree is
   refused by the binary job of the next step. `just test-forgejo`, with the
   Go release `go.mod` names, rebuilds the CLI and compares it with the
   recorded checksum, so you can check a commit yourself.
4. **Publish the binary and build your signer image.** One workflow does
   both, in that order. Copy
   [`examples/forgejo/signer-image.yml`](../examples/forgejo/signer-image.yml)
   into one repository of your copy's owner, set its two placeholders to
   your copy and the commit of step 2, and run it. It needs the
   `CI_RUNNER_LABEL` variable (a native AMD64 runner),
   `CI_RUNNER_LABEL_ARM64` (a native ARM64 runner for the arm64 image
   build, which never runs under emulation) and two
   credentials: `ENGINE_PACKAGE_TOKEN` writes the engine owner's generic
   package on this forge; `SIGNER_REGISTRY_TOKEN`, with `SIGNER_REGISTRY_USER`,
   authenticates to the signer image registry.
   To host the signer on another registry, set the workflow's literal
   `SIGNER_IMAGE_REPOSITORY` to its full OCI repository without a tag or digest.
   Both architecture builds and assembly use that destination; source labels
   still identify the authenticated engine checkout.
   - Its first job builds the CLI of the pinned commit for both
     architectures from modules it downloads itself, stops unless each build
     is the one the commit's `.engine/bin/forge-ci-linux-<arch>.sha256`
     names, and uploads both to the generic packages of the owner of your
     copy. Until that has run, every job of the
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

### Maintaining your own CI and release configuration

Upstream `.forgejo/workflows` holds only the reusable layer, and
`test/forgejoguard` treats every file there as a layer workflow: it must start
only through `workflow_call` and name no organization. A fork that adds its own
Forgejo workflows there exempts those exact files in its own copy of the
guards, never a pattern or a layer path. Run them on
`${{ fromJSON(vars.CI_RUNNER_LABEL) }}`, or pass that variable as `runs-on` to
a reusable workflow, and keep your organization's repository, signer-image and
trust coordinates in those files only; the reusable layer stays
organization-neutral.

The actionlint configuration accepts Forgejo full-URL action references only
in Forgejo workflow files. Use the consumer release examples for the build/sign
separation and run `check-l3-isolation` against the fork's release workflow as
well.

The runner and permissions remain deployment responsibilities. Forgejo does
not enforce GitHub's `permissions:` field: configure the actual repository-token
permissions. Untrusted pull-request code must not share persistent writable
runner/tool state with release jobs or receive release credentials. For
token-bearing release tool installation, use `setup-toolchain` with
`cache: 'false'`, which isolates mise state as well as disabling cache restores.

### Updating pins and checking a migration

Treat a pin update as one reviewed change:

1. Authenticate the engine commit and build it with the compiler in `go.mod`.
   If build inputs changed, run `just refresh-engine-binary` and commit the
   `.engine/bin/forge-ci-linux-{amd64,arm64}.sha256` recordings first.
2. Publish and verify the recorded binaries of the commit you will pin.
   A newer checkout's checksum cannot be paired with an older action pin.
3. Update all engine `uses:` references, `forge-ci-sha` inputs and explicit
   `expected-sha256` values together. Match the expected checksum to the runner
   architecture. Keep signer-image digests and the Cosign trust-root digest
   explicit and reviewed too.
4. Run `just check-engine-binary`, the workflow guards, and release-isolation
   validation. Then verify a hosted pull request before the first release.
   Local tests alone do not prove the runner image, registry access or secrets.

`forge-ci init https://FORGE_HOST/OWNER/REPOSITORY` works from a renamed
copy too; its `.forge-ci/consumer-defaults.yml` presets the organization's images
and trust values, as under [Quick start](#quick-start).

What a copy cannot do, such as run on a runner that is neither linux/amd64
nor linux/arm64, is listed under [Limits](#limits).

### Try your repository against your copy, on a lab

Before you move your consumers' pin to a commit of your copy, you can run a
consumer against that commit on a disposable lab with
`just test-live-consumer forgejo /path/to/consumer pull-request` or
`release`; see [Trying a consumer](live-runs.md#trying-a-consumer). It shows
that the consumer and the layer agree on the lab's runner, not on yours.

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

An artifact upload that fails at its final step may or may not have landed;
rerun, and give the forge operator the run ID and the UTC time from the log.
`--log-level=debug` also logs that step's status and timing.

## Limits

Not supported:

- A runner that is neither linux/amd64 nor linux/arm64. The binary is
  published for those two; its file name carries the architecture, and the
  resolver refuses another one.
- A serving repository that needs credentials to read, or one whose owner's
  packages do. The download of the binary sends no token, and has no input
  for one.
- A certificate authority the job images do not already trust.
- A pull-request job image from this repository: `CI_JOB_IMAGE` must carry
  nanolinter, its check tools and your toolchain, and nothing here builds one
  for a Forgejo instance.

Not in the layer:

- A MegaLinter action and SARIF ingestion.
- Builds other than Go, Cargo and Maven: no Gradle, npm or Xcode action. A
  Gradle consumer's build job runs `forge-ci build gradle run` itself, as
  [examples/gradle-app/forgejo/](../examples/gradle-app/forgejo/) does.
- Publication to the forge's package registry, other than
  [distro packages](#distro-packages-in-the-owners-registries): no npm or
  Maven publisher workflow.
- Planning the release build from `artifacts.yml`: the build job is yours.
  The layer reads the file for the version bump, signing, release images and
  snapshots.
- Keyless or KMS signing in the workflows.
