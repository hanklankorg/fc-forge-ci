# Documentation Index

This index separates end-user documentation by purpose.

> **Which version to pin:** the snippets in these guides show
> `PIN_FORGE_CI_SHA # v0.90.0` and zero image digests marked `FIXME`.
> `forge-ci init` and `forge-ci doctor` fill in or report the commit and digests
> of the release you install. By hand, pin the release's commit
> (`git rev-parse vX.Y.Z^{commit}`) and image digests as the
> [Advanced Pinning Recipe](runtime-images.md#advanced-pinning-recipe) shows.

New to Forge CI? Start with [Your first pull-request check](tutorial-first-pull-request.md).
Evaluating it? Read [Workflow Architecture](workflows.md) and the [Threat Model](threat-model.md).
Wiring a repository? Go to the [GitHub](github.md), [GitLab](gitlab.md) or [Forgejo](forgejo.md) guide.

## Tutorials

Learn by starting from a working setup.

- [Your first pull-request check](tutorial-first-pull-request.md) - From a bare
  Go repository on GitHub to a checked pull request with `init` and `doctor`,
  in about fifteen minutes.
- [Examples](../examples/README.md) - Start from a working Maven, NPM, Gradle, Android, Go, Cargo, or monorepo setup.

## How-To Guides

Solve a specific task.

- [Release signing secrets](quickstart-setup-secrets.md) - Linux key setup and
  hardware-backed/keyless signing guidance.
- [Release setup requirements](setup-requirements.md) - What `forge-ci doctor --requirements`
  reports: the credentials and settings your release workflows use.
- [Publishing](publishing.md) - Configure Maven Central, Gradle libraries, GitHub Packages, containers, App Store Connect, and Google Play.
- [Private dependencies](private-dependencies.md) - Opt into scoped dependency-read credentials for npm, Maven and Gradle.
- [Runtime Images](runtime-images.md) - Override runtime images for reproducibility, mirroring, or branch testing.
- [Verification](verification.md) - Verify release assets, checksums, signatures, attestations, and SBOMs.
- [Administrator check enforcement](admin-policy.md) - Admin-owned lint, vulnerability, application-test and reproducible-build enforcement, set at run time per forge, reporting and consumer boundaries.
- [SBOM](sbom.md) - Configure SBOM layers and understand release/snapshot defaults.
- [Checked multi-architecture images](multiarch-images.md) - Assemble and verify
  exact platform sets, then compose checksum-bound index SBOMs.
- [GitHub](github.md) - The GitHub reusable workflows: adoption, inputs, secrets, runtime images, PR gates, releases and promotion, snapshots, signing, Code Scanning, forks, troubleshooting and limits.
- [GitLab](gitlab.md) - The GitLab components: Catalog and checksum-bound components, the native release policy, signing, SAST reports, forks, troubleshooting and limits.
- [Forgejo](forgejo.md) - The Forgejo delivery layer: calling it, the signer image and engine binary, the signing boundary, using it from a fork, limits; and the CLI on a Forgejo runner.
- [Runners and runner lending](runners.md) - What each job kind needs from its runner, the runner variables, and borrowing an ephemeral runner for container builds when the pool denies user namespaces.
- [Failed Release Recovery](failed-release-recovery.md) - Safe reruns, immutable-tag rules, cleanup, and rollback.

## Reference

Look up exact fields, defaults, commands, and requirements.

- [Artifacts Reference](artifacts-reference.md) - Complete `artifacts.yml` schema, defaults, and validation constraints.
- [Components Reference](components.md) - Reusable workflow components and direct-use caveats.
- [CLI Reference](cli-reference.md) - Generated `forge-ci` command surface.
- [Secrets, Permissions, and Validation](reference.md) - Required secrets, permissions, and validation matrices.

## Explanation

Understand the design and tradeoffs.

- [Artifact and Image Flows](flows.md) - How a declared artifact becomes a released one: run artifacts between jobs, and the image ledger from build to signature to final tag.
- [Workflow Architecture](workflows.md) - Orchestrator, stage, and component interaction model.
- [Ecosystem Support](ecosystems.md) - Artifact-first vs container-first ecosystem model and what each ecosystem supports.
- [Providers and Runners](providers.md) - Forge detection, the runner/forge-API axes, overrides, the per-forge capability matrix (GitHub, GitLab, Forgejo, local), and what changes when a consumer moves forge.
- [Threat Model](threat-model.md) - What forge-ci defends against, what it deliberately doesn't, and where adopter controls layer on top.
- [CLI Black Box](cli-black-box.md) - The CLI-as-black-box contract between workflows and the binary.

### Architecture Decisions

Current architectural decisions and the constraints future changes must retain.

- [ADR 0001: Provider workflow shell boundary](adr/0001-forgejo-shell-home.md)
- [ADR 0002: Signer trust boundary](adr/0002-signer-trust-boundary.md)
- [ADR 0003: CLI verb lexicon](adr/0003-cli-verb-lexicon.md) - the shared verbs (`validate`, `plan`, `sign` and others) the CLI commands use.
- [ADR 0004: Package layering](adr/0004-package-layering.md) - the hexagonal layering, and the guard that enforces it.

## Maintainer Docs

- [Development](DEVELOPMENT.md) - Project identity, local development, testing, branch verification, and releasing forge-ci itself.
- [Testing](testing.md) - Test layers, Go test helpers, conventions and guards.
- [Testing Matrices](testing-matrices.md) - What each project kind and variant is proven to do live, per forge.
- [Live Runs](live-runs.md) - Plan, launch and retain evidence for resource-aware asynchronous live runs.
- [Scripts](scripts.md) - Every shell script in the repository, by directory, and what moved to the Go binary.
- [Engine Delivery](engine-delivery.md) - How the GitHub workflows build the engine binary, hand it to jobs and choose their tools.
- [Workflow Design Policy](workflow-design-policy.md) - Design rules for maintainers extending the workflow graph.
