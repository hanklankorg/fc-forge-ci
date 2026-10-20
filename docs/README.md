# Documentation Index

This index separates end-user documentation by purpose. The examples and docs
target the 0.90.0 Go-backed workflow contract.

> **Unpublished 0.90.0:** `v0.90.0` and its matching runtime images do not exist yet.
> Treat `@v0.90.0` snippets as forward-looking; for pre-release testing, follow
> the reviewed-branch procedure in [Runtime Images](runtime-images.md) instead
> of substituting an unreviewed moving ref in production.

## Tutorials

Learn by starting from a working setup.

- [Examples](../examples/README.md) - Start from a working Maven, NPM, Gradle, Android, Go, Cargo, or monorepo setup.

## How-To Guides

Solve a specific task.

- [Publishing](publishing.md) - Configure Maven Central, GitHub Packages, containers, App Store Connect, and Google Play.
- [Gradle publishing](gradle-publishing.md) - Export JVM publications, sign files and observe Central deployment state.
- [Private dependencies](private-dependencies.md) - Opt into scoped dependency-read credentials for npm, Maven and Gradle.
- [Runtime Images](runtime-images.md) - Override runtime images for reproducibility, mirroring, or branch testing.
- [Verification](verification.md) - Verify release assets, checksums, signatures, attestations, and SBOMs.
- [SBOM](sbom.md) - Configure SBOM layers and understand release/snapshot defaults.
- [GitHub](github.md) - The GitHub reusable workflows: adoption, inputs, secrets, runtime images, PR gates, releases and promotion, snapshots, signing, Code Scanning, forks, troubleshooting and limits.
- [GitLab](gitlab.md) - The GitLab components, in the same section order: Catalog and checksum-bound components, the native release policy, signing, SAST reports, forks, troubleshooting, limits and live acceptance.
- [Forgejo](forgejo.md) - The Forgejo delivery layer, in the same section order: calling it, the signer image and engine binary, the signing boundary, using it from a fork, limits; and the CLI on a Forgejo runner.
- [Threat Model](threat-model.md) - What forge-ci defends against, what it deliberately doesn't, and where adopter controls layer on top.
- [Failed Release Recovery](failed-release-recovery.md) - Safe reruns, immutable-tag rules, cleanup, and rollback.

## Reference

Look up exact fields, defaults, commands, and requirements.

- [Artifacts Reference](artifacts-reference.md) - Complete `artifacts.yml` schema, defaults, and validation constraints.
- [Components Reference](components.md) - Reusable workflow components and direct-use caveats.
- [CLI Reference](cli-reference.md) - Generated `forge-ci` command surface.
- [Project Reference](project.md) - Repository, CLI, configuration, versioning and release readiness.
- [Secrets, Permissions, and Validation](reference.md) - Required secrets, permissions, and validation matrices.

## Explanation

Understand the design and tradeoffs.

- [Artifact and Image Flows](flows.md) - How a declared artifact becomes a released one: run artifacts between jobs, and the image ledger from build to signature to final tag. Starts jargon-free and layers up.
- [Workflow Architecture](workflows.md) - Orchestrator, stage, and component interaction model.
- [Ecosystem Support](ecosystems.md) - Artifact-first vs container-first ecosystem model and current support status.
- [Providers and Runners](providers.md) - Forge detection, the runner/forge-API axes, overrides, and the per-forge capability matrix (GitHub, GitLab, Forgejo, local).
- [CLI Black Box](cli-black-box.md) - The CLI-as-black-box contract between workflows and the binary.

### Architecture Decisions

Current architectural decisions and the constraints future changes must retain.

- [ADR 0001: Provider workflow shell boundary](adr/0001-forgejo-shell-home.md)
- [ADR 0002: Signer trust boundary](adr/0002-signer-trust-boundary.md)
- [ADR 0003: CLI verb lexicon](adr/0003-cli-verb-lexicon.md) - the bar a guard test has to clear to earn its keep.
- [ADR 0004: Package layering](adr/0004-package-layering.md) - the hexagonal layering, and the guard that enforces it.

## Maintainer Docs

- [Development](DEVELOPMENT.md) - Local development, testing, and branch verification for this repository.
- [Testing](testing.md) - Go test helpers and testing conventions.
- [Consumer Migration Handoff](consumer-migration-handoff.md) - DiggSweden trial results, fix ownership, remaining acceptance, cleanup and upstream adoption checklist.
- [Consumer Validation Lessons](consumer-validation-lessons.md) - Reusable invariants and regressions from native consumer trials.
- [Scripts](scripts.md) - Remaining bootstrap scripts and the workflow surface moved to the Go binary.
- [Workflow Design Policy](workflow-design-policy.md) - Design rules for maintainers extending the workflow graph.
- [Open Questions](open-questions.md) - Review-question status and links to the durable policy decisions.
