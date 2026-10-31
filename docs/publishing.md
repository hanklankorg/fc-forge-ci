# Publishing Guide

Configure and verify the supported publishing targets: Maven Central,
GitHub Packages, container registries, App Store Connect, and Google
Play. Per-target sections cover prerequisites, the `artifacts.yml`
fields and secrets each one needs, and the consumer-side fetch
commands.

> **Current source versus release pins:** `v0.90.0` examples identify a historical
> baseline, not the current source revision. Confirm matching workflows, CLI
> assets and runtime images before adoption. For unreleased changes, follow
> [Branch Testing](runtime-images.md#branch-testing).

## Check enforcement before publication

The [administrator's immutable engine policy](admin-policy.md) controls whether
recognized lint, vulnerability and application-test findings block progression.
All three settings default to enforcing. Legacy `skip-tests: true` (including
`SKIP_TESTS=true`) is refused, and exported/published OCI layouts are scanned
even when a consumer sets `enable-scan: false`. SBOM selection is separate from
this vulnerability scan.

To report recognized findings without blocking, use a reviewed administrator-built
report-only distribution with matching protected workflow, binary and image
pins. Checks still run and findings remain visible. Tool startup, invalid reports,
compilation, packaging, credentials, signatures and source/digest identity
failures remain blocking; a report-only publication is not a scan-clean receipt.

## Release-file manifests

The build producer supplies `dist/release-files.json` before handing its
distribution to signing. This versioned contract names the public `assets`
and classifies `checksums`, `sboms`, `evidence` and `provenance`; every classified
file also belongs in `assets`. Each entry has a relative `path` under `dist/`,
its basename as `name`, and a nonempty `source` label. Exactly one checksums
file is required. Producers can use any build tool and file naming convention.

For example, a JAR producer can supply:

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

`forge-ci release files declare` writes that hand-off for a producer that
names its files: the checksums file over every `--asset` and `--sbom` file,
then the manifest. A path may be a glob pattern, which the command expands, so
`--asset 'dist/*/*'` declares the per-platform binaries of `build go run` or
`build cargo run`. It refuses a pattern that names no file, an existing
manifest or checksums file, and a dist that already holds a signature or the
provenance statement. `release files import-goreleaser` is the same step for a
GoReleaser build.

`forge-ci release files validate` checks the manifest. Signing uses its
classified sections. `release sign --checksums-from-manifest` and
`release provenance --checksums-from-manifest` take the manifest's single
checksums file instead of a named one, and first check that it lists every
checksum subject exactly once with its actual SHA-256 digest; on a refusal
nothing is signed and no statement is written. The provenance statement is the
one `--checksums-file` produces from the same file. After signing, `forge-ci release files manifest`
finalizes that same declared file set, adding the required SLSA provenance and
its bundle, plus `.bundle` and `.sig` companions of declared assets. It preserves
the producer's classifications and reads no build-tool metadata.
When `release publish --strategy=reconcile` selects assets through a manifest,
it requires a finalized manifest: one that declares the provenance statement,
its bundle and the bundle of every `evidence` file. A producer's manifest is
refused before upload. Explicit `--asset` values instead select files directly;
`--strategy=recreate` uses a release assembly. See the
[publish command reference](cli-reference.md#forge-ci-release-publish).

For manifest-driven selection, a manifest named with `--manifest` or
`RELEASE_FILES_MANIFEST` must exist. Only an absent default
`dist/release-files.json` selects GoReleaser discovery, and a manifest that
exists but cannot be read or validated is refused.

### What covers each published file

Every file in the manifest's `assets` is published. The validate/sign/finalize
flow must establish the following coverage before publication:

| File | Covered by |
|---|---|
| a declared asset, an SBOM included | its line in the checksums file |
| the checksums file | its signature bundle |
| an `evidence` file | its own signature bundle, which finalization requires |
| the provenance statement | its signature bundle, which finalization requires |
| `<declared file>.bundle`, `<declared file>.sig` | it is the signature of that file |

Finalization checks file presence and classification, including the required
provenance and evidence bundles. It does not verify signature contents or
recheck checksum digests, and it does not require a checksums bundle. Complete
the checksum validation and configured signing steps before finalization;
successful finalization alone does not establish the coverage in this table.

The checksums file is written by the producer, before evidence is signed and
before the provenance statement exists, so it lists neither. Nothing else is
exempt: a file is a checksum subject whatever it is named, and a `.bundle` or
`.sig` file is a signature only next to the declared file it signs. The
`provenance` section admits one path, `<dist>/slsa-provenance.intoto.json`,
which the engine writes.

Finalization cannot tell a signature the signer wrote from a file that was in
the dist before signing, so the producer hands over an unsigned dist and the
signing job checks that first:

```bash
forge-ci release files validate --unsigned
```

It refuses a manifest that declares provenance or a signature, and a dist that
already holds the provenance statement or a `.bundle` or `.sig` of a declared
file. Run it before the first signing verb; after signing those files exist by
design. The signing verbs replace the sidecars of the files they sign.

### Importing GoReleaser output

Consumers using GoReleaser can convert its metadata to this contract after the
build, adding explicit evidence before signing:

```bash
forge-ci release files import-goreleaser --evidence-file dist/release-plan.json
```

Import creates `dist/release-files.json` from the publishable GoReleaser
artifacts and the explicitly named evidence files. Repeat `--evidence-file` for
additional evidence. Files must be regular files under the distribution directory;
an evidence file cannot reclassify an existing GoReleaser asset. Import
refuses an existing manifest to preserve its classifications, and a dist that
is not unsigned in the sense above: a signature GoReleaser wrote next to an
imported file is not the release signer's and is not published as one.

For flows without a producer manifest, `release files collect` and fresh
`release files manifest` writes retain their GoReleaser discovery behavior.
The `--assets-json` input reuses that collected asset list. Build-tool-neutral
producers provide the classified manifest directly instead of using discovery.

## Targets at a glance

| Target                    | Artifact types     | Authentication              |
|---------------------------|--------------------|-----------------------------|
| Maven Central             | Maven and Gradle JVM libraries | Signed release request, GPG key and Sonatype credentials |
| GitHub Packages (Maven)   | Maven and Gradle JVM libraries | `GITHUB_TOKEN` (automatic)  |
| GitHub Packages (NPM)     | NPM packages       | `GITHUB_TOKEN` (automatic)  |
| Container registries      | Container images   | `GITHUB_TOKEN` or registry-password |
| Apple App Store           | iOS / macOS apps   | App Store Connect API v2    |
| Google Play Store         | Android apps       | Service Account JSON        |

---

### NPM

Publishing requires an explicit version-specific `config.npm-tag`, normally
`release-{version}`. There is no movable default channel. See the
[npm publication policy](ecosystems.md#npm-artifact-first).

`forge-ci publish npm validate-tarball` reads packed metadata without executing
package code. CLI packages declaring `bin` retain the historical `dist/cli.js`
requirement. Libraries may declare a literal `exports` string naming an
unescaped canonical `./` file target, or a relative `main` path to a regular
file when `exports` is absent/null. Exports targets cannot contain backslashes,
percent-encoded paths, query/fragment syntax, empty/dot/parent segments or
`node_modules` segments. Conditional/subpath objects, arrays, other non-string
exports values, bare targets and noncanonical paths are refused rather than
guessed or replaced by `main`. The selected entry must exist as a regular file;
this bounded validation does not prove every export works. Keep a fresh
installed-package oracle for actual package behavior.

```bash
# Configure npm to use GitHub Packages
npm config set @your-github-org:registry https://npm.pkg.github.com
npm config set //npm.pkg.github.com/:_authToken YOUR_GITHUB_TOKEN
```

#### Containers

```bash
# Pull image
podman pull ghcr.io/<owner>/<repo>:v1.0.0
```

---

## Maven Central

Gradle uses exported Maven-layout files with separate signing and upload; see
[Gradle JVM package publishing](gradle-publishing.md). The Maven profile setup
below applies to Maven projects.

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

1. **Request a release**: push a SIGNED request tag:

   ```bash
   git tag -s release-request/v1.0.0 -m "Release v1.0.0"
   git push origin release-request/v1.0.0
   ```

   forge-ci verifies your signature (against the committed allowlist, when
   enabled), bumps the version + changelog, then **creates the immutable
   `v1.0.0` release tag once** at the bump commit. No tag is force-pushed or
   mutated. Your signed `release-request/v1.0.0` tag remains as the
   authorisation anchor, and the bot's release commit records you as the
   original tagger (`Release-Authorized-By` / `Co-authored-by` trailers).

   The forge-ci example release workflows trigger on `release-request/v*`.
   (SNAPSHOT/dev builds are a separate `workflow_dispatch` flow, described by
   the snapshot orchestrator, and do not use release-request tags.)

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

The release flow defaults to stable versions; `release-policy: stable-prerelease`
also admits immutable beta/RC releases with the same signed-request gate.
Branch snapshots use `release-snapshot-orchestrator.yml` and do not create
release tags or GitHub Releases. They support content-addressed npm versions
and opt-in Maven-format snapshots through `publish-maven-snapshots: true`.
Maven snapshots use ordinary CI authorization; immutable Central releases
require an allowlisted signed request.
See [snapshot publication](gradle-publishing.md#snapshots) for Maven, Gradle JVM
and Android-library usage and the separate Central snapshot endpoint.

---

## NPM Packages (GitHub Packages)

NPM publishing goes to `npm.pkg.github.com` and authenticates with the
auto-provided `GITHUB_TOKEN`; no separate npm token is needed. The
package name must use the `@<owner>/` scope, with `<owner>` matching
the lowercased GitHub repository owner.

Public-registry publishing to `npmjs.org` is not implemented today.
The `npmjs` `publish-to` value is reserved in the schema; current
config validation rejects it.

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

1. **Tag your release:**

   ```bash
   git tag -s release-request/v1.0.0 -m "Release v1.0.0"
   git push origin release-request/v1.0.0
   ```

2. **Workflow automatically:**
    - Runs `npm run build` when a `build` script exists
    - Packs the package tarball
    - Publishes the tarball to GitHub Packages

3. **Availability:**
    - Appears under the repository/organization packages in GitHub
    - Installable through `npm.pkg.github.com` after registry authentication

### Consuming Published Package

Users install with:

```bash
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

```yaml
jobs:
  publish-container:
    needs: build-maven
    uses: diggsweden/forge-ci/.github/workflows/publish-container.yml@v0.90.0
    permissions:
      contents: read
      packages: write
      id-token: write
      actions: read
    with:
      branch: main
      forge-ci-binary-ref: v0.90.0
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

The release orchestrator publishes containers to GHCR. For Docker
Hub, call `publish-container.yml` directly after the build job, set
the Docker Hub image name explicitly, and pass registry credentials
through the `registry-username` input and the `registry-password`
secret:

```yaml
jobs:
  publish-container:
    needs: build-maven
    uses: diggsweden/forge-ci/.github/workflows/publish-container.yml@v0.90.0
    permissions:
      contents: read
      packages: write
      id-token: write
      actions: read
    with:
      branch: main
      forge-ci-binary-ref: v0.90.0
      container-file: Containerfile
      artifact-types: maven
      registry: docker.io
      image-name: docker.io/dockerhub-org/my-app
      enforce-namespace: dockerhub-org
      registry-username: ${{ vars.DOCKERHUB_USERNAME }}
      use-ci-token: false
      sign-image: true
      sign-method: sigstore
    secrets:
      registry-password: ${{ secrets.DOCKERHUB_TOKEN }}
```

**Image naming:**

```text
docker.io/dockerhub-org/my-app:v1.0.0
```

### Custom Registry

Configure `REGISTRY_USER` as a variable and `REGISTRY_TOKEN` as a secret
authorized for the destination. This example retains the source owner's
namespace and repository name, producing `registry.example.com/source-org/my-app`.

```yaml
jobs:
  publish-container:
    needs: build-maven
    uses: diggsweden/forge-ci/.github/workflows/publish-container.yml@v0.90.0
    permissions:
      contents: read
      packages: write
      id-token: write
      actions: read
    with:
      branch: main
      forge-ci-binary-ref: v0.90.0
      container-file: Containerfile
      artifact-types: maven
      registry: registry.example.com
      registry-username: ${{ vars.REGISTRY_USER }}
      use-ci-token: false
      sign-image: true
      sign-method: sigstore
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

**Note:** iOS apps use `publish-to: []` because they don't publish to package
registries like Maven Central or npm. The App Store upload happens automatically
for signed Xcode artifacts and is skipped for unsigned archive-only builds.

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
      macos-version: macos-26
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

1. **Tag your release:**

   ```bash
   git tag -s release-request/v1.0.0 -m "Release v1.0.0"
   git push origin release-request/v1.0.0
   ```

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

Android requires an explicit supported SBOM selection: APK/AAB
`analyzed-artifact` scanning is not implemented. See
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

For gradual releases, use `inProgress` status with `user-fraction`:

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

1. **Tag your release:**

   ```bash
   git tag -s release-request/v1.0.0 -m "Release v1.0.0"
   git push origin release-request/v1.0.0
   ```

2. **Workflow automatically:**
   - Builds Android app with Gradle
   - Signs AAB with release keystore
   - Uploads to Google Play Console
   - Assigns to configured track

3. **Availability:**
   - Internal track: Immediately after upload
   - Alpha/Beta: After processing (~minutes)
   - Production: After review (first release) or immediately (updates)

---

## Security Features

Published artifacts can include these security features, depending on artifact
type and enabled workflow inputs:

- **GPG Signing** - Release checksums and package artifacts when signing is enabled
- **SBOM Generation** - CycloneDX/SPDX outputs for supported `sboms` layers
- **SLSA Provenance** - signed SLSA v1.0 provenance via cosign on compatible registries when signing and provenance are enabled; a signature alone does not establish a SLSA build level
- **Namespace Validation** - Confines image names to the configured destination namespace; registry authorization is separate

For verification instructions, see [Artifact Verification Guide](verification.md).
