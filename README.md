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

Install the CLI, let it write your repository's CI files, then follow
`forge-ci setup status` to a first release.

### Install

```bash
mise use -g github:diggsweden/forge-ci@0.93.1
forge-ci --version
```

This installs the released CLI globally; no Go toolchain is required.
Alternatively, download the archive for your platform from the
[release page](https://github.com/diggsweden/forge-ci/releases/tag/v0.93.1)
and [verify it](docs/verification.md#verify-the-forge-ci-cli-before-you-install-it)
before installation.
Source builds are covered in [Development](docs/DEVELOPMENT.md).

### Write the CI files

From your project's root, preview what `init` would write, then write it:

```bash
forge-ci init https://github.com/diggsweden/forge-ci --dry-run
forge-ci init https://github.com/diggsweden/forge-ci
```

The URL is the **Forge CI template repository**, not your application's
remote; use your organization's fork or mirror instead when it has one. It
decides the generated CI layout: `init` does not infer the forge from your
application's Git remote.

| Your forge | Template source | Generated CI location |
|---|---|---|
| GitHub | A Forge CI repository on `github.com` | `.github/workflows/` |
| GitLab | Your instance's Forge CI mirror, declaring `forge: gitlab` | `.gitlab-ci.yml` |
| Forgejo | Your instance's Forge CI mirror, declaring `forge: forgejo` | `.forgejo/workflows/` |

Outside `github.com`, the mirror's operator must put that `forge` value in
`.forge-ci/consumer-defaults.yml` at the selected release tag; the
[GitLab](docs/gitlab.md#getting-started) and
[Forgejo](docs/forgejo.md#getting-started) guides say how. Without it,
initialization refuses rather than guessing from the hostname.

The preview picks an example from your build files, pins the newest release,
and lists the files it would create, any existing-file conflicts, and the
values left for you to fill in. Existing files are never replaced. Use
`--example` to choose another example and `--ref` to select another release.
`init` writes no linter configuration; Nanolinter's defaults and the required
lint floor apply until you add a `nanolinter.toml`.
[Your first pull-request check](docs/tutorial-first-pull-request.md) walks
through this step by step.

### Follow the steps to a first release

```bash
forge-ci setup status
```

It lists every step from no Forge CI files to a first release, marks the ones
the repository shows are done, and names the command for each:

```text
[x] 1. Write the Forge CI files
[ ] 2. Fill in the example values
       run: forge-ci doctor   (lists each value with file:line and how to find it)
[ ] 3. List who may sign releases
       run: forge-ci setup signer --principal <your email> --public-key ~/.ssh/id_ed25519.pub   (and again for the release bot)
[?] 4. Create the release secrets
       run: forge-ci setup secrets
...
[?] 9. Request the release
       run: forge-ci setup release-request v0.1.0   (checks your signing setup, prints the git commands)
```

`[?]` marks steps the repository cannot show, such as secrets stored on the
forge; check those yourself. Run `setup status` again after each step.

A release starts when you push a signed `release-request/vX.Y.Z` tag. The
release workflow validates it, then creates the final `vX.Y.Z` tag itself;
never create or push the final tag yourself. Two kinds of keys are involved:
your own key signs the request tag, and the release bot's keys, stored as
`RELEASE_*` secrets, sign the release commit, the final tag and the artifacts.
The allowed-signers file `setup signer` writes must list both.

### Without init

The [examples](examples/README.md) hold the complete caller files for each
ecosystem and forge; copy one and replace every placeholder its header lists.
The [GitHub](docs/github.md#getting-started),
[GitLab](docs/gitlab.md#getting-started) and
[Forgejo](docs/forgejo.md#getting-started) guides describe this route.
`forge-ci doctor` and `forge-ci setup status` work the same on hand-copied
files.

## Using Forge CI

Start with the [Documentation Index](docs/README.md) for the full map.

- **Tutorial** • [Your first pull-request check](docs/tutorial-first-pull-request.md),
  from a bare repository to a checked pull request
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
  [Secrets, Permissions, and Validation](docs/reference.md)
- **Concepts** • [Workflow Architecture](docs/workflows.md) ·
  [Artifact and Image Flows](docs/flows.md) ·
  [Ecosystems](docs/ecosystems.md) ·
  [Providers and Runners](docs/providers.md) ·
  [CLI Black Box](docs/cli-black-box.md) ·
  [Threat Model](docs/threat-model.md)

## Working on Forge CI

This repository is enough to build the engine and run its unit tests. External
compatibility checks also need
[forge-ci-blackbox-tests](https://github.com/diggsweden/forge-ci-blackbox-tests),
which runs the real binary against fixtures and toolchains, with no forge
involved. Live consumer trials run real pull requests and releases on local
GitLab and Forgejo instances and on hosted GitHub, and additionally need a
Forge Lab; see [Live runs](docs/live-runs.md).

- **[Development](docs/DEVELOPMENT.md)** • Source checkout, installing the CLI
  from it, prerequisites, checks and branch testing
- **[Testing](docs/testing.md)** • Test helpers and conventions
- **[Black-box suite running guide][blackbox-running]** • Local, strict and
  focused compatibility runs
- **[Workflow Design Policy](docs/workflow-design-policy.md)** ·
  **[Scripts](docs/scripts.md)** ·
  **[Architecture Decisions](docs/README.md#architecture-decisions)**
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

[blackbox-running]: https://github.com/diggsweden/forge-ci-blackbox-tests/blob/main/docs/running.md
