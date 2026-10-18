# Gradle JVM and Android-library publishing

This source revision adds artifact-first Gradle publications. Use matching engine,
workflow/component and runtime revisions; an older released runtime does not
provide the new commands.

## Supported scope

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

## Configure the project

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

## GitHub release configuration

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

## GitLab and Forgejo

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

Generated GitLab publish child pipelines still refuse the unsupported sibling
artifact handoff. Component availability does not imply generated-pipeline support.

Forgejo consumers compose the same CLI phases in their own trusted jobs:
`build gradle export-publications`, followed by the chosen publisher. The lab and
provider resolver supply the native Maven endpoint; no GitHub URL is hardcoded in
the native uploader. There is no new Forgejo Gradle orchestrator.

## Central lifecycle and recovery

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

The legacy Maven-profile path remains available. Its successful process exit
reports completion of that profile, not an independently observed Central
publication state.

Native Maven deployment runs trusted consumer project code with credentials.
Its secret-free `--validate-only` preflight is an early check; actual deployment
also resolves and validates the reactor with the deployment environment before
running `deploy`, including the same `skipTests` property. This catches profiles
activated by publication credentials. It does not sandbox Maven extensions or
plugins, nor prevent project code from changing files or versions during the
lifecycle. Use exported-file publication when that stronger boundary is required.

## Android libraries

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

## Snapshots

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
