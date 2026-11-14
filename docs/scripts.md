# Scripts

The Go CLI owns the core workflow operations. Scripts and third-party actions
still handle bootstrap, environment setup and some artifact handoffs. This page
lists every tracked shell script by directory; each script's header comment
holds the longer description.

For the **CLI surface** available to workflows, see
[docs/cli-reference.md](cli-reference.md). That file is auto-generated
from the binary's urfave/cli command tree by `cmd/gen-cli-reference`;
run `just gen-cli-reference` after any CLI change.

## Delivery layers

These run in consumer jobs from a checkout of this repository and are part of
the layers' contract; see the [Forgejo guide](forgejo.md) and the
[GitLab guide](gitlab.md).

| Path | Purpose |
|------|---------|
| `.engine/bin/lib-forge-ci.sh` | The shared resolver for the layers' forge-ci binary: downloads it by the recorded `forge-ci-linux-<arch>.sha256` digest and verifies it before it runs. Sourced by the Forgejo and GitLab layer scripts. |
| `.forgejo/actions/setup-forge-ci/setup-forge-ci.sh` | The `setup-forge-ci` action: makes the engine root's forge-ci binary present in the job, verifies it against the recorded checksum and an optional caller pin, then exposes it. |
| `.forgejo/actions/verify-forgejo-token/verify-forgejo-token.sh` | The `verify-forgejo-token` action: checks a Forgejo API token with `forge-ci validate auth token` and reports a rejection or a redacted diagnostic. |
| `.forgejo/bin/setup-image-builder.sh` | Exec glue for the `setup-image-builder` action and callable workflows: runs `forge-ci container setup-image-builder` for the selected image builder. |
| `.forgejo/bin/snapshot-images.sh` | Native snapshot steps sourced by `snapshot-images.yml` from the pinned layer checkout; the engine owns planning and publication. |
| `.forgejo/scripts/verify-signer-toolchain.sh` | Checks that a signer image has the required tools, none of the forbidden ones, and the layer's recorded forge-ci binary. Run by the runtime and base image workflows. |
| `.gitlab/bin/release.sh` | Trusted GitLab release orchestration behind `templates/release-jobs.yml`; see the [GitLab guide](gitlab.md). |
| `.gitlab/bin/snapshot-images.sh` | Native GitLab snapshot transport behind `templates/snapshot-image-jobs.yml`; the Go owners plan and admit the child graph. |

## `scripts/bootstrap/`

Toolchain installers invoked at runtime-image build time and by jobs that run
on the consumer's runner. Each is sourced, then its `install_<tool>` function is
called. Renovate-managed pinning lives here where applicable.

| Path | Purpose |
|------|---------|
| `scripts/bootstrap/install-common.sh` | Shared helpers (install directory, OS and architecture normalization) sourced by the other installers. |
| `scripts/bootstrap/install-buildah.sh` | Makes buildah and podman available for `forge-ci container build`; a no-op where they are pre-installed. |
| `scripts/bootstrap/install-cargo-cyclonedx.sh` | Installs the pinned cargo-cyclonedx, the Cargo Build SBOM generator. |
| `scripts/bootstrap/install-cosign.sh` | The one pin of cosign: the runtime Containerfile bakes it, and the engine's own bootstrap chain, which cannot run in the images it produces (Self Runtime Container, build-cli, release-binary, the self-release and self-pullrequest gates), installs it on the host. Every consumer route runs the cosign baked into the runtime images. |
| `scripts/bootstrap/install-cyclonedx-gomod.sh` | Installs the pinned cyclonedx-gomod, the Go Build SBOM generator. |
| `scripts/bootstrap/install-forgejo-runner.sh` | Installs the pinned forgejo-runner for a lent machine. |
| `scripts/bootstrap/install-gh.sh` | Installs the pinned GitHub CLI. |
| `scripts/bootstrap/install-git-cliff.sh` | Installs the pinned git-cliff. |
| `scripts/bootstrap/install-glab.sh` | Installs the pinned GitLab CLI. |
| `scripts/bootstrap/install-mise.sh` | Installs the pinned mise. |
| `scripts/bootstrap/install-publiccode-parser.sh` | Installs the pinned publiccode-parser-go CLI. |
| `scripts/bootstrap/install-syft.sh` | Installs the pinned Syft SBOM generator. |
| `scripts/bootstrap/install-trivy.sh` | Installs the pinned Trivy vulnerability scanner. |
| `scripts/bootstrap/install-yq.sh` | Installs the pinned Mike Farah yq. |

## `scripts/ci/`

This repository's own CI, release and live-tier glue. None of it is a consumer
contract.

| Path | Purpose |
|------|---------|
| `scripts/ci/prepare-live.sh` | Prepares one commit's inputs once in a directory: the images and demo checkouts the cases of the Go tables run on with `just test-live-prepared`. Behind `just test-live-prepare`; see [live runs](live-runs.md#prepare-and-run). |
| `scripts/ci/canonical-cli-delivery.sh` | Protected canonical delivery glue for `canonical-cli-bootstrap.yml`; transport is the pinned native Actions upload/download pair. |
| `scripts/ci/canonical-consumer.sh` | Receiver glue for the `consume-canonical-cli` action, run from a fresh administrator-pinned engine checkout; see [GitHub](github.md#images-and-the-engine-binary). |
| `scripts/ci/demo-checkout.sh` | Makes a built-in demo, `test/live/fixtures/consumers/<name>/consumer`, a one-commit repository for a consumer trial. Behind `just demo`. |
| `scripts/ci/image-build-settings.sh` | Sourced by the image-building recipes (`just test-live-image`, `just runtime-image-archive`, `just buildah-job-image-archive`): turns their optional resource settings into build arguments; Go does the validation. |
| `scripts/ci/engine-binary.sh` | The shared delivery-binary build, checksum and publisher: behind `just refresh-engine-binary`, the recorded-build guard, and Forgejo/GitLab generic package publication. See [GitLab bootstrap](gitlab.md#checksum-bound-engine-bootstrap) for GitLab coordinates and token types. |
| `scripts/ci/publish-live-image.sh` | Builds and publishes the signer or kaniko job image to the configured Forge Lab registry for authorized live-tier runs; see [live runs](live-runs.md). |
| `scripts/ci/run-github-cells.sh` | Runs the hosted GitHub cells of a run set, up to three at once and never two of one demo, the engine prepared once and reused. Behind `just test-live-github-cells`; see [live runs](live-runs.md#hosted-github-cells). |
| `scripts/ci/run-live-tests.sh` | The live conformance tier's entry, behind `just test-live` and `just test-live-consumer forgejo`. |
| `scripts/ci/runtime-evidence.sh` | Uploads a runtime image's evidence directory under its artifact name and retention; called by `runtime-images.sh`. |
| `scripts/ci/runtime-images.sh` | Runtime image publication orchestration for `.forgejo/workflows/runtime-images.yml`: binds the runtime roster to the source commit, registry namespace, run and artifact names. |
| `scripts/ci/runtime-smoke.sh` | Smoke test run inside a candidate runtime image, without publication credentials; the caller passes the Go version from the source Containerfile. |
| `scripts/ci/sign-reproducible-release.sh` | Run by the GitHub release workflows after GoReleaser and before signing: compares the payload with the final-tag rebuild, validates the checksums, then runs the given `forge-ci release sign` command. |
| `scripts/ci/test-clean-build.sh` | Regression for `just clean-build`'s refusal of overridden and linked output directories; included in `just test-unit`. |
| `scripts/ci/trusted-bootstrap.sh` | Key-free trusted leaf exec glue, invoked from an administrator-pinned copy before consumer checkout or secrets; see [engine delivery](engine-delivery.md#bootstrap). |
| `scripts/ci/trusted-runtime-tools.sh` | Places the approved runtime Containerfile's fixed tool directories for the admitted immutable engine; see [engine delivery](engine-delivery.md#tool-profiles). |
| `scripts/ci/verify-reproducible.sh` | Rebuilds the release twice and compares the archives with `forge-ci release files compare`; behind `just verify-reproducible`, and run directly with `--compare` or `--release-reference` (see [verification](verification.md)). |

## `scripts/runner-lender/`

| Path | Purpose |
|------|---------|
| `scripts/runner-lender/lend-forgejo-runner.sh` | Lends this machine to the orchestrating forge for exactly one job as an ephemeral forgejo-runner; see [runners](runners.md). |

## `examples/`

Scripts a consumer copies with an example.

| Path | Purpose |
|------|---------|
| `examples/base-images/base-plan.sh` | Prints the frozen plan of one stage of base images for `base-images.yml`. |
| `examples/base-images/gitlab/check-release.sh`, `examples/monorepo/gitlab/check-release.sh` | Merge-request and release-candidate checks, run by Forge CI's GitLab check job; copied to `.gitlab/`. |
| `examples/base-images/gitlab/build-release.sh`, `examples/monorepo/gitlab/build-release.sh` | Release build in a key-free GitLab job; leaves the unsigned hand-off in `dist/`. |

## Embedded in the binary

The Go code embeds these with `//go:embed`; they run inside jobs, not from a
checkout.

| Path | Purpose |
|------|---------|
| `internal/app/container/github-kaniko-action.sh` | Entrypoint of the disposable GitHub Docker action that runs a kaniko image, extract or snapshot build through the CLI. |
| `test/live/internal/livetest/probes/credential-setup-forgejo.sh` | Live-tier in-runner probe prelude: takes the Forgejo OIDC token endpoint out of the environment, failing when none was injected. |
| `test/live/internal/livetest/probes/credential-setup-gitlab.sh` | Live-tier in-runner probe prelude: takes the GitLab `SIGSTORE_ID_TOKEN` out of the environment, failing when none was minted. |

## `test/live/` fixtures

Consumer-side scripts of the live tier's cases and demos. They run in trial
consumer repositories, not in this repository's CI.

| Path | Purpose |
|------|---------|
| `test/live/fixtures/services/npm-rest-service/installed-oracle.sh` | Installs the retained npm package into a scratch prefix and checks the installed CLI; set as the case's oracle in `operator.env`. |
| `test/live/fixtures/development/rust-development/consumer/ci/build.sh` | Tests and builds the fixture's static Rust binaries offline and checks their versions. |
| `test/live/fixtures/development/rust-development/consumer/ci/produce.sh` | The native provider job's development build: runs `build.sh`, builds and scans the images and writes `development-artifacts/`. |
| `test/live/fixtures/development/rust-development/consumer/ci/receipt.sh` | Runs the fixture's receipt example in `development-artifacts/`; called by `produce.sh`. |
| `test/live/fixtures/adapters/generic-cli/prepare.sh` | Operator-only: creates an unsigned disposable adapter snapshot; see its README. |
| `test/live/fixtures/adapters/generic-cli/check-release.sh` | The adapter's release checks for a Go or Cargo consumer. |
| `test/live/fixtures/adapters/generic-cli/build-release.sh` | The adapter's release build into `dist/` for a Go or Cargo consumer. |
| `test/live/fixtures/adapters/generic-cli/verify-rust-job.sh` | Checks the Rust job image's user, toolchain and tools; installed into the image by `rust-job.Containerfile`. |

Each built-in demo with a GitLab release carries the same small set of GitLab
release scripts under `test/live/fixtures/consumers/<name>/consumer/.gitlab/`:

| Script | Purpose | Demos |
|--------|---------|-------|
| `check-release.sh` | Merge-request and release-candidate checks, run by Forge CI's check job. | every demo with a GitLab release |
| `build-release.sh` | Release build in a key-free job; leaves the unsigned hand-off in `dist/`. | every demo with a GitLab release |
| `export-release.sh` | Per-platform binary export from the Containerfile into `dist/`, run by the release policy's export jobs. | `hello-cargo-container`, `hello-cargo-workspace` |

A few demos carry scripts of their own in the consumer root:

| Path | Purpose |
|------|---------|
| `hello-base-images/consumer/base-plan.sh` | A copy of the example's `base-plan.sh`. |
| `hello-maven-reactor/consumer/run-app.sh` | Runs the application jar as a user of the release does; every forge's gate calls it after the build. |
| `hello-meta/consumer/install.sh` | The demo's Git-distributed script: `sh install.sh` runs it; sources `lib/greeting.sh`. |
| `hello-meta/consumer/lib/greeting.sh` | A shell library that `install.sh` sources. |

The `gradlew` files in the Gradle demos are Gradle's own wrapper and are not
listed here.
