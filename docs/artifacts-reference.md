# Artifacts Reference

Complete reference for the artifacts config format.

> **Which version to pin:** snippets here pin `v0.90.0`; replace it with the
> release you install, as [Which version to pin](README.md#documentation-index)
> describes. For unreleased changes, follow
> [Branch Testing](runtime-images.md#branch-testing).

---

> For per-ecosystem capability matrices and the artifact-first vs container-first framing, see **[docs/ecosystems.md](ecosystems.md)**.

## Location

The canonical path is **`.forge-ci/artifacts.yml`**. The release-orchestrator reads it by default, so operators no longer need to pass `artifacts-config:` explicitly. To use a different path, set `artifacts-config: <path>` on the workflow_call.

Co-located with the other forge-ci files:

```text
.forge-ci/
├── artifacts.yml                       ← the release plan
├── artifacts.schema.json               ← JSON Schema for editor autocomplete
├── allowed_signers                     ← release-authorisation (SSH)
└── allowed_gpg_keys.asc                ← release-authorisation (GPG)
```

### JSON Schema

[`.forge-ci/artifacts.schema.json`](../.forge-ci/artifacts.schema.json) is a Draft 2020-12 schema for editor completion and structural checks. `forge-ci config schema > .forge-ci/artifacts.schema.json` writes the one your binary validates against. Most editors honour the `# yaml-language-server: $schema=…` directive at the top of a YAML file:

```yaml
# yaml-language-server: $schema=./artifacts.schema.json
artifacts:
  - name: my-artifact
    project-type: maven
```

The schema is contract-tested against shipped `examples/*/artifacts.yml`, but
does not replace `config parse-artifacts`. Ecosystem-specific `config` blocks
remain open in the schema; runtime parsing rejects unknown keys and validation
checks supported combinations.

Copy `.forge-ci/artifacts.schema.json` from the reviewed Forge CI revision that
matches your workflow and executor into your project's `.forge-ci/` directory.
The relative directive above resolves beside `artifacts.yml`. If using a remote
schema instead, pin its URL to that same immutable source revision rather than
`main`; a moving schema can accept fields an older executor does not support.
Coordinate schema updates with the [runtime and CLI pins](runtime-images.md#advanced-pinning-recipe).

## Auto-derive: skip the file for simple repos

When `.forge-ci/artifacts.yml` is absent AND the repo has **exactly one** ecosystem manifest at root, forge-ci synthesises the equivalent plan in-memory:

| Manifest at root | Synthesised |
|---|---|
| `pom.xml` | one Maven artifact, name = `artifactId`, working-directory = `.` |
| `package.json` | one NPM artifact, name = unscoped package name |
| `Cargo.toml` | one Cargo artifact, name = `package.name` |
| `go.mod` | one Go artifact, name = basename of `module` path |
| `build.gradle` or `build.gradle.kts` | one Gradle artifact, name = `rootProject.name` (or repo dir name as fallback) |

This collapses the common "single-library repo" case to zero configuration. **Polyglot repos** (more than one root manifest) refuse to auto-derive and demand an explicit `artifacts.yml`. The error names every manifest it found.

Auto-derive synthesises no container, so no image is released from it. The
release-images plan (`forge-ci container release-images plan`, which the
Forgejo `release-images.yml` runs) is the exception: called without an
`artifacts.yml`, it releases the repository's own image, one container named
after the repository and built from the root `Containerfile` with every
default (`linux/amd64`, scanned). Calling it is the repository's choice to
release images; declare `containers:` for anything else.

## Overview

`artifacts.yml` has two main sections:

```yaml
artifacts:
  - name: my-artifact
    # ... artifact configuration

containers:
  - name: my-container
    from: [my-artifact]
    # ... container configuration
```

### Configuration troubleshooting

- **`artifacts.yml` not found:** match the workflow's `artifacts-config` input
  to the actual path; the default is `.forge-ci/artifacts.yml`.
- **Containerfile not found:** check each container's `container-file` and
  `context` against the checkout layout.
- **Build succeeded but nothing was published:** select a supported `publish-to`
  target for package-registry output, or declare `containers` for image output.
  `forge-packages` serves NPM packages and supported libraries. Maven application
  JARs may be release assets, but are not Maven registry packages; their images
  require a separate container declaration.

## Signing Vocabulary

Artifact signing and Git signing are separate top-level policies. The following
blocks track closed runtime/schema vocabularies, not support for every combination
or provider.

### `sign.method`
<!-- schema-values:sign.method -->
`gpg`, `sigstore`, `kms`
<!-- /schema-values:sign.method -->

On Forgejo and GitLab, `gpg` signs the release's checksums and SBOMs with
the OpenPGP key, as does [`sign.files-method: gpg`](#signfiles-method), and
the provenance, the evidence and the images with the Cosign key. A
declaration there without a `sign` section, or with another method, has
all of them signed with the Cosign key; see
[Forgejo signing](forgejo.md#signing) and
[GitLab signing](gitlab.md#signing). A method or signing key a forge's release does
not use is not refused: the release plan and `doctor` warn about it, and name
the key that signs instead
([signing methods](verification.md#signing-methods-for-release-artifacts)).

### `sign.files-method`

`gpg`, or omitted.

Omitted, `sign.method` signs the release files and the container images
alike. Set to `gpg`, the release files get OpenPGP `.asc` signatures, and
`sign.method` signs the images and their SLSA provenance alone:

```yaml
sign:
  method: sigstore    # images: Cosign signatures and SLSA provenance
  files-method: gpg   # release files: .asc, nothing in the transparency log
```

- `sign.method` must then be `sigstore` or `kms`. Beside `gpg`, written or by
  default, the field is refused: that method signs the files with GPG already.
- No other value is accepted. A second Cosign method for files would need a
  key, issuer and transparency choice of its own.
- `sign.key`, `sign.oidc-issuer` and `sign.transparency` keep describing
  `sign.method`, so here they apply to the images.
- The release needs the `RELEASE_GPG_*` secrets for the files beside whatever
  `sign.method` needs for the images. On the GitHub release, a declaration
  without a container then signs nothing with Cosign, and its plan asks for
  no id-token, Cosign key or Sigstore endpoint. The Forgejo and GitLab
  releases sign the provenance and the evidence with the Cosign key all the
  same, so their Cosign secrets stay required.
- The GitHub, Forgejo and GitLab releases sign the files with GPG. The
  Forgejo and GitLab releases sign the images with their Cosign key,
  whatever `sign.method` says.

### `sign.transparency`
<!-- schema-values:sign.transparency -->
`public`, `none`
<!-- /schema-values:sign.transparency -->

The GitHub release reads it from the declaration. The GitLab and Forgejo
releases do not: their `transparency-log` input decides, `none` by default.
A declaration that sets `sign.transparency` is not refused there, and their
release plans and `doctor` warn that the value is not used.

### `git-signing.method`
<!-- schema-values:git-signing.method -->
`gpg`, `ssh`
<!-- /schema-values:git-signing.method -->

### `sign.key` URI Schemes
<!-- schema-values:sign.key schemes -->
`awskms`, `gcpkms`, `azurekms`, `hashivault`, `pkcs11`, `file`
<!-- /schema-values:sign.key schemes -->

Beside these schemes, `sign.key` accepts exactly `env://COSIGN_KEY`: a Cosign
key the layer hands its signing steps as the `RELEASE_COSIGN_PRIVATE_KEY`
secret. No other environment variable is accepted, because Cosign would
receive it.

The `file` scheme is an explicit local-development/integration option, not a
claim that a local key is a KMS. Runtime validation also checks method-specific
requirements and incompatible fields.

## Artifacts Section

### Artifact Required Fields

#### Artifact `name`

- **Type:** `string`
- **Description:** Unique identifier for this artifact
- **Used for:** Referencing in containers `from:` field, artifact upload names
- **Example:** `backend-api`, `frontend-ui`, `shared-lib`

#### `project-type`

- **Type:** `string`
- **Description:** Build system type
<!-- schema-values:project-type -->
- **Valid values:** `maven`, `npm`, `gradle`, `gradle-android`, `xcode-ios`, `go`, `cargo`, `meta`
<!-- /schema-values:project-type -->
- **Note:** for `go` and `cargo`, `config.build-mode` selects the pipeline shape; omitted defaults to `artifact-first`.
- **Example:** `project-type: maven`

---

### Artifact Optional Fields

#### `working-directory`

- **Type:** `string`
- **Description:** Path to project root (relative to repository root)
- **Default:** `.` (repository root)
- **Contains:** `pom.xml` (Maven), `package.json` (NPM), `build.gradle` (Gradle)
- **Example:** `.`, `services/backend`, `packages/frontend`

#### `build-type`

- **Type:** `string`
- **Description:** Application or library role used for build behavior and publication eligibility.
<!-- schema-values:build-type -->
- **Valid values:** `application` (default), `library`
<!-- /schema-values:build-type -->
- **Default:** Omit the key to use the application build lifecycle. Explicit empty
  strings (`""`, `''`) and YAML nulls (`null`, `~`, or a bare `build-type:`) are
  invalid. Omission remains unset in the plan and preserves Maven
  `forge-packages` eligibility when requested; explicit `application` excludes
  the artifact from that publish set. Maven Central still requires explicit
  `library`.
- **Applies to:** Maven lifecycle, Central publication eligibility, and Android application/library mode. JVM Gradle build tasks are selected by `config.gradle-tasks`.
- **Example:** `build-type: library`
- **Behavior:**
  - `application`: Builds with `mvn package`
  - `library`: Selects the compile/test/package lifecycle and optional Maven profile. The project POM/profile owns attached sources and javadoc artifacts; forge-ci does not invoke source or javadoc plugin goals. Maven Central validation later requires both JARs.
  - Android `library`: Builds a release AAR, exports native Maven publications, and bumps `version` rather than application `versionName`/`versionCode`. It uses neither an Android keystore nor Google Play. See [Android libraries](publishing.md#android-libraries).

#### `require-authorization`

- **Type:** `boolean`
- **Description:** Require the release tag to be signed by a key listed in the project's committed allowlist files (`.forge-ci/allowed_signers` for SSH-signed tags, `.forge-ci/allowed_gpg_keys.asc` for GPG-signed tags).
- **Default:** `false`
- **Use case:** Production libraries that need cryptographic proof of release authorship.
- **Example:** `require-authorization: true`
- **Effect:** When true, a missing or empty allowlist file fails the release closed with `EX_NOPERM` (exit 77). When the file is present, the signing-key fingerprint must match an entry. See [verification.md → Release Authorisation](verification.md#release-authorisation) for the file format and a worked example.

#### `publish-to`

- **Type:** `array of strings`
- **Description:** Publishing targets for built artifacts
- **Default:** `[]` (no package-registry publisher; container publishing is configured separately under `containers[]`)
<!-- schema-values:publish-to -->
- **Schema values:** `maven-central`, `forge-packages`, `google-play`, `npmjs`
<!-- /schema-values:publish-to -->
- **Reserved value:** `npmjs` is schema-recognized for future use, but current
  config validation rejects it because production npmjs.org publishing is not
  implemented yet. Use `forge-packages` for current NPM package publishing.
- **Example:**

  ```yaml
  publish-to:
    - forge-packages
    - maven-central
  ```

- **Example (Android):**

  ```yaml
  publish-to:
    - google-play
  ```

- **Behavior:** Workflows only run if target is listed
- **Note:** iOS apps use `publish-to: []` as signed Xcode artifacts publish via App Store Connect automatically; unsigned archive-only builds skip App Store upload

#### `sboms`

- **Type:** `string` (enum / comma-list)
- **Description:** Which CISA SBOM types to generate for this artifact. See [docs/sbom.md](sbom.md) for the full taxonomy.
<!-- schema-values:sboms layer -->
- **Grammar tokens:** `all`, `none`, `build`, `analyzed-artifact`, `analyzed-container`
<!-- /schema-values:sboms layer -->
- **Accepted values:**
  - `all`: request Build + Analyzed-artifact + Analyzed-container (default for supported project types; ecosystem limitations still apply)
  - `none`: skip SBOM generation entirely
  - `build`: CISA Build SBOM only (cyclonedx plugin during build)
  - `analyzed-artifact`: Syft scan of the built artifact
  - `analyzed-container`: Syft scan of the published container only
  - Any comma-list of the three layer names, e.g. `build,analyzed-artifact`
- **Default:** Automatic based on project type:
  - `all` for: `maven`, `npm`, `gradle`, `gradle-android`, `cargo`, `go`
  - `none` for: `xcode-ios`, `meta`
- **Examples:**

  Default for a supported project type:

  ```yaml
  sboms: all
  ```

  Build layer only:

  ```yaml
  sboms: build
  ```

  Disable SBOM generation for this artifact:

  ```yaml
  sboms: none
  ```
- **Formats produced:** See [SBOM formats and schema versions](sbom.md#consuming-sboms); versions depend on the generator and its configuration.
- **Release policy:** The production `release.sboms` input (default `all`) is intersected with the pipeline-wide union. It selects release-bundle layers and gates container-first Go/Cargo Build SBOM jobs. It does not rewrite per-artifact `effective-sboms`, disable artifact-first Build SBOM jobs, or disable container SBOM attestations. The snapshot `sboms` input (default `none`) has separate workflow-wide generation gates.
- **What it controls:** Build-time artifact-first Build SBOM execution, container SBOM generation, and per-artifact release inclusion. Direct build workflow calls still default their Build SBOM input to `true` unless explicitly disabled. See [docs/sbom.md](sbom.md) for the full semantics.
- **Note:** `analyzed-*` scans use [Syft](https://github.com/anchore/syft); ecosystem coverage varies. Gradle Android does not support APK/AAB analyzed-artifact SBOMs, and requesting that layer fails release assembly. Configure Android with `sboms: build`, or `build,analyzed-container` when it feeds a container. The `build` layer uses ecosystem-native CycloneDX tooling; see [Cargo SBOM scope](sbom.md#cargo-sbom-scope) for target/feature limits.

---

### Configuration Fields (Maven/Gradle)

#### `config.private-dependencies`

- **Type:** `boolean`; default `false`.
- **Applies to:** Maven, JVM Gradle and npm.
- **Description:** Opt into a separate dependency-read credential. GitHub build workflows use their job token with `packages: read`; CLI callers supply `DEPENDENCY_TOKEN` or `--dependency-token-file`.
- **Maven:** Requires consumer-owned `config.settings-path`; preserve mirrors and repository IDs and reference the dependency environment variables in those settings.
- **Gradle:** Configure dependency authentication in the project using `DEPENDENCY_TOKEN` and `DEPENDENCY_USERNAME`. Private builds avoid reusable daemons and workflow caches.
- **npm:** Also requires `config.dependency-registry`; authentication is available only during `npm ci`.
- **Details:** [Private dependency access](private-dependencies.md).

#### `config.java-version`

- **Type:** `string` or `number`
- **Description:** For Maven, the JDK version forwarded to GitHub Packages publishing/setup steps. The build runtime is selected by the workflow `runtime-image` input; this field does not select the Gradle build toolchain.
- **Default:** `25`
- **Valid values:** `8`, `11`, `17`, `21`, `25`
- **Example:** `java-version: 25`

#### `config.settings-path`

- **Type:** `string`
- **Applies to:** Maven only; Gradle config rejects this key
- **Description:** Path to Maven settings.xml (relative to working-directory)
- **Default:** None (uses default Maven settings)
- **Example:** `settings-path: .mvn/settings.xml`
- **Use case:** Custom Maven repository configuration

---

### Configuration Fields (NPM)

#### `config.npm-tag`

- **Type:** `string`; no default.
- **Required:** When publishing npm through `forge-packages`.
- **Values:** Explicit `release-{version}` or `release-<packed-version>`, with backward-compatible aliases `release.{version}` and `release.<packed-version>` for Forgejo compatibility. The publisher reads the packed manifest; a literal tag must match npm's normalized registry version. The selected separator is preserved; tags are never silently switched between aliases.
- **Policy:** Movable tags such as `latest`, `next` and `snapshot` are refused. An existing tag bound to a different version is refused before publication. See [npm caller responsibilities](ecosystems.md#npm-artifact-first).

#### `config.dependency-registry`

- **Type:** `string`; required with `config.private-dependencies: true`.
- **Description:** HTTPS registry URL whose host/path receives the dependency token. Loopback HTTP is permitted for local fixtures. This does not replace consumer-owned registry or scope routing in `.npmrc`.

#### `config.node-version`

- **Type:** `string` or `number`
- **Description:** Node.js version forwarded to publishing/setup steps. The build runtime is selected by the workflow `runtime-image` input.
- **Default:** `24`
- **Valid values:** `18`, `20`, `22`, `24`
- **Example:** `node-version: 24`

---

### Configuration Fields (Go)

#### `config.build-mode`

- **Type:** `string`
- **Required:** No; omitted defaults to `artifact-first`
<!-- schema-values:go build-mode -->
- **Valid values:** `artifact-first` (default), `container-first`
<!-- /schema-values:go build-mode -->
- **Description:** Selects whether forge-ci compiles Go binaries in `build-go.yml` (artifact-first) or lets the project's Containerfile compile them (container-first).

Use `artifact-first` for CLI/tools released as standalone binaries:

```yaml
artifacts:
  - name: my-cli
    project-type: go
    config:
      build-mode: artifact-first
      main-package: ./cmd/my-cli
      binary-name: my-cli
      platforms: linux/amd64,linux/arm64,darwin/amd64,darwin/arm64
```

Use `container-first` for services where the container image is the primary deliverable:

```yaml
sign:
  method: sigstore
artifacts:
  - name: my-service
    project-type: go
    config:
      build-mode: container-first
containers:
  - name: my-service
    from: [my-service]
    container-file: Containerfile
    context: .
    target: runtime
```

When an artifact-first Go binary is copied into a container, that container may
reference one artifact-first Go artifact. Multiple Go binaries should either be
built in the Containerfile (`container-first`) or split across containers.
The downloaded layout inside the container build context is
`dist/<goos>-<goarch>/<binary>-<goos>-<goarch>`, so multi-arch Containerfiles
can copy `dist/${TARGETOS}-${TARGETARCH}/...` using the target arguments Buildah
exposes for each `--platform` build.

#### `config.main-package`

- **Type:** `string`
- **Applies to:** Go `artifact-first`
- **Default:** `.`
- **Description:** Go package passed to `go build`.
- **Example:** `main-package: ./cmd/my-cli`

#### `config.binary-name`

- **Type:** `string`
- **Applies to:** Go `artifact-first`
- **Default:** artifact `name`, then Go module basename
- **Description:** Output binary basename.

#### `config.platforms`

- **Type:** comma-separated `GOOS/GOARCH` list
- **Applies to:** Go `artifact-first`
- **Default:** `linux/amd64,linux/arm64,darwin/amd64,darwin/arm64`
- **Description:** Cross-compile targets written as `dist/<goos>-<goarch>/<binary>-<goos>-<goarch>` so release asset basenames are unique.

#### `config.build-tags`, `config.ldflags`, `config.skip-tests`

- **Applies to:** Go `artifact-first`
- **Description:** Optional build tags and additional ldflags. `config.skip-tests` is a legacy compatibility field: `true` is refused, not a way to suppress release-build tests. Administrator-owned policy controls whether recognized test findings block; tests still run. `govulncheck` remains caller-owned in PR/test workflows. See [Administrator check enforcement](admin-policy.md).

### Configuration Fields (Cargo)

#### `config.build-mode`

- **Type:** `string`
- **Required:** No; omitted defaults to `artifact-first`
<!-- schema-values:cargo build-mode -->
- **Valid values:** `artifact-first` (default), `container-first`
<!-- /schema-values:cargo build-mode -->
- **Description:** Selects whether forge-ci cross-compiles Rust binaries in `build-cargo.yml` (artifact-first) or lets the project's Containerfile compile them (container-first). Symmetric with Go's `build-mode`.

Use `artifact-first` for CLI/tools released as standalone binaries:

```yaml
artifacts:
  - name: my-cli
    project-type: cargo
    config:
      build-mode: artifact-first
      binary-name: my-cli
      platforms: linux/amd64,linux/arm64
```

Use `container-first` for services where the container image is the primary deliverable. This is also the right choice when your Containerfile already runs `cargo build`:

```yaml
sign:
  method: sigstore
artifacts:
  - name: my-service
    project-type: cargo
    config:
      build-mode: container-first
containers:
  - name: my-service
    from: [my-service]
    container-file: Containerfile
    context: .
    target: runtime
```

When an artifact-first Cargo binary feeds a container, the downloaded layout
inside the build context matches Go's: `dist/<goos>-<goarch>/<binary>-<goos>-<goarch>`.
Multi-arch Containerfiles can copy `dist/${TARGETOS}-${TARGETARCH}/...` the same
way Go containers do.

#### `config.binary-name`

- **Type:** `string`
- **Applies to:** Cargo `artifact-first`
- **Default:** Cargo.toml `[[bin]]` target name, then `[package].name`
- **Description:** Output binary basename. The default is what cargo itself produces under `target/<triple>/release/`.

#### `config.platforms`

- **Type:** comma-separated `GOOS/GOARCH` list
- **Applies to:** Cargo `artifact-first`
- **Default:** `linux/amd64`
- **Description:** Cross-compile targets. Each value is mapped to a Rust target triple at build time (`linux/amd64 → x86_64-unknown-linux-gnu`, `linux/arm64 → aarch64-unknown-linux-gnu`, etc.). The runtime image installs the targets and cross-linkers it supports; `linux/amd64` + `linux/arm64` are validated for the default `runtime-rust-stable` image. Other targets require either a runtime image extension or a host with the matching cross-linker.

#### `config.skip-tests`

- **Applies to:** Cargo `artifact-first`
- **Description:** Legacy compatibility field; `true` is refused. The release build invokes `cargo test --locked --all-targets` under administrator-owned application-test enforcement. Keep caller-owned PR/test workflows for the project's full test matrix.

### Configuration Fields (Gradle)

#### `config.gradle-tasks`

- **Type:** `string`
- **Applies to:** `gradle` (JVM)
- **Description:** Gradle packaging tasks to execute after the mandatory test invocation. Task overrides do not remove that invocation; consumer task exclusions are refused. Gradle may report cached, up-to-date or `NO-SOURCE` test tasks, so invocation alone does not prove physical test execution.
- **Default:** `assemble`
- **Example (JVM):** `gradle-tasks: build test`
- **Android:** `gradle-android` config rejects this key. The orchestrator derives packaging tasks from `product-flavor`, `build-types` and `include-aab`; configure those instead. Direct callers of `build-gradle-android.yml` can use its `gradle-tasks` workflow input. The selected module's test aggregate remains mandatory before packaging even with overrides; native task outcomes determine whether physical tests ran.

#### `config.gradle-version-file`

- **Type:** `string`
- **Description:** File containing version properties
- **Default:** `gradle.properties`
- **Example:** `gradle-version-file: gradle.properties`

---

### Configuration Fields (Gradle Android)

#### `config.build-module`

- **Type:** `string`
- **Description:** Gradle module to build (the application module).
- **Required:** Yes (for `project-type: gradle-android`)
- **Example:** `build-module: app`

#### `config.product-flavor`

- **Type:** `string`
- **Description:** Product flavor for the build. Combined with `build-types` and `include-aab` to derive the gradle task list (e.g. `demo` + `release` + AAB → `assembleDemoRelease app:bundleDemoRelease`).
- **Default:** `""` (no flavor)
- **Example:** `product-flavor: demo`

#### `config.build-types`

- **Type:** `string`
- **Description:** Comma-separated build types to produce.
- **Default:** `debug,release`
- **Example:** `build-types: release`

#### `config.include-aab`

- **Type:** `boolean`
- **Description:** Also build the Android App Bundle (AAB) for the release build type. Required for Google Play publishing.
- **Default:** `true`
- **Example:** `include-aab: true`

#### Direct Android workflow naming

`artifact-name-prefix` is a string input to `build-gradle-android.yml`, defaulting
to `""`. Direct callers can set it under `with:`. It is not an artifacts.yml
config key; strict parsing rejects `config.artifact-name-prefix`. The
orchestrator uses the artifact's `name` as the upload identifier so the build
and Google Play publisher agree.

---

### Configuration Fields (Xcode iOS/macOS)

#### `config.xcode-version`

- **Type:** `string`
- **Description:** Xcode version to use for building
- **Required:** Yes
- **Valid values:** `15.4`, `16.0`, `16.1`, etc.
- **Example:** `xcode-version: "16.1"`

#### `config.scheme`

- **Type:** `string`
- **Description:** Xcode scheme to build
- **Required:** Yes
- **Example:** `scheme: "DemoApp"`

#### `config.workspace`

- **Type:** `string`
- **Description:** Xcode workspace file (mutually exclusive with `project`)
- **Required:** One of `workspace` or `project`
- **Example:** `workspace: "MyApp.xcworkspace"`

#### `config.project`

- **Type:** `string`
- **Description:** Xcode project file (mutually exclusive with `workspace`)
- **Required:** One of `workspace` or `project`
- **Example:** `project: "MyApp.xcodeproj"`

#### `config.configuration`

- **Type:** `string`
- **Description:** Build configuration
- **Default:** `Release`
- **Valid values:** `Debug`, `Release`, or custom configurations
- **Example:** `configuration: Release`

#### `config.use-xcodegen`

- **Type:** `boolean`
- **Description:** Run XcodeGen before version extraction and archive
- **Default:** `false`
- **Example:** `use-xcodegen: true`

#### `config.xcodegen-spec`

- **Type:** `string`
- **Description:** Path to the XcodeGen spec file, relative to `working-directory`
- **Default:** `project.yml`
- **Example:** `xcodegen-spec: project.yml`
- **Note:** Keep `project` or `workspace` set so later steps use a deterministic build target after generation

#### `config.enable-code-signing`

- **Type:** `boolean`
- **Description:** Enable iOS/macOS code signing and IPA export. When false, the build archives with `CODE_SIGNING_ALLOWED=NO`, so the team and provisioning profile the project names are not needed, uploads the archive, and release publishing skips App Store Connect upload.
- **Default:** `true`
- **Example:** `enable-code-signing: true`
- **Requires secrets:**
  - `IOS_SIGNING_CERTIFICATE_BASE64` - Base64-encoded .p12 certificate
  - `IOS_SIGNING_CERTIFICATE_PASSPHRASE` - Certificate password
  - `PROVISIONING_PROFILE_BASE64` - Base64-encoded provisioning profile

#### `config.export-options-var`

- **Type:** `string`
- **Description:** Name of GitHub variable containing base64-encoded exportOptions.plist
- **Default:** `EXPORT_OPTIONS_BASE64`
- **Example:** `export-options-var: EXPORT_OPTIONS_BASE64`
- **Note:** Variable should contain base64-encoded exportOptions.plist for IPA export

#### `config.build-number`

- **Type:** `string`
- **Description:** The build number of the archive, passed to `xcodebuild` as `CURRENT_PROJECT_VERSION`. A fixed value: every release builds with it until it is changed.
- **Default:** empty (the project's own `CURRENT_PROJECT_VERSION`)
- **Example:** `build-number: "42"`
- **Note:** App Store Connect refuses a build number it has already received for the version. A caller that computes one for each release, such as a commit count in a job of its own, passes it as the `xcode-build-number` input of the GitHub `release-orchestrator.yml`, which takes its place.

The macOS runner is not an artifact setting: the caller's workflow names it
with the `runs-on-darwin-arm64` input of the orchestrator, default
`["macos-26"]`. See [Runners](runners.md#github-runners).

#### Direct Xcode workflow destination

`destination` is a string input to `build-xcode-ios.yml`, defaulting to
`generic/platform=iOS`. Direct callers can set it under `with:`, for example
`destination: generic/platform=macOS`. It is not an artifacts.yml config key;
strict parsing rejects `config.destination`.

#### `config.submit-for-review`

- **Type:** `boolean`
- **Description:** Records App Store review intent in the upload summary. Current workflows upload to App Store Connect/TestFlight; review submission is manual in App Store Connect.
- **Default:** `false`
- **Example:** `submit-for-review: false` (upload only)
- **Example:** `submit-for-review: true` (record that review submission is intended)
- **Note:** Automatic App Store review submission is not implemented today

#### `config.skip-validation`

- **Type:** `boolean`
- **Description:** Skip IPA validation before upload to App Store Connect
- **Default:** `false`
- **Example:** `skip-validation: false` (Recommended - validates before upload)
- **Note:** Only set to `true` if validation fails incorrectly

---

### Configuration Fields (Gradle Android - Google Play)

#### `config.enable-android-signing`

- **Type:** `boolean`
- **Description:** Enable Android app signing for release builds
- **Default:** `false`
- **Example:** `enable-android-signing: true`
- **Requires secrets:**
  - `ANDROID_KEYSTORE` - Base64-encoded keystore file
  - `ANDROID_KEYSTORE_PASSWORD` - Keystore password
  - `ANDROID_KEY_ALIAS` - Key alias
  - `ANDROID_KEY_PASSWORD` - Key password
- **Note:** The workflow maps `ANDROID_KEYSTORE` to the internal
  `ANDROID_KEYSTORE_BASE64` environment variable used by the `forge-ci` CLI.
- **Behavior when false:** Skips forge-ci's keystore injection only. Requested
  release APK/AAB tasks and run-artifact uploads still execute and may produce
  unsigned outputs; forge-ci does not reject them at config-parse time.

#### Android upload selection

`build-gradle-android.yml` uploads each requested debug APK, release APK, and
release AAB as a GitHub Actions run artifact. The production release of an
application attaches the release APK to the GitHub Release when `build-types`
includes `release`, and the AAB when `include-aab` is true as well. A debug
APK is never attached. `publish-to: [google-play]` also selects the AAB; configuration
validation does not currently require `enable-android-signing: true`, although
Google Play normally requires a signed bundle.

#### `config.package-name`

- **Type:** `string`
- **Description:** Android package name (application ID)
- **Required:** Yes (for Google Play publishing)
- **Example:** `package-name: com.example.myapp`

#### `config.google-play-track`

- **Type:** `string`
- **Description:** Google Play release track
- **Default:** `internal`
- **Valid values:** `internal`, `alpha`, `beta`, `production`
- **Example:** `google-play-track: internal`

#### `config.google-play-status`

- **Type:** `string`
- **Description:** Release status on Google Play
- **Default:** `completed`
- **Valid values:** `completed`, `inProgress`, `halted`, `draft`
- **Example:** `google-play-status: completed`
- **Note:** Use `inProgress` with `user-fraction` for staged rollouts

#### `config.google-play-user-fraction`

- **Type:** `string`
- **Description:** Staged rollout percentage (0.0-1.0)
- **Default:** Empty (full rollout)
- **Example:** `google-play-user-fraction: "0.1"` (10% rollout)
- **Note:** Only applies when `google-play-status: inProgress`

#### `config.google-play-update-priority`

- **Type:** `string`
- **Description:** In-app update priority level
- **Default:** `"0"`
- **Valid values:** `"0"` to `"5"` (5 is highest)
- **Example:** `google-play-update-priority: "3"`

#### `config.google-play-release-name`

- **Type:** `string`
- **Description:** Custom release name (defaults to version from AAB)
- **Default:** Empty (auto-generated)
- **Example:** `google-play-release-name: "Summer Update"`

#### `config.google-play-changes-not-sent-for-review`

- **Type:** `boolean`
- **Description:** Hold changes for manual review in Play Console
- **Default:** `false`
- **Example:** `google-play-changes-not-sent-for-review: true`

#### `config.whats-new-directory`

- **Type:** `string`
- **Description:** Directory containing localized release notes
- **Default:** Empty (no release notes)
- **Example:** `whats-new-directory: distribution/whatsnew`
- **Format:** Files named `whatsnew-<LOCALE>` (e.g., `whatsnew-en-US`, `whatsnew-sv-SE`)

#### `config.mapping-file`

- **Type:** `string`
- **Description:** Path to ProGuard/R8 mapping.txt file
- **Default:** Empty
- **Example:** `mapping-file: app/build/outputs/mapping/release/mapping.txt`
- **Use case:** De-obfuscate crash reports in Play Console

#### `config.debug-symbols`

- **Type:** `string`
- **Description:** Path to native debug symbols (zip or folder)
- **Default:** Empty
- **Example:** `debug-symbols: app/build/intermediates/merged_native_libs/release/out/lib`
- **Use case:** Symbolicate native crashes in Play Console

---

## Containers Section

For artifact-first examples that need no image, omit `containers` or set it to
`[]`; retain the artifact declaration. A repository that builds its binaries
with its own tooling may declare only `containers:` and omit `artifacts`; a
file must declare at least one of the two. Container-first builds need their
container build path, so choose the appropriate build mode before removing it.
Use `platforms` for one or multiple native architecture builds. Per-container
`enable-slsa` and `scan-severity` values flow through the typed plan without
additional workflow-input wiring. SLSA defaults to enabled, and disabling it
removes that guarantee. Every exported or published image is scanned;
`scan-severity` selects the blocking threshold.
Container SBOM scanning derives from the source artifacts' `sboms` selection:
omitting `analyzed-container` (for example, `build,analyzed-artifact`) omits that
layer rather than configuring a separate container SBOM switch.

Containers reference artifacts via the `from:` field and are built after all artifacts complete.

### Container Required Fields

#### Container `name`

- **Type:** `string`
- **Description:** Container image name (becomes part of image tag)
- **Example:** `backend-api`, `frontend-ui`
- **Resulting image:** `ghcr.io/owner/repo/backend-api:v1.0.0`
- **Single-container collapse:** When `name` equals the repo's short name (the common single-container pattern), the redundant `<repo>/<repo>` subpath is collapsed to `ghcr.io/owner/repo:<tag>`. Multi-container layouts where each `name` is distinct from the repo are unaffected.

---

### Container Optional Fields

#### `from`

- **Type:** `array of strings`
- **Description:** List of artifact names to include in this container
- **Default:** `[]` (no artifact dependencies or source-derived container SBOM)
- **Must reference:** Existing artifact names from `artifacts[]` section
- **Example:** `from: [backend-api]` (single artifact)
- **Example:** `from: [api, worker, web]` (multi-artifact container)

#### `container-file`

- **Type:** `string`
- **Description:** Path to Containerfile/Dockerfile (relative to repository root)
- **Default:** `Containerfile`
- **Example:** `Containerfile`, `services/backend/Containerfile`

#### `context`

- **Type:** `string`
- **Description:** Docker build context directory
- **Default:** `.` (repository root)
- **Example:** `context: services/backend`

#### `platforms`

- **Type:** `string` (comma-separated)
- **Description:** Target CPU architectures. Each platform builds natively on a runner of that architecture (`linux/amd64` → `ubuntu-26.04`, `linux/arm64` → `ubuntu-26.04-arm`). No QEMU. Multi-platform inputs split across runners and merge into a single manifest list.
- **Default:** `linux/amd64`
- **Example:** `platforms: linux/amd64,linux/arm64`
- **Performance:** Multi-platform runs in parallel; wall-clock is dominated by the slowest arch leg (≈1.1× single-arch, plus the merge job).

#### `enable-slsa`

- **Type:** `boolean`
- **Description:** Generate SLSA build-provenance attestation for this container. The per-container value propagates through the typed plan to `publish-container.yml`; set to `false` to opt this specific image out of provenance.
- **Default:** `true`
- **Requires:** release artifact signing enabled and `sign.method: sigstore` or `sign.method: kms`; production planning rejects enabled pushed provenance when signing is disabled or uses GPG. To keep GPG signatures on the release files, add [`sign.files-method: gpg`](#signfiles-method) beside the Cosign method. Keyless Sigstore also requires `id-token: write`; the orchestrator supplies workflow permissions.
- **Example:** `enable-slsa: true`

#### Unsupported container `enable-sbom`

The `enable-sbom: bool` field on the container block is unsupported and strict parsing rejects it. Container **SBOM generation** is derived from each source artifact's `sboms` field. The analyzed-container SBOM is generated if any source artifact has `analyzed-container` in its effective sboms, the default for buildable types. Exclude that layer to omit this SBOM, not the mandatory Trivy vulnerability scan of an exported/published OCI layout.

#### Unsupported container `enable-scan`

The `enable-scan: bool` field on the container block is unsupported and strict parsing rejects it. Every exported or published OCI layout is scanned with Trivy; there is no switch to turn the scan off.

With explicit `enforce-vulnerabilities: true`, a finding at `scan-severity` or above with a fixed package fails the publish, and so does an unfixed CRITICAL unless `.trivyignore.yaml` approves it; other findings are only listed. Under the default `false`, the scanner still runs and reports findings; scanner and operational errors remain blocking. See [Administrator check enforcement](admin-policy.md).

Results appear in Code Scanning when the `CODE_SCANNING_TOKEN` org secret is set.

#### `scan-severity`

- **Type:** `string` (a trivy severity, or a comma-separated list)
- **Description:** Blocking threshold of the container scan. Under an enforcing administrator policy, a finding at this severity or above fails the publish when a fixed package exists; an unfixed CRITICAL fails it unless `.trivyignore.yaml` approves it. A list blocks at its least severe entry, so `"CRITICAL,HIGH"` blocks at HIGH and above, as the former default did.
  The scan always reports down to HIGH, or down to the threshold when it is lower: findings below the threshold are listed as "reported, not blocking" and still reach the scan report, SARIF and GitLab report.
- **Default:** `"CRITICAL"`
- **Example:** `scan-severity: "CRITICAL,HIGH"`

An unfixed CRITICAL that the team accepts is approved in Trivy's own ignore
file at the repository root, with the reason and a date for another look:

```yaml
# .trivyignore.yaml
vulnerabilities:
  - id: CVE-2026-12345
    statement: glibc in the distroless base; no fixed package in Debian 13 yet
    expired_at: 2027-01-31
```

Once `expired_at` passes, the finding blocks again.

#### `image-name`

- **Type:** `string`
- **Description:** The image repository instead of the one derived from the repository and `name`, as the GitHub `publish-container.yml` input of the same name takes it: a path under the registry (`owner/app`) or a full repository (`ghcr.io/owner/app`). Lowercase, no tag or digest. The Forgejo `release-images.yml` refuses it: a release there publishes to the one repository of its `image-repository` input.
- **Default:** empty (derived)
- **Example:** `image-name: owner/app`
- **Used by:** the GitHub `publish-container.yml`

#### `tag-suffix`

- **Type:** `string`
- **Description:** Appended to the release tag to make this container's tag, so `v1.2.3` with `-distroless` publishes `v1.2.3-distroless`. Starts with `-`; lowercase letters, digits and `.`, `_` or `-` separators.
- **Default:** empty (the release tag itself)
- **Example:** `tag-suffix: -distroless`
- **Used by:** the Forgejo `release-images.yml`; see [Release images](forgejo.md#release-images)

#### `target`

- **Type:** `string`
- **Description:** Containerfile stage to build for the runtime image. Useful for multi-stage Containerfiles where the deployable image is not the last stage.
- **Default:** empty (builds the last stage; current `docker build` behavior)
- **Example:** `target: runtime`
- **Used by:** container-first ecosystems primarily, but the field is generic: any multi-stage Containerfile may set it.
- **See also:** [artifact-first vs container-first framing](ecosystems.md)

#### `extract.binary`

Opt-in extraction of compiled binaries as CI artifacts. Used by container-first
ecosystems (`cargo`, `go`) where the binary is a byproduct of the container build.
Each platform leg uploads `${container.name}-binaries-${arch}`. Select one source:

- `paths`: copy regular executables from the retained runtime image filesystem,
  with no second build. Use this when release assets must be the exact bytes in
  the scanned and published image.
- `target`, optionally with `names`: run the separate Containerfile export stage.

With `target`, extraction is a separate local-target build. It does not receive the main
build's registry-cache settings; any reuse depends on the builder and
Containerfile. Do not assume it avoids another compile or yields bytes
identical to those in the runtime image.

##### `extract.binary.target`

- **Type:** `string`
- **Description:** Containerfile stage that exposes the binaries. Typically `FROM scratch AS export-binary` with `COPY --from=builder ...` lines.
- **Required when** selecting export-stage extraction instead of `paths`.
- **Example:** `target: export-binary`

##### `extract.binary.names`

- **Type:** list of `string`
- **Description:** Binary file basenames to suffix with `-linux-<arch>` before upload. Every listed name must exist as a regular file; missing names fail extraction processing before any rename.
- **Default:** When omitted, suffix every top-level file in the extracted output
- **Example:** `names: [my-service, my-service-cli]`

##### `extract.binary.paths`

- **Type:** list of image filesystem paths (1–128).
- **Description:** Copy selected nonempty regular files from the verified,
  retained per-architecture OCI layout. Output names are the unique basenames;
  they receive the same `-linux-<arch>` suffix as export-stage binaries.
- **Cannot combine with:** `target` or `names`.
- Paths must be clean, with no parent traversal, whitespace, comma or backslash.
  Symlinks and hardlinks are not followed; select the actual regular executable.
- The current GitHub delivery path requires `push-image: true`, which retains
  the scanned OCI layout. The underlying
  `forge-ci container layout extract-files` command is provider-independent and
  reads a local layout without a container engine, image pull or rebuild.
- The existing blob verifier and go-containerregistry's effective filesystem
  reader preserve layer ordering and whiteouts. Files are bounded to 512 MiB
  each and 2 GiB selected output; existing output files are never overwritten.
- **Example:** `paths: [/my-service, /usr/local/bin/my-service-cli]`.

##### Full example

Exact runtime-image bytes:

```yaml
containers:
  - name: my-service
    from: [my-service]
    container-file: my-service/Containerfile
    target: runtime
    platforms: linux/amd64,linux/arm64
    extract:
      binary:
        paths: [/my-service, /usr/local/bin/my-service-cli]
```

Separate export-stage build:

```yaml
containers:
  - name: my-service
    from: [my-service]
    container-file: my-service/Containerfile
    context: .
    target: runtime
    platforms: linux/amd64,linux/arm64
    extract:
      binary:
        target: export-binary
        names: [my-service, my-service-cli]
```

##### Output

- GHA artifacts `${container.name}-binaries-${arch}` for each platform leg (for example, `my-service-binaries-amd64` and `my-service-binaries-arm64`).
- Automatically included in the GitHub Release when extraction produced files; callers can still add their own `release.attachartifacts` globs for other assets.

#### `build-args`

- **Type:** key/value object
- **Description:** Build arguments for the Containerfile (e.g., `RUST_VERSION`, `DEBIAN_VARIANT`). The parser converts the object to `KEY=VALUE` lines for `forge-ci container build` (buildah `--build-arg`), so callers can use the more readable object form in `artifacts.yml`.
- **Example (in artifacts.yml):**

  ```yaml
  containers:
    - name: my-svc
      from: [my-svc]
      container-file: Containerfile
      context: .
      build-args:
        RUST_VERSION: "1.94"
        DEBIAN_VARIANT: bookworm-slim
  ```

- **Direct caller equivalent** (when invoking `publish-container.yml` directly, bypassing the orchestrator):

  ```yaml
  uses: diggsweden/forge-ci/.github/workflows/publish-container.yml@PIN_FORGE_CI_SHA # v0.90.0
  with:
    runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
    runtime-image-full: ghcr.io/diggsweden/forge-ci-runtime@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
    branch: main
    # plus runtime-image, runtime-image-full and the canonical-* inputs of a
    # canonical-cli-bootstrap.yml job in the same run; see publishing.md
    forge-ci-binary-ref: <full-engine-commit>
    artifact-types: cargo
    build-args: |
      RUST_VERSION=1.94
      DEBIAN_VARIANT=bookworm-slim
  ```

#### `build-secrets`

- **Type:** array of strings (each matches `^[A-Z_][A-Z0-9_]*$`)
- **Description:** Names of GitHub Actions secrets to forward into the Containerfile
  build as `RUN --mount=type=secret` mounts. Use this for secrets needed at build
  time (private registry tokens, build-time API keys). `build-args` values surface
  in image/build records and are unsuitable for secrets. Secret mounts keep values
  out of forge-ci's mount-spec outputs and build arguments; consuming `RUN`
  commands must still avoid printing values or copying them into image files. See
  the [trusted build-code boundary](threat-model.md#what-forge-ci-does-not-defend-against).
- **How it works:** Each name listed here must appear as a key in
  the `FORGE_CI_BUILD_SECRETS_JSON` envelope secret (see the
  recipe below). At build time, `forge-ci container
  materialize-build-secrets` unpacks the envelope into mode-0600
  files in a unique mode-0700 `forge-ci-build-secrets-*` directory
  under the selected temporary root (`$RUNNER_TEMP` in this workflow), and emits
  `id=<lowercased-name>,src=PATH` mount specs as a compact JSON array. The scalar-safe
  encoding crosses GitHub/Forgejo output files and GitLab dotenv reports
  without multiline ambiguity; `forge-ci container build` decodes it and
  passes each entry to buildah's `--secret`. An `if: always()` step removes
  the private files after their final build consumer, including on
  self-hosted runners; a failed materialization removes its partial files
  immediately.
  Reserved names, those forge-ci already uses for its own
  publish flows (`RELEASE_GPG_PRIVATE_KEY`, `MAVEN_CENTRAL_PASSWORD`,
  etc.), are rejected at config-parse time to prevent accidental
  shadowing.
- **artifacts.yml:**

  ```yaml
  containers:
    - name: my-svc
      from: [my-svc]
      container-file: Containerfile
      context: .
      build-secrets:
        - PRIVATE_REGISTRY_TOKEN
        - BUILD_TIME_API_KEY
  ```

- **Containerfile:** declare each secret on the `RUN` line that consumes it. The `id=` value is the **lowercased** name; `target=` is the file path inside the mount namespace (a conventional value, the file disappears once the `RUN` step exits).

  ```dockerfile
  # syntax=docker/dockerfile:1
  RUN --mount=type=secret,id=private_registry_token,target=/run/secrets/token \
      curl -H "Authorization: Bearer $(cat /run/secrets/token)" \
           https://internal.example/private/dep.tar.gz -o /tmp/dep.tar.gz
  ```

- **Caller workflow:** the adopter's top-level caller forwards the envelope as the `FORGE_CI_BUILD_SECRETS_JSON` secret. The value must stay inside the `secrets:` context end-to-end. Step outputs, job outputs, and `with:` inputs are **not** secret-scoped (their values are queryable through the workflow-run API even when GHA masks them in log text). Two correct shapes, picked by how much scoping you want:

  **Option A, a single pre-packed org secret (recommended).** Create one GitHub secret named `FORGE_CI_BUILD_SECRETS_JSON` at the repo or org level. Its value is the compact JSON object you'd otherwise build at runtime:

  ```json
  {"PRIVATE_REGISTRY_TOKEN":"<value>","BUILD_TIME_API_KEY":"<value>"}
  ```

  Then forward it untouched:

  ```yaml
  jobs:
    release:
      uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@PIN_FORGE_CI_SHA # v0.90.0
      with:
        # Engine commit and image digests: see the Advanced Pinning Recipe.
        branch: main
      secrets:
        # ... your usual secret forwards ...
        FORGE_CI_BUILD_SECRETS_JSON: ${{ secrets.FORGE_CI_BUILD_SECRETS_JSON }}
  ```

  Pros: the value never enters a non-secret channel; rotation is one secret update; the envelope ships exactly the keys you declared.

  **Option B, the `toJSON(secrets)` shortcut.** If every name in `containers[].build-secrets` matches a top-level secret name exactly, forward the whole context:

  ```yaml
  secrets:
    FORGE_CI_BUILD_SECRETS_JSON: ${{ toJSON(secrets) }}
  ```

  The materialize step ignores envelope keys it didn't declare, so unrelated org secrets riding along are decoded into memory and dropped without touching disk. The value still stays in the secrets channel. Trade-off: the envelope JSON is larger than it needs to be, and the auto-`GITHUB_TOKEN` rides along.

  Avoid: packing the envelope at runtime via a `jq` step into `$GITHUB_OUTPUT`, then forwarding `needs.pack.outputs.envelope`. Step / job outputs are not a secrets channel, and the value leaves the masked path the moment you write it there.

---

## Distro Packages Section

`distro-packages` is a block of the whole release, not of one artifact.
When the block is present, every `.deb`, `.rpm` and `.apk` file of the
release is published to the forge's Debian, RPM and Alpine registries, each
file as the release built and signed it. The block holds only the places;
`distro-packages: {}` publishes with the defaults.

```yaml
distro-packages:
  debian:
    distribution: stable
    component: main
  rpm:
    group: rocky/el9
  alpine:
    branch: stable
    repository: main
```

| Field | Default | Meaning |
|---|---|---|
| `debian.distribution` | `stable` | The distribution a `.deb` is published to, as in an apt source line. |
| `debian.component` | `main` | Its component. |
| `rpm.group` | `stable` | The group an `.rpm` is published to; it may have several steps, such as `rocky/el9`. |
| `alpine.branch` | `stable` | The branch an `.apk` is published to. |
| `alpine.repository` | `main` | Its repository. |

A name is lower-case letters and digits, with single `.`, `_` or `-`
between them, at most 64 characters; only the RPM group takes `/` between
such names, and is at most 128 characters. Forgejo keeps a package's place
in a 255-character column, which two names and an architecture fit. An
unknown field is refused, and so is a place that is empty or no string.

A bare `distro-packages:`, without a value, is refused and not read as an
absent block: write `distro-packages: {}` for the defaults.

The Forgejo release publishes them; see
[Distro packages in the owner's registries](forgejo.md#distro-packages-in-the-owners-registries)
for the job, the token, installation and what is refused. The GitHub and
GitLab releases refuse a declaration with this block when they are planned,
since neither forge has registries that fit.

---

## Publishing Targets

### `forge-packages`

- **Description:** GitHub Packages registry
- **Requirements:** `GITHUB_TOKEN` (automatic)
- **Applies to:** Maven libraries, npm packages, and [Gradle JVM/Android-library publications](publishing.md#gradle-jvm-and-android-libraries). Maven apps are not supported by this target.
- **Registry:** `maven.pkg.github.com/<owner>/<repo>` (Maven), `npm.pkg.github.com` (NPM). Container registries are configured separately under `containers`.
- **npm name:** GitHub Packages takes an npm package only under the scope of the repository's owner, so `package.json` names it `@<owner>/<name>`, the owner in lower case. An unscoped name is answered with 404 and another owner's scope with 403.

### `maven-central`

- **Description:** Maven Central (Sonatype Central Portal)
- **Requirements:**
  - `MAVEN_CENTRAL_USERNAME` secret
  - `MAVEN_CENTRAL_PASSWORD` secret
  - `build-type: library` (required)
- **Authorization:** Requires an allowlisted signed `release-request/vMAJOR.MINOR.PATCH` and the exact prepared release SHA, even when general release authorization is disabled.
- **Package signing:** Requires `RELEASE_GPG_PRIVATE_KEY` and `RELEASE_GPG_PUBLIC_KEY`, plus `RELEASE_GPG_PASSPHRASE` for a protected key, independently of the release artifact signing method. See [publisher credentials](components.md#artifact-publishers).
- **Applies to:** Maven and [Gradle JVM/Android-library publications](publishing.md#gradle-jvm-and-android-libraries).
- **Snapshots:** Explicit snapshot mode uses ordinary CI credentials; the signed-request and GPG-key requirements above apply to stable releases. See [snapshot publication](publishing.md#snapshots).
- **Note:** Requires Sonatype account and approved groupId

### `npmjs`

- **Description:** Public npmjs.org registry (reserved for future production support)
- **Requirements:** Not currently wired into production release publishing; current config validation rejects this target
- **Applies to:** NPM only
- **Note:** Current NPM package publishing uses `forge-packages` and
  `npm.pkg.github.com`.

### `google-play`

- **Description:** Google Play Store
- **Requirements:**
  - `GOOGLE_PLAY_SERVICE_ACCOUNT_JSON` secret (service account JSON key)
  - `ANDROID_KEYSTORE`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD` secrets (for signing)
  - Signed release AAB setup. `config.enable-android-signing: true` is normally required by Google Play, but forge-ci does not currently validate this coupling and may hand the upload workflow an unsigned AAB.
  - `config.package-name` specified
- **Applies to:** Gradle Android only
- **Note:** App must already exist in Google Play Console (upload first AAB manually)

---

## Quick Start Examples

### Single Artifact (Maven)

**`.forge-ci/artifacts.yml`**
```yaml
artifacts:
  - name: my-app
    project-type: maven
    working-directory: .
    build-type: application
    config:
      java-version: 25
```

**`.github/workflows/release-workflow.yml`**
```yaml
jobs:
  release:
    uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@PIN_FORGE_CI_SHA # v0.90.0
    with:
      branch: main
      # The full commit the workflow ref above names; every plain-runner job
      # runs the CLI built in this run from exactly this commit.
      forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
      # Pinned by digest and admitted before any job runs; replace FIXME with
      # the digests of the release you call (runtime-images.md).
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      canonical-builder-image: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      artifacts-config: .forge-ci/artifacts.yml
      release-publisher: github-cli
```

### Single Artifact with Container (Maven)

**`.forge-ci/artifacts.yml`**
```yaml
sign:
  method: sigstore
artifacts:
  - name: my-app
    project-type: maven
    working-directory: .
    build-type: application
    config:
      java-version: 25

containers:
  - name: my-app
    from: [my-app]
    container-file: Containerfile
    context: .
    platforms: linux/amd64,linux/arm64
```

### Maven Library (Multiple Targets)

```yaml
artifacts:
  - name: my-lib
    project-type: maven
    working-directory: library
    build-type: library
    require-authorization: true
    publish-to:
      - forge-packages
      - maven-central
    config:
      java-version: 25
      settings-path: .mvn/settings.xml
```

### NPM Application

```yaml
artifacts:
  - name: my-ui
    project-type: npm
    working-directory: frontend
    config:
      node-version: 24
```

### Gradle Android App

```yaml
artifacts:
  - name: my-android-app
    project-type: gradle-android
    working-directory: .
    sboms: build
    config:
      build-module: app
      product-flavor: demo
      build-types: release
      gradle-version-file: gradle.properties
```

### Gradle Android App with Google Play Publishing

```yaml
artifacts:
  - name: my-android-app
    project-type: gradle-android
    working-directory: .
    sboms: build
    publish-to:
      - google-play
    config:
      build-module: app
      product-flavor: demo
      build-types: release
      gradle-version-file: gradle.properties
      enable-android-signing: true
      # Google Play configuration
      package-name: com.example.myapp
      google-play-track: internal
      google-play-status: completed
```

**Required Secrets:**
```text
ANDROID_KEYSTORE
ANDROID_KEYSTORE_PASSWORD
ANDROID_KEY_ALIAS
ANDROID_KEY_PASSWORD
GOOGLE_PLAY_SERVICE_ACCOUNT_JSON
```

### iOS/macOS App (Xcode)

```yaml
artifacts:
  - name: my-ios-app
    project-type: xcode-ios
    working-directory: .
    publish-to: []  # iOS apps don't publish to package registries
    config:
      xcode-version: "16.1"
      scheme: "MyApp"
      use-xcodegen: true
      xcodegen-spec: "project.yml"
      project: "MyApp.xcodeproj"
      configuration: Release
      enable-code-signing: true
      export-options-var: EXPORT_OPTIONS_BASE64
```

**Required Secrets:**
```text
IOS_SIGNING_CERTIFICATE_BASE64
IOS_SIGNING_CERTIFICATE_PASSPHRASE
PROVISIONING_PROFILE_BASE64
APP_STORE_CONNECT_ISSUER_ID
APP_STORE_CONNECT_API_KEY_ID
APP_STORE_CONNECT_API_PRIVATE_KEY_BASE64
```

**Required Variables:**
```text
EXPORT_OPTIONS_BASE64
```

**Encoding certificates/profiles to base64:**
```bash
# Certificate
base64 -i certificate.p12 -o certificate.txt

# Provisioning Profile
base64 -i profile.mobileprovision -o profile.txt

# Export Options
base64 -i exportOptions.plist -o exportOptions.txt
```

### iOS/macOS App With XcodeGen

```yaml
artifacts:
  - name: my-ios-app
    project-type: xcode-ios
    working-directory: .
    publish-to: []
    config:
      xcode-version: "16.1"
      scheme: "MyApp"
      use-xcodegen: true
      xcodegen-spec: "project.yml"
      project: "MyApp.xcodeproj"
      configuration: Release
      enable-code-signing: true
      export-options-var: EXPORT_OPTIONS_BASE64
```

Use `project` or `workspace` alongside XcodeGen so version detection and archive steps target the generated Xcode project explicitly.

### Multiple iOS Schemes (Demo, Production)

```yaml
artifacts:
  - name: ios-app-demo
    project-type: xcode-ios
    working-directory: .
    config:
      xcode-version: "16.1"
      scheme: "DemoApp"
      project: "DemoApp.xcodeproj"
      configuration: Release
      submit-for-review: false  # Upload only for demo builds

  - name: ios-app-production
    project-type: xcode-ios
    working-directory: .
    config:
      xcode-version: "16.1"
      scheme: "ProductionApp"
      project: "DemoApp.xcodeproj"
      configuration: Release
      submit-for-review: true  # Record production review intent in the summary
```

### iOS App Store Submission Options

```yaml
artifacts:
  - name: my-ios-app
    project-type: xcode-ios
    working-directory: .
    config:
      xcode-version: "16.1"
      scheme: "MyApp"
      project: "MyApp.xcodeproj"

      # App Store Connect upload options
      submit-for-review: true   # Summary intent only; submit review manually in App Store Connect
      skip-validation: false    # Validate IPA before upload (recommended)
```

---

## Monorepo Configuration

Build multiple artifacts from a single repository.

### Separate Containers (One Artifact → One Container)

**`.forge-ci/artifacts.yml`**
```yaml
sign:
  method: sigstore
artifacts:
  - name: backend
    project-type: maven
    working-directory: java-backend
    build-type: application
    config:
      java-version: 25

  - name: frontend
    project-type: npm
    working-directory: frontend
    config:
      node-version: 24

containers:
  - name: backend
    from: [backend]
    container-file: java-backend/Containerfile
    context: .
    tag-suffix: -backend

  - name: frontend
    from: [frontend]
    container-file: frontend/Containerfile
    context: .
    tag-suffix: -frontend
```

A release publishes every container to one image repository, so separate
containers each carry a `tag-suffix`; without it the release-image plan
refuses two containers that would publish the same tag.

### Combined Container (Multiple Artifacts → One Container)

**`.forge-ci/artifacts.yml`**
```yaml
sign:
  method: sigstore
artifacts:
  - name: backend
    project-type: maven
    working-directory: java-backend

  - name: frontend
    project-type: npm
    working-directory: frontend

containers:
  - name: full-stack-app
    from: [backend, frontend]          # Multiple artifacts in one container
    container-file: Containerfile
    context: .
```

**`Containerfile`**
```dockerfile
FROM registry.access.redhat.com/ubi9/openjdk-21-runtime:latest
COPY java-backend/target/*.jar app.jar
COPY dist/ /app/static/
CMD ["java", "-jar", "app.jar"]
```

### Workflow Configuration

**`.github/workflows/release-workflow.yml`**
```yaml
name: Release Workflow

on:
  push:
    tags:
      - "release-request/v*"

permissions:
  contents: read

jobs:
  release:
    uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@PIN_FORGE_CI_SHA # v0.90.0
    permissions:
      contents: write
      packages: write
      id-token: write
      actions: read
      attestations: write
    # Name each secret the release uses; `secrets: inherit` hands the called
    # workflow every secret and fails the default lint gate's SAST check.
    secrets:
      RELEASE_GPG_PRIVATE_KEY: ${{ secrets.RELEASE_GPG_PRIVATE_KEY }}
      RELEASE_GPG_PASSPHRASE: ${{ secrets.RELEASE_GPG_PASSPHRASE }}
      RELEASE_GPG_PUBLIC_KEY: ${{ secrets.RELEASE_GPG_PUBLIC_KEY }}
      RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}
    with:
      branch: main
      # The full commit the workflow ref above names; every plain-runner job
      # runs the CLI built in this run from exactly this commit.
      forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
      # Pinned by digest and admitted before any job runs; replace FIXME with
      # the digests of the release you call (runtime-images.md).
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      canonical-builder-image: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      artifacts-config: .forge-ci/artifacts.yml
      changelog-creator: git-cliff
      release-publisher: github-cli
```

### Monorepo Limitations

- **Unified versioning**: All artifacts share the version requested by the signed release-request tag
- **Single changelog**: One changelog for the entire repository
- **No change detection**: All artifacts build on every release
- **Serialized release preparation**: Multi-artifact version bumps run one at a
  time against the release branch. After every bump succeeds, one separate job
  creates the final release tag once at the resulting branch HEAD without force.

### Assembly and dependency ordering

Container `from` entries must exactly match artifact names. To combine several
outputs, list them together, for example `from: [api, worker, web]`; to ship
independent microservices, give each its own container entry.

The orchestrator fans builds out by ecosystem and does not infer arbitrary
"shared library before application" ordering. Model that dependency in the
build system: use a Maven reactor/root build, install the library in the
consuming build, or publish it before consuming it. A Maven reactor may be
declared once as `project-type: maven`, `build-type: application`,
`working-directory: .`, with an application container referencing that artifact.

`publish-container.yml` downloads and unpacks outputs into its build workspace.
A Maven or Gradle artifact is restored in its `working-directory`, so its jar
is under `<working-directory>/target/` or `<working-directory>/build/libs/` as
on Forgejo and GitLab. The npm output is unpacked at the repository root, in
`dist/`. Typical paths for the combined Maven/Gradle/NPM example, with the
Maven module in `services/api` and the Gradle project in `services/worker`,
are:

```dockerfile
COPY services/api/target/*api*.jar /app/api.jar
COPY services/worker/build/libs/*worker*.jar /app/worker.jar
COPY dist /app/web
```

## Complete Working Examples

For complete working examples, see the [`examples/`](../examples/) directory:

- **Maven Application**: [`examples/maven-app/`](../examples/maven-app/)
- **NPM Application**: [`examples/npm-app/`](../examples/npm-app/)
- **Gradle JVM Library**: [`examples/gradle-app/`](../examples/gradle-app/)
- **Android Application**: [`examples/android-app/`](../examples/android-app/)
- **Monorepo**: [`examples/monorepo/`](../examples/monorepo/)

---
