# SBOM generation

forge-ci produces Software Bills of Materials at multiple points in the pipeline. This page tells you what you get by default, how to change that, and how it works under the hood.

## Quick start

By default, SBOM-capable artifact types request all three layers: Build,
Analyzed-artifact and Analyzed-container. Snapshot builds generate none by
default. If that suits you, leave `sboms` unset; Android artifacts are the
exception (see [Defaults](#defaults)). To change it, set the `sboms` field.

## The `sboms` field

A single string field, accepted in two places:

- **Per-artifact** in `artifacts.yml`: what kinds of SBOMs that artifact produces during build/container stages.
- **Per-orchestrator-call** (input on `release-orchestrator.yml`, `release-snapshot-orchestrator.yml`, `release-create-github.yml`): release/snapshot selection policy.

What each one controls:

- **The artifact's `sboms`** decides what is generated: the artifact-first
  Build SBOM job (`enable-build-sbom`), the analyzed-artifact SBOM, and the
  analyzed-container SBOM attested on each image built from the artifact.
  `sboms: none` skips all of them for that artifact.
- **`release.sboms`** on the release orchestrator is intersected with what the
  artifacts produce. It selects what goes into the release's SBOM bundle, and
  it gates the container-first Go and Cargo Build SBOM jobs. It does not turn
  off artifact-first Build SBOM jobs or the image SBOM attestations, so
  `release.sboms: none` is not a general off switch.
- **The snapshot orchestrator's `sboms`** (default `none`) selects the
  snapshot's package SBOM layers.

The input is `release.sboms` on the release orchestrator, whose inputs carry a
`release.` prefix, and `sboms` on the snapshot orchestrator.

### Accepted values

| Value | Produces |
|---|---|
| `all` | `build` + `analyzed-artifact` + `analyzed-container` |
| `none` | nothing |
| `build` | Ecosystem-native CycloneDX dependency metadata |
| `analyzed-artifact` | Syft scan of the built artifact only |
| `analyzed-container` | Syft scan of the pushed container only |
| `build,analyzed-artifact` | any comma-list of the three layer names |

`all` and `none` are shortcuts and cannot be combined with layer names. Whitespace around commas is tolerated. Unknown tokens are rejected at parse time.

### Defaults

| Where | Default | Why |
|---|---|---|
| Per-artifact (`artifacts.yml`) | `all` for SBOM-capable ecosystems (`maven`, `npm`, `gradle`, `gradle-android`, `cargo`, `go`); `none` for `xcode-ios` and `meta` | SBOMs on for buildable types |
| Release orchestrator (`release.sboms`) | `all` | Runs once per release; SBOMs are expected for compliance |
| Snapshot orchestrator (`sboms`) | `none` | Runs on every push; most reviews don't need SBOMs |

Gradle Android must leave out `analyzed-artifact`: there is no analyzed SBOM
for APK/AAB files, and requesting one fails release assembly. Set Android
artifacts to `sboms: build`, or `sboms: build,analyzed-container` when they
feed a container.

### Examples

Each block below is a separate example. Choose one `artifacts.yml` variant;
the later `jobs` blocks belong in a caller workflow, with its required triggers,
permissions and secrets (see [Components Reference](components.md)). Replace
`<sha>` with the reviewed workflow commit.

```yaml
# artifacts.yml — default behaviour (equivalent to sboms: all for maven)
artifacts:
  - name: my-app
    project-type: maven
```

```yaml
# Select only the Build SBOM layer
artifacts:
  - name: my-lib
    project-type: maven
    sboms: build
```

```yaml
# Explicit opt-out (rare; this artifact contributes no SBOMs)
artifacts:
  - name: my-toy
    project-type: maven
    sboms: none
```

```yaml
# Release selection: include only Build SBOMs in the release SBOM bundle
jobs:
  release:
    uses: diggsweden/forge-ci/.github/workflows/release-orchestrator.yml@PIN_FORGE_CI_SHA # v0.90.0
    with:
      # Engine commit and image digests: see the Advanced Pinning Recipe.
      branch: main
      artifacts-config: .forge-ci/artifacts.yml
      release.sboms: build      # release-orchestrator dot-prefix convention
```

```yaml
# Snapshot flow: turn on full SBOM generation for testing
jobs:
  release-snapshot:
    uses: diggsweden/forge-ci/.github/workflows/release-snapshot-orchestrator.yml@PIN_FORGE_CI_SHA # v0.90.0
    with:
      # Engine commit and image digests: see the Advanced Pinning Recipe.
      branch: ${{ github.ref_name }}
      artifacts-config: .forge-ci/artifacts.yml
      sboms: all                # release-snapshot-orchestrator uses flat names
```

> **Mandatory when selected:** a Build SBOM tool failure fails the workflow.
> See [Failure semantics](#failure-semantics).

## CISA SBOM types: what we produce

| CISA type | Layer name | Produced by | Accuracy |
|---|---|---|---|
| **Build** | `build` | Ecosystem-native CycloneDX tooling | Resolved dependency metadata; scope depends on ecosystem, target and features |
| Analyzed (artifact) | `analyzed-artifact` | Syft scanning the built JAR, tgz or binary after the build; not generated for Gradle Android APK/AAB | Good, inferred from artifact contents |
| Analyzed (container) | `analyzed-container` | Syft scanning the pushed container image in `publish-container.yml` | Good, inferred from image layers |
| Design / Deployed / Runtime | — | Not generated; require non-CI information (architecture, live infra, runtime agents) | n/a |

### Build-layer scope

Build-layer generation uses ecosystem-native CycloneDX tooling where available;
its dependency coverage depends on the tool, target and feature configuration.
Go uses `forge-ci sbom build --project-type go`, backed by `cyclonedx-gomod`,
for a module-level Build SBOM without compiling every platform. A Build-layer
document is not necessarily an inventory observed during compilation.

### Per-language Build tools

| Ecosystem | Tool | Workflow | SBOM artifact |
|---|---|---|---|
| Maven | `cyclonedx-maven-plugin` (`makeAggregateBom`) | `build-maven.yml` | `maven-build-sbom` |
| Gradle (JVM) | `cyclonedx-gradle-plugin` (init-script) | `build-gradle-app.yml` | `gradle-build-sbom` (or `<artifact-name>-build-sbom` when overridden) |
| Gradle (Android) | same as above | `build-gradle-android.yml` | per matrix-variant name |
| npm | `@cyclonedx/cyclonedx-npm` (via `npx`) | `build-npm.yml` | `npm-build-sbom` |
| Cargo | `cargo-cyclonedx` (`--all` includes transitive dependencies) | `build-cargo.yml` (artifact-first) or `sbom-cargo.yml` (container-first, publish stage; lockfile-derived) | `cargo-build-sbom` or `<artifact-name>-cargo-build-sbom` |
| Go | `forge-ci sbom build --project-type go` (`cyclonedx-gomod`) | `build-go.yml` (artifact-first) or `sbom-go.yml` (container-first) | `go-build-sbom` or `<artifact-name>-go-build-sbom` |

The build SBOM lives in its own upload artifact, separate from the code artifact (`<artifact-name>-build-artifacts`, `<artifact-name>-build-sbom`, etc.; direct single-workflow callers keep defaults such as `maven-build-artifacts`). When Build SBOM generation is enabled, both generation and upload are mandatory: a plugin failure or a missing expected file fails the job before publication. Tool versions are pinned and tracked by Renovate via `# renovate: datasource=...` comments.

### Cargo SBOM placement follows build-mode

Cargo is dual-mode (same as Go). Where the Build SBOM is emitted depends on `config.build-mode`:

- **`build-mode: artifact-first`**: `build-cargo.yml` cross-compiles the
  binary AND emits the Build SBOM inline (one `cargo cyclonedx` step against
  the lockfile). Matches the build-go.yml shape. The SBOM and the binaries
  upload as sibling artifacts from the same build job.
- **`build-mode: container-first`**: `sbom-cargo.yml` runs at publish stage
  alongside the container build. It deliberately does **not** invoke `cargo
  build` or `cargo test`, because compilation is owned by the
  Containerfile (`linux/amd64,linux/arm64` via native split-runner builds),
  with the lockfile-derived SBOM shipping next to the image.

Workspace-level `cargo test` lives in a caller-owned workflow either way, since
workspace features can't be expressed per-artifact; forge-ci's artifact-first
build runs `cargo test --locked --all-targets` as a release-build sanity gate
(under administrator-owned test enforcement; `skip-tests: true` is refused),
not as a substitute for the caller's PR tests.

### Cargo SBOM scope

Cargo SBOM generation reads Cargo metadata and the lockfile; it does not observe
each compiled binary or the Containerfile's build. The engine invokes
`cargo cyclonedx --all --format json --override-filename bom` without target or
feature overrides. The generator defaults to the host target and default
features. `--all` includes transitive dependencies; it does not request a union
of all platforms or enable every feature.

Consequently, a BOM can omit dependencies used by another target or include
ones excluded by the actual build's feature selection. A shared lockfile alone
does not establish build equivalence. When assessing coverage, compare the
generator's target/features with those used to produce the artifact or image.

The artifact-first and container-first split is described in
[Ecosystems](ecosystems.md).

## How it works internally

### Direct builder calls

Called directly, not from the release orchestrator, each builder workflow,
`sbom-cargo.yml` and `sbom-go.yml` included, defaults to
`enable-build-sbom: true`; pass `false` to skip the Build SBOM.

### Container scanning is derived, not separately gated

Container SBOM scanning (`analyzed-container` layer) is **derived** from source-artifact `sboms`. `publish-container.yml` runs syft on the pushed image if and only if any of the container's source artifacts (`from: [a, b, …]`) has `analyzed-container` in its effective sboms. With the default `sboms: all` on a buildable artifact, that's always true.

To omit the analyzed-container **SBOM**, set the source artifact's `sboms` to
exclude `analyzed-container` (e.g. `sboms: build,analyzed-artifact`). This does
not omit the Trivy vulnerability scan required for exported/published OCI
layouts, which always runs; an
[administrator setting `enforce-vulnerabilities: false`](admin-policy.md) still runs it and
reports findings, while operational and identity errors remain blocking.

Strict config parsing rejects `containers[].enable-sbom: bool`; use artifact
`sboms` instead. It rejects `containers[].enable-scan` too: the scan always runs.

> **Snapshots.** The snapshot flow publishes the declared containers by
> default (`publish-containers: false` opts out), unsigned, with tags ending
> in `-snapshot`. Scanning and image SBOMs follow each container's
> configuration. The snapshot `sboms` input (default `none`) selects the
> package SBOM layers (`build`, `analyzed-artifact`) apart from that.

### Source layer is not generated

Source SBOMs are not generated and are not part of the user-facing `sboms` enum.

### Aggregation in release flows

`forge-ci sbom assemble` consolidates per-stack SBOMs into release files under a single CISA-aligned naming pattern. It harvests the language-native build BOM and syft-scans built artifacts and containers, so it runs *after* the build. The short commit SHA is injected for traceability when run inside a git repo:

```text
<project>-<version>-<short-sha>-build-sbom.cyclonedx.json
<artifact-basename>-<short-sha>-analyzed-jar-sbom.{cyclonedx,spdx}.json          # Maven / Gradle (JVM)
<artifact-basename>-<short-sha>-analyzed-tararchive-sbom.{cyclonedx,spdx}.json   # npm
<artifact-basename>-<short-sha>-analyzed-binary-sbom.{cyclonedx,spdx}.json       # Go / Rust
<project>-<version>-<short-sha>-analyzed-container-sbom.{cyclonedx,spdx}.json
```

`<artifact-basename>` is derived from the actual scanned file (so multi-jar projects get unique names per jar). `build` SBOMs use `<project>-<version>` instead, because there is one Build SBOM per project, not per output file. Container scans have a single artifact-type so the analyzed-container layer omits the further modifier.

Release creation downloads the exact artifact names in the typed transfer plan
through `forge-ci release download-artifacts`. Build-BOM lookup is relative
to each artifact's working directory: it searches `release-artifacts/` when
present, otherwise that working directory.

The lookup patterns are ecosystem-specific. Maven expects `target/bom.json`;
npm excludes `node_modules/`, Cargo excludes `target/`, and Go excludes `dist/`.
Go first prefers a BOM under the artifact's named directory. Within the selected
patterns, the shallowest matching BOM wins over deeper per-module matches.

## Failure semantics

SBOM generation is mandatory by default. Each builder runs the cyclonedx plugin
without `continue-on-error`, so a tool failure (broken plugin, missing
lockfile, unreadable manifest) fails the workflow on the spot. This matches
the deterministic-pipeline contract: a passing pipeline implies a complete
set of the SBOM layers selected by policy.

To skip a layer, leave it out of the artifact's `sboms`, or pass
`enable-build-sbom: false` to a directly called builder, whose step summary
then records the Build SBOM as skipped. See [The `sboms` field](#the-sboms-field)
for what `release.sboms` does and does not control.

## Verifying a Build SBOM was produced

Each build workflow writes a one-line status to its GitHub step summary:

```text
### Build SBOM
- ✅ CycloneDX (aggregate): `server/target/bom.json`
```

To verify without clicking into the run, the SBOM appears as its own artifact (`maven-build-sbom`, etc.). Download and inspect:

```bash
gh run download <run-id> --name maven-build-sbom
jq '.metadata.component.name, (.components | length)' bom.json
```

## Delivery and verification

Release SBOMs have two delivery paths:

- The build and analyzed SBOMs selected by effective `release.sboms` policy are packaged in a signed
  `*-sboms.zip` GitHub Release asset. The release checksum manifest covers the
  ZIP and the other release assets.
- Each per-architecture container digest receives its analyzed-container
  CycloneDX document as a signed cosign attestation. This path is registry- and
  forge-neutral; it does not depend on GitHub's attestation API.

Verify release files in trust order: authenticate the checksum manifest, check
downloaded files against it, then authenticate the optional detached SBOM ZIP
signature:

```bash
gh release download "$TAG" -p 'checksums.sha256*' -p '*-sboms.zip*'
gpg --verify checksums.sha256.asc checksums.sha256
sha256sum -c checksums.sha256 --ignore-missing
gpg --verify PROJECT-1.0.0-sboms.zip.asc PROJECT-1.0.0-sboms.zip
```

For a container SBOM, choose the expected signer from trusted release policy
and follow the [verified-predicate recipe](verification.md#verify--inspect-the-sbom).
It verifies the exact platform digest and extracts the predicate only from the
successful verifier's retained output, without a separate unverified download.

See [Verification](verification.md#full-verification-guide) for the surrounding
artifact and provenance checks.

## Consuming SBOMs

Build SBOMs are harvested as CycloneDX JSON, preserving the producer's bytes.
Analyzed-artifact and analyzed-container layers are emitted as CycloneDX JSON
and SPDX JSON. Schema versions follow the generator and its configuration;
the assembler does not convert every document to a fixed version. Inspect
`specVersion` or `spdxVersion` when choosing a compatible consumer.

```bash
# Vulnerabilities, with a release-relevant severity floor
trivy sbom --severity HIGH,CRITICAL artifact.cyclonedx.json

# License findings
trivy sbom --scanners license artifact.cyclonedx.json

# Inspect SPDX package/license data directly
jq '.packages[] | {name, version: .versionInfo, license: .licenseConcluded}' \
  artifact.spdx.json

# Convert an existing SBOM to Syft's table view
syft convert artifact.spdx.json -o table
```

Build and Analyzed describe the intended collection scope using CISA terminology.
Actual content depends on the generator, configuration and available metadata.
The assembler preserves harvested Build documents after format and optional
subject/version checks; it does not assess NTIA minimum-element coverage or
regulatory compliance. Assess the emitted documents against the requirements
for the release and its intended use. Source, Design, Deployed and Runtime
SBOMs are not emitted. An SBOM supports CRA transparency work, but CRA update
and incident-reporting obligations remain outside CI.

Further references:

- [CISA SBOM guidance](https://www.cisa.gov/sbom)
- [CISA SBOM types](https://www.cisa.gov/resources-tools/resources/types-software-bill-materials-sbom)
- [CycloneDX specifications](https://cyclonedx.org/specification/overview/)
- [SPDX 2.3](https://spdx.github.io/spdx-spec/v2.3/)
- [Syft](https://github.com/anchore/syft)
- [Trivy](https://github.com/aquasecurity/trivy)
