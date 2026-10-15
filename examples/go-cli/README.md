# Go CLI example

A Go command-line tool released as standalone binaries. This is the
`artifact-first` Go path: `build-go.yml` compiles release binaries before the
publish stage.

## Per forge

- **GitHub**: [`github/pullrequest-workflow.yml`](github/pullrequest-workflow.yml)
  and [`github/release-workflow.yml`](github/release-workflow.yml), with
  `artifacts.yml`. See the [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml), the lint gate,
  `check` and `build-go`; the release runs from a separate policy project. See
  the [GitLab guide](../../docs/gitlab.md#getting-started).
- **Forgejo**: [`forgejo/pullrequest.yml`](forgejo/pullrequest.yml) and
  [`forgejo/release.yml`](forgejo/release.yml), a `build-go` binary release
  between the layer's prepare and signer workflows. See the
  [Forgejo guide](../../docs/forgejo.md#getting-started).

## Files

- `artifacts.yml` declares `project-type: go` with `config.build-mode: artifact-first`. The GitHub release reads it; the GitLab and Forgejo files set the same platforms in their build step.
- `github/release-workflow.yml` calls the release orchestrator and attaches `release-artifacts/*/my-cli-*` to the GitHub Release.
- `github/pullrequest-workflow.yml` runs forge-ci PR checks and leaves `go test`, `go vet`, and `govulncheck` to a caller-owned test workflow.

## Output Layout

`build-go.yml` writes binaries under:

```text
dist/linux-amd64/my-cli-linux-amd64
dist/linux-arm64/my-cli-linux-arm64
dist/darwin-amd64/my-cli-darwin-amd64
dist/darwin-arm64/my-cli-darwin-arm64
```

Set `config.platforms` to change the release target list.

GitHub release creation downloads the build artifact contents under
`release-artifacts/<goos>-<goarch>/`. Filenames include the platform suffix so
GitHub Release assets have unique basenames.
