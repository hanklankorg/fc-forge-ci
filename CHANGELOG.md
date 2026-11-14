# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The GitHub release and snapshot orchestrators take `xcode-version`, the
  Xcode version of their Xcode builds, from a caller that keeps it outside
  the repository, such as `${{ vars.XCODE_VERSION }}`. A project that moves
  to each new Xcode changes the variable and commits nothing. Empty uses
  `config.xcode-version` of the declaration, as before.
- The GitHub release orchestrator takes `xcode-build-number`, the build
  number of the release's Xcode builds, from a caller that computes one for
  each release. `config.build-number` of the declaration is a fixed value,
  and App Store Connect refuses a build number it has already received.
- A GitHub release of an Android application attaches the release APK
  beside the AAB, signed and listed in the checksums like the other release
  files, so the application can be installed from the release page. A
  library and a build without the `release` build type attach none.
- The GitLab release signs its checksums and SBOMs with GPG when
  `artifacts.yml` says `sign.method: gpg` or `sign.files-method: gpg`, as
  the Forgejo release does. `Sign: files and images` signs them with
  `RELEASE_GPG_PRIVATE_KEY` and `RELEASE_GPG_PASSPHRASE`, refuses a key
  whose fingerprint is not `RELEASE_GPG_FINGERPRINT`, and writes an armored
  `.asc` beside each file, dated at the release commit's time, in place of
  the Cosign bundle. The provenance statement, the evidence and the images
  are signed with the Cosign key as before, and so are the files of any
  other declaration. `Check: signing access` requires the three GPG
  variables for such a declaration before the release commit, also with
  `sign-distro-packages: false`; `doctor --requirements` reports them as
  required, and the release plan and `doctor` no longer warn about either
  `gpg` setting on GitLab. The live lane `gpg-files-gitlab` releases
  `hello-go-cli` this way. See `docs/gitlab.md#signing`.
- The Forgejo release signs its checksums and SBOMs with GPG when
  `artifacts.yml` says `sign.method: gpg` or `sign.files-method: gpg`. A
  step of its own in the sign job of `sign-and-publish-release.yml` holds
  only `RELEASE_GPG_PRIVATE_KEY`, `RELEASE_GPG_PASSPHRASE` and
  `RELEASE_GPG_FINGERPRINT`, refuses a key with another fingerprint, and
  writes an armored `.asc` beside each file, dated at the release commit's
  time as on GitHub, which the release page offers in place of the Cosign
  bundle. The provenance statement, the evidence and the images are signed
  with the Cosign key as before. A declaration without a `sign` section, or
  with another method, is signed as before, with the Cosign key.
  `prepare-release.yml` refuses a `gpg` release whose GPG secrets are not
  configured before it makes the release commit, and `doctor --requirements`
  reports them as required for such a declaration. The new command
  `forge-ci plan release-file-signing` says which key signs, and
  `release sign --method gpg` takes `--fingerprint`. The live lane
  `gpg-files-forgejo` releases `hello-go-service` this way. See
  `docs/forgejo.md#signing`.
- The live launcher runs a table row again, once, when the row failed on a
  host its jobs could not reach and on nothing else. The conclusion of the
  forge run must be the test's only failure, which the harness marks with
  `FORGE_CI_LIVE_CONCLUSION`, and the engine must have ended every failed
  job of it on `Error: …: dependency unavailable` with transport evidence of
  the host and exit status 69, in a log the harness printed under that
  job's name. A time limit, a limited rate, a server that answered with an
  error, another failure beside it and a failure with no cause are not run
  again, and neither is a rerun. The rerun runs after the run's other rows, in the same invocation
  and locks, and is the row's result. The log marks it with
  `FORGE_CI_LIVE_RERUN`, `verdict.json` lists the row under `reruns` with
  both attempts and marks the rerun's tests `"attempt": 2`, the
  `Live result:` line says a pass needed a rerun, and the rerun's job logs
  are kept in `job-logs/rerun`. `test/live/evidence.txt` marks such a pass
  `✅+rerun`, which the testing matrices show as "passed on rerun after an
  unavailable dependency". See `docs/live-runs.md`.
- The Forgejo release publishes distro packages to a registry. A top-level
  `distro-packages` block in `artifacts.yml` sends every `.deb`, `.rpm` and
  `.apk` file of the release to the Debian, RPM and Alpine registries of
  the repository's owner, so a user installs them with `apt`, `dnf` or
  `apk`. The block is the opt-in and holds only the places, so
  `distro-packages: {}` is enough. The places default to
  distribution `stable` and component `main`, group `stable`, and branch
  `stable` and repository `main`. The new job `Publish: packages` of
  `sign-and-publish-release.yml` runs after `Publish: release`, with
  `RELEASE_REGISTRY_TOKEN` and no consumer command, and publishes each file
  of the signed hand-off unchanged with
  `forge-ci publish forge-packages distro-packages`. The registry is read
  back for the file's SHA-256: a rerun leaves a published package as it is,
  the same package version with other content fails, and nothing is
  replaced or deleted. The GitHub and GitLab releases refuse the block when
  they are planned, since neither forge has registries that fit. What would
  stop the packages is found before the release is public: the prepare
  workflow checks the token for a declaration with the block, and the `sign`
  job admits the packages before it signs. Callers of
  `sign-and-publish-release.yml` see a fifth job, which is skipped in a
  release without the block.
- The GoReleaser alternative of the `go-cli` example builds distro
  packages. `examples/go-cli/.goreleaser.yml` packages the command as
  `.deb`, `.rpm` and `.apk` with `nfpms`, the Forgejo workflow
  `forgejo/release-goreleaser.yml` now passes `sign-distro-packages: 'true'`,
  and with `artifacts-forgejo-packages.yml` as the declaration the release
  publishes them to the owner's registries. The live suite has a Forgejo
  row for it, `distro-packages/hello-go-cli`, which also verifies the GPG
  signature of each published package. The row is written and not yet run
  on a forge; an install with `apt`, `dnf` or `apk` is not tested.
- A snapshot can keep the built files of a project. `publish-files: true`
  on `snapshot-images` (Forgejo and GitHub workflow, GitLab component, and
  the GitHub snapshot orchestrator) builds every declared Go, Cargo, Maven,
  Gradle and npm artifact with the snapshot version and keeps the files
  with a receipt, `snapshot-files.json`, as an unsigned artifact of the run,
  named `<artifact>-<version>-snapshot-<branch>-<commit>` (on Forgejo and
  GitHub after `<artifact-name>-files-`) and kept for 7 days for readers of
  the run. The job runs the project's build without a registry, release or
  signing secret and writes nothing outside the run. The input is `false`
  by default. A declaration with artifacts and no container needs no image
  repository: `image-repository` is optional on Forgejo and GitLab, and
  `registry-user` on Forgejo, both still required when images are
  published and refused in the plan step when missing. See docs/workflows.md, Snapshot files.
- A release is read back after it is published. `release validate-published`
  lists the release on the forge and compares its files with the planned
  ones: the files `release publish` uploads from the same `--manifest`,
  `--assembly` or `--asset` input, signatures and the provenance statement
  included. A planned file that is missing, or has another size where the
  forge reports one, fails the run and is named; a file the plan does not
  name is a warning. The `Publish: release` job of each forge runs it as its
  last step with the run's own token, not `RELEASE_TOKEN`, and reads only
  that one release. A draft is hidden from that token, so a draft release,
  on GitHub every release with `release.draft: true`, is reported as not
  read back and nothing is compared.
- A release can sign its files with GPG and its images with Cosign.
  `sign.files-method: gpg` in `artifacts.yml`, beside `sign.method`
  `sigstore` or `kms`, gives the release files `.asc` signatures and writes
  nothing about them to the transparency log, while the images keep their
  Cosign signatures and SLSA provenance. Without the field `sign.method`
  signs both, as before. On GitLab and Forgejo, `sign.files-method: gpg`
  gives the files GPG signatures as `sign.method: gpg` does, and the
  images keep their Cosign key.
- The GitLab and Forgejo release plans warn about signing they do not
  use: a `sign.method` or `sign.files-method` other than `kms` or `gpg`,
  a `sign.key` other than `env://COSIGN_KEY`, and any
  `sign.transparency`. The warning names the key that signs instead. One
  declaration often serves several forges, so the release goes on.
- The GitLab release publishes npm packages. An npm project that lists
  `forge-packages` under `publish-to` adds the plan-driven job
  `Publish: npm packages` to the release child: after `Publish: release` it
  publishes the signed hand-off's packed tarballs to the project's package
  registry under `release-<version>`, with the deploy token
  `RELEASE_PACKAGE_TOKEN` of the `release/packages` environment and on the
  signer's runner. Each tarball must pack the name and version of its
  project's `package.json` at the release commit
  (`publish forge-packages npm-tarballs`), and a retry leaves a package the
  registry already holds with the same content. `plan
  gitlab-release-pipeline` has a `--packages-output` flag.
- `publish forge-packages deploy --project-type npm` runs npm without the
  job's `npm_config_*` and `NODE_OPTIONS` variables, on GitLab refuses a
  `CI_PROJECT_ID` that is not a number or not the project of the job's
  pipeline, and publishes with `RELEASE_PACKAGE_TOKEN` where the job holds
  one. It refuses a package whose `publishConfig` names another registry
  than the forge's, for every package or for a scope
  (`@scope:registry`).
- The GitLab release builds a project's own graph of base images. The
  policy names the graph as `base-stages`, for example
  `base tools+debug:base`, and the project's planner as `base-plan`.
  `Release: plan` adds the jobs of each stage: `Base: plan` and
  `Base: build` run the project's planner and Containerfiles with no
  release secret, `Base: parent` verifies a child's promoted parent against the
  pinned Cosign key and fetches it in a signer job, `Base: publish` pushes
  the unsigned candidates to the publication project's `bases` repository
  with `RELEASE_REGISTRY_TOKEN`, never the job token, and
  `Base: sign and promote` holds the key. A child's plan waits for its
  parent's promotion. A stage whose final tags an earlier release promoted
  is verified and reused. The build commands read each promoted image from
  `FORGE_CI_BASE_IMAGES_DIR/<flavor>.json`. `plan gitlab-release-pipeline`
  has the flags `--base-stages` and `--image-builder`, and
  `container base-images child-base fetch` fetches the parents of a child
  stage in one call. `examples/base-images` gains its GitLab files.
- Forgejo builds a project's own graph of base images. `base-images.yml`
  builds one base, a root or a child on one authenticated parent, in native
  jobs that hold no secret, and stages an unsigned candidate with its ledger,
  its declared input map and its SBOMs. `verify-base-container-images.yml`
  takes `expected-ref` and authenticates exactly that published base as a
  parent, with `parent-base-json` as its output.
  `promote-base-container-images.yml` takes a stage's `ledger-json` in place
  of `all-images-json`. `container base-images root-base plan` and
  `child-base plan` emit the `builds` and `archs` outputs a layer schedules
  its build jobs from. `examples/base-images` is a root and one flavor
  released as one workflow, with a live case in the `base-graph` lane.
- `base-images.yml` builds every sibling of one parent in one call. A child
  stage's `image-plan-json` may list several images, each held to the one
  `parent-base-json`; each image and architecture builds in a job of its
  own, every layout is scanned before the first upload, and the ledger and
  `base-inputs-json` carry one entry per flavor for the one signing,
  promotion and verification call that follows. A plan of one image is
  unchanged. `container base-images child-base` admits and publishes such a
  plan, the `plan` verbs emit a `layouts` output, and the `build` verbs
  take `--image`. `examples/base-images` gains a second flavor, a sibling
  built in the same stage, which the `base-graph` live case releases.
- A base that is already published is a child's parent on the terms of one
  promoted in the run. Called without `expected-ref` and with the registry
  token, `verify-base-container-images.yml` returns `parent-bases-json`, a
  map of every found flavor to the `{"ref","input_id"}` its evidence
  verified on, and `container base-images validate --auth-file` writes it
  as `parent_bases_json`. Without the token the map is empty.
- GitLab release-image plans now receive every declared runner input from
  readonly policy bindings, both before release preparation and at the image
  planning job. Missing native build runners are refused by shared admission,
  not an inferred architecture list. There are no new component input names
  or contract/SDK requirements; policies must name each selected platform's
  build runner. Export admission remains on its separate interface.
- `container platform-plan` accepts the repeatable `--runner-input
  name=value` of the other plans and admits each platform it plans as an
  image build: a platform whose build runner input has no value is refused
  with the same usage error, before any output, with no fallback to the
  ordinary runner or another architecture's. Forgejo's `runtime-images.yml`
  and GitHub's `publish-container.yml` pass their runner inputs, so an empty
  or `[]` build input of a selected platform now stops at the plan instead
  of at the leg; their inputs are unchanged. A Forgejo release that exports
  files per platform before `release-images.yml` should pass the plan the
  build runner values it gives that workflow and drop its own arm64 check.
  Without `--runner-input` nothing is checked, as before.
- GitHub release and snapshot plans now receive all declared runner inputs
  after workflow defaults, and refuse image/Xcode platforms without their
  own runner before emitting plan outputs. `plan snapshot-release` accepts
  the same repeatable `--runner-input name=value` interface as `plan release`.
  Direct CLI callers should pass their layer's inputs; omission stays
  compatible until the GitLab and Forgejo admission migration is complete.
- GitLab components take their runner tags as inputs: `runner-tags` on
  `check`, `nanolinter`, `megalinter`, `build-*`, `publish-maven-central`,
  `publish-forge-packages` and `release`, and `signer-runner-tags` on
  `release` for the jobs in `signer-image`. Each defaults to `[]`, untagged,
  as before. docs/runners.md gains one table of the runner roles and their
  input on each forge.
- `container release-images fetch-build-inputs` creates the missing parents
  of `--inputs-dir`, so a build's `inputs/<name>-arch-<arch>` can be named
  directly; the directory itself must still be new.
- Add credential-free root-only plan and canonical-payload admission against
  independently approved exact plan bytes and explicit producer source facts.
  Verify retained producer SBOM pins without regeneration; content revisions
  remain distinct from checkout commits. No protected workflow is activated.
- `forge-ci setup status` adds a step to protect the release tags before the
  first release request. The README Quick Start and the forge guides now lead
  from `init` to `setup status`, and the README pins the current release.
- Admit exact or namespace-qualified workflow dependencies with
  `platform dependency`: require one job, an admitted result and explicit output
  presence before downstream reusable calls consume producer facts.
- Fetch locked Cargo dependencies with bounded cancellation-aware retries,
  without invoking builds or tests. Extend existing unsigned file manifests
  with evidence without rediscovering GoReleaser metadata or replacing subjects.
- Admit base promotion ledgers and native layout-source bindings in existing
  image commands; stage and verify release handoff bytes in the release owner.
- Reuse release-request source admission before loading the prepare signing key.
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
- Give the Android library demo a release on hosted GitHub: its module
  declares a Maven publication of the release variant, its declaration
  publishes that to the forge's package registry, and a release fixture calls
  the release orchestrator. It declares `sign.method: gpg`, so a hosted
  trial signs as declared, and the hosted release oracle reads a Maven
  package back from GitHub Packages and removes it afterwards. Written and
  not yet run.

### Changed

- The default lint floor (`required-lints`) is `secrets` on every forge; it
  was `secrets,sast`. SAST still runs where a project's plan lists it and its
  findings are reported, but they no longer fail a pull request by default.
  A caller that wants them to block sets `required-lints: secrets,sast`, or
  the project lists `sast` under `[policy] block`. The example plans no
  longer block on it.
- The GitLab release renders its changelog beside the consumer checks,
  once `Check: release request` has authorized the request, rather than
  after them. `Prepare: release commit and tag` now needs
  `Check: consumer checks` itself, so nothing is committed, tagged or
  published before the checks pass.
- **BREAKING**: `forge-ci plan release` and
  `forge-ci container snapshot-images plan` refuse a forge's call that
  passes no `--runner-input`, exit 2, as the base stage plans do. GitHub's
  and GitLab's snapshot image plans now pass their runner inputs; a
  consumer's own `plan release` call passes its own, as the GitLab
  monorepo example does. A `--provider local` run takes none.
- **BREAKING**: `release publish`,
  `publish forge-packages distro-packages` and `release validate-published`,
  when they publish from a release file manifest, refuse one that declares
  a checksums file or an SBOM without its signature. Each must have a
  Cosign `.bundle` or an OpenPGP `.asc` beside it, as the provenance
  statement and the evidence already must, on every forge. Publishing with
  `--asset` or `--assembly`, as the GitHub release does, is unchanged. An
  `.asc` is now a recognised release signature on GitHub, GitLab and
  Forgejo alike. Before, a release whose signing step did not run could be
  published with unsigned checksums. The Forgejo sign job also stops when
  the engine names a release file signing method other than `gpg` or
  `cosign`, so neither signing step is skipped without a word.
- **BREAKING**: A Cargo version bump (`forge-ci version bump` and
  `version bump-plan`) fails when it cannot refresh an existing
  `Cargo.lock`. Without `cargo` on `PATH` it exits 78, a missing
  prerequisite, and writes nothing; when `cargo update --workspace` fails
  offline and online it exits with cargo's failure, 1, or 69 when cargo
  reports that it could not reach the crates index. The error names `Cargo.lock` and the
  cause. Before, the bump printed a warning that the lockfile would heal
  itself and ended successfully, with `Cargo.toml` changed and the
  lockfile stale: a release then committed the stale lockfile or was
  refused a job later. A project without a `Cargo.lock` is bumped as
  before and needs no `cargo`.
- A GitLab job is run once more when the engine ended it because a
  dependency was unavailable (exit code 69), and for no other failure:
  `retry: {max: 1, exit_codes: 69}`, a literal of the `check`, `release`
  and `snapshot-images` components. The jobs that read or build get it.
  Every job that pushes an image, signs, tags, moves a tag, publishes the
  release or a package, deletes, or triggers the pipeline of such jobs
  says `retry: 0`, so a `default: retry` of the including configuration
  does not reach it. The fetch of the engine's helpers asks a forge it
  could not reach three times and then ends the job with 69. The lint and
  `build-*` components name the same retry, and the
  `publish-forge-packages`, `publish-maven-central` and
  `publish-apple-appstore` components say `retry: 0`, which also switches
  off a `default: retry` the including configuration set for them.
  `docs/gitlab.md` lists the jobs.
- **BREAKING**: Exit code 69 means only that a registry, forge or download
  host could not be reached or is rate limited. A required tool that is
  missing or cannot start, an unsupported architecture, a malformed engine
  pin and a missing engine binary now exit 78, with invalid configuration;
  a tool that ran and failed, a redirect limit and an engine binary or
  checksum file that is not the recorded one exit 1. The shell library
  returns 69 only when curl could not resolve, reach or keep a transfer
  with the download host, and no longer for an HTTP status. A caller that
  retried on 69 no longer retries these; one that read 69 as "tool missing"
  reads 78.
- forge-ci's own release always runs its strict black-box and integration
  gates, and is skipped when either fails. The repository variable
  `SELF_RELEASE_INTEGRATION_GATES`, which skipped both unless it was
  `true`, is no longer read; remove it. No job of `self-release.yml`
  carries a condition, so no variable or input waives a gate.
- forge-ci's own runtime images are scanned under the engine's
  vulnerability policy, and their administrator chooses whether a finding
  blocks. The default reports: findings are printed, the scan reports are
  kept and the image is published. Enforcement is the opt-in, with the
  policy a consumer's release images get with
  `enforce-vulnerabilities: true`: a `CRITICAL` finding refuses the image
  unless `.trivyignore.yaml` approves an unfixed one. On GitHub, Self
  Runtime Container reads the repository variable
  `FORGE_CI_RUNTIME_VULNERABILITIES`: `enforce` turns the gate on, and any
  other value fails the run before anything is built. On Forgejo,
  `runtime-images.yml` takes `enforce-vulnerabilities`, `false` by
  default. The GitHub build passes the setting to `container build`, which
  scans before the first push; the Forgejo assembly runs
  `security scan container` in place of a raw Trivy call, scans every
  architecture, and uploads the reports of a refused scan as
  `runtime-scan-<target>`. Consumer images and their settings are
  unchanged.
- `base-images.yml` takes `enforce-vulnerabilities`, with the name, the
  default `false` and the meaning it has in `release-images.yml`, and
  passes it to the base publication. The stage could only report its scan
  findings before; with `true` a blocking finding refuses publication.
- **BREAKING**: A snapshot reads the release's declaration. `container
  snapshot-images plan` refused a declaration with containers whose `sign`
  block names `sigstore` or `kms`, or whose container sets
  `enable-slsa: true`, so a project that signs its release images kept a
  second declaration for snapshots. The plan now leaves out what only a
  release does, `sign`, `git-signing` and `enable-slsa`, and publishes the
  declared images as snapshots: unsigned, without provenance, under their
  `-snapshot` tags. The release route reads the same file as before and its
  signing is unchanged. A snapshot caller that relied on the refusal sets
  `publish-containers: false` to publish no snapshot images; a project with
  a second, unsigned declaration can drop it and the `artifacts-config`
  input that names it. `build-secrets` are still refused. See
  docs/workflows.md, What a snapshot takes from the declaration.
- **BREAKING**: A GitLab release whose `artifacts.yml` sends an npm project
  to `forge-packages` published no package before and now does. The `*.tgz`
  files at the root of its `dist/` must be exactly its planned packages, one
  each, or `Build: release files` refuses the release before signing, and
  the project needs the `RELEASE_PACKAGE_TOKEN` variable. Leave each packed
  tarball there and set the variable, or remove `forge-packages` from
  `publish-to` to keep releasing files alone.
- **BREAKING**: A GitLab release whose `artifacts.yml` sends a Maven library
  or a Gradle project to `forge-packages` is refused when it is planned; it
  released without that package before. Remove `forge-packages` from that
  project's `publish-to` and publish it with the `publish-forge-packages`
  component.
- **BREAKING**: `--runner-archs` is removed from
  `container base-images root-base plan`, `child-base plan` and
  `container release-images plan`. The base stage plans take the layer's
  runner inputs as repeatable `--runner-input name=value` and admit each
  build through the same runner admission as the release plans: a build of
  a platform whose image build runner input has no value is refused, with
  no fallback to the ordinary runner or to another architecture's. A base
  plan given no `--runner-input` is refused. Forgejo's `base-images.yml`
  passes `runs-on`, `build-runs-on-linux-amd64` and
  `build-runs-on-linux-arm64` as the caller gave them and no longer derives
  the architectures in shell; its inputs are unchanged. The GitLab release
  passes `build-runner-tags-linux-amd64`, the runner of its one base build
  job, so an arm64 base is still refused there. Direct CLI callers replace
  `--runner-archs amd64` with the runner input of each platform they build.
  The refusal is now a usage error that names the input, not an
  invalid-configuration error.
- **BREAKING**: `container base-images root-base` and `child-base` refuse a
  plan whose image has another final tag than `<base-input-id>-<flavor>`,
  on every forge; such a plan was built, signed and promoted before. Name
  the final tag that way, as `examples/base-images/base-plan.sh` does. The
  verbs take `--stage-flavors`, with which a caller that names the stage
  itself, as the GitLab policy does, holds the plan to exactly those
  flavors.
- **BREAKING**: The GitLab release policy input `base-images` is removed,
  with the path it selected: a release no longer signs and promotes base
  images that the project's build commands pushed with the job token, and a
  build that hands over `dist/base-images.json` is refused. Name the graph
  as `base-stages` and the planner as `base-plan`; the policy builds the
  images.
- **BREAKING**: Base-image promotion requires the expected input map.
  `promote-base-container-images.yml` has a required `base-inputs-json`
  input, and `container base-images promote` a required
  `--base-inputs-json` flag: a JSON array naming every promoted flavor once
  with its `base_input_id` and, for a child, its `parent_base`
  `{"ref","input_id"}`. Each image's own signed statement must carry those
  facts, or nothing is promoted. A caller of `base-images.yml` passes its
  `base-inputs-json` output; any other caller passes the map it already
  gives `verify-base-container-images.yml`, or prints the map a build
  ledger declares with `container base-images declared-inputs`.
- **BREAKING**: The GitHub reusable workflows take their runners as JSON-array
  inputs: `runs-on` for ordinary jobs, `build-runs-on-linux-amd64` and
  `build-runs-on-linux-arm64` for image builds, and `runs-on-darwin-arm64`
  for the unsigned Xcode build and Swift lint jobs, defaulting to today's hosted
  labels. The orchestrators pass them on. Signing, promotion and publication
  jobs stay on GitHub-hosted runners, signed Android and Xcode builds
  included. The `macos-version` workflow input and
  the `xcode-ios` `config.macos-version` key are removed, with no alias:
  replace `macos-version: macos-26` with
  `runs-on-darwin-arm64: '["macos-26"]'`, and drop the key from
  `artifacts.yml`, which now refuses it. App Store publication runs on
  `macos-26`. See docs/runners.md, GitHub runners.
- **BREAKING**: GitLab runner inputs follow one pattern,
  `[role-]runner-tags[-<os>-<arch>]`, with no aliases for the old names.
  `release` and `snapshot-images` rename `build-runner-tags` to
  `build-runner-tags-linux-amd64` and `build-runner-tags-arm64` to
  `build-runner-tags-linux-arm64`; `publish-apple-appstore` renames
  `runner-tags` to `runner-tags-darwin-arm64`. An include that passes an old
  name fails, because GitLab refuses an undeclared input; rename it in the
  policy project. `release` drops `checks-runner-tags`: the consumer checks
  and all family/release-files build commands, amd64 image and export jobs
  run on `build-runner-tags-linux-amd64`, whichever the image builder,
  kaniko included. Version and ordinary jobs stay on `runner-tags`.
  Each job takes its own role's input; none falls back to
  another's.
- **BREAKING**: `build-runner-tags-linux-amd64` and
  `build-runner-tags-linux-arm64` of `release` and `snapshot-images` have no
  default. Every policy names them: the amd64 tags of a real runner, such as
  `[privileged]` or a GitLab.com `saas-linux-*` tag, and the arm64 runner's or
  `[]` for none. The release refuses empty amd64 tags at its first job, and a
  snapshot refuses an amd64 or arm64 build without tags. An untagged build
  could land on a runner of any platform.
- **BREAKING**: GitLab release jobs refuse a `signer-runner-tags` that names a
  `$variable`: GitLab expands a variable in a tag, which would let a project
  choose the runner that sees the release credentials.
- **BREAKING**: Maven `build maven run --working-dir` now selects the execution
  directory as well as metadata. It must stay workspace-relative; relative
  settings resolve from that project. The three forge adapters use this contract.
- **BREAKING**: `container base-images root-base admit-plan` and
  `admit-payload` are removed; no workflow or job called them.
- **BREAKING**: A GitLab release with `enforce-vulnerabilities: true` and
  `base-stages` refuses to publish a base image with a blocking
  vulnerability finding. The base publication did not receive the setting
  and only reported, whatever the policy said. Fix or waive the finding in
  `.trivyignore.yaml`; a release with the default `false` is unchanged.
- **BREAKING**: `release attachments upload` fails when any file fails to
  upload, after it attempted the rest, and names each one. It warned and
  passed while at least one file landed, so a release could lose its
  signature bundle or provenance and stay green. This holds for forge-ci's
  own binary release (`release-binary.yml`) and for
  `publish-maven-github.yml`. Run the job again: the upload replaces a
  file the release already holds.
- **BREAKING**: `release validate-published --draft` fails when the release
  is not visible; it warned that the draft was not read back and passed, so
  a release that was never created passed too. The GitHub and Forgejo
  release jobs read a draft back with `RELEASE_TOKEN`, which created it,
  and compare its files like those of a published release; a published
  release is still read with the run's read-only token. A caller of the
  command gives it a token that can read drafts.
- `release validate-published` compares the SHA-256 digest of each planned
  file where the forge reports one, which GitHub does for a release asset.
  Name and size alone let a file of the same size with other bytes pass.
  Forgejo and GitLab report no digest, so that comparison is not made
  there. GitHub computes the digest of every asset at upload, so an asset
  it reports without a SHA-256 digest fails the readback: its content was
  not verified. A GitHub Enterprise Server too old to report asset digests
  fails it for that reason.
- **BREAKING**: `security scan container` fails when the SARIF or the GitLab
  container-scanning report of a scan with findings cannot be written. It
  warned before, so under `enforce-vulnerabilities: false` the step passed
  and the jobs that upload those reports had nothing to upload. The
  findings are still printed and gated, and the other report is still
  written, before the command fails naming the lost report. A clean scan
  writes both reports too, with no findings; it retired the previous ones
  and wrote none, so "clean" could not be told from "no report".
- **BREAKING**: `container manifest digest --digest-file` fails when the
  named file cannot be read, is empty or holds something other than a
  digest. It read the digest from the registry alone then, and compared it
  with nothing.
- **BREAKING**: `container release-images validate --allow-reattest` refuses
  an image whose signed provenance verifies and names another release. It
  answered `reattestable` when the image's OCI labels matched, so new
  provenance could be attached over evidence that the tag belongs to
  another commit.
- **BREAKING**: `container release-images validate --allow-reattest` refuses
  an image that has a SLSA provenance attestation which does not verify:
  signed with another key or identity, changed, or made for another
  subject. It answered `reattestable` when the image's OCI labels matched,
  because cosign ends on the same error for an attestation it rejected as
  for none, so new provenance could be attached beside the one that was
  refused. Whether an attestation is stored is now read from the registry,
  from the OCI referrers of the digest (the Referrers API, or the referrers
  tag where a registry has none) and the `sha256-<hex>.att` tag, and only
  an image with none is `reattestable`. A registry that cannot be read for
  it fails the command as an unavailable dependency (exit 69), and stored
  evidence that cannot be read as an attestation, a layer an `.att` tag
  names and the registry does not serve, or a layer over 128 MiB is
  refused. The check guards against provenance that does not match and
  provenance that was changed, not against whoever controls the registry
  deleting it.
- **BREAKING**: `policy nanolinter` fails when the lint runner reports a
  security check (`osv`, `trivy-fs`, `go-vulncheck`, `rust-audit`,
  `secrets`, `sast` or another check of class `security`) as skipped, for
  example for a network outage or under `--offline`. It printed
  `result=clean count=0` and passed before, as for a clean scan. The gate
  prints `result=incomplete` with `could-not-complete=<check ids>` and the
  command fails as a dependency failure with enforcement on or off: the
  settings waive findings, not whether a check ran. A security check that
  does not apply to the repository still passes, and is named as
  `not-applicable=<check ids>`; its gate prints `result=not-applicable`
  when no check of the gate ran. The rule reads the rows of the runner's
  SARIF. Nanolinter 0.8.6 writes a row only for a failed check or a
  finding, so with it a skipped or inapplicable check is not in the report
  and the gate reads as before; the rule takes effect with a Nanolinter
  that reports those checks. A job that cannot run a security check
  names it in `disable-checks`: an input of the Forgejo `lint-nanolinter`
  action, of the GitLab `nanolinter` component and of the GitHub pull
  request orchestrator, which forwards it to `lint-nanolinter.yml`. Each
  passes it to `nanolinter verify --disable-check`; it cannot remove a
  check `required-lints` names.
- **BREAKING**: The GitLab `nanolinter` and `megalinter` components fail
  when their SARIF cannot be converted to the GitLab SAST report. They
  warned and passed, and the merge request showed no findings. The gate's
  verdict is printed first and the SARIF is kept. For `megalinter` this
  includes a failed checkout or download of the engine, so its image needs
  Git, curl and `sha256sum`, or `enable-gitlab-sast: false`.

### Removed

- **BREAKING**: The `install-forge-ci` composite action and
  `scripts/bootstrap/install-forge-ci.sh` are removed. The macOS jobs
  (`lint-swift`, `build-xcode-ios` and `publish-apple-appstore`) build the
  engine with `.github/actions/build-darwin-cli` from a checkout at the
  commit `forge-ci-binary-ref` names. A workflow that used the action or
  the script downloads the CLI from a release instead.
- **BREAKING**: The forge-ci release no longer ships a darwin/amd64 CLI;
  macOS gets darwin/arm64 only.
- The Forgejo guards no longer carry a fork self-CI registry or the
  action-origin guard. Upstream registered no self-CI workflow; a fork
  exempts its own workflows in its own copy of the guards.
- **BREAKING**: The `KEYCHAIN_PASSWORD` secret is removed from
  `release-orchestrator.yml`, `release-snapshot-orchestrator.yml`, the
  build stages and `build-xcode-ios.yml`. Nothing read it: the iOS build
  mints its own keychain password per run. A caller that passes it by
  name drops it; `secrets: inherit` callers change nothing. It is no
  longer a reserved `build-secrets` name.

### Fixed

- A GitHub draft release without release files is read back. GitHub lists
  a new draft about a second after it created it, and the list is the only
  way to find a draft by its tag, so the readback ran first and reported
  that no release existed. `release publish` now returns once the list
  shows the draft, and fails with the tag if it never does.
- The GitHub image job of a container that names an npm artifact no longer
  requires the package to hold a `dist/` directory. A Nuxt application
  packs `.output/`, and its image job failed on the missing `dist/`. The
  job checks that the packed package arrived: `container
  extract-npm-tarball` fails without one, and `container validate
  artifacts` no longer takes `--project-type npm`.
- An Xcode build with `enable-code-signing: false` archives with
  `CODE_SIGNING_ALLOWED=NO`. A project that names a team and a
  provisioning profile failed to archive without them, although the build
  was not to be signed.
- The lint gate under `enforce-lint: true` no longer fails on the findings
  of a check the project's plan only reports (`[policy] warn`, or absent
  from `[policy] block`). It counted every failed check, so a plan could
  not demote one. The floor of `required-lints` blocks as before, and
  dependency advisories follow `enforce-vulnerabilities` as before.
- A GitHub release or snapshot of an Android application whose declaration
  does not set `include-aab` builds and uploads the AAB again. The build
  stage compared the unset value with `false`, which GitHub's expressions
  treat as equal, so the AAB was left out and the release then failed for
  want of it. The same comparison turned Xcode code signing off for a
  declaration that does not set `enable-code-signing`; both now default to
  true as documented.
- Security: the engine and the Go runtime image build on Go 1.27.4,
  which fixes GO-2026-6859, an HTTP/1 server crash from a
  `MaxBytesReader` header race in `net/http` that govulncheck reports as
  reachable through the engine's HTTP paths.
- Security: on Forgejo 17, a publication job of
  `sign-and-publish-release.yml` could start after the signer refused the
  release. In a called workflow Forgejo 17 evaluates a job's own `if` but
  not the implicit success of its needs, so `Publish: images`,
  `Publish: release`, `Publish: tidy up` and `Publish: packages` ran after
  `Sign: files and images` had failed; with images, promotion could have
  moved stage tags from the unsigned build. Every job of the Forgejo
  workflows that needs another now names `needs.<job>.result == 'success'`
  for each need in its own condition, among them the release commit and
  tag of `prepare-release.yml` and the image publication of
  `release-images.yml`, `runtime-images.yml`, `base-images.yml` and
  `snapshot-images.yml`, and `test/forgejoguard` refuses a job that does
  not.
- The swap policy of `release sign` and `release sbom-zip --sign` no
  longer refuses a job whose container runs with swap disabled.
  `/proc/swaps` is host-wide, so a job container saw the host's swap and
  the GPG step stopped although its memory could not be swapped out. When
  `/proc/swaps` lists swap, the policy now passes if `memory.swap.max` is
  `0` for the process's cgroup v2 or an ancestor, and says so in a Notice
  naming the cgroup; cgroup v1 or unreadable files still refuse. Set
  memory-swap equal to memory for the job containers in the runner setup:
  `container.options` on Forgejo, `memory` and `memory_swap` under
  `[runners.docker]` on GitLab. The refusal names these fixes. See
  `docs/verification.md#swap-policy`.
- On Forgejo, an artifact upload whose finalisation is refused with HTTP
  500 is finalised again, within the runtime client's retry bounds.
  Forgejo 17 confirms an artifact by opening every chunk of the run, so a
  parallel job of the same run that removes its merged chunks at that
  moment fails the confirmation with `HTTP 500: dependency unavailable`,
  although nothing of the artifact was changed. A transport failure, whose
  outcome is unknown, and every other status still end the upload at once.
- `release sign --method gpg` and `release sbom-zip --sign --method gpg`
  apply the swap policy before they read the private key, so the key is
  never decrypted on a runner with swap on. Before, the key was loaded
  and then refused.
- `forge-ci init` on GitHub pins the engine by the commit of the release
  it installs. The GitHub examples and docs pinned `uses:` and
  `forge-ci-binary-ref` by the tag object of v0.90.0, not its commit, and
  init replaced only the `v0.90.0` comment, so a project
  got the v0.90.0 object labelled as the release it asked for. The
  examples now carry `PIN_FORGE_CI_SHA`, as the Forgejo and GitLab ones
  do, and init writes the release tag's commit in its place. A placeholder
  left in place is reported by init and by `forge-ci doctor`.
- **BREAKING**: `artifacts.yml` refuses a container whose `platforms`
  names no platform, such as `","` or `" "`. Such a value was accepted and
  planned an image with no build: the Forgejo release-images workflow got
  an empty build matrix and its later jobs were blocked without a reason,
  and the other release plans and the runner check named no platform for
  it. An omitted `platforms` still builds `linux/amd64`. A container's
  `platforms` now reads as one list on every route, separated by commas
  or spaces: the release-images plan, the runner check and
  `RELEASE_PLATFORMS` narrowing split on commas alone, so
  `linux/amd64 linux/arm64` was refused there as one unknown platform,
  where the image build and the transfer names took two.

- A Gradle release no longer ships the jar of `buildSrc`. The upload of
  `build-gradle-app.yml` (`**/build/libs/*.jar`) and of the GitLab
  `build-gradle` job (`**/build/libs/`) matched `buildSrc/build/libs/`,
  though `buildSrc` builds the build itself and is no module of the
  product; the snapshot's file selection already skipped it. Both uploads
  now exclude every `buildSrc` directory. Forgejo has no Gradle build
  action: the consumer's own job names the jars it releases.
- A GitHub development snapshot builds the image of a Gradle artifact from
  the jar its build stage built. The Gradle build uploads `build/libs` of
  the project and of every subproject, so the downloaded artifact holds the
  project's jar under `build/libs/`, and the snapshot import looked for it
  at the top of the download: `prepare-context` failed with
  `has no nonempty gradle inputs`. The import now takes `build/libs` of a
  Gradle download, as it takes `target` of a Maven one.
- **BREAKING**: A GitHub release restores a Gradle artifact where it was
  built, as it does a Maven one. `publish-container.yml` unpacked the
  download into `build/libs/`, and the artifact `build-gradle-app.yml`
  uploads keeps `build/libs`, so the jar landed in `build/libs/build/libs/`
  and the `Verify Gradle artifacts` step found none. The plan now carries
  the Gradle artifact's working directory of a container
  (`gradle_working_directory`), and the new input `gradle-working-directory`
  restores and verifies the artifact there, so a Containerfile copies from
  `<working-directory>/build/libs/` on every forge. A Containerfile of a
  Gradle project that is not at the repository root copies from there
  instead of `build/libs/`. A direct caller of `publish-container.yml` that
  uploads its own Gradle artifact with the jars at its top must upload them
  under `build/libs/` instead, as `build-gradle-app.yml` does.
- `publish-container.yml` refuses a `maven-working-directory` or
  `gradle-working-directory` that is absolute or has a `..` segment, in its
  `Verify source-confined build paths` step and before anything is unpacked.
  A direct caller could name a directory outside the source checkout. The
  check is the new repeatable `--source-dir` flag of
  `container validate containerfile`.
- A Buildah build that could not reach a base image's registry ends as an
  unavailable dependency, with exit status 69. It ended as a validation
  failure, with exit status 1, so a registry name that did not resolve
  read as a defect of the build. The class is read from the `Error:` line
  Buildah ends on, as it is for Trivy and cosign, and the engine's error
  repeats what that line states, so it names the host. A `RUN` step's own
  output, the instruction Buildah quotes for a failed step, a pull Buildah
  tried again and a registry that answered (a refused login, a denied
  pull, an unknown manifest) stay a validation failure. A step's output
  can still pass for Buildah's error if it is a line that begins with
  `Error:` inside an instruction of several lines. The adapter's `push`
  and `manifest push` exports are classified the same way.
- More tools that could not reach a host end as an unavailable dependency,
  with exit status 69, where they ended as a validation failure, with exit
  status 1. As for Buildah, the class is read from the tool's own error
  and the engine's error repeats the line that names the host. When the
  tool's error leaves a doubt, the failure stays a validation failure, and
  so does a tool that was killed, by a time limit or a cancelled run:
  - `apt-get`: an install that fails after a package source could not be
    resolved or connected to. An update that cannot read a source only
    warns, so the install after it ended on `Unable to locate package`. A
    package script that fails, a dependency that cannot be met, and any
    file that failed for another reason, a hash that does not match, a
    signature `apt-get` refuses or a status, stay a validation failure,
    also beside a source that was not reached. A package whose name is
    wrong still ends as unavailable when a source was not read: `apt-get`
    says the same for it as for a package that source would have held.
  - The Gradle wrapper: a `./gradlew` that could not download its Gradle
    distribution, read from the wrapper's own stack trace. A dependency
    Gradle itself could not resolve is still a validation failure, since
    Gradle reports it like a task's own error.
  - Maven: a build Maven ends on a file it could not transfer, when Maven
    states the unreached host, in that error or in a warning about the
    same repository. That holds for every Maven run of the engine that
    shows Maven's output, the effective POM of a publication included. A
    build that ends on failed tests, a warning about another repository,
    and a transfer that ended on a checksum, a certificate, a status or a
    blocked mirror stay a validation failure. Reading a single value with
    `mvn help:evaluate` is not classified. Maven's standard output now
    always passes through the engine, as it did with a dependency token,
    and the engine holds it in memory while Maven runs.
  - `npm ci`: a registry that could not be resolved or reached, by the
    error code npm prints. `npm test`, `npm run` and `npx` are left alone.
  - kaniko: a registry it could not reach, read from the
    `error building image:` line it ends on. A `RUN` step's output is not
    read.
  - git: a fetch, push or `ls-remote` whose remote was not resolved or
    connected to, read from git's `fatal: unable to access` line and from
    ssh's own lines. What the remote sent is not read.
  - cosign: a registry or a Sigstore service it could not reach while
    signing or attesting, read from the `Error:` line it ends on. A
    verification (`verify`, `verify-blob`, `verify-attestation`) is never
    classified: it ends as a validation failure whatever cosign says.

  A tool whose error states a limited rate (a 429 and no other answer)
  ends as rate limited, also with exit status 69: git, `apt-get`, kaniko,
  Maven and cosign when it signs or attests, as Buildah already did. syft
  is not classified.
- A Forgejo snapshot that plans no image ends. With files alone
  (`publish-files: true` and no container) or `publish-containers: false`,
  the build and publish matrices of `snapshot-images.yml` were empty.
  Forgejo creates no job for an empty matrix and never settles the jobs
  that need it, so `Publish` and `Summary` stayed blocked until the run was
  cancelled, although the files were built and kept. Both matrices now have
  one row for such a plan, which the job's condition skips, as the artifact
  and files matrices have, and `Summary` runs after them. A snapshot with
  an image is unchanged. Not yet run on a forge.
- A GitHub release that changes the version of a Cargo project with a
  committed `Cargo.lock` no longer stops in `Prepare: version, release
  commit and tag` with `existing lockfile Cargo.lock must participate in the
  version update`. The key-free job that prepares the version files changed
  `Cargo.toml` and left the lockfile as it was: its `host-toolchain` tool
  profile searched `/usr/bin`, `/bin` and `/usr/local/bin`, and a hosted
  runner is assumed to have `cargo` in `$HOME/.cargo/bin` instead. The
  profile now ends with that directory and refuses a `HOME` that is not one
  absolute directory. A hosted release that changes a Cargo version has not
  yet been run with it. The jobs that hold signing keys keep the `host`
  profile, and the lockfile check is unchanged.
- The vulnerability gate's image scan allows Trivy 30 minutes, as the image
  evidence scan does, and scans for vulnerabilities only. It ran with
  Trivy's default of five minutes, which the download of Trivy's Java
  database alone can use up on a slow link, and the gate then failed as a
  validation failure. It also ran Trivy's secret scan, whose result nothing
  read.
- A prepared live run whose pattern names a table's test and selects no row
  of it is refused as a usage error that names the pattern. It ran the test
  with no case in it and ended `passed`.
- The Android runtime image build survives a dropped SDK download.
  `sdkmanager --install` was run once, so one closed connection to the SDK
  repository failed the whole image build. The install is now tried up to
  three times with a short pause; sdkmanager verifies each archive's
  checksum, and a failure still ends with its own output.
- Preparing the engine for a hosted GitHub trial recovers a failed engine
  build. `FORGE_CI_LIVE_ENGINE_MODE=prepare` found the finished, failed
  `Self Runtime Container` run of the commit and stopped, so one transient
  job failure was final until someone reran the jobs by hand. It now asks
  GitHub once to rerun the failed jobs of that run, waits for the new
  attempt and judges it, logging the run, its old conclusion and the
  attempt. A second failure stands, and `reuse` still only reads.
- A Forgejo snapshot of a Go or Cargo artifact that writes its Build SBOM no
  longer stops at `cyclonedx-gomod not found in $PATH` (or `cargo-cyclonedx`).
  The producer and files jobs of `snapshot-images.yml` build in the caller's
  toolchain image, which carries no forge-ci tool, and ran the build without
  the generator the release's `build-go` and `build-cargo` actions install.
  Both jobs now install the same pinned, checksum-verified generator before
  a build whose plan includes the Build SBOM, and use one already on `PATH`
  at the pinned version. A declaration with `sboms: none`, and a Maven,
  Gradle or npm build, installs nothing. `container snapshot-images
  read-plan` answers the new field `build-sbom` for a producer or file
  output. GitHub and GitLab are unchanged: their jobs run in the runtime
  image, which carries the generators. The installs of the snapshot workflow,
  the publisher's Trivy and Syft among them, now download into the job's
  `RUNNER_TEMP` and no longer into a `/tmp` that jobs on one host share.
- A GitHub release or snapshot of an Android library no longer hands the
  Android build an empty `enable-signing`. The plan leaves
  `enable-android-signing` out when the declaration does not set it, and the
  build stages forwarded that as null to a boolean input; they now pass
  `false`, as the GitLab plan does.
- `Publish: npm packages` works on a GitLab instance with a private CA. The
  release jobs gave the instance's CA to curl, Git and Go but not to Node,
  so npm refused the instance's package registry, tried three times and
  failed after 70 seconds with `npm dist-tag: exit status 1` and nothing
  else. They now set `NODE_EXTRA_CA_CERTS` as well.
- A failed npm registry read prints npm's own error output, which was
  dropped, and takes its class from npm's error code: a registry that
  could not be resolved, reached or kept is an unavailable dependency, and
  a status or a certificate npm refused stays a failure.
- An answer outweighs a host that could not be reached. A tool's failure
  that names both, such as a mirror that refused the connection and a 404
  or a checksum mismatch from the next, is the tool's failure and is not
  retried; only a 429 is. Only the tool's own error lines are read, not a
  path or message it repeats from what it scanned. A certificate that does
  not verify, a TLS alert and a registry credential file that cannot be
  read are failures, not an unavailable dependency, also in the Forgejo
  client, the Maven Central and Maven registry uploads, the release
  attachment upload and the enforced base-image freshness check. A 408 from
  a server the engine asks itself is an unavailable dependency, as a 5xx.
- Exit code 69, an unavailable dependency, is no longer given for a failure
  that asking again cannot pass, and such a failure is no longer retried.
  A pinned tool download the server refuses keeps its status's class (401
  and 403 exit 77, 404 exits 66) and only a 408, a 429, a 5xx and a failed
  transfer are tried again. A Trivy scan, a `mise install`, a rustup
  toolchain download, `go mod download`, `cargo fetch` and the base-image
  evidence check are run again only when the tool states a transport
  failure: a name that does not resolve, a connection refused, reset or
  timed out. A checksum the tool refuses, a registry status and any other
  failure of the tool return from the first attempt and exit 1. A registry
  outage during the base-image evidence check now exits 69, where it was
  reported as a failed verification.
- `container base-images freshness --enforce=false` tells a pin it could
  not check from a current one. A source tag that could not be resolved
  was skipped with a warning and left `stale-count` at 0, as for a current
  pin. The run still passes; the warning carries the cause and a note
  counts the pins that were not compared.
- `policy nanolinter` reports and writes the findings of every check before
  it fails for a check that could not complete. One check's tool or network
  failure stopped the command before the security SARIF was written and the
  findings were counted, so real findings of the other checks went
  unpublished. The gate of the failed check prints `result=incomplete` with
  `could-not-complete=<check ids>` instead of `result=clean`, and the
  command still fails as a dependency failure whatever the enforcement
  settings.
- An image build outlasts a registry or resolver that stops answering for
  a couple of minutes. Buildah retries the pulls and pushes of a build
  five times, 15 seconds apart (`--retry 5 --retry-delay 15s`, where its
  default is three retries within seven seconds), and kaniko fetches a
  base image again up to five times (`--image-download-retry 5`, where its
  default is none). This holds for release, snapshot and base images. A
  failing build step is not retried.
- Release asset retention keeps an SBOM named `<name>.cdx.json`, the name
  `release files declare --sbom` documents. The default `--keep-files`
  pattern matched `.sbom.json`, `.cyclonedx.json` and `.spdx.json` alone, so
  an older release lost that SBOM while its `.bundle` stayed.
- A child base selected for kaniko is refused when it is planned, not after
  its parent was fetched. `container base-images child-base plan` takes
  `--image-builder` (`CI_IMAGE_BUILDER`), and `base-images.yml` passes it the
  validated builder: a child of an authenticated parent builds with Buildah,
  and the message names the runner-lending route for a kaniko-only pool.
- Snapshot a container that declares `extract.binary`: the snapshot builds
  and publishes its runtime image and extracts nothing, since the binaries
  are release assets. Build secrets and requested provenance stay refused.
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
