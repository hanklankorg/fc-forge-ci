# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Removed

- **BREAKING:** Replace `release-type` and `derive-release --require-stable`
  with `release-policy: stable|stable-prerelease`, without compatibility aliases.
  Keep workflow, binary and runtime-image versions aligned; select prerelease
  admission in trusted configuration.
- Remove ignored GitHub-style permission declarations from Forgejo workflows,
  examples and live consumers. Automatic-token rights, explicit credentials and
  OIDC behavior are unchanged; the Forgejo guide states their actual boundaries.
- **BREAKING:** Remove the `build-release-image` composite action after migrating
  the Maven, npm and Cargo demos and proving their Forgejo release hand-offs.
  Use `release-images.yml` for native image builds and join its evidence with
  the unsigned file artifact before signing. The direct container CLI remains.
- **BREAKING:** Remove the `build-push-oci-image` composite action. Its remaining
  registry-smoke fixture calls the existing `forge-ci container build-push-oci-image`
  command directly, preserving direct final-tag publication and image/digest
  outputs. Release consumers should use `release-images.yml`.

### Added

- Administrator policy setting `enforce-reproducible-builds` (strict
  reproducible builds, default `true`). With `false`, missing Maven
  `project.build.outputTimestamp` or Gradle reproducible-archive settings are
  reported with their fix and policy decision without stopping the release.
  Unreadable or malformed build files still block. See `docs/admin-policy.md`.
- Signed numbered prereleases use the existing release pipeline with
  `release-policy: stable-prerelease`, including release evidence and retry
  behavior. Prereleases do not move stable image aliases or promotion stages;
  GitLab identifies them in release notes because it has no native prerelease flag.
- Declared in-progress snapshot containers publish by default on GitHub, Forgejo
  and GitLab; `publish-containers: false` explicitly opts out. Shared planning,
  native architecture staging, scan/SBOM policy and exact digest receipt collection
  use a frozen source SHA. Container tags always include the original branch hash
  and end in lower-case `-snapshot`; package naming is unchanged. GitHub integrates
  after artifact builds and reuses their exact uploads. Standalone GitHub and
  native Forgejo/GitLab automatically build required Go, Cargo, Maven, Gradle JVM
  and npm artifact-first producers; prepared native contexts are optional advanced
  overrides. Shared Go commands validate producer sets and safely restore confined
  inputs with source-bound digests and canonical `0644`/`0755` string modes across
  ZIP transport. Npm tarballs use isolated per-artifact context destinations.
  Snapshot OCI signing/provenance, build secrets and extraction remain unsupported.
  Minimal three-forge snapshot acceptance is pending operator execution.
- Package retention accepts repeatable `--keep-digest` roots, preserving old
  consumer pins and their platform/signature closure. A missing pin refuses the
  pass. A weekly/manual opt-in example previews a caller's reviewed runtime-image
  policy without deleting packages.
- GitHub's standalone and multi-artifact version preparation run native project
  tools in a separate key-free job. Signing reuses version-only patch validation;
  `version bump-plan --patch --patch-sha256` applies those prepared changes without
  constructing native-tool adapters.
- GitLab `release` takes `build-runner-tags`, the runner tags of its build job
  alone, so an image build can run where user namespaces are allowed while the
  signing jobs stay on untagged runners. Empty, the default, changes nothing.
- Forgejo artifact reads retry HTTP 500 with the shared bounded backoff policy.
  Recovery preserves evidence verification; uploads and finalization remain
  single-attempt, and exhausted reads retain artifact/file diagnostics.
- Runtime evidence uploads are downloaded and checked against the original
  file inventory before assembly succeeds. Forgejo download errors identify
  the artifact and failing entry without exposing signed URLs or response bodies.
- Successful native smoke jobs record their exact candidate digest. Assembly
  requires matching source, flavour, architecture and digest receipts before
  scanning or publishing an index, rejecting stale smoke results after rebuilds.
- Runtime publication supports private source repositories and private image
  packages. Source operations use the forge job token; final verification
  authenticates to the registry and checks the promoted tag's exact digest.
- A configurable Forgejo publication example for the complete runtime family
  to a reviewed GHCR namespace. Reuse platform planning, native runner routing/lending and
  the Buildah/kaniko selector; smoke-test exact candidate digests before
  assembling, signing and promoting source-commit tags. Keep signing keys
  out of source builds and candidate execution. Preserve per-platform SBOMs,
  advisory vulnerability reports and signed SLSA provenance.
  Both runtime publishers default to amd64 and arm64; `RELEASE_PLATFORMS`
  can narrow that selection. Android remains amd64-only.
- Shared runtime smoke checks for the GitHub and Forgejo publishers, with
  tests keeping their flavour inventories aligned. Document the example's
  publication boundaries and evidence needed for future forge migrations.
- The Go runtime image supplies GCC, libc development headers and gettext
  for CGO, race tests and translation checks. Its Go toolchain and the live
  Go consumer fixtures use 1.27.3; runtime smoke checks exercise the race
  detector and require all four gettext catalog tools.
- `container release-images plan` without an `artifacts.yml` releases the
  repository's own image from its root `Containerfile`, so the simple case
  needs no declaration wherever a layer calls the plan (Forgejo's
  `release-images.yml` today). An `artifacts.yml` may now declare only
  `containers:`, for a repository that builds its binaries itself.
- `release-images.yml`, a Forgejo reusable workflow that builds the release
  images `artifacts.yml` declares with the organization's image builder,
  Buildah or kaniko: one native build per container and architecture to an
  OCI layout, without registry writes or secrets, then a publish job that
  scans every architecture, uploads only when all pass, and records the
  index under its staging tag for the signer. Each image carries the
  standard labels `build-release-image` set: source, revision, title,
  version, url, documentation, ref.name and created.
  `docs/forgejo.md#release-images`.
- `container release-images plan` turns the declared containers into that
  workflow's build and publish matrices, and refuses what it does not carry
  out before any build. A release publishes to one repository, the one the
  signer is bound to: `--image-repository` (the workflow's
  `image-repository` input), or the forge's own registry when empty.
  Containers are tag variants there, told apart by `tag-suffix`; the plan
  refuses an `image-name`.
- `multiarch-image assemble --from-layouts --scan-output-template` scans every
  staged architecture with the container publication gate before the first
  upload.
- Container `image-name` and `tag-suffix` in `artifacts.yml`, so two
  containers can publish tag variants of one repository.
- `setup-image-builder` prepares whichever image builder `CI_IMAGE_BUILDER`
  selects, Buildah's storage or the digest-pinned kaniko job image, so an
  image-build job names no builder. The signer example uses it.
- `multiarch-image build-arch` takes `--target`, `--build-arg` and `--label`,
  with both builders. With `--stage` it needs no `--auth-file`: a staged
  build writes no registry, and without one it pulls its bases anonymously,
  never with a login the runner keeps.
- Runner lending: `forge-ci platform request-runner` asks a lender for one
  ephemeral runner before a job that builds container images, for a runner
  pool that denies the user namespaces rootless Buildah needs. The GitHub
  lender dispatches `examples/runner-lender/github-actions/lend-runner.yml`,
  which lends a GitHub-hosted VM to the Forgejo instance for exactly one job
  through `scripts/runner-lender/lend-forgejo-runner.sh`; `none` is the
  installation whose container runners exist already. `docs/runners.md`.
- `setup-buildah` names the runner, the variable that moves the job, and
  `CI_IMAGE_BUILDER=kaniko`, when Buildah is refused a user namespace.
- kaniko as a second image builder, `--image-builder kaniko`, for `container
  build` and `multiarch-image build-arch` on a pool that denies user
  namespaces; the Forgejo examples select it by the builder type
  `CI_IMAGE_BUILDER` (`buildah` when unset) and run it in the job image
  `CI_IMAGE_BUILDER_JOB_IMAGE`. It builds to an OCI
  layout for the same digest push, scan and ledger; load and local modes and
  build secrets are refused by name. `containers/kaniko-job/Containerfile` is
  the job image such jobs run in, and
  `examples/forgejo/kaniko-job-image.yml` its bootstrap, run by dispatch
  once the pin's engine binary is published. `docs/runners.md`.

### Fixed

- Bootstrap source installs build a detached Git checkout instead of resolving
  a commit through a cached moving Go module tag. Host jobs bind the binary to
  their bootstrap checkout, support full SHA-1/SHA-256 IDs and relative install
  directories, and forward the selected revision to Swift lint jobs.
- GitHub binary defaults resolve one concrete published stable release tag,
  with matching default runtime-image references and preserved explicit
  overrides. Self Release continues to use its exact request commit.
- Pin PR and release compatibility gates to the upstream companion suite's
  current release-policy, SSH handshake and registry-authority tests.
- PR black-box CI explicitly rebuilds the exact source checkout and passes that
  binary to the companion suite. Go's race-test budget accommodates cold native
  engine rebuilds on two-core runners while retaining bounded fixture timeouts.
- Reject per-platform inventory filename collisions before scans or registry
  writes, including inventories in different local directories that would share
  one public release URL.
- The opt-in package-retention example uses its pinned published engine directly,
  so it can preview a consumer's policy without Forge CI source or a Go compiler.
- Prepared version-bump plans retain changelog destination admission. A symlinked
  or nonregular changelog now refuses before either the native version patch or
  any changelog write, rather than losing that refusal when native tools are skipped.
- The go-service Forgejo example uses `release-images.yml` with a separate
  non-extracting declaration and prebuilt versioned binaries. Native image
  builds run without registry tokens and join their ledger before signing.
  Its toolchain install uses committed `mise.lock` inputs instead of requiring
  a GitHub token.
- `platform checkout --sparse` checks out a private repository. It made a
  partial clone, whose deferred blob fetch during the checkout carried no
  credential and failed with "could not read Username"; `release-images.yml`
  could not publish a private repository's images.
- The Forgejo tag deleter refuses an image that is not on the server's own
  registry. The package API knows packages by owner and name alone, so a
  GHCR ref deleted through it removed a same-named Forgejo package's tag.
- `setup-image-builder` admits the kaniko job image when the layer's
  repository arrives as a validated clone URL ending in `.git`.
- Emit index SBOM evidence paths as a multiline CI output.
- The Forgejo signer-image example fetches the engine without credentials.
  `actions/checkout` sent the policy repository's job token, which Forgejo
  refused for the engine repository with 403.
- A release host-key pin that is not the `SHA256:<base64>` fingerprint
  alone, such as one copied with its host and key type, is refused by name.
  It was reported as a fingerprint mismatch naming the same fingerprint.
  The same fingerprint with its `=` base64 padding is accepted.
- An SSH signing key pasted into a forge secret with CRLF line ends is
  accepted; OpenSSH refused it as "error in libcrypto". A key that still
  fails is refused before anything is written, naming the expected format
  or that it is passphrase-protected.
- `version render-changelog` refuses git-cliff on a SHA-256 repository,
  naming git-chglog, instead of failing inside git-cliff with "unknown object
  format 'sha256'". It also refuses a changelog or commit-body config that
  does not exist; git-cliff rendered its built-in config instead.
- A failing `buildah info` reports Buildah's own message. `container
  setup-buildah` once failed a hosted runner with only "exit status 1",
  leaving the storage refusal unexplained.

### Changed

- **BREAKING:** Shared build gates refuse legacy `skip-tests: true` and
  `SKIP_TESTS=true`; exported/published OCI layouts are vulnerability-scanned
  even with consumer `enable-scan: false`. Keep scanner prerequisites available.
  Migrate finding exceptions to a reviewed administrator-built engine policy
  and matching protected workflow, binary and image pins. The embedded
  `enforce-lint`, `enforce-vulnerabilities` and `enforce-application-tests`
  settings default to `true`. Report-only distributions still execute checks
  and retain findings; operational, compilation/packaging, credential, signature
  and source/digest identity errors remain blocking. SwiftLint's legacy
  `--fail-on-warning` flag is compatibility-only, not enforcement authority.
  Gradle invokes tests separately before packaging, preserving Android's
  module-bound test aggregate even with task overrides; cache and `NO-SOURCE`
  outcomes do not prove physical test execution. See `docs/admin-policy.md`.
- Temporarily pause Self Release's strict integration and companion black-box
  jobs while Nanolinter publication is pending. Branch resolution, engine
  checksum verification and runtime-image availability remain required. Set
  `SELF_RELEASE_INTEGRATION_GATES=true` in repository variables to restore both
  jobs and their mandatory release checks after bootstrap.
- **Breaking:** command environment inputs use `REGISTRY_USER`,
  `RELEASE_IMAGES_PATH`, `CHANGELOG_FILE` and `TOOLSET_TOOLS` consistently.
  Removed the `REGISTRY_USERNAME`, `RELEASE_IMAGES_LEDGER`, `CHANGELOG_PATH`,
  `TOOLS_SUBSET` and `MISE_TOOLS` spellings. Workflow callers move with the
  engine; explicit command flags are unchanged.
- The release push and `release ssh setup` read the signing key, and the
  push the server's host key, with `golang.org/x/crypto/ssh` instead of
  running `ssh-keygen -y`, `ssh-keyscan` and `ssh-keygen -l`;
  `known_hosts` is written from the handshake's key.
- `release-git-url` (Forgejo and GitLab) and `commit-changelog-release
  --git-url` are optional: unset, the release pushes to the repository on the
  forge's web host over SSH, port 22. Set it where SSH runs elsewhere. The
  host-key fingerprint stays required, and the engine no longer suggests
  fetching it from the host it verifies.
- **BREAKING** The engine tells another image registry, such as GHCR, from
  the forge's own by the image repository's host, so `--external-registry`
  and `EXTERNAL_IMAGE_REGISTRY` are gone from the `container release-images`
  and `container base-images` verbs; drop them. On those verbs `--registry`
  now names only the forge's own registry when it is not on the server's
  host (GitLab's `CI_REGISTRY`), and the login goes to the repository's host:
  a caller that passed `--registry` for another registry drops it too, or
  that registry counts as the forge's.
- **BREAKING** The Forgejo `sign-and-publish-release.yml` input
  `publication-image-repository` is `image-repository`, the value
  `release-images.yml` takes under the same name. Rename it in the caller.
- The Forgejo image workflows read the registry user from the variable
  `RELEASE_REGISTRY_USER` when `registry-user` is empty, so the registry
  credential is one named pair, the secret `RELEASE_REGISTRY_TOKEN` and that
  variable, and a caller passes no user.
- **BREAKING** An empty `registry-user`, with no `RELEASE_REGISTRY_USER`, is
  the repository owner only on the forge's own registry. On another registry the Forgejo image workflows stop
  before they log in, and `release-images.yml` before it builds, until
  `registry-user` names the user there; they no longer try the forge owner.
  `container release-images plan` writes the decided user as its
  `registry-user` output.
- Base-image `sign`, `import` and `promote` no longer require the repository
  below the source repository's owner: the registry token already decides
  where they can write. Every image must still be in the one base-image
  repository.
- **BREAKING** The Forgejo signer-image example reads the engine package
  token as `ENGINE_PACKAGE_TOKEN`, no longer `RELEASE_REGISTRY_TOKEN`, which
  every consumer uses for its image registry. Rename the secret where the
  example is deployed.
- Forgejo jobs that build container images may be moved off the pool:
  `CI_RUNNER_LABEL_CONTAINER` and `CI_RUNNER_LABEL_CONTAINER_ARM64` name
  where they run instead, and a `request-runner` job asks the lender
  `CI_RUNNER_LENDER` and `CI_RUNNER_LENDER_REPOSITORY` name for an ephemeral
  one. All unset, the builds run on the pool and nothing is borrowed. The
  signer-image and go-service examples carry the shape.
- **BREAKING** Configure Forgejo `CI_RUNNER_LABEL`, `CI_RUNNER_LABEL_ARM64`
  and reusable `runner-label` inputs as JSON label arrays, including a single
  label (`["ci-builder"]`). Workflows decode them only at `runs-on`.
- **BREAKING** The GitHub SLSA attestor's configurable `runs-on` input also takes
  a JSON label array; its default remains the same Ubuntu runner. GitLab runner
  tags continue to use native YAML arrays.
- Keep signer architecture candidates in the image registry and transfer only
  digest metadata through Forgejo artifacts before assembling the final index.
- Resolve the external Nanolinter and Gommitlint development tools through their
  Codefloe release API, using published Gommitlint 0.93.0.

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

[0.91.13]: https://github.com/diggsweden/forge-ci/compare/v0.91.10..v0.91.13
[0.91.10]: https://github.com/diggsweden/forge-ci/compare/v0.91.9..v0.91.10
[0.91.9]: https://github.com/diggsweden/forge-ci/compare/v0.91.8..v0.91.9
[0.91.8]: https://github.com/diggsweden/forge-ci/compare/v0.91.0..v0.91.8
[0.91.0]: https://github.com/diggsweden/forge-ci/compare/v0.90.0..v0.91.0

<!-- generated by git-cliff -->
