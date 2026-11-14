# Gradle JVM Build Example

Gradle JVM build example. For Android applications, see [examples/android-app](../android-app/). That path uses `project-type: gradle-android` and routes to `build-gradle-android.yml`, which understands product flavors, AABs, and Google Play publishing.

## Per forge

- **GitHub**: [`github/pullrequest-workflow.yml`](github/pullrequest-workflow.yml)
  and [`github/release-workflow.yml`](github/release-workflow.yml), with
  `artifacts.yml`. See the [GitHub guide](../../docs/github.md#getting-started).
- **GitLab**: [`gitlab/.gitlab-ci.yml`](gitlab/.gitlab-ci.yml), the lint gate,
  `check` and `build-gradle`. See the
  [GitLab guide](../../docs/gitlab.md#getting-started).
- **Forgejo**: [`forgejo/pullrequest.yml`](forgejo/pullrequest.yml) and
  [`forgejo/release.yml`](forgejo/release.yml). The layer has no Gradle
  action, so the release's build job runs `forge-ci build gradle run`
  itself; the release plan's bump writes the release version into
  `gradle.properties`. See the [Forgejo guide](../../docs/forgejo.md).

The rest of this page describes the GitHub configuration.

## Project Structure

```text
my-gradle-lib/
├── src/
├── build.gradle(.kts)
├── gradle.properties
├── settings.gradle(.kts)
├── .forge-ci/
│   └── artifacts.yml
└── .github/
    └── workflows/
        ├── pullrequest-workflow.yml
        └── release-workflow.yml
```

## Configuration Files

### `.forge-ci/artifacts.yml`

See [artifacts.yml](artifacts.yml) in this directory.

Key points:
- `project-type: gradle` → routes to `build-gradle-app.yml` (JVM-only; no Android SDK, no APK/AAB).
- `config.gradle-tasks: build` → runs the project's normal Gradle build.
- This example publishes nothing. To publish the library to GitHub Packages or Maven Central, set `build-type: library` and `publish-to: [forge-packages, maven-central]` on the artifact, and apply Gradle's `maven-publish` plugin; see [Gradle publishing](../../docs/publishing.md#gradle-jvm-and-android-libraries).

## How to Use

1. **Copy files to your repository:**
   ```bash
   mkdir -p .github/workflows .forge-ci
   cp examples/gradle-app/artifacts.yml .forge-ci/
   cp examples/gradle-app/nanolinter.toml .
   cp examples/gradle-app/github/release-workflow.yml .github/workflows/
   cp examples/gradle-app/github/pullrequest-workflow.yml .github/workflows/
   ```

2. **Customize for your project:**
   - Update `name` in `artifacts.yml`.
   - Adjust `gradle-tasks` if your build task differs.
   - Set `version=` in `gradle.properties` (the workflow reads this for the build summary).
   - **Set `preserveFileTimestamps = false` AND `reproducibleFileOrder = true` on every `AbstractArchiveTask`**: required for reproducible archives; `validate jvm-reproducibility` fails the release if either is missing. See [Reproducible Builds](../../docs/verification.md#reproducible-builds) for the exact snippet.

3. **Create first release:**
   ```bash
   git tag -s release-request/v1.0.0 -m "Release v1.0.0"
   git push origin release-request/v1.0.0
   ```

   Release automation creates the final `v1.0.0` tag after preparation; do not
   create or push that final tag directly.

## What Gets Built

- JAR artifacts from `build/libs/` (root and any subprojects, not `buildSrc`), uploaded as `<artifact name>-build-artifacts`, here `my-gradle-lib-build-artifacts`.
- A CycloneDX Build SBOM, uploaded as `<artifact name>-build-sbom`. forge-ci adds the `cyclonedx-gradle-plugin` itself; your `build.gradle` needs no change.

On a release, the orchestrator's prepare stage bumps the version.

## See Also

- [Artifacts Reference](../../docs/artifacts-reference.md)
- [Publishing Guide](../../docs/publishing.md)
- [SBOM Generation](../../docs/sbom.md)
