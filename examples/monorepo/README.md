# Monorepo Example

Multiple artifacts built from a single repository with container support.

## Per forge

- **GitHub**: [`github/pullrequest-workflow.yml`](github/pullrequest-workflow.yml)
  and [`github/release-workflow.yml`](github/release-workflow.yml), with
  `artifacts.yml` or `multi-artifact-container.yml`. See the
  [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml). On a tag, the
  pipeline starts one `build-<ecosystem>` job per artifact of `artifacts.yml`
  ([Builds](../../docs/gitlab.md#builds)). For the release, copy
  [`gitlab/check-release.sh`](gitlab/check-release.sh) and
  [`gitlab/build-release.sh`](gitlab/build-release.sh) to `.gitlab/` and name
  them in the release policy's `checks` and `build` inputs. The frontend's
  tests run in `build-release.sh`, so a failing test stops the release before
  anything is signed. See
  [Releases and promotion](../../docs/gitlab.md#releases-and-promotion).
- **Forgejo**: [`forgejo/pullrequest.yml`](forgejo/pullrequest.yml), the lint
  gate and each project's checks in the image of its toolchain, and
  [`forgejo/release.yml`](forgejo/release.yml), one release of both projects.
  The workflow names a build job for each project and joins their files into
  one hand-off; the images those jobs run in are pinned by digest in the
  workflow. See
  [Delivery layers side by side](../../docs/providers.md#delivery-layers-side-by-side).

On every forge the release is one version of both projects under one tag: the
backend's jar and the frontend's package as signed release files with their
Build SBOMs, an image of each in one image repository, told apart by tag
suffix, and the frontend's package in the forge's package registry.

The rest of this page describes the GitHub configuration.

## Two variants

**[`artifacts.yml`](artifacts.yml)**: one container per artifact.
`backend-api` (Maven application, Java 25) ships as a release jar and an image
tagged `-backend`; `frontend-ui` (npm, Node 24) also publishes to the forge's
package registry and ships an image tagged `-frontend`.

```text
my-monorepo/
├── services/
│   └── backend/
│       ├── src/
│       ├── pom.xml
│       └── Containerfile
├── packages/
│   └── frontend/
│       ├── src/
│       ├── package.json
│       └── Containerfile
├── .forge-ci/
│   └── artifacts.yml
└── .github/
    └── workflows/
        └── release-workflow.yml
```

```bash
mkdir -p .forge-ci
cp examples/monorepo/artifacts.yml .forge-ci/artifacts.yml
```

**[`multi-artifact-container.yml`](multi-artifact-container.yml)**: one
combined container, `combined-app`, from three artifacts: `api` (Maven,
`services/api`), `worker` (Gradle JVM, `services/worker`) and `web` (npm,
`apps/web`). It builds from a `Containerfile` at the repository root; copy
[`Containerfile.multi-artifact`](Containerfile.multi-artifact) there.

```bash
mkdir -p .forge-ci
cp examples/monorepo/multi-artifact-container.yml .forge-ci/artifacts.yml
cp examples/monorepo/Containerfile.multi-artifact Containerfile
```

Change each `working-directory` and `container-file` to match your layout.
The [monorepo reference](../../docs/artifacts-reference.md#monorepo-configuration)
describes both patterns.

## How a release runs

```text
1. Parse artifacts.yml
   └─> Identify all artifacts and containers

2. Build Stage (parallel)
   ├─> Build backend-api (Maven)
   └─> Build frontend-ui (npm)

3. Publish Stage (parallel)
   ├─> backend-api container → GHCR
   ├─> frontend-ui package → GitHub Packages
   └─> frontend-ui container → GHCR

4. Release Stage
   └─> Single GitHub release with all artifacts
```

All artifacts share the version of the signed release-request tag:
`release-request/v1.0.0` makes every artifact version 1.0.0.

## Getting started

1. Pick a variant and copy its files as shown above.
2. Copy the workflows:

   ```bash
   cp examples/monorepo/github/*.yml .github/workflows/
   ```

3. Configure secrets and create the release request as the
   [examples Quick Start](../README.md#quick-start) describes (steps 4 and 5).

## Limitations and troubleshooting

- [Monorepo limitations](../../docs/artifacts-reference.md#monorepo-limitations):
  one version and one changelog for all artifacts, and every artifact builds
  on every release. For dependent Maven modules, declare one Maven reactor
  artifact.
- [Assembly and dependency ordering](../../docs/artifacts-reference.md#assembly-and-dependency-ordering):
  missing artifact names, build dependencies and Containerfile paths.

## See Also

- [Artifacts Reference](../../docs/artifacts-reference.md)
- [Publishing Guide](../../docs/publishing.md)
