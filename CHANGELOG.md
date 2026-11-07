# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Admit exact or namespace-qualified workflow dependencies with
  `platform dependency`: require one job, an admitted result and explicit output
  presence before downstream reusable calls consume producer facts.
- Fetch locked Cargo dependencies with bounded cancellation-aware retries,
  without invoking builds or tests. Extend existing unsigned file manifests
  with evidence without rediscovering GoReleaser metadata or replacing subjects.
- Admit base promotion ledgers and native layout-source bindings in existing
  image commands; stage and verify release handoff bytes in the release owner.
- Reuse release-request source admission before loading the prepare signing key.
- **BREAKING**: Maven `build maven run --working-dir` now selects the execution
  directory as well as metadata. It must stay workspace-relative; relative
  settings resolve from that project. The three forge adapters use this contract.
- Move GitLab snapshot child rendering, native export admission and pre-manifest
  export fan-in into Go owners; reuse shared image-builder admission for releases.
- Compare reproducible release subjects with caller-owned inventory patterns in
  Go, and reuse release checksum validation before GitHub self-release signing.
- Add optional exact-content artifact upload read-back verification and bounded,
  cancellation-aware retries to the existing Go dependency download command.
- Verify runtime downloaded/baked CLI bytes in Go without executing candidates.
- Move Forgejo image-builder setup selection and Kaniko job-image admission
  into Go, reusing existing Buildah storage setup and registry validation.
- Verify runtime smoke receipts in Go against the exact source, target, native
  architecture and candidate digest, refusing stale evidence before assembly.
- Add a reusable Forgejo unsigned GoReleaser packaging action and a callable,
  preview-only GHCR retention workflow. Keep product packaging and protected
  consumer pins in caller-owned configuration rather than copied workflows.
- Add read-only `doctor --requirements` setup guidance for GitHub, GitLab and
  Forgejo release entry points, with caller mappings and explicit coverage limits.

### Fixed

- Protect the union of consumer digests per GHCR package in retention previews,
  and reuse Go runtime admission for build/assembly target and metadata selection.
- Classify redacted HTTP transport failures without exposing private causes.
  Report Forgejo artifact finalization timing and uncertain server outcomes,
  with safe debug progress logs and no automatic mutation retries.
- Run GitHub self-release binary publication after a successful release even
  when optional ancestor gates are skipped; retain failure/cancellation refusal.
- Explicitly sign GitHub CLI checksum manifests after unsigned packaging and
  before artifact/release uploads, preserving stable-release rebuild verification.

## [0.93.1] - 2026-11-06

### Added

- Support Linux and macOS hosts

### Changed

- Pin reviewed private-input black-box coverage
- Refresh reviewed binary checksums
- Update forge-lab-contract to v0.93.0
- Restore clean lint baseline

### Fixed

- Authenticate private base verification
- Isolate private inputs from release builds
- Exercise disposable consumer snapshot workflows
- Probe backends without rendering notes
- Reconcile Darwin portability with main
- Resolve static checks and extend image readiness wait
- Stabilize GPG agent cleanup

## [0.93.0] - 2026-11-05

### Added

- Support local runners on either lab road
- Support validated release image labels
- Add a first pull-request tutorial and a docs index signpost
- Add enforce-reproducible-builds admin setting
- Add service, npm and generic CLI acceptance trials

### Changed

- Pin verified black-box regressions
- Refresh binary checksums
- Neutralize examples and project references
- Use neutral repository-owned examples
- Refresh final source checksums
- Default all check enforcement to report-only
- State required-workflow deployment prerequisites
- Build images in an isolated Kaniko action
- Describe both supported local lab roads
- Describe delivery parity boundaries
- Run deadcode in the pull-request gate
- Forward administrator settings to every gate
- Accept explicit administrator enforcement flags
- Check dead code across every build tag
- Require trusted pipeline-variable permissions
- Reject frozen OpenPGP imports
- Describe unsupported nanolinter checks
- Run Go and Gradle example workflows on GitHub
- Cover Cargo, npm and Android on local forges
- Export release files with the selected builder
- Consolidate trial guidance and remove old reviews
- Detect GitLab job-name and input conflicts
- Report unsupported commit and SAST checks
- Isolate image builds in the builder job image
- Run as nonroot and provide a Buildah job image
- Use consistent neutral forge and registry coordinates
- Build release images from native job plans
- Use neutral consumer names and coordinates
- Prepare campaigns and run declared provider lanes
- Block on CRITICAL and scan every published image
- Accept CVE-2026-60002 until Debian 13 ships a fix
- Build Go tools with a digest-pinned Go
- Update runtime image pins and let Renovate manage them
- Build a candidate runtime image for GitLab trials
- Share OCI layout blobs through a run blob store
- One name for the consumer's read-only tool token
- Trim the orchestrator header and state the dotted-input rule
- Run tests in the GitHub starters and explain releases
- Make a bare forge-ci a usage error with a start card
- Embed administrator enforcement settings
- Run consumer checks on their own runner tags
- Bridge explicit CA trust into the selected JVM
- Publish source-bound snapshot images on every forge
- Record release-policy and signed prereleases
- Use the current released CLI in installation examples
- Restore a green lint baseline
- Temporarily pause self-release integration gates
- Document release migration and upstream contribution boundaries
- Share native smoke and publication evidence
- Replace image actions with native release workflow
- Bind release policy and build runner tags
- Isolate release preparation and signed handoffs
- Unify command inputs and build validation
- Protect consumer pins and preview cleanup
- Stage native images with verified index evidence
- Enforce cross-forge prerelease boundaries
- Request ephemeral native runners
- Preview forge-specific consumer setup
- Adopt shared lint and validation foundations

### Fixed

- Restore tagged checks and confined evidence handling
- Constrain Maven Resolver 2 transport
- Share bounded download retries and backoff
- Use search-only Linux ancestor handles
- Verify declared GitLab release evidence
- Pin the Android demo build bootstrap
- Honour private Maven library selection
- Report source-image phases accurately
- Qualify retained Go source-image fixtures
- Use Java 25 for Gradle pull requests
- Retain the logger through group interruption
- Validate recovery traces against receipt policy
- Scope release signing to the selected event
- Require a separate GitHub release credential
- Checkpoint recovery after policy publication
- Pin minimal bootstrap fixture references
- Honour hosted API visibility selection
- Authenticate private GitLab asset reads
- Align Rust pins with the prepared runtime
- Refresh command lookup after Kaniko cleanup
- Infer checkout format without an API request
- Pin source-gate merge controls
- Admit Forgejo development builder bindings
- Share bounded standard-base64 key decoding
- Expose explicit GitLab scenario inputs
- Share structural rendering across consumers
- Bind policy in controller-owned callers
- Separate upload idle and API request budgets
- Admit settings and builders before execution
- Share Git-based source inventory across guards
- Respect independent checkout boundaries
- Declare Android campaign capabilities
- Reduce remote cache compression memory
- Retain full content snapshots
- Protect provenance labels in resolved plans
- Bind snapshot registry reads to policy
- Honour private GitLab campaign visibility
- Preserve commas in repeated key-value arguments
- Constrain verification and builder environments
- Include the newer demos in private lanes
- Select exports only through release platforms
- Bind instance and registry to literal inputs
- Require an explicit nanolinter image digest
- Verify default release image declarations
- Separate npm and Android package names
- Quote the engine-layer version
- Preserve literal settings and explain their source
- Honour provider visibility and builder selection
- Verify the native runner group
- Retain provider diagnostics through slow execution
- Record the scanned platform in image evidence
- Validate artifact paths and runtime digest pins
- Require explicit transfer arguments
- Validate declared versions and component values
- Keep nonroot tool installs and caches usable
- Isolate SBOM generation and scan platform inputs
- Resolve release-owned component images
- Reject unread input environment bindings
- Validate native job users and groups
- Preserve contract diagnostics and archive scope
- Bind gate policy to literal component inputs
- Supply pinned images to private GitLab lanes
- Normalise self-workflow image paths
- Require an explicit serving repository
- Validate every layout before release publication
- Isolate scans from job and checkout settings
- Validate snapshot hand-off ownership
- Authenticate Kaniko job-image reads
- Replace personal owners with neutral examples
- Reject unused workflow inputs
- Lowercase owner-derived image repositories
- Preserve failures across guards and pipelines
- Reuse tools only at their pinned versions
- Bootstrap from the repository serving the workflow
- Refuse ambiguous mise host-tool resolution
- Resolve pinned host tools beyond mise shims
- Reject host shims without installed tools
- Run private npm and Maven development trials
- Honour Containerfile-specific ignore files
- Classify runner failures and record a verdict per run
- Classify test output while it streams
- Name the image workflow jobs by stage
- Show the gradle-app and monorepo examples as demos
- Show the go-cli and go-service examples as demos
- Survive forge log, dispatch and cancel failures
- Trial signed releases, file secrets and GitLab builds
- Write the changelog configuration Forgejo and GitLab read
- Build one Go main package and name cargo needs
- Build the Cargo workspace member at working-directory
- Say the lint action fetches its pinned engine
- Name a Containerfile's own ignore file explicitly
- Build actions bring their pinned Build SBOM generator
- Give downloaded files the job's ordinary mode
- Say what a refused artifact upload of 413 means
- Bound every file transfer by progress, not wall time
- Find the Gradle JAR a release job downloaded
- Apply build-npm and build-cargo working-directory once
- Name the SSH signing principal with placeholders
- Trust the checkout in both lint components alike
- Name the registry user by one rule in every login
- Link guides by URL from job output, examples and the schema
- End reports and errors with the next step
- Plan only the platforms a GitHub run builds
- Isolate provider lanes and retain consumer evidence
- Validate declared library entrypoints
- Refuse test opt-outs and never waive crashes
- Keep compilers out of the signer and ship ssh-keygen
- Preserve npm JSON formatting in bump patches
- Give published files canonical modes under any umask
- Verify library releases and development consumption
- Keep the complete library producer lifecycle unsigned
- Scope self-runtime caches and images to the publisher
- Make consumer PR trials portable and evidence-bound
- Name the snapshot jobs by stage
- Use fictional identities in portable consumer fixtures
- Keep Go telemetry out of the hermetic HOME
- Install pinned Just before Go test recipes
- Pin compatible suite and rebuild the PR candidate
- Align architecture and bootstrap regression guards
- Adopt v5 contracts and exact workflow cleanup
- Preserve multiline index evidence outputs
- Isolate JSON output from diagnostics
- Retry transient artifact reads safely
- Inspect GitLab OIDC declarations
- Harden private checkout and pinned SSH transport

### Removed

- Remove the unused warned outcome
- Remove ineffective scan and SwiftLint controls
- Remove unused publication and scorecard inputs
- Remove the superseded consumer migration handoff
- Remove ignored workflow permission declarations

## [0.91.13] - 2026-10-23

### Added

- Add checked image indexes and index SBOMs
- Add release signing secret setup guide

### Fixed

- Keep multiarch output and selection coherent

## [0.91.10] - 2026-10-22

### Fixed

- Install goreleaser regression

## [0.91.9] - 2026-10-22

### Added

- Support explicitly registered fork self-CI

### Changed

- Refresh signed companion suite pin
- Pin the fork-portable companion suite

### Fixed

- Tighten companion checkout validation and refresh its pin
- Verify renamed forks and document migration
- Reject subsets that omit the selected Go runtime
- Accept full SHA-256 workflow pins

## [0.91.8] - 2026-10-22

### Added

- Add gpg-keys format check for coherent allowlist validation
- Add allowed-signers format check as CLI subcommand
- Add missing principal to allowed_signers entry
- Add RequireWorkingTool and use it for cargo
- Add maintainer release runbook to DEVELOPMENT.md
- Add diggadmin to allowed releasers

### Changed

- Use forge-neutral delivery paths
- Gate tests on fresh binary checksums
- Use library parsers instead of hand-rolled validation
- Validate allowed_signers entry format
- Replace forward-looking notice with actionable quick start

### Fixed

- Align allowlist checks with key parsers
- Fix stale gitleaks allowlist comment
- Bump blackbox test suite pin to 9fd37b4
- Verify cargo actually runs, not just exists on PATH
- Bump blackbox test suite pin to 792a045
- Install rust via apt instead of rustup on ubuntu-26.04
- Install rust toolchain explicitly before setting default
- Bump blackbox test suite pin to 31e7e72
- Set rustup default toolchain on ubuntu-26.04 runners
- Defer runtime image check until after test gates
- Skip CLI signing on tag pushes in self-runtime-container

### Removed

- Remove rustup proxy shims after apt cargo install

## [0.91.0] - 2026-10-22

### Changed

- Initial commit

[0.93.1]: https://github.com/diggsweden/forge-ci/compare/v0.93.0..v0.93.1
[0.93.0]: https://github.com/diggsweden/forge-ci/compare/v0.91.13..v0.93.0
[0.91.13]: https://github.com/diggsweden/forge-ci/compare/v0.91.10..v0.91.13
[0.91.10]: https://github.com/diggsweden/forge-ci/compare/v0.91.9..v0.91.10
[0.91.9]: https://github.com/diggsweden/forge-ci/compare/v0.91.8..v0.91.9
[0.91.8]: https://github.com/diggsweden/forge-ci/compare/v0.91.0..v0.91.8
[0.91.0]: https://github.com/diggsweden/forge-ci/compare/v0.90.0..v0.91.0

<!-- generated by git-cliff -->
