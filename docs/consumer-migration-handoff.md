# DiggSweden consumer migration handoff

Status reconciled on **2026-10-20**, during combined-engine contract acceptance.

Latest frozen engine/controller: `77a9c3403542c46a75c4665ecd22bc65f3c47f08`.
Public/private Node26 web stable0.2.4 passes (`39403245605` / `39403250036`),
including image verification and private exact-coordinate anonymous denial.
Private Maven2.7.8 passes (`39403249538`); positive SARIF processing, category,
commit and known-alert acceptance passes (`39403232350`). Independent web
development passes public/private (`39405582859` / `39405582564`): five artifact
archives each verified, main unchanged, no stable tags/releases. Public Maven
also passes (`39405597442`), including installed-package acceptance and five
verified Cosign bundles. All seven consumer cases finished successfully; the
guarded status route confirms no hosted GitHub session remains.
Rust retained-image extraction already passed on engine4248579, runs
`39393995864` / `39393998582`: all four downloaded executables match their
retained runtime images, with private asset/image denial checked.
After operator network recovery, native GitLab artifact handoff passes and
consecutive npm releases **2.0.1 → 2.0.2 pass public/private** on controller
`db3e21e`. Public parent/child pipelines:359/360 then364/365; private:370/371
then375/376. Each release verifies three Cosign bundles and agreement between
signed manifest/lockfile, published package and Build SBOM versions. Follow-up
MR gates368/379 also pass. The harness reuses the existing protected-MR
next-candidate path rather than requesting a release at a `[skip ci]` commit.
Earlier IPAM failures352/355 are historical, not a current blocker.
Native promotion retry also passes: pipeline380 retains the failed job787's
journal after a real registry write; clean-job retry790 converges to the expected
digest while publication/cleanup remain blocked during the injected failure.
This proves the existing recovery primitive, not whole-consumer interruption.
Consecutive container-first Cargo fixture releases **0.1.1 → 0.1.2 pass
public/private** on `fddc70e`: parent/child `399/400` then `404/405`, and `410/411`
then `415/416`; follow-up MR gates `408/419` pass. Each release checks the committed
Cargo version/lockfile, required release bundles and final image attestations.
Native cross-job rejection also passes for stale digests, changed artifact bytes
and changed bundle bytes, requiring the actual CLI's digest-mismatch reason.
Signed-image registry checks now also pass on GitLab and Forgejo: matching
evidence is accepted; wrong source, commit, workflow, trust key and subject are
rejected for their intended reasons. The wrong-subject envelope is independently
verified as cryptographically valid before subject enforcement rejects it.
Release-file cryptographic rejection also passes public/private on Forgejo
(runs `128/129`, SHA-1/SHA-256 consumers) and GitLab (parent/child `421/422`
and `427/428`): changed downloaded content/signatures and wrong public keys fail
signature verification after the original bytes verify successfully.
Full consumer job-boundary identity cases remain open.
Consecutive Forgejo npm 2.0.1→2.0.2 now passes public `132/133` and private
`134/135`, reusing the repository and signing authority within each trial.
Each version's signed manifest/lockfile, downloaded package and Build SBOM agree.
The overall migration and cleanup remain incomplete.

## Bottom line

**Substantial implementation and native acceptance are complete; the migration
is not finished, upstreamed or residue-free.** Passing rows below concern the
named trial flows and revisions, not every capability of each application.

- Shared fixes and executable regressions are now committed locally with signed,
  signed-off Conventional Commits: `c00b38f` (core release contracts) and
  `7f06705` (native acceptance). The documentation accompanies this checkpoint.
  These source commits have not been pushed.
- Commit-checkpoint verification: full `go test -race ./...` passes in both
  Forge CI and Forge Lab (with each repository's pinned tools on PATH).
  Forge Lab has no pending source changes and needs no new commit. These
  provider-free suites do not replace the remaining guarded native acceptance.
- Consumer changes are in isolated, version-controlled trial copies. Original
  DiggSweden repositories have not been modified. These are upstream change
  candidates, not merged fixes.
- Evidence spans several frozen engines and later working-tree fixes. One final
  combined-engine acceptance checkpoint is still required.
- The hosted contract batch and recovered local consecutive-release validation
  finished successfully. No hosted session remains. Package/image residue still
  needs exact-resource cleanup.

## What has passed

| Consumer | Native trial evidence | Important boundary |
| --- | --- | --- |
| Java service, Java CLI, Java library, API linter, web | Public/private PR or MR gates and stable release flows on GitHub, GitLab and Forgejo; consecutive API-linter npm releases on GitLab and Forgejo | npm registry installation and remaining provider development flows remain open |
| Container-first Cargo fixture | Public/private consecutive GitLab releases 0.1.1→0.1.2, bundles, image attestations and MR gates | Built-in demo fixture; does not establish the actual Rust application's local-forge acceptance |
| Android | Full app/camera PR or MR checks on all three forges; GitHub unsigned and test-signed development builds | Local stable/development, real devices, production signing and store delivery are not established |
| iOS | Public/private GitHub ARM64 simulator PRs and unsigned development archives | No physical device, Secure Enclave or App Store acceptance |
| Rust services | Public/private GitHub PRs and stable releases; all three images per visibility independently verified; all four downloaded executables match retained runtime images and pass bounded loader/help checks | Local-forge PR/stable/development coverage remains open; no HSM/service-startup claim |
| Maven artifact-first publication | GitLab/Forgejo fixture four-file readback, plus real GitHub public/private library original-build identity, fresh-cache classifiers, MAC0 smoke and private denial | Hosted2.7.6 closes the named forge-package contract; Sonatype production and every custom reactor/profile remain separate |

Useful immutable checkpoints:

- Published shared engine: `2f10cb7e8032b61a1d817743a2db195698b5dade`.
- Hosted cancellation-aware controller: `aaba23e48b4f8bacbfe90bfaa18b2877cbe6de41`.
- Local renderer: `895b132`; later source fixes are not implicitly in this engine.
- Maven native profile run: `20261020T060923Z-1999907`, working tree based on
  `de18a8b532d43ea53ebc29f217331d87767dc8e2`, **not that base commit alone**.
- Maven main-JAR installed-consumer GitHub runs: public `39315849447`, private
  `39315861486`. These precede classifier completeness acceptance.
- iOS native PRs: `39328005798` / `39328006834`; package 164 passes plus one
  expected failure, app 51 passes, zero unexpected failures/skips.
- Rust stable runs: `39373540707` / `39373543550`; all six distinct image tags
  independently verified. Multiple signatures are not additional images.

## Fix ownership: shared tooling

All entries below belong in **Forge CI**, with tests in the existing application,
adapter, workflow-behavior or live-conformance tiers. Do not copy engine defects
into each consumer as workarounds.

| Issue | Implemented change | Source area / retained check |
| --- | --- | --- |
| FC-01 | Failing configured npm tests stop build/SBOM/pack; absent tests are explicitly skipped | `internal/app/build/npmreleasebuild*` |
| FC-02 | Remove only inherited competing `SOURCE_DATE_EPOCH` when supplying Buildah's pinned timestamp | `internal/adapters/buildah/`; argv and native reproducibility checks |
| FC-03 | Snapshot SBOM resolver supplies the required `--value` flag | Snapshot publisher workflow; actual step through the real CLI |
| FC-04 | Forward the selected engine binary revision to the package publisher | Stable publisher workflow; workflow-boundary regression |
| FC-05 | Align pinned Forgejo git-cliff to the core's `--offline` support | Render-changelog action version and both architecture digests |
| FC-06 | Preserve tracked ignored files during committed-source export; exclude uncommitted data | `internal/livetest/githubtrial*` |
| FC-07 | Normalize ANSI and use byte-locale credential redaction | `scripts/ci/run-live-tests.sh` and inert logger regressions |
| FC-08 | Update runtime Node/npm to the verified combination fixing the optional-WASM SBOM failure | Runtime Containerfile; unchanged consumer lockfile comparison |
| FC-09 | Prepare engine once; use read-only reuse and distinct consumer/visibility/event/workflow identities | Hosted trial orchestration and regressions |
| FC-10 | Treat cancelled hosted runs as terminal immediately | `internal/livetest/githubtrial*` |
| FC-11 | Seed an absent GitLab main branch only once across multiple release tags | `internal/livetest/scratch*`; two-tag and rejected-read cases |
| FC-12 | Consume GitLab's actual uppercase `IMAGE`/`DIGEST` dotenv names | npm image demo; shell handoffs driven with real provider sink bytes |
| FC-13 | Reject repeated `image=` selectors rather than silently checking only the last | Hosted settings parser; single CSV selector regression |
| FC-14 | Forward the library publication profile through stable/snapshot workflows, CLI, snapshot model preflight and Maven deploy | `internal/app/publish/forgepackages*`, CLI and workflow regressions; native four-file readback |
| FC-15 | Reject a zero-byte extracted binary before renaming any sibling | `internal/app/container/binaries*`; reproduced acceptance first, then full container application race suite passed with the preflight fix |
| FC-16 | Publish retained Maven build bytes rather than rebuilding during publication | `internal/app/publish/mavenbuild*`; original-build/fresh-cache native checks |
| FC-17 | Forward configured Node version, retain actual runtime version and supply libatomic for Node26 | npm reusable workflows and shared runtime; public/private native web stable0.2.4 |
| FC-18 | Render SARIF automation IDs using GitHub's category/run-ID convention | Security domain/application tests; processed known-alert native acceptance39403232350 |
| FC-19 | Preserve the build-tool image's environment in key-free GitLab version preparation; the standard Rust image's Cargo lives outside the signer's fixed PATH | `templates/release-jobs.yml`; role-credential behavior tests and native consecutive Cargo releases |

Related working-tree fixes cover Android's default test execution and task/SBOM
boundaries, exact scoped toolchain selection, scan diagnostics, private host image
authentication, signed-handoff and image-evidence checks. Some surrounding changes
predate this handoff; review the diff rather than attributing every modified file
to the latest Maven change.

**Forge Lab:** operational provisioning and target refreshes were required, but
no new Forge Lab source defect is established by those incidents. Do not invent
a lab code fix for a stale credential, missing tool or consumer fixture mistake.

## Fix ownership: DiggSweden projects

Each trial retains its own `test/live/MIGRATION.md` and, where applicable,
package/reproducibility/output-compatibility notes. The table separates code
changes worth reviewing upstream from lab-specific adaptations.

The project ledger now estimates the **CI-only GitHub adoption patch** at roughly
2–4 caller/config files per project, or 3–5 for iOS. Adding GitLab or Forgejo is
roughly 3–6 small caller/config/build-entry files per provider. These are planning
ranges, excluding lab fixtures and separately reviewed application fixes below;
they are not measured final patch counts. GitHub keeps the reusable-workflow
model, with bounded ref/input/trigger/config adjustments.

| Original project | Changes to review upstream | Evidence and remaining decisions |
| --- | --- | --- |
| `wallet-provider` | Migrate obsolete artifact/publisher configuration; align a supported non-root Java runtime; use fixed, aligned Jackson BOM versions until the platform manages them | 99 tests and native service matrices; revalidate chosen runtime/dependency pins at adoption time |
| `wallet-dss-cli` | Produce an executable versioned JAR and preserve its unversioned executable alias using the existing assembly setup | Alias changes `78b6477` / private `dd6a62e`; actual downloads, runnable/byte-identical JARs and all three public/private stable matrices pass |
| `cose-lib` | Preserve Java 21 API bytecode; add a versioned reproducible output timestamp; reconcile README coordinates and duplicate compiler properties; retain main/source/Javadoc publication | Java 21/25 tests, installed MAC0/wrong-key consumer, hosted classifiers and actual-library publication identity pass; Sonatype production publication is not proved |
| `rest-api-profil-lint-processor` | Read installed package metadata for CLI version; include API resources; resolve paths relative to the package; restrict local reference traversal to own data properties | 308 tests/23 suites, clean packed installation and consecutive public/private GitLab/Forgejo releases pass; published npm install/dist-tag remains open |
| `wallet-verifier-test-web` | Use noninteractive tests; package complete Nitro output; run the packed output and reuse it in images; update the pinned runtime and remove unused package managers | 32 tests, deployed HTML/client assets and native matrices pass; configured Node26 selection and runtime fix now pass public/private stable0.2.4 |
| `wallet-r2ps` | Align exact Rust builder/toolchain intent; use `--locked` and shared workspace versions; preserve all three image/four binary outputs; update vulnerable Debian runtimes | Public/private GitHub PR/stable, six image checks and downloaded executable/image-byte checks pass; local-forge flows and development remain open |
| `wallet-app-android` | Correct backup-rule domains while retaining blocking lint; validate OAuth callback destination/state/code before exchanging tokens; explicitly execute camera-module tests | 42 focused callback checks, full native PR/MR matrix, APK/AAB/SBOM inspection; AAB streaming-verifier warning still requires investigation |
| `wallet-app-ios` | Use SDK-supported builders/status-bar APIs; approve only reviewed locked build plug-ins; align scanner tests with callbacks and finite streams; use simulator-appropriate signing and a hardware-independent test fake | Public/private native simulator tests pass at `0b9d049`; preserve production crypto and Secure Enclave behavior, with no software-authentication fallback |

Do **not** upstream neutral application IDs, inert endpoints, disposable registry
coordinates, debug/test keys, local lab CA setup, temporary job images, scanner
compatibility repetition of inherited Renovate policy, or private fixture trust
settings as production requirements. Rust's flagged values were explicitly
confirmed as intentional scenario data: leave values/history unchanged, with no
blanket scanner ignore or renewed approval hold.

## Ordered remaining work

### P0: close acceptance gaps before claiming complete migration

- [x] Hosted Maven: extend the existing consumer inspector to download all four
  registry files; compare to actual build outputs; resolve source/Javadoc from a
  fresh Maven cache; repeat exact-path anonymous denial for the private sibling.
  Profile-only engine8b38ec9 exposed a genuine rebuilt-JAR mismatch (FC-16).
  The shared artifact-first fix passes public/private2.7.6 on frozen9e5b271,
  with 14 provider-free checker cases each and local native registry coverage.
- [x] Rust: download every one of the four released executables; inspect format,
  architecture and loader/dependencies; run bounded safe help in the intended
  non-root runtime without network. Local rebuilt binaries are not substitutes.
- [x] Private ACLs: pair authenticated success with anonymous denial on identical
  asset, package and image coordinates, beyond the existing Maven POM/main-JAR
  checks. The shared hosted harness now implements image/asset checks through
  existing OCI/API paths; status/credential/redirect regressions pass. Native
  checks pass for the named Rust, Maven and web trials; this does not establish
  every consumer's optional publication surface.
- [ ] Finish real cross-job rejection: native artifact/bundle byte mutations and
  stale handoff digests now reject for the expected digest-mismatch reason.
  GitLab/Forgejo signed-image verification also rejects wrong keys and provenance
  subject/source/commit/workflow, and published release-file bundles reject changed
  content, changed signatures and wrong keys on both local forges. Full consumer
  job-boundary identity cases remain open; unrelated setup failures do not satisfy
  them.

### P1: reusable release behavior and consolidation

- [ ] Interrupt an image-producing consumer after candidate push; retry the same
  version with retained remote state; prove final digest/assets and preservation
  of prior moving tags. Existing rollback primitives do not prove this workflow.
- [ ] Complete consecutive npm/Cargo local releases and compare version, lockfile,
  tag, SBOM and provenance. Public/private npm 2.0.1→2.0.2 passes on GitLab and
  Forgejo; GitLab container-first Cargo fixture 0.1.1→0.1.2 also passes.
  Consecutive Forgejo Cargo cases remain open.
- [ ] Add independent GitLab/Forgejo development flows and Rust container-first
  development; preserve unchanged main and absence of stable tags/releases.
- [ ] Complete Rust local-forge PR/stable coverage and applicable Android local
  stable/development coverage, or explicitly agree narrower acceptance scope.
- [x] Prove positive GitHub SARIF ingestion by waiting for processing and querying
  the expected analysis/category/commit. Optional upload success is not proof.
- [x] Reconcile declared versus executed Node toolchains, especially the web
  release builder. Do not change healthy lockfiles to hide a tool-version defect.
- [ ] Freeze one engine containing all intended root fixes; refresh its applicable
  generated checksums; run the required provider-free gates and representative
  public/private PR, development, package and container-first acceptance.
- [ ] Extract coherent reviewed patches for each owner, preserving attribution
  and separating trial adaptations. Upstream application/PRs remain a distinct
  delivery step; original repositories have not been changed.

### P2: compatibility and explicitly deferred scope

- [ ] Resolve required legacy SBOM ZIP/layout and GPG sidecar expectations.
- [ ] Investigate Android AAB manifest/JarInputStream ordering warnings without
  treating a JarFile verification success as universal compatibility.
- Physical devices, real stores and mixed Linux architectures are outside the
  agreed initial amd64/simulator/test-signing scope, not accidental omissions.

## Residue and cleanup acceptance

**No-residue acceptance is still open.** Preserve intended source and evidence;
remove only verified, owned disposable resources after the final runs.

| Category | Current observation | Required closure |
| --- | --- | --- |
| Active hosted session | Guarded status confirms none after all seven contract cases finished | Session closure does not prove package/image absence |
| Validation supervisors | Hosted batch and recovered consecutive npm trials passed; historical failed unit records remain | Preserve results, then clear exact obsolete supervisor state |
| Source and snapshots | Main Forge CI changes are committed locally; isolated committed consumer/engine copies remain | Retain deliverable commits/docs; inventory and remove only obsolete copies after handoff |
| Local build/evidence storage | Shared `.live-validation` tree reports approximately 31 GiB with two unreadable subtrees; includes other projects/history | This is an incomplete shared-tree measurement, not this trial's exact residue size; inventory exact task-owned paths before removal |
| Hosted packages/images | Trial packages can outlive deleted repositories; no fresh remote package inventory was completed in this reconciliation | Enumerate exact immutable package/resource identities, retain receipts, use appropriate authenticated exact-resource cleanup and prove absence |
| Local images, archives and caches | Prepared job/signer images, image archives and ecosystem caches retained for reproducible reruns | Inventory through the documented lab/engine routes; remove only task-owned resources after final acceptance |
| Credentials | Borrowed host PATs/auth config are operator-owned; local target generation remains available | Remove ephemeral session/key copies and retire task-only target credentials through the producer; do not delete/revoke borrowed operator credentials |
| Host tool-manager incident OPS-01 | Earlier `mise trust` unexpectedly removed 16 older tool versions | Separate restoration decision remains unresolved; do not silently reinstall or change cleanup policy |

Persistent locks and the running shared lab are not disposable trial leftovers.
Do not unlink locks, reset a shared lab or delete by prefix alone. A historical
failed run is retained evidence, not a reason to erase its source or reports.

## Reusable lessons and evidence map

1. Freeze once, reuse read-only, and bind each claim to an engine and consumer
   revision. A green aggregate is not a final combined-engine checkpoint.
2. Test workflow-to-workflow and workflow-to-CLI contracts with actual step
   execution. Correct upload configuration does not prove lifecycle attachments.
3. Accept the installed or unpacked output, outside the source tree, rather than
   assuming a build success proves usability or complete packaging.
4. Compare built and published bytes. Maven deploy can rebuild; pre-existing
   source/Javadoc files are not automatically attached to a new Maven session.
5. Keep private authenticated success, same-path anonymous denial and intended
   signing trust as separate assertions.
6. Count executed, skipped and expected failures separately. Repeated log summaries
   and multiple signatures do not increase test or artifact coverage.
7. Attribute causes correctly: shared engine, upstream consumer, trial fixture,
   operator environment and deferred scope have different fixes.
8. Preserve production security boundaries: no relaxed CVE thresholds, broad
   plug-in trust, fabricated production credentials or hardware-crypto fallbacks.

Detailed reusable invariants: [Consumer validation lessons](consumer-validation-lessons.md).
Executable tier/recipes: [Testing](testing.md#maven-format-publication-evidence).

The validation workspace at
`/home/jos/development/codefloe/itiquette/.live-validation/` retains:

- `UPSTREAM-ISSUES-INDEX.md`: issue ownership and correction status.
- `acceptance-matrix-20261019.md`: revision-specific provider/consumer coverage.
- `upstream-migration-ledger-20261019.md`: detailed chronology and reproductions.
- `consumer-trial-20261019-status.md`: latest reconciliation followed by historical
  checkpoints; old queued/running/blocked statements are not current status.
- `rust-image-verification-20261020.md`: all six Rust image trust/digest receipts.
- `maven-profile-native-evidence-20261020/`: initial fixture failure and corrected
  two-provider native pass. Launcher retention is 30 days; preserve selected
  evidence before expiration when preparing the final deliverable.
- Each isolated consumer's `test/live/` notes, inspectors and regressions.

This handoff supersedes historical pending/blocked statements for the completed
flows above. It intentionally retains unresolved acceptance as unchecked work.
