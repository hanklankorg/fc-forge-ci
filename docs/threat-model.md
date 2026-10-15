# Threat model

This document names what forge-ci defends against, what it deliberately
doesn't, and where the adopter's own controls have to layer on top.
Adopters evaluating forge-ci for production use should read this to
understand where the trust boundaries sit. Maintainers should consult it
when assessing proposed changes. A "security improvement" that doesn't
close a real attack vector is over-engineering; one that does should be
reflected here.

## Trust boundaries

| Layer | Owned by | What can go wrong here |
|---|---|---|
| Caller workflow YAML | Adopter | Over-permissioned `permissions:`, over-scoped `secrets: inherit` |
| `.forge-ci/artifacts.yml` | Adopter | Names with path traversal (validated), unsafe `working-directory` (validated), env-var refs (validated) |
| `pom.xml` / `build.gradle` / `Cargo.toml` / `package.json` / `Containerfile` | Adopter | Arbitrary code at build/publish time, **by design** (that's what `mvn package`, `gradle build`, `cargo build`, `npm pack`, `docker build` do) |
| mise configuration / `gradlew` / `rust-toolchain.toml` / the changelog renderer's configuration | Adopter | They choose which tools a job downloads and runs, or are run themselves (`gradlew`), so they are code in the same sense as the build files above |
| `.forge-ci/allowed_signers` / `allowed_gpg_keys.asc` | Adopter | Allowlist additions; only meaningful when combined with branch protection |
| Reusable workflow YAML | forge-ci | Documented inputs/outputs; SHA-pinned third-party actions; every internal `workflow_call` site declares the secret names it needs explicitly (no blanket `secrets: inherit` between reusable workflows) |
| `forge-ci` Go binary | forge-ci | Argv-pinned tools (tests assert exact argv); env-only secret reads; reproducible builds |
| Runtime images | forge-ci | Tag-pinned (`@vN.N.N`) by default; **see Known trust boundaries below** |
| Third-party GitHub Actions | upstream | SHA-pinned by forge-ci's Renovate base config |
| Toolchain base images (`debian:13-slim` etc.) | upstream | Digest-pinned (`@sha256:…`); a local Go contract test enforces the pin and Renovate updates it |
| Forge run-artifact store (GitHub Actions artifacts / Forgejo Actions artifacts) | upstream (forge) | Carries build output between jobs. Every downloaded entry is gated for path traversal, symlinks, and size (`domainartifact.SafeJoin`). A cross-job build-to-sign hand-off must be bound out of band: the producer emits `release dist-digest` as a job output and the signer re-checks with `release validate-dist --expected-digest`, so the expected value travels the forge control plane. `validate isolation` checks selected static wiring and lexical hints, not runtime verification. The owner of the pinned signing workflow must enforce the trusted digest input, verifier failure gate and ordering before signing; on Forgejo that workflow is this repository's `.forgejo/workflows/sign-and-publish-release.yml`. The store is trusted for availability and for any flow that does not bind. See [Artifact and image flows](flows.md#how-each-flow-binds-build-to-signature). |

Two assumptions sit under every row. Within one job, state persists between steps: the job environment file, additions to `PATH`, and the workspace. So every step of a job that later holds a token or a key is trusted; an earlier step can redirect which binary a later step runs. Between jobs, the runner is assumed fresh. GitHub-hosted runners are; the Forgejo layer requires a runner that gives each job a fresh container and filesystem (see [its requirements](forgejo.md#requirements)).

## What forge-ci defends against

| Concern | Mechanism |
|---|---|
| CLI secret inputs in argv | File/stdin/environment interfaces. Native macOS `security` subprocesses still receive signing passwords in argv; see [Argv hygiene](verification.md#argv-hygiene) for the scope and runner requirement |
| Signing material left on the runner | GPG keys are imported from standard input into a temporary keyring and are never written as key files. An `env://` cosign key is passed to the cosign subprocess alone. Three kinds of material need a file and are written owner-only, then removed by the command that wrote them: the SSH signing key (`release ssh setup` and `cleanup`), the Android keystore, and the iOS certificate and provisioning profile, which go into a transient keychain. A job killed between the write and the removal leaves the file, which is why the runner must be fresh per job |
| Tampered third-party action versions | Renovate `pinDigests: true` on the `github-actions` manager (base config) |
| Tampered Containerfile base images | Renovate `pinDigests: true` on the `dockerfile` manager (local config) |
| Path traversal via `working-directory` | `validateWorkingDirectory` at config-parse time |
| Path traversal via artifact `name` / `binary-name` | `validateArtifactFileNames` at config-parse time |
| Env-var refs in artifacts.yml string fields | Rejected by `validateWorkingDirectory` (no silent literal-string fallback) |
| Silent CVE-laden container release | `publish-container.yml` Trivy scan + severity gate (fail on CRITICAL/HIGH by default) |
| Silent SBOM-missing release | All SBOM steps mandatory; explicit `enable-build-sbom: false` opt-out |
| Non-reproducible JVM / Cargo releases | `validate jvm-reproducibility` + `validate cargo` hard-fail at prerequisites stage |
| Stale-runner drift | `runs-on: ubuntu-24.04` pinned (not `ubuntu-latest`) |
| Workflow contract drift | `TestWorkflowInputContract` fails CI if a `with:` key doesn't match a declared `inputs:` |
| Plan/code drift | `TestTargetKeys_MatchStructTags` + `TestPlanContracts_Golden` |
| Docs/code drift | `TestDocsCLIReferenceInSync` covers the generated CLI surface. `cmd/forge-ci/smoke_cli_contract_test.go` maps the binary-smoke scenarios, including `ART-01`–`ART-05`, to real CLI behaviour. `ART-06`–`ART-09` in `docs/cli-black-box.md` are manual traceability references to supporting domain/adapter/live evidence, not docs/code guards; those direct tests do not prove built-binary flag/env wiring, provider selection, or process exit-code wiring. |
| Timestamp non-determinism in artifacts | `SOURCE_DATE_EPOCH` baked from `git log -1 --format=%ct HEAD` for Go + Cargo binaries and security reports |
| Per-step secret over-scoping | Each publish step's `env:` block lists only the secrets that specific step needs; no step receives the union of its job's secrets |
| **Untrusted-trigger publish/release**: signing, package, and API secrets reaching a workflow run whose commit context is contributor-controlled | Two layers: (a) `pullrequest-orchestrator.yml` declares only `CODE_SCANNING_TOKEN` in its `workflow_call.secrets` block, so the publish-secret family is unreachable through the documented PR path; (b) privileged publish / release workflows run `forge-ci validate event-context` before publishing or signing. Its default policy refuses PR and comment triggers. The CLI's flag/environment policy inputs are trusted workflow configuration; the published reusable workflows expose no override input. See [Trigger policy](components.md#trigger-policy) for the allowlist and precedence. |

## What forge-ci does NOT defend against

| Concern | Why not | Adopter mitigation |
|---|---|---|
| **Malicious build extensions** (pom.xml plugins, build.gradle tasks, Cargo `build.rs`, package.json scripts, Containerfile `RUN`) | These run with whatever env is set on their step, by design. `mvn deploy` invokes maven-deploy-plugin; if a malicious extension is on the classpath, it runs with the credentials in env. | Per-team / per-repo secrets (not org-wide); branch protection so unreviewed code doesn't reach the release branch; release-authorisation allowlist gate |
| **Multi-tenant secret reuse** | A shared org secret used by N teams' forge-ci callers can be exfiltrated by any team's malicious build extension during a publish step. forge-ci has no way to know "this caller is team A, that one is team B." | Scope secrets per-team / per-repo at the GitHub org-secrets level |
| **Compromised commit access** | A bad actor with push rights to the release branch can publish releases via the normal flow. The signature on the tag is also under their control. | Branch protection + required reviews + `require-allowlisted-signer: true` + `.forge-ci/allowed_signers` with the maintainer fingerprints |
| **Runtime-image registry compromise** | `ghcr.io/diggsweden/forge-ci-runtime-*:v0.90.0` is tag-pinned. A tag rewrite at ghcr.io would ship a different image. | Adopters override `runtime-image-*` inputs with `@sha256:…` digests when they need stricter posture |
| **Caller-selected job container with write authority** | A `runtime-image` override becomes the job container, which starts before the first step. No step-level check can validate it, because the image is already what runs the steps. Most container jobs hold no durable write permission, but two do: `version-bump.yml/bump-version` (`contents: write`) and `publish-snapshot-npm.yml/build-and-publish` (`packages: write`). An unreviewed image there can write to the repository or publish a package. GitHub release creation signs and publishes on the plain runner with the pinned forge-ci binary instead; its container job only assembles files. This is not an escalation across a trust boundary — the caller already holds those permissions — but it is worth knowing before overriding the input. | Override `runtime-image` only with a reviewed reference, pinned by `@sha256:` digest. `TestCallerSelectedContainersWithWriteAuthorityAreDeclared` in `test/workflowguard` fails if a third such job appears without being recorded here |
| **Checkout destination swapped after it was checked** | `forge-ci platform checkout` guards its destination and then hands the path to Git, which takes a pathname rather than a directory descriptor. Anything able to write the parent directory can swap the path between the check and the write. Prevention is not available without Git accepting a descriptor. | The guarded directory's inode is recorded and re-checked before publication, so a run whose path was swapped refuses instead of publishing an unverified directory as the caller's checkout. This is detection, not prevention: work may already have been written into the substituted directory. It sees a rename or symlink swap — what a hostile replacement is — and not a delete-and-recreate that the filesystem happens to give the freed inode. A destination this run creates is also built in a sibling staging directory and renamed into place only on success |
| **Self-hosted runner abuse** | Adopters using their own runners inherit those runners' risks (host filesystem persistence, network position, container escape). forge-ci can't see this layer. | Use GitHub-hosted runners where possible; isolate self-hosted runners per-repo |
| **Egress exfiltration during publish steps** | `harden-runner` is in `audit` mode by default on publish workflows that have variable network surface (mvn/gradle/xcode plugins reach unpredictable endpoints). | Set `egress-policy: block` + `egress-allowed-endpoints` per caller on the workflows that declare these inputs; grep `egress-policy` across `.github/workflows/` for the current set. |
| **GitHub Actions log secret masking bypass** | GHA masks secrets in logs by substring match. Base64-encoding or split-string echoing defeats this. | Same as malicious build extensions: limit who has code-write access; rotate any secret that was theoretically observable; per-team secret scoping |
| **`build-args` values leaked via the published image** | `publish-container.yml` passes `containers[].build-args` from artifacts.yml to the build (buildah `--build-arg`) verbatim. They surface in the image config / build records (e.g. `docker history`, any registry inspect). buildah emits no build provenance, so they are not written into a SLSA provenance attestation, but build-args remain unsuited for secrets. | **Never pass secrets via `containers[].build-args`.** Use [build-secret mounts](artifacts-reference.md#build-secrets): values remain in the secret envelope and private mount files, while forge-ci forwards path-only specs. Consuming `RUN` commands must still avoid printing values or copying them into image files; the trusted-build-code boundary above applies. |
| **Error-message inference of allowlist contents** | `validate release-authorization` returns different exit codes / annotations depending on whether the tag signer is missing from the allowlist vs the allowlist file itself is missing vs the signature is malformed. A probing caller can narrow which keys are listed. | Limit who can trigger releases (branch protection). The allowlist file is repo-readable anyway, so it is informational, not a credential. |

## Subprocess credential policy

Keep environment selection tool- and mode-specific. `internal/cli/secretenv`
removes the engine's known credential names, plus caller-selected key variables,
from tools such as Syft, Trivy and changelog renderers. This is a denylist, not
proof that arbitrary adopter-defined credentials are absent. Keep job/step secrets
narrow and rendering separate from signing; filtering is not a substitute for
that separation.

Cosign's `ForKeyRef` restricts `env://` signing to the selected key, password,
runtime settings and explicitly allowed extras. File, KMS and keyless modes
deliberately retain inherited authentication inputs. Proxy and TLS settings are
runtime inputs too, and proxy URLs may contain credentials. GPG package signing
uses `NewIsolated`; import and cleanup currently use the inherited environment.

A universal subprocess allowlist is not the policy: guessing registry, KMS,
OIDC, proxy or TLS requirements can break supported signing paths. Revisit a
specific tool/mode when a concrete unnecessary credential or narrower supported
input set is identified, with tests for both exclusion and required authentication.
Maintaining the shared known-credential list remains routine work as inputs change.

## Adopter responsibilities

These are the controls that have to live on the adopter's side. forge-ci can document them but cannot enforce them across organizational boundaries.

1. **Branch protection** on the release branch (require PRs, require reviews, require signed commits if your org policy demands it)
2. **Per-team / per-repo secrets** instead of org-wide where possible; limits multi-tenant blast radius
3. **Review `.forge-ci/allowed_signers`** and `allowed_gpg_keys.asc` like any other security policy file (the test for it is "would I be comfortable if this person could ship a release with my org's name on it")
4. **Audit the publish-workflow caller** for `secrets: inherit` scope. Internally, forge-ci does not use `secrets: inherit` between reusable workflows; every call site names the secrets it forwards. The **top-level caller in the adopter's repo is still adopter-controlled**, though: a caller workflow that inherits everything will pass everything down.
5. **Override runtime-image to a digest** when stricter posture is required: `runtime-image: ghcr.io/diggsweden/forge-ci-runtime-go-1.27@sha256:…`
6. **Promote egress to `block`** on the workflows that declare `egress-policy` / `egress-allowed-endpoints` (grep `.github/workflows/`), where the network surface is bounded and you can enumerate the endpoints
7. **Rotate publish credentials periodically** (Maven Central tokens, npm tokens, and so on) independent of any specific incident; reduces the value of a compromised credential

## Frequently confused: what's an attack vs. a foot-gun

forge-ci's validators reject some patterns that aren't really security threats but DO cause confusing failures deep in the pipeline:

- `working-directory: /srv/build`: rejected as absolute, but the failure mode without rejection is "build fails on a fresh runner because the path doesn't exist", not "attacker escapes the workspace"
- `binary-name: "../bad"`: rejected as path-traversal, but the failure mode without rejection is "compiled binary lands one directory up where upload-artifact doesn't find it", not "attacker writes /etc/passwd"
- `working-directory: ${WORKSPACE}/svc`: rejected as env-ref, but artifacts.yml doesn't perform shell expansion so the value would land as the literal string `${WORKSPACE}/svc` and a stat would fail. Not exfiltration; just a confusing error.

These rejections are **input hygiene**, not security boundaries. The runner is assumed fresh per job (see [Trust boundaries](#trust-boundaries)); the workspace contains only the consumer's own files. A clear parse-time error pointing at the offending field is the value, not "we prevented an exploit."

## Attested parameter ownership

Caller-declared provenance extras cannot use engine-owned names. `source`,
`ref`, `workflow`, `image`, `flavor`, and `base_input_id` remain reserved even
when the selected profile omits one of them, so changing profiles cannot turn an
engine fact into caller-controlled data. Existing predicate fields are reserved
as well.

## OCI release identity

OCI release identity matching requires non-empty revision, version, ref name,
and source expectations before comparing image labels. An unlabelled image
therefore cannot match an empty or incomplete expected identity, even when the
caller bypasses CLI-required flags and invokes the application API directly.

Promotion admits the candidate tag against the ledger's recorded digest, then
copies from that candidate repository pinned to the digest. Moving the candidate
tag after admission therefore cannot change the selected image. Destination
digests are still checked after copying, and cross-repository promotion still
requires signature copying. If the registry has discarded the recorded manifest,
the copy can fail; promotion does not substitute the tag's newer image.

## Reporting a security issue

See [SECURITY.md](../SECURITY.md) for the disclosure process. The threat
model above sets expectations for what's in scope: a report claiming
"a malicious pom.xml plugin can read MAVEN_CENTRAL_PASSWORD during
mvn deploy" is correctly classified as out-of-scope (documented in the
table above) and would be closed with a pointer here.

A report claiming "argv leakage during release sign" or "a non-control
path puts a secret in step summary output" would be in-scope and
treated with the usual disclosure timeline.
