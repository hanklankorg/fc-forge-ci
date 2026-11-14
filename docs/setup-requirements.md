<!-- SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government -->
<!-- SPDX-License-Identifier: CC0-1.0 -->

# Release setup requirements

Run `forge-ci doctor --requirements` after initialization to see the credential
names, caller mappings and public settings used by known release entry points.
It reads local files only. It never reads secret values, generates keys, uploads
secrets or contacts a forge. Normal doctor checks and exit codes are unchanged.

For purposes, credential issuers, job/environment scopes and unused settings:

```sh
forge-ci --json doctor --requirements
```

## What the report covers

The report recognizes the GitHub release orchestrator, the Forgejo release
preparation and signing workflows, and a direct include of GitLab's `release`
component. `--forge-ci-repository owner/repository` names a mirror's
repository. It does not expand indirect GitLab includes or custom jobs, and
it warns instead of evaluating expressions, aliases or malformed YAML. It
does not cover the mobile publishers or backend-specific KMS authentication;
see [publishing](publishing.md) and
[setting up secrets](quickstart-setup-secrets.md).

The rules come from the CLI you run. When the CLI's own commit matches the
workflow's pin, the report calls them revision-matched; otherwise they are
reference guidance, marked unverified, and you check the pinned release's
documentation. On GitHub `forge-ci-binary-ref` must equal the orchestrator
pin too, and on Forgejo `forge-ci-sha` must equal the workflow pin;
otherwise the contract is marked unverified.

GitHub container `build-secrets` need the caller's
`FORGE_CI_BUILD_SECRETS_JSON` mapping; the report lists the name, never the
values. An empty Forgejo `forge-ci-repository` falls back to
`vars.FORGE_CI_REPOSITORY`, which the report names without checking that it
is set.

In the JSON, `complete` means every recognized call was covered, not that the
setup works. `unknowns` lists what the report could not cover, and `notes`
explains conditional requirements, such as images found only in a runtime
ledger. A credential listed as required may well be set already: the report
never reads remote secrets.

## Configure credentials at their actual destination

- Keep source-forge API credentials separate from external registry credentials.
  A Forgejo repository publishing to GHCR needs different authorities for those
  operations. A registry token's scopes do not grant its account package access.
- Pass Actions secrets explicitly. The report recognizes direct
  `${{ secrets.NAME }}` mappings, including renamed caller secrets; it does not
  infer values from `secrets: inherit` or arbitrary expressions.
- GitLab's key-based release component uses protected project variables with
  environment scopes. Follow [the GitLab contract](gitlab.md#secrets-and-tokens),
  not a global all-jobs variable group. It needs `RELEASE_REGISTRY_TOKEN` only
  when it publishes images: `release-images: true` or a `base-stages` graph.
  An archive-only release needs no registry token.
- Public keys and fingerprints are public configuration, although some workflows
  transport them through secret inputs. Trust digests belong in reviewed code.
  Never replace a trust root merely to resolve a key mismatch.

See [signing-key setup](quickstart-setup-secrets.md),
[Forgejo secrets](forgejo.md#secrets-and-tokens),
[GitHub permissions](reference.md), and [publisher credentials](publishing.md).
The report does not check runner inputs. A release plan is refused at plan
time when a platform or macOS job it includes has no runner; see
[A planned platform needs its runner](runners.md#a-planned-platform-needs-its-runner).
Local checks cannot establish runner isolation, protected-branch access, package
visibility or successful publication; the release workflow's preflight jobs
check some of these when the release runs.
