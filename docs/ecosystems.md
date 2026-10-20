# Ecosystem support

This document explains forge-ci's ecosystem patterns and records current
support in per-ecosystem capability matrices. The linked workflows define
their direct-call contracts; [Artifacts Reference](artifacts-reference.md)
defines the orchestrator configuration.

## How forge-ci classifies ecosystems

Different language ecosystems have different deliverable shapes, so
forge-ci handles them in two complementary patterns. The pattern is
determined by **where platform commitment naturally happens** in the
language's build model.

### artifact-first: standalone deliverable built before publish

```text
source ─▶ build-<lang>.yml ─▶ artifact (JAR, tarball, binary, APK/AAB, IPA)
                                      │
                                      └─▶ optional supported container COPY
```

`build-<lang>.yml` produces the standalone deliverable before the publish stage.
Maven, npm, Gradle JVM, and artifact-first Go/Cargo outputs can also feed a
container via `from: [<artifact>]`; Android and Xcode outputs do not use that
container handoff.

**Implemented for:** maven, gradle, gradle-android, npm, xcode-ios, and go/cargo
(when `config.build-mode: artifact-first`).

### container-first: compiled-native, container is the build environment

```text
source ─┬─▶ sbom-<lang>.yml ─▶ Build SBOM (manifest-derived)
        │   (no compile, reads Cargo.lock / go.sum)
        │
        └─▶ publish-container.yml
              ├─ Containerfile builder stage compiles the binary
              ├─ runtime stage receives the binary
              └─ optional `extract.binary` re-uses the compile to
                 produce a CI artifact alongside the image
```

The Containerfile is the build environment. The runtime image is the
primary deliverable; the binary is a byproduct, optionally extracted as a
CI artifact via `extract.binary`. Platform commitment happens at
`--platform=` time on the container build, which is where it naturally
belongs for compiled-native code.

**Implemented for:** cargo, go (when `config.build-mode: container-first`).

### Why two patterns

Forcing compiled-native ecosystems into artifact-first would require
cross-compile machinery in CI (cargo-zigbuild, cross-rs, native-apt
sysroots), which is significant complexity for a problem that split-runner
multi-arch (`ubuntu-26.04` for amd64 + `ubuntu-26.04-arm` for arm64)
already solves inside the Containerfile. container-first accepts that the
container IS the build environment and orchestrates accordingly. The
naming reflects this: `build-<lang>.yml` for artifact-first (produces the
artifact), `sbom-<lang>.yml` for container-first (handles platform-agnostic
side-concerns; SBOM is one of them).

When a future language has both patterns available (e.g., python with
wheel as artifact-first or pyproject manifest SBOM as container-first), forge-ci
picks the pattern that matches how the language actually ships in
practice.

### SBOM placement reflects the pattern

Artifact-first ecosystems produce the artifact and its Build SBOM together in
`release-build-stage`; each artifact's `sboms` field controls that builder.
Container-first ecosystems have nothing to compile in the build stage, so their
manifest/lockfile-derived Build SBOM (`sbom-cargo.yml`, `sbom-go.yml`) runs in
`release-publish-stage` beside the container build. Those container-first SBOM
jobs run only when both the artifact policy and the effective `release.sboms`
policy include `build`. For pure Cargo/container-first Go projects,
`release-build-stage` runs zero jobs by design.

Cargo's SBOM is metadata/lockfile-derived, not an observation of each compiled
binary or container build. The generator uses the host target and default
features, which can differ from the actual build. See [Cargo SBOM scope](sbom.md#cargo-sbom-scope).

### Container-first reproducibility

`publish-container.yml` uses the supplied `source-date-epoch` or derives it once
from `git log -1 --format=%ct HEAD` in `prep`, then passes it as `SOURCE_DATE_EPOCH` on both the main
build and the optional `extract.binary` build. buildah (`--timestamp`) uses it
to normalize the image-config `created` field and layer timestamps. Identical
digests also require controlled base images, labels, build inputs and generated
content; a fixed tag and timestamp alone are insufficient. See
[Container reproducibility](verification.md#container-reproducibility-whats-stable-what-isnt).

What the env does NOT do: it doesn't auto-propagate as a Docker build-arg
into the build sandbox. The compile step inside the Containerfile (cargo
build, go build, …) won't see `SOURCE_DATE_EPOCH` unless the Containerfile
explicitly declares it. This is deliberate: it keeps forge-ci out of
caller policy decisions about what gets embedded in the binary.

**Container-first Go:** omit date-injecting ldflags unless the caller owns a
separate injection mechanism. `artifacts.yml` is parsed literally, so a
`containers[].build-args` value cannot reference the workflow-computed
`SOURCE_DATE_EPOCH`; writing a GitHub expression there passes the expression as
plain text. The reference `examples/go-service/Containerfile.example` therefore
uses reproducible flags without an embedded build date.

**Container-first Cargo:** pin the toolchain and lockfile, and keep `build.rs`,
native generators and other build inputs deterministic. `--locked` prevents
lockfile changes; it does not prevent a build script from reading the clock or
environment. If a script needs a build timestamp, supply a controlled value
such as `SOURCE_DATE_EPOCH` explicitly. The example at
`examples/cargo-app/Containerfile.example` is one build configuration, not a
guarantee for arbitrary Cargo projects.

## Per-ecosystem support matrices

Future ecosystem reviews should use the Cargo matrix structure so support can
be compared at a glance.

---

### Cargo (dual-mode)

| Property | Value |
|---|---|
| Pattern | dual-mode (artifact-first OR container-first per artifact) |
| project-type identifier | `cargo` |
| Canonical tool | cargo (Rust's universal package manager + build tool) |
| Status | Supported in the 0.90.0 baseline (container-first and artifact-first) |

Cargo mirrors Go's dual-mode shape: a Cargo artifact picks `build-mode:
artifact-first` or `build-mode: container-first`. Omitted defaults to
artifact-first (the binary-releasing shape); container-first is an explicit
opt-in.

#### When to pick which

**`build-mode: artifact-first`**, pick this when:
- The deliverable is a standalone CLI / tool binary released on GitHub Releases.
- The same binary is consumed by a container build (the binary lands in `dist/`
  and the Containerfile `COPY`s it in with no `cargo build` of its own).
- You want reproducible cross-compile binaries with attached SBOMs and signatures.

**`build-mode: container-first`**, pick this when:
- The Containerfile already runs `cargo build` itself (e.g. multi-stage build
  with a `builder` stage and a `runtime` stage).
- The container image is the primary deliverable; binaries leaving the container
  are byproducts (extracted via `extract.binary`, not a first-class release).
- You need OS-specific native deps from `apt-get install` during compile.

#### Capabilities

| Concern | Status | Workflow / Mechanism | Notes |
|---|---|---|---|
| Build (standalone deliverable) | ✅ (artifact-first) | `build-cargo.yml` (`cargo build --release --target …`) | Cross-compiles per `platforms` GOOS/GOARCH list. Each value maps to a Rust target triple. Output lands in `dist/<goos>-<goarch>/<binary>-<goos>-<goarch>`, matching Go's shape. |
| Build (container-first) | n/a | n/a | Compile happens inside Containerfile |
| Lint | external | caller's `test.yml` | Workspace-specific clippy, rustfmt, cargo-audit, or cargo-deny checks belong in the consumer repo |
| Test | external + opt-in `cargo test` (artifact-first) | caller's `test.yml` + `build-cargo.yml` (`cargo test --locked --all-targets`) | Release-build invocation is opt-out via `skip-tests`. Workspace features / testcontainers are still caller-owned. |
| SBOM: build layer (artifact-first) | ✅ | inline `cargo cyclonedx` in `build-cargo.yml` | Same generator as `sbom-cargo.yml`; emitted as a per-artifact upload alongside the binaries. |
| SBOM: build layer (container-first) | ✅ | `sbom-cargo.yml` | Lockfile-derived (reads `Cargo.lock`); runs in publish stage as a sibling of the container build. |
| SBOM: analyzed-artifact | ✅ (artifact-first) / conditional (container-first) | syft scan of binaries | Artifact-first: scans `dist/<goos>-<goarch>/...`. Container-first: scans extracted binaries when the container declares `extract.binary`. |
| SBOM: analyzed-container | ✅ | `publish-container.yml` (syft) | Standard for all containers; derived from `sboms` |
| Container build | ✅ | `publish-container.yml` | Native split-runner multi-arch (no QEMU) |
| Multi-arch (linux variants) | ✅ | split-runner matrix | linux/amd64 → `ubuntu-26.04`, linux/arm64 → `ubuntu-26.04-arm`, merged into one manifest list |
| Multi-arch artifact-first (linux x86 + arm) | ✅ | `build-cargo.yml` with `platforms: linux/amd64,linux/arm64` | The `runtime-rust-stable` image installs `aarch64-unknown-linux-gnu` target + `gcc-aarch64-linux-gnu` cross-linker. |
| Multi-arch artifact-first (macOS / Windows) | partial | runtime image extension required | The mapping table accepts darwin/windows triples; the default runtime image does not install osxcross / mingw-w64. Adopters extend the image or use a host runner with the right toolchain. |
| Binary extraction (CI artifact) | ✅ | `extract.binary` field | Opt-in; uploads one `${name}-binaries-${arch}` artifact per architecture |
| Multi-binary in one container | ✅ | `extract.binary.names: [a, b]` | E.g., `hsm-worker` + `digg-hsm-keytool`; basenames suffixed with `-linux-${arch}` to avoid release-asset collision |
| Workspace (multi-crate) | ✅ | sbom-cargo `--all`; bump-version `[workspace.package]` | One bump per release |
| Single-crate | ✅ | Same workflows; bump-version `[package].version` | |
| Version-bump (Cargo.toml + Cargo.lock) | ✅ | `forge-ci version bump --project-type cargo --version 1.2.3` | `cargo update --workspace` syncs lockfile |
| Release prerequisite checks | ✅ | `validate-release-prerequisites.yml` | Cargo.lock + toolchain pin; validation runs in the Rust runtime when Cargo artifacts are present |
| Publish: container to ghcr | ✅ | `publish-container.yml` | SLSA provenance + scan + analyzed-container SBOM |
| Publish: container to other OCI registries | ✅ | `publish-container.yml` | Signed SLSA provenance is portable, subject to OCI/cosign registry compatibility. It requires pushed images, enabled SLSA and artifact signing with `sign.method: sigstore` or `kms`. |
| Publish: crates.io (libraries) | ❌ | — | No crates.io publisher is provided |
| Standalone binary release (no container, GitHub Release attach) | ✅ (artifact-first) | `build-cargo.yml` + `release-create-github.yml` | Binaries land in `dist/`, get checksummed, SBOM'd, signed, and attached to the GitHub Release. |
| macOS binary distribution | partial | requires darwin runtime image | Mapping table covers `darwin/amd64`, `darwin/arm64`. Default runtime image is Linux; adopters needing darwin extend it or run a separate workflow on a macOS runner. |
| Windows binary distribution | partial | requires mingw-w64 in runtime image | Same model as macOS. |

#### Caller responsibilities

**Artifact-first only:**
- **`[[bin]]` or `[package].name` matches the binary you want**: the default. Override with `config.binary-name` only when the package builds multiple bins and you want one specific bin to be the deliverable.
- **`config.platforms` matches the runtime image's installed Rust targets + cross-linkers**: the default `runtime-rust-stable` image carries `linux/amd64` + `linux/arm64`. Adding `darwin/*` or `windows/*` needs an extended image or a non-default host runner.

**Container-first only:**
- **Per-service multi-stage `Containerfile`** with named stages: `builder`, optional `export-binary`, and a runtime stage referenced by `target:` in `artifacts.yml`.
- **Native build deps** inside the Containerfile builder stage (`apt-get install`).

**Both modes:**
- **Workspace tests** in a caller-owned workflow. `cargo test --workspace` can't be expressed per-artifact, and forge-ci does not invoke project tests automatically (artifact-first's opt-out test invocation is a release-build sanity gate, not a substitute for a PR test workflow).
- **Native lint deps** in the caller's Rust test/lint job: clippy compiles, so the same packages must be available there.
- **`Cargo.lock` checked in**: required for reproducible SBOM and release.
- **`rust-toolchain.toml` or `rust-toolchain` checked in**: required by release prerequisite validation.

#### See also

- [Container schema fields](artifacts-reference.md#container-optional-fields)
- [Cargo config fields](artifacts-reference.md#configuration-fields-cargo)
- [SBOM patterns](sbom.md)
- [Cargo example](../examples/cargo-app/)

---

### Maven (artifact-first)

| Property | Value |
|---|---|
| Pattern | artifact-first (platform-agnostic deliverable) |
| project-type identifier | `maven` |
| Canonical tool | Maven |
| Supported versions | **3.9.x and newer** |
| Status | Production |
| Tracked since | 0.90.0 baseline |

Maven 3.9 is the floor, matching the JDK runtime image
(`forge-ci-runtime-java-25` ships Maven 3.9.x). Older Maven is not tested
and not supported. The floor is not cosmetic: 3.9 is where the deploy plugin
moved to the Maven Resolver native transport, so behaviour a pipeline depends
on. Transport selection, and therefore how TLS and proxy settings are
honoured, differs from 3.8 and earlier.

#### Capabilities

| Concern | Status | Workflow / Mechanism | Notes |
|---|---|---|---|
| Build (standalone deliverable) | ✅ | `build-maven.yml` | Applications run `mvn clean package`; libraries run clean/compile, an explicit test phase, then package. Produces `.jar` (and optionally `-sources.jar`, `-javadoc.jar` for libraries) under `target/`. |
| Lint | external | caller's `test.yml` | Checkstyle, SpotBugs, PMD belong in the consumer repo's PR workflow. |
| Test | release build + external PR gate | `build-maven.yml` + caller's `test.yml` | Library builds explicitly run `mvn test`; application `mvn package` runs the normal test lifecycle. Both skip tests only when `skip-tests` is enabled. |
| SBOM: build layer | ✅ | `build-maven.yml` via cyclonedx-maven-plugin | A separate `makeAggregateBom` invocation follows the selected build lifecycle. See [Build tools](sbom.md#per-language-build-tools). |
| SBOM: analyzed-artifact | ✅ | syft scan of the produced `.jar` | Detects bundled dependencies (fat jars) the build-layer SBOM may miss. |
| SBOM: analyzed-container | ✅ | `publish-container.yml` (syft) | Standard for containers wrapping Maven artifacts. |
| Container build (wrapping the jar) | ✅ | `publish-container.yml` (artifact-first → container) | The build artifact is downloaded into the build context; the Containerfile `COPY`s it in. |
| Multi-arch container (same jar) | ✅ | split-runner matrix | The JAR is platform-agnostic; the runtime base image is what differs per arch. |
| Build reproducibility | prerequisite check | `validate jvm-reproducibility` checks `<project.build.outputTimestamp>` in pom.xml | Missing settings fail validation; the property is required. |
| Multi-module Maven (parent + children) | ✅ | one Maven artifact entry pointing at parent POM | Inheritance handles children; `mvn package` at the parent builds everything. |
| Library artifact (sources + javadoc) | ✅ | `build-type: library` selects the library lifecycle/profile | The project POM/profile must attach the sources and Javadoc JARs; forge-ci does not inject plugin goals. Maven Central validation checks that both exist. |
| Application artifact (executable jar) | ✅ | `build-type: application` (default) | The main jar only. |
| Version-bump (pom.xml + multi-module child POMs) | ✅ | `forge-ci version bump --project-type maven --version 1.2.3` | Uses `versions:set` semantics; multi-module aware. |
| Release prerequisite checks | ✅ | `validate-release-prerequisites.yml` | Confirms `MAVEN_CENTRAL_USERNAME`/`PASSWORD` set when publishing there; validates GPG availability when sign.method=gpg. |
| Publish: Maven Central | ✅ | `publish-maven-central.yml` | Sonatype Central Portal (the `central-publishing-maven-plugin`); requires `MAVEN_CENTRAL_USERNAME`, `MAVEN_CENTRAL_PASSWORD`, `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE`. |
| Publish: GitHub Packages | ✅ | `publish-maven-github.yml` | Uses auto-provided `GITHUB_TOKEN`; respects `<distributionManagement>` in pom.xml. |
| Publish: other OCI / private repo | partial | extend `publish-maven-github.yml` pattern | Not wired today; would need a per-repo `<settings.xml>` injection. |

#### Caller responsibilities

- **`pom.xml`** with declared `<groupId>`/`<artifactId>`/`<version>` and (for Maven Central) `<licenses>`, `<scm>`, `<developers>` per Maven Central requirements.
- **Library attachments in the project POM/profile**: configure pinned source and Javadoc plugins when those artifacts are required. `build-type: library` selects the lifecycle/profile but does not synthesize attachments.
- **`<project.build.outputTimestamp>`** in `pom.xml`'s `<properties>`: required by `forge-ci validate jvm-reproducibility`; missing settings fail the release prerequisite check.
- **GPG key material** committed neither to repo nor exported: set as repo/org secret per the workflow's declared inputs (`RELEASE_GPG_PRIVATE_KEY` etc.).
- **For multi-module**: register only the parent POM as an artifact (Maven inheritance handles children) or register children individually if each ships a separately-released variant.
- **Tests in a caller-owned PR workflow**: the release build runs Maven unit tests unless `skip-tests` is enabled. It does not replace a PR gate, and does not independently invoke `mvn verify` or integration tests.

#### See also

- [Maven config fields](artifacts-reference.md#configuration-fields-mavengradle)
- [Maven example](../examples/maven-app/)
- [Maven Central publishing setup](verification.md#release-authorisation)

---

### NPM (artifact-first)

| Property | Value |
|---|---|
| Pattern | artifact-first (platform-agnostic deliverable) |
| project-type identifier | `npm` |
| Canonical tool | npm (dominant package manager for the Node.js ecosystem) |
| Status | Production |
| Tracked since | 0.90.0 baseline |

#### Capabilities

| Concern | Status | Workflow / Mechanism | Notes |
|---|---|---|---|
| Build (standalone deliverable) | ✅ | `build-npm.yml` (`npm ci`, application build, SBOM, `npm pack`) | Produces a tarball under `<name>-<version>.tgz`; uploaded as the build artifact. Library shape (no build script) and app shape (with build script writing `dist/`) are both supported. |
| Lint | external | caller's `test.yml` | ESLint / Prettier belong in the consumer repo. |
| Test | blocking release check + external PR gate | `build-npm.yml` + caller's test job | The release build runs a configured `npm test` unless `skip-tests` is enabled. Failure stops before build, SBOM and packaging. An absent script is explicitly reported as skipped, never executed. |
| SBOM: build layer | ✅ | `@cyclonedx/cyclonedx-npm` via `npx` | Runs after the application build and before pack. See [Build tools](sbom.md#per-language-build-tools). |
| SBOM: analyzed-artifact | ✅ | syft scan of the packed `.tgz` | Reports dependencies detectable in the shipped tarball. |
| SBOM: analyzed-container | ✅ | `publish-container.yml` (syft) | Standard for containers wrapping NPM artifacts. |
| Container build (wrapping the tarball) | ✅ | `publish-container.yml` (artifact-first → container) | Containerfile `COPY`s the tarball or `dist/` into the runtime image. |
| Multi-arch container (same tarball) | ✅ | split-runner matrix | Node tarballs are platform-agnostic; native add-ons require their own multi-arch wheels (not handled here). |
| Build reproducibility | ✅ | npm ≥ 10 (npm/cli#3536 fix) | Runtime image pins Node 24 LTS; uses npm 10+. `package-lock.json` checked in is required. |
| Library shape (no build script) | ✅ | `npm pack` produces the publishable tarball directly | Matches scoped npm-package conventions. |
| App shape (build script writes dist/) | ✅ | Explicit application build before SBOM generation and `npm pack` | Configure the package's file selection to include the desired build output in the tarball. |
| Version-bump (package.json + package-lock.json) | ✅ | `forge-ci version bump --project-type npm --version 1.2.3` | Updates both files; preserves lockfile coherence. |
| Release prerequisite checks | shared release checks | `validate-release-prerequisites.yml` | No npm-specific token, lockfile or Node-pin validator. The build uses `npm ci`; registry authentication is configured in the publisher. |
| Publish: npmjs.com | snapshot only | `publish-snapshot-npm.yml` | Set `registry: https://registry.npmjs.org`, `use-ci-token: false` and the `registry-password` secret. Production `publish-to: npmjs` remains unsupported. |
| Publish: GitHub Packages | ✅ | `publish-maven-github.yml` (also handles npm scoped to `npm.pkg.github.com`) | Uses auto-provided `GITHUB_TOKEN`. |
| Publish: other private registries | snapshot only | `publish-snapshot-npm.yml` | Set `registry`, optional `package-scope`, `use-ci-token: false` and the `registry-password` secret. The workflow generates npmrc. |

#### Caller responsibilities

- **`package.json`** with declared `name` (scoped or unscoped), `version`, `main`/`exports`, and (for npmjs) `repository`, `license`, `description`.
- **`package-lock.json`** committed: required for reproducible builds and the build-layer SBOM.
- **`files:` allowlist in `package.json`** (recommended): keeps `npm pack` from including dev files like tests / source maps in the published tarball.
- **Registry configuration**: production `forge-packages` publishing generates a private temporary npmrc for the active forge. Direct snapshot callers select the registry/scope and provide its credentials through the workflow inputs and secrets.
- **Version-specific tags**: set `config.npm-tag: release-{version}` explicitly for npm package publication.
  The publisher reads the version from the packed tarball and publishes, for example, `release-1.2.3` or `release-1.2.3-rc.1`.
  A literal tag must match the registry version. npm removes SemVer build metadata from published versions;
  Forge CI warns before publication and uses the same normalized version for the tag.
  `latest`, `next`, `snapshot`, and other movable channel tags are refused; nothing selects a channel by default.
  An existing version-specific tag pointing elsewhere is refused before publication.
  This read-before-publish check is not atomic and does not prevent another registry client from changing tags.
- **Prebuilt publication**: the publisher disables npm lifecycle scripts with `--ignore-scripts`.
  Run package preparation during the build, before publication credentials are available.
- **Early checks**: configuration validation rejects a missing or invalid npm tag before building. Direct callers can run `forge-ci publish npm validate-tag --npm-tag 'release-{version}'` without a package, token, or network. The deploy command also checks the packed identity and existing registry tag before its publishing request. Packed `publishConfig` must not override the selected tag or registry.
- **Tests in a caller-owned PR workflow**: keep a real test job beside the lint gate. The release build also runs configured tests unless `skip-tests` is enabled, and test failures block packaging. A package without a test script is supported, but its summary records tests as skipped.

#### See also

- [NPM config fields](artifacts-reference.md#configuration-fields-npm)
- [NPM example](../examples/npm-app/)

---

### Gradle (artifact-first, JVM)

| Property | Value |
|---|---|
| Pattern | artifact-first (platform-agnostic deliverable) |
| project-type identifier | `gradle` |
| Canonical tool | Gradle |
| Status | Production |
| Tracked since | 0.90.0 baseline |

#### Capabilities

| Concern | Status | Workflow / Mechanism | Notes |
|---|---|---|---|
| Build (standalone deliverable) | ✅ | `build-gradle-app.yml` (wrapper, default `assemble`) | `config.gradle-tasks` selects tasks. The workflow uploads JARs from root/subproject `build/libs/`; WAR/distZip outputs need a caller-owned upload path. |
| Lint | external | caller's `test.yml` | detekt / ktlint / checkstyle belong in the consumer repo. |
| Test | task-dependent release check + external PR gate | selected Gradle tasks + caller's `test.yml` | The default `assemble` task does not run tests. Select `build` or explicit test tasks for release-build checks. |
| SBOM: build layer | ✅ | cyclonedx-gradle-plugin via init script | A separate SBOM invocation follows the selected tasks. See [Build tools](sbom.md#per-language-build-tools). |
| SBOM: analyzed-artifact | ✅ | syft scan of produced jars | |
| SBOM: analyzed-container | ✅ | `publish-container.yml` (syft) | |
| Container build (wrapping the jar) | ✅ | `publish-container.yml` | |
| Multi-arch container | ✅ | split-runner matrix | The JAR is platform-agnostic. |
| Build reproducibility | prerequisite check | `validate jvm-reproducibility` checks `preserveFileTimestamps = false` AND `reproducibleFileOrder = true` on `AbstractArchiveTask` | Missing either setting fails validation; supports both Kotlin and Groovy DSL forms. |
| Gradle wrapper required | ✅ | `gradlew` checked in | Pinned-version wrapper guarantees the same Gradle across CI and developer machines. |
| Version-bump (build.gradle / gradle.properties) | ✅ | `forge-ci version bump --project-type gradle --version 1.2.3` | Updates `version =` in build.gradle{,.kts} or `version=` in gradle.properties. |
| Release prerequisite checks | ✅ | `validate-release-prerequisites.yml` | Confirms JDK toolchain pin + wrapper presence. |
| Publish: Maven Central | ✅ JVM libraries | exported Maven-layout files, separate signing and Central API upload | Native `maven-publish`, immutable release version, sources/javadoc and Central metadata required; see [Gradle publishing](gradle-publishing.md). |
| Publish: GitHub Packages | ✅ JVM libraries | exported Maven-layout files, forge-native repository upload | `publish-to: [forge-packages]`; no Gradle execution in the publishing job. GitLab/Forgejo composition limits are documented in the [guide](gradle-publishing.md#gitlab-and-forgejo). |

#### Caller responsibilities

- **Gradle wrapper (`gradlew`)** committed: pinned to a specific Gradle version.
- **`gradle-tasks` in `artifacts.yml`**: explicitly name build tasks (e.g. `build`, `assemble`, `distZip`). Package publication is a separate exported-artifact phase; do not select a remote `publish` task as a build task.
- **Reproducibility settings on archive tasks**: see `validate jvm-reproducibility` for the exact form.
- **Tests in a caller-owned PR workflow**: the release workflow defaults to `assemble`. Select `config.gradle-tasks: build` or explicit test tasks if release-build tests are also required.

#### See also

- [Gradle config fields](artifacts-reference.md#configuration-fields-gradle)
- [Gradle example](../examples/gradle-app/)

---

### Gradle Android (artifact-first)

| Property | Value |
|---|---|
| Pattern | artifact-first (applications: APK / AAB; libraries: AAR) |
| project-type identifier | `gradle-android` |
| Canonical tool | Gradle + Android SDK |
| Status | Production |
| Tracked since | 0.90.0 baseline |

#### Capabilities

| Concern | Status | Workflow / Mechanism | Notes |
|---|---|---|---|
| Build (standalone deliverable) | ✅ | `build-gradle-android.yml` (`gradle assemble`) | Produces release APK (signed if `enable-signing: true`) and AAB (if `include-aab: true`, default). |
| Library build/publication | ✅ | `build-type: library`, native `maven-publish` export | Produces a release AAR; stable and snapshot publication to Central or forge packages uses exported files. See [Android libraries](gradle-publishing.md#android-libraries). |
| Lint | external | caller's `test.yml` | Android Lint + detekt belong in the consumer repo. |
| Test | external | caller's `test.yml` | Unit tests run via `gradle test`; instrumented tests need emulator (out-of-scope). |
| SBOM: build layer | ✅ | cyclonedx-gradle-plugin | Uses the Gradle init-script generator; see [Build tools](sbom.md#per-language-build-tools). |
| SBOM: analyzed-artifact | ❌ | `sbom assemble` rejects `gradle-android` | APK/AAB Syft scanning is not implemented; set `sboms: build` or `build,analyzed-container`. |
| Build reproducibility | ✅ | same archive settings as Gradle JVM | |
| Signing (APK + AAB) | ✅ | `config.enable-android-signing: true` reads ANDROID_KEYSTORE et al. from caller secrets | `false` only skips keystore injection; requested release tasks and uploads still run and may produce unsigned outputs. |
| `secrets.properties` injection | ✅ | Base64-encoded via `SECRETS_PROPERTIES_BASE64` secret | Decoded into `secrets.properties` at build time for Google Maps API keys etc. |
| Product flavor selection | ✅ | `product-flavor` config field | E.g., `staging`, `production`. |
| Build-type subset | ✅ | `build-types: debug,release` (default) | Pick which variants to build. |
| Version-bump (Android `versionName` / `versionCode`) | ✅ | `forge-ci version bump --project-type gradle-android --version 1.2.3` | Bumps `versionName` and increments `versionCode` together. |
| GitHub Release selection | Application AAB or library AAR | release artifact-transfer plan | Applications require release/AAB selection; libraries use their AAR artifact. APK uploads remain run artifacts. |
| Release prerequisite checks | ✅ | `validate-release-prerequisites.yml` | Confirms keystore secrets present when `enable-signing: true`. |
| Publish: Google Play | ✅ | `publish-google-play.yml` | Track-aware (`internal` / `alpha` / `beta` / `production`); supports staged rollouts. |
| Whats-new notes | ✅ | `whats-new-directory` config field | Localised release notes. |
| Mapping file (Proguard / R8) | ✅ | `mapping-file` config field | Symbolicated stack traces in Play Console. |
| Native debug symbols | ✅ | `debug-symbols` config field | Required for native (NDK) crashes. |
| In-app updates priority | ✅ | `google-play-update-priority` config field | 0–5. |

#### Caller responsibilities

- **Application signing keystore**: set as repo/org secret per the declared inputs (`ANDROID_KEYSTORE`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`). Libraries do not use an Android keystore.
- **Explicit Android SBOM policy**: exclude `analyzed-artifact` until APK/AAB scanning is implemented.
- **Signing policy**: enable Android signing for distributable release outputs. forge-ci does not reject an unsigned release APK/AAB or the `google-play` combination at config-parse time.
- **Service-account JSON** for Google Play (`GOOGLE_PLAY_SERVICE_ACCOUNT_JSON`): required when publishing.
- **`build-module`** config field: multi-module Android projects need this to point at the app module (Gradle's task tree isn't auto-discovered).
- **NDK setup**: if your build needs the NDK, provide it in the selected build runtime. `enable-ndk` is not an artifacts.yml config field.
- **Gradle wrapper + reproducibility settings**: same as Gradle JVM.

#### See also

- [Gradle Android config fields](artifacts-reference.md#configuration-fields-gradle-android)
- [Android app example](../examples/android-app/)
- [Android-library configuration and snapshots](gradle-publishing.md#android-libraries)
- [Google Play workflow](../.github/workflows/publish-google-play.yml)

---

### Xcode iOS (artifact-first)

| Property | Value |
|---|---|
| Pattern | artifact-first (iOS/macOS-specific deliverable: IPA) |
| project-type identifier | `xcode-ios` |
| Canonical tool | Xcode + xcodebuild |
| Status | Production |
| Tracked since | v0.90.0 |
| Runner requirement | macOS (`macos-26` by default; override via `macos-version` config field) |

#### Capabilities

| Concern | Status | Workflow / Mechanism | Notes |
|---|---|---|---|
| Build (standalone deliverable) | ✅ | `build-xcode-ios.yml` (`xcodebuild archive` + `xcodebuild -exportArchive`) | Produces an IPA. |
| Lint | external | caller's `test.yml` | SwiftLint via `lint-swift.yml` is available as a sibling workflow. |
| Test | external | caller's `test.yml` | `xcodebuild test` belongs in the consumer repo. |
| SBOM: build layer | ❌ | not implemented | Defaults to `sboms: none`; explicitly requesting the Build layer fails assembly. |
| Code signing (DER cert + provisioning profile) | ✅ | base64-encoded secrets imported into ephemeral keychain | `build xcode-ios run` deletes the keychain and removes the staged certificate, profile and xcconfig on every exit, including failures. |
| Ephemeral keychain isolation | ✅ | keychain password minted per run; nothing to provision | The run's keychain is appended to the user search list, not substituted for it, and the prior list is restored afterwards. `security(1)` takes passwords only as argv, so the run mints its own rather than exposing an operator secret there. The workflows still declare a `KEYCHAIN_PASSWORD` secret so existing callers keep parsing; nothing reads it. |
| xcconfig overrides | ✅ | base64-encoded `XCCONFIG_BASE64` secret | For team ID, bundle ID, app group, etc. |
| Workspace vs project | ✅ | `workspace` and `project` config fields | One must be set; forge-ci doesn't auto-detect. |
| Scheme selection | ✅ | `scheme` config field | Required; `.xcscheme` must be shared in the project. |
| Version-bump | ✅ (marketing version) | `forge-ci version bump --project-type xcode-ios --version 1.2.3` | Creates/updates `MARKETING_VERSION` in `versions.xcconfig` by default. It does not increment `CURRENT_PROJECT_VERSION`. |
| Release prerequisite checks | ✅ | `validate-release-prerequisites.yml` | Confirms App Store Connect secrets present when publishing. |
| Publish: App Store Connect | ✅ | `publish-apple-appstore.yml` | Validates unless skipped, then uploads the IPA with `xcrun altool`. The `submit-for-review` value records intent in the summary only. |
| App Store review submission | manual | App Store Connect | The workflow does not submit a build for review; `submit-for-review: true` only changes the post-upload guidance. |

#### Caller responsibilities

- **Signing certificate (.p12)**: base64-encoded as `IOS_SIGNING_CERTIFICATE_BASE64`; passphrase as `IOS_SIGNING_CERTIFICATE_PASSPHRASE`.
- **Provisioning profile (.mobileprovision)**: base64-encoded as `PROVISIONING_PROFILE_BASE64`.
- **App Store Connect API key (.p8)**: base64-encoded as `APP_STORE_CONNECT_API_PRIVATE_KEY_BASE64`; key ID + issuer ID as plaintext secrets.
- **Workspace or project + scheme**: declared in artifacts.yml `config:`.
- **Tests in a caller-owned PR workflow**: `xcodebuild test` is consumer-owned.
- **macOS runner availability**: iOS builds require macOS runners which are billed differently than Linux runners on GitHub.

#### See also

- [Xcode config fields](artifacts-reference.md#configuration-fields-xcode-iosmacos)
- [App Store Connect workflow](../.github/workflows/publish-apple-appstore.yml)

---

### Python (not currently supported)

| Property | Value |
|---|---|
| Pattern | TBD |
| project-type identifier | `python` (rejected at config-parse time) |
| Status | Not supported. Setting `project-type: python` in artifacts.yml fails validation with "must be one of: maven, npm, gradle, gradle-android, xcode-ios, go, cargo, meta". |

The `projecttype.Python` constant exists in the Go codebase as
scaffolding for future support (file-pattern detection, SBOM dispatch,
release-name resolution) but is intentionally **omitted from
`ValidProjectTypes`** in `internal/domain/config/schema.go` until a
build/publish workflow ships. The previous "Reserved but recognised"
state silently produced nothing at release time, replaced with
loud failure now that the alternative is worse than fixing the typo
or removing the artifact.

Python's build-tool landscape (pip, poetry, uv, hatch, flit, setuptools)
has no canonical winner, so the eventual choice between artifact-first
(build wheel, COPY into container) and container-first (manifest-based
SBOM only, container does the build) depends on the first real caller's
shape. Re-enabling Python is a one-line change to `ValidProjectTypes`
(and the JSON Schema enum) once an implementation lands.

---

### Go (artifact-first or container-first)

| Property | Value |
|---|---|
| Pattern | Explicit per artifact: `artifact-first` for standalone binaries, `container-first` for services built inside Containerfile |
| project-type identifier | `go` |
| Canonical tool | go (Go's universal build tool) |
| Status | Production |
| Tracked since | 0.90.0 baseline (container-first and artifact-first) |

Go's `config.build-mode` tells the orchestrator whether to run
`build-go.yml` in the build stage or `sbom-go.yml` in the publish stage.
Omitted defaults to `artifact-first`; `container-first` is an explicit
opt-in. Go and Cargo are the ecosystems where the caller picks the flow,
for every other ecosystem, the pattern is fixed by the deliverable shape.

#### Capabilities

| Concern | artifact-first | container-first |
|---|---|---|
| Build (binary) | ✅ `build-go.yml` cross-compiles `dist/<goos>-<goarch>/<binary>-<goos>-<goarch>` | ✅ Buildah runs the Containerfile per native platform with `TARGETOS`/`TARGETARCH`. |
| Cross-platform binaries | ✅ Configurable via `platforms` (default `linux/amd64,linux/arm64,darwin/amd64,darwin/arm64`) | n/a (Linux container only) |
| Build flags / ldflags | ✅ `config.build-tags`, `config.ldflags`, `-trimpath` default | Set in Containerfile's `RUN go build`. |
| Build SBOM | ✅ `build-go.yml` runs `forge-ci sbom build --project-type go` (cyclonedx-gomod) | ✅ `sbom-go.yml` runs the same, in the publish stage |
| SBOM: analyzed-artifact | ✅ syft on each per-arch binary | partial, only when `extract.binary` is set |
| SBOM: analyzed-container | ✅ when wrapped in a container and requested by `sboms` | ✅ `publish-container.yml` (syft on pushed image) |
| Container image | optional; `publish-container.yml` downloads `<name>-go-build-artifacts` into the build context's `dist/` and the Containerfile COPYs the binary in | ✅ primary deliverable |
| Binary extraction (CI artifact) | n/a (binaries already are the artifact) | ✅ `containers[].extract.binary` re-extracts the compiled binary as a separate CI artifact alongside the image |
| Multi-binary per container | n/a (one artifact = one binary) | ✅ `extract.binary.names: [a, b]` |
| Reproducibility | ✅ `-trimpath` + `SOURCE_DATE_EPOCH`-derived ldflags (`-X main.date={{.CommitDate}}`); Go module/build cache | Container image timestamps are pinned by Buildah; binary flags and timestamps are Containerfile-owned |
| Skip-tests opt-in | ✅ `config.skip-tests: true` | n/a (tests run during Containerfile build if the Containerfile invokes them) |
| Version-bump (go.mod doesn't store version) | partial, no version-bump-go workflow, uses tag-only | partial, same |
| Release prerequisite checks | ✅ `validate-release-prerequisites.yml` | ✅ same |
| Publish: standalone release binaries | ✅ via `release-create-github` (named binaries attached to GitHub Release) | n/a |
| Publish: container to ghcr | optional, wrap the artifact-first binary in a container | ✅ primary path |
| Publish: module proxy / `go install`-able | ✅ implicit (tag = module version per Go's contract) | ✅ implicit |
| govulncheck / golangci-lint / staticcheck | external | external (in caller's PR workflow) |

#### When to pick which

| Question | artifact-first | container-first |
|---|---|---|
| Primary deliverable is a CLI binary consumers `go install` or download? | ✅ pick this | ❌ over-engineered |
| Primary deliverable is a service consumers run as a container? | ❌ binary becomes a byproduct | ✅ pick this |
| Need macOS/Windows binaries? | ✅ artifact-first builds them | ❌ container-first is Linux-only |
| Need CI to build a multi-arch container with the compiled binary? | ✅ artifact-first cross-compiles in CI, container layer just COPYs | ✅ container-first compiles per-arch in the runtime; uses split-runner matrix |
| Want a single `go build` step you can also run locally without a container builder? | ✅ matches `go build` directly | ❌ requires a container build to compile |
| Sigstore-keyless OIDC signing on the binary? | ✅ release-create-github + `sign.method: sigstore` | ✅ `publish-container.yml` signs the image, not the binary |

#### Caller responsibilities

- **`go.mod`** with module path matching the repo (Go's contract for tag-based versioning).
- **`go.sum`** committed: required for reproducible SBOM.
- **For artifact-first**: `config.main-package` defaults to `.`; set it explicitly for a command under `./cmd/`. See [Go config fields](artifacts-reference.md#configuration-fields-go) for `binary-name` and `platforms` defaults.
- **For container-first**: a Containerfile with a multi-stage builder stage that respects `TARGETOS`/`TARGETARCH`; optional `target:` selecting the runtime stage.
- **Tests / lints / vulnerability checks** in a caller-owned PR workflow (`go test`, `go vet`, `govulncheck`, `staticcheck`, golangci-lint).

#### See also

- [Go CLI example (artifact-first)](../examples/go-cli/)
- [Go service example (container-first)](../examples/go-service/)
- [Go config fields](artifacts-reference.md#configuration-fields-go)

---

### Meta (no buildable artifact)

| Property | Value |
|---|---|
| Pattern | n/a, no artifact to build |
| project-type identifier | `meta` |
| Status | Production |
| Tracked since | v0.90.0 |

The `meta` project-type exists for repositories that have a release
cadence (changelog generation, tagged GitHub Releases, signed release
commits) but **no buildable artifact**. Used internally by
`forge-ci`'s own `self-release.yml`. The actual binaries come
from a separate `release-binary.yml` (goreleaser); the orchestrator
handles changelog + GitHub Release creation.

External use cases:
- Documentation-only repos that publish versioned docs but don't build software
- Specification repos (the spec text IS the deliverable; no compile step)
- Configuration-repos used as central sources of truth (e.g., a renovate config repo)
- "Bundle" / "umbrella" repos that consume other artifacts and merely tag a coherent release set

#### Capabilities

| Concern | Status | Notes |
|---|---|---|
| Build | n/a | `release-build-stage.yml` runs zero jobs |
| SBOM | n/a | `sboms: none` enforced |
| Container | n/a | No `containers:` block honoured |
| Publish | n/a | `release-publish-stage.yml` runs zero jobs |
| Changelog generation | ✅ | `generate-changelog.yml` runs against the meta artifact |
| GitHub Release creation | ✅ | `release-create-github.yml` creates the release |
| Tag signing | ✅ | Same `validate tag signature` flow as any other ecosystem |
| `require-authorization` allowlist | ✅ | Same `.forge-ci/allowed_signers` enforcement |

#### Caller responsibilities

- **`artifacts.yml`** with at least one artifact of `project-type: meta`. No `config:` block (none would apply).
- **Changelog tooling** if used (git-cliff or another `changelog-creator`).
- **A meaningful release notes flow**: the value of a meta release is the human-readable changelog + the signed tag.

---

## Adding a new ecosystem

When adding support for a new ecosystem:

1. Pick the pattern that matches how the language actually ships in
   production (don't force-fit).
2. Add the project-type identifier to `internal/domain/config/schema.go`
   (`ValidProjectTypes`, and `SBOMSupportedTypes` if it produces SBOMs).
3. For artifact-first: add `build-<tool>.yml`. For container-first: add
   `sbom-<tool>.yml` plus document the caller's Containerfile contract.
4. Wire into `release-build-stage.yml` matrix (if a build/sbom workflow
   produces something) and/or `release-publish-stage.yml` (if a new
   publisher is needed).
5. Add a section to this document with the same matrix structure as the
   cargo section above.
6. Add a working example under `examples/<ecosystem>-app/`: its
   `artifacts.yml` and README, and a `github/`, `gitlab/` or `forgejo/`
   directory for each forge that builds it.

The matrix structure is uniform on purpose: a reader comparing two
ecosystems can scan the same row labels and immediately see what differs.
