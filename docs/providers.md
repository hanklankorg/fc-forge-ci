# Providers and runners

The `forge-ci` CLI is forge-agnostic: the same verbs run on GitHub
Actions, GitLab CI, Forgejo Actions, and on a developer laptop. This page
explains how the CLI decides which forge it is talking to, how to override
that, and which features are available where. It is the authority for provider
maturity; provider quick starts link here instead of restating status.

## Two axes: runner conventions vs forge API

"Which CI platform" is really two independent things:

- **Runner conventions** (`RunnerKind`): how the CLI emits output. The
  workflow-command dialect (`::error::`), the `$*_OUTPUT` key/value writes,
  and the step-summary file.
- **Forge API** (`ForgeAPI`): the server REST surface used for releases,
  asset upload, token/permission checks, repo metadata, SARIF, and run
  artifacts (upload/download via each forge's artifact service: GitHub v4
  results, Forgejo v3 runtime).

They are detected separately because the two axes genuinely disagree,
most visibly for **Forgejo**, which is its own value on *both*. Forgejo
Actions sets `GITHUB_ACTIONS=true`, but Forgejo itself states it is
["not designed to be compatible"](https://forgejo.org/docs/latest/user/actions/github-actions/)
with GitHub Actions, only familiar. The differences land squarely on this
binary's output surface: Forgejo does **not** render `::error::`
annotations, `::group::` log folds, or job summaries
([go-gitea/gitea#27898](https://github.com/go-gitea/gitea/issues/27898);
[nektos/act#1187](https://github.com/nektos/act/issues/1187)), its native
step-output var is `$FORGEJO_OUTPUT` (with `$GITHUB_OUTPUT` as a
compatibility alias since Forgejo Runner 7.0.0), and it
[documents no job-summary variable at all](https://forgejo.org/docs/latest/user/actions/reference/),
with only `OUTPUT`/`ENV`/`PATH` step files. So `forgejo` is a distinct runner
dialect: it emits plain `Error: …` log lines instead of unrenderable
workflow commands, and routes step summaries to the job log. Keeping the
axes separate is what lets one binary serve every forge without misrouting
API calls *or* emitting output a runner silently drops.

| Forge | RunnerKind | Forge API (`ForgeAPI`) |
|---|---|---|
| GitHub Actions | `github` | `github` |
| Forgejo Actions | `forgejo` | `forgejo` |
| GitLab CI | `gitlab` | `gitlab` |
| Local / dev | `local` | `local` |

## Detection and overrides

**Runner auto-detection** checks the environment in this order:

1. `$GITHUB_ACTIONS=true` and either a truthy `$FORGEJO_ACTIONS` /
   `$GITEA_ACTIONS` or a nonempty `$FORGEJO_OUTPUT` → **forgejo**
2. `$GITHUB_ACTIONS=true` → **github**
3. `$GITLAB_CI=true` → **gitlab**
4. otherwise → **local**

GitHub/GitLab markers require the exact value `true`. Forgejo/Gitea action
markers accept `true`, `1`, `yes` or `on`, ignoring case and surrounding space;
mere presence or `false` does not enable them.

**Forge API auto-detection** uses those same signals, plus a target rule:
`GITHUB_ACTIONS=true` with a nonempty `FORGEJO_SERVER_URL` or
`FORGEJO_REPOSITORY` selects the Forgejo API. Those target variables do not
change runner identity. A GitHub job publishing to Forgejo therefore retains
GitHub annotations and summary routing.

Auto-detection is only a default. Both axes have explicit overrides, which is useful
when a runner masquerades as another forge or is misconfigured:

| Override | Flag | Env var | Values |
|---|---|---|---|
| Forge API | `--provider` | `FORGE_CI_PROVIDER` | `auto`, `github`, `gitlab`, `forgejo`, `local` |
| Runner | `--runner` | `FORGE_CI_RUNNER` | `auto`, `github`, `forgejo`, `gitlab`, `local` |

```bash
# Force the Forgejo forge API even though the runner sets GITHUB_ACTIONS=true
forge-ci --provider forgejo release publish ...
```

Run `forge-ci doctor` to see the resolved provider, runner, and the
capability matrix for the current environment.

### Summary destinations

Summary routing follows the runner axis, independently of the forge API:

| Runner | Markdown summary destination |
|---|---|
| GitHub | `GITHUB_STEP_SUMMARY`; no summary write when unset |
| Forgejo | `GITHUB_STEP_SUMMARY` if explicitly set, otherwise the job log |
| GitLab | `CI_SUMMARY_FILE`; no summary write when unset |
| Local | No summary destination; summary appends are no-ops |

On GitLab, set `CI_SUMMARY_FILE` to a file such as `summary.md`, ensure its
parent directory exists, and declare it under the job's `artifacts.paths` to
retain it. Use `artifacts.when: always` when failure summaries must also be
uploaded. The CLI appends Markdown; the workflow owns publication and retention.
`CI_OUTPUT` is the separate dotenv destination for scalar outputs. Structured
job/stage records use the [file-based report handoff](workflows.md#file-based-report-handoff).

## Installing `forge-ci`

Jobs using a forge-ci runtime container already have the binary installed.
Plain Linux and macOS jobs should use the repository's composite installer,
with the action itself pinned to an immutable commit SHA:

```yaml
- name: Install forge-ci
  uses: diggsweden/forge-ci/.github/actions/install-forge-ci@<full-commit-sha>
  with:
    ref: v0.90.0
```

The action pin selects the reviewed installer implementation; `ref` selects the
binary release. For published release refs, the installer bootstraps the
repository-pinned cosign with a SHA-256 check, then downloads the release assets
and verifies `checksums.txt` and its Sigstore bundle before checking the binary
archive. Bootstrap, download, checksum or signature failures stop installation;
there is no automatic source fallback. Cosign need not be preinstalled.

Branch and commit refs select source installation and require Go. The upstream
route uses version-suffixed `go install`, which cannot install revisions with
the current local module replacement. Use the reviewed
[source checkout procedure](DEVELOPMENT.md#source-checkout) for those revisions.
Forks use a checkout-based source build instead. Setting
`FORGE_CI_USE_GO_INSTALL=1` explicitly selects source installation even for a
release ref; that route does not verify release assets.

The underlying [`install-forge-ci.sh`](../scripts/bootstrap/install-forge-ci.sh)
is for this composite action and for jobs that have already checked out or
vendored the forge-ci repository. A consumer must not assume that
`scripts/bootstrap/install-forge-ci.sh` exists in its own checkout.

On Forgejo, a job gets the binary from the delivery layer instead: the
`setup-forge-ci` action under `.forgejo/actions/` downloads the binary the
pinned commit records from the instance's package registry, verifies it
against that checksum and puts it on `PATH`. See
the [Forgejo guide](forgejo.md#installing-the-binary-in-a-job). The release
installer above also runs on a Forgejo runner that supports remote composite
actions; where it does not, use a prebuilt runtime image. Do not download and
execute an unpinned installer script.

## Repository and image names

Repository inputs are URL paths, not display names. GitHub and Forgejo use
`owner/name`; GitLab also accepts `group/subgroup/name`. The selected forge
controls that shape independently of the runner. Local mode accepts nested
paths without assuming a forge.

On GitLab, `CI_PROJECT_PATH` supplies the repository and `CI_PROJECT_NAMESPACE`
supplies its complete namespace. Explicit `REPOSITORY` / `REPOSITORY_OWNER`
values take precedence; source identity used for provenance still comes from
runner-attested context.

Source spelling is preserved, including uppercase and mixed case. Addressing
checks reject empty or relative segments, URL escapes, control characters and
ambiguous separators. They accept letters, digits, dots, underscores and dashes,
including repository names such as `.github`. Creation-time reserved names and
availability remain the server's responsibility.

Container destinations follow OCI rules separately:

- `MixedOwner/My.Repo` becomes `ghcr.io/mixedowner/my.repo` on GHCR.
- `Group/Sub/My_App` retains its full namespace on the selected registry.
- Docker Hub results are fully qualified, such as `docker.io/owner/app`.
- `CONTAINER_NAME` / `--name` adds a subpath unless it equals the repository name
  after lowercasing. Tags retain their own case-sensitive grammar.
- OCI path components must start and end with lowercase letters or digits.
  Separators may be one dot, one or two underscores, or one or more dashes.

A source name such as `.github` cannot be used literally as an OCI component.
Choose an explicit image name and an independently configured publication base:

```bash
forge-ci container ref resolve \
  --registry ghcr.io --repository Org/.github --repository-owner Org \
  --image-name ghcr.io/org/github-config

forge-ci container validate namespace \
  --registry ghcr.io --repository Org/.github --enforce-namespace Org \
  --image-name ghcr.io/org/github-config \
  --expected-image-repository ghcr.io/org/github-config
```

`publish-container.yml` exposes the corresponding `image-name` and
`expected-image-repository` inputs. Set the expected repository in the reviewed
publication configuration, rather than copying a candidate image into the
policy. It must remain below the enforced registry and namespace; the existing
suffix/subpath constraints still apply. Names are never silently sanitized.
Other image commands expose their destination through `--image`,
`--expected-repository` or `--expected-image-repository`; consult their help.

GitHub and Forgejo package adapters lowercase npm scopes. GitHub also lowercases
the Maven owner coordinate. Source repository identities retain their spelling.

## Git operation credentials

Git checkout, changelog and release commands accept explicit tokens through
`--token-file <path>` or `--token-file -` for stdin. The literal `--token` value
flag is not supported. Omit the file flag to keep the normal environment
selection, including the dedicated `RELEASE_TOKEN` preference on release-write
commands. Runner-issued tokens retain their issuing-server scope.

On GitLab CI, `CI_JOB_TOKEN` is the final environment fallback, after the
explicit `CI_TOKEN` and existing Forgejo/GitHub token names. It stays bound to
the issuing `CI_SERVER_URL` and uses the `gitlab-ci-token` username required
by GitLab's HTTPS Git service. Other tokens use `x-access-token`. Authentication
is transient: no token-bearing remote URL or persisted Git auth header.

Explicit file/stdin input overrides environment credentials and is sent to the
operator-selected destination. Trailing CR/LF is trimmed; other bytes are
preserved. An empty file supplies no HTTP token rather than falling back to the
environment. A missing/unreadable file fails before Git or provider effects.
`--token-file ""` is a usage error; use an empty file to suppress the environment
token. A command with both SSH-key and token inputs cannot read
both from stdin. See the [CLI reference](cli-reference.md) for command-specific
inputs and prerequisites.

Checkout metadata discovery uses the selected `--server-url` and repository.
An explicit token file applies to both metadata and Git; an empty file suppresses
ambient authentication for both. GitLab job tokens use `JOB-TOKEN` for metadata;
an independently configured `GITLAB_TOKEN` can supply API access when no token
file is selected. `--object-format` skips metadata discovery entirely.

## Capability matrix

Not every forge implements every feature. The CLI reports each forge's
capabilities, and `doctor` prints them. Commands that require a missing
capability refuse; optional operations such as SARIF upload have explicit
nonfatal behavior, described below.

| Capability | github | gitlab | forgejo | local |
|---|:---:|:---:|:---:|:---:|
| SARIF / Code Scanning upload | ✅ | ❌ | ❌ | ❌ |
| SLSA build-provenance attestation API | ✅ | ❌ | ❌ | ❌ |
| Keyless OIDC signing (public Fulcio, no extra flags) | ✅ | ✅ | ❌ | ❌ |
| Keyless OIDC signing against your own Fulcio | ✅ | ✅ | ✅ | ❌ |
| Release asset upload | ✅ | ✅ | ✅ | ❌ |
| Run artifact upload/download | ✅ | ❌ | ✅ | ❌ |
| Container tag deletion (package/registry API) | ❌ | ✅ | ✅ | ❌ |
| Container tag listing (package/registry API) | ❌ | ✅ | ✅ | ❌ |

This matrix is written by hand, and `PAR-CAP-1` fails when it drifts from what
the adapters implement: every model field needs exactly one row, every row one
cell per platform, and every cell must match the adapter.

Two things it does **not** say. Each row is read from an adapter given an *empty*
environment, so it describes the forge's canonical hosted instance, a
self-hosted deployment can differ, and for the public-Fulcio row it does: only
github.com and gitlab.com publish an issuer public Fulcio trusts, so the product
reports `false` there on GitHub Enterprise Server or behind a `$CI_SERVER_URL`,
and says so up front rather than failing inside cosign. And the live conformance
tier exercises what the *lab* can host, which is GitLab and Forgejo; the rows
that only github claims, SARIF upload and the attestation API, are covered
only by their absence elsewhere (`PAR-CAP-2`).

Tag deletion (`container release-images rollback` and `cleanup`) takes its token
from `--provider-token-file`, then `$FORGE_CI_PACKAGE_API_TOKEN`, then the
detected forge's own variable: `$FORGEJO_TOKEN` or `$GITEA_TOKEN` on Forgejo,
`$GITLAB_TOKEN` on GitLab. Another forge's token is not read, `$GITHUB_TOKEN` on
a Forgejo runner included; without one of these the command refuses before it
calls the forge.

Behavior when a capability is absent:

A refusal for a missing capability exits **78** (`EX_CONFIG`), never 69
(`EX_UNAVAILABLE`). 69 means an external system is unavailable and is worth
retrying; a capability the forge does not have never arrives, so a pipeline
that retries on 69 would loop forever. Treat 78 here as "this pipeline is
configured for a forge that cannot do this" and change the configuration or the
forge.

- **SARIF upload** (`security report upload-sarif`): only github implements
  the code-scanning role. On gitlab/forgejo/local the command emits a
  `Notice` and exits 0; route findings to a [configured summary](#summary-destinations)
  or an uploaded artifact instead. No failed API call.
- **Keyless signing** (`release sign --method=sigstore`): on a forge without
  keyless OIDC and no explicit `--oidc-issuer`, the CLI emits a `Warning`
  naming the forge and suggesting `--method=gpg`/`--method=kms` or an explicit
  issuer. This warning does not stop signing; a later cosign failure still
  fails the command.
- **Provenance** (`release provenance`): generation does not require a forge
  attestation API. The default `generic` profile uses the checksums file and
  active runner's source and identity, independently of the target provider.
  `--profile forgejo-actions` preserves the legacy provider-derived context.
  Signing with cosign is optional and depends on the configured method/key;
  see the [command reference](cli-reference.md#forge-ci-release-provenance).
- **Run artifacts**: github and forgejo expose an intra-run artifact store
  through their runner's own service; gitlab does not, so a step that would
  hand a file to a later job must use GitLab's native `artifacts:` keyword in
  the pipeline definition instead. The capability is reported only when both
  upload and download are implemented, so a half-supported forge reports false
  rather than promising a store that cannot round-trip.

## Delivery layers side by side

The capability matrix above is the CLI's adapters. The delivery layers built on
them differ too. Each forge's guide, [GitHub](github.md), [GitLab](gitlab.md)
and [Forgejo](forgejo.md), has the same section order and states what its layer
lacks.

| Capability | GitHub | GitLab | Forgejo |
|---|---|---|---|
| Lint gate with a mandated floor | `lint-nanolinter.yml` | `nanolinter` component | `lint-nanolinter` |
| MegaLinter as the lint engine | `lint-megalinter.yml` | `megalinter` component | none |
| Findings in the forge's interface | Code Scanning (SARIF) | merge-request Security tab (SAST report; display needs Ultimate) | none: Forgejo ingests no report; the step log |
| Release-isolation gate | none | none: the signing policy is a separate pinned project | `check-l3-isolation` |
| Build, Go and Cargo | `build-go.yml`, `build-cargo.yml` | `build-go`, `build-cargo` components | `build-go`, `build-cargo` |
| Build, Maven, Gradle, npm, Xcode | workflows | components, without Xcode | none; no consumer yet |
| Job image | runtime images | runtime images; digest-pinned images you supply for `check` and `release` | the consumer's image or `setup-toolchain` |
| Engine binary | runtime image or verified release archive | runtime image, or the checksum-bound build for `check` and `release` | the checksum-bound build |
| Plan-driven fan-out from `artifacts.yml` | `release-build-stage.yml` | generated child pipeline | none: the consumer's workflow is the plan |
| Image build, scan, ledger | `publish-container.yml` | the consumer's `build` commands; `release` imports the ledger | `build-release-image` |
| Release preparation, signed tag | `release-prepare-stage.yml` | `release` component | `prepare-release.yml` |
| Release branch | `branch` input, required | `release-branch` input, the project's default branch by default | `release-branch` input, `main` by default |
| Version bump in project files | `version-bump.yml` | none | none |
| Signing and publication | keyless, GPG, KMS or Cosign key (`env://COSIGN_KEY`) | `release` component, separately privileged jobs, Cosign key | `sign-and-publish-release.yml`, separately privileged jobs, Cosign key |
| Public transparency log | `sign.transparency`, `public` by default (keyless signing needs the log; `none` only with a key) | `transparency-log` input, `none` by default | `transparency-log` input, `none` by default |
| Image promotion | `:dev` → `:staging` → release, gated by Environments | final tags in a separate publication project | final tags (`promote` job) |
| Snapshot flow | `release-snapshot-orchestrator.yml` (npm) | none | none |
| Forge package registry | `publish-maven-github.yml` | `publish-forge-packages` | none |
| Base-image lifecycle | none | `release` with `base-images: true` | three workflows and `prune-base-images`; the build graph around them is the consumer's |

## Per-forge notes

### github
Full role set. OIDC issuer `https://token.actions.githubusercontent.com`.
Token advice on `validate auth token` (classic-PAT refusal). Releases,
assets, and SARIF go through the GitHub REST API (`go-github`).

The reusable GitHub release workflows currently support **github.com only**.
They pin `actions/upload-artifact@v7`, which does not support GitHub Enterprise
Server. `release-orchestrator.yml` therefore exits with configuration error 78
in its first provider preflight on GHES, before checkout, build, or signing;
`forge-ci doctor` reports the same limitation. The CLI's direct release API
commands still support GHES independently of this workflow restriction. The
[GitHub guide](github.md) is the delivery layer's manual.

### gitlab
Releases via `/api/v4`. OIDC issuer `$CI_SERVER_URL` (self-hosted) or
`https://gitlab.com`. No SARIF (GitLab consumes the JSON SAST report
directly). Provenance uses the `generic` profile, because there is no GitLab-specific
one, and none is needed.

The provider adapter and the GitLab Catalog delivery are separate maturity
claims. Live conformance covers the adapter's metadata, authentication,
registry, release, report, and failure contracts. The Catalog surface is still
incremental: lint components and Linux build components exist; publishing
components currently cover forge packages, Maven Central, and App Store
Connect; Google Play and container publishing components are not shipped. The
plan emitters can name those future components, but a generated include is not
usable until its matching `templates/publish-*.yml` exists. Catalog publication
and consumer pipelines therefore need their own live validation rather than
borrowing the adapter's conformance result. The [GitLab guide](gitlab.md) is
the delivery layer's manual.

**Scalar step outputs are renamed on GitLab.** GitLab has no per-step output file: values reach
later jobs through a dotenv report, whose variable names must be upper snake
case. So the CLI's lower-hyphenated output keys are translated on the way out:

| Output | Forgejo / GitHub | GitLab |
|---|---|---|
| `version` | `version` | `VERSION` |
| `version-no-v` | `version-no-v` | `VERSION_NO_V` |
| `project-name` | `project-name` | `PROJECT_NAME` |

The value is identical; only the name a later job resolves differs. The job
must nominate the dotenv path in `$CI_OUTPUT` and declare it. The CLI writes
where the pipeline says, because GitLab provides no default:

```yaml
resolve:
  variables:
    CI_OUTPUT: build.env
  script:
    - forge-ci release resolve metadata --version "$CI_COMMIT_TAG" --repository "$CI_PROJECT_PATH"
  artifacts:
    reports:
      dotenv: build.env
```

`PAR-RUN-5` asserts this against a real runner on both forges.

### forgejo

This repository ships a delivery layer for Forgejo under `.forgejo/`, with
reusable workflows and composite actions for the pull-request lint gate, Go and
Cargo builds, container images, release preparation, signing and publication.
The [delivery inventory](forgejo.md#what-ships) lists the workflows, actions and
signer image definition. Lint and build are actions the consumer's own jobs
call, not workflows. The
[Forgejo guide](forgejo.md) is its manual, and its
[comparison](#delivery-layers-side-by-side) sets the layer beside the
other two. The rest of this section is about the CLI's Forgejo adapter.

**Runner conventions.** A Forgejo runner also sets `GITHUB_ACTIONS=true`, so
detection probes Forgejo first, as [Detection and
overrides](#detection-and-overrides) lists; `--provider forgejo` and
`--runner forgejo` force it when a job's environment is ambiguous. On that
runner the adapter:

- writes outputs to `FORGEJO_OUTPUT`, falling back to `GITHUB_OUTPUT`;
- routes step summaries to the job log, since Forgejo renders none;
- prints plain `Error:` lines instead of `::error::` annotations;
- speaks Forgejo's actions-artifact protocol for `artifact upload`,
  `download` and `digest`, v1 upload quirks included, and accepts only the
  current run's artifacts, so producing and consuming jobs stay
  `needs:`-chained inside one workflow run.

**Publishing packages needs a token you supply. The automatic one is not
enough.** A Forgejo Actions job gets an automatic token as `$FORGEJO_TOKEN` /
`$GITHUB_TOKEN`, but on `push` events it carries
[read permission to the repository](https://forgejo.org/docs/latest/user/actions/reference/),
and the package registry refuses it: a write answers 401/403. So
`publish forge-packages deploy` fails with the automatic token, and the job must
supply one with `write:package`:

```yaml
- name: publish to the forge package registry
  env:
    FORGEJO_TOKEN: ${{ secrets.PACKAGE_TOKEN }}   # needs write:package
  run: forge-ci publish forge-packages deploy --project-type npm
```

The container registry is different: the automatic token *does* authenticate
there for reads, so `container login` needs no extra secret. GitLab needs
nothing in either case: `$CI_JOB_TOKEN` carries package-write for the project
that issued it. `PAR-PKG-1` and `PAR-REG-5` check both against real runners.

Releases + asset upload via the Gitea `/api/v1` surface (official Gitea Go
SDK). Auth precedence `$CI_TOKEN` → `$FORGEJO_TOKEN` → `$GITEA_TOKEN` →
`$GITHUB_TOKEN` → `$CI_JOB_TOKEN`; server from `$CI_SERVER_URL` →
`$FORGEJO_SERVER_URL` → `$FORGEJO_SERVER` → `$GITHUB_SERVER_URL`.
There is no default: Forgejo is self-hosted, so a run that names no server is
refused instead of sent to an
assumed host. Precedence is only half the rule: a token is handed over only
if it may be sent to the server being called. A token the current runner
injected is valid only at that runner's own server, so on a GitHub runner
publishing to a Forgejo instance, `$GITHUB_TOKEN` is GitHub's job token and is
withheld rather than transmitted. A token the runner does not inject (a
workflow that sets `$FORGEJO_TOKEN` on GitHub, or a dedicated `$RELEASE_TOKEN`)
was set deliberately and is used as asked.
No SARIF ([forgejo#3669](https://codeberg.org/forgejo/forgejo/issues/3669)).
Forgejo **v15.0+** issues OIDC id-tokens (job-level
[`enable-openid-connect`](https://forgejo.org/docs/v15.0/user/actions/security-openid-connect/)),
but public Fulcio does not trust a Forgejo issuer. So keyless signing works,
against a Fulcio configured to trust the instance, with an explicit
`--oidc-issuer` and `--fulcio-url`, but is not the default. Without those,
use key-based signing. That is the distinction the two keyless rows in the
matrix above draw. Has a provenance profile (`.forgejo/workflows/`).

### local
The dev/test fallback. There is no forge API, so operations requiring release
or token API roles return a typed "unsupported on platform local" error.
SARIF upload is an explicit exception: it reports a skip and succeeds.
Forge-independent commands remain available on a laptop.

## Implementation status

Honest maturity per forge, so you know what to rely on:

| Forge | Status |
|---|---|
| **github** | ✅ Established production path (the original target). |
| **gitlab** | 🟡 Partial: release creation, asset upload/linking (project uploads + release links), token validation, and repo metadata; **no** SARIF ingestion (GitLab consumes the JSON SAST report instead) and no GitLab-specific provenance profile (the generic one applies). |
| **forgejo** | 🟢 Adapter fully implemented (release create + asset upload, token + bot-permission probes, repo metadata, capabilities), unit-tested against an httptest Gitea server, and exercised by the guarded live conformance tier. The delivery layer under `.forgejo/` covers the lint gate, Go and Cargo builds, containers, release preparation, signing and publication; other ecosystems, forge packages and version bumps are not in it. It requires Forgejo v16+ for `workflow_call` job expansion. |
| **local** | ✅ Dev/test fallback; required forge-API operations refuse. Optional SARIF upload reports a skip. |

The matrix describes implemented adapter capabilities; its static guard checks
that the published values match the adapters. Live conformance adds evidence
for the providers and scenarios it exercises, subject to the
[coverage limits above](#capability-matrix). Workflow and Catalog delivery need
their own acceptance evidence. API resemblance and fake tests alone do not
establish end-to-end live support. Unsupported capabilities must be reported
explicitly rather than silently emulated.

Cross-forge verbs status:

- `release provenance` (generate) and `container ledger {add,validate}`: pure,
  golden/table-tested, ready.
- `release provenance --key` and `container ledger {validate-digests,promote}`:
  fake-tested for logic. The ledger operations talk to the registry in-process via
  go-containerregistry (daemonless, no docker); cross-registry promotion and
  `release provenance --key` additionally shell out to `cosign`.
  `container ledger {cleanup,rollback}` delete staging and pointer tags through
  the **forge's own package/registry API** (a `TagDeleter` provider role;
  Forgejo via the Gitea SDK, GitLab via the project registry API), *not* skopeo:
  staging and final tags share one manifest, so an OCI manifest-delete would
  destroy the promoted image. Both forge APIs are tag-scoped and keep the
  manifest. Cleanup is therefore forge-gated; github and local have no deleter
  yet and refuse with a typed `unsupported` error rather than reaching for an
  unsafe generic delete.
  `container base-images cleanup` and `prune` also support `--local-registry`.
  That path uses tag-addressed OCI DELETE requests with a conservative shared-tag
  precheck and no digest fallback; the registry must support the operation.
  See [registry ports](flows.md#ports) and [base-image retention](flows.md#base-image-retention).
- **Untagged manifests are not retained everywhere.** GitLab keeps a manifest
  reachable by digest after its last tag moves away; Forgejo drops it
  immediately. This decides whether a release is recoverable: `container ledger
  rollback --journal` restores a moving tag (`:stable`) to the digest it held
  before, and on Forgejo that previous image survives only while some tag still
  references it, in practice its own immutable `:<version>` tag. Keep version
  tags for as long as you may want to roll back to them. When the previous image
  is genuinely gone, rollback says so and fails permanently; it does not report a
  transient error that would have CI retry.

## Forge-agnostic commands worth knowing

These work the same on every forge (the forge-specific plumbing is hidden):

- `release provenance`: generate an in-toto / SLSA-v1.0 provenance
  statement from a checksums file and CI context, optionally signed with
  `cosign sign-blob`. With no method or key configured, the output is unsigned.
  Signing requires a file output; see the
  [method/key options](cli-reference.md#forge-ci-release-provenance).
  `--workflow` names the calling workflow file and must be a relative path
  with no `..` step and no control character; any other value is a usage
  error and no statement is written.
  `--ref-name` and `--source-sha` state the source ref and commit for a job
  that runs on another ref than the one it attests, such as a release request
  that signs the release tag. They replace the run's own ref name and commit
  in the statement for both profiles; without them the statement is unchanged.
  The repository, the invocation and the generic builder stay the runner's
  own, while the `forgejo-actions` builder ID names the workflow at the source
  ref and so follows `--ref-name`. A flag that is set is never replaced by the
  environment: an empty or malformed value is refused and no statement is
  written.
- `container ledger`: the digest-first release-image ledger. `validate` checks
  entries against release-tag rules offline; `validate-digests` checks recorded
  digests against the registry. See the
  [ledger command reference](cli-reference.md#forge-ci-container-ledger) for
  recording, signing, promotion, cleanup and rollback operations.
