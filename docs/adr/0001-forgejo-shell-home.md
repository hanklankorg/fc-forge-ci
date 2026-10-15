# ADR 0001: Provider delivery layers live with the engine

Status: Accepted

## Decision

Forge CI owns its CLI and all three provider delivery layers in one repository:

- GitHub reusable workflows and actions in `.github/`.
- GitLab CI/CD Catalog components in `templates/` and orchestration in `.gitlab/`.
- Forgejo reusable workflows, actions and binary resolver in `.forgejo/`.

A command change and the delivery code that calls it can therefore be reviewed
and released together. The signer image definition lives in `containers/signer/`.

Organization-specific policy remains caller-owned: trusted keys and digests,
signer-image coordinates, bot identity, repository protections and independent
revision approval. Forge CI supplies no organization key or signer-image default.

## Binary and trust boundary

The delivery layers run a build of their selected source revision. The tree
records its SHA-256 rather than committing an executable. Jobs download the
matching package and verify it before execution; a checksum identifies bytes,
while independent source authentication establishes trust in the revision.

Forgejo workflows and actions use the caller's `forge-ci-repository` input or
`FORGE_CI_REPOSITORY` variable to select the serving repository. GitLab components
must be hosted on the consumer's GitLab instance and selected by full commit SHA.

## Verification

`test/forgejoguard`, `test/forgejobehavior`, and `test/gitlabbehavior` cover the
delivery contracts and shell behavior. `internal/livetest` exercises real
provider/runner behavior through the guarded disposable-lab launcher.

See [Forgejo](../forgejo.md), [GitLab](../gitlab.md), and
[providers](../providers.md) for current setup and capability requirements.
