# Workflow Design Policy

This document defines the preferred structure for GitHub Actions workflows in this repository.

It does not apply to the Forgejo layer under `.forgejo/`, which is shaped by
two different constraints and is deliberately flat. Forgejo resolves a nested
relative `uses:` against the calling repository, so there are no orchestrators
and no stage workflows there; and each privileged job repeats its own trust
establishment in plain sight, which the "move noisy logic out of YAML" goal
below must not be read as a reason to hide. Its rules are in the
[Forgejo guide](forgejo.md#signing-boundary) and are held by
`test/forgejoguard`.

## Goals

- keep public workflow contracts stable
- keep orchestrators readable
- avoid mega-workflows
- move implementation logic out of YAML when it becomes noisy
- make workflow refactors predictable and safe

## Public And Internal Boundaries

- Public high-level entry points should stay stable.
- Internal helper workflows may still be used directly by advanced consumers, but they should be treated as lower-level building blocks.
- Do not rename public orchestrator workflow files casually.

## Preferred Workflow Shape

For large orchestrators, prefer this structure:

1. `parse-config`
2. validation and release-preparation stages
3. stage-level execution jobs such as `execute-build-stage` and `execute-publish-stage`
4. release creation
5. summary jobs

The workflow should read like a pipeline, not a policy engine.

For large release flows, the public orchestrator should act as a control plane and delegate ecosystem fanout to stage-level reusable workflows.

## Workflow Structure

The workflow layer is organized into public orchestrators, stage helpers, and
focused leaf helpers for validation, release, build, publish, quality, and
security work. [Components Reference](components.md) is the authoritative file
inventory and direct-call contract; do not copy that list into design policy.

Orchestrators should remain thin and orchestration-focused.
Helper workflows should do the lower-level build, publish, release, validation, lint, and security work.

Stage-level reusable workflows are preferred when they make the public orchestrator materially easier to scan without turning contracts into giant generic input maps.
This applies to the lighter snapshot flow too, even when it intentionally skips the full production policy layer.

## What Belongs In Planning Logic

Planning logic is for global decisions such as:

- release creation gating
- latest-release decisions
- draft-release decisions
- version-bump gating
- authorization gating
- SBOM/signing policy decisions
- target membership and run gates for stage-level workflows
- compact matrix item payloads derived from `artifacts.yml`

Planning logic should not absorb GitHub Actions topology or ecosystem command
arguments. It may own matrix item contracts when those items are policy-derived
or when naming/download contracts must stay consistent across stages.

## What Should Stay Inline

Keep these inline in workflows:

- per-artifact build defaults
- per-container publish defaults
- ecosystem-specific arguments
- one-off workflow-call wiring that is clearer inline

## Script Extraction Rules

Prefer an existing CLI verb for reusable business logic. For shell-specific
bootstrap or job glue, extract a script when one or more of these are true:

- the shell block is long enough to hide workflow intent
- the same shell pattern appears in multiple workflows
- the logic is easier to test as a script

Do not extract shell just to reduce line count.

## Shared Logic Structure

Treat the `forge-ci` binary as the real module system for workflow
implementation logic.

Guideline:

- workflows decide what runs
- `internal/app` decides how logic is computed or reported
- `internal/domain` holds provider-neutral rules and data shapes
- `internal/adapters` owns external tools and platform APIs
- `internal/cli/commands` is the only workflow-facing command surface

The same line runs between the engine and whoever calls it. The engine owns
the rules about release material: what a release file manifest, a checksums
file, an image ledger and a provenance statement must be before anything is
signed, promoted or published. It knows no consumer beyond its provider
adapters. Callers own orchestration and policy: which verbs run, in what order
and in which job, with which secrets, and what they expect of a repository or
a workflow.

## Bootstrap Script Rules

Shell supports image-build and plain-runner bootstrap plus CI/live tooling;
[Scripts](scripts.md) owns the current inventory. For shell helpers:

- use `set -euo pipefail`
- keep scripts single-purpose
- prefer helper functions plus `main()`
- keep side effects explicit
- add targeted Go black-box tests when practical

## Anchor Rules

- use anchors only when they remove real duplication
- keep anchors local and obvious
- avoid anchors for permission blocks
- avoid YAML merge-key patterns
- inline low-value anchors when they add more indirection than value

## Reusable Workflow Rules

- workflow files under `.github/workflows/` use the `.yml` extension; `.yaml`
  is rejected by the default-tier `TestWorkflowFilesUseYMLExtension` guard
- only pass declared `workflow_call` inputs
- keep public contracts stable unless intentionally versioned
- prefer explicit job flow over generic abstraction
- prefer small stage contracts over many per-target cross-stage outputs
- prefer structured stage payloads when they reduce top-level dependency sprawl
- privileged credential use must follow a successful `forge-ci validate
  event-context` gate. Prefer an early local guard after trusted bootstrap,
  before the steps receiving secrets. The parsed guard also accepts a required,
  successful guard-job dependency, including a validated transitive dependency.
  Conditions such as `always()` must not bypass a failed guard.
- `TestPrivilegedWorkflowsHaveEventContextGuard` checks privileged uses and their
  guard ordering/dependencies. Its documented exceptions cover public verification
  material and PR Code Scanning use; exemption is not proof of confidentiality.
  A forwarder-only workflow may be added to `forwarderWorkflows` with a reason,
  but it may not consume privileged values directly. A job-level secret needs
  a successful upstream gate before the job starts.

## Third-Party Action Rules

**Goal: node-less, supply-chain-minimal, forge-portable.** Behaviour lives in
the `forge-ci` Go binary (and a few audited bootstrap scripts), not in
Marketplace JavaScript actions. Three things follow from one move:

- **Supply chain:** every action removed is one fewer third-party release,
  Renovate edge, and opaque step running with the workflow's secrets.
- **Forge portability:** shared verbs reuse provider-neutral logic, while
  adapters implement supported roles and runner conventions. Availability and
  delivery still follow the [provider contracts](providers.md).
- **Security:** auditable Go with tests, primitive port signatures, and secrets
  kept out of argv, rather than transitive JS dependency trees.

The target is **not zero actions**. Retain platform glue where its runner
integration is needed, including GitHub cache and artifact-upload actions.
The upload CLI does exist; the shipped GitHub workflows use the action for its
authenticated runner context, as described below. This is a delivery choice,
not a claim that other forges lack equivalent artifact operations.
Signing and SLSA/SBOM attestation are verbs (`container sign`, `container
attest`), portable and verifiable with cosign on any forge, with no GitHub
attestation-API dependency. Prefer an existing verb when it supplies the needed
operation and authentication context.

The binary must be on `PATH` before any verb runs, which is purely a matter of
HOW the job gets it:

- **Jobs running in a forge-ci runtime image** (`container: <runtime-image>`)
  have the binary **baked in**, so it is present from step 1, even the *first*
  checkout can be `platform checkout`, no `actions/checkout` at all.
- **Bare-runner jobs** must obtain the verified binary before using it.
  Bootstrap may use a reviewed helper checkout or pinned installer scripts.
  A consumer checkout can be separate from the helper checkout; there is no
  universal checkout count. Follow [installation guidance](providers.md#installing-forge-ci)
  and the [current component contract](components.md).

So the right default for a node-less job is to run it in the runtime image.

Every external action (`uses: <owner>/<repo>@...`) is supply-chain surface
area: a compromised release, an unreviewed transitive dependency, or an
opaque shell step running with the workflow's secrets and `GITHUB_TOKEN`.
Treat each one as an explicit risk decision, not a convenience.

- prefer a direct `forge-ci` command or a `scripts/bootstrap/install-<tool>.sh`
  helper over a Marketplace action that wraps the same binary
- prefer baking tools into the runtime container over installing them per
  job: fewer downloads, one audit point
- when an action **must** be used, pin to a full commit SHA with a version
  comment, never a floating tag or `@main`
- before adding a new action, check if it duplicates something already
  available via a runtime-container binary, a `scripts/bootstrap/install-*.sh`
  helper, or a few lines of bash
- removing an action is a feature: smaller attack surface, fewer
  Renovate edges, fewer breakages on upstream rewrites
- apply the same CLI-first preference to GitLab, while keeping native artifact
  handoffs, variable scopes, supported capabilities and pipeline topology explicit

The image build has a CLI verb (`container build`). Other action uses should
justify their own bootstrap, toolchain or platform role during review.

Current CLI alternatives, subject to their provider and runtime contracts:
- `docker/setup-buildx-action` + `docker/build-push-action` → `container build`
  (buildah builds; the ggcr adapter pushes tagless by digest, daemonless, no
  BuildKit and no GitHub-marketplace action; the multi-arch index is assembled by
  `container manifest merge`).
- `github/codeql-action/upload-sarif` → `security report upload-sarif` (stamps
  the category into each run's `automationDetails.id`, the field Code Scanning
  keys analyses on).
- `docker/login-action` → `container login` (writes the shared `{"auths":…}`
  config read by docker, podman/buildah, skopeo, and cosign).
- `actions/download-artifact` → `artifact download` (node-less, forge-portable).
  Download uses the GitHub REST artifacts API, which a `run:` step *can* reach, so
  the CLI verb stays. Flag mapping via `env:`: `name:`→`$ARTIFACT_NAME`; `path:`→
  `$ARTIFACT_DIR`; `pattern:`→`$ARTIFACT_PATTERN`; `merge-multiple:`→
  `$ARTIFACT_MERGE_MULTIPLE`. The REST call needs auth, and `$GITHUB_TOKEN` is
  **not** auto-present in run steps, so each download step passes
  `GH_TOKEN: ${{ github.token }}`.
- GitHub workflows retain `actions/upload-artifact` for **upload**. The CLI also
  implements that transport, but it needs the Actions "results" credentials.
  Upload goes through that service, gated by
  `ACTIONS_RUNTIME_TOKEN`, which the runner injects into *actions* but **never into
  `run:` steps**: a run-step CLI cannot authenticate (and there is no REST upload
  endpoint). The `forge-ci artifact upload` verb still exists for Forgejo (whose
  runner *does* expose the runtime token to run steps) and for interop with the v4
  store, but every GitHub workflow uploads via the action. `with:` mapping is 1:1:
  `name`, `path` (glob `*`/`**`/`[set]`/`!exclude`, multi-line), `if-no-files-found`,
  `retention-days`, `include-hidden-files`.

Checkout boundary:
- `actions/checkout` → `platform checkout` (`--fetch-tags`, `--sparse` cone +
  partial clone, `--path`). Runtime-image jobs may use the verb, including for
  their first checkout. A bare-runner bootstrap must first make the binary
  available through its reviewed installer/checkout path (see above).

## Validation Expectations

For workflow refactors, run at least:

- `actionlint .github/workflows/*.yml`
- YAML parsing of all workflows
- reusable-workflow input compatibility checks
- `bash -n` for touched bootstrap scripts
- relevant Go tests when binary behavior is added or changed

## Stop Rule

Stop refactoring when abstraction starts making the workflow harder to read.

Maintainability wins come from clearer structure, not from maximum indirection.
