# Go service example

A Go service shipped as binaries and a container image. GitHub uses the
`container-first` Go path: the Containerfile is the build environment, and
`sbom-go.yml` emits the Build SBOM alongside publication. Forgejo builds
versioned binaries first and assembles images from them in separate native jobs.

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
  image from [`forgejo/Containerfile.example`](forgejo/Containerfile.example)
  using the prebuilt binaries and `release-images.yml`.
  [`forgejo/snapshot.yml`](forgejo/snapshot.yml) builds a container-only
  snapshot on every push to `main`, or when run by hand. Commit `mise.lock`
  for the runner platforms to use the locked toolchain setup. Images default
  to linux/amd64; arm64 requires a native runner and
  `RELEASE_PLATFORMS=linux/amd64,linux/arm64`. See the
  [Forgejo guide](../../docs/forgejo.md#getting-started).

## Files

- `artifacts.yml` declares `project-type: go` with `config.build-mode: container-first`. The GitHub release reads it.
- `Containerfile.example` shows the container-first multi-stage pattern: `builder`, `export-binary`, and `runtime`. Copy it to `Containerfile` for the GitHub path.
- `forgejo/Containerfile.example` copies the versioned binaries into a runtime image. Copy it to `Containerfile.forgejo` and match its binary name to `ARTIFACT_NAME` in the workflow. Its build context must include `dist/linux-*/`: if your `.containerignore` excludes `dist/`, add `!dist/linux-*/` after that line.
- `artifacts-forgejo.yml` declares those images without extraction. Copy it to `.forge-ci/images.yml` for the Forgejo release.
- `github/release-workflow.yml` calls the release orchestrator; the container is published by `publish-container.yml`.
- `github/pullrequest-workflow.yml` runs forge-ci PR checks and, in its own `test` job, `go vet` and `go test`; `govulncheck` and any other policy are yours to add there.

## Containerfile Pattern

Buildah runs one native build per requested platform and exposes `TARGETOS` /
`TARGETARCH` to the Containerfile, so `linux/amd64` and `linux/arm64` compile on
split GitHub-hosted runners without emulation. The optional `extract.binary`
block reuses the same builder stage to upload `${name}-binaries-${arch}`
artifacts that can be attached to a GitHub Release.
