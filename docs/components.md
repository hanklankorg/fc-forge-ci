# Components Reference

## Available Components

The reusable GitHub workflows, and how to call them directly. The
[GitHub guide](github.md) is the GitHub layer's manual; the
[GitLab guide](gitlab.md) lists the GitLab components and the
[Forgejo guide](forgejo.md#reusable-workflows) the Forgejo workflows and
actions. Snippets pin `v0.90.0`; see
[Which version to pin](README.md#documentation-index). For what each
ecosystem builds, see [ecosystems](ecosystems.md).

Most projects call one of the three orchestrators. The leaf workflows
(`build-*`, `publish-*`, `lint-*`, `security-*`, `validate-*` and the release
helpers) can be called directly for custom orchestration; inside this
repository the same workflows are called by local `./.github/workflows/...`
paths.

### Workflow Inventory

Every file in `.github/workflows/` that can be called with `workflow_call`:

| Role | Workflows | Contract |
|---|---|---|
| Supported orchestrators | `pullrequest-orchestrator.yml` (pull requests), `release-orchestrator.yml` (releases), `release-snapshot-orchestrator.yml` (development branches) | Recommended consumer entrypoints |
| Internal composition stages | `pullrequest-quality-stage.yml`, `release-prepare-stage.yml`, `release-build-stage.yml`, `release-publish-stage.yml`, `release-snapshot-build-stage.yml`, `release-snapshot-publish-stage.yml`, `snapshot-images.yml` | Orchestrator internals; callable but less stable |
| Bootstrap | `resolve-forge-ci-bootstrap.yml`, `canonical-cli-bootstrap.yml` | The first jobs of the workflows that run forge-ci: admit the engine commit and the runtime image digests, and build the canonical CLI |
| Build leaves | `build-maven.yml`, `build-npm.yml`, `build-go.yml`, `build-cargo.yml`, `build-gradle-app.yml`, `build-gradle-android.yml`, `build-xcode-ios.yml` | Direct-call building blocks |
| SBOM leaves | `sbom-go.yml`, `sbom-cargo.yml` | Direct-call container-first SBOM building blocks |
| Publish and promotion leaves | `publish-maven-github.yml`, `publish-maven-central.yml`, `publish-container.yml`, `publish-apple-appstore.yml`, `publish-google-play.yml`, `publish-snapshot-npm.yml`, `promote-stage.yml` | Direct-call privileged building blocks; trigger constraint below applies |
| Quality and security leaves | `lint-nanolinter.yml`, `lint-megalinter.yml`, `lint-swift.yml`, `security-openssf-scorecard.yml` | Lint-engine and Swift jobs are selected by the PR orchestrator; Scorecard requires a separate caller |
| Release and validation leaves | `version-bump.yml`, `generate-changelog.yml`, `release-create-github.yml`, `validate-release-prerequisites.yml` | Direct-call release building blocks; trigger constraint applies where secrets are handled |
| Repository build/release support | `build-cli.yml`, `release-binary.yml` | Used by this repository's self-runtime and self-release pipelines; not general project build workflows |
| Experimental attestation | `slsa-attestor.yml` | Operator-managed isolation topology; not a turnkey SLSA level upgrade |

The release orchestrator's `release.*` and `changelog.*` inputs are options of
one run's release and changelog steps: draft or not, which SBOM layers,
signing on or off, the signer allowlist, attach patterns, the changelog
configuration and whether to bump. Hyphenated inputs wire the pipeline:
images, refs, environments and promotion targets. What is built, signed and
published is declared in `.forge-ci/artifacts.yml`; `release.sboms` caps the
per-artifact `sboms` there, and the plan reports a cap with no overlap.

### Trigger policy

Publish, release and signing leaves call `forge-ci validate event-context`
before publishing or signing. Its default allowlist is `push`,
`workflow_dispatch`, `release`, `schedule`, `workflow_run` and `merge_group`;
PR and comment triggers are refused under that policy. The reusable workflows
do not expose an `allowed-events` input.

In a caller-owned workflow step, the CLI accepts `--allowed-events` or
`ALLOWED_EVENTS`; an explicit flag takes precedence over the environment.
A nonempty override replaces the default allowlist, so include every event
the step should accept. Both sources are trusted workflow configuration and
must remain under the workflow owner's control. See the
[threat model](threat-model.md#trust-boundaries) for the job-state assumptions.

The YAML blocks in this page are **job-level snippets**. Place them under
`jobs.<job-id>` in your workflow and add the permissions/secrets required by the
component you call.

See [Workflow Architecture](workflows.md) for the orchestrators and
[Artifacts Reference](artifacts-reference.md) for configuration.

### Two ways to call it

**A) Use the orchestrator as-is:**

```yaml
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

**B) Compose leaves yourself:**

```yaml
jobs:
  build-npm:
    uses: diggsweden/forge-ci/.github/workflows/build-npm.yml@PIN_FORGE_CI_SHA # v0.90.0
    with:
      forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-node-24@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
      working-directory: .

  publish-github:
    needs: build-npm
    uses: diggsweden/forge-ci/.github/workflows/publish-maven-github.yml@PIN_FORGE_CI_SHA # v0.90.0
    permissions:
      actions: read
      contents: read
      packages: write
    with:
      forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
      branch: main
      package-type: npm
      npm-tag: release-{version}
      artifact-source: npm-build-artifacts
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-node-24@sha256:<digest>

  build-container:
    needs: build-npm
    uses: diggsweden/forge-ci/.github/workflows/publish-container.yml@PIN_FORGE_CI_SHA # v0.90.0
    permissions:
      contents: read
      packages: write
      id-token: write
      attestations: write
      actions: read
    secrets:
      # Optional: uploads the image scan SARIF to Code Scanning.
      CODE_SCANNING_TOKEN: ${{ secrets.CODE_SCANNING_TOKEN }}
    with:
      branch: main
      # The canonical CLI of this run: a full engine commit, the admitted
      # image digests and the canonical-* outputs of a canonical-cli-bootstrap.yml
      # job in the same run (see publish-container.yml below).
      forge-ci-binary-ref: <full-engine-commit>
      runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:<digest>
      runtime-image-full: ghcr.io/diggsweden/forge-ci-runtime@sha256:<digest>
      container-file: Containerfile
      artifact-types: npm
```

### Build Workflows

Every build workflow takes `enforce-application-tests`, and the lint,
container-publication and validation workflows take the
`enforce-lint`, `enforce-vulnerabilities` or `enforce-reproducible-builds`
their gates apply: the administrator's
[check enforcement](admin-policy.md), default `false`; the orchestrators
forward them. Whoever writes the call can pass them, so set them only in the
administrator's entry point, the ruleset-required workflow, and never in a
consumer's own workflow.

#### `build-maven.yml`
Builds Maven projects (apps or libraries).
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-maven.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-java-25@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  build-type: app           # "app" or "lib"
  working-directory: "."    # Path to pom.xml
```

Direct callers use `app`/`lib`. In `artifacts.yml`, use
`application`/`library`; the orchestrator maps those values to this workflow's
input. The selection changes the lifecycle and optional profile only; the
project POM/profile owns attached source and Javadoc artifacts.

#### `build-npm.yml`
Builds NPM projects.
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-npm.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-node-24@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  working-directory: "."    # Path to package.json
```

#### `build-go.yml`
Builds artifact-first Go binaries, generates a native CycloneDX Build SBOM with
`cyclonedx-gomod`, and uploads `dist/<goos>-<goarch>/<binary>-<goos>-<goarch>`.
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-go.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  working-directory: "."
  main-package: ./cmd/my-cli
  binary-name: my-cli
  platforms: linux/amd64,linux/arm64,darwin/amd64,darwin/arm64
```

#### `sbom-go.yml`
Generates the native CycloneDX Build SBOM for container-first Go projects with
`cyclonedx-gomod`. It does not compile; the project's Containerfile owns
`go build`.
```yaml
uses: diggsweden/forge-ci/.github/workflows/sbom-go.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  working-directory: "."
```

#### `build-cargo.yml`
Builds artifact-first Rust binaries via `cargo build --release --target …`,
cross-compiles per platform into `dist/<goos>-<goarch>/<binary>-<goos>-<goarch>`
(matching Go's shape), and emits an inline CycloneDX Build SBOM with
`cargo-cyclonedx`. Used for Cargo artifacts with `config.build-mode:
artifact-first`. Container-first cargo continues to use `sbom-cargo.yml`.
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-cargo.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-rust-stable@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  working-directory: "."
  binary-name: my-cli
  platforms: linux/amd64,linux/arm64
```

#### `sbom-cargo.yml`
Generates the lockfile-derived CycloneDX Build SBOM for container-first Rust
projects with `cargo-cyclonedx`. It does not compile; the project's
Containerfile owns `cargo build`.
```yaml
uses: diggsweden/forge-ci/.github/workflows/sbom-cargo.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-rust-stable@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  working-directory: "."
```

#### `build-gradle-app.yml`
Builds Gradle JVM projects (libraries, applications, plugins) and uploads their JARs. Android is out of scope; use `build-gradle-android.yml` for APKs/AABs with flavors and Google Play publishing.
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-gradle-app.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-java-25@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  working-directory: "."       # Path to build.gradle
  gradle-tasks: "build"        # Gradle tasks to run
  skip-tests: false            # Legacy input; true is refused
  artifact-name: ""            # Custom artifact name (optional)
```

#### `build-gradle-android.yml`
Builds Android applications with multiple product flavors and build types. Sole Android path: sets up the Android SDK, handles keystore decoding for signing, and produces split APK/AAB artifacts with Google Play-friendly naming.
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-android-35@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  build-module: "app"             # Gradle module
  product-flavor: "demo"          # Product flavor (demo, prod, etc.)
  build-types: "debug,release"    # Build types to create
  include-aab: true               # Build AAB for Play Store
  enable-signing: true            # Enable Android signing
  artifact-name: ""               # Override the derived upload name (orchestrator passes artifacts.yml `name:`)
  artifact-name-prefix: ""        # Prefix for derived names (ignored when artifact-name is set)
  include-date-stamp: true        # Include date in derived names (ignored when artifact-name is set)
  # gradle-tasks: ""              # Optional: explicit task list, overrides product-flavor/build-types/include-aab
```

Direct-call input defaults are maintained here rather than in each example:

| Parameter | Required | Default | Purpose |
| --- | --- | --- | --- |
| `build-module` | Yes | None | Gradle application module |
| `product-flavor` | No | `""` | Empty selects no flavor |
| `build-types` | No | `"debug,release"` | Comma-separated build types |
| `include-aab` | No | `true` | Build Android App Bundles |
| `gradle-tasks` | No | `""` | Override the derived task list |
| `artifact-name` | No | `""` | Override the derived upload name |
| `artifact-name-prefix` | No | `""` | Prefix derived upload names |
| `include-date-stamp` | No | `true` | Include the date in derived names |
| `enable-signing` | No | `false` | Enable Android signing |
| `working-directory` | No | `"."` | Project directory |
| `branch` | No | `""` | Empty selects the triggering ref |
| `skip-tests` | No | `false` | Legacy compatibility input; `true` is refused. Tests run under administrator-owned enforcement; inspect native reports for actual execution. |
| `enforce-application-tests` | No | `false` | Administrator setting; see [check enforcement](admin-policy.md) |
| `enable-build-sbom` | No | `true` | Run Build SBOM generation |
| `runtime-image` | Yes | | The Android runtime image, by digest (`image@sha256:<64 hex>`) |
| `forge-ci-binary-ref` | Yes | | The full engine commit (40 hex) the workflow ref names |

APK outputs are found under `app/build/outputs/apk/**/debug/*.apk` and
`app/build/outputs/apk/**/release/*.apk`; AABs are under
`app/build/outputs/bundle/**/*.aab` for the example's `app` module.
The [Android example](../examples/android-app/) shows flavor configuration,
keystore wiring and an internal-track snapshot workflow.

**Task derivation.** By default the workflow derives packaging tasks from `product-flavor` + `build-types` + `include-aab`, for example `product-flavor: demo`, `build-types: release`, `include-aab: true` → `assembleDemoRelease app:bundleDemoRelease`.
Before packaging, the builder invokes the selected module's test aggregate under administrator enforcement. No surrounding `build` task is required. Set `gradle-tasks` to override packaging tasks; the module-bound test aggregate is preserved even with overrides. Inspect Gradle task outcomes and reports: cached, up-to-date and `NO-SOURCE` outcomes do not establish physical test execution.

**Artifact naming.** Two modes:

- **Override (orchestrator path).** `release-build-stage.yml` forwards each artifact's `name:` from artifacts.yml as `artifact-name`. The AAB uploads under that exact identifier; APK debug/release and the build SBOM get `<name>-debug`, `<name>-release`, `<name>-sbom`. `release-publish-stage.yml` then hands the same `name:` to `publish-google-play.yml` as `aab-artifact-name`, so the download matches what build uploaded. `artifact-name-prefix` and `include-date-stamp` are ignored in this mode.
- **Derived (direct callers).** When `artifact-name` is empty, upload names are composed as `[<date> - ][<prefix> - ]<repo>[ - <flavor>] - {APK debug|APK release|AAB release|build SBOM}` from `include-date-stamp`, `artifact-name-prefix`, `${{ github.event.repository.name }}`, and `product-flavor`. Direct callers should wire the publish step's `aab-artifact-name` to `${{ needs.build.outputs.aab-name }}` instead of duplicating the format string.

### Artifact publishers

| Workflow | Publishes | Secrets |
|---|---|---|
| `publish-maven-github.yml` | Maven libraries, npm packages and Gradle publications to GitHub Packages, for `publish-to: [forge-packages]` | `GITHUB_TOKEN` |
| `publish-maven-central.yml` | Maven and Gradle JVM/Android libraries to Maven Central | `MAVEN_CENTRAL_USERNAME`, `MAVEN_CENTRAL_PASSWORD`; stable releases also `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PUBLIC_KEY` and, for a protected key, `RELEASE_GPG_PASSPHRASE` |

#### `publish-maven-github.yml`
Publishes Maven libraries, npm packages, and exported Gradle JVM/Android publications
to GitHub Packages. npm requires an explicit `npm-tag`; Gradle requires
`version` and its separate publication artifact. See [Gradle publishing](publishing.md#gradle-jvm-and-android-libraries).
```yaml
uses: diggsweden/forge-ci/.github/workflows/publish-maven-github.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  branch: main
  package-type: maven          # maven, npm, or gradle
  artifact-source: maven-build-artifacts  # Name of workflow artifact
  working-directory: "."
  # The job container: the Java image for maven/gradle, the Node image for npm.
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-java-25@sha256:<digest>
```

#### `publish-maven-central.yml`
Publishes Maven libraries through their profile, or exported Gradle JVM/Android
publications through the Central API. Supply a full prepared commit SHA, trigger
from the signed release request, and pass the publisher secrets listed above.
Set `snapshot: true` for Maven-format snapshots. The snapshot
orchestrator selects this mode with `publish-maven-snapshots: true`; snapshot
jobs do not import release signing keys or require signed release-request tags.
```yaml
uses: diggsweden/forge-ci/.github/workflows/publish-maven-central.yml@<reviewed-ref>
with:
  forge-ci-binary-ref: <reviewed-ref>
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-java-25@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  branch: <full-prepared-release-sha>
  artifact-source: maven-build-artifacts  # Name of workflow artifact
  working-directory: "."
  settings-path: ".mvn/settings.xml"
```

### Container Workflows

#### `publish-container.yml`
Production container builds with full security features. Supports multiple registries.
The key-holding jobs (early key check, layout publication, manifest signing and
attestation) run in `runtime-image`, the per-platform SBOM evidence in
`runtime-image-full`; the Buildah build legs are host jobs that receive the
canonical CLI of the run. A direct caller therefore runs
`canonical-cli-bootstrap.yml` first and forwards its outputs as the
`canonical-*` inputs, with `forge-ci-binary-ref` as the full engine commit and
`runtime-image` as the admitted base image digest, exactly as
`release-publish-stage.yml` does.
```yaml
uses: diggsweden/forge-ci/.github/workflows/publish-container.yml@PIN_FORGE_CI_SHA # v0.90.0
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
  container-file: "Containerfile"
  context: "."
  artifact-types: maven
  platforms: "linux/amd64,linux/arm64"
  enable-slsa: true
  enable-analyzed-container-sbom: true   # CycloneDX SBOM of the built image
  registry: "ghcr.io"
```

### Other Components

#### `version-bump.yml`
Handles version bumping and updates version files. Secrets: `RELEASE_TOKEN`,
which pushes the bump commit, and a commit signing key:
`RELEASE_GPG_PRIVATE_KEY` with `RELEASE_GPG_PASSPHRASE`, or
`RELEASE_SSH_SIGNING_KEY` when `git-signing-method` is `ssh`.
```yaml
uses: diggsweden/forge-ci/.github/workflows/version-bump.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  project-type: maven      # Determines version file (pom.xml vs package.json)
  branch: main             # Branch to check out, update and push
  working-directory: "."   # Path to project root
```

#### `generate-changelog.yml`
Generates changelog from git commits.
```yaml
uses: diggsweden/forge-ci/.github/workflows/generate-changelog.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  branch: main             # Base branch for changelog comparison
  changelog-config: ""     # Optional: custom git-cliff config
```

#### `release-create-github.yml`
Creates the GitHub release with its assets; the release orchestrator calls
it. Secrets: `RELEASE_TOKEN`, and the signing credentials of the
[selected backend](verification.md#choosing-a-method) when `sign-artifacts` is
`true`; GPG final tags also need the public `RELEASE_GPG_PUBLIC_KEY`.

A direct caller supplies:

- the `canonical-*` inputs of a `canonical-cli-bootstrap.yml` job in the same
  run, with `forge-ci-binary-ref` as the full engine commit and
  `runtime-image` as the admitted base image digest;
- `authorized-source-sha`, the commit of the signed request, apart from the
  prepared `checkout-ref`;
- `request-is-release: true` only for a planned release with no version
  bump, where the two commits are the same;
- `require-allowlisted-signer` from the release policy.

The signer and the publisher check the request signature again before they
sign or publish. They run no consumer tool, but Git on the runner still reads
its own startup configuration, so the runner must be trusted. See
[binding the release to its request](verification.md#binding-the-release-to-its-request).
Release files beyond the planned ones are attached only through explicit,
workspace-relative `attach-artifacts` patterns (for example,
`release-extra/**/*.txt`).

#### `validate-release-prerequisites.yml`
Validates release requirements; the release orchestrator calls it first.

### PR Orchestrator

#### `pullrequest-orchestrator.yml`
Orchestrates all quality checks for pull requests. Composes a control-plane interface, delegates to the quality stage, and produces a top-level summary.

```yaml
uses: diggsweden/forge-ci/.github/workflows/pullrequest-orchestrator.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # the full commit the uses: ref names; every job runs the CLI built from it
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest: control-plane and summary jobs
  canonical-builder-image: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest: builds the canonical CLI
  project-type: maven              # Required: maven, npm, gradle, gradle-android, xcode-ios, cargo, or go
  base-branch: ""                  # Optional: auto-detects PR target
  lint-engine: nanolinter          # General lint engine: nanolinter (default) | megalinter | none — mutually exclusive
  required-lints: secrets,sast     # Caller-selected floor; the project's nanolinter.toml cannot remove it
  nanolinter-image: ghcr.io/itiquette/nanolinter:java@sha256:PIN_DIGEST  # Required for nanolinter: the flavour for project-type, pinned by digest
  megalinter-image: ""             # Required for megalinter; see the digest-pinned example below
  linters.swiftformat: false       # Swift format for iOS/macOS (separate macOS job, orthogonal to the engine)
  linters.swiftlint: false         # SwiftLint for iOS/macOS
```

The quality stage's result follows the
[Stage Result Contract](workflows.md#stage-result-contract).

### Lint Workflows

These workflows are automatically called by `pullrequest-orchestrator.yml`. The
`lint-engine` input picks the single general engine (mutually exclusive, because two
would duplicate Code Scanning findings); Swift/iOS checks run separately in
`lint-swift.yml` regardless of the engine.

The orchestrator defaults to `nanolinter`; `forge-ci plan pr` on its own
defaults to `none`, and warns that no general lint runs. The input alone
selects the engine; a config file is optional. See
[Linting](verification.md#linting).

#### `lint-nanolinter.yml` (default)
Runs `nanolinter verify` against the consumer's `nanolinter.toml` verify plan,
inside the nanolinter flavour image (which bakes nanolinter and its check
toolchain, so no `just`/`justfile` is required). Fast, node-less. Security findings
upload to GitHub Code Scanning as SARIF.
```yaml
uses: diggsweden/forge-ci/.github/workflows/lint-nanolinter.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  nanolinter-image: ghcr.io/itiquette/nanolinter:java@sha256:PIN_DIGEST
  forge-ci-binary-ref: PIN_FORGE_CI_SHA
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  canonical-artifact-id: ${{ needs.canonical-cli.outputs.artifact-id }}
  # ... the other six canonical-* inputs, as in the publish-container example
```

A direct call is a canonical receiver like `publish-container.yml`: a
`canonical-cli` job in your workflow builds the CLI of the run from the engine
commit (`canonical-cli-bootstrap.yml`), and the lint job receives it before
the checkout. The [GHCR example in publishing.md](publishing.md#github-container-registry-ghcrio)
shows the `canonical-cli` job and the seven `canonical-*` inputs it feeds.

**Flavour image.** The examples pin a flavour of
`ghcr.io/itiquette/nanolinter` by digest. Replace `PIN_DIGEST` with the digest your registry serves for the
platform you run on, or pass a mirror through `nanolinter-image`.

Nanolinter's image layout has one image per ecosystem; there is no
image that carries every toolchain. Pick the flavour for `project-type`:

| `project-type` | Flavour |
|---|---|
| `go` | `go` |
| `maven` | `java` |
| `gradle`, `gradle-android` | `kotlin` |
| `npm` | `javascript-typescript` |
| `cargo` | `rust` |
| `xcode-ios` | `swift` |
| anything else | `core` (secrets, SAST, dependency scans, text and CI files) |

`nanolinter-image` is required, pinned by digest, on the orchestrator (with
`lint-engine: nanolinter`), on a direct call of `lint-nanolinter.yml` and on
the GitLab `nanolinter` component. None has a default: the image receives the
checkout and the run context, and a component cannot tell which registry
serves it. `forge-ci plan pr` refuses a tag in the plan job and names the
flavour for `project-type`; `lint-nanolinter.yml` refuses one in its first
step, and GitLab refuses one before the pipeline starts. Pin the flavour for
your ecosystem, or a mirror of it, as the examples show (Renovate's docker
manager then moves tag and digest together). A polyglot repository runs one
lint job per ecosystem, each in its own flavour.

**The same choice on each forge.** "Lint with nanolinter for my ecosystem" is
said once per forge, in the place that forge has for it:

| Forge | Where the flavour is chosen | What the lint job runs in |
|---|---|---|
| GitHub | `nanolinter-image` on the orchestrator, by digest | the flavour image, started by the runner |
| GitLab | `nanolinter-image` on the `nanolinter` component, by digest | that image, as the job image |
| Forgejo | the `CI_JOB_IMAGE` variable, a flavour image by digest | that image; the `lint-nanolinter` action runs the nanolinter it finds there and installs nothing |

On every forge the same `nanolinter.toml` is read and the same
`required-lints` floor applies. The `commits` check, in every example plan,
validates the pull request's commit messages with gommitlint against the base
branch, which is why the checkouts fetch it; every flavour image carries
gommitlint.

**Mandated lint floor.** The caller selects `required-lints` (default
`secrets,sast`). A nonempty floor forces those checks to run and block regardless
of the project's `nanolinter.toml`; an empty input disables the floor. See
[Linting](verification.md#linting) for the caller/project policy boundary.

Swift/iOS linting runs separately in `lint-swift.yml` (macOS), because nanolinter has
no native macOS binary.

#### `lint-megalinter.yml`
Runs a caller-selected MegaLinter image against the project's `.mega-linter.yml`.
Through the PR orchestrator, select `lint-engine: megalinter`. Both direct and
orchestrated calls require `megalinter-image` pinned by digest; there is no
default MegaLinter image, and tag-only references are rejected.
The image receives a writable checkout, a read token and the runner's read-write
Docker socket, so choose a reviewed image digest. Its security findings use
the Code Scanning category `megalinter-security`.

For a direct call, replace `REVIEWED_SHA256` below with the reviewed image
digest's 64 lowercase hexadecimal characters:

```yaml
uses: diggsweden/forge-ci/.github/workflows/lint-megalinter.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  megalinter-image: oxsecurity/megalinter@sha256:REVIEWED_SHA256
  forge-ci-binary-ref: PIN_FORGE_CI_SHA
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
  canonical-artifact-id: ${{ needs.canonical-cli.outputs.artifact-id }}
  # ... the other six canonical-* inputs; the job is a canonical receiver like lint-nanolinter.yml
```

### Security Workflows

The PR orchestrator selects the lint engine; Scorecard is a separate caller-owned
job.

SAST, dependency scanning and secret detection run inside the selected lint
engine: `nanolinter` runs OpenGrep, OSV, secret detection and trivy-fs
(configured in `nanolinter.toml`), `megalinter` its own linters (configured in
`.mega-linter.yml`). Findings go to Code Scanning; see
[Code scanning (SARIF)](github.md#code-scanning-sarif).

#### `security-openssf-scorecard.yml`
Generates an OpenSSF security scorecard for the repository. Invoke it explicitly
from your caller workflow and define `on.schedule` there for scheduled analysis.

```yaml
uses: diggsweden/forge-ci/.github/workflows/security-openssf-scorecard.yml@PIN_FORGE_CI_SHA # v0.90.0
with:
  forge-ci-binary-ref: PIN_FORGE_CI_SHA # v0.90.0
  # The SARIF upload job runs in this image with its baked forge-ci.
  runtime-image: ghcr.io/diggsweden/forge-ci-runtime-base@sha256:0000000000000000000000000000000000000000000000000000000000000000 # FIXME v0.90.0 digest
```

### Snapshot vs Production Release

| Aspect | Snapshot | Production |
|--------|----------|------------|
| Relative scope | Lightweight artifact-only branch flow | Full build, policy, signing, publication, and promotion flow |
| Container image | ✓ by default (`snapshot-images.yml`, vulnerability scan; `publish-containers: false` opts out) | ✓ + SLSA + SBOM + vulnerability scan |
| Build artifacts | ✓ for artifact-first ecosystems | ✓ |
| SBOMs | Default `none`; opt in with `sboms` | Default `all` |
| NPM publish | ✓ (snapshot tag) | ✓ |
| Maven publish | Opt in with `publish-maven-snapshots: true` | ✓ (libraries only) |
| GitHub Release | — | ✓ |
