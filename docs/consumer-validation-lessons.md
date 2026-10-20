# Reusable lessons from native consumer validation

These are engineering invariants and troubleshooting guidance, not a claim that
every consumer/provider combination has passed. Keep native run IDs, exact
engine/consumer revisions and output checks in the acceptance evidence for the
particular run. Local success, skipped checks and source-only fixes do not replace
native acceptance.

## 1. Freeze the engine before parallel consumers

- Use one authorized hosted organization session and one engine revision for a
  batch. Finish engine/image preparation before starting consumers.
- `FORGE_CI_LIVE_ENGINE_MODE=prepare` prepares the engine without running a
  consumer; `reuse` checks the prepared engine read-only. Prepare every required
  image platform before reuse, and keep the engine frozen until children finish.
- Isolate repository identity by consumer, visibility, event **and workflow**.
  Unsigned and test-signed development workflows can otherwise collide.
- Supply multi-image verification as one comma-separated `image=` selector.
  Repeating that setting used to retain only the last image, despite successful
  builds of every image. `TestParseGitHubTrialSettings_RejectsRepeatedImageSelectors`
  prevents that silent coverage loss; assert the expected image count in evidence.
- Bound concurrency and wait for every child, including its cleanup, before the
  outer session exits. Keep one log and exit status per case; continue independent
  cases after a failure.
- Treat cancelled runs as terminal results. A one-dispatch disposable consumer
  has no guaranteed replacement run; skipping cancellation can stall cleanup
  and the entire next batch until a controller timeout.
- Local forge trials share fixed resource coordinates and a persistent account
  lock. Parallel hosted work does not justify bypassing that local lock.
- A multi-release fixture must seed a bare GitLab branch only once. Read the
  branch before creating the initial README; preparing subsequent tags must not
  recreate that file or mutate the existing commit. A rejected branch read must
  stop before writes. Regression: `TestPrepareGitLabTagsSeedsOnlyTheFirstRelease`
  in `internal/livetest/scratch_internal_test.go`.
- A consecutive GitLab release needs a new admitted candidate. The generated
  release commit carries `[skip ci]`; signing the next request directly at that
  commit produces a skipped pipeline, not a second release. Reuse the protected
  merge-request path already exercised by warm-base acceptance, without changing
  branch protection or the release bot's commit message. The native npm trial
  first exposed this after 2.0.1 passed manifest/lockfile/package/SBOM checks;
  pipeline 347 for 2.0.2 was skipped. A skipped request cannot count as acceptance.
  With the next-candidate path, both versions pass public/private native GitLab
  acceptance on controller `db3e21e` (parent pipelines 359/364 and 370/375).

## 2. Preserve the committed source tree

The hosted renderer exports committed source into a fresh repository. A tracked
file can still match `.gitignore` (Cargo `src/bin` is one example). Ordinary
`git add` in the fresh repository silently loses such a file. Force-stage only
the committed archive and rendered fixtures, never arbitrary uncommitted files.

Regression: `TestGitHubConsumerPreservesTrackedIgnoredSource` in
`internal/livetest/githubtrial_internal_test.go` also checks that uncommitted,
ignored local data does not enter the snapshot.

## 3. Validate workflow-to-CLI and workflow-to-workflow contracts

- Required CLI values must use their declared flags or environment sources;
  a shell positional value is not equivalent. The snapshot SBOM resolver needs
  `config expand-sboms --value`, plus the expected CI step outputs.
- Forward `forge-ci-binary-ref` through every reusable workflow boundary.
  Otherwise a branch/fork test can silently use a release default or fail to
  locate it, despite the outer workflow selecting another engine.
- Run the actual workflow step through the real CLI where feasible. A flag-name
  inventory alone cannot detect a stray positional argument.
- Pin tools consistently across runners: the Forgejo git-cliff installer must
  support the core's `--offline` behavior, just as the runtime-image installer
  does. Update version and both architecture checksums together.
- Parse YAML before a live trial. Shell quotes inside a YAML plain scalar do not
  escape `: `; use a block scalar for commands containing such text.
- GitLab dotenv outputs normalize keys to uppercase snake case. A script that
  reads `image=`/`digest=` instead of `IMAGE=`/`DIGEST=` can fail after a successful
  registry push. Exercise hand-off scripts with bytes from the actual provider
  sink; `TestGitLabImageDemosConsumeActualDotenvOutputs` covers the npm, Maven and
  Cargo image demos without invoking a container engine or registry.
- Declare the release-file manifest once, after the final unsigned assembly.
  An intermediate package job can upload a digest-bound payload without declaring
  the final manifest. An image job can verify that payload, add its ledger, then
  declare and upload the completed hand-off. Preserve the write-once guard rather
  than deleting an earlier manifest to redeclare it.

Regressions: `test/forgejobehavior/snapshotsboms_test.go`; renderer archives are
checksum-verified by `internal/app/toolchain/changelogrenderer.go`.

## 4. Gate build failures and verify what was packed

- Configured npm test failures must stop before build, SBOM and pack. An absent
  test script is a supported explicit skip, not a successful test execution.
- Count executed, ignored and no-source tests separately. Running the same suite
  in two lifecycle phases does not create additional unique tests.
- Test a clean install or deployment from the **packed artifact**, outside the
  source tree. A source checkout can mask missing OpenAPI/configuration assets,
  wrong working-directory assumptions and incomplete Nuxt/Nitro packages.
- A CLI's reported version must come from its installed metadata, not a stale
  literal. A versioned JAR must be exercised, not assumed runnable because an
  unversioned companion works.
- Runtime containers should reuse that tested output rather than rebuild it.
- Extracted binaries must be nonempty before the naming handoff mutates any
  files. A regular zero-byte file previously passed the shared extraction
  preflight. FC-15 rejects it as invalid input; the regression
  `TestSuffixExtractedBinaries_EmptyBinaryRefusesBeforeAnyRename` first reproduced
  acceptance and now proves the valid sibling remains untouched on refusal.
  This is a source-reproduced core defect, not evidence that a particular native
  release shipped an empty executable.

Regressions include `TestNPMReleaseBuild_FailingTestsStopBeforeBuildSBOMAndPack`
and `TestNPMReleaseBuild_MissingTestScriptIsReportedAsSkipped` in
`internal/app/build/npmreleasebuild_test.go`.

## 5. Diagnose toolchain defects without rewriting healthy dependencies

A native SBOM failure can be a package-manager bug rather than an application
lockfile defect. Compare fresh installs with an identical lockfile and controlled
tool versions. One observed npm11.6.1 optional-WASM dependency tree failed
`npm ls`/CycloneDX; npm11.13.0 succeeded with the unchanged lockfile.

Record the executed tool version. A configuration field or summary value is not
proof that a runtime actually selected that version. Unsupported engine warnings
remain a compatibility gap even when a build happens to pass.

FC-17 corrects the GitHub npm build path that ignored the artifact's selected
Node version and hardcoded Node24 in its summary. Stable/snapshot build stages
now forward the optional version to `build-npm`, which uses the existing pinned
setup-node action before the npm lifecycle and records `node --version` in its
summary input. Empty preserves a caller-supplied runtime image's toolchain.
Automatic package-manager caching is disabled rather than adding an implicit
cache to this path. Provider-free forwarding/order/actual-version-output checks
and native web acceptance must cover this change before calling the Node26 gap
closed; job images still determine toolchains on other delivery roads.

Native web runs `39393996433` / `39393999339` selected Node26.11.1 correctly but
proved a missing shared runtime dependency, `libatomic.so.1`. The common runtime
now includes `libatomic1`, with a real loader-library smoke check during image
verification. Version recording also explicitly fails if `node --version` fails;
a command substitution inside printf had previously masked that failure and
written an empty version. The runtime retry passes public/private stable web
runs `39403245605` / `39403250036`.

GitLab's key-free version-preparation job also needs its build image's tool
environment. Resetting it to the signer's fixed PATH hid Cargo in the standard
Rust image: the bump warned, left Cargo.lock unchanged and the signer correctly
refused the incomplete patch. FC-19 retains the tool environment for that key-free
job; public/private consecutive Cargo fixture releases now pass. Require named
checksum/provenance bundles, not an arbitrary minimum count: container-first
releases can carry their SBOM as image evidence instead of a standalone asset.

Scoped tool installation does not waive exact-version validation of the complete
mise config. Pin every declared version; the selected subset only narrows installs
and required backend runtimes, not the immutable-pin policy.

## 6. Keep reproducibility inputs explicit

- Maven projects need a versioned `project.build.outputTimestamp` or a deliberate
  SCM-epoch integration. A new wall-clock timestamp on every build defeats
  reproducibility. Compare clean main/source/Javadoc outputs with the same pinned
  toolchain; do not infer equality from a property check alone.
- Modern Buildah treats inherited `SOURCE_DATE_EPOCH` as a competing timestamp
  mode. When supplying `--timestamp`, remove only that inherited environment
  value from the child. Preserve explicit caller build arguments; a conflicting
  explicit source-date-epoch argument still needs a clear configuration decision.

Regressions: `internal/adapters/buildah/timestamp_test.go` and the native
`TestBuildTimestamp_InheritedEpoch_WithRealBuildah` integration check.

## 7. Retain security findings and distinguish their causes

- A PR diff scan is not a full current-tree or history secret scan. Retain
  unresolved baseline/history findings with file/rule metadata; never print
  credential values or label them harmless test data without review.
- A static scanner may not resolve inherited policy. Verify the actual preset
  before calling a Renovate update-age finding a missing-policy defect. Trial
  compatibility overrides are not automatically production recommendations.
- Exported mobile entry points can be intentional. Review the reachable actions
  and callback validation; do not break launch/deep links just to silence an audit.
  Any exemption should name the exact rule and documented reviewed scope.
- Normalize ANSI sequences before credential-key redaction. Use a fixed byte
  locale for that regex processing; color escapes and locale collation can
  otherwise defeat redaction or crash the logger.
- Do not lower a vulnerability threshold to accept an old image. Update the
  pinned base and remove unused package managers/build tooling. Report remaining
  unfixed findings accurately even when the existing gate permits them.

Locale/redaction regressions live in `scripts/ci/livescript_test.go`.

## 8. Treat mobile setup and test lifecycle as real dependencies

- Pin and verify generation/lint tools. Approve only reviewed package plug-in
  identities, source URLs and locked revisions; do not globally skip validation.
- An availability guard cannot make a symbol exist in an older SDK. Adapt to the
  declared SDK and preserve UI behavior; keep source compatibility findings
  separate from package-download and signing failures.
- Tests must follow the actual callback/state contract. Finite scripted async
  streams need an explicit finish; cancellation fixtures intentionally stay open.
- Consumer-owned inspectors must declare their interpreter and standard-library
  dependencies. A minimal Python runtime need not contain XML support.
- Unsigned archive/debug-key success does not prove production signing, device
  camera behavior or store distribution.

## 9. Publish is not install or privacy acceptance

Verify package resolution from a fresh cache before cleaning the disposable
repository. Bind downloaded POM/JAR bytes to what the resolver installs, and run
a separate consumer without the registry token. Private anonymous denial must be
checked at the same paths that authenticated access successfully reads.

GitHub Maven requires authentication for public packages too. A successful upload
or release asset is not an anonymous-public-install test. On failures, expose
only controlled status/reason diagnostics, not request headers, environment dumps
or private redirect URLs.

A registry download may return a redirect. Keep automatic credential forwarding
disabled; bounded manual HTTPS storage redirects must discard all initial request
headers. Reject login redirects rather than treating an HTML response as an
artifact, and keep anonymous privacy probes independent of authenticated redirects.

Private image verification runs on the controller host after the workflow. Its
registry login is separate from the workflow's package permissions. Forward the
existing isolated `DOCKER_CONFIG` into supervised controller processes; a fully
successful release can otherwise fail its host-side verification as anonymous.

The shared hosted consumer harness now pairs successful private image
verification with a manifest probe using the existing credential-free OCI
adapter. It also pairs each private release asset's authenticated content GET
with an anonymous GET of that exact API coordinate. Only HTTP 401/403/404 is
denial evidence: public success, redirects, TLS/network failures and HTTP 500 do
not pass. Inert registry/API tests cover these distinctions. These new checks
need a controller containing `githubprivacy.go`; an older controller's native
pass does not establish anonymous image/asset denial.

### FC-14: preserve Maven lifecycle attachment profiles

**Owner:** Forge CI, Maven forge-package publication. A library build activated
its project-owned `maven-profile`, but the GitHub package publisher invoked plain
`mvn deploy -DskipTests`. Source/Javadoc JARs already downloaded into `target`
were not attached to that new Maven session; the registry could therefore contain
the main JAR and POM while the release assets contained all three JARs.

**Reproduction:** deploy a library with profile-owned source/Javadoc executions
to an isolated file repository, first without the profile, then with it, then
without it while leaving the prebuilt classifiers in `target`. Only the profiled
deploy publishes all three JARs. This is a shared publication defect, not proof
of a package permission problem or a missing consumer attachment configuration.

**Fix:** stable and snapshot GitHub forge-package stages forward the library
build's `maven.maven_profile` (default `central-release`), through the reusable
publisher's optional `profile` input and `MAVEN_PROFILE`, to the CLI's `--profile`
and Maven argv. Snapshot effective-model validation receives the same profile.
Application defaults remain empty. The distinct Maven Central `config.profile`
setting retains its existing purpose. GitHub Packages has no GPG-secret contract;
its job skips project GPG execution just as the library build does, while the
separate release-signing boundary and Central policy retain their own contracts.

**Repeatable checks:** `TestForgePackagesDeploy_ActivatesThePublicationProfile`,
`TestMavenPackagePublisherProfileThroughTheEngine`, and
`TestMavenPackageStagesKeepTheLibraryBuildProfile` cover argv, real workflow-step
execution through the CLI, and reusable-workflow propagation. The existing live
`TestInRunner_ForgePackages_MavenDeployReachesTheRegistry` now compares registry
main/POM/source/Javadoc bytes against a receipt frozen before deployment and
inspects the three archive contents. See [the testing guide](testing.md#maven-format-publication-evidence)
for the guarded invocation and exact evidence boundary.

**Native evidence (2026-10-20):** focused run `20261020T060923Z-1999907`
passed both GitLab and Forgejo with all four hashes matching. It exercised the
uncommitted working tree based on `de18a8b532d43ea53ebc29f217331d87767dc8e2`,
not that base commit alone. The preceding run `20261020T060315Z-1993868` passed
Forgejo but exposed a fixture mistake: GitLab `CI_JOB_TOKEN` cannot use the project
uploads API for release attachments. Retaining its receipt as a job artifact
corrected the fixture without introducing a broader token or changing publisher
permissions. These results are local native lifecycle/registry evidence only.

Maven deploy still runs the project lifecycle. Profile propagation fixes missing
attachments; it does **not** universally guarantee build-to-published byte identity.
Keep per-consumer four-file comparisons, fresh-cache resolution and same-path
private anonymous denial as separate acceptance oracles. Hosted GitHub acceptance
must use an engine revision containing this change; earlier main-JAR-only install
passes do not establish classifier completeness.

### FC-16: publish the original Maven library build, not another build

Hosted run `39381824726` built and published successfully on profile-fix engine
`8b38ec96cee886689d322553cdac61f86aa78205`, then the stronger consumer gate
proved that the registry main JAR differed from the original build-job artifact.
Private retry `39383146380` reproduced the same drift after its independent
artifact-name fixture correction. Profile forwarding alone cannot close this.

The shared Maven publisher now has an explicit `--from-build` library path.
Stable/snapshot library stages select it; the build handoff includes project
POMs as well as JARs. Go stages those unchanged bytes into the **existing** Maven
repository format, validates the complete set, writes existing checksum sidecars,
and uses the existing stable uploader or native snapshot deploy-file adapter.
Stable publication no longer installs a JDK or executes the consumer project.
The legacy lifecycle path remains explicit for callers that select it.

The initial prebuilt scope is literal jar/pom reactor coordinates, explicit module
lists, standard `target` outputs, required main/source/Javadoc JARs, and additional
standard classifier JARs. Parent group/version inheritance and simple project
finalName substitutions are supported. Profile-selected reactor modules and
unresolved custom finalName values refuse rather than guessing a partial export.
Project POM bytes are retained, not replaced with an effective POM that could
contain environment-expanded properties.

Existing native Maven conformance now exercises this path: run
`20261020T071017Z-2054031` passed GitLab and Forgejo with all four independent
registry downloads exactly matching the pre-publication receipt. Focused reactor,
snapshot, classifier, malformed/missing/wrong-version and workflow/real-CLI
regressions pass. Hosted artifact-first acceptance still needs a newly frozen
engine; the failed older-engine receipts remain failures.

Hosted artifact-first acceptance now passes public/private stable `2.7.6` on
frozen engine/controller `9e5b27152168e1c7ebe5f366501fd28f97f1fa8c`: runs
`39388373461` / `39388374482`. Both original-build comparisons and real fresh-cache
main/POM/source/Javadoc resolution succeeded, followed by the independent
credential-free MAC0/wrong-key consumer. The private checker proved anonymous
denial at all four registry paths; the shared controller separately paired
authenticated content reads with anonymous denial for all ten release assets.
This closes the tested Maven forge-package contract, not Sonatype production
publication or every custom Maven reactor/profile layout.

### FC-18: SARIF automation IDs must encode an actual analysis category

The positive-ingestion canary uploaded and processed a known benign report on
engine4248579, but GitHub analysis `1987278029` at commit
`de567fbc57a26ea14221b0f4b227d199898cbb25` reported an empty category. The shared
formatter emitted `automationDetails.id = category` for one run. GitHub defines
this as `category/run-id`: with no slash the entire value is a run ID and the
category is empty. The authoritative interpretation is documented in
[SARIF support, runAutomationDetails](https://docs.github.com/en/code-security/code-scanning/integrating-with-code-scanning/sarif-support-for-code-scanning#runautomationdetails-object).

Generated IDs now end with `/`: `category/` for one run, `category/index/` for
multiple runs. Collision avoidance reserves the actual category component, even
when a producer supplies its own nonempty run ID; producer-owned IDs themselves
remain unchanged. Regression tests first reproduced the wrong category under
GitHub's parser rule and now verify correct hierarchical categories, distinct
multi-run categories, producer identity preservation and numeric-data integrity.
Native exact-category/commit/alert acceptance must pass on the corrected engine
before closing the positive-ingestion gap. Upload success alone is insufficient.

### Prioritized acceptance gaps

Rust's downloaded-executable inspector exposed BFF byte drift in the documented
second-build export mode (public run `39383143140`; the private sibling also
failed the same comparison). The worker and keytool downloads already matched
and passed offline checks. Do not call the older target-based contract a promise
of image-to-asset identity, and do not infer that every Rust executable was
accepted after the inspector stopped at BFF.

The shared core now provides an explicit `extract.binary.paths` mode and
`container layout extract-files`: copy selected regular files from the existing
verified, scanned image layout instead of recompiling an export stage. This
reuses the OCI blob verifier and upstream layer/whiteout handling. The mode is
provider-independent at the CLI; its current shared workflow integration is the
retained-layout GitHub path. Core file/overwrite/path tests, real layered-layout
and corrupt-blob tests, and the actual workflow step through the real CLI cover
the implementation. Native Rust acceptance on an engine containing this new
mode is still required; legacy export-stage build reproducibility remains a
separate application/toolchain finding.

Native Rust0.1.3 now passes public/private `39393995864` / `39393998582` on
`42485794a7c7a82bf2b9a85ffd7ac679dd23cf05`: all four actual downloaded binaries
match their released runtime-image bytes, all four pass non-root/offline loader
checks, and keytool/load CLI help passes. The private controller also proves
authenticated success plus anonymous denial for all twelve release assets and
all three image references. Worker/BFF service startup and hardware HSM behavior
are still outside that binary-inspection claim.

These are explicit follow-up scenarios, not passing coverage. Extend the existing
`internal/livetest` consumer/conformance suites and consumer-owned inspectors.

| Priority | Missing oracle | Existing place to extend |
| --- | --- | --- |
| P0 | Download every Rust release executable; inspect architecture/loader and run bounded safe help in its intended runtime | Hosted consumer release downloads and Rust consumer inspector |
| P0 | Actual consumer main/POM/sources/Javadoc equality, fresh-cache install and same-path private denial | Maven consumer package acceptance; native Maven registry scenario above |
| P0 | Private image/assets/package authenticated success paired with anonymous denial of identical coordinates | Existing image, release and package readback helpers |
| P0 | Cross-job tampered artifact/bundle, stale handoff and wrong provenance subject/source/commit/workflow reject for the expected reason | Existing signed-handoff and image-evidence conformance scenarios |
| P1 | Interrupt an image-producing consumer after candidate push and retry the same version with retained remote state | Existing consumer interruption hooks and promotion/rollback scenarios |
| P1 | Consecutive npm/Cargo local releases keep version, lockfile, tag and SBOM consistent | Existing consumer release fixtures; initial-version success is insufficient |
| P1 | Independent local GitLab/Forgejo development and Rust container-first development flows | Existing consumer development scenarios |
| P1 | GitHub SARIF upload finishes processing and is queryable for the expected commit/category | Existing security-report upload scenario; optional upload success is insufficient |
| P1 | Representative public/private PR, development, package and container-first runs share one final engine revision | Existing prepare/reuse batching; preserve revision-specific prior evidence |
| P2 | Required legacy SBOM ZIP/GPG sidecars and Android AAB streaming-verifier compatibility | Consumer output parity and Android artifact inspectors |

## 10. Preserve a reusable handoff

For every issue, retain: owning project, original revision, affected paths,
reproduction, proposed fix, changed trial revision, local checks, native run IDs,
remaining limits and whether the change is production-relevant or lab-only.
Keep upstream source/legal attribution. Preserve earlier failed evidence rather
than rewriting it as success, and retain temporary proof outside ephemeral
directories. Use the resulting matrix to drive the next run, not session memory.
