# No-build example

Scripts and configuration distributed through Git: a shell installer, a
script library, a configuration file. There is nothing to compile, no
container and no version file. `artifacts.yml` declares the project
`project-type: meta`, so Forge CI builds, bumps and scans nothing.

## Per forge

- **GitHub**: [`github/pullrequest-workflow.yml`](github/pullrequest-workflow.yml)
  and [`github/release-workflow.yml`](github/release-workflow.yml), with
  `artifacts.yml`. See the [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml), the lint gate
  and `check`; the release runs from a separate policy project. See the
  [GitLab guide](../../docs/gitlab.md#getting-started).
- **Forgejo**: [`forgejo/pullrequest.yml`](forgejo/pullrequest.yml) and
  [`forgejo/release.yml`](forgejo/release.yml), a job that archives the
  release commit between the layer's prepare and signer workflows. See the
  [Forgejo guide](../../docs/forgejo.md#getting-started).

## What a release is

The release differs between the forges, because their layers do:

| Forge | Release commit | Final tag | Files the release offers |
|---|---|---|---|
| GitHub | None: `changelog.skipversionbump: true` leaves `main` where it was | On the requested commit | None: the release is the signed tag and its notes |
| GitLab | The changelog alone | On the release commit | What the policy's build commands hand over: at least one file |
| Forgejo | The changelog alone | On the release commit | What the `build` job hands over: at least one file |

On GitLab and Forgejo the signer takes a hand-off of files: a manifest, one
checksums file over the files it names, and release notes. It signs each
file and writes provenance over them, and refuses a hand-off that names
none. A project with nothing to build therefore still hands one file over.
The Forgejo workflow archives the tree of the release commit and declares
the archive, in two steps that come to:

```sh
git archive --format=tar.gz --prefix="source-${RELEASE_TAG}/" \
  --output="dist/source-${RELEASE_TAG}.tar.gz" HEAD
forge-ci release files declare --asset "dist/source-${RELEASE_TAG}.tar.gz"
```

A GitLab release policy runs the same two commands as its `build` input. The
plan of a meta project has no build job of a toolchain family, so they run
once, in the release files job, with `FORGE_CI_BUILD_FAMILY` empty.

## Pull/merge-request gate coverage

| Forge | Included in this starter | What the project adds |
|---|---|---|
| GitHub | Lint and security checks, then the installer in the starter's own `test` job | Its own checks; require both jobs as status checks |
| GitLab | Lint, then the installer in `check` | Its own checks as `check` commands |
| Forgejo | Lint, release-isolation validation, then the installer | Its own checks in the `check` job |

The lint image is the `core` nanolinter flavour: secrets, SAST, text and CI
files, with no toolchain.
