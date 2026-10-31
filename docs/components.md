# Components Reference

## Available Components

This document is the authoritative inventory of reusable workflow components and
their direct-call contracts. Architecture pages link here instead of maintaining
parallel workflow lists.

> **Current source versus release pins:** `v0.90.0` examples identify a historical
> baseline, not the current source revision. Confirm matching workflows, CLI
> assets and runtime images before adoption. For unreleased changes, follow
> [Branch Testing](runtime-images.md#branch-testing).

---

> For a per-ecosystem capability matrix and the artifact-first vs container-first framing (when does a language ship via `build-<lang>.yml` vs `sbom-<lang>.yml` + Containerfile compile?), see **[docs/ecosystems.md](ecosystems.md)**.

This page covers the GitHub layer; the [GitHub guide](github.md) is its manual,
and the [GitLab guide](gitlab.md) lists the GitLab components. The Forgejo layer's reusable workflows and
composite actions, under `.forgejo/`, are listed in the
[Forgejo guide](forgejo.md#reusable-workflows); it has no orchestrators, since
Forgejo resolves a nested relative `uses:` against the calling repository. Its
lint and build are [actions](forgejo.md#build-and-checks) for the consumer's
own jobs.

**Supported high-level GitHub entrypoints:**
- `pullrequest-orchestrator.yml`
- `release-orchestrator.yml`
- `release-snapshot-orchestrator.yml`

Leaf helper workflows can still be used directly by advanced consumers, and are suitable for custom orchestration. These are `build-*`, `publish-*`, `lint-*`, `security-*`, `validate-*`, and selected release helpers. The examples below use external consumer refs; inside this repository's own workflows, the same components are called with local `./.github/workflows/...` paths.

### Workflow Inventory

This table is the complete inventory of files in `.github/workflows/` that
currently expose `workflow_call`, including repository-specific helpers.
Workflows without that callable interface are excluded.

| Role | Workflows | Contract |
|---|---|---|
| Supported orchestrators | `pullrequest-orchestrator.yml`, `release-orchestrator.yml`, `release-snapshot-orchestrator.yml` | Recommended consumer entrypoints |
| Internal composition stages | `pullrequest-quality-stage.yml`, `release-prepare-stage.yml`, `release-build-stage.yml`, `release-publish-stage.yml`, `release-snapshot-build-stage.yml`, `release-snapshot-publish-stage.yml` | Orchestrator internals; callable but less stable |
| Build leaves | `build-maven.yml`, `build-npm.yml`, `build-go.yml`, `build-cargo.yml`, `build-gradle-app.yml`, `build-gradle-android.yml`, `build-xcode-ios.yml` | Direct-call building blocks |
| SBOM leaves | `sbom-go.yml`, `sbom-cargo.yml` | Direct-call container-first SBOM building blocks |
| Publish and promotion leaves | `publish-maven-github.yml`, `publish-maven-central.yml`, `publish-container.yml`, `publish-apple-appstore.yml`, `publish-google-play.yml`, `publish-snapshot-npm.yml`, `promote-stage.yml` | Direct-call privileged building blocks; trigger constraint below applies |
| Quality and security leaves | `lint-nanolinter.yml`, `lint-megalinter.yml`, `lint-swift.yml`, `security-openssf-scorecard.yml` | Lint-engine and Swift jobs are selected by the PR orchestrator; Scorecard requires a separate caller |
| Release and validation leaves | `version-bump.yml`, `generate-changelog.yml`, `release-create-github.yml`, `validate-release-prerequisites.yml` | Direct-call release building blocks; trigger constraint applies where secrets are handled |
| Repository build/release support | `build-cli.yml`, `release-binary.yml` | Used by this repository's self-runtime and self-release pipelines; not general project build workflows |
| Experimental attestation | `slsa-attestor.yml` | Operator-managed isolation topology; not a turnkey SLSA level upgrade |

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

**When to use components:**
- You need fine-grained control over builds, publishing, validation, or security checks
- You want to compose custom workflows around leaf helpers
- You're integrating with existing CI/CD pipelines

**When to use orchestrators:**
- You want a complete, ready-to-use release workflow
- You prefer convention over configuration
- You're starting a new project (recommended)

See [Workflow Guide](workflows.md) for orchestrator documentation and [Artifacts Reference](artifacts-reference.md) for configuration.

### Two ways to call it

**A) Use the orchestrator as-is:**

```yaml
uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@v0.90.0
with:
  branch: main
  forge-ci-binary-ref: v0.90.0
  artifacts-config: .forge-ci/artifacts.yml
  release-publisher: github-cli
```

**B) Compose leaves yourself:**

```yaml
jobs:
  build-npm:
    uses: diggsweden/forge-ci/.github/workflows/build-npm.yml@v0.90.0
    with:
      working-directory: .

  publish-github:
    needs: build-npm
    uses: diggsweden/forge-ci/.github/workflows/publish-maven-github.yml@v0.90.0
    permissions:
      actions: read
      contents: read
      packages: write
    with:
      branch: main
      package-type: npm
      npm-tag: release-{version}
      artifact-source: npm-build-artifacts

  build-container:
    needs: build-npm
    uses: diggsweden/forge-ci/.github/workflows/publish-container.yml@v0.90.0
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
      forge-ci-binary-ref: v0.90.0
      container-file: Containerfile
      artifact-types: npm
```

### Common Components

#### Artifact Publishers

| Component | Purpose | Output | Required Secrets | Use When |
|-----------|---------|--------|------------------|----------|
| **publish-maven-github** | Publishes Maven libraries/NPM to GitHub Packages | Artifacts in GitHub Packages | GITHUB_TOKEN | When supported artifacts include `publish-to: [forge-packages]` |
| **publish-maven-central** | Publishes Maven and Gradle JVM/Android libraries | Public Maven artifacts | MAVEN_CENTRAL_USERNAME, MAVEN_CENTRAL_PASSWORD; stable releases also need RELEASE_GPG_PRIVATE_KEY, RELEASE_GPG_PUBLIC_KEY and the passphrase for a protected key | Stable: signed release request; snapshot: ordinary CI authorization |

#### Container Builders

| Component | Purpose | Features | Build Time | Use When |
|-----------|---------|----------|------------|----------|
| **publish-container** | Production multi-platform container builds | SLSA attestation, SBOM, vulnerability scanning, native split-runner multi-arch (no QEMU) | Varies by image/platform | Production releases |

#### Release Tools

| Component | Purpose | Creates/Updates | Required Secrets | Use When |
|-----------|---------|----------------|------------------|----------|
| **release-create-github** | GitHub release creation | GitHub release, changelog, signatures | RELEASE_TOKEN; signing credentials when `sign-artifacts: true`, according to the [selected backend](verification.md#choosing-a-method) | Any production release |
| **version-bump** | Version management | Updated version files | GITHUB_TOKEN, RELEASE_TOKEN | Before releases |
| **generate-changelog** | Changelog generation | Formatted changelog | GITHUB_TOKEN | Before releases |

#### Validators

| Component | Purpose | Validates | Blocks On | Use When |
|-----------|---------|-----------|-----------|----------|
| **validate-release-prerequisites** | Pre-release checks | Version match, permissions, secrets | Any validation failure | Before any release |

> **Note:** To request a new component or publisher, open an issue in the forge-ci repository.

### Build Workflows

#### `build-maven.yml`
Builds Maven projects (apps or libraries).
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-maven.yml@v0.90.0
with:
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
uses: diggsweden/forge-ci/.github/workflows/build-npm.yml@v0.90.0
with:
  working-directory: "."    # Path to package.json
```

#### `build-go.yml`
Builds artifact-first Go binaries, generates a native CycloneDX Build SBOM with
`cyclonedx-gomod`, and uploads `dist/<goos>-<goarch>/<binary>-<goos>-<goarch>`.
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-go.yml@v0.90.0
with:
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
uses: diggsweden/forge-ci/.github/workflows/sbom-go.yml@v0.90.0
with:
  working-directory: "."
```

#### `build-cargo.yml`
Builds artifact-first Rust binaries via `cargo build --release --target …`,
cross-compiles per platform into `dist/<goos>-<goarch>/<binary>-<goos>-<goarch>`
(matching Go's shape), and emits an inline CycloneDX Build SBOM with
`cargo-cyclonedx`. Used for Cargo artifacts with `config.build-mode:
artifact-first`. Container-first cargo continues to use `sbom-cargo.yml`.
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-cargo.yml@v0.90.0
with:
  working-directory: "."
  binary-name: my-cli
  platforms: linux/amd64,linux/arm64
```

#### `sbom-cargo.yml`
Generates the lockfile-derived CycloneDX Build SBOM for container-first Rust
projects with `cargo-cyclonedx`. It does not compile; the project's
Containerfile owns `cargo build`.
```yaml
uses: diggsweden/forge-ci/.github/workflows/sbom-cargo.yml@v0.90.0
with:
  working-directory: "."
```

#### `build-gradle-app.yml`
Builds Gradle JVM projects (libraries, applications, plugins) and uploads their JARs. Android is out of scope; use `build-gradle-android.yml` for APKs/AABs with flavors and Google Play publishing.
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-gradle-app.yml@v0.90.0
with:
  working-directory: "."       # Path to build.gradle
  gradle-tasks: "build"        # Gradle tasks to run
  skip-tests: false            # Legacy input; true is refused
  artifact-name: ""            # Custom artifact name (optional)
```

#### `build-gradle-android.yml`
Builds Android applications with multiple product flavors and build types. Sole Android path: sets up the Android SDK, handles keystore decoding for signing, and produces split APK/AAB artifacts with Google Play-friendly naming.
```yaml
uses: diggsweden/forge-ci/.github/workflows/build-gradle-android.yml@v0.90.0
with:
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
| `enable-build-sbom` | No | `true` | Run Build SBOM generation |
| `runtime-image` | No | Matching Android runtime image | Toolchain image override |

APK outputs are found under `app/build/outputs/apk/**/debug/*.apk` and
`app/build/outputs/apk/**/release/*.apk`; AABs are under
`app/build/outputs/bundle/**/*.aab` for the example's `app` module.
The [Android example](../examples/android-app/) shows flavor configuration,
keystore wiring and an internal-track snapshot workflow.

**Task derivation.** By default the workflow derives packaging tasks from `product-flavor` + `build-types` + `include-aab`, for example `product-flavor: demo`, `build-types: release`, `include-aab: true` → `assembleDemoRelease app:bundleDemoRelease`. Before packaging, the builder invokes the selected module's test aggregate under administrator enforcement. No surrounding `build` task is required. Set `gradle-tasks` to override packaging tasks; the module-bound test aggregate is preserved even with overrides. Inspect Gradle task outcomes and reports: cached, up-to-date and `NO-SOURCE` outcomes do not establish physical test execution.

**Artifact naming.** Two modes:

- **Override (orchestrator path).** `release-build-stage.yml` forwards each artifact's `name:` from artifacts.yml as `artifact-name`. The AAB uploads under that exact identifier; APK debug/release and the build SBOM get `<name>-debug`, `<name>-release`, `<name>-sbom`. `release-publish-stage.yml` then hands the same `name:` to `publish-google-play.yml` as `aab-artifact-name`, so the download matches what build uploaded. `artifact-name-prefix` and `include-date-stamp` are ignored in this mode.
- **Derived (direct callers).** When `artifact-name` is empty, upload names are composed as `[<date> - ][<prefix> - ]<repo>[ - <flavor>] - {APK debug|APK release|AAB release|build SBOM}` from `include-date-stamp`, `artifact-name-prefix`, `${{ github.event.repository.name }}`, and `product-flavor`. Direct callers should wire the publish step's `aab-artifact-name` to `${{ needs.build.outputs.aab-name }}` instead of duplicating the format string.

### Publish Workflows

#### `publish-maven-github.yml`
Publishes Maven libraries, npm packages, and exported Gradle JVM/Android publications
to GitHub Packages. npm requires an explicit `npm-tag`; Gradle requires
`version` and its separate publication artifact. See [Gradle publishing](gradle-publishing.md).
```yaml
uses: diggsweden/forge-ci/.github/workflows/publish-maven-github.yml@v0.90.0
with:
  branch: main
  package-type: maven          # maven, npm, or gradle
  artifact-source: maven-build-artifacts  # Name of workflow artifact
  working-directory: "."
```

#### `publish-maven-central.yml`
Publishes Maven libraries through their profile, or exported Gradle JVM/Android
publications through the Central API. Supply a full prepared commit SHA, trigger
from the signed release request, and pass the publisher secrets listed above.
New publication inputs require matching engine, workflow and runtime revisions;
see [branch testing](runtime-images.md#branch-testing).
Set `snapshot: true` for Maven-format snapshots. The existing snapshot
orchestrator selects this mode with `publish-maven-snapshots: true`; snapshot
jobs do not import release signing keys or require signed release-request tags.
```yaml
uses: diggsweden/forge-ci/.github/workflows/publish-maven-central.yml@<reviewed-ref>
with:
  branch: <full-prepared-release-sha>
  artifact-source: maven-build-artifacts  # Name of workflow artifact
  working-directory: "."
  settings-path: ".mvn/settings.xml"
```

### Container Workflows

#### `publish-container.yml`
Production container builds with full security features. Supports multiple registries.
```yaml
uses: diggsweden/forge-ci/.github/workflows/publish-container.yml@v0.90.0
with:
  branch: main
  forge-ci-binary-ref: v0.90.0
  container-file: "Containerfile"
  context: "."
  artifact-types: maven
  platforms: "linux/amd64,linux/arm64"
  enable-slsa: true
  enable-analyzed-container-sbom: true   # CycloneDX SBOM of the built image
  enable-scan: true
  registry: "ghcr.io"
```

### Other Components

#### `version-bump.yml`
Handles version bumping and updates version files.
```yaml
uses: diggsweden/forge-ci/.github/workflows/version-bump.yml@v0.90.0
with:
  project-type: maven      # Determines version file (pom.xml vs package.json)
  branch: main             # Base branch for comparison
  working-directory: "."   # Path to project root
```

#### `generate-changelog.yml`
Generates changelog from git commits.
```yaml
uses: diggsweden/forge-ci/.github/workflows/generate-changelog.yml@v0.90.0
with:
  branch: main             # Base branch for changelog comparison
  changelog-config: ""     # Optional: custom git-cliff config
```

#### `release-create-github.yml`
Creates GitHub releases with assets.

Usually called by `release-orchestrator.yml`, but can also be used directly by advanced consumers that want lower-level release composition.

#### `validate-release-prerequisites.yml`
Validates release requirements (called automatically by orchestrator).

Usually called by `release-orchestrator.yml`, but can also be used directly by advanced consumers that want explicit prerequisite validation.

### PR Orchestrator

#### `pullrequest-orchestrator.yml`
Orchestrates all quality checks for pull requests. Composes a control-plane interface, delegates to the quality stage, and produces a top-level summary.

```yaml
uses: diggsweden/forge-ci/.github/workflows/pullrequest-orchestrator.yml@v0.90.0
with:
  project-type: maven              # Required: maven, npm, gradle, gradle-android, xcode-ios, cargo, or go
  base-branch: ""                  # Optional: auto-detects PR target
  lint-engine: nanolinter          # General lint engine: nanolinter (default) | megalinter | none — mutually exclusive
  required-lints: secrets,sast     # Caller-selected floor; the project's nanolinter.toml cannot remove it
  nanolinter-image: ""              # Optional: empty derives the nanolinter flavour from project-type; set to pin a digest or mirror
  megalinter-image: ""             # Required for megalinter; see the digest-pinned example below
  linters.swiftformat: false       # Swift format for iOS/macOS (separate macOS job, orthogonal to the engine)
  linters.swiftlint: false         # SwiftLint for iOS/macOS
  forge-ci-binary-ref: v0.90.0   # Match the pinned workflow release
```

**Behavior:** The orchestrator remains the supported entrypoint. Internally it delegates to the quality stage, which writes a normalized manifest consumed by the top-level PR summary. See [Stage Result Contract](workflows.md#stage-result-contract) for the internal schema.

### Lint Workflows

These workflows are automatically called by `pullrequest-orchestrator.yml`. The
`lint-engine` input picks the single general engine (mutually exclusive, because two
would duplicate Code Scanning findings); Swift/iOS checks run separately in
`lint-swift.yml` regardless of the engine.

GitHub's orchestrator defaults to `nanolinter`; the provider-neutral
`forge-ci plan pr` command defaults to `none`. Selecting `none` (or leaving
the CLI selection empty) emits a warning during PR planning that no supported
lint engine is enabled and general lint checks will not run. Selection uses
the explicit input, not config-file presence or executable discovery. A missing
executable for a selected engine remains a job failure, not a disabled engine.
Nanolinter's `required-lints` floor and MegaLinter's blocking gate still apply
when their engine is selected. Config files are optional customizations: without
one, the selected engine uses its defaults, not the `none` selection. Init does
not create either engine's config; consumers own those files, with examples
available to copy when custom settings are wanted.

#### `lint-nanolinter.yml` (default)
Runs `nanolinter verify` against the consumer's `nanolinter.toml` verify plan,
inside the nanolinter flavour image (which bakes nanolinter and its check
toolchain, so no `just`/`justfile` is required). Fast, node-less. Security findings
upload to GitHub Code Scanning as SARIF.
```yaml
uses: diggsweden/forge-ci/.github/workflows/lint-nanolinter.yml@v0.90.0
with:
  nanolinter-image: ghcr.io/itiquette/nanolinter:java@sha256:PIN_DIGEST
```

**Flavour image.** The configured repository is `ghcr.io/itiquette/nanolinter`.
The GHCR migration must be published and verified before activating these
defaults. Replace `PIN_DIGEST` with a reviewed, registry-served digest for the
required platform, or pass an available mirror through `nanolinter-image`.
The repository name and placeholder alone are not availability evidence.

Nanolinter's image layout has one image per ecosystem; there is no
image that carries every toolchain. Through the orchestrator, `forge-ci plan
pr` picks the flavour from `project-type`:

| `project-type` | Flavour |
|---|---|
| `go` | `go` |
| `maven` | `java` |
| `gradle`, `gradle-android` | `kotlin` |
| `npm` | `javascript-typescript` |
| `cargo` | `rust` |
| `xcode-ios` | `swift` |
| anything else | `core` (secrets, SAST, dependency scans, text and CI files) |

The `nanolinter-image` input overrides the derived image verbatim, for example to
pin `@sha256:` or use a mirror. A direct call of `lint-nanolinter.yml` knows no
project type, so it defaults to the `core` tag, as does the GitLab
`nanolinter` component; pass the flavour for your ecosystem, pinned by digest as
the examples show (Renovate's docker manager then moves tag and digest
together). The defaults become digests once the images are published. A polyglot repository runs one lint job per
ecosystem, each in its own flavour.

**The same choice on each forge.** "Lint with nanolinter for my ecosystem" is
said once per forge, in the place that forge has for it:

| Forge | Where the flavour is chosen | What the lint job runs in |
|---|---|---|
| GitHub | `project-type` on the orchestrator; `nanolinter-image` overrides | the flavour image, started by the runner |
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
uses: diggsweden/forge-ci/.github/workflows/lint-megalinter.yml@v0.90.0
with:
  megalinter-image: oxsecurity/megalinter@sha256:REVIEWED_SHA256
```

### Security Workflows

The PR orchestrator selects the lint engine; Scorecard is a separate caller-owned
job.

> **SAST + dependency scanning** are no longer standalone workflows. The
> selected lint engine runs them as part of its plan: `nanolinter` (default)
> runs OpenGrep SAST, OSV dependency scanning, secret detection, and trivy-fs
> (configure in `nanolinter.toml`); `megalinter` runs its own SAST/secrets
> linters (configure in `.mega-linter.yml`). Either uploads its findings to
> GitHub Code Scanning as SARIF via `lint-nanolinter.yml` / `lint-megalinter.yml`.

SARIF is uploaded to GitHub Code Scanning when `CODE_SCANNING_TOKEN` is passed
to the workflow and SARIF upload is enabled. The lint engines do not save SARIF
as a downloadable artifact; Scorecard and container-scan jobs do. See the
[Code Scanning reference](reference.md#code_scanning_token) for categories and
artifact names.

#### `security-openssf-scorecard.yml`
Generates an OpenSSF security scorecard for the repository. Invoke it explicitly
from your caller workflow and define `on.schedule` there for scheduled analysis.
The legacy `cron-schedule` input is ignored and does not schedule runs.

```yaml
uses: diggsweden/forge-ci/.github/workflows/security-openssf-scorecard.yml@v0.90.0
```

---

## Workflow Reference

### Orchestrator Workflows

| Workflow | Purpose | When to Use |
|----------|---------|-------------|
| `pullrequest-orchestrator.yml` | Pull request quality control plane | Every repository |
| `release-orchestrator.yml` | Production release control plane | Production releases |
| `release-snapshot-orchestrator.yml` | Lightweight snapshot release control plane (npm/SBOM-only) | Development branches |

### Snapshot vs Production Release

| Aspect | Snapshot | Production |
|--------|----------|------------|
| Relative scope | Lightweight artifact-only branch flow | Full build, policy, signing, publication, and promotion flow |
| Container image | — (builds none; a release-built image is promoted to `:dev` by the promotion ladder) | ✓ + SLSA + SBOM + vulnerability scan |
| Build artifacts | ✓ for artifact-first ecosystems | ✓ |
| SBOMs | Default `none`; opt in with `sboms` | Default `all` |
| NPM publish | ✓ (snapshot tag) | ✓ |
| Maven publish | — | ✓ (libraries only) |
| GitHub Release | — | ✓ |
