# Container snapshots

A container-only example: the image contains `hello.txt` and has no executable.
No ecosystem build or package publication is needed. Declaring the container
enables snapshot publication by default, with the scan severity and image SBOM
settings of the declaration; the example does not disable security checks.

## GitHub

Copy `artifacts.yml` to `.forge-ci/artifacts.yml`, `Containerfile` and `hello.txt`
to the repository root, and `github/release-snapshot-workflow.yml` to
`.github/workflows/`. Allow the GitHub token to publish packages to GHCR.
The default image repository is `ghcr.io/<owner>/<repository>`.

**Container snapshot** runs on push to `develop` and `feature/**`, or manually
with any developer branch in `source-branch`, for example `feature/login`. An
empty input uses the workflow's selected branch. The `container-images-json`
output lists each image's source, platforms and digest.

Set `publish-containers: false` under `with:` to turn publishing off. For
another registry, set `container-image-repository` (for example
`registry.example.com/example-org/hello`), `container-registry-user`, and secret
`container-registry-password`.

## Forgejo and GitLab

The same files work with the
[Forgejo snapshot workflow](../../docs/forgejo.md#snapshots) and the
[GitLab snapshot component](../../docs/gitlab.md#snapshots). Pin them as those
guides describe.

## Containers built from an artifact

A container that `from:`s a Go, Cargo, Maven, Gradle JVM or npm artifact gets
that artifact built first; see
[automatic artifact-first inputs](../../docs/workflows.md#automatic-artifact-first-inputs).

## Image tags

A snapshot tag looks like `1.2.3-feature-login-<branchhash>-a1b2c3d-snapshot`.
Snapshots are unsigned and never move stable tags or promotion stages. They read
the release's `artifacts.yml` without its `sign` block and `enable-slsa`; see
[what a snapshot takes from the declaration](../../docs/workflows.md#what-a-snapshot-takes-from-the-declaration).
Build secrets are not supported in snapshots. More in the
[shared snapshot contract](../../docs/workflows.md#snapshot-container-contract-all-three-forges).
