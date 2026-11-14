# NPM Application Example

Simple NPM/Node.js application with container build.

## Per forge

- **GitHub**: [`github/pullrequest-workflow.yml`](github/pullrequest-workflow.yml)
  and [`github/release-workflow.yml`](github/release-workflow.yml), with
  `artifacts.yml`. See the [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml), the lint gate,
  `check` and `build-npm`. No component builds or publishes the image; the
  release policy publishes the tarball to the project's package registry, as
  `publish-forge-packages` does without one. See the
  [GitLab guide](../../docs/gitlab.md#package-publication).
- **Forgejo**: not available; the Forgejo layer has build actions for Go, Cargo and Maven only. See
  [Delivery layers side by side](../../docs/providers.md#delivery-layers-side-by-side).

The rest of this page describes the GitHub configuration.

## Project Structure

```text
my-npm-app/
├── src/
│   └── index.ts
├── package.json
├── tsconfig.json
├── Containerfile
├── .forge-ci/
│   └── artifacts.yml
└── .github/
    └── workflows/
        ├── pullrequest-workflow.yml
        └── release-workflow.yml
```

## How to Use

1. **Copy files to your repository:**
   ```bash
   mkdir -p .github/workflows .forge-ci
   cp examples/npm-app/artifacts.yml .forge-ci/
   cp examples/npm-app/nanolinter.toml .
   cp examples/npm-app/github/pullrequest-workflow.yml .github/workflows/
   cp examples/npm-app/github/release-workflow.yml .github/workflows/
   ```

2. **Customize for your project:**
   - Update `name` in artifacts.yml
   - Set `config.node-version`
   - Verify `Containerfile` path
   - Configure release secrets from [Reference Guide](../../docs/reference.md), including `RELEASE_TOKEN` and the GPG secrets that sign the release commit and tag

3. **Create first release:**
   ```bash
   git tag -s release-request/v1.0.0 -m "Release v1.0.0"
   git push origin release-request/v1.0.0
   ```

   Release automation creates the final `v1.0.0` tag after preparation; do not
   create or push that final tag directly.

## What Gets Built

[`artifacts.yml`](artifacts.yml) declares one npm artifact and one container:

- NPM package → Runs `npm run build` when a `build` script exists, then packs
  the package
- Published to → GitHub Packages only (`publish-to: [forge-packages]`). See the
  [Publishing Guide](../../docs/publishing.md#npm-packages-github-packages).
- Container image → `ghcr.io/owner/repo:v1.0.0`
- Platforms → `linux/amd64`, `linux/arm64`

## See Also

- [Artifacts Reference](../../docs/artifacts-reference.md)
- [Publishing Guide](../../docs/publishing.md)
