# Maven Library Example

A Maven library released as its JARs and published to the package registry
of the repository's owner.

## Per forge

- **Forgejo**: [`forgejo/pullrequest-workflow.yml`](forgejo/pullrequest-workflow.yml) and
  [`forgejo/release-workflow.yml`](forgejo/release-workflow.yml), a `build-maven` library
  release of a project at the repository root. `prepare-release.yml` bumps
  the POM versions from [`artifacts.yml`](artifacts.yml), and `build-maven`
  checks the version it builds. See the
  [Forgejo guide](../../docs/forgejo.md#maven-npm-and-gradle-packages-in-the-owners-registry).
- **GitHub** and **GitLab**: no workflow files here. A library differs from
  the [Maven application example](../maven-app/) only in its declaration:
  `build-type: library` and `publish-to`, as in [`artifacts.yml`](artifacts.yml).
  See [Publishing](../../docs/publishing.md).

## How the library reaches the registry

1. The build job runs `build-maven` with `build-type: lib` and the profile
   of your POM that attaches the sources and Javadoc JARs.
2. As a library build, `build-maven` uploads every `pom.xml` and built JAR as
   the run artifact `maven-build-artifacts`, as GitHub's `build-maven`
   workflow does.
3. After the release is signed and published, the `publish-package` job runs
   `publish-forge-packages` with `from-build: "true"`. It downloads that
   artifact and uploads the POM and the main, sources and Javadoc JARs as
   built. Maven does not run again.

## How to Use

1. **Copy files to your repository:**
   ```bash
   mkdir -p .forgejo/workflows .forge-ci
   cp examples/maven-library/artifacts.yml .forge-ci/
   cp examples/maven-library/nanolinter.toml .
   cp examples/maven-library/forgejo/pullrequest-workflow.yml .forgejo/workflows/
   cp examples/maven-library/forgejo/release-workflow.yml .forgejo/workflows/
   ```

2. **Customize for your project:**
   - Replace every placeholder and `FIXME` the workflow headers list
   - Update `name` in artifacts.yml and set `config.java-version`
   - Set `profile` to the profile of your POM that attaches the sources and
     Javadoc JARs; the publisher refuses a library without them
   - Configure `RELEASE_REGISTRY_TOKEN`, a token that may write packages of
     the owner

## See Also

- [Artifacts Reference](../../docs/artifacts-reference.md)
- [Publishing Guide](../../docs/publishing.md)
