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
  [`gitlab/snapshot-files-policy.yml`](gitlab/snapshot-files-policy.yml) is
  the include that policy project holds to keep a branch's binaries as
  unsigned job artifacts for 7 days
  ([snapshot files](../../docs/workflows.md#snapshot-files)).
- **Forgejo**: [`forgejo/pullrequest.yml`](forgejo/pullrequest.yml) and
  [`forgejo/release.yml`](forgejo/release.yml), a `build-go` binary release
  between the layer's prepare and signer workflows. See the
  [Forgejo guide](../../docs/forgejo.md#getting-started).
  [`forgejo/snapshot.yml`](forgejo/snapshot.yml) builds and tests every
  pushed branch except `main`, without signing or publishing anything.
  [`forgejo/snapshot-files.yml`](forgejo/snapshot-files.yml), run by hand on
  a branch, also keeps its binaries as an unsigned run artifact for 7 days
  ([snapshot files](../../docs/workflows.md#snapshot-files)).

A Forgejo release can also publish `.deb`, `.rpm` and `.apk` packages to the
owner's package registries:
[`artifacts-forgejo-packages.yml`](artifacts-forgejo-packages.yml) declares
it, [`.goreleaser.yml`](.goreleaser.yml) builds the packages with `nfpms`, and
[`forgejo/release-goreleaser.yml`](forgejo/release-goreleaser.yml), copied as
`release.yml`, signs each with the release's OpenPGP key. That workflow
installs `go`, GoReleaser and syft from the project's own mise config and
`mise.lock`. See
[Distro packages in the owner's registries](../../docs/forgejo.md#distro-packages-in-the-owners-registries).

## Pull/merge-request gate coverage

| Forge | Included in this starter | What the project adds |
|---|---|---|
| GitHub | Lint and security checks, then `go vet` and `go test` in the starter's own `test` job | The project's vulnerability policy, for example `govulncheck`; require both jobs as status checks |
| GitLab | Lint, `go vet`, `go test`, then a cross-platform build and Build SBOM | Any additional project-specific checks; tests run once in the `test` stage |
| Forgejo | Lint, a check that the workflow keeps release jobs separate, `go test`, and a build without a Build SBOM | Any additional project-specific checks |

## Files

- `artifacts.yml` declares `project-type: go` with `config.build-mode: artifact-first`. The GitHub release reads it; the GitLab and Forgejo files select platforms in their build step.
- `github/release-workflow.yml` calls the release orchestrator and attaches `release-artifacts/*/my-cli-*` to the GitHub Release.
- `github/pullrequest-workflow.yml` runs forge-ci PR checks and, in its own `test` job, `go vet` and `go test`; `govulncheck` and any other policy are yours to add there.

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
| `enable-build-sbom` | `true` | `true` | `'true'` |

Select `platforms` explicitly when moving a build between forges. The examples
build `main-package: ./cmd/my-cli` on every forge, in pull requests and
releases alike; use `.` for a command at the module root. It is relative to
`working-directory`. Forgejo action booleans are string inputs.

GitHub release creation downloads the build artifact contents under
`release-artifacts/<goos>-<goarch>/`. Filenames include the platform suffix so
GitHub Release assets have unique basenames.
