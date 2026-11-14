# Publishing Guide

Configure and verify the supported publishing targets: Maven Central,
GitHub Packages, container registries, App Store Connect, and Google
Play. Per-target sections cover prerequisites, the `artifacts.yml`
fields and secrets each one needs, and the consumer-side fetch
commands.

Snippets pin `v0.90.0`; see [Which version to pin](README.md#documentation-index).

The [administrator's check enforcement](admin-policy.md) decides whether lint,
vulnerability, test and reproducible-build findings block a publication. All
four report without blocking by default; the checks still run.

## Release-file manifests

The build hands its files to signing in `dist/release-files.json`. The
manifest lists the public `assets` and marks which of them are `checksums`
(exactly one file), `sboms`, `evidence` and `provenance`. Each entry has a
`path` under `dist/`, its basename as `name`, and a `source` label:

```json
{
  "version": 1,
  "assets": [
    {"path": "dist/app.jar", "name": "app.jar", "source": "producer"},
    {"path": "dist/SHA256SUMS", "name": "SHA256SUMS", "source": "producer"}
  ],
  "checksums": [{"path": "dist/SHA256SUMS", "name": "SHA256SUMS", "source": "producer"}],
  "sboms": [],
  "evidence": [],
  "provenance": []
}
```

The commands, in the order a release runs them:

1. `forge-ci release files declare` writes the checksums file and the manifest
   from `--asset` and `--sbom` paths, which may be glob patterns
   (`--asset 'dist/*/*'`). For a GoReleaser build, use
   `release files import-goreleaser` instead.
2. `forge-ci release files validate --unsigned` checks the manifest and
   refuses a `dist/` that already holds signatures or provenance, so every
   signature comes from the signing job.
3. `release sign` and `release provenance` with `--checksums-from-manifest`
   check every checksum against its file, then sign.
4. `forge-ci release files manifest` adds the provenance and the signatures
   to the manifest. When `release publish` picks files through a manifest, it
   accepts only such a finished one.

Every published file is covered: an asset by its line in the checksums file,
the checksums file, each evidence file and the provenance by their own
signature bundles. See the [CLI reference](cli-reference.md#forge-ci-release-files)
for each command's flags.

## Targets at a glance

| Target                    | Artifact types     | Authentication              |
|---------------------------|--------------------|-----------------------------|
| Maven Central             | Maven and Gradle JVM libraries | Signed release request, GPG key and Sonatype credentials |
| GitHub Packages (Maven)   | Maven and Gradle JVM libraries | `GITHUB_TOKEN` (automatic)  |
| GitHub Packages (NPM)     | NPM packages       | `GITHUB_TOKEN` (automatic)  |
| Container registries      | Container images   | `GITHUB_TOKEN` or registry-password |
| Apple App Store           | iOS / macOS apps   | App Store Connect API v2    |
| Google Play Store         | Android apps       | Service Account JSON        |
| Forgejo Debian, RPM and Alpine registries | `.deb`, `.rpm` and `.apk` release files | `RELEASE_REGISTRY_TOKEN`; see [Distro packages in the owner's registries](forgejo.md#distro-packages-in-the-owners-registries) |

A Maven library published to GitHub Packages carries the exact POM, main,
sources and Javadoc bytes of its build job (`publish forge-packages deploy
--from-build`); the project lifecycle does not run again. It refuses
profile-selected reactor modules and a `finalName` that does not resolve to
a plain file name.

---

## Maven Central

Gradle uses exported Maven-layout files with separate signing and upload; see
[Gradle JVM and Android libraries](#gradle-jvm-and-android-libraries). The
Maven profile setup below applies to Maven projects.

### Prerequisites

1. **GPG Key Setup**: configure `RELEASE_GPG_PRIVATE_KEY`,
   `RELEASE_GPG_PASSPHRASE`, and `RELEASE_GPG_PUBLIC_KEY` as repository
   or organization secrets.

2. **Maven Central Credentials**: configure `MAVEN_CENTRAL_USERNAME`
   and `MAVEN_CENTRAL_PASSWORD` the same way.

3. **Release authority**: use an allowlisted signed `release-request/vMAJOR.MINOR.PATCH`
   and the exact prepared release commit. The publisher rechecks authorization,
   including on reruns, before importing keys or running project tools.

### Configuration

```yaml
# .forge-ci/artifacts.yml
artifacts:
  - name: my-library
    project-type: maven
    working-directory: .
    build-type: library              # Required for Maven Central
    require-authorization: true      # Recommended for production libraries
    publish-to:
      - forge-packages              # Also publish to GitHub
      - maven-central                # Publish to Maven Central
    config:
      java-version: 25
      settings-path: .mvn/settings.xml  # Optional: custom settings
```

### Project Requirements

Your `pom.xml` must include the required metadata and project-owned attachments
shown below.

The source and Javadoc plugins below are project-owned and version-pinned. The
`library` build type selects the library lifecycle and `central-release`
profile; forge-ci does not inject those plugin goals.

```xml
<project>
  <!-- Required metadata -->
  <groupId>com.example</groupId>
  <artifactId>my-library</artifactId>
  <version>1.0.0</version>
  <packaging>jar</packaging>

  <name>My Library</name>
  <description>A brief description</description>
  <url>https://github.com/<owner>/my-library</url>

  <!-- Required license -->
  <licenses>
    <license>
      <name>MIT License</name>
      <url>https://opensource.org/licenses/MIT</url>
    </license>
  </licenses>

  <!-- Required developer info -->
  <developers>
    <developer>
      <name>Your Name</name>
      <email>your.email@example.com</email>
      <organization>Your Organization</organization>
      <organizationUrl>https://example.com</organizationUrl>
    </developer>
  </developers>

  <!-- Required SCM info -->
  <scm>
    <connection>scm:git:git://github.com/<owner>/my-library.git</connection>
    <developerConnection>scm:git:ssh://github.com:<owner>/my-library.git</developerConnection>
    <url>https://github.com/<owner>/my-library/tree/main</url>
  </scm>

  <build>
    <plugins>
      <!-- Maven Central Publishing (modern approach) -->
      <plugin>
        <groupId>org.sonatype.central</groupId>
        <artifactId>central-publishing-maven-plugin</artifactId>
        <version>0.8.0</version>
        <extensions>true</extensions>
        <configuration>
          <checksums>all</checksums>
          <skipPublishing>false</skipPublishing>
          <publishingServerId>central</publishingServerId>
        </configuration>
      </plugin>
    </plugins>
  </build>

  <profiles>
    <profile>
      <id>central-release</id>
      <build>
        <plugins>
          <!-- GPG Signing -->
          <plugin>
            <groupId>org.apache.maven.plugins</groupId>
            <artifactId>maven-gpg-plugin</artifactId>
            <version>3.2.8</version>
            <executions>
              <execution>
                <id>sign-artifacts</id>
                <phase>verify</phase>
                <goals>
                  <goal>sign</goal>
                </goals>
              </execution>
            </executions>
          </plugin>

          <!-- Sources JAR -->
          <plugin>
            <groupId>org.apache.maven.plugins</groupId>
            <artifactId>maven-source-plugin</artifactId>
            <version>3.3.1</version>
            <executions>
              <execution>
                <id>attach-sources</id>
                <goals>
                  <goal>jar-no-fork</goal>
                </goals>
              </execution>
            </executions>
          </plugin>

          <!-- Javadoc JAR -->
          <plugin>
            <groupId>org.apache.maven.plugins</groupId>
            <artifactId>maven-javadoc-plugin</artifactId>
            <version>3.11.3</version>
            <executions>
              <execution>
                <id>attach-javadocs</id>
                <goals>
                  <goal>jar</goal>
                </goals>
              </execution>
            </executions>
          </plugin>
        </plugins>
      </build>
    </profile>
  </profiles>
</project>
```

### Maven Settings (Optional)

If you need custom repository configuration:

```xml
<!-- .mvn/settings.xml -->
<settings>
  <servers>
    <server>
      <id>central</id>
      <username>${env.MAVEN_CENTRAL_USERNAME}</username>
      <password>${env.MAVEN_CENTRAL_PASSWORD}</password>
    </server>
  </servers>
</settings>
```

Reference it in artifacts.yml:

```yaml
config:
  settings-path: .mvn/settings.xml
```

### Release Process

1. **Request a release**: push a signed request tag:

   ```bash
   git tag -s release-request/v1.0.0 -m "Release v1.0.0"
   git push origin release-request/v1.0.0
   ```

   forge-ci verifies the signature, bumps the version and changelog, and
   creates the final `v1.0.0` tag once; see
   [Request → promote](verification.md#request--promote-release-tags-are-immutable).
   Every target on this page is released this way. The example release
   workflows trigger on `release-request/v*`.
   Snapshot builds use the snapshot orchestrator instead: its example callers
   run on pushes to `develop` and `feature/**` and on `workflow_dispatch`,
   and use no release-request tag.

2. **Build and deployment:**
   - Runs the library lifecycle/profile; the project POM attaches sources and javadoc
   - Runs Maven deploy with the selected settings and profile; the project profile handles GPG signing
   - Delegates upload and publication policy to the configured Maven Central plugin

   Configure the plugin's publication behavior in the project POM/profile.
   The engine does not perform a separate portal release or wait for availability.

3. **Availability:**
   - Confirm deployment/publication status in the Central Portal
   - Availability and indexing depend on provider processing; check <https://central.sonatype.com/>

### Consuming Published Library

#### Released Versions

Users add to their `pom.xml`:

```xml
<dependency>
  <groupId>com.example</groupId>
  <artifactId>my-library</artifactId>
  <version>1.0.0</version>
</dependency>
```

No additional configuration needed - Maven Central is included by default.

#### Snapshot channel

Branch snapshots publish Maven-format snapshots when
`publish-maven-snapshots: true`; see [Snapshots](#snapshots). For prereleases,
see [release policy](workflows.md#stable-and-prerelease-policy).

---

## Gradle JVM and Android libraries

Gradle libraries are published from files that Gradle's `maven-publish`
plugin exports. Signing and upload happen in separate jobs.

### Supported scope

The publisher supports JVM JAR, Android-library AAR and POM publications produced
by Gradle's native `maven-publish` plugin. Multiple publications and subprojects
must have unique coordinates. Stable releases use `MAJOR.MINOR.PATCH`, with every
publication matching the authorized release version. Snapshot mode accepts only
`MAJOR.MINOR.PATCH-SNAPSHOT`; see [Snapshots](#snapshots).

Kotlin Multiplatform formats beyond JAR/AAR/POM and arbitrary remote publishing
task overrides are outside this publication contract.

The CLI exports the entire Gradle build by default. Use `--project :lib` to
export only that exact Gradle project's publications (or `--project :` for the
root project). Android workflows and components pass `build-module` as this
selector. All publications of the selected project are included; a product flavor
does not select a publication. Declare only the variants intended for publication
in that module's `publishing` configuration.

### Configure the project

Apply `java-library` and `maven-publish`, declare a publication from the Java
component, and set the project's group and version. For Central, attach sources
and javadoc and provide POM name, description, URL, license, developer and SCM
metadata. A conventional Kotlin DSL publication is:

```kotlin
plugins {
    `java-library`
    `maven-publish`
}
java {
    withSourcesJar()
    withJavadocJar()
}
publishing {
    publications {
        create<MavenPublication>("library") {
            from(components["java"])
            pom {
                name.set("Example library")
                description.set("An example JVM library")
                url.set("https://example.org/library")
                licenses { license {
                    name.set("Apache-2.0")
                    url.set("https://www.apache.org/licenses/LICENSE-2.0")
                } }
                developers { developer { id.set("maintainer") } }
                scm { url.set("https://example.org/library.git") }
            }
        }
    }
}
```

The build needs no remote publishing repository or signing plugin. Forge CI adds
a temporary file repository for stable export and uses a private Maven-local
repository for snapshot export. Publishing jobs consume those exported files.
They do not execute the consumer's Gradle/Maven project or its publishing tasks.
Snapshot upload uses a pinned Maven `deploy-file` goal in an empty directory to
handle timestamp allocation and remote metadata merging.

### GitHub release configuration

Use the [Gradle example](../examples/gradle-app/) with a library artifact:

```yaml
artifacts:
  - name: my-library
    project-type: gradle
    build-type: library
    publish-to: [forge-packages, maven-central]
    require-authorization: true
    config:
      gradle-tasks: build
      gradle-version-file: gradle.properties
```

Selecting Central requires an allowlisted signed release request even when
`require-authorization` is omitted. The builder exports a separate
`<artifact>-maven-publications` workflow artifact, preserving the existing build
JAR/SBOM artifact layouts. Every exported POM is checked against the release
version. Missing publication inputs fail before the build; produced metadata
and file sets are validated immediately after export.

Central requires its token username/password and the release GPG private/public
key, plus the passphrase when the key is encrypted. Git authorization is checked
again in the publishing job before importing keys, and before deployment. The
public key is also the trust root for every exported artifact's `.asc` signature.

### GitLab and Forgejo

GitLab `build-gradle` and `build-gradle-android` support `export-publications`,
`version`, `snapshot-publication` and `central-publication`. Select `library: true`
for an Android library. Pass the exported directory through native
`needs:`/`dependencies:` into `publish-forge-packages` or `publish-maven-central`
with `project-type: gradle`. For forge packages, set `working-directory` to the
exported directory. For Central, `publication-directory` is relative to the
source `working-directory`, and `release-sha` must name the prepared checkout.
Fetch the request and final tags for authorization.
Both publisher components accept `snapshot: true` for ordinary CI snapshot
publication without a signed release request.

The publish child pipelines GitLab generates from a plan cannot hand over the
exported publications; wire the components by hand as above.

On Forgejo, compose the same CLI steps in your own trusted jobs:
`build gradle export-publications`, then the chosen publisher. The uploader
takes the Maven endpoint from the forge; no GitHub URL is built in.

### Central lifecycle and recovery

The exported-file path validates and signs files with armored OpenPGP signatures,
then verifies every signature and its MD5, SHA-1, SHA-256 and SHA-512 checksum
sidecars before bundling. The weak hashes are Maven compatibility metadata;
OpenPGP supplies authenticity. `publish maven-central bundle`
submits exactly once. By default it waits for validation and requires manual
release in the Portal; `--automatic` waits for `PUBLISHED`. `VALIDATED` or
`PUBLISHING` at the deadline is a failure to confirm publication, never success.

Keep the printed deployment ID. Resume observation without uploading again:

```text
forge-ci publish maven-central wait --deployment-id <id> --automatic
```

If submission failed without returning an ID, inspect the Central Portal before
retrying: the upload may have been accepted. Native forge uploads likewise report
how many files completed before a partial failure; they never delete existing
package versions or automatically replay uploads.

With the Maven-profile path, a successful exit means the profile finished,
not that Central has published.

Native Maven deployment runs trusted consumer project code with credentials.
Its secret-free `--validate-only` preflight is an early check; actual deployment
also resolves and validates the reactor with the deployment environment before
running `deploy`, including the same `skipTests` property. This catches profiles
activated by publication credentials. It does not sandbox Maven extensions or
plugins, nor prevent project code from changing files or versions during the
lifecycle. Use exported-file publication when that stronger boundary is required.

### Android libraries

Use the existing Android project type with an explicit library build:

```yaml
artifacts:
  - name: android-library
    project-type: gradle-android
    build-type: library
    publish-to: [forge-packages, maven-central]
    sboms: build
    config:
      build-module: lib
      gradle-version-file: gradle.properties
```

Apply `com.android.library` and `maven-publish` in the library module, configure
`android.publishing.singleVariant("release")` with sources/javadoc, and publish
`components["release"]` with the same POM metadata as the JVM example. Configure
the project's AGP, Gradle wrapper and JDK/SDK runtime as a compatible set.

Library mode selects the module's release AAR, uses the Maven `version` property,
and does not increment application `versionCode` or change `versionName`.
APK/AAB signing and Google Play are application features: explicitly selecting
them for a library is rejected. The library's default SBOM selection is `build`;
unsupported analyzed-artifact requests are rejected before building.

### Snapshots

The existing snapshot orchestrator can publish Maven-format snapshots when
explicitly enabled:

```yaml
with:
  publish-maven-snapshots: true
```

This covers configured Maven, Gradle JVM and Android-library Maven targets.
Normal repository CI authorization and registry credentials are sufficient;
there is no allowlisted human release-request requirement for snapshots.
Stable releases still require that signed request. Every resolved module must
be a snapshot when using the snapshot route, so it cannot accidentally release a
stable version. Independently versioned snapshot modules are permitted.

For an exported Gradle project:

```sh
forge-ci build gradle export-publications --snapshot --output publication
forge-ci publish forge-packages deploy --project-type gradle --snapshot --working-dir publication
# Or use Central's snapshot repository:
forge-ci publish maven-central snapshot --working-dir publication
```

Snapshot upload requires Maven 3.9+ and uses the pinned deploy plugin to produce
timestamped filenames and merge repository metadata. It runs outside the consumer
checkout, refuses redirects, and disables transport/deployment retries. A failed
upload reports partial progress; inspect the repository before repeating it.

For Maven projects using the modern Central plugin:

```sh
forge-ci publish maven-central deploy --snapshot --profile central-release --settings-path settings.xml
```

The plugin supports snapshots from version 0.7.0. Forge CI validates the effective
reactor before deployment and sets `gpg.skip=true` for snapshots; release signing
keys are not required. The Maven profile still owns the native project lifecycle.

Central must have snapshots enabled for the namespace. Its snapshot repository
is `https://central.sonatype.com/repository/maven-snapshots/`; stable bundles use
the Publisher API. The modern Maven plugin routes between them automatically.
Snapshots are mutable and subject to retention. These Maven semantics do not
change npm's required version-specific tags.

---

## NPM Packages (GitHub Packages)

NPM publishing goes to `npm.pkg.github.com` and authenticates with the
auto-provided `GITHUB_TOKEN`; no separate npm token is needed. The
package name must use the `@<owner>/` scope, with `<owner>` matching
the lowercased GitHub repository owner. Configuration validation refuses the
`npmjs` `publish-to` value.

Publishing requires an explicit version-specific `config.npm-tag`, normally
`release-{version}`; there is no movable default channel. See the
[npm publication policy](ecosystems.md#npm-artifact-first). Before publishing,
`forge-ci publish npm validate-tarball` reads the packed metadata without
running package code: a package with `bin` needs `dist/cli.js`, and a library
needs a literal `exports` string naming a plain `./` file, or a `main` path
when `exports` is absent. Other `exports` forms are refused.

### Configuration

```yaml
# .forge-ci/artifacts.yml
artifacts:
  - name: my-package
    project-type: npm
    working-directory: packages/my-package
    publish-to:
      - forge-packages  # Publish to GitHub Packages
    config:
      node-version: 24
      npm-tag: release-{version}
```

### Package Requirements

Your `package.json` must include:

```json
{
  "name": "@your-github-org/my-package",
  "version": "1.0.0",
  "description": "A brief description",
  "main": "dist/index.js",
  "types": "dist/index.d.ts",
  "license": "MIT",
  "repository": {
    "type": "git",
    "url": "https://github.com/your-github-org/my-package.git"
  },
  "publishConfig": {
    "access": "public"
  },
  "files": [
    "dist",
    "README.md",
    "LICENSE"
  ]
}
```

### Release Process

1. **Request the release** with a signed `release-request/v1.0.0` tag, as in
   [the Maven Central release process](#release-process).

2. **Workflow automatically:**
    - Runs `npm run build` when a `build` script exists
    - Packs the package tarball
    - Publishes the tarball to GitHub Packages

3. **Availability:**
    - Appears under the repository/organization packages in GitHub
    - Installable through `npm.pkg.github.com` after registry authentication

### Consuming Published Package

Point npm at GitHub Packages, then install:

```bash
npm config set @your-github-org:registry https://npm.pkg.github.com
npm config set //npm.pkg.github.com/:_authToken YOUR_GITHUB_TOKEN
npm install @your-github-org/my-package
```

---

## Container Registries

`ghcr.io` is the default, using `GITHUB_TOKEN` for registry authentication.
Other destinations can use the `registry-username` input and `registry-password`
secret on `publish-container.yml`'s call site. Credentials must authorize writes
to the chosen destination.

Direct callers select image signing explicitly. The job fragments below use
`sign-image: true` and `sign-method: sigstore`; with the default
`enable-slsa: true`, they also attach signed provenance. The registry must
support the OCI and cosign operations used by the workflow. See
[container attestation and verification](verification.md#container-image-attestation--verification).

### GitHub Container Registry (ghcr.io)

Add the container to an artifact configuration with a compatible
[signing method](verification.md#choosing-a-method):

```yaml
containers:
  - name: my-app
    from: [my-app]
    container-file: Containerfile
    # registry defaults to ghcr.io
```

**Image naming:**

```text
ghcr.io/OWNER/REPO_NAME/container-name:v1.0.0
```

When the container name is empty or equals the repository's short name, the
default image is `ghcr.io/OWNER/REPO_NAME:v1.0.0` without the extra name segment.

**Namespace security:**

- Under `<registry>/<owner>/`, names may be `<repo>`, `<repo>-<suffix>` or
  `<repo>/<subpath>`. Combining a sibling suffix and subpath is refused.
- The owner defaults to `github.repository_owner`; comparison ignores case.
- `image-name` selects the destination. `enforce-namespace` sets the expected
  owner and does not rewrite the image name. The repository's short name remains
  part of the required prefix.
- The workflow checks its selected registry before a requested push. This
  confines the destination name; the registry still enforces credential permissions.

**Custom namespace (direct component use only):**

These caller job fragments assume source repository `source-org/my-app` and a
`build-maven` job that uploads `maven-build-artifacts`. Adjust the source and
destination names together for your project. For the custom GHCR destination,
configure `REGISTRY_USER` as a variable and `REGISTRY_TOKEN` as a secret
authorized to publish under `my-custom-org`.

A direct caller supplies what the release orchestrator supplies: the
canonical CLI of the same run. `canonical-cli-bootstrap.yml` builds it from
the full engine commit in a pinned Go builder image (the image the
orchestrator's `canonical-builder-image` input takes); the host build legs
of `publish-container.yml` receive it, and its other jobs run in the
`runtime-image` and `runtime-image-full` digests. Tags are refused there.

```yaml
jobs:
  canonical-cli:
    permissions:
      contents: read
      actions: read
    uses: diggsweden/forge-ci/.github/workflows/canonical-cli-bootstrap.yml@PIN_FORGE_CI_SHA # v0.90.0
    with:
      engine-repository: diggsweden/forge-ci
      engine-commit: <full-engine-commit> # the commit the pinned forge-ci tag points at
      runtime-image: <go-builder-image>@sha256:<digest>
      receive-runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:<digest>
  publish-container:
    needs: [build-maven, canonical-cli]
    uses: diggsweden/forge-ci/.github/workflows/publish-container.yml@PIN_FORGE_CI_SHA # v0.90.0
    permissions:
      contents: read
      packages: write
      id-token: write
      actions: read
    with:
      branch: main
      forge-ci-binary-ref: <full-engine-commit>
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:<digest>
      runtime-image-full: ghcr.io/diggsweden/forge-ci-runtime@sha256:<digest>
      canonical-artifact-id: ${{ needs.canonical-cli.outputs.artifact-id }}
      canonical-artifact-name: ${{ needs.canonical-cli.outputs.artifact-name }}
      canonical-producer-run-id: ${{ needs.canonical-cli.outputs.producer-run-id }}
      canonical-producer-run-attempt: ${{ needs.canonical-cli.outputs.producer-run-attempt }}
      canonical-producer-engine-repository: ${{ needs.canonical-cli.outputs.engine-repository }}
      canonical-producer-engine-commit: ${{ needs.canonical-cli.outputs.engine-commit }}
      canonical-producer-result: ${{ needs.canonical-cli.result }}
      container-file: Containerfile
      artifact-types: maven
      registry: ghcr.io
      image-name: ghcr.io/my-custom-org/my-app
      enforce-namespace: my-custom-org
      registry-username: ${{ vars.REGISTRY_USER }}
      use-ci-token: false
      sign-image: true
      sign-method: sigstore
    secrets:
      registry-password: ${{ secrets.REGISTRY_TOKEN }}
```

**Pull image:**

```bash
# Replace v1.0.0 with the tag emitted by your workflow.
podman pull ghcr.io/my-custom-org/my-app:v1.0.0
```

### Docker Hub

Requires a Docker Hub access token. Configure `DOCKERHUB_USERNAME` as a
repository or organization variable and `DOCKERHUB_TOKEN` as a secret.

The release orchestrator publishes containers to GHCR. For Docker Hub, call
`publish-container.yml` directly, as in the custom-namespace example above,
with the `canonical-cli` job and every input it shows. Change only these
lines of the `publish-container` job:

```yaml
    with:
      # ...the other inputs as in the custom-namespace example
      registry: docker.io
      image-name: docker.io/dockerhub-org/my-app
      enforce-namespace: dockerhub-org
      registry-username: ${{ vars.DOCKERHUB_USERNAME }}
      use-ci-token: false
    secrets:
      registry-password: ${{ secrets.DOCKERHUB_TOKEN }}
```

**Image naming:**

```text
docker.io/dockerhub-org/my-app:v1.0.0
```

### Custom Registry

Configure `REGISTRY_USER` as a variable and `REGISTRY_TOKEN` as a secret
authorized for the destination. Use the custom-namespace example above,
leave out its `image-name` and `enforce-namespace` lines, and change these.
Without `image-name`, the image keeps the source owner's namespace and
repository name: `registry.example.com/source-org/my-app`.

```yaml
    with:
      # ...the other inputs as in the custom-namespace example
      registry: registry.example.com
      registry-username: ${{ vars.REGISTRY_USER }}
      use-ci-token: false
    secrets:
      registry-password: ${{ secrets.REGISTRY_TOKEN }}
```

---

## Apple App Store (TestFlight)

iOS builds upload to App Store Connect via API v2 (no Fastlane).
Review submission stays a manual step in App Store Connect; forge-ci
gets the artifact uploaded and stops there.

### Prerequisites

1. **Apple Developer Account**
   - Enrolled in Apple Developer Program
   - App created in App Store Connect

2. **App Store Connect API Key**
   - Navigate to [App Store Connect > Users and Access > Integrations > App Store Connect API](https://appstoreconnect.apple.com/access/integrations/api)
   - Create a new API key with "App Manager" role
   - Download the `.p8` private key file (only available once!)
   - Note the Key ID and Issuer ID

3. **Code Signing**
   - Distribution certificate (`.p12` file)
   - Provisioning profile for App Store distribution
   - Export options plist configured for App Store

### Enabling/Disabling App Store Publishing

iOS apps with `project-type: xcode-ios` **automatically publish to App Store Connect** when:
1. `enable-code-signing: true` is set
2. The required secrets are configured

**To build without App Store publishing**, set `enable-code-signing: false`:

```yaml
config:
  enable-code-signing: false  # Build archive only; release publish skips App Store upload
```

### Configuration

```yaml
# .forge-ci/artifacts.yml
artifacts:
  - name: my-ios-app
    project-type: xcode-ios
    working-directory: .
    publish-to: []  # iOS apps publish via App Store Connect, not package registries
    config:
      xcode-version: "16.1"
      scheme: "MyApp"
      use-xcodegen: true
      xcodegen-spec: "project.yml"
      project: "MyApp.xcodeproj"
      configuration: Release
      enable-code-signing: true   # Enables IPA export and App Store Connect upload
      export-options-var: EXPORT_OPTIONS_BASE64
      # App Store Connect upload options
      submit-for-review: false  # Summary intent only; submit review manually in App Store Connect
      skip-validation: false    # Validate IPA before upload (recommended)
```

If your app uses XcodeGen, keep `project` or `workspace` configured as well so the generated build target is explicit in later steps.

### Required Secrets

```text
# Code Signing
IOS_SIGNING_CERTIFICATE_BASE64              # Base64-encoded .p12 distribution certificate
IOS_SIGNING_CERTIFICATE_PASSPHRASE          # Certificate password
PROVISIONING_PROFILE_BASE64     # Base64-encoded provisioning profile

# App Store Connect API
APP_STORE_CONNECT_ISSUER_ID           # From App Store Connect API keys page
APP_STORE_CONNECT_API_KEY_ID          # Key ID from App Store Connect
APP_STORE_CONNECT_API_PRIVATE_KEY_BASE64  # Base64-encoded .p8 private key
```

### Required Variables

```text
EXPORT_OPTIONS_BASE64           # Base64-encoded exportOptions.plist
```

### Encoding Files to Base64

```bash
# Certificate (.p12)
base64 < Certificates.p12 > certificate.txt

# Provisioning Profile
base64 < MyApp_Distribution.mobileprovision > profile.txt

# App Store Connect API Key (.p8)
base64 < AuthKey_XXXXXXXXXX.p8 > apikey.txt

# Export Options
base64 < exportOptions.plist > exportOptions.txt
```

### Export Options Example

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>method</key>
    <string>app-store-connect</string>
    <key>teamID</key>
    <string>YOUR_TEAM_ID</string>
    <key>uploadSymbols</key>
    <true/>
    <key>destination</key>
    <string>upload</string>
</dict>
</plist>
```

### Release Process

1. **Request the release** with a signed `release-request/v1.0.0` tag, as in
   [the Maven Central release process](#release-process).

2. **Workflow automatically:**
   - Builds iOS app with Xcode
   - Signs with distribution certificate
   - Exports IPA file
   - Validates IPA with Apple
   - Uploads to App Store Connect

3. **Availability:**
   - TestFlight: ~10-15 minutes after upload processing
   - App Store: After manual review submission in App Store Connect

---

## Google Play Store

Android builds upload to Google Play via the Developer API v3. The
workflow supports the four standard tracks (internal, alpha, beta,
production) and staged rollouts to a fraction of users.

### Prerequisites

1. **Google Play Developer Account**
   - Enrolled in Google Play Developer Program
   - App created in Google Play Console (must upload first APK/AAB manually)

2. **Service Account Setup**
   - Enable Google Play Android Developer API in [Google Cloud Console](https://console.cloud.google.com/apis/library/androidpublisher.googleapis.com)
   - Create service account in [IAM & Admin > Service accounts](https://console.cloud.google.com/iam-admin/serviceaccounts)
   - Create and download JSON key for the service account
   - In [Google Play Console > Users and permissions](https://play.google.com/console), invite the service account email
   - Grant "Release manager" or appropriate permissions for your app

3. **App Signing**
   - Keystore file for signing release builds
   - Key alias and passwords

### Enabling/Disabling Google Play Publishing

**To enable Google Play publishing**, add `google-play` to the `publish-to` array:

```yaml
publish-to:
  - google-play    # Enable Google Play publishing
```

**To disable Google Play publishing**, remove `google-play` from the array or use an empty array:

```yaml
publish-to: []     # No publishing - only build and attach to GitHub Release
```

### Configuration

```yaml
# .forge-ci/artifacts.yml
artifacts:
  - name: my-android-app
    project-type: gradle-android
    working-directory: .
    sboms: build
    publish-to:
      - google-play    # <-- This enables Google Play publishing
    config:
      build-module: app
      product-flavor: demo
      build-types: release
      gradle-version-file: gradle.properties
      enable-android-signing: true
      # Google Play configuration (required when publish-to includes google-play)
      package-name: com.example.myapp
      google-play-track: internal          # internal, alpha, beta, production
      google-play-status: completed        # completed, inProgress, halted, draft
      # Optional settings
      google-play-user-fraction: ""        # 0.1 = 10% rollout (only for inProgress)
      google-play-update-priority: "0"     # 0-5 (5 = highest priority)
      google-play-release-name: ""         # Custom release name
      whats-new-directory: ""              # Path to localized release notes
      mapping-file: ""                     # ProGuard mapping.txt path
      debug-symbols: ""                    # Native debug symbols path
```

Android needs an explicit SBOM selection, since APK/AAB files get no
`analyzed-artifact` SBOM. See
[Android SBOM selection](sbom.md#defaults).

### Required Secrets

```text
# App Signing
ANDROID_KEYSTORE                # Base64-encoded keystore file
ANDROID_KEYSTORE_PASSWORD       # Keystore password
ANDROID_KEY_ALIAS               # Key alias name
ANDROID_KEY_PASSWORD            # Key password

# Google Play API
GOOGLE_PLAY_SERVICE_ACCOUNT_JSON  # Service account JSON key (plain text, not base64)
```

The workflow maps the `ANDROID_KEYSTORE` secret to the internal
`ANDROID_KEYSTORE_BASE64` environment variable used by the `forge-ci` CLI.
Repository users should create the secret as `ANDROID_KEYSTORE`.

### Encoding Keystore to Base64

```bash
base64 < release-keystore.jks > keystore.txt
```

### Gradle Signing Configuration

Your `app/build.gradle.kts` should read signing config from environment:

```kotlin
android {
    signingConfigs {
        create("release") {
            storeFile = file(System.getenv("ANDROID_KEYSTORE_PATH") ?: "release.keystore")
            storePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD") ?: ""
            keyAlias = System.getenv("ANDROID_KEY_ALIAS") ?: ""
            keyPassword = System.getenv("ANDROID_KEY_PASSWORD") ?: ""
        }
    }

    buildTypes {
        release {
            signingConfig = signingConfigs.getByName("release")
            // ... other config
        }
    }
}
```

### Google Play Track Options

| Track | Description | Review Required |
|-------|-------------|-----------------|
| `internal` | Internal testing (up to 100 testers) | No |
| `alpha` | Closed testing | No |
| `beta` | Open testing | No |
| `production` | Full release | Yes (first time) |

### Staged Rollouts

For gradual releases, use `inProgress` status with `google-play-user-fraction`:

```yaml
config:
  google-play-track: production
  google-play-status: inProgress
  google-play-user-fraction: "0.1"  # 10% of users
```

### Localized Release Notes

Create a directory with `whatsnew-<LOCALE>` files:

```text
distribution/
└─ whatsnew/
  ├─ whatsnew-en-US
  ├─ whatsnew-sv-SE
  └─ whatsnew-de-DE
```

Reference in config:

```yaml
config:
  whats-new-directory: distribution/whatsnew
```

### Release Process

1. **Request the release** with a signed `release-request/v1.0.0` tag, as in
   [the Maven Central release process](#release-process).

2. **Workflow automatically:**
   - Builds Android app with Gradle
   - Signs AAB with release keystore
   - Uploads to Google Play Console
   - Assigns to configured track

3. **Availability:**
   - Internal track: Immediately after upload
   - Alpha/Beta: After processing (~minutes)
   - Production: After review (first release) or immediately (updates)
