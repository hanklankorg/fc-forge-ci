# Maven Application Example

Simple Maven application with container build.

## Per forge

- **GitHub**: [`github/pullrequest-workflow.yml`](github/pullrequest-workflow.yml)
  and [`github/release-workflow.yml`](github/release-workflow.yml), with
  `artifacts.yml`. See the [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml), the lint gate,
  `check` and `build-maven`. No component builds or publishes the image. See
  the [GitLab guide](../../docs/gitlab.md#getting-started).
- **Forgejo**: [`forgejo/pullrequest.yml`](forgejo/pullrequest.yml) and
  [`forgejo/release.yml`](forgejo/release.yml), a `build-maven` jar release of
  a project at the repository root. `prepare-release.yml` bumps the POM
  versions, and `build-maven` checks the version it builds. An image
  is added to the build job as in the
  [Go service example](../go-service/forgejo/release.yml).
  [`forgejo/snapshot.yml`](forgejo/snapshot.yml) publishes a development
  image of `main` through the shared snapshot workflow, the jar built first
  in a digest-pinned Java toolchain image. See the
  [Forgejo guide](../../docs/forgejo.md#getting-started).

The rest of this page describes the GitHub configuration.

## Project Structure

```text
my-maven-app/
├── src/
│   └── main/java/...
├── pom.xml
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
   cp examples/maven-app/artifacts.yml .forge-ci/
   cp examples/maven-app/nanolinter.toml .
   cp examples/maven-app/github/pullrequest-workflow.yml .github/workflows/
   cp examples/maven-app/github/release-workflow.yml .github/workflows/
   ```

2. **Customize for your project:**
   - Update `name` in artifacts.yml
   - Set `config.java-version`
   - Verify `Containerfile` path
   - Configure release secrets from [Reference Guide](../../docs/reference.md), including `RELEASE_TOKEN` and the GPG secrets that sign the release commit and tag
   - **Set `<project.build.outputTimestamp>` in `pom.xml` `<properties>`**: required for reproducible jars; `validate jvm-reproducibility` fails the release if missing. See [Reproducible Builds](../../docs/verification.md#reproducible-builds) for the exact snippet.

3. **Create first release:**
   ```bash
   git tag -s release-request/v1.0.0 -m "Release v1.0.0"
   git push origin release-request/v1.0.0
   ```

   Release automation creates the final `v1.0.0` tag after preparation; do not
   create or push that final tag directly.

## What Gets Built

[`artifacts.yml`](artifacts.yml) declares one Maven application and its
container, published to GHCR:

- Maven JAR artifact → `target/*.jar`
- Container image → `ghcr.io/owner/repo:v1.0.0`
- Platforms → `linux/amd64`, `linux/arm64`

## See Also

- [Artifacts Reference](../../docs/artifacts-reference.md)
- [Publishing Guide](../../docs/publishing.md)
