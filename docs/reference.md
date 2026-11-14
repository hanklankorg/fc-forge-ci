# Secrets, Permissions, and Validation

Common secrets, caller permissions, and validation boundaries for the GitHub
reusable workflows. For other forges, see the [GitLab](gitlab.md#secrets-and-tokens)
and [Forgejo](forgejo.md#secrets-and-tokens) guides.
For the generated command surface, see [CLI Reference](cli-reference.md).

## Canonical command environment names

These names are consumed by the CLI inside a job. Repository variables do
not automatically become process environment variables: a workflow maps its
inputs, repository variables and secrets to the step environment or CLI flags.
The generic publishing examples use `REGISTRY_USER` at both boundaries;
release workflows map their repository variable `RELEASE_REGISTRY_USER` to it.

| Purpose | Canonical name | Default when absent |
|---|---|---|
| Registry login user | `REGISTRY_USER` | No guessed user for external registries; release-image planning can derive the owner for the forge's own registry |
| Release-image ledger file | `RELEASE_IMAGES_PATH` | `dist/release-images.json`; release-image operations resolve it beneath their selected dist directory |
| Changelog file | `CHANGELOG_FILE` | `CHANGELOG.md` for rendering/commit operations; validation requires a file |
| Whitespace-separated mise tool selection | `TOOLSET_TOOLS` | The tools declared by the project |

Explicit flags take precedence; a plan-backed flag resolves flag, plan field,
environment, then default. The repository pair `RELEASE_REGISTRY_USER` /
`RELEASE_REGISTRY_TOKEN` maps to the CLI's `REGISTRY_USER` / `REGISTRY_TOKEN`
only in the jobs that need it. `REGISTRY_PASSWORD` is an alternative password
input, not a second required secret.

## Environment Variables Matrix

| Variable/Secret | Required For | When Checked | Expected Value | Notes |
|-----------------|--------------|--------------|----------------|--------|
| **GITHUB_TOKEN** | GitHub checkout, run artifacts and package operations | When used by the job | Automatic job token | Permissions are scoped by the workflow; see below |
| **RELEASE_TOKEN** | Production release preparation and publication | Release preflight and later Git/API operations | Fine-grained PAT with access to the target repository | Bot token for pushing prepared commits/tags and creating releases |
| **RELEASE_GPG_PUBLIC_KEY** | GPG preflight and tag verification | Plan-gated presence check and applicable signature checks | Armored OpenPGP public key | Presence alone does not establish key validity or expiration status |
| **RELEASE_GPG_PRIVATE_KEY** | Selected GPG signing paths, including GPG-based publishers | Key import and signing | Armored OpenPGP private key, optionally base64-encoded | Needed when artifact/git-object signing or the publisher uses GPG |
| **RELEASE_GPG_PASSPHRASE** | Selected GPG key/operation | Key import and signing | Key passphrase | Supply as required by the key and selected operation |
| **RELEASE_SSH_SIGNING_KEY** | `git-signing.method: ssh` | Release commit/tag preparation | OpenSSH private key | Separate from artifact-signing credentials |
| **MAVEN_CENTRAL_USERNAME** | Maven Central publishing | Presence in preflight; authentication during deploy | Sonatype publishing username | Configure the project's Maven settings/plugin for deployment |
| **MAVEN_CENTRAL_PASSWORD** | Maven Central publishing | Presence in preflight; authentication during deploy | Sonatype publishing token | Not the account password |
| **CODE_SCANNING_TOKEN** | Optional Code Scanning upload | SARIF upload | Token with Code Scanning write access | Scan execution and report upload are separate |
| **NPM_TOKEN** | Unused: `publish-to: npmjs` is refused | Not checked | — | npm publishing uses GitHub Packages and `GITHUB_TOKEN` |

Requirements follow the selected signing methods and publishing targets. A
workflow secret declared `required: false` can still be required by a selected
operation. See [artifact signing](verification.md#signing-methods-for-release-artifacts),
[Git-object signing](verification.md#signing-the-release-commit--tag-git-objects),
and the [publisher-specific credentials](publishing.md).

Release authorisation is not a secret. It lives in the repo as
`.forge-ci/allowed_signers` (SSH) and
`.forge-ci/allowed_gpg_keys.asc` (GPG). See [verification.md](verification.md#release-authorisation).

## Prerequisites Check Matrix

The production workflow runs these checks before build/publish jobs. The
[prerequisite workflow](../.github/workflows/validate-release-prerequisites.yml)
selects target-specific checks from the resolved plan.

| Check | When Performed | What It Validates | Fails If | How to Fix |
|-------|----------------|-------------------|----------|------------|
| **Release ref and tags** | Production setup and preflight | Request format, tag uniqueness and commit relationship to the target branch | Unsupported ref/tag, conflicting tags or wrong commit | Follow the [release-request contract](verification.md#request--promote-release-tags-are-immutable) |
| **Tag signature and authorization** | Tag preflight | Annotated tag with a signature and the configured verification/allowlist policy | Unsigned/lightweight tag or required verification/allowlist failure | Configure the [committed signer policy](verification.md#release-authorisation) |
| **Existing final tag** | Exact-tag recovery | Release bump parent, branch head and valid final-tag signature | Existing immutable identity disagrees | Follow [failed-release recovery](failed-release-recovery.md#final-git-tag-exists) |
| **GPG key configuration** | When artifact or Git-object signing selects GPG | Public-key presence; the workflow separately imports the private key | Required material is missing or import fails | Supply the selected key material in a supported encoding |
| **Release token and bot access** | Release preflight | Token acceptance and user/repository access through API probes, and push access where the forge reports it (GitHub and Forgejo `permissions.push`, GitLab Developer role or higher); branch listing is also probed | Token policy rejection, failed user/repository access, a forge that reports no push access, or API error | Give the bot write access to the repository; push access the forge does not report (GitHub App tokens, roles inherited from GitLab ancestor groups) and branch-listing refusal are not refused |
| **Maven Central credentials** | A Maven Central target is planned | Username and token are nonempty | Either value is blank | Configure both publishing secrets; deploy performs authentication |
| **Cargo prerequisites** | A Cargo artifact is planned | Applicable toolchain and manifest requirements | The required toolchain or project inputs are unavailable/invalid | Follow [Cargo support](ecosystems.md) |
| **JVM reproducibility** | A Maven, Gradle or Gradle Android artifact is planned | Required settings in each registered artifact's manifest | Required timestamp/archive settings are absent | Follow [JVM preflight guidance](verification.md#what-validate-jvm-reproducibility-does) |

Presence/import checks are not key-expiration or remote Maven authentication
probes. Read-only forge probes do not establish protected-branch bypass rights,
registry push rights or successful future release mutations. Containerfile,
artifact and upload checks occur at their build/publish boundaries.

Release preparation updates project versions; prerequisite success does not
mean project files already contain the requested release version. The release
preflight also does not assess dependency-license compatibility. PR lint
coverage follows the [configured lint policy](verification.md#linting).

## Permission Requirements Matrix

The caller must permit the scopes declared by the selected reusable workflow.
These are the aggregate declarations of the current orchestrators; each job
narrows its own permissions, and a selected backend may not use every grant.

| Workflow | Caller permissions | Purpose |
|----------|--------------------|---------|
| [PR](../.github/workflows/pullrequest-orchestrator.yml) | `contents: read`, `actions: read` | Checkout and stage-result/summary artifact downloads |
| [Release](../.github/workflows/release-orchestrator.yml) | `contents: write`, `packages: write`, `id-token: write`, `actions: read` | Declared release-job permissions, package operations, keyless OIDC and cross-job artifact downloads |
| [Snapshot](../.github/workflows/release-snapshot-orchestrator.yml) | `contents: read`, `packages: write`, `actions: read` | Checkout, npm snapshot publication and artifact downloads |

`permissions:` scopes the automatic `GITHUB_TOKEN`; it does not grant access to
a separately supplied PAT, App token or registry credential. Those credentials
need their own permissions. An explicit `secrets:` mapping passes secret values
and belongs outside `permissions:`; `secrets: inherit` passes every secret, and
the default lint gate's SAST check rejects it.

`id-token: write` enables keyless OIDC signing, not provenance generation in
general. Container provenance and SBOM attestations use cosign and the registry;
these orchestrators do not declare GitHub's `attestations: write` permission.
For direct component calls, use that component's declared permissions.

## Getting Access to Secrets

### How Secrets Work

Create the secrets required by your resolved release plan as repository or
organization secrets in GitHub. Production releases use `RELEASE_TOKEN`.
Artifact and Git-object signing both default to GPG, but their methods are
configured independently. Supply GPG credentials when a selected signing or
publishing path uses them; SSH Git-object signing uses
`RELEASE_SSH_SIGNING_KEY`. Sigstore/KMS artifact signing has its own
[backend requirements](verification.md#signing-methods-for-release-artifacts).

The caller passes them by name in an explicit `secrets:` mapping. Other
publishers have their own [credential requirements](publishing.md).

### RELEASE_TOKEN

Used for pushing prepared commits and immutable final tags, and for creating
GitHub releases.

Use a fine-grained PAT with `contents: write` permission, scoped to the target
repositories. Classic PATs are rejected. The token must pass the preflight API
probes and be authorized for the later Git/API mutations, including applicable
branch-protection rules; recognizing its prefix is not proof of those rights.

Note: GitHub Packages uploads use `GITHUB_TOKEN` (automatic, no configuration needed).

### CODE_SCANNING_TOKEN

Used for uploading security scan results (SARIF) to GitHub Security / Code Scanning. Without this token, scans still run and SARIF is still generated. The results will not appear in Security / Code Scanning.

**Option A, a GitHub App (recommended):**
- Create a GitHub App with the repository permission "Code scanning alerts" set to **Write**
- Install on target repositories
- Have the caller's authentication setup generate a fresh installation token
  for each run and pass it as `CODE_SCANNING_TOKEN`

**Option B, a fine-grained PAT:**
- Create a fine-grained PAT with "Code scanning alerts" set to **Write**
- Scope to the target repositories
- Store as repository or organization secret `CODE_SCANNING_TOKEN`

Pass the token through an explicit `secrets:` mapping.
Installation-token generation and renewal belong to the caller's authentication
setup; the reusable lint workflow consumes the supplied token.

**Code Scanning categories:**

Results appear in the Code Scanning tab grouped by category:

| Category | Scanner | When |
|----------|---------|------|
| `nanolinter-security` | nanolinter (OpenGrep SAST, OSV, secrets, trivy-fs) | PR lint, default `lint-engine` |
| `megalinter-security` | MegaLinter | PR lint with `lint-engine: megalinter` |
| `scorecard` | OpenSSF Scorecard | Scheduled analysis |
| `container-scan-<name>-<arch>` | Trivy | Release container build, one per image and architecture |

The two lint categories are the `sarif-category` defaults of `lint-nanolinter.yml` and `lint-megalinter.yml`. The Scorecard and container-scan SARIF files are also saved as workflow artifacts (`sarif-scorecard`, `sarif-container-scan-<name>-<arch>`) regardless of whether the token is configured; the lint engines upload their SARIF to Code Scanning without saving it as an artifact.

## Version Tag Format

A release is requested with a signed `release-request/vMAJOR.MINOR.PATCH` tag
and produces the final `vMAJOR.MINOR.PATCH` tag. Prereleases need
`release-policy: stable-prerelease`; see
[release policies](workflows.md#stable-and-prerelease-policy).

---

## Validation

Configuration parsing and release-request validation are separate setup steps:

1. **Resolve configuration** - Read the configured file. If it is absent, a
   supported single-ecosystem root can [auto-derive a plan](artifacts-reference.md#auto-derive-skip-the-file-for-simple-repos).
   Missing or ambiguous root manifests refuse. An existing empty, malformed or
   unreadable config does not select this fallback.
2. **Validate the artifact set** - Require at least one artifact and validate
   supported project types, publishing combinations and declared container
   references. YAML decoding rejects unknown keys and wrong value types.
3. **Resolve defaults** - Derive SBOM selections and container fields. See
   [Artifacts Reference](artifacts-reference.md) for defaults and constraints.
4. **Validate the release request** - With the default `release-policy: stable`,
   only `release-request/vMAJOR.MINOR.PATCH` is accepted; `stable-prerelease`
   also admits numbered beta and RC versions. Any other tag fails before
   anything is changed.

The selected release preflight checks are described in the matrix above.
