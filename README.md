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

## Quick Start

Install the CLI, then add pull/merge-request checks and release configuration
for your forge. GitHub, GitLab and Forgejo use their own CI syntax.

### Install

```bash
mise use -g github:diggsweden/forge-ci@0.91.13
forge-ci --version
```

This installs the released CLI globally; no Go toolchain is required.
Alternatively, download the archive for your platform from the
[release page](https://github.com/diggsweden/forge-ci/releases/tag/v0.91.13)
and follow [Artifact Verification](docs/verification.md) before installation.
Source builds are covered in [Development](docs/DEVELOPMENT.md).

`forge-ci init <repo-url>` writes a project's first CI files;
`forge-ci doctor` checks local configuration.

### Choose your forge and preview initialization

The URL is the **Forge CI template repository**, not your application's remote.
It also determines the generated CI layout: `init` does not infer the forge from
your application's Git remote or a global `--provider` selection.

| Your forge | Template source | Generated CI location |
|---|---|---|
| GitHub | A Forge CI repository on `github.com` | `.github/workflows/` |
| GitLab | Your instance's Forge CI mirror, declaring `forge: gitlab` | `.gitlab-ci.yml` |
| Forgejo | Your instance's Forge CI mirror, declaring `forge: forgejo` | `.forgejo/workflows/` |

Outside `github.com`, the mirror's operator must put that `forge` value in
`.forge-ci/consumer-defaults.yml` at the selected release tag.
Without it, initialization refuses rather than guessing from the hostname.

From your project directory, choose **one** of the following previews.
Replace the example URL with the published template repository your
organization uses:

```bash
# GitHub
forge-ci init https://github.com/YOUR-ORG/forge-ci --dry-run

# GitLab — mirror defaults must declare forge: gitlab
forge-ci init https://gitlab.example.com/team/forge-ci --dry-run

# Forgejo — mirror defaults must declare forge: forgejo
forge-ci init https://forgejo.example.com/team/forge-ci --dry-run
```

The preview reports the target directory, resolved release and commit, selected
example, proposed files, existing-file conflicts, and remaining placeholders.
Check that its reported forge and proposed file locations match your project.
It fetches templates into temporary storage but writes no project files.
Conflicts return a nonzero status; existing files are never replaced.
Use `--example` to choose an example and `--ref` to select an available release.
After review, repeat without `--dry-run`, retaining the reported release and example.
Preview and generation fetch the source independently; preview is not a saved
execution plan.

Linter configuration files are optional customizations: init does not create
or modify Nanolinter or MegaLinter configs. The `lint-engine` input selects the
engine (Nanolinter by default in the GitHub PR workflow); without a config file,
the selected engine uses its defaults and the workflow's required lint floor
still applies. The [examples](examples/README.md) include configs you can copy
to customize those defaults.
Nanolinter performs its own lookup: root `nanolinter.toml`, then
`development/nanolinter.toml`, then built-in defaults.

### GitHub: Pull-request checks

The following manual examples are for GitHub only.
Use the [GitLab guide](docs/gitlab.md#getting-started) or
[Forgejo guide](docs/forgejo.md#getting-started) for those CI systems.
Replace `diggsweden/forge-ci` in the GitHub examples with your selected template
repository and pin an available release consistently.

One caller workflow runs lint and security scans on every PR. Add your
own test job alongside it:

```yaml
# .github/workflows/pr.yml
on:
  pull_request:
permissions:
  contents: read
jobs:
  pr-checks:
    uses: diggsweden/forge-ci/.github/workflows/pullrequest-orchestrator.yml@v0.91.13
    permissions:
      actions: read
      contents: read
    with:
      forge-ci-binary-ref: v0.91.13
      project-type: go  # maven, npm, gradle, cargo, ...
```

### GitHub: Release

Push a signed tag — the flow builds, signs, publishes and creates the
final version tag:

```yaml
# .github/workflows/release.yml
on:
  push:
    tags: ["release-request/v*"]
permissions:
  contents: read
jobs:
  release:
    uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@v0.91.13
    permissions:
      contents: write
      packages: write
      id-token: write  # Sigstore keyless signing
      actions: read
    secrets:
      RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}
      RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
    with:
      branch: main
      forge-ci-binary-ref: v0.91.13
```

```bash
git tag -s release-request/v1.0.0 -m "Release v1.0.0"
git push origin release-request/v1.0.0
```

GitLab and Forgejo use the same two flows through their own CI syntax —
see the [examples](examples/README.md) for complete per-forge files.

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
- **[Black-box suite running guide][blackbox-running]** • Local, strict and
  focused compatibility runs
- **[Workflow Design Policy](docs/workflow-design-policy.md)** ·
  **[Scripts](docs/scripts.md)** ·
  **[Architecture Decisions](docs/README.md#architecture-decisions)** ·
  **[Open Questions](docs/open-questions.md)**
- **[Security](SECURITY.md)** • How to report a vulnerability
- **[Changelog](CHANGELOG.md)**

## License

[blackbox-running]: https://github.com/diggsweden/forge-ci-blackbox-tests/blob/main/docs/running.md

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
