# Forge CI

[![Tag](https://img.shields.io/github/v/tag/diggsweden/forge-ci?style=for-the-badge&color=green)](https://github.com/diggsweden/forge-ci/tags)

[![License: EUPL-1.2 OR GPL-3.0-or-later](https://img.shields.io/badge/License-EUPL--1.2%20OR%20GPL--3.0--or--later-blue?style=for-the-badge)](LICENSE)
[![REUSE](https://img.shields.io/badge/dynamic/json?url=https%3A%2F%2Fapi.reuse.software%2Fstatus%2Fgithub.com%2Fdiggsweden%2Fforge-ci&query=status&style=for-the-badge&label=REUSE&color=lightblue)](https://api.reuse.software/info/github.com/diggsweden/forge-ci)

[![OpenSSF Scorecard](https://img.shields.io/ossf-scorecard/github.com/diggsweden/forge-ci?label=openssf+scorecard&style=for-the-badge)](https://scorecard.dev/viewer/?uri=github.com/diggsweden/forge-ci)

Forge CI gives a project pull-request checks, builds, publishing and signed
releases on GitHub, GitLab and Forgejo, driven by one Go CLI, `forge-ci`.
You call its workflows from your repository instead of building SBOMs,
signing, SLSA provenance and pinned third-party actions yourself.

- One release flow: push a signed `release-request/vX.Y.Z` tag, and Forge CI
  validates it, creates the final `vX.Y.Z` tag, builds, publishes and signs.
- A single-manifest repository needs no configuration file; add
  `.forge-ci/artifacts.yml` only when you need more.

The first planned release is **0.90.0**, and it is not published yet. The
`@v0.90.0` pins in the docs are forward-looking; consumers wait for the tag
and its runtime images, and maintainers test unreleased changes with the
pre-release branch's `sha-*` images as described in
[Runtime Images](docs/runtime-images.md#branch-testing).
[Project Reference](docs/project.md) has names, versioning and release
readiness.

## Quick Start

On every forge a release starts from a signed `release-request/vX.Y.Z` tag,
and the flow creates `vX.Y.Z` once: never push the final tag yourself.
`forge-ci doctor` in a job shows what the detected forge supports.

On a laptop, `go install github.com/diggsweden/forge-ci/cmd/forge-ci@v0.90.0`
installs the CLI; `forge-ci doctor` then checks a repository's setup.

### GitHub

Copy the example's `github/` workflows into `.github/workflows/`. The
pull-request caller is one job:

```yaml
jobs:
  pr-checks:
    uses: diggsweden/forge-ci/.github/workflows/pullrequest-orchestrator.yml@v0.90.0
    permissions:
      actions: read
      contents: read
    with:
      forge-ci-binary-ref: v0.90.0
      project-type: maven  # or npm, gradle, gradle-android, xcode-ios, cargo, go
```

See [Getting started](docs/github.md#getting-started) and the
[examples](examples/README.md).

### GitLab

Include the components, hosted on your own instance, in `.gitlab-ci.yml`; the
example's `gitlab/.gitlab-ci.yml` is the merge-request gate, the lint
component and the `check` component:

```yaml
include:
  - component: gitlab.example.com/team/ci/forge-ci/nanolinter@0.90.0
    inputs:
      forge-ci-repository: team/ci/forge-ci
      nanolinter-image: "ghcr.io/itiquette/nanolinter:go@sha256:PIN_DIGEST"
  - component: gitlab.example.com/team/ci/forge-ci/check@PIN_FORGE_CI_SHA
    inputs:
      forge-ci-repository: team/ci/forge-ci
      job-image: registry.example.com/team/check-tools@sha256:PIN_IMAGE_DIGEST
      commands:
        - just test-unit
```

Releases run from a separate, pinned policy project that includes the
`release` component; once it is the project's CI configuration path, GitLab
runs it instead of `.gitlab-ci.yml` and the policy includes this gate for
merge requests.

See [Getting started](docs/gitlab.md#getting-started) and the
[examples](examples/README.md).

### Forgejo

Fill in the example's `forgejo/` workflows and add them to
`.forgejo/workflows/`. The release calls the layer, served on your instance,
around your own build job:

```yaml
jobs:
  prepare:
    uses: OWNER/forge-ci/.forgejo/workflows/prepare-release.yml@<commit>
```

See [Getting started](docs/forgejo.md#getting-started) and the
[examples](examples/README.md).

## Using Forge CI

Start with the [Documentation Index](docs/README.md) for the full map.

- **Examples** • [Ready-made pipelines](examples/README.md) for Go, Cargo,
  Maven, Gradle, npm, Android and a monorepo, on each forge that builds them
- **Forge guides** • [GitHub](docs/github.md) · [GitLab](docs/gitlab.md) ·
  [Forgejo](docs/forgejo.md), in the same section order
- **How-to** • [Publishing](docs/publishing.md) ·
  [Runtime Images](docs/runtime-images.md) ·
  [Artifact Verification](docs/verification.md) · [SBOM](docs/sbom.md) ·
  [Failed Release Recovery](docs/failed-release-recovery.md)
- **Reference** • [Artifacts Reference](docs/artifacts-reference.md) ·
  [Components Reference](docs/components.md) ·
  [CLI Reference](docs/cli-reference.md) ·
  [Secrets, Permissions, and Validation](docs/reference.md) ·
  [Project Reference](docs/project.md)
- **Concepts** • [Workflow Architecture](docs/workflows.md) ·
  [Artifact and Image Flows](docs/flows.md) ·
  [Ecosystems](docs/ecosystems.md) ·
  [Providers and Runners](docs/providers.md) ·
  [CLI Black Box](docs/cli-black-box.md) ·
  [Threat Model](docs/threat-model.md)

## Working on Forge CI

- **[Development](docs/DEVELOPMENT.md)** • Source checkout, installing the CLI
  from it, prerequisites, checks and branch testing
- **[Testing](docs/testing.md)** • Test helpers and conventions
- **[Workflow Design Policy](docs/workflow-design-policy.md)** ·
  **[Scripts](docs/scripts.md)** ·
  **[Architecture Decisions](docs/README.md#architecture-decisions)** ·
  **[Open Questions](docs/open-questions.md)**
- **[Security](SECURITY.md)** • How to report a vulnerability
- **[Changelog](CHANGELOG.md)**

## License

The `forge-ci` program, meaning the Go source, is dual-licensed
[EUPL-1.2](LICENSES/EUPL-1.2.txt) **OR**
[GPL-3.0-or-later](LICENSES/GPL-3.0-or-later.txt), at your option.

Everything you copy into your own repository is
[CC0-1.0](LICENSES/CC0-1.0.txt): the reusable workflows, the GitLab Catalog
components in `templates/`, the Forgejo workflows and actions in `.forgejo/`,
the shell scripts, and all configuration, documentation and examples. Lift them
freely. The shell files under `.forgejo/` are the exception: they carry the
program's licence.

See [LICENSE](LICENSE) for the full statement. Every file carries an SPDX
identifier, which is authoritative for that file.
