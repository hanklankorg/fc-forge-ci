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

## Pull/merge-request gate coverage

| Forge | Included in this starter | What the project adds |
|---|---|---|
| GitHub | Lint and security checks | A test job for `go test`, `go vet`, and the project's vulnerability policy; require it alongside the lint gate |
| GitLab | Lint, `go vet`, `go test`, then a cross-platform build and Build SBOM | Any additional project-specific checks; tests run once in the `test` stage |
| Forgejo | Lint, release-isolation validation, `go test`, and a build without a Build SBOM | Any additional project-specific checks |

The starters intentionally use each forge's native job composition. A successful
starter pipeline proves only the checks listed above.

## Files

- `artifacts.yml` declares `project-type: go` with `config.build-mode: artifact-first`. The GitHub release reads it; the GitLab and Forgejo files select platforms in their build step.
- `github/release-workflow.yml` calls the release orchestrator and attaches `release-artifacts/*/my-cli-*` to the GitHub Release.
- `github/pullrequest-workflow.yml` runs forge-ci PR checks and leaves `go test`, `go vet`, and `govulncheck` to a caller-owned test workflow.

## Output Layout

The GitHub release and GitLab build examples select these binary targets:

```text
dist/linux-amd64/my-cli-linux-amd64
dist/linux-arm64/my-cli-linux-arm64
dist/darwin-amd64/my-cli-darwin-amd64
dist/darwin-arm64/my-cli-darwin-arm64
```

Set `config.platforms` in `artifacts.yml` for the GitHub release. For GitLab,
set the `platforms` input on `build-go` (and in the separate release policy's
build configuration); for Forgejo, set `platforms` on the release's `build-go`
action. The Forgejo release example selects only Linux amd64 and arm64; add
`darwin/amd64,darwin/arm64` there to match the four-target examples above.

When calling the builders directly, their defaults differ:

| Input | GitHub | GitLab | Forgejo |
|---|---|---|---|
| `platforms` | Linux and macOS, amd64 and arm64 | Linux, amd64 and arm64 | Linux amd64 |
| `main-package` | `.` | `.` | `.` |
| `skip-tests` | `false` | `false` | `'false'` |
| `enable-build-sbom` | `true` | `true` | `'true'` |

Select `platforms` explicitly when moving a build between forges. Use
`main-package: ./cmd/my-cli` for a command below the module root; it is relative
to `working-directory`. Forgejo action booleans are string inputs.

GitHub release creation downloads the build artifact contents under
`release-artifacts/<goos>-<goarch>/`. Filenames include the platform suffix so
GitHub Release assets have unique basenames.
