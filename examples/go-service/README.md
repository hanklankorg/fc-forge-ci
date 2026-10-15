# Go service example

A Go service shipped as a multi-arch container image. This is the
`container-first` Go path: the Containerfile is the build environment, and
`sbom-go.yml` emits the Build SBOM from `go.mod` / `go.sum` alongside the
container publish stage.

## Per forge

- **GitHub**: [`github/pullrequest-workflow.yml`](github/pullrequest-workflow.yml)
  and [`github/release-workflow.yml`](github/release-workflow.yml), with
  `artifacts.yml`. See the [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml), the lint gate,
  `check` and `build-go`. No component builds the image: in a release the
  policy project's `build` commands do. See the
  [GitLab guide](../../docs/gitlab.md#getting-started).
- **Forgejo**: [`forgejo/pullrequest.yml`](forgejo/pullrequest.yml) and
  [`forgejo/release.yml`](forgejo/release.yml), the service binaries and one
  multi-arch image from `Containerfile.example` with `build-release-image`. See
  the [Forgejo guide](../../docs/forgejo.md#getting-started).

## Files

- `artifacts.yml` declares `project-type: go` with `config.build-mode: container-first`. The GitHub release reads it.
- `Containerfile.example` shows the required multi-stage pattern: `builder`, `export-binary`, and `runtime`. Copy it to `Containerfile`; the Forgejo release builds its final `runtime` stage.
- `github/release-workflow.yml` calls the release orchestrator; the container is published by `publish-container.yml`.
- `github/pullrequest-workflow.yml` runs forge-ci PR checks and leaves `go test`, `go vet`, and `govulncheck` to a caller-owned test workflow.

## Containerfile Pattern

Buildah runs one native build per requested platform and exposes `TARGETOS` /
`TARGETARCH` to the Containerfile, so `linux/amd64` and `linux/arm64` compile on
split GitHub-hosted runners without emulation. The optional `extract.binary`
block reuses the same builder stage to upload `${name}-binaries-${arch}`
artifacts that can be attached to a GitHub Release.
