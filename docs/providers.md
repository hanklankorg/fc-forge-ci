# Providers and runners

The `forge-ci` CLI is forge-agnostic: the same verbs run on GitHub
Actions, GitLab CI, Forgejo Actions, and on a developer laptop. This page
explains how the CLI decides which forge it is talking to, how to override
that, and which features are available where.

## Two axes: runner conventions vs forge API

"Which CI platform" is really two independent things:

- **Runner conventions**: how the CLI writes output. The workflow-command
  dialect (`::error::`), the `$*_OUTPUT` key/value writes, and the step-summary
  file.
- **Forge API**: the server API used for releases, asset upload, token checks,
  repository metadata, SARIF and run artifacts.

Forgejo differs from GitHub on both. Forgejo Actions sets
`GITHUB_ACTIONS=true`, but it does not render `::error::` annotations or
`::group::` folds, its step-output file is `$FORGEJO_OUTPUT`, and it renders
job summaries only from Forgejo 17 with Runner 13.2, through
`$FORGEJO_STEP_SUMMARY`. So on Forgejo the CLI prints plain `Error: …` lines
and writes summaries to the summary file where Forgejo sets one, and to the
job log where it does not.

| Forge | Runner | Forge API |
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
| Forgejo | `FORGEJO_STEP_SUMMARY`, then `GITHUB_STEP_SUMMARY`; otherwise the job log |
| GitLab | A job-log section for every summary, plus `CI_SUMMARY_FILE` when set |
| Local | No summary destination; summary appends are no-ops |

GitLab renders no summary file, so every summary also appears in the job log
as its own foldable "Summary" section. To keep it as a file too, set
`CI_SUMMARY_FILE` to a file such as `summary.md`, ensure its parent directory
exists, and declare it under the job's `artifacts.paths`, as the build
templates do. Use `artifacts.when: always` when failure summaries must also be
uploaded. The CLI appends Markdown; the workflow owns publication and retention.
`CI_OUTPUT` is the separate dotenv destination for scalar outputs. Structured
job/stage records use the [file-based report handoff](workflows.md#file-based-report-handoff).

## Installing `forge-ci`

No GitHub workflow installs forge-ci at run time. Container jobs run in a
forge-ci runtime image pinned by digest, which carries the engine. Linux host
jobs receive the CLI that the run's `canonical-cli` job builds from the pinned
engine commit, and macOS jobs build it from that commit themselves. Every
workflow takes `forge-ci-binary-ref` as the full 40-hex engine commit; tags,
branches and short hashes are refused. [Engine delivery](engine-delivery.md)
describes each path. A direct caller of a leaf workflow that needs the
canonical CLI adds a `canonical-cli` job of its own, as the
[publish-container example](publishing.md#github-container-registry-ghcrio)
shows.

A person installs the CLI from a GitHub release and verifies `checksums.txt`
against its Sigstore bundle, as [verification](verification.md) describes.

On Forgejo, and in GitLab's checksum-bound components, a job gets the binary
from the delivery layer: the binary the pinned commit records, downloaded
from the instance's package registry and checked against its SHA-256. On
Forgejo the `setup-forge-ci` action does this; see the
[Forgejo guide](forgejo.md#installing-the-binary-in-a-job). Where a job
cannot run that action, use a prebuilt runtime image. Do not download and
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

Names are never silently changed. Source spelling is kept, and a name with
empty or relative segments, URL escapes or control characters is refused.

Container destinations follow OCI rules:

- `MixedOwner/My.Repo` becomes `ghcr.io/mixedowner/my.repo` on GHCR.
- `Group/Sub/My_App` keeps its full namespace on the selected registry.
- `CONTAINER_NAME` / `--name` adds a subpath unless it equals the repository
  name after lowercasing.

A source name such as `.github` is not a valid OCI component: set an explicit
`image-name` and `expected-image-repository` in `publish-container.yml`. The
[CLI reference](cli-reference.md) has the `container ref resolve` and
`container validate namespace` commands that check a name.

GitHub and Forgejo package adapters lowercase npm scopes. GitHub also lowercases
the Maven owner coordinate. Source repository identities retain their spelling.

## Git operation credentials

Git checkout, changelog and release commands read an explicit token from
`--token-file <path>`, or `--token-file -` for stdin; there is no `--token`
flag. Without it, the token comes from the environment, with `RELEASE_TOKEN`
preferred on commands that write a release. A token the runner issued is sent
only to its own server. An empty token file means "no token", not "fall back
to the environment".

On GitLab CI, `CI_JOB_TOKEN` is the last fallback, after `CI_TOKEN` and the
Forgejo and GitHub token names. It is sent only to `CI_SERVER_URL`, with the
`gitlab-ci-token` user name GitLab's HTTPS Git service needs. No token is
written to a remote URL or a stored Git setting. See the
[CLI reference](cli-reference.md) for each command's inputs.

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

Each row describes the forge's hosted instance; a self-hosted one can differ.
Keyless signing with public Fulcio works only on github.com and gitlab.com;
on GitHub Enterprise Server or a self-hosted GitLab, `doctor` reports it as
unavailable.

Tag deletion (`container release-images rollback` and `cleanup`) takes its token
from `--provider-token-file`, then `$FORGE_CI_PACKAGE_API_TOKEN`, then the
detected forge's own variable: `$FORGEJO_TOKEN` or `$GITEA_TOKEN` on Forgejo,
`$GITLAB_TOKEN` on GitLab. Another forge's token is not read, `$GITHUB_TOKEN` on
a Forgejo runner included; without one of these the command refuses before it
calls the forge.

A refusal for a missing capability exits **78** (`EX_CONFIG`): change the
configuration or the forge. Exit **69** (`EX_UNAVAILABLE`) means only that a
registry, forge or download host could not be reached or was overloaded, and
is worth retrying. A tool missing from the job also exits 78, since a retry
finds the same job.

Behavior when a capability is absent:

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
  `--profile forgejo-actions` takes the source and builder identity from the
  Forgejo provider instead.
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
them differ too. The [GitHub](github.md), [GitLab](gitlab.md) and
[Forgejo](forgejo.md) guides have the entry points, and
[Admin Policy](admin-policy.md) how an administrator attaches policy on each.

| Capability | GitHub | GitLab | Forgejo |
|---|---|---|---|
| Lint gate with a mandated floor | `lint-nanolinter.yml` | `nanolinter` component | `lint-nanolinter` |
| MegaLinter as the lint engine | `lint-megalinter.yml` | `megalinter` component | none |
| Findings in the forge's interface | Code Scanning (SARIF) | merge-request Security tab (SAST report; display needs Ultimate) | none: Forgejo ingests no report; the step log |
| Release-isolation gate | none | none: the signing policy is a separate pinned project | `check-l3-isolation` |
| Build, Go and Cargo | `build-go.yml`, `build-cargo.yml` | `build-go`, `build-cargo` components | `build-go`, `build-cargo` |
| Build, Maven | `build-maven.yml` | `build-maven` component | `build-maven` |
| Build, Gradle, npm, Xcode | workflows | components, without Xcode | no action; a Gradle consumer's build job runs `forge-ci build gradle run` itself |
| Job image | runtime images | runtime images; digest-pinned images you supply for `check` and `release` | the consumer's image or `setup-toolchain` |
| Engine binary | runtime image, or the run's canonical CLI | runtime image, or the checksum-bound build for `check` and `release` | the checksum-bound build |
| Plan-driven fan-out from `artifacts.yml` | `release-build-stage.yml` | generated child pipeline | none: the consumer's workflow is the plan |
| Run-artifact transport | one ZIP blob per artifact | the runner's native `artifacts:` and `needs:` | each file through the artifact API |
| Linux image builder | Buildah on the host (default) or Kaniko in a Docker action | Buildah (default) or Kaniko | Buildah (default) or Kaniko |
| Image build, scan, ledger | `publish-container.yml` | native `release` image jobs; consumer `build` commands produce the release files | `release-images.yml` |
| Release preparation, signed tag | `release-prepare-stage.yml` | `release` component | `prepare-release.yml` |
| Release branch | `branch` input, required | `release-branch` input, the project's default branch by default | `release-branch` input, `main` by default |
| Version bump in project files | `version-bump.yml` | `release` component, planned from `artifacts.yml`: key-free version job a family, text verification, signed commit with the changelog | `prepare-release.yml`, planned from `artifacts.yml`: key-free version job a family, text verification, signed commit with the changelog |
| Signing and publication | keyless, GPG, KMS or Cosign key (`env://COSIGN_KEY`) | `release` component, separately privileged jobs, Cosign key, optional GPG signatures for distro packages | `sign-and-publish-release.yml`, separately privileged jobs, Cosign key; GPG for the checksums and SBOMs with `sign.method: gpg` or `sign.files-method: gpg`; optional GPG signatures for distro packages |
| Public transparency log | `sign.transparency`, `public` by default (keyless signing needs the log; `none` only with a key) | `transparency-log` input, `none` by default | `transparency-log` input, `none` by default |
| Image promotion | `:dev` → `:staging` → release, gated by Environments | final tags in a separate publication project | final tags (`promote` job) |
| Snapshot flow | `snapshot-images.yml`; `release-snapshot-orchestrator.yml` for npm | `snapshot-images` component | `snapshot-images.yml` |
| Forge package registry | Maven/npm registry workflows | `publish-forge-packages` | `forge-ci publish forge-packages` in your own job |
| Distro packages (`.deb`, `.rpm`, `.apk`) in a registry | refused: GitHub Packages has no such registry | refused: no RPM or Alpine registry, and the Debian registry is off by default | `distro-packages`, published by the `packages` job |
| Base-image lifecycle | CLI commands in your own workflow | `release` with `base-stages` and `base-plan`: the policy builds, signs and promotes each stage | release-image and base verify/promote workflows plus `prune-base-images`; the build graph remains caller policy |

When you move a consumer from one forge to another, these are the places
where its setup changes:

| Concern | GitHub | GitLab | Forgejo |
|---|---|---|---|
| Entry points | `.github/workflows/` | `.gitlab-ci.yml` and included components | `.forgejo/workflows/` |
| Job identity | GitHub contexts and token | CI variables and job token | Forgejo contexts and repository-scoped job token |
| Registry authority | GitHub token when granted, or separate credentials | Job or deploy token, or separate credentials | `RELEASE_REGISTRY_TOKEN`, in the jobs that publish |
| Runner routing | Hosted or self-hosted labels | Runner tags | JSON arrays of labels |

## Per-forge notes

### github
Full role set. OIDC issuer `https://token.actions.githubusercontent.com`.
Token advice on `validate auth token` (classic-PAT refusal). Releases,
assets, and SARIF go through the GitHub REST API (`go-github`).

The reusable GitHub workflows run on github.com only; see the
[GitHub guide](github.md#requirements), the delivery layer's manual.

### gitlab
Releases via `/api/v4`. OIDC issuer `$CI_SERVER_URL` (self-hosted) or
`https://gitlab.com`. No SARIF (GitLab consumes the JSON SAST report
directly). Provenance uses the `generic` profile, because there is no GitLab-specific
one, and none is needed.

The GitLab components lint, check and build merge requests, publish to the
GitLab Package Registry, Maven Central and App Store Connect, and the `release`
and `snapshot-images` components build and publish container images. A plan
that needs a component GitLab does not have is refused. The
[GitLab guide](gitlab.md) is the delivery layer's manual.

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

### forgejo

The Forgejo delivery layer under `.forgejo/` is described in the
[Forgejo guide](forgejo.md), and [Runners and runner lending](runners.md)
covers pools that cannot run rootless Buildah. This section is about the CLI's
Forgejo adapter.

**Runner conventions.** A Forgejo runner also sets `GITHUB_ACTIONS=true`, so
detection probes Forgejo first, as [Detection and
overrides](#detection-and-overrides) lists; `--provider forgejo` and
`--runner forgejo` force it when a job's environment is ambiguous. On that
runner the adapter:

- writes outputs to `FORGEJO_OUTPUT`, falling back to `GITHUB_OUTPUT`;
- writes step summaries to `FORGEJO_STEP_SUMMARY` (Forgejo 17 and later
  render them), or to the job log on older Forgejo;
- prints plain `Error:` lines instead of `::error::` annotations;
- speaks Forgejo's actions-artifact protocol for `artifact upload`,
  `download` and `digest`, v1 upload quirks included, and accepts only the
  current run's artifacts, so producing and consuming jobs stay
  `needs:`-chained inside one workflow run.

**Publishing packages needs a token you supply. The automatic one is not
enough.** A Forgejo Actions job gets an automatic token as `$FORGEJO_TOKEN` /
`$GITHUB_TOKEN`. It can write the repository, except in a fork's pull request
(see [Authorization and job tokens](forgejo.md#authorization-and-job-tokens)),
but the package registry refuses it: a write answers 401/403. So
`publish forge-packages deploy` fails with the automatic token, and the job must
supply one with `write:package`:

```yaml
- name: publish to the forge package registry
  env:
    FORGEJO_TOKEN: ${{ secrets.PACKAGE_TOKEN }}   # needs write:package
  run: forge-ci publish forge-packages deploy --project-type npm --npm-tag 'release-{version}'
```

The container registry is different: the automatic token *does* authenticate
there for reads, so `container login` needs no extra secret. GitLab needs
nothing in either case: `$CI_JOB_TOKEN` carries package-write for the project
that issued it.

Releases and asset uploads use the Gitea `/api/v1` API. The token comes from
`$CI_TOKEN`, `$FORGEJO_TOKEN`, `$GITEA_TOKEN`, `$GITHUB_TOKEN` or
`$CI_JOB_TOKEN`, the first set; the server from `$CI_SERVER_URL`,
`$FORGEJO_SERVER_URL`, `$FORGEJO_SERVER` or `$GITHUB_SERVER_URL`. A run that
names no server is refused. A token the runner injected goes only to that
runner's own server, so on a GitHub runner publishing to Forgejo,
`$GITHUB_TOKEN` is withheld. A token you set yourself, such as `$FORGEJO_TOKEN`
on GitHub or `$RELEASE_TOKEN`, is used as given.
No SARIF ([forgejo#3669](https://codeberg.org/forgejo/forgejo/issues/3669)).
Public Fulcio does not trust a Forgejo issuer, so keyless signing works only
against a Fulcio you configure to trust the instance, with an explicit
`--oidc-issuer` and `--fulcio-url`. Otherwise use key-based signing, as the
two keyless rows of the matrix show.

### local
The dev/test fallback. There is no forge API, so operations requiring release
or token API roles return a typed "unsupported on platform local" error.
SARIF upload is an explicit exception: it reports a skip and succeeds.
Forge-independent commands remain available on a laptop.

## Untagged manifests and rollback

GitLab keeps a manifest reachable by digest after its last tag moves away;
Forgejo drops it at once. This decides whether a release can be rolled back:
`container ledger rollback --journal` restores a moving tag (`:stable`) to the
digest it held before, and on Forgejo that image survives only while another
tag, in practice its own `:<version>` tag, still names it. Keep version tags
for as long as you may want to roll back to them. When the previous image is
gone, rollback says so and fails for good, without asking CI to retry.

Tag deletion in `cleanup` and `rollback` goes through the forge's own package
API, because staging and final tags share one manifest and a plain OCI
manifest delete would remove the promoted image. GitHub and local have no
such API, so these commands refuse there.

## Forge-agnostic commands worth knowing

These work the same on every forge (the forge-specific plumbing is hidden):

- `release provenance`: generate an in-toto / SLSA v1.0 provenance statement
  from a checksums file and the CI context, optionally signed with
  `cosign sign-blob`. `--ref-name` and `--source-sha` name the source when the
  job attests another ref than its own, such as a release request signing the
  release tag. See the
  [command reference](cli-reference.md#forge-ci-release-provenance).
- `container ledger`: the digest-first release-image ledger. `validate` checks
  entries against release-tag rules offline; `validate-digests` checks recorded
  digests against the registry. See the
  [ledger command reference](cli-reference.md#forge-ci-container-ledger) for
  recording, signing, promotion, cleanup and rollback operations.
