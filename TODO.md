# Forge CI: what is left

forge-ci gives projects ready-made CI for Forgejo, GitLab and GitHub: checks
on pull requests, development builds ("snapshots") and signed releases. The
live tests prove that it works on real forges. They push small demo projects
(`test/live/fixtures/consumers/`, named `hello-*`) to a local test lab (Forge
Lab) and to a GitHub test organization, then read back what was built, signed
and published. The rows that have passed are listed in
`test/live/evidence.txt`; `docs/live-runs.md` says how to run them.

Last checked against `main` on 2026-11-14.

## Waiting on the owner

- **Release Nanolinter**, the lint runner forge-ci uses. forge-ci needs it
  before its own release: the pinned 0.8.6 lets a reachable Go vulnerability
  pass the lint gate (it judges govulncheck by its exit code, and govulncheck
  exits 0 in SARIF mode). Before releasing, check which Nanolinter history is
  the one to publish, and that tag names match the `Cargo.toml` versions.
  Publish the `ghcr.io/itiquette/nanolinter` images the examples pin with it.
- **Push, tag and release forge-ci.** Always done by hand, by the owner.
- **The owner's manual testing**, then the full live set at the final commit.
- **GitHub private-visibility rows.** The public halves pass; the private
  halves wait for Actions minutes in the test organization.
- **`TestGitLabWorkflow_InterruptedJob`** is manual and has not been run.
- **"GitLab generic-cli bump"**: an old open item. Confirm it is done, or
  drop it.

## Before the next release

1. After the Nanolinter release, bump the pin in `.mise.toml` and confirm
   that `just security-govulncheck` fails on a tree with a reachable
   vulnerability.
2. CHANGELOG lines for: the Go toolchain bump (consumers move
   `canonical-builder-image` and `runtime-image-go` to the new digest together
   with the engine pin), the two breaking fixes (`KEYCHAIN_PASSWORD` removed,
   `--runner-input` required), the removed classified release route, the
   `forge-ci init` engine pin fix, the removed fork self-CI registry, and the
   docs cleanup.
3. Release order: push `main`, wait until Self Runtime Container has published
   all runtime images, then push the `release-request/v*` tag. The canonical
   delivery refuses a builder image whose Go differs from `go.mod`.
4. Check that the engine binary's provenance names the release commit. Do
   this at every release.

## Live tests to add

- **Private dependencies**: no test builds a project that fetches a
  dependency from a private package registry. Add one to a demo that already
  declares private dependencies. On GitHub it needs a private package in
  GitHub Packages.
- **Re-attestation**: one release with `--allow-reattest`, so the check that
  an image has no attestation yet runs against a real registry.
- **arm64**: arm64 builds are cross-compiled and never run. Run one GitHub
  test on an arm64 runner.
- **GitLab project variables in policy jobs**: a pipeline execution policy
  with `variables_override: allowed: false`, and a project that sets file-type
  `SSL_CERT_FILE` and `CI_SERVER_TLS_CA_FILE`. The policy job must not see
  them. If it passes, `docs/gitlab.md` may call the setting sufficient; if it
  fails, the engine must start tools with an allowed list of variables.
- **GitLab `hello-go-cli` snapshot** should run the example's own policy file
  (`examples/go-cli/gitlab/snapshot-files-policy.yml`), not one the test
  writes.
- **Release tools on the runner**: a Maven, npm or Cargo release that bumps
  the version must find `mvn`, `npm` or `cargo` on the runner (Cargo from
  `$HOME/.cargo/bin`, with the version from `rust-toolchain.toml`).
- **Untested settings**: `lint-engine: megalinter` and `lint-engine: none`
  (checked offline only), and a release from a branch other than `main`.
- **Kotlin linting**: Gradle projects are linted as Kotlin, but no demo has
  Kotlin source. Needs a demo with Kotlin code and a Kotlin lint image.
- **Coverage still missing**, each needs a test or a removal:
  - recovery of an interrupted GitHub run (Forgejo and GitLab have one);
  - Forgejo and GitHub base images built cold and then reused (only GitLab
    measures both);
  - a check of a published GitHub snapshot;
  - whether a forge-ci self-release needs a Forgejo or GitLab route, or stays
    GitHub-only.

## Live tests to retire or repair

- **Tests that need an outside project**:
  `TestForgejoLayer_NPMApplicationDevelopment`,
  `TestForgejoLayer_ConsumerSnapshotTrial`, and the modes of
  `TestForgejoLayer_ConsumerTrial` that use an outside npm or Maven
  application. Move what each checks into a test of a shipped demo, then
  delete it.
- **Tests that always skip** because they want manual input:
  `TestForgejoLayer_RustDevelopment`, `TestGitLabLayer_RustDevelopment`,
  `TestForgejoLayer_CargoBinaryRelease`,
  `TestForgejoLayer_GoModuleReleaseReachesAFreshConsumer`. Delete those a demo
  test already covers; make the rest run from a prepared test directory. Keep
  `TestForgejoLayer_ResolvedImagePlan`: nothing else checks its route.
- **The migration pull-request tests** (`TestForgejoConsumerPRUnitChecks`,
  `TestGitHubConsumerPRUnitBaseline`, `consumerpr.md`) test outside projects.
  Delete them.
- **`TestGradleLibraryProbe_RenderedShell`** fails under a loaded `test-unit`
  ("fixture exceeded its deadline") and passes alone. Find what the deadline
  guards and make the test independent of machine load; do not just raise the
  deadline.
- **The Gradle library fixture** (`test/live/fixtures/libraries/gradle-jvm/`)
  has no `.gitignore` for `.gradle/`; a Gradle run inside it leaves caches in
  the tree.

## Product

- **GitLab runner selection.** The old flag `--arm64-tags` is still used in
  `.gitlab/bin/release.sh` and `.gitlab/bin/snapshot-images.sh`. Forgejo
  already uses the new runner inputs; move GitLab to them too.
- **Remove the test override `FORGE_CI_REVIEWED_BOOTSTRAP`** and its skip in
  `scripts/ci/canonicaldelivery_test.go`.
- **Shipped pieces with no user.** Run each once, or remove it:
  - `.github/workflows/slsa-attestor.yml` has no caller;
  - the Forgejo `runtime-images.yml` is a draft that was never accepted;
  - the generic-cli adapter (`test/live/fixtures/adapters/generic-cli/`)
    exists to release other projects from this repo. Move its binary and
    identity checks to a demo, then delete it.
- **Small fixes:**
  - the test matrices print the code "D-5"; show a plain reason instead;
  - remove the legacy `skip-tests` input from the GitHub build workflows;
  - remove the "legacy" `forgejo-actions` provenance profile and the
    experimental GitLab Debian package job;
  - drop the unimplemented `publish-to: npmjs` value and the `NPM_TOKEN`
    secret nothing reads;
  - refuse `publish-to: google-play` unless
    `config.enable-android-signing: true`;
  - update the GitLab native release live test to the current release policy
    shape.
- **Private self-hosted forge-ci.** Every workflow fetches the engine without
  credentials, so the engine must be publicly readable. An organization that
  hosts forge-ci privately needs that fetch to authenticate: the GitLab job
  token, or a scoped Forgejo secret, sent only to its own forge, and in every
  flow at once. Public users must see no change. On Forgejo, private reusable
  workflows are an upstream limit to report.
- **Mobile releases**: Android on Forgejo needs a job image with the Android
  SDK; iOS needs a macOS runner.
- **Android library and Gradle JVM publications on GitLab**: the GitLab
  publish step cannot hand over an Android library or the exported Gradle JVM
  publications yet. Both work on GitHub.
- **Android development builds** keep their APKs and SBOM as job artifacts,
  but nothing records which commit they came from. That needs a small product
  change, then a test for `hello-android`.
- **A signed Maven library release**: no demo is a Maven library. Add an
  example and a demo, and a release test that checks the release holds exactly
  the POM, jar, sources jar, Javadoc jar, SBOM, checksums and provenance.
- **GitLab base images**: a reused base image still runs its build jobs and
  pushes temporary tags again (wasteful, not harmful); base images build for
  amd64 only.
- **Checks that guess from text**: the npm publish check reads "E404" from
  npm's text output (`internal/app/publish/npm.go`; `npm --json` gives a
  proper error code). Nanolinter records no result for a skipped check. A
  network failure in Syft, Gradle or Maven is not yet recognised as
  "dependency unavailable".
- **Installer errors**: a failing `scripts/bootstrap/install-*.sh` exits with
  a plain 1, so a failed download looks like a product failure.
- **Runner lending** leaves stale one-shot runner registrations behind. Choose
  a forge-side cleanup, or ask upstream for self-retirement.
- **Small guard fixes**:
  - the upload-path guard trusts `${{ inputs['build-module'] }}` in
    `.github/workflows/build-gradle-android.yml` to stay inside the
    workspace; check it properly;
  - the build-tag guard ignores files that are constrained by their name
    (`x_windows.go`).
- **Ideas from the removed "classified-files" release route**, to re-add only
  when a release flow needs them:
  - download a build artifact by its exact ID, checking that it was built
    from the expected commit and not from a fork;
  - GPG-sign only the `.deb`, `.rpm` and `.apk` files listed in the release
    manifest.

## Docs and tooling

- **User pages:** trim `verification.md`, `forgejo.md`, `gitlab.md`,
  `publishing.md`, `runtime-images.md`, `workflows.md`, `flows.md`, `sbom.md`
  and the rest. Take out dates, test names, internal function names and
  repeats. Each fact gets one home. `docs/live-runs.md` is the only manual for
  live runs and should carry no dates or run numbers.
- **Rules while editing docs:**
  - Tests read some headings and tables: `gitlab.md` "Secrets and tokens" and
    "Inputs and variables", the role table in `runners.md`, "Capability
    matrix" in `providers.md`, the schema-values blocks in
    `artifacts-reference.md`, and every GitLab `component:` YAML block. Keep
    them.
  - Pages listed in `docs/docs.go` are built into the engine. Refresh its
    checksums after editing them, and update the list when adding or removing
    one.
- **Every demo should be a copy of an example**, so the tests run what users
  copy. Still hand-written: the Gradle service, the Cargo CLI, the Maven
  reactor, the npm workspace, and the Android library.
- **Broken lint recipes:** check `just lint-commits` (the pinned gommitlint
  did not know the `-gommitconfig` flag) and `just lint-markdown` (MD076 in
  `CHANGELOG.md`).
- **Nanolinter config**: drop `go-format` from `[policy].block` in
  `nanolinter.toml`; golangci already runs it. Ask Nanolinter for a
  configurable actionlint timeout.
- **CLI flag forms**: test the other spellings of flags, beyond
  `--log-level`, as `docs/cli-black-box.md` (HAR-08) asks.
- **Progress tracing for live runs** with OpenTelemetry: one span per test,
  phase and forge job, so a slow or failed run can be read without its log.
- **Known CVEs in runtime images**, waiting for upstream fixes: Bouncy Castle
  in the Android command-line tools, and libxml2 in Debian 13.
- **Report upstream** that a Maven multi-module scan checks sibling modules it
  should not.

## Rule for live tests

Never in live tests: Maven Central, npmjs, cloud KMS, or any write to a public
transparency log (Rekor). Live tests sign with `transparency: none`.
