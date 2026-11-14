# Testing matrices

These tables say what Forge CI is proven to do live, on which forge, and
through which demo. Every cell the product offers should be filled, and an
empty cell names its reason (a product gap, a demo gap, an owner decision or a
deliberate exclusion).

Matrix 1 crosses each demo, standing for a project kind, with the flows the
orchestrators offer. Matrix 2 lists the whole-flow `lifecycle` event once per
variant axis value (ecosystem, route, images, signing, image builder,
visibility, enforcement, architecture), plus the kept scenario lines.

- The tables are generated from the Go case tables, the demos' declarations
  and the demo admission; a test fails when the committed tables are stale.
- Expected outcomes come from the planner, never written per case. The live
  marks are a hand-kept overlay in `test/live/evidence.txt`.
- Every forge runs amd64; arm64 is cross-compiled and never emulated in a
  release.

## How the tables are made

Matrices 1 and 2 are generated from one source by `just gen-testing-matrices`
(`livetest.RenderTestingMatrices`): the shipped demos' declarations as the
planner and the demo admission read them, the cases and lanes of the Go
tables, and the live marks of `test/live/evidence.txt`, a
hand-kept overlay keyed by forge, demo and flow, since whether a run proved
something is a judgement; `go run ./test/live/internal/livetest/preflight
testing-evidence ~/.local/state/forge-ci/live-runs` drafts the lab forges'
lines for review from the recorded verdicts; a hosted GitHub trial's line is
written by hand from its log. A refusal carries the planner's or the
admission's reason. `TestTestingMatricesInSync` in
`test/live/internal/livetest` fails when the committed tables are stale.
No prepared lane runs on hosted GitHub. There a
line is the fixture workflow a demo ships under `test/live/github/workflows`
for the event the hosted trial settings name, and the lifecycle has one when
the demo ships all three and the hosted settings accept the lifecycle.

Legend: ✅ live-proven · ◐ partial · ⏳ running or queued · ❌ failed, root fix
committed · ⛔ refused by planner admission (reason pinned) · 📝 line exists,
unrun · ⬚ no line · — not applicable. A ✅ that reads "passed on rerun after
an unavailable dependency" is a row the launcher ran twice
([the one rerun of a row](live-runs.md#the-one-rerun-of-a-row)).

### 1 Capabilities by project kind × flow (cells: Forgejo / GitLab / GitHub)

Rows are what a consumer is (the demo that embodies it), columns are the
flows the orchestrators offer. "Artifact-first" builds the snapshot image from
the planned artifacts; "container-first" builds from the declared container.

<!-- generated:project-kinds -->

| Demo (project kind) | PR / MR gate | Artifact-first snapshot | Container-first snapshot | Release | Full lifecycle |
|---|---|---|---|---|---|
| hello-android (gradle-android · no container · no image) | ⬚ / ✅ (2026-11-13) / ✅ (2026-11-12, runs 43159891577 and 43160497879) | — | — | — / — / — | ⛔ / — / ⛔ |
| hello-android-library (gradle-android · no container · no image) | ⬚ / ✅ (2026-11-13) / ✅ (2026-11-11, runs 43085301830 and 43086200652) | — | — | — / — / ✅ (2026-11-12, runs 43134153965 and 43135583711, 3 GPG signatures verified against the trial key) | ⛔ / — / ⛔ |
| hello-base-images (meta · no container · no image) | ⬚ / ⬚ / ⬚ | — | — | ✅ (2026-11-13) / ✅ (2026-11-13) / — | ⛔ / ⛔ / ⛔ |
| hello-cargo-cli (rust · no container · no image) | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-12, runs 43147401452 and 43148004422) | — | — | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-12, runs 43126466168 and 43127750273, 3 Cosign bundles verified against the trial key) | ⛔ / ⛔ / ⛔ |
| hello-cargo-container (rust · container-first · one image) | ✅ (2026-11-14) / ✅ (2026-11-13) / ✅ (2026-11-10, run 42848860564) | — | ✅ (lifecycle phase 3, 2026-11-10) / 📝 lifecycle / 📝 lifecycle | ✅ (2026-11-14) / ✅ (2026-11-13) / 📝 | ✅ (2026-11-14) / ✅ (2026-11-14) / ✅ (2026-11-10, release run 42919737094) |
| hello-cargo-workspace (rust · container-first · 2 images) | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-10, run 42849993300) | — | ✅ (2026-11-13) / ✅ (2026-11-13) / ⛔ | ✅ (2026-11-14) / ✅ (2026-11-13) / ✅ (2026-11-10, run 42915679647, 4 Cosign bundles verified against the trial key) | ⛔ / ⛔ / ⛔ |
| hello-go-cli (go · no container · no image) | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-12, runs 43148821568 and 43149415886) | — | — | ✅ (2026-11-14) / ✅ (2026-11-14) / ✅ (2026-11-10, run 42855974665, 6 Cosign bundles verified against the trial key; GPG variant, run 42853659291; on 2026-11-12 the variant with no SBOM bundle, runs 43163726687 and 43164848434, and the one with no changelog, runs 43166247915 and 43167368648) | ⛔ / ⛔ / ⛔ |
| hello-go-service (go · container-first · one image) | ✅ (2026-11-14) / ✅ (2026-11-13) / ✅ (2026-11-10, run 42845340779) | — | ✅ (2026-11-14) / ✅ (2026-11-13) / ✅ (2026-11-12, runs 43159984233 and 43160847885) | ✅ (2026-11-14) / — / ✅ (2026-11-12, the variant with GPG-signed files, runs 43137312566 and 43138850750) | ✅ (2026-11-13) / — / ✅ (2026-11-10, release run 42912107999) |
| hello-gradle (java · no container · no image) | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-12, runs 43144460883 and 43156917809; the private one failed once on a dependency its runner did not find (run 43145103157)) | — | — | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-10, run 42860266897, 5 Cosign bundles verified against the trial key) | ⛔ / ⛔ / ⛔ |
| hello-gradle-service (java · artifact-first · one image) | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-11, runs 43085300596 and 43086150292) | 📝 lifecycle / 📝 lifecycle / 📝 lifecycle | — | ✅ (2026-11-13) / ✅ (2026-11-13) / 📝 | ✅ (2026-11-14) / ✅ (2026-11-14) / ✅ (2026-11-12, release run 43135845817) |
| hello-ios (xcode-ios · no container · no image) | ⬚ / ⬚ / ✅ (2026-11-12, runs 43161227194 and 43162258904) | — | — | — / — / — | ⛔ / — / ⛔ |
| hello-maven (java · no container · no image) | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-12, runs 43143015490 and 43143618148) | — | — | ✅ (2026-11-14) / ✅ (2026-11-13) / ✅ (2026-11-12, runs 43151439440 and 43152520181) | ⛔ / ⛔ / ⛔ |
| hello-maven-reactor (java · no container · no image) | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-11, runs 43087350088 and 43088177855) | — | — | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-11, runs 43102069751 and 43103635645) | ⛔ / ⛔ / ⛔ |
| hello-maven-service (java · artifact-first · one image) | ✅ (2026-11-14) / ✅ (2026-11-13) / ✅ (2026-11-10, run 42842794730) | ✅ (2026-11-13) / ✅ (2026-11-13) / 📝 lifecycle | — | ✅ (2026-11-13) / ✅ (2026-11-13) / 📝 | ✅ (2026-11-14) / ✅ (2026-11-14) / ✅ (2026-11-12, release run 43155392448) |
| hello-meta (meta · no container · no image) | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-12, runs 43150118621 and 43150691588) | — | — | ✅ (2026-11-13) / ✅ (2026-11-14) / ✅ (2026-11-10, run 42831178553) | ⛔ / ⛔ / ⛔ |
| hello-monorepo (java, node · artifact-first · nested artifacts (monorepo)) | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-10, run 42851535452) | ✅ (2026-11-13) / ✅ (2026-11-13) / ⛔ | — | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-11, runs 43108226275 and 43110448315) | ⛔ / ⛔ / ⛔ |
| hello-npm-package (node · no container · no image) | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-12, from a fork, refused before checkout as it must be, run 43163527676) | — | — | ✅ (2026-11-14) / ✅ (2026-11-13) / ✅ (2026-11-11, runs 43104656725 and 43106178358) | ⛔ / ⛔ / ⛔ |
| hello-npm-service (node · artifact-first · one image) | ✅ (2026-11-14) / ✅ (2026-11-13) / ✅ (2026-11-10, run 42846629993) | 📝 lifecycle / 📝 lifecycle / 📝 lifecycle | — | ✅ (2026-11-14) / ✅ (2026-11-14) / 📝 | ✅ (2026-11-13) / ✅ (2026-11-13) / ✅ (2026-11-12, release run 43127999884) |

Outside the live harness's scope, the trial's acceptance and not a product rule:

- hello-cargo-workspace, forgejo: .forge-ci/release-images.yml is not a service snapshot's declaration, so the demo has no development route: the trial judges one container of the root Containerfile and context, planned for amd64, without image name, tag suffix or declared provenance, from at most one artifact
- hello-cargo-workspace, gitlab: .forge-ci/release-images.yml is not a service snapshot's declaration, so the demo has no development route: the trial judges one container of the root Containerfile and context, planned for amd64, without image name, tag suffix or declared provenance, from at most one artifact
- hello-cargo-workspace, github: .forge-ci/release-images.yml is not a service snapshot's declaration, so the demo has no development route: the trial judges one container of the root Containerfile and context, planned for amd64, without image name, tag suffix or declared provenance, from at most one artifact
- hello-monorepo, forgejo: the demo has no root Containerfile, so no service snapshot and no development route
- hello-monorepo, gitlab: the demo has no root Containerfile, so no service snapshot and no development route
- hello-monorepo, github: the demo has no root Containerfile, so no service snapshot and no development route

<!-- /generated:project-kinds -->

### 2 E2E scenarios × variants (lifecycle unless stated)

<!-- generated:e2e-variants -->

| Axis | Variant | Forgejo | GitLab | GitHub |
|---|---|---|---|---|
| ecosystem | go | ✅ hello-go-service (2026-11-13) | ⛔ hello-go-cli | ✅ hello-go-service (2026-11-10, release run 42912107999) |
|  | gradle-android | ⛔ hello-android | — hello-android | ⛔ hello-android |
|  | java | ✅ hello-gradle-service (2026-11-14) | ✅ hello-gradle-service (2026-11-14) | ✅ hello-gradle-service (2026-11-12, release run 43135845817) |
|  | meta | ⛔ hello-base-images | ⛔ hello-base-images | ⛔ hello-base-images |
|  | node | ✅ hello-npm-service (2026-11-13) | ✅ hello-npm-service (2026-11-13) | ✅ hello-npm-service (2026-11-12, release run 43127999884) |
|  | rust | ✅ hello-cargo-container (2026-11-14) | ✅ hello-cargo-container (2026-11-14) | ✅ hello-cargo-container (2026-11-10, release run 42919737094) |
|  | xcode-ios | ⛔ hello-ios | — hello-ios | ⛔ hello-ios |
| route | artifact-first | ✅ hello-gradle-service (2026-11-14) | ✅ hello-gradle-service (2026-11-14) | ✅ hello-gradle-service (2026-11-12, release run 43135845817) |
|  | container-first | ✅ hello-cargo-container (2026-11-14) | ✅ hello-cargo-container (2026-11-14) | ✅ hello-cargo-container (2026-11-10, release run 42919737094) |
|  | no container | ⛔ hello-base-images | ⛔ hello-base-images | ⛔ hello-base-images |
| images | 2 images | ⛔ hello-cargo-workspace | ⛔ hello-cargo-workspace | ⛔ hello-cargo-workspace |
|  | nested artifacts (monorepo) | ⛔ hello-monorepo | ⛔ hello-monorepo | ⛔ hello-monorepo |
|  | no image | ⛔ hello-base-images | ⛔ hello-base-images | ⛔ hello-base-images |
|  | one image | ✅ hello-cargo-container (2026-11-14) | ✅ hello-cargo-container (2026-11-14) | ✅ hello-cargo-container (2026-11-10, release run 42919737094) |
| signing | GPG declaration | ⛔ hello-go-cli | ⛔ hello-go-cli | ⛔ hello-go-cli |
|  | default | ✅ hello-cargo-container (2026-11-14) | ✅ hello-cargo-container (2026-11-14) | ✅ hello-cargo-container (2026-11-10, release run 42919737094) |
|  | gpg | ⛔ hello-android-library | — hello-android-library | ⛔ hello-android-library |
|  | sigstore | ✅ hello-gradle-service (2026-11-14) | ✅ hello-gradle-service (2026-11-14) | ✅ hello-gradle-service (2026-11-12, release run 43135845817) |
| image builder | buildah | ✅ lifecycle-forgejo (2026-11-14) | ✅ lifecycle-gitlab (2026-11-14) | ⬚ no lane |
|  | kaniko | ✅ lifecycle-forgejo (2026-11-13) | ✅ lifecycle-gitlab (2026-11-13) | ⬚ no lane |
| visibility | public | ✅ lifecycle-forgejo (2026-11-14) | ✅ lifecycle-gitlab (2026-11-13) | ✅ hello-cargo-container (2026-11-10, release run 42919737094) |
|  | private | ✅ lifecycle-forgejo (2026-11-14) | ✅ lifecycle-gitlab (2026-11-14) | ✅ hello-cargo-container (2026-11-12, release run 43165254088; its release stalled once on an asset upload (run 43161570088)) |
| enforcement | admin lint | 📝 lifecycle-forgejo | 📝 lifecycle-gitlab | ⬚ no lane |
|  | admin vulnerabilities | 📝 lifecycle-forgejo | 📝 lifecycle-gitlab | ⬚ no lane |
|  | admin application-tests | 📝 lifecycle-forgejo | 📝 lifecycle-gitlab | ⬚ no lane |
|  | admin reproducible-builds | 📝 lifecycle-forgejo | 📝 lifecycle-gitlab | ⬚ no lane |
|  | none enforced | 📝 lifecycle-forgejo | 📝 lifecycle-gitlab | ⬚ no lane |
| architecture | amd64 | ✅ hello-cargo-container (2026-11-14) | ✅ hello-cargo-container (2026-11-14) | ✅ hello-cargo-container (2026-11-10, release run 42919737094) |
|  | arm64 | — D-5 | — D-5 | — D-5 |

Kept scenario lines, by lane:

| Lane | Scenario | State |
|---|---|---|
| build-capability-gitlab | `TestGitLabRelease_BuildImageCapability` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| harness-inputs-gitlab | `TestGitLabRelease_ConsumerBranchProtection` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| harness-inputs-gitlab | `TestGitLabRelease_RejectsPreseededProvenance` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| harness-inputs-gitlab | `TestGitLabRelease_ConsumerInterruptedRecovery` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| interruption-manual-gitlab | `TestGitLabWorkflow_InterruptedJob` | 📝 |
| k3s-private-gitlab | `TestGitLabRelease_MinimalSignedArtifacts` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| k3s-private-gitlab | `TestGitLabCheck_ChecksOutPrivateConsumerWithJobToken` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| k3s-private-gitlab | `TestGitLabCheck_ChecksOutTheConsumerPipelineCommit` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| k3s-private-gitlab | `TestGitLabPolicy_ConsumerCannotReplaceTheSigningPipeline` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| k3s-private-gitlab | `TestGitLabPolicy_JobTokenCannotWriteThePublicationRegistry` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| k3s-private-gitlab | `TestGitLabRelease_MinimalBranchProtection` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| k3s-private-gitlab | `TestGitLabRelease_ResourceGroupSpansTheChild` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| k3s-private-gitlab | `TestGitLabRelease_PromotionRetryAfterRegistryWrite` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| k3s-private-gitlab | `TestGitLabBootstrap_PublishesAndDownloadsTheRecordedBinary` | ✅ (2026-11-13, V 20261113T195248Z-1081137) |
| recovery-forgejo | `TestForgejoRelease_InterruptedRecovery` | ✅ (2026-11-14, V 20261114T011738Z-1069575) |
| trust-refusal-forgejo | `TestForgejoRelease_RejectsPreseededProvenance` | ✅ (2026-11-14, V 20261114T011738Z-1069575) |
| forgejo17-forgejo | `TestInRunner_NativeStepSummary` | ✅ (2026-11-14, V 20261114T011738Z-1069575) |
| forgejo17-forgejo | `TestForgejoRelease_RerunAfterInterruption` | ✅ (2026-11-14, V 20261114T014333Z-1342205) |

<!-- /generated:e2e-variants -->

