# Live runs

This page is the manual for the live tests: how to prepare them, run them
and read their result. [Testing conventions](testing.md) describes the other
test layers, and [Testing matrices](testing-matrices.md) shows what has passed.

The live tests run Forge CI against real forges: a Forgejo and a GitLab in a
local test lab (Forge Lab), and a GitHub test organization. Every other test
layer checks an adapter against a fake written from the same assumptions as
the adapter, so a wrong assumption is wrong in both places. Only a real forge
breaks that tie.

The [live ownership map](../test/live/README.md) describes the test-only tree.
`test/live/internal/livetest/` holds a kit and `test/live/conformance/` the
scenarios (`PAR-*`). One scenario body runs against every configured lab forge
that *claims* the capability it needs, so parity is enforced by shape rather
than by discipline.

The live tests are destructive and started by a person against a disposable
lab. No CI job runs them.

The lab cannot show three things, so no live test claims them:

- a SARIF upload on a private GitHub repository, which needs GitHub Advanced
  Security; the upload is tested on a public repository;
- a release refused for a real vulnerability, because every scanner needs a
  network advisory database and so gives no fixed finding offline;
- that GitLab cleanup removed more than the repository: its teardown checks
  only the repository.

## Plan before launch

Run about three lanes at once. Check the host's free memory, CPU load and disk
first, and leave room for the forges, compiler work and cleanup. Forgejo,
GitLab and hosted GitHub lanes may overlap; runs on one forge share its
runners and slow each other down. Reassess after each lane ends.

## Choose a consumer trial

All commands below run from Forge CI. Checkout and archive arguments are
absolute paths. Trials mutate disposable resources; the linked sections explain
setup, evidence and cleanup.

| Forge / event | Command | Preparation |
|---|---|---|
| Forgejo PR, release or lifecycle | `just test-live-consumer forgejo <checkout> <pull-request\|release\|lifecycle> [setting ...]` | Local `forgeci` contract and runner; consumer job image; signer image for releases; lifecycle: the Maven service demo and the container/readiness runner ([Lifecycle](#lifecycle)) |
| Forgejo snapshot | `just test-live-consumer forgejo <checkout> snapshot CI_JOB_IMAGE=<digest reference> [CI_IMAGE_BUILDER=buildah\|kaniko]` | A planned image route and the selected builder's prerequisites; [snapshot admission](#snapshot); no readiness |
| GitLab MR | `just test-live-consumer gitlab <checkout> pull-request job-archive=<image-archive>` | Local `forgeci` contract and runner; prepared MR job image archive |
| GitLab release | `just test-live-consumer gitlab <checkout> release job-archive=<archive> build-archive=<archive> signer-archive=<archive> [setting ...]` | Local contract, prepared image archives and guarded full-release runner profile |
| GitLab lifecycle | `just test-live-consumer gitlab <checkout> lifecycle job-archive=<archive> build-archive=<archive> signer-archive=<archive> lint-archive=<archive> [setting ...]` | As the release; a demo with a service snapshot and a GitLab release, `hello-maven-service` or `hello-cargo-container` ([Lifecycle](#lifecycle)) |
| GitLab snapshot | `just test-live-consumer gitlab <checkout> snapshot job-archive=<archive>` | Planned images, selected builder archive and any producer runtime archives; [snapshot admission](#snapshot); no readiness |
| GitHub PR, development build or release | `just test-live-consumer github <checkout> <pull-request\|dev-release\|release> org=<org-id> [setting ...]` | Forge Lab's hosted `github run` session, consumer workflow fixtures and public engine images |
| GitHub lifecycle | `just test-live-consumer github <checkout> lifecycle org=<org-id> [version=X.Y.Z] [image=<name>]` | As the release, plus the consumer's `release-dev.yml` fixture and the release credential; the `hello-maven-service`, `hello-cargo-container`, `hello-go-service` and `hello-npm-service` demos ([Lifecycle](#lifecycle)) |

See [Forgejo trials](#trying-a-consumer), [GitLab acceptance](#gitlab-live-acceptance)
and [GitHub trials](#trying-a-consumer-on-github). `just test-live` is the local
GitLab/Forgejo conformance suite; it does not run the hosted GitHub trial.
For a retained demonstration, use [`just demo <forge> <name>`](#showing-a-consumer).

`hello-maven` is a Maven application JAR without an image, not a library
publication fixture. `hello-maven-service` adds the runtime image required by
the service snapshot and lifecycle roads. On GitHub, that service's snapshot
uses `dev-release`, not `snapshot`; its `release-dev.yml` fixture selects the
[shared snapshot oracle](#trying-a-consumer-on-github). The plain application
ships no such development fixture. Image snapshots do not prove Maven SNAPSHOT
package publication.

A trial answers one of two questions; keep them apart.

- **Does an example work?** Run its demo, `test/live/fixtures/consumers/hello-go-cli` for
  example, whose CI files are the example filled in and kept so by a unit
  test. Do not copy an example into a scratch checkout by hand: such copies
  went live missing a release workflow or the files a release reads.
- **Does Forge CI work for this project?** Run the project's own checkout
  with its own workflows. The trial moves only the Forge CI pin to the
  candidate; never replace the project's workflows with an example's, since
  its own policy tests then rightly fail and the run proves neither.

## Running it

The tests need one input: a live-target contract, version 6, for this suite's
lab account, `forgeci`. Forge Lab writes it to
`$XDG_STATE_HOME/forge-lab/targets/forgeci.json` (`~/.local/state` when
unset), owner-only. Everything else is read from it. `LAB_TARGETS_FILE`
overrides the path.

1. Prepare the lab with Forge Lab's README and
   `docs/modules/ROOT/pages/how-to.adoc`: providers, fixtures, runners, the
   opt-in Sigstore profile, and the contract (`just targets --account forgeci
   gitlab forgejo` in Forge Lab). Fixtures and runners belong to a road
   (Compose or k3s) and are prepared once per road. A contract names one road's
   hosts, and minting one replaces and revokes the one at its path: to keep one
   per road, give each its own `LAB_TARGETS_FILE`, both when minting and when
   running.
2. Here, provide the pinned Go toolchain and the launcher's host tools:
   `base64`, `cosign`, `flock`, `jq`, `syft`, `buildah`, `git` and `sha256sum`.
   A Mise shim counts only when `mise which` resolves it to an installed tool;
   otherwise run `mise install && mise reshim`. Buildah must work for the
   current user.
3. Run the suite:

   ```text
   just test-live                            # the complete profile
   just test-live 'TestRegistry_.*'          # an explicitly partial run
   ```

The suite builds host and runner binaries for the architecture of the
contract's ordinary runner pool, writes caches and evidence, runs containers
and trusted runner jobs, may fetch modules and images, and deletes and
recreates `fci-` resources on the lab. The launcher builds the product and the
runner-side helpers outside the tests and hands them over by path, so a run
tests the artifact the release flow produces. It compiles each package's test
binary, then runs them one after another under the account lock.

On `SIGINT` or `SIGTERM` the suite cancels its work through
`livetest.Context(t)` and gives the `t.Cleanup` callbacks a shared two-minute
grace period. Cleanup is registered before a scratch resource is created, so a
cancelled creation can still be reclaimed. The launcher exits 130. A supervisor
must allow at least three minutes for this cleanup (`TimeoutStopSec=180` under
systemd). Forced termination cannot complete cleanup, and an interrupted run is
never acceptance evidence. With Forge Lab's full-release wrapper, run
`just test-live-preflight` with the run's arguments first
([Preflight a run](#preflight-a-run)): the wrapper swaps the privileged runner
in before the launcher's own preflight, so a stale contract costs two runner
swaps instead of seconds.

### Full and focused runs

`full` requires both GitLab and Forgejo in the contract, each with a deployed
runner (`workflow-runs`) and a Fulcio issuer mapping, so a green full run
cannot have quietly skipped the in-runner or keyless half of the matrix.
`focused` takes a `-run` filter and requires nothing beyond what the selected
scenarios ask for through `Requires`.

A focused contract may select just GitLab or just Forgejo. Capability
scenarios with no eligible selected provider report an explicit skip; they
still fail a full run. The JSON doctor scenario checks each selected
provider's identity and required schema fields, then reports the separate
cross-forge comparison as skipped when only one provider is selected.
Neither skip proves parity. GitLab's runner-managed job artifacts are
distinct from the Actions-style `artifact upload/download` capability.
`TestInRunner_GitLabNativeArtifactsReachTheNextJob` proves a native
`artifacts`/`needs` handoff between clean jobs, checking the exact bytes of a
binary payload bound to the pipeline and commit.

Before anything is built the preflight prints what the run will touch
(endpoints, capabilities and credential lifetime) and the run log keeps it;
`forge-labctl targets explain` shows the same for a contract on disk. The
suite refuses a stale credential; mint a fresh contract in Forge Lab. See
[Preflight a run](#preflight-a-run) for when a credential is stale.

### Evidence and the verdict

The launcher keeps a credential-redacted log of each run under
`${XDG_STATE_HOME:-$HOME/.local/state}/forge-ci/live-runs/<run>` for 30 days
(`FORGE_CI_LIVE_EVIDENCE_ROOT` moves it) and prints the path at the end.

The run ends with one `Live result:` line: `passed`, `failed`, `interrupted`,
`skipped` when no registered scenario ran, `not-covered` when a focused run
passed and one of the scenarios it selected skipped itself or every subtest
(a non-zero exit), or `infrastructure` when the lab, not the product, failed
every test that failed (still a non-zero exit). `verdict.json` beside the log
records the same verdict for tooling: the run's inputs, each leaf test's
result and time, and the enforcement-policy decisions the log shows. It is
read from the redacted log by the same reading that decides the verdict, so
the two cannot disagree. Read each lane's outcome from it.

One live run per lab account and provider runs at a time;
[Execution scopes and locks](#execution-scopes-and-locks) says how runs share
the lab.

### Private sources and private job images

For the runtime publisher's private-source authentication contract, run
`just test-live '^TestInRunner_PrivateSourceBranchAcceptsJobToken$'`. It creates
a private Forgejo repository, proves its branch API refuses anonymous reads,
then requires HTTP 200 with the actual runner-issued job token. The separate
`TestInRunner_ForgeInjectedRegistryCredentialAuthenticates` case checks the
registry authentication handshake. Neither case claims that a private GHCR
job image was pulled successfully; that needs its own exact-image execution
evidence from the relevant runner.

`TestInRunner_PrivateGHCRJobImage` supplies that separate, opt-in evidence.
Set `FORGE_CI_LIVE_PRIVATE_JOB_IMAGE` to an approved existing private GHCR
digest reference, plus `FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE` (an absolute
private token-file path) and `FORGE_CI_LIVE_EXTERNAL_REGISTRY_USER`. Then run
`just test-live '^TestInRunner_PrivateGHCRJobImage$'`.

The controller verifies authenticated access, anonymous refusal and a matching
native architecture before creating a private scratch Forgejo repository.
Its dynamic-matrix job uses `container.credentials` to pull and execute the
selected image. The oracle waits for the complete workflow and requires an
actual pull record identifying the supplied user plus successful execution.
Choose a digest absent from the runner cache: a cached image does not qualify
as cold-pull evidence. GHCR access is read-only; the scratch repository and its
temporary secret are removed through the normal lab lifecycle.

## Execution scopes and locks

Focused local suites support `FORGE_CI_LIVE_PROVIDER=forgejo` or `gitlab`.
Selection is checked by Go preflight, runtime provider selection and runner
architecture selection. A single-provider run cannot claim the full profile.
The retained report names its execution provider and preserves the complete
contract identity/provenance. Read each lane's outcome from the `verdict.json`
beside its log; an `infrastructure` verdict is the lab's failure, not the
product's: fix the lab and run the case again rather than counting it either way.

The launcher takes `live.lock` shared and the lock of its lab account on that
provider exclusive. Fixtures use fixed
repository names under the run's account, so two runs on one provider conflict
only when they act as the same account. Different providers can overlap, and
so can runs on one provider that each hold their own account's contract:

```sh
# In Forge Lab, once per extra account; the lab's runners serve every account.
LAB_TARGETS_FILE=/absolute/forgeci2.json just targets --account forgeci2 gitlab forgejo
# Here, beside a run that uses the forgeci contract:
LAB_TARGETS_FILE=/absolute/forgeci2.json just test-live-prepared <dir> gitlab '<pattern>'
```

Give each run a pattern that selects different cases, so no case runs twice.
A prepared run runs each case as a process of its own, one after another, and
each has three hours: a case that ends there fails and costs that case alone.
Runs side by side share the lab's runners and slow each other down.
One prepared directory serves every account, its images being public in the
lab registry or pushed by each run from the saved archives. The product
admits a kaniko job image only for the layer repository its source label
names, so a Forgejo release, lifecycle or snapshot that builds with kaniko
pushes the saved kaniko job image (`FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE`)
once per case, labelled for its own account's layer repository, as the
scenarios do. Any account with a Forgejo endpoint runs any Forgejo lane.

The Forgejo layer lane is the longest. To shorten its wall time, split it
between two accounts, the lifecycles in one run and everything else in the
other; together the two patterns select each row and scenario of
`^TestForgejoLayer_` once, in every run set and delta run:

```sh
just test-live-prepared <dir> forgejo '^TestForgejoLayer_ConsumerLifecycle$' set=medium &
LAB_TARGETS_FILE=/absolute/forgeci2.json \
  just test-live-prepared <dir> forgejo '^TestForgejoLayer_([^C]|Ca|Consumer[^L])' set=medium &
wait
```

Unscoped/all-provider launchers take `live.lock` exclusively. Locks remain
held through test cleanup and are not inherited by suites or loggers. Never
unlink a lock file or split HOME/XDG to create concurrency.

For GitHub, use the guarded Forge Lab hosted session recipe and its own
organization lock; shared SDK preparation precedes parallel read-only reuse by
independent consumers.

Hosted GitHub consumer **release** trials require
`FORGE_CI_LIVE_GITHUB_RELEASE_TOKEN_FILE` to name an owner-private regular file
containing a fine-grained PAT. Only its path enters the environment; the token
is read natively with a 4096-byte bound and uploaded only as the consumer's
`RELEASE_TOKEN`. There is deliberately no fallback to the hosted session's
management/Packages token. Missing or invalid input refuses before hosted
resources are created. Pull-request, development-release and engine-only
`FORGE_CI_LIVE_ENGINE_MODE=prepare` runs do not require this file. The guarded
session and SDK/GHCR publication keep their existing credential unchanged.
Token kind admission does not prove repository selection or write permissions;
the ordinary release workflow and its content-write assertions remain authoritative.

## Preflight a run

A preflight makes every admission check a run makes and stops before the run's
first effect. It writes nothing to the lab, so it takes no live lock and may run
beside another run. It takes the same arguments as the run:

```bash
just test-live-preflight                                   # as just test-live
just test-live-preflight '^TestGitLabLayer_ConsumerTrial$' # as just test-live <scenario>
FORGE_CI_LIVE_PREFLIGHT=1 just test-live-consumer gitlab /absolute/consumer pull-request
FORGE_CI_LIVE_PREFLIGHT=1 just test-live-prepared ~/prepared/k3s gitlab '^TestGitLabLayer_ConsumerTrial$/^private$'
```

`FORGE_CI_LIVE_PREFLIGHT=1` reaches the launcher, `scripts/ci/run-live-tests.sh`,
which every recipe above ends in. The launcher reads the real lab contract the
run would use and checks it, its CA, the host tools and the build as for the
run. Each scenario then runs up to its admission boundary: everything it
checks before its first effect, such as the runner capabilities the contract
declares, the contract's credential for that forge, and the consumer checkout
and gate it was given. Runner inputs are not judged here: the engine refuses a
plan whose platform has no runner when the run plans it. A prepared run first
checks the prepared directory against the checkout, as for the run, and then
preflights each case.

A scenario declares its boundary with `livetest.RegisterAdmittedScenario(t)` in
place of `RegisterLiveScenario`, and calls `livetest.Admitted(t)` before its
first lab write. A preflight run stops the scenario at `Admitted` and reports it
admitted; one with a subtest per visibility is admitted only when every
subtest was. A scenario that declares no boundary does not run, and the
verdict is `unadmitted`, because a preflight that skipped its admission cannot
vouch for the run. A preflight run blanks the credential a scenario is handed
and refuses the lab client, so a scenario that forgets `Admitted` fails at its
first effect instead of making it. The GitLab consumer trial declares a
boundary. The verdict is `admitted` (exit 0), `unadmitted`, `skipped`
(nothing ran) or `failed` (an admission check refused); `verdict.json` beside the
log lists the admitted scenarios.

The preflight judges only the credentials of the endpoints the run uses: both
forges' for an unscoped run, and one forge's under `FORGE_CI_LIVE_PROVIDER`. An
unused endpoint's credential is not judged and is not mentioned. A used one is
refused when it is stale. A credential without an expiry is stale 24 hours after
it was created. One with an expiry is stale when less than five minutes of it
remain, or when the expiry is more than 31 days away. A stale credential stops
the preflight, and the refusal names the safe step. Other sessions may hold the
shared account's contract, and emitting it again in place revokes their tokens.
Instead, mint a contract of your own beside it in Forge Lab with
`LAB_TARGETS_FILE=/absolute/path/own.json just targets --account forgeci gitlab forgejo`,
then point `LAB_TARGETS_FILE` at that path here. An unscoped run is also told it
can select the other forge with `FORGE_CI_LIVE_PROVIDER`.

## What the contract is, and what it is not

The contract describes infrastructure: endpoints with their web, API and Git
roots, capabilities, credential, optional OCI registry origin and optional
fixture manifest; a transport `ca_file`; an optional Fulcio origin with exact
per-endpoint issuer mappings; and the durable fixture catalog version. It is
decoded with unknown fields refused and validated for what this tier uses.

The contract carries no resource namespace or independent owner override.
Repository lifecycle helpers use the compiled `fci-` prefix and the credential's
user as owner, refusing names outside that prefix before sending a request.
Using another account's contract selects that account as the lifecycle owner.
Direct HTTP, SDK and native CLI operations do not inherit those resource checks;
the scenario must scope them itself. `Target.Reading(owner)` intentionally
selects the durable-fixture owner for read-only checks. The forge still enforces
the credential's permissions on each request.

The run identity derived from the generation, hosts, owners and namespace is
what the preflight prints and the run log keeps; the module's
`RequireConfirmation` remains for a consumer that admits a host outside the
lab and wants an operator to type it.

The one thing this suite knows about Forge Lab is the list of hosts it may
destroy things on. That allowlist is compiled into the contract module, and
every host a contract names, forge, registry or Fulcio, must be on it: an
allowlist read from the environment would be one typo away from pointing a
destructive suite at production, and the environment is exactly what an
accident controls. Nothing else about the lab's topology is assumed, so any
operator willing to meet the documented shape can produce a contract.

Egress follows the same list. Host-side clients trust only the CA captured
from the contract at acceptance, and they send requests, redirects and
registry token exchanges only to hosts on that list: the contract module's
`DisposableTransport` refuses any other request before a retry or a dial.
Each spawned product or cosign process is pointed at a per-invocation
loopback CONNECT proxy that tunnels only to those hosts. In the runner, the keyless probe runs keyless
`release sign` behind a separately built static copy of the same proxy, so
neither the product nor the tools it runs can reach a public Sigstore, Rekor,
TUF or CT log service; the Forgejo probe fetches its ID token before enabling
that proxy, behind a shell guard on the forge origin. The registry probe runs
its OCI login check behind the same proxy.

The contract is read through the shared `forge-lab-contract` module
(`codefloe.com/itiquette/forge-lab-contract`): the JSON Schema and the Go
package that decode it, apply the private-file policy, hold the disposable-host
list, check credential lifetime and derive the run identity, with the accepted
and refused corpus pinned there. This repository keeps only its two accepted
fixtures under `test/live/internal/livetest/testdata/`; ordinary untagged tests project
them onto targets, exercise the guard and test the preflight without reaching a
provider.

## Scenarios are repeatable by construction

`NewScratchRepo` deletes **before** it creates, so a run that died between
create and cleanup cannot make "start from empty" a lie; anything a forge
scopes to the *owner* rather than the repository (packages) is swept explicitly
and versioned per run, because deleting the repository does not remove it; and
fixtures pre-check that state is absent, so "it was published" cannot be
mistaken for "it was already published". Scratch repositories are created,
deleted and swept through the contract module's repository lifecycle, bounded
to the `fci-` prefix, over this tier's own lab-only transport.

Within one scenario the per-forge subtests run in parallel; scenarios still
run one after another. Each forge is its own instance with its own runner, and
nothing a subtest writes is shared: scratch repositories, packages and
registry tags live on that forge's own host and registry, the module clients
and the contract digest are cached behind a `sync.Map` and a mutex, every
product or cosign process gets its own loopback proxy, and scenario files and
registry credentials sit under the subtest's own `t.TempDir()`. A
scenario that compares forges collects their results under a mutex inside one
`forges` group and compares after it. `checkout_env_test.go` sets process
environment and stays serial. A workflow run is read as soon as it is
committed and then every 2 s; its 4-minute bound runs from the commit, so it
covers the time the run waits for a runner.

The checkout scenario is the exception that reads a durable fixture instead:
the selected endpoint's `fixture_manifest_file` names `fx-tag-behind-head`, a
repository whose tag sits behind its branch head. The scenario compares the
manifest's commit identities with the forge before checking out `v1.0.0`, and
requires `after-tag.txt` to be absent. It never creates, advances or deletes
that fixture; manifest data grants no mutation authority.

**The layer scenarios** (`TestForgejoLayer_*`) test `.forgejo/` itself. Each
pushes HEAD's tree to a branch of its scratch repository and pins that commit
in a fixture under `test/live/conformance/testdata/layer`. A job
runs the binary that commit's checksum names, downloaded as the layer
delivers it: `just test-live` builds it with `scripts/ci/engine-binary.sh`,
and the scenario uploads it to the scratch owner's generic packages and
deletes it afterwards. A tree whose checksum is not that build is refused
before anything is pushed. Uncommitted changes under `.forgejo/` are refused; the
rest of the tree may be dirty. They wait on the workflow run, for up to 20
minutes, and a failure prints every job with its status and the end of the log
of each that did not succeed. The release scenario needs a signer image:

```text
export FORGE_CI_LIVE_SIGNER_IMAGE="$(just test-live-image signer /path/to/forge-lab)"
just test-live 'TestForgejoLayer_.*'
```

The recipe builds `containers/signer/Containerfile` at the current commit and
publishes it under the `forgeci` account with Forge Lab's `publish-image`. Without
the variable the release scenarios skip and name the recipe.

`TestForgejoLayer_RegistrySignerSmoke` is the one scenario that builds an
image in a job: one `FROM scratch` image for linux/amd64, pushed to the
forge's registry, recorded in the image ledger, signed by
`sign-and-publish-release.yml`, and pushed a second time as a base that
`sign-container-images.yml` signs and `verify-base-container-images.yml`
verifies. The host verifies both images with `cosign verify` against the
scenario's own key.

Its release request is the one signed request of the tier that is released:
an annotated tag signed with a throwaway SSH key and pushed with git, a
`.forgejo/release-allowed-signers` in the consumer that names the key, and
`verify-release-request-signature: 'true'` in the fixture. The minimal
release keeps the default, an unsigned tag created through the forge's API.
`TestForgejoLayer_PrepareReleaseRefusesAnUntrustedRequest` signs its request
the same way and commits a file that names another key, or no file: the run
must fail in the job that signs, before the bot's key is loaded, and leave no
release commit, final tag or release.

`TestForgejoLayer_PrepareReleaseStopsOnAMissingSecret` passes every release
secret but the registry token. The run must fail in `Prepare: changelog`, in
its first step: the log names that one secret and none of the others, holds
no secret's value and no later step, the signing job reaches no runner, and
the repository's branches, tags, releases, artifacts and the owner's packages
are as they were. It needs a Forgejo runner and nothing else.

Rootless Buildah needs user namespaces, which the contract's ordinary
runner pool may deny. The scenarios that build an image with it,
this one and the container and Rust development scenarios on Forgejo, run
those jobs on the contract's build runner pool,
through `CI_RUNNER_LABEL_CONTAINER`, and skip when the contract names none.
On Forge Lab's Compose road, start its runners with the full-release
acknowledgement and mint the contract again. A build that fails there is a
failure: the fixture's first job, `Check that a job can build an image`, is
the layer's `setup-buildah` alone, so the failure names the runner and not the
release. Forge Lab's k3s road publishes no such runner, so they skip there.
The GitLab image scenarios use the exact per-platform build inputs with
either builder: `build-runner-tags-linux-amd64` for amd64 and the corresponding
arm64 input when that native platform is covered. Kaniko may use a pool
without Buildah's user namespaces, but still names that pool's tags through
the build input. Select it with `FORGE_CI_LIVE_GITLAB_IMAGE_BUILDER=kaniko`; see
[Image builds with kaniko](#image-builds-with-kaniko).

## Focused resolved-image-plan smoke

`TestForgejoLayer_ResolvedImagePlan` is the smallest live acceptance case for
the resolved-policy shared image workflow: one public SHA-256 consumer, two
tiny native linux/amd64 `FROM scratch` images, and no product release or signer job.
It uses the ordinary restricted runner with the admitted Kaniko job image:

```text
export FORGE_CI_LIVE_IMAGE_PLAN_JOB_IMAGE="$(just test-live-image kaniko-job /absolute/forge-lab)"
just test-live '^TestForgejoLayer_ResolvedImagePlan$'
```

Use a current version-6 `forgeci` lab contract (`LAB_TARGETS_FILE` when needed).
The existing layer checksum and committed-`.forgejo/` guards apply; an isolated
committed snapshot is suitable for testing changes before their real commit.
The image publisher uses the same consumer-trial scratch layer coordinate as
this scenario. Publish the job image before starting the trial, not while a
live run holds the account lock.

Three sequential runs exercise bounded architecture slots: `cold` builds both
`steady` and `changing`; `partial` changes only `changing`'s marker, content
revision and required input label; `warm` repeats that policy. The shared
publisher populates the slots through its normal complete-set gates. The host
requires actual build-job logs: misses must execute Kaniko, and hits must report
the previously observed immutable cache-child ref without executing Kaniko.
Equal digests alone are insufficient, because a fresh build can be reproducible.

Each phase omits `scan`, requiring real per-platform Trivy and CycloneDX evidence;
carries an opaque native builder identity and base-lineage fields; and uses its
own `resolved-base-review-<phase>` artifact namespace and shared ledger output.
Both unselected Containerfile stages reference a missing file. The host checks
the registry-served candidate against the ledger, the delivered premade SBOM
against its ledger pin and platform inventory, and the actual marker filesystem
bytes without executing them. Provider-free fixture tests execute the policy,
admit its plans, and reject missing or contradictory cache/evidence signals.
Scratch repositories, image packages and temporary engine
packages use the normal guarded scenario cleanup. The prepared job image is a
separate reusable lab prerequisite.

This focused case proves that shared build/scan/publication path, not a full
Nanolinter flavor build, signed-base reuse, or product runtime smoke checks.

## Local runner-lending lifecycle

The focused Forge CI conformance scenario runs through the same launcher:

```text
just test-live '^TestRunnerLending_LocalLifecycle/job$'
just test-live '^TestRunnerLending_LocalLifecycle/(idle|cancel)$'
```

It requires an accepted `forgeci` contract for a disposable Forgejo endpoint on
either the Compose or k3s road, local rootless Podman and the cached digest-pinned
Forgejo 13.2.1 runner image named in `test/live/internal/livetest/lending.go`. The lent
worker remains a local rootless Podman container even when Forgejo runs on k3s.
`LAB_TARGETS_FILE` selects a dedicated contract as usual; use a separate absolute
path per road when retaining both contracts. No Forge Lab source change,
administrator credential, persistent runner change, privileged profile, or
hosted GitHub dispatch is involved.

Provisioning is a local-container substitute for the hosted provisioning edge.
The receiving Forgejo, repository-scoped ephemeral registration, runner binary,
single job and retirement observations are real. The test uses the same
ephemeral/`one-job --wait` protocol and runner release as the lending script;
it does **not** execute that script's root bootstrap or registration-token CLI.
The worker has no engine socket or host mounts, runs as uid 1000 with dropped
capabilities, and uses host networking only to reach the local forge. Its cache
listener is disabled. The job selects a unique label belonging to this scratch
repository. This establishes native host execution only, not ARM64 or Buildah.

`job` checks exactly one successful job, worker exit and automatic registration
absence. `idle` waits two minutes before SIGINT; `cancel` sends SIGINT after
online proof without submitting a job. Neither tests cancellation mid-job.
Both hold the [limitation](runners.md#unused-runner-retirement-limitation) as it
stands: the registration stays behind, the runner's own credential cannot
delete it, and the owner's cleanup removes it by exact ID. A runner that
retires itself unused fails the case, since the guide and the lender would
then be out of date. Cleanup runs under the suite's separate grace period, removes the exact
created container ID, and rechecks the exact runner ID/name before fallback
revocation. Scratch repository cleanup uses the existing consumer lifecycle.

## Prepare and run

Prepare a directory once per commit with `just test-live-prepare`. Then run
the cases of the Go tables on it with `just test-live-prepared`, or one
consumer from its `demos` with `just test-live-consumer`.

Every image a prepare builds on the host is tagged with the full commit:
`localhost/forge-ci-live-signer:<commit>`,
`localhost/forge-ci-kaniko-job:<commit>`,
`localhost/forge-ci-buildah-job:<commit>` and
`localhost/forge-ci-runtime-<target>:<commit>`. A prepare of another commit
on the same host therefore builds beside a running lane's images rather than
taking their names. The names matter although a lane pushes the archives to
the lab registry: the lab's GitLab runner uses the host's Podman image store,
finds a pushed job image there as the image the prepare built, and starts it
by that image's local name, so a moved name fails the job with "no such
image". Each recipe refuses image inputs with uncommitted changes, so the tag
names what was built.

### Build resource budget

An image build may be given a resource budget:
`test-live-prepare ... build-budget=required [build-cpus=N]
[build-memory-mib=N] [compiler-jobs=N]`, or the positional
`[default|required] [CPUS] [MEMORY_MIB] [JOBS]` fields of `test-live-image`,
`buildah-job-image-archive` and `runtime-image-archive`. A required budget
defaults to 2 CPUs, 4096 MiB and as many compiler jobs as CPUs, and asks
Podman and the Go build for those limits. Values may also come from
`FORGE_CI_IMAGE_BUILD_{BUDGET,CPUS,MEMORY_MIB,COMPILER_JOBS}`; where two
sources give one value they must agree. Without `required` a build runs
unbudgeted. A build that fails under its budget is never retried without it.

Prepare once per commit, from a clean checkout whose engine checksum is
fresh, naming what will run: `forgejo` or `gitlab`, every lane of the forge, or
a lane of `livetest.PreparedLanes` by name
(`test/live/internal/livetest/preparedlanes.go`). It publishes the
signer and named nanolinter job images with Forge Lab, saves the
signer image, the Buildah and kaniko job images
(`FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE`, `FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE`;
a run pushes the kaniko one labelled for its own account's layer)
and the runtime images as archives, exports every demo as a one-commit
checkout and writes `<dir>/prepared.env` (references and paths, no secrets,
and `PREPARED_COMMIT`). The runtime images are those the GitLab cases of the
named lanes need, each as `FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_<TARGET>`: the
image of each case's flavor, the scenarios' go image, also
`FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE`, every `$CI_JOB_IMAGE_<TARGET>` the gate of
a case's demo checks in, and the producer families of a snapshot
(`go run ./test/live/internal/livetest/preflight prepared-runtime-targets . <forge or lane> ... flavor=<flavor> ...`
lists them). Each nanolinter job image is published as `JOB_IMAGE_<FLAVOR>`
and saved as `LINT_IMAGE_ARCHIVE_<FLAVOR>`, the flavor in capitals with `-` as
`_`; a lane with a case of a flavor the prepare is not given is refused
before anything is built:

```bash
just test-live-prepare ~/src/forge-lab ~/prepared/k3s \
  k3s-private-forgejo k3s-private-gitlab \
  job-image=go:<local image> job-image=java:<local image> \
  job-image=javascript-typescript:<local image> job-image=rust:<local image>
```

The cases are rows of two Go tables, `livetest.ForgejoPreparedCases` and
`livetest.GitLabPreparedCases` (`test/live/internal/livetest/preparedcases.go`).
Each runs as the subtest `<test>/<lane>/<demo>/<event>/<builder>/<visibility>`
of the live test of its event:

| Forge | Event | Test |
|---|---|---|
| forgejo | pull-request, release | `TestForgejoLayer_ConsumerTrial` |
| forgejo | lifecycle | `TestForgejoLayer_ConsumerLifecycle` |
| forgejo | snapshot | `TestForgejoLayer_Snapshot` |
| gitlab | pull-request | `TestGitLabLayer_ConsumerTrial` |
| gitlab | release | `TestGitLabRelease_ConsumerTrial` |
| gitlab | lifecycle | `TestGitLabLayer_ConsumerLifecycle` |
| gitlab | snapshot | `TestGitLabLayer_Snapshot` |

`just test-live-prepared <dir> <forge> <pattern>` runs the cases a `go test
-run` pattern selects, on the demos and images of the prepared directory. It
refuses a checkout other than `PREPARED_COMMIT`, or one with changes on top.
A case takes its images from `prepared.env` and its builder from its own row;
an image variable left in the caller's environment is never taken for one. A
case whose image was not prepared fails naming the missing key.

Run one forge beside the other, against fresh forgeci targets (minted with
`LAB_ROAD=k3s` in Forge Lab for that road). `LAB_TARGETS_FILE`,
`FORGE_CI_LIVE_CONSUMER_VISIBILITY` and the administrator inputs,
`FORGE_CI_LIVE_ADMIN_<CHECK>`, stay the caller's:

```bash
export FORGE_CI_LIVE_CONSUMER_VISIBILITY=private
just test-live-prepared ~/prepared/k3s forgejo '^TestForgejoLayer_ConsumerTrial$/^private$' &
FORGE_CI_LIVE_ADMIN_REPRODUCIBLE_BUILDS=true \
  just test-live-prepared ~/prepared/k3s gitlab '^(TestGitLabLayer_ConsumerTrial|TestGitLabRelease_ConsumerTrial)$/^private$' &
wait
```

The `private` and `private-android` lanes hold their cases to private
repositories, and on GitLab to the reproducible-build check, whatever the
caller sets (`PreparedCase.LaneEnvironment`). Set both as above all the same,
so the run header records what ran. The other lanes fix nothing: unset
visibility runs each case public and private.

The four administrator inputs are independent and default to `false`; each
accepts exactly `true` or `false`, and an invalid value refuses preflight.
Checks still run when their findings are nonblocking. An input reaches a check
only where an orchestrator of the forge declares it, `enforce-<check>`, read
from this checkout's `.forgejo/workflows` and `.forgejo/actions` or
`templates/`. No Forgejo orchestrator takes `enforce-reproducible-builds`, so
leave `FORGE_CI_LIVE_ADMIN_REPRODUCIBLE_BUILDS` unset on Forgejo: nothing
there reads it, and the run header would claim a check that never ran.

The harness supplies literals only in the wrappers and independent GitLab policy
projects it owns. Consumer source checkouts and repository variables receive no
enforcement settings. GitLab MR gates are snapshotted into a separately pinned
policy project, including the MR gate used by a release trial. Development trials
preserve their root push workflow and jobs in that policy project, with its local
gate include bound to the captured administrator gate. Forgejo and GitHub wrappers
receive only the settings declared by the called engine workflow/action at its
exact pinned Git commit, including when local uncommitted files differ.
The embedded engine default stays report-only; no lab-only policy source
edit or checksum rebuild is needed to choose the run's settings.

A lane of `livetest.PreparedLanes` also names the live tests it runs beside
its cases. The nine of `k3s-private-gitlab` are not table cases. Each takes
from the prepared directory what its lane runs with
(`livetest.PreparedScenarioEnvironment`): the go runtime image, the signer
and the kaniko job image, with private repositories, the reproducible-build
check and kaniko. A directory that lacks one of the images is refused,
naming its key. The command names the test and nothing more:

```bash
just test-live-prepared ~/prepared/k3s gitlab '^TestGitLabRelease_MinimalSignedArtifacts$'
```

The scenarios of `harness-inputs-gitlab` and `build-capability-gitlab` take
their consumer and their image from the prepared directory in the same way,
and `interruption-manual-gitlab` takes nothing from it. See
[Scenario lanes on GitLab](#scenario-lanes-on-gitlab).

The pattern normally selects a lane, many rows of the tables. The launcher
asks the tables which rows it selects and runs the test binary once for each
row, with the row's own pattern, then once more for whatever else the pattern
selects. So each row's verdict is in the log when the row ends, and the test
binary's three-hour limit is a row's: a row that runs into it, or crashes,
loses its own verdict and cleanup and no other row's. A pattern that names a
visibility keeps it in every row. When a row ends, the run prints its line:
how its process ended (`passed`, `failed` or `interrupted`), how long it took
and the pattern that reruns it.

```text
FORGE_CI_LIVE_ROW passed 41m12s ^TestForgejoLayer_ConsumerTrial$/^private$/^hello-go-cli$/^release$/^kaniko$/^(public|private)$
FORGE_CI_LIVE_ROW failed 180m01s ^TestForgejoLayer_ConsumerTrial$/^private$/^hello-maven$/^release$/^kaniko$/^(public|private)$
```

A row that ran into the limit is `failed`, with the panic above its line. The
line is the process's end, not the run's verdict: a row whose case ran no
leaf, as a private lane's with a `^public$` pattern, exits clean and ends the
run `not-covered`. A scenario that is no row of a table runs as before, every
selected scenario in one process, and so does a run on a named consumer
checkout and a preflight.

A Forgejo run that can never end fails its row without waiting out the
limit. Forgejo keeps a run `blocked` for as long as a job of it waits for a
job that will never run, as a job with an incomplete matrix. The wait fails
the row, names the blocked jobs and cancels the run when all of this has
held for ten minutes: the run is `blocked`, no other run of the repository
is still going, at least one job is `blocked`, every other job is over, and
no job's status has changed. A job that waits for a busy runner, or runs, is
progress, however slow, and never ends the row. Forgejo unblocks the jobs a
finished job was needed by within seconds; ten minutes allows for a loaded
instance, where that time is not measured. This is a real failure and no rerun. GitLab has no such state: it skips a job
whose need cannot be met, and the pipeline ends.

A run goes on past a failed case and fails when any case did. A focused run,
as every prepared run is, selected each of its scenarios: one that skipped
itself or every subtest ends the run `not-covered` with a non-zero exit,
though another scenario passed. Read the outcome from the `verdict.json`
beside the run's log, as above.

A failed test whose forge job stopped on the network carries a `cause` in
`verdict.json`, such as `infrastructure: name resolution (gcr.io)`, and the
run ends with a `Live cause:` line for it; the test stays `failed` and the
exit status is the same. The cause is the engine's own
classification: it is given only when the engine ended a failed job with
`Error: …: dependency unavailable` (or `rate limited`), with the kind and
host that line states, and when no failed job of the test ended on another
engine error. A finding, an HTTP status and a bare exit status give none.
The cause is read from text a job printed, so it is a hint for the reader
and no proof: a program of a job that prints a line of that form gives the
same cause.

### The one rerun of a row

The launcher runs a failed row of a table again, once, after the run's other
rows, inside the same invocation and its locks, when the first failure was an
unavailable dependency and nothing else. `livetest.RerunRows`
(`test/live/internal/livetest/livererun.go`) decides it from the log, and says
no when in doubt. A row qualifies only when:

- it is the row's first process, and that process failed: it was not
  interrupted and did not run into the time limit or crash;
- in every failed leaf test, the conclusion of a forge run was the test's
  first failure (`FORGE_CI_LIVE_CONCLUSION`, written by
  `livetest.FailOnRunConclusionf`);
- every failed job's log, printed by the harness, ends in an engine
  `Error: …: dependency unavailable` line with transport evidence of a host
  that could not be reached, followed by the runner's exit status 69;
- the lab marked no test (`FORGE_CI_LIVE_INFRASTRUCTURE`).

There is never a second rerun, and a scenario that is no table row, a run on a
named consumer checkout and a hosted GitHub cell are never run again.

The log holds both processes; the rerun starts with `FORGE_CI_LIVE_RERUN` and
its `FORGE_CI_LIVE_ROW` line is the row's result. `verdict.json` lists each
row that ran twice under `reruns`, with both attempts, and marks every test of
a rerun `"attempt": 2`, so it cannot be read as a pass at the first attempt.
The rerun keeps its job logs in `job-logs/rerun`. In `test/live/evidence.txt`
a pass that needed the rerun is marked `✅+rerun`, never plain `✅`.

### Run sets

The tables have three run sets, each within the next. A case of
`livetest.ForgejoPreparedCases` or `GitLabPreparedCases` names the smallest
set it is in (`Set`, `SetFull` for the full set alone) and the one visibility it runs with below the full set
(`Half`; none is both). Both are written by hand. Nothing computes a set,
and the full set is the whole table with both visibilities.

| Set | What it has | When to run it |
|---|---|---|
| `small` | One pull request of each ecosystem (`hello-maven`, `hello-gradle`, `hello-npm-package`, `hello-cargo-cli`, `hello-go-cli`, `hello-meta`), one release that bumps the version and releases an image (`hello-maven-service`) and one development build (`hello-go-service`) | After a change |
| `medium` | Every lane and every demo's flows once: one lifecycle of each service demo, the private lane, the snapshots and the release variants, mostly with one visibility | For a forge's round, and before a merge to `main` |
| `full` | Every case with both visibilities: the medium set's demos and events, with the second visibility, the second builder and a service demo's events alone | Before a release, and before closing a plan |

Each set covers the same demos and events on Forgejo, GitLab and hosted
GitHub ([Hosted GitHub cells](#hosted-github-cells)). A lifecycle counts as
the demo's pull request, development build and release, and a snapshot as
its development build. Each forge keeps its own builder, visibility,
lane, release options and fixture variants. On the lab the small set's
pull requests and release are the private lane's, with kaniko, so the
small set builds and releases an image with kaniko (the
`hello-maven-service` release), and builds one with Buildah (the
`hello-go-service` development build, a snapshot). The medium and full
sets differ between forges only where a forge cannot run a demo's event:

| Demo and event | Not on | Why |
|---|---|---|
| `hello-ios` pull request | Forgejo, GitLab | The build needs a macOS runner, which only hosted GitHub has |
| `hello-go-service` release | GitLab | The demo has no GitLab release build |
| `hello-android`, `hello-android-library` pull request | Forgejo | Missing: the demos' Forgejo workflows and an Android job image on Forgejo |
| `hello-android-library` release | Forgejo, GitLab | Missing: a Forgejo release workflow and a GitLab release build |
| `hello-base-images` release | GitHub | Missing: a hosted release fixture |
| `hello-cargo-workspace`, `hello-go-cli`, `hello-monorepo`, `hello-npm-package` development build | GitHub | Missing: the demos' hosted `release-dev.yml` fixtures |

`TestRunSetsCoverTheSamePairsOnEveryForge`
(`test/live/internal/livetest/runsetparity_internal_test.go`) holds this:
it fails on a demo's event that one forge's set has and another's lacks
without an exception, and on an exception that no longer holds. Move a row
between sets on every forge at once, or name the exception and its reason.

`just test-live-prepared` takes the set as its last argument, `set=full`
when left out. With `set=small` or `set=medium` it runs only the set's cases
among those the pattern selects, each with its visibility in the set. A
pattern whose rows are all outside the set is refused, and the run's header
records the set as `forge_ci_live_set`.

```bash
just test-live-prepared ~/prepared/k3s forgejo '^TestForgejoLayer_' set=small
```

Beside the rows, the small set runs only the scenarios the pattern names
exactly, as `^<test>$` or `^(<test>|<test>)$`, and the medium set also
the lanes' scenarios the pattern matches (`livetest.PreparedLanes`). A broad pattern therefore runs
no scenario that waits for an operator's inputs and skips, which would make
the run not covered. The full set runs every scenario the pattern matches.
A pattern that selects neither a row nor a scenario of the set is refused.

On Forgejo the pull requests of the four service demos with a lifecycle,
and the release of `hello-cargo-container`, are in the full set alone: the
lifecycle of each runs them in the medium set. The private lane's
`hello-maven-service` release is the small set's release.

A delta run takes `after=small` or `after=medium` after the set, a set
below it, and runs what the set runs and the set after did not: the cases
of the set outside the set after, the other visibility of a case the set
after ran with one, and the scenarios the set after did not run. The
small set, then the medium set after small, then the full set after medium
are together one run of the full set, each case and visibility once:

```bash
just test-live-prepared <dir> forgejo '^TestForgejoLayer_' set=small
just test-live-prepared <dir> forgejo '^TestForgejoLayer_' set=medium after=small
just test-live-prepared <dir> forgejo '^TestForgejoLayer_' set=full after=medium
```

The three runs count as one only as `preflight testing-evidence` takes them,
below. A delta run alone accepts nothing: it is not a run of its set. An
`after` that is not below the set is a usage error, and the run's header
records the set after as `forge_ci_live_after` and the SHA-256 of the
prepared directory's `prepared.env` as `forge_ci_live_prepared_env_sha256`,
the hash alone and never the file's values. A pattern whose rows all ran in
the set after, or that names only scenarios the set after ran, is refused.
The full set after a smaller one runs every scenario a broad pattern
matches but those the set after ran, which the launcher cannot list before
the run, so such a pattern with nothing left is not refused: it ends
`skipped`.

`preflight testing-evidence` keeps the latest result of each case, a
prepared case with its visibility or the consumer of a run that names one
with its scenario, axis values and release option, and marks each row
with the worst result of its cases. A later run of other cases of a row therefore leaves a failed case
standing, and only a later run of that case replaces its result.

It takes a delta run only beside the runs it completes: an earlier run of
its set after on the same commit, provider, pattern and
`forge_ci_live_prepared_env_sha256`, which ran to its end (not
`interrupted`) and is a whole run or a delta run with such a run before it
in turn. Runs recorded before the hash was recorded have none, and match
only each other. It then keeps the worse result of each case the two ran,
so a failure of the earlier run is not hidden by the delta's pass; a later
run of the case that is no delta replaces both. A delta run without its
runs before it is left out of the draft, and a `# left out:` line at the
top names it.

### Rows a change can affect

After a change, run the rows it can affect and not the whole matrix:

```bash
just test-live-affected <base> forgejo
```

`<base>` is the commit whose rows already ran; the change is
`git diff <base>..HEAD`, committed work alone, as a prepared run takes it.
The recipe prints and runs nothing. For `forgejo` and `gitlab` each line is a
pattern for `just test-live-prepared`: a lane of a table's test when the
change can affect all of its cases, otherwise one case, and a lane's scenario
by its name. For `github` each line is a cell of `livetest.GitHubCells`,
`<demo> <event>` and its settings, as `just test-live-consumer github`
takes them.

```text
^TestForgejoLayer_ConsumerLifecycle$/^lifecycle$
^TestForgejoLayer_Snapshot$/^snapshot$
^TestForgejoLayer_ConsumerTrial$/^private$/^hello-go-cli$/^release$/^kaniko$
```

A set as the last argument, `set=small` or `set=medium`, prints the rows of
that run set alone, and no scenario for the small one. Give
`just test-live-prepared` the same set, which also picks each row's
visibility.

The rows come from one table, `liveEffects` in
`test/live/internal/livetest/affectedrows.go`: path prefixes, and for each
the forges, demos and events a change under it can affect. It is narrow only
where the tree makes that plain. A forge's workflows affect that forge, a
demo's files that demo, the snapshot workflows and scripts the snapshot and
lifecycle rows, and pages and unit tests nothing. The engine's code under
`internal/` and `cmd/`, the images under `containers/` and most of the
harness affect every row, because the table does not follow Go imports.

A path with no entry affects every row, so a missing entry costs a longer
run and never a row. Standard error names each path whose effect is every
row and why, `no entry of the table` or the entry that says so. Add an entry
when such a path plainly reaches fewer rows.

The affected rows are a shorter loop, not the acceptance. Run the full
matrix, every lane of each forge from a directory prepared at the final
commit and the hosted cells, before a release and before closing a plan.
The table is kept by hand and can be wrong; only the full run shows that.

### Rerun one cell

The pattern of one case, down to its visibility, reruns the matrix cell it
names and nothing more:

```bash
just test-live-prepared ~/prepared/lc gitlab '^TestGitLabLayer_ConsumerLifecycle$/^lifecycle$/^hello-maven-service$/^lifecycle$/^buildah$/^public$'
just test-live-prepared ~/prepared/lc forgejo '^TestForgejoLayer_ConsumerLifecycle$/^lifecycle$/^hello-maven-service$/^lifecycle$/^buildah$/^private$'
just test-live-prepared ~/prepared/sn gitlab '^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^public$'
```

A pattern that names a table's test exactly, as `^<test>$`, goes on below it
and selects no row is refused before anything is built, as a usage error
that names the pattern, and the launcher exits 2: a mistyped lane, demo, builder or
visibility would otherwise run the test with no case in it and pass. Any
other pattern is `go test -run`'s, which does not fail on one that matches
nothing, so check that the verdict lists the case.

#### Hosted GitHub cells

A hosted GitHub cell is the demo's fixture workflow for the event, run by
`just test-live-consumer github <checkout> <event>` from a one-commit checkout
of the built-in demo, inside Forge Lab's hosted session. A prepared directory
holds one for every demo, `<dir>/demos/<demo>`, as the prepared commit has it.

The cells are one list, `livetest.GitHubCells`
(`test/live/internal/livetest/githubcells.go`): the demo, the event, the
settings the cell needs and its [run set](#run-sets). A release or lifecycle
of a demo that declares `0.1.0` carries `version=0.2.0`, because a hosted
release requests `0.1.0` unless told and would then bump nothing. Each
variant, a fixture root of its own, carries its `fixtures=` and `workflow=`.
One recipe runs a set of them:

```bash
export FORGE_CI_LIVE_GITHUB_RELEASE_TOKEN_FILE=<file>
just test-live-github-cells <dir> <org-id> set=medium \
  nanolinter-image=java:<digest reference> nanolinter-image=go:<digest reference> ...
```

Start it inside the hosted session, as a single trial; it starts none. It
prepares the engine once, then runs the cells with the engine reused, up to
three at once, in the set's order, never two of one demo: the cells of one
demo share package coordinates and, as a fork pull request and the demo's
own, can share a repository name. Cells of different demos share nothing
they change: each repository is named after its demo, visibility, event
and workflow, and the packages the organization names apart from a
repository are each one demo's. Three cells stay roughly within the free
organization's limit of 20 concurrent jobs on standard hosted runners, 5 on
macOS; a job over it waits in the queue, within the run's 90-minute bound,
rather than failing. Their six visibilities, each reading its run's status
once every 20 seconds, take some 1,100 of the 5,000 requests an hour of the
session's token. Within a cell the two visibilities run side by side, each
in a repository of its own named after it, and finish in the time of the
slower one. A cell whose release publishes a package the organization names apart from the repository, an npm
or Maven package in GitHub Packages or an image under a declared
`image-name`, runs them one after the other instead
(`livetest.GitHubReleaseSharesPackages`): both would publish the same package
at the same version. Of the cells that is the release of `hello-npm-package`,
`hello-monorepo` and `hello-android-library`, and the release and lifecycle of
`hello-npm-service`; `TestGitHubReleaseSharesPackagesOfTheCells` holds the
list. The cell's log holds both visibilities; `go test -v`
names the subtest before each line it switches to (`=== CONT`, `=== NAME`),
and the cell passes on the top-level `--- PASS` line alone. Each demo's
pull-request fixture pins its Nanolinter flavor by a placeholder digest
(`nanolinter:kotlin@sha256:0...`), so the run names one public image per
flavor, `nanolinter-image=<flavor>:<digest reference>` as the prepare's
`job-image=` does, and each cell lints with the image of its flavor. If a
cell's flavor has no image, nothing runs. The other settings after the set
go to every cell but a variant, whose fixture root has workflows of its own
that take none. Each cell runs with its own visibility, whatever
`FORGE_CI_LIVE_CONSUMER_VISIBILITY` the caller has set; the fork pull
request runs public in every set. Before anything runs it refuses a setting
a cell does not take, and a set with a release or lifecycle when
`FORGE_CI_LIVE_GITHUB_RELEASE_TOKEN_FILE` is unset. It reads no credential
itself and runs no cell twice.

| Set | Cells |
|---|---|
| `small` | One pull request of each ecosystem, one release that bumps and one development build, as on the lab forges |
| `medium` | One lifecycle of each service demo with one visibility, every other demo's pull request and release, the four release variants, the fork pull request and the small set's development build |
| `full` | Every cell with both visibilities, the service demos' events alone included |

As with the tables, `after=small` or `after=medium` right after the set
makes a delta run, of the cells and visibilities the set after did not run,
so `set=small`, `set=medium after=small` and `set=full after=medium` on the
same commit are together the full set's cells. A delta run alone accepts
nothing.

```bash
just test-live-github-cells <dir> <org-id> set=medium after=small nanolinter-image=java:<digest reference> ...
```

The run directory is one of the live runs' root
(`FORGE_CI_LIVE_EVIDENCE_ROOT`), with `engine-prepare.log`, a log for each
cell and `cells.txt`: the run set, `forge_ci_live_set`, and of a delta run
the set after, `forge_ci_live_after`, then a line a cell as it ends, in
the order the cells end. Only the recipe writes the lines, one cell after
another, so they never interleave; a cell's log is numbered in the set's
order:

```text
forge_ci_live_set=medium
FORGE_CI_LIVE_CELL passed hello-maven release both version=0.2.0 nanolinter-image=<reference> <run directory>/07-hello-maven-release.log
```

A cell is `passed` when its trial exited clean and its test passed,
`not-covered` when it exited clean and judged nothing, and `failed`
otherwise. Any cell that did not pass fails the recipe once the others have
run. An interrupt, Ctrl-C, reaches the running cells, which clean up after
themselves; no other cell starts, and once those running have ended, each
with its line, the recipe exits 130. Hosted cells do not run the lab
launcher or classifier, so they write no `verdict.json`: keep the run
directory, and with it each cell's interval, engine and consumer revisions,
native run IDs and cleanup output.

`just test-live-consumer github` still runs one cell, with the words its
line in `cells.txt` or `just test-live-affected <base> github` gives.
`github-gpg-files` declares `sign.files-method: gpg` beside a Cosign key
with no transparency log: the release must offer an `.asc` for every file
and no Cosign bundle, and the image must carry a Cosign signature and SLSA
provenance.

### Lifecycle

The `lifecycle` lane of each table runs the linked flow of one consumer in one
scratch repository, the `lifecycle` event, for each demo with a development
route on the forge: `hello-maven-service`, `hello-gradle-service`,
`hello-cargo-container`, `hello-npm-service` and, on Forgejo alone,
`hello-go-service`, which ships no GitLab release build.

```bash
just test-live-prepared <dir> forgejo '^TestForgejoLayer_ConsumerLifecycle$/^lifecycle$'
just test-live-prepared <dir> gitlab '^TestGitLabLayer_ConsumerLifecycle$/^lifecycle$'
```

The lane fixes no visibility and no administrator input.
The visibility is the run's selector: `FORGE_CI_LIVE_CONSUMER_VISIBILITY`
unset or `both` runs each case public and private. So are the
administrator inputs: `FORGE_CI_LIVE_ADMIN_<CHECK>=true` enforces that check
on every case, unset enforces none, and the run header records them. The
builder is each case's own. The GitLab table has every demo once a builder;
the Forgejo table has `hello-maven-service` with both and the others with
Buildah. On Forgejo both builders' image builds, and the service's
readiness, run on the contract's container/readiness runner
(`CI_RUNNER_LABEL_CONTAINER`); on GitLab kaniko builds on the ordinary runner.

On Forgejo the phases, each ending the
lifecycle when it fails, with their acceptance owners unchanged:

1. **Pull request.** The consumer is rendered once, with the release's public
   facts and the release requester's public allowed-signers staged in the
   private index before the base commit, so every commit of the flow carries
   the trust. Base to `main`, the change to `development`, a pull request
   judged by the consumer's own `pullrequest.yml`. No tag, release or other run.
2. **Merge.** The pull request is merged natively with a merge commit, the forge
   told which head commit is meant. A mirror then shows `main` at a commit whose
   parents are the base and the change and whose tree is the change's.
3. **Development.** `development.yml`, the harness's service development
   workflow (`test/live/internal/livetest/testdata/service-development.yml.tmpl`), is
   dispatched on the merged `main` and snapshots `development`: the
   snapshot-images workflow over the staged declaration, then a readiness job
   that runs the published digest with Podman. The acceptance is the frozen
   plan, one amd64 image receipt, digest and tag with no alias, and the
   readiness evidence. Only the development registry credential is set for it.
4. **Release.** The deploy key and the release secrets are set now, a signed
   `release-request/v<version>` is pushed at the merged `main`, and the release
   is judged as a release trial's: one signed release commit on `main`, signed
   tag, a release whose Cosign bundles verify against the trial's key.
5. **Final.** The repository holds exactly those three runs, two tags, one
   release and the bot's deploy key; `development` still points at the change.

On Forgejo the event takes the release's settings but `workflow=`,
`next-version=` and `interrupt-after=`; `CI_JOB_IMAGE`, pinned by digest, is a
nanolinter image of the demo's toolchain flavour, the `job-image=<flavour>:`
the prepare publishes: the demo's lint and build jobs run in it, and the
snapshot builds the demo's producers in it, through the repository variable
the snapshot-images workflow reads for each producer family,
`SNAPSHOT_ARTIFACT_JOB_IMAGE_<FAMILY>` (none for a container-first service). A
plain runtime image fails the lint job on the forge, "nanolinter is not on
PATH". It selects its builder as the other events do, with the consumer's
`CI_IMAGE_BUILDER`, and runs in the consumer trial's layer repository
(`fci-consumer-trial-layer`, beside its own `fci-consumer-lifecycle`): a run
admits a kaniko job image only for the repository its source label names, and
a prepared case pushes the saved one labelled for that repository, so one
image serves every event and visibility of the case; under kaniko,
`CI_IMAGE_BUILDER_JOB_IMAGE` pinned by digest is required before anything is
created, as the run admits it by digest and label in the development phase.
It needs what the published snapshot and a Forgejo release
need: the contract's container/readiness runner, whichever builder builds the
image, `FORGE_CI_LIVE_SIGNER_IMAGE`, ssh-keygen and gpg.

On GitLab (`TestGitLabLayer_ConsumerLifecycle`) the same five phases run in
one project under one pinned policy file, whose includes serve the three
pipelines: the demo's gate for merge-request pipelines, the `snapshot-images`
component for the push pipelines of `main`, the `release` component for the
request tag. The merge request is merged through the API, told the head
commit; the push that merge makes to `main` is the development phase, a
snapshot of `development` whose child pipeline builds the producer the
declaration names in the build image, the ecosystem's toolchain image (none
for a container-first service, whose Containerfile builds it), builds the
image with the selected builder and publishes it to the project's registry;
the acceptance is the frozen plan, one amd64 receipt, digest and tag with no
alias. The component has no readiness or checked-JAR job, so none is
claimed. The release is judged as the GitLab release trial's,
packages and images included. The event takes the release's archives and
settings but `scenario=` and `next-version=`. The credentials are protected
variables scoped to their jobs' environments: the merge request's pipeline, on
an unprotected branch, gets none; the snapshot's publisher the development
registry credential alone, set before the flow; the release's jobs the
release's, set at the release phase and not before, as on Forgejo. The bot's
deploy key alone exists from the start, because `main` is protected to it
before its first write. The project's pipeline configuration is set after the
base and the change are pushed, so the merge's push is the first pipeline on
`main`. GitLab resolves a policy only as it creates a pipeline, never ahead of
one: its project lint reads the project's own repository, not a policy in
another project, so a refused include or component input is read from the
pipeline's `yaml_errors`, as for every GitLab trial. The include rules'
variables are the documented ones, `CI_PIPELINE_SOURCE` and
`CI_COMMIT_REF_NAME`. The GitLab `snapshot-images` component has no standalone
trial: the lifecycle's development phase is its first conformance exercise,
so a failure there may be the component's rather than the flow's.

On hosted GitHub (`TestGitHubLayer_ConsumerLifecycle`) the same five phases
run in one disposable repository of the test organization, against the engine
the checkout pushed there, through the three orchestrators the demo's GitHub
fixtures call: `pullrequest.yml` for the pull request, `release-dev.yml`, the
demo's call of the snapshot orchestrator, dispatched on the merged `main` with
`source-branch: development` for the development phase, and `release.yml`
for the release. The pull request is merged through the API, told the head
commit, and the merge is read back as on the lab forges: `main` at it and
`development` still at the change, through the API, then, in the working
copy fetched to `main`, its parents the base and the change and its tree the
change's. The development
phase snapshots the service snapshot's declaration the trial stages into
the pushed tree, `.forge-ci/service-development.yml`, overlaid unsigned and
native amd64 as on the lab forges (`livetest.ServiceSnapshotOverlay`). Its
acceptance is the development build's (a green run, its artifacts
delivered, `main`, tags and releases unchanged) and the same snapshot oracle
the lab forges use (`livetest.RequireSnapshotImages`): the frozen plan from
the run's `snapshot-images-inputs` artifact equal to the planner's, the
receipts from its `snapshot-images` artifact collected against it, and each
image read from the repository's container package on GHCR, which is
removed with the repository. The release is judged as
the GitHub release trial's. The snapshot publishes with the workflow's own
token; the release secrets are set at the release phase and not before. It
runs outside the prepared lanes, inside Forge Lab's hosted session:
`just test-live-consumer github <checkout> lifecycle org=<org-id>`
([Trying a consumer on GitHub](#trying-a-consumer-on-github)). On GitHub the event is
admitted for the demos that ship all three fixtures and a service
snapshot's declaration: `hello-maven-service`, `hello-cargo-container`,
`hello-go-service` and `hello-npm-service`.

**Development routes.** On Forgejo and GitLab the event is admitted for a
demo with a development route on the provider, read from the demo's own
files, not its name; a demo without one is refused rather than run as a
shorter flow. The route is a *service snapshot*: the snapshot-images
policy, the reusable workflow on Forgejo and the component on GitLab, over
the demo's declaration. What the snapshot builds is the planner's rule
(`snapshotimage.New`): the harness plans the declaration, overlaid for the
trial as native linux/amd64 and unsigned into
`.forge-ci/service-development.yml` (`livetest.ServiceSnapshotDeclarationFile`),
and keeps no copy of the planner's rules. The trial's own scope is one
container of the root Containerfile and context with native amd64 declared,
without image name, tag suffix or declared provenance, from at most one
artifact, since that is the one image it judges. On both forges the
declaration is the one the demo's release builds its images from,
`.forge-ci/release-images.yml` when the demo has one (its `artifacts.yml` then
serves another forge's flow in a shape the snapshot refuses too), else
`.forge-ci/artifacts.yml` (`livetest.ServiceSnapshotSource`); on GitLab the
demo must also ship the GitLab release build, `.gitlab/build-release.sh`. On
Forgejo the harness writes the consumer-side development workflow, one for
every family (`test/live/internal/livetest/testdata/service-development.yml.tmpl`): the
snapshot-images workflow builds the producers in the image the repository's
`SNAPSHOT_ARTIFACT_JOB_IMAGE_<FAMILY>` variable names, the lifecycle's
`CI_JOB_IMAGE`, then the image; a readiness job runs the published digest.
The case tables hold only lifecycles a demo can run: a test admits every
row (`admitLifecycle`) and fails with the reason. The generated matrix in
[Testing matrices](testing-matrices.md) lists every demo's state; the reasons
behind it:

| Demo | Forgejo | GitLab | Why |
|---|---|---|---|
| hello-maven-service | lifecycle | lifecycle | artifact-first Maven application and its runtime image |
| hello-gradle-service | lifecycle | lifecycle | artifact-first Gradle application and its runtime image |
| hello-cargo-container | lifecycle | lifecycle | container-first from `release-images.yml`, no producer |
| hello-npm-service | lifecycle | lifecycle | artifact-first: `artifacts.yml` is a service snapshot's (one npm producer), with the snapshot-npm-layout merge's layout, and the demo ships its GitLab release build |
| hello-cargo-workspace | — | — | two runtime images; the lifecycle judges one container (the snapshot event judges both, see [Snapshot](#snapshot)) |
| hello-go-service | lifecycle | — | container-first from `artifacts.yml`, no producer; its `extract.binary` is the release's, which the snapshot does not run; no GitLab release build, as its example ships none (the snapshot event runs its development phase on GitLab, see [Snapshot](#snapshot)) |
| hello-go-cli, hello-maven, hello-npm-package | — | — | no container, so no image snapshot and no lifecycle (the snapshot event judges the files of hello-go-cli and hello-npm-package, see [Snapshot](#snapshot)); hello-maven's library development route publishes a SNAPSHOT package, another shape |
| hello-monorepo | — | — | two runtime images, neither of a root Containerfile; the lifecycle judges one container (the snapshot event judges both, see [Snapshot](#snapshot)) |
| hello-gradle, hello-android, hello-android-library, hello-ios | — | — | no Forgejo flow or no container |

The runners are amd64 only. Standalone
`test-live-consumer <forge> <checkout> lifecycle …` runs the same test
without a prepared directory; the GitLab lifecycle reads the checkout's snapshot
source itself. The standalone pull-request and release trials, and GitHub's
snapshot trial, remain the single-phase entrypoints; the lifecycle calls
their extracted checks. On Forgejo its development phase is the one
published development snapshot trial (the standalone
`published-unsigned-snapshot` event retired into it). No lifecycle signs
with GPG: `hello-go-cli` has the GPG declaration (`artifacts-gpg.yml`) but no
development route, and the Forgejo demos' release workflows take the default
declaration.

### Snapshot

The `snapshot` lane of each table runs the `snapshot` event: the
lifecycle's development phase alone, over every image the planner plans from
the demo's declaration. It is the route of every development cell no
lifecycle case runs: the multi-image demos' until their full lifecycle
exists, and `hello-go-service`'s on GitLab, which has no GitLab release
build. It releases nothing and does not couple to a release's image
fan-out. Forgejo runs `hello-cargo-workspace` and `hello-monorepo` with
Buildah; GitLab runs `hello-cargo-workspace`, `hello-monorepo` and
`hello-go-service`, each once a builder. Each forge also runs `hello-go-cli`
and `hello-npm-package`, whose declarations have no container: their
snapshots keep files, as described below; and, with Buildah,
`hello-maven-service` from its variant declaration, which keeps the Build
SBOM alone.

Each cell reruns with one command:

```bash
just test-live-prepared <dir> forgejo '^TestForgejoLayer_Snapshot$/^snapshot$/^hello-cargo-workspace$/^snapshot$/^buildah$/^public$'
just test-live-prepared <dir> gitlab '^TestGitLabLayer_Snapshot$/^snapshot$/^hello-cargo-workspace$/^snapshot$/^buildah$/^public$'
just test-live-prepared <dir> gitlab '^TestGitLabLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^public$'
just test-live-prepared <dir> gitlab '^TestGitLabLayer_Snapshot$/^snapshot$/^hello-go-service$/^snapshot$/^buildah$/^public$'
just test-live-prepared <dir> forgejo '^TestForgejoLayer_Snapshot$/^snapshot$/^hello-go-cli$/^snapshot$/^buildah$/^public$'
just test-live-prepared <dir> gitlab '^TestGitLabLayer_Snapshot$/^snapshot$/^hello-go-cli$/^snapshot$/^buildah$/^public$'
just test-live-prepared <dir> forgejo '^TestForgejoLayer_Snapshot$/^snapshot$/^hello-npm-package$/^snapshot$/^buildah$/^public$'
just test-live-prepared <dir> gitlab '^TestGitLabLayer_Snapshot$/^snapshot$/^hello-npm-package$/^snapshot$/^buildah$/^public$'
just test-live-prepared <dir> forgejo '^TestForgejoLayer_Snapshot$/^snapshot$/^hello-monorepo$/^snapshot$/^buildah$/^public$'
just test-live-prepared <dir> forgejo '^TestForgejoLayer_Snapshot$/^snapshot$/^hello-maven-service$/^snapshot$/^buildah$/^public$'
just test-live-prepared <dir> gitlab '^TestGitLabLayer_Snapshot$/^snapshot$/^hello-maven-service$/^snapshot$/^buildah$/^public$'
```

The declaration is the lifecycle's, `livetest.ServiceSnapshotSource`, overlaid
as native linux/amd64 and unsigned into `.forge-ci/service-development.yml`.
The planner decides the images; a case names none. The trial's scope is every
planned image under the run's one image repository, so no container may name
an image of its own (`snapshotImagesAdmitted`), and each image must declare
native amd64; a declared request for provenance stays in the staged
declaration, where the snapshot leaves it out. The acceptance is the frozen plan, equal to the
plan the planner makes of the staged declaration for the selected source,
every build native amd64, and one receipt per planned image as the product's
own collection requires (`snapshotimage.Collect`), each digest the registry
serves under its receipt's tag, with no tag but the planned images' and
their `-amd64` architecture tags. The manifest at each digest is an index
of its planned platforms and no other child, neither another architecture nor an
attestation, on every snapshot and lifecycle row of the three forges. This
snapshot-only event probes no readiness, even for a single image: the image
count does not declare a runnable service. The lifecycle event retains its service readiness check.
Snapshot-only events need neither a readiness URL nor a service fixture;
the Forgejo fixture does not read the operator's service fixture for this event.
The branches stay where the trial pushed them, and the repository
holds no tag and no release.

On Forgejo (`TestForgejoLayer_Snapshot`) the consumer's main and a
change on `development` are pushed with Actions off; the harness's
`development.yml` is the only workflow planned, and the only run: it is
dispatched on `main` and snapshots `development`. The receipts are the
snapshot-images workflow's own `snapshot-images` artifact. The settings are
the lifecycle's `CI_JOB_IMAGE` and `CI_IMAGE_BUILDER` (with
`CI_IMAGE_BUILDER_JOB_IMAGE` under kaniko), and none of the release's. The
harness pins its workflow as the demo's `.forgejo/workflows/release.yml` pins
the layer, so a demo without one has no Forgejo route. Each producer family
builds in the case's image of that family where it names one, as
`CI_JOB_IMAGE_NODE`, and in `CI_JOB_IMAGE` otherwise, given the workflow as
`SNAPSHOT_ARTIFACT_JOB_IMAGE_<FAMILY>`: the `hello-monorepo` row names the
Java image and, as `NodeJobImage`, the Node one.

A declaration without a container is a snapshot of files. It is staged as it
is, with nothing overlaid, and must plan at least one file output. The trial
gives the snapshot no image repository, registry user or registry credential
and sets `publish-files: true`: on Forgejo the harness's workflow passes the
input in place of its registry inputs and secret, on GitLab the policy's
include does and the project holds no registry variable. The acceptance is the
frozen plan, equal to the plan the planner makes of the declaration when asked
for its files, with file outputs and no image
(`livetest.RequireSnapshotFilesPlan`), and one bundle per planned output: on
Forgejo the run artifact `snapshot-images-files-<artifact>-<snapshot
version>`, on GitLab the artifacts of the child job `Snapshot: files
<artifact>` below `snapshot-files/<artifact>/`. Each bundle's
`snapshot-files.json` must name the output, its project type, the selected
head and branch, the snapshot version and `signing: NONE`, list at least one
file, and every listed file must be there with its size and SHA-256 and
nothing else beside them (`livetest.SnapshotFilesFindings`); the first
difference is named. An npm output lists its one packed tarball and no
other file. The repository holds no tag and no release, and no
package: on Forgejo none of the owner's packages is linked to or named after
the repository, on GitLab the project has no package and no container
repository. The `hello-npm-package` rows, in the medium set, are the
development build of an npm package, held to its commit by the receipt.

On Forgejo a consumer that ships its own `snapshot-files.yml` is snapshotted
through it: for a snapshot of files from the default declaration that
caller is the workflow planned, in place of the harness's, and it is
dispatched on `development`, the branch it passes on as its `branch`
(`livetest.ForgejoSnapshotRoute`). `hello-go-cli` ships the Go CLI
example's caller, so its Forgejo row runs what a project copies. The GitLab
row writes the harness's policy, not the example's
`snapshot-files-policy.yml`.

A snapshot also takes `snapshot-declaration=<repository-relative
declaration>`, a variant of the consumer's own declaration staged in place of
`ServiceSnapshotSource`'s, for a policy the release declaration does not have;
a case names it as `SnapshotDeclaration`. `hello-maven-service` ships one,
`.forge-ci/artifacts-build-sbom.yml`: the artifact-first producer keeps its
Build SBOM and the image gets none, so the run's retained evidence must hold
the image's scan and no `sbom.cyclonedx.json`
(`livetest.SnapshotEvidenceFindings`). The rows
`snapshot/hello-maven-service/snapshot/buildah` of both tables run it, in the
medium set; standalone, from an exported checkout:

```bash
just test-live-consumer forgejo <exported hello-maven-service> snapshot CI_JOB_IMAGE=<nanolinter java image, digest reference> CI_IMAGE_BUILDER=buildah snapshot-declaration=.forge-ci/artifacts-build-sbom.yml
```

On GitLab (`TestGitLabLayer_Snapshot`) one pinned policy includes the
`snapshot-images` component alone, admitted only for the pipeline the
harness requests on `main` through the API; the pushes of `main` and
`development` match no rule. The frozen plan is the parent's `Snapshot:
plan` job's, the receipts the child's `Snapshot: collect` job's. The
component probes no readiness, so none is claimed. Each planned image's
evidence is read from the artifacts of the child's `Snapshot: publish
<image>` job below `snapshot-evidence/<image>/` and held to the
declaration's SBOM policy as on Forgejo. The event takes
`job-archive=`, the component's job image, and no setting but
`snapshot-declaration=`; each producer builds in
the runtime image of its toolchain family,
`FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_<FAMILY>`, which `test-live-prepare`
prepares from the plan (none for the container-first Cargo workspace,
`java` and `node` for the monorepo), and the images with the case's
builder. `job-archive=` is required even without producers and is not the
builder archive: Buildah requires `FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE`,
kaniko requires `FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE`. A table case and
the standalone trial refuse a missing archive of their builder before
creating mutable resources. The development registry credential is a protected
variable scoped to the publisher's environment, on a protected `main`. No release build,
signer or lint archive is asked for.

The generated [Testing matrices](testing-matrices.md) show these cases in the
snapshot columns, `📝 snapshot`; a run's verdict is evidence for the
`development` flow of the overlay. The runners are amd64 only. Standalone
`test-live-consumer <forge> <checkout> snapshot …` runs the same test
without a prepared directory.

### A Forgejo consumer of your own

No lane runs `TestForgejoLayer_ConsumerTrial` on a checkout from outside this
repository. The lanes `k3s-private-forgejo` and `release-variants-forgejo`
run the same test on every demo: each demo's pull request with its job image
by digest, and its release with and without a requested version. One pull
request, `release-variants/hello-go-service/pull-request/buildah`, selects
Buildah, so the trial names the runner that permits user namespaces for its
image builds; every other pull request selects kaniko. For a
consumer of your own, name an absolute, clean, reviewed Git root and keep it
frozen. Its pull request names the job image on the command line, a reviewed
nanolinter image by digest that satisfies the consumer's actual tool
contract; its release requests a version:

```bash
export FORGE_CI_LIVE_CONSUMER_VISIBILITY=private FORGE_CI_LIVE_FORGEJO_IMAGE_BUILDER=buildah
just test-live-consumer forgejo <checkout> pull-request CI_JOB_IMAGE=<digest reference>
just test-live-consumer forgejo <checkout> release version=0.3.0
```

An unpublished consumer draft may use exactly `PIN_FORGE_CI_SHA` in every layer
reference. The consumer-trial planner replaces that marker, including policy-test
literals, with the frozen lab layer commit before serving any workflow. Mixed
draft/commit pins, other markers and branches are refused; full SHA-1 and
SHA-256 layer commits are supported. Pull-request trials keep reviewed public
signer and key literals, which the isolation gate reads but never runs; release
trials replace them with the lab's verified signer and key. Both checkouts must
still be clean and frozen.

Only those two events are run this way: development snapshots have a
distinct scenario and must not be relabelled as releases. All normal source,
target, acknowledgement, resource, locking and cleanup gates remain. The
consumer's own workflows and packaging run without a replacement build.

The release oracle verifies offered Cosign bundles. For authenticated
`checksums.txt` or `*_checksums.txt` it also verifies every uniquely named
published subject's SHA-256, refusing missing or substituted payloads. It does
not check that the release holds every file intended, embedded package
signatures, or image signatures on the local registry.

#### Release refusal and interrupted recovery

The lanes `trust-refusal-forgejo`, `recovery-forgejo` and
`forgejo17-forgejo` run a shipped demo's own release workflow to another end
than a release. Each such scenario runs on
the private lane's release case of its demo (`livetest.PreparedScenarioCase`),
so the lane fixes private repositories and a prepare of the lane holds its
images; its fixture is staged in the harness's private index, never in the
checkout:

```bash
just test-live-prepare ~/src/forge-lab ~/prepared/refusal-recovery \
  trust-refusal-forgejo recovery-forgejo forgejo17-forgejo \
  job-image=go:localhost/nanolinter-go:dev
just test-live-prepared ~/prepared/refusal-recovery forgejo '^TestForgejoRelease_RejectsPreseededProvenance$'
just test-live-prepared ~/prepared/refusal-recovery forgejo '^TestForgejoRelease_InterruptedRecovery$'
just test-live-prepared ~/prepared/refusal-recovery forgejo '^TestForgejoRelease_RerunAfterInterruption$'
```

| Scenario | Demo | Expected end |
|---|---|---|
| `TestForgejoRelease_RejectsPreseededProvenance` | `hello-go-cli` | The run fails in `Sign: files and images` with `unsigned dist must not hold dist/slsa-provenance.intoto.json: signing writes it`; no `signed-dist` artifact, no publication job on a runner, no release, no new package. The release commit and final tag exist: `prepare-release.yml` pushes them before the build |
| `TestForgejoRelease_InterruptedRecovery` | `hello-go-cli` | The run is cancelled once the harness's job `Fixture: recovery checkpoint reached` succeeded, after the unsigned hand-off and before signing: the version's tag exists and nothing is published. The harness then creates the variable `FIXTURE_HOLD_BEFORE_SIGNING`, puts a commit on `main` and requests the next patch version. That run succeeds with the release every row is held to. The interrupted version keeps its tag on the commit its run prepared and has no release and no package; the repository has four tags, one release and two runs |
| `TestForgejoRelease_RerunAfterInterruption` | `hello-go-cli` | Forgejo 17 or later. The run is cancelled at the same checkpoint. The harness creates `FIXTURE_HOLD_BEFORE_SIGNING` and reruns the cancelled job `Fixture: recovery checkpoint hold` (`POST /repos/{owner}/{repo}/actions/jobs/{job}/rerun`, 204), which reruns the jobs that need it in the same run. The hold runs at attempt 2. Forgejo creates a called workflow's jobs when its caller is unblocked, which the cancelled run never reached, so `Sign: files and images` is not in it: the rerun creates it and a runner takes it at attempt 1. Every job that succeeded at attempt 1 keeps it, and signing takes attempt 1's `build-dist`. The run succeeds with the release every row is held to, on the commit attempt 1 prepared; the repository has two tags, one release and one run, and the owner the packages the release publishes, and no other (this demo publishes none, so the owner's packages are unchanged) |

The recovery the second proves
is the next patch version, the one
[docs/forgejo.md](forgejo.md#quick-start) gives for every Forgejo. The third
runs the cancelled jobs again, in Forgejo 17 or later: its API reruns a run
(`/actions/runs/{run}/rerun`) or a job and the jobs that need it
(`/actions/jobs/{job}/rerun`), and still has no call that reruns the failed
jobs alone. On an older Forgejo the row skips, naming the version the forge
gives (`livetest.NeedsForgejo17Actions`). The demo releases files alone.

The lane `forgejo17-forgejo` also runs `TestInRunner_NativeStepSummary`,
which needs no consumer: on Forgejo 17 or later a job has
`FORGEJO_STEP_SUMMARY`, and a report's summary goes to that file and not to
the job log. Forgejo has no API that reads a stored summary, so the job
checks the file itself and the run's conclusion is the result.

#### Local release-image output readback

A native image release opts into the readback with
`release-images-config=<repository-relative declaration>`. The
`release-variants` lane of the Forgejo table runs it for three demos:
`hello-go-service`, `hello-cargo-workspace` and `hello-npm-service`.

```bash
just test-live-prepared <dir> forgejo '^TestForgejoLayer_ConsumerTrial$/^release-variants$/^hello-go-service$/^release$/^buildah$'
```

The setting is
release-only, mutually exclusive with external `image=` verification, and does
not change the consumer build or declaration. It requires the accepted local
registry and the standard engine `release-images` run artifact; a missing,
duplicate, expired or empty receipt is a failure, not an image-free success.
An explicit external-registry session is refused before the release starts.

After verifying the prepared Git commit/tag, the oracle reads that commit's
regular declaration blob and resolves it through the existing engine planner.
It admits the complete collected ledger against this independently expected
output set and repository before any registry reads. The ledger is not a trust
root: every final tag must serve its exact top-level digest, and the existing
release-image verifier then authenticates that digest's Cosign signature,
CycloneDX attestation and SLSA release source, prepared commit, tag and caller
workflow. Re-attestation eligibility is never accepted as verified output.
The lab's no-public-transparency policy and existing scoped auth/CA transport
apply unchanged.

The readback then requires the promotion journal the run kept
([failed-release-recovery.md](failed-release-recovery.md#container-promotion-partially-completed)):
one unexpired run artifact `release-promote-<attempt>` that holds
`image-promotions.jsonl` alone, and in it one record an image of the
verified ledger, with that image's final tag, candidate tag and digest.

This bounded route supports declarative release images, not custom resolved/base
plans, moving-tag policies or custom artifact namespaces. It checks planned
image presence and final digest/evidence readback, not full architecture/payload
inventory, package signatures, arbitrary provenance extensions or equality with
an independently authenticated pre-signing build digest. The internal ledger
alone cannot prove that last property. Releases without this explicit readback
setting retain their existing, narrower oracle; workflow success must not be
reported as independent image verification.

The same readback compares an image with the release file it was built from,
where there is one. For each planned linux/amd64 build it reads the planned
Containerfile from the prepared commit; a `COPY` from the build context's
`dist/`, without `--from`, names a release file and its path in the image.
The release must serve that file, an artifact of the run must hold the same
bytes at the copied path, and the published image's file must have them too.
A hand-off `COPY` that names no one file and absolute path is a failure. An
image whose Containerfile builds for itself copies none and is not compared,
which the log states; other architectures are not read.
The `release-variants` lane runs this comparison for the Go service; no
shipped Cargo demo builds its image from a released file.

#### One release of files, a package and an image

`hello-npm-service` publishes all three in one release: the packed package
and its Build SBOM as signed release files, the same package in the forge's
package registry, and the signed image built from it. The release oracle
judges the package beside the other two wherever the consumer publishes one,
by asking the forge's own registry API: the version must be absent before
the release is requested and listed after it.

- **Forgejo.** The layer has no package job, so the demo's `release.yml`
  ends in one that runs `forge-ci publish forge-packages deploy`. A release
  workflow with that command is held to every npm package its declaration
  sends to `forge-packages`, in the release trial and the lifecycle alike.
  The `release-variants` row of the demo adds the image readback above:

  ```bash
  just test-live-prepared <dir> forgejo '^TestForgejoLayer_ConsumerTrial$/^release-variants$/^hello-npm-service$/^release$/^buildah$'
  ```

- **GitHub.** The release orchestrator publishes the package to GitHub
  Packages. `CheckRelease` reads it there as a package of the trial's
  repository, for a release and a lifecycle; `image=` verifies the image:

  ```bash
  just test-live-consumer github <dir>/demos/hello-npm-service release org=<id> version=0.2.0 image=hello-npm-service
  ```

- **GitLab.** The release child's plan-driven `Publish: npm packages` job
  publishes the packed package of the signed hand-off to the project's own
  package registry, with the `RELEASE_PACKAGE_TOKEN` the harness sets in the
  `release/packages` environment; the job token stays read-only. Every
  release trial and lifecycle is held to the npm
  packages its declaration sends to `forge-packages`; the `release-variants`
  row of the demo runs the three outputs with Buildah:

  ```bash
  just test-live-prepared <dir> gitlab '^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-npm-service$/^release$/^buildah$'
  ```

A package registry is the owner's on Forgejo and the organization's on
GitHub, not the repository's, and a version is published once. On GitLab it
is the project's, and goes with the trial's project. The trial
removes what its release published when it ends, and refuses to start where
the version (Forgejo) or the package (GitHub) is already there. Two trials
that publish the same package for the same owner therefore run one after
the other, as the two visibilities of such a GitHub cell do
([Hosted GitHub cells](#hosted-github-cells)). A Maven package on GitHub is
named after coordinates only the build knows, so the trial does not look for
one beforehand: it reads back and removes the Maven packages bound to its own
repository, and one an earlier run left under the same coordinates fails the
release's publication. On Forgejo the cases of the
demo request different versions per lane. A kept demo keeps its package,
so a second demo of the same name for the same owner needs another
`version=`.

#### One release of two projects

`hello-monorepo` releases a Maven backend and an npm frontend as one
version under one tag: the jar and the packed package as signed release
files, each with its Build SBOM, an image of each in one image repository,
told apart by tag suffix, and the package in the forge's registry. The rows
are ordinary release rows of the `private` lane, with kaniko.

- **Forgejo.** The demo's `release.yml` is the monorepo example's: a build
  job for each project and one job that joins their files into the hand-off
  and their build outputs into the image context. The version bumps, the
  builds and the package job run in a Maven and a Node image the workflow
  pins by digest, as literals, so the row takes no job image of its own
  beyond the lane's. The frontend's job runs `npm ci` and `npm test` before
  its build, which alone would only warn on a failing test:

  ```bash
  just test-live-prepared <dir> forgejo '^TestForgejoLayer_ConsumerTrial$/^private$/^hello-monorepo$/^release$/^kaniko$'
  ```

- **GitLab.** The policy's plan generates a version job and a build job for
  the java and the node family. The node family's jobs run in the runtime
  image the demo's gate names `$CI_JOB_IMAGE_NODE`,
  `FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_NODE`, which the prepare builds for the
  row; every other family runs in the build image, as before. The policy's
  checks run in the one job image, the Java one, so `check-release.sh`
  verifies the backend alone. The frontend's tests gate in the node
  family's build job, where `build-release.sh` runs `npm ci` and `npm test`
  before the build: a failing test fails that job, after the release commit
  is prepared and before anything is signed or published:

  ```bash
  just test-live-prepared <dir> gitlab '^TestGitLabRelease_ConsumerTrial$/^private$/^hello-monorepo$/^release$/^kaniko$'
  ```

The demo has no lifecycle row: a lifecycle judges one container of a root
Containerfile, and the monorepo has two images, each of its project's own.

#### Release options

A release trial takes `release-option=<draft|prerelease|retention>`, gives
its release that option and holds the release to it
(`livetest.ReleaseOptionFindings`, `livetest.ReleaseRetentionFindings`):

- `draft`: the forge lists the release as a draft.
- `prerelease`: the trial requests `version=X.Y.Z-<identifiers>`, which it
  refuses without the option. Forgejo records a prerelease; GitLab has no
  flag for one, so the release notes open with the pre-release notice.
- `retention`: with `next-version=`, after the second release the first
  offers exactly the files it offered that retention keeps (checksums,
  signatures, provenance, SBOMs, `release-plan.json`): every other file is
  gone, and it had one. The second keeps a file that is none of those. On
  GitLab a kept file also serves the bytes it served, and a removed file's
  upload is gone with its link.

The setting is the one place the option is given. On Forgejo the option is
an input of the layer's workflows, so the consumer's release workflow
passes it: `hello-cargo-container` reads `RELEASE_DRAFT`, `RELEASE_POLICY`
and `RELEASE_ASSET_RETENTION` from repository variables, each with the
layer's default, and the trial sets the option's variable
(`livetest.ForgejoReleaseOptionVariables`). On GitLab the option is an
input of the release policy, which the trial writes
(`livetest.GitLabReleaseOptionInputs`); GitLab has no draft release.
Retention on Forgejo is the demo's case of two releases in a row.

An option is the last element of its case's name, before the visibility:

```bash
just test-live-prepared <dir> forgejo '^TestForgejoLayer_ConsumerTrial$/^release-variants$/^hello-cargo-container$/^release$/^buildah$/^draft$'
just test-live-prepared <dir> forgejo '^TestForgejoLayer_ConsumerTrial$/^release-variants$/^hello-cargo-container$/^release$/^buildah$/^prerelease$'
just test-live-prepared <dir> forgejo '^TestForgejoLayer_ConsumerTrial$/^release-variants$/^hello-cargo-container$/^release$/^buildah$/^retention$'
just test-live-prepared <dir> gitlab '^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-go-cli$/^release$/^buildah$/^prerelease$'
just test-live-prepared <dir> gitlab '^TestGitLabRelease_ConsumerTrial$/^release-variants$/^hello-go-cli$/^release$/^buildah$/^retention$'
```

A pattern that ends at the builder selects the demo's case without an
option and its option cases together; end it with `/^(public|private)$` for
the case without an option alone.

#### Distro packages

The Forgejo lane `distro-packages` releases
`hello-go-cli` as the example's GoReleaser alternative: the trial setting
`fixtures=test/live/forgejo-packages` stages the demo's files of that
directory over its root, in the trial's own index, so the one release
workflow builds with GoReleaser, the layer signs the `.deb`, `.rpm` and
`.apk` files (`sign-distro-packages: 'true'`), and the declaration's
`distro-packages` block has `Publish: packages` send them to the owner's
Debian, RPM and Alpine registries.

```bash
just test-live-prepared <dir> forgejo '^TestForgejoLayer_ConsumerTrial$/^distro-packages$/^hello-go-cli$/^release$/^buildah$'
```

`fixtures=` is not the hosted GitHub setting of that name. There the
directory holds `workflows/*.yml`, which replace `.github/workflows`, and a
fixture workflow selects any other file by an input, such as
`artifacts-config`. Here every file of the directory is staged at the same
path under the root: the example's GoReleaser workflow reads the default
declaration, as a project that copies it does, so the declaration is
replaced too, and the mise configuration and lockfile it needs stay out of
the demo's other rows. The host then reads the workflow, the declaration
and the projects from the staged files, not from the checkout.

The registries are the owner's and outlive the trial's repository. Before
the release is requested the trial reads them and stops where one already
holds the package at the version, alone or with a package revision after
it, with the registry and version named: remove it, or request another
`version=`. When the run is over, before it is judged, the trial reads them
again; the versions that are new are the release's, and exactly those are
removed when the test ends, unless the run keeps its demo
(`FORGE_CI_LIVE_DEMO`). That holds because the launcher's lock of the lab
account keeps other runs out of the account's registries until cleanup is
over (Execution scopes and locks); a suite started without the launcher has
no such lock. A run that ends before the readback, as one that times out
waiting, removes nothing, and the next run names what to remove.

After the release the trial verifies the `.sig` sidecar of every package
the release offers with its own GPG key, and fails on a package without
one (`livetest.VerifyPackageSignatures`, which the GitLab release trial
shares), and requires that the release added the package to each of the
three registries at the version and at no other. It installs nothing: `apt`, `dnf` and `apk` are not run.

#### GPG-signed release files

The Forgejo lane `gpg-files` releases `hello-go-service` from its
declaration with `sign.method: gpg`: the trial setting
`fixtures=test/live/forgejo-gpg-files` stages the demo's
`.forge-ci/artifacts.yml` of that directory over its own, and the
workflows stay the demo's. It is the Forgejo counterpart of the GitHub cell
`github-gpg-files`, which declares `sign.files-method: gpg` instead.

```bash
just test-live-prepared <dir> forgejo '^TestForgejoLayer_ConsumerTrial$/^gpg-files$/^hello-go-service$/^release$/^buildah$'
```

The sign job loads the GPG key in process, so the lane needs runners with
swap off, or job containers with swap disabled (memory-swap equal to
memory), as `distro-packages` does: on a runner whose job can swap the
release stops at the GPG step. After the release the trial verifies every `.asc`
the release offers with its own GPG key and requires the signature to be
that key's (`livetest.VerifyOpenPGPSignature`), verifies the Cosign bundles
of the provenance and the evidence with its Cosign key, binds every file
through the checksums the `.asc` authenticates, and requires that the
checksums have an `.asc` and that no file with one also has a Cosign
bundle (`livetest.GPGReleaseFileFindings`).

The GitLab lane `gpg-files-gitlab` releases `hello-go-cli` the same way,
from `fixtures=test/live/gitlab-gpg-files`: the Go service has no GitLab
release build. The GitLab release trial stages the fixtures as the Forgejo
trial does and refuses fixtures that change what the release builds. Its
readback verifies the `.asc` of the checksums with the trial's GPG key in
place of a Cosign bundle and applies the same findings.

```bash
just test-live-prepared <dir> gitlab '^TestGitLabRelease_ConsumerTrial$/^gpg-files$/^hello-go-cli$/^release$/^buildah$'
```

The signer runner needs swap off, or `memory_swap` equal to `memory` for its
job containers, for this lane too.

### Scenario lanes on GitLab

Three lanes run scenarios that are not cases of the table. Each is prepared
with one command and run with one:

| Lane | Prepare | Run |
|---|---|---|
| `harness-inputs-gitlab` | `just test-live-prepare <forge-lab> <dir> harness-inputs-gitlab job-image=go:<local image> job-image=java:<local image>` | `just test-live-prepared <dir> gitlab '^(TestGitLabRelease_ConsumerBranchProtection\|TestGitLabRelease_RejectsPreseededProvenance\|TestGitLabRelease_ConsumerInterruptedRecovery)$'` |
| `build-capability-gitlab` | `just test-live-prepare <forge-lab> <dir> build-capability-gitlab` | `just test-live-prepared <dir> gitlab '^TestGitLabRelease_BuildImageCapability$'` |
| `interruption-manual-gitlab` | none: it takes nothing from a prepared directory | `just test-live-gitlab-interrupted-job`, then the operator's interrupt |

A directory prepared for `gitlab`, every lane of the forge, serves the first
two as well. The table's `\|` is `|` on the command line.

A scenario of `harness-inputs-gitlab` runs on a demo of the prepared
directory as the `private` lane's release case of that demo
(`livetest.PreparedScenarioCase`): with the demo's runtime image as job and
build image, the prepared signer, the flavor's lint image, kaniko,
`version=0.2.0`, the runtime images' nonroot user, private repositories and
the reproducible-build check. Its subtest carries the case's name, so a
verdict names the demo and the builder:
`TestGitLabRelease_ConsumerInterruptedRecovery/private/hello-maven-service/release/kaniko/private`.
The scenarios and their consumers:

| Scenario | Demo | Why this demo |
|---|---|---|
| `TestGitLabRelease_ConsumerBranchProtection` | `hello-go-cli` | any consumer's first push; it uses no job image |
| `TestGitLabRelease_RejectsPreseededProvenance` | `hello-go-cli` | an ordinary native release build, of files alone |
| `TestGitLabRelease_ConsumerInterruptedRecovery` | `hello-maven-service` | one policy-owned release image, in the project's own repository under the version's tag |

The standalone release trial is not among them. `k3s-private-gitlab` runs
`TestGitLabRelease_ConsumerTrial` on every demo with a GitLab release build,
with the same settings.

For provenance rejection the demo's
original build runs first, then the controller's wrapper inserts inert unsigned
provenance into `dist`. The source checkout remains clean. The test must observe
the specific handoff refusal, blocked signer/publication and empty release APIs.

Recovery requires a consumer using `release-images: true`, producing one
release image in the source project's root image repository, with the unsuffixed
version tag (no image flavor suffix). Its build produces files/image context only;
it must not write the registry or implement a pause. The old build-running checkpoint
is unreachable: policy image builds and `Images: publish` need successful
`Build: release files`. Retained consumers that publish inside the build are not
compatible and must not be reused as this case's fixture. `hello-maven-service`
meets this: its build leaves the jar in `dist/` and `image-context/`, and its
`.forge-ci/artifacts.yml` declares one container without an image name. An
offline test holds the demo to the one planned image the recovery admits
(`TestPreparedScenarioCasesAreThePrivateReleasesOfTheirDemos`).

The harness generates a test-owned component from the exact captured engine parent
and pins it in the independent policy project. It preserves the engine SHA,
parent mirror/lock/forwarding policy and production child include, then adds a
separately pinned child extension. `Fixture: candidate checkpoint` runs after
successful `Images: publish`, without a credential environment or downloaded
artifacts. Signing keeps its original image-publication artifact dependency and
also waits for this checkpoint. Production templates have no fixture-variable
hook. The checkpoint prints `fci-live-candidate-checkpoint-ready` and holds while
its launch-time `FIXTURE_PAUSE_AFTER_CANDIDATE` value is true; expiry fails closed.

The controller observes the owned running checkpoint, reads the exact successful
publication job's immutable `_release-images/dist/release-images.json` receipt,
and checks the source `staging-v<version>` tag against its repository and digest.
It cancels the original child before signing, proves no release/new final image
and unchanged prior `latest`/`v0.0.0`, clears the project variable, and retries that
same child. Successful preparation, file-build and image jobs must retain their
original job IDs; the prepared Git tag/commit and original receipt stay unchanged.
The versioned final publication must use the original candidate digest. The
ordinary release-image plan declares no moving tag, so `latest` keeps its initial
presence and digest (including remaining absent); uncertain registry reads fail.
Only a receipt explicitly declaring `latest` may advance it to that same candidate
digest. Other moving destinations are outside this fixture's scope and refused.
`v0.0.0` remains unchanged. Normal signed-release verification still follows.
One explicit nonzero version is required; `next-version` is not accepted for it.
Local inert regressions cover wrapper/DAG ordering, fail-closed checkpoint behavior
and receipt identity; only a live run of the lane can prove GitLab's
cancellation/retry semantics and registry/signing outcome.

`harness-inputs-gitlab` needs no variable beside the prepared directory. The
case fixes private visibility and the reproducible-build check, as it does
for the same demos' releases in `k3s-private-gitlab`, and the builder,
kaniko:

```bash
just test-live-prepared <dir> gitlab '^(TestGitLabRelease_ConsumerBranchProtection|TestGitLabRelease_RejectsPreseededProvenance|TestGitLabRelease_ConsumerInterruptedRecovery)$'
```

The lane `build-capability-gitlab` is the Buildah probe. It runs a `FROM
scratch` build with a `RUN` step in the prepared Buildah job image
(`FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE` of `prepared.env`) on the lab's
privileged runner, as a private project with Buildah, which the lane fixes.
The k3s road has no such runner and reports the probe not covered:

```bash
just test-live-prepared <dir> gitlab '^TestGitLabRelease_BuildImageCapability$'
```

The lane `interruption-manual-gitlab` is the dedicated recipe. It needs no
checkout, archive or prepared directory. The one input the repository cannot
provide is the interrupt, which must come from outside the launcher after
the job is seen running:

```bash
FORGE_CI_LIVE_CONSUMER_VISIBILITY=private just test-live-gitlab-interrupted-job
```

Observe `fci-live-interruption-ready` in the
owned GitLab job trace, then interrupt the launcher with Ctrl-C. Its expected
outcome is exit 130, an `interrupted` verdict and normal exact-resource cleanup
receipts. This is not an automated PASS
and must not be counted as normal successful scenario coverage. If the fixture
finishes its sleep normally, it fails rather than claiming cancellation proof.

For a consumer or a build image of your own, the same scenarios take explicit
inputs in place of the prepared directory's. Checkouts must be clean reviewed
Git roots, kept frozen through the runs, and every archive a reviewed image
whose toolchain fits its consumer. Leave `lint-archive=` out for a consumer
whose gate does not lint; a mismatch is refused at admission. The controller
validates `scenario=` before launch:

```bash
just test-live-gitlab-branch-protection <checkout>
just test-live-consumer gitlab <checkout> release job-archive=<job> build-archive=<build> signer-archive=<signer> lint-archive=<lint> scenario=TestGitLabRelease_RejectsPreseededProvenance
just test-live-consumer gitlab <checkout> release job-archive=<job> build-archive=<build> signer-archive=<signer> lint-archive=<lint> version=0.3.0 scenario=TestGitLabRelease_ConsumerInterruptedRecovery
just test-live-gitlab-buildah <build archive>
```

### Optional Android lane

The lane `k3s-private-gitlab` does not prepare the large Android runtime
image. The lane `k3s-private-android-gitlab` does. Its two cases are the
merge-request gates of the Android demos: `hello-android`, an app, and
`hello-android-library`, a library.

The lane takes one image this checkout does not build, the Java nanolinter
flavour the other private lanes take as `job-image=java:<local image>`. Both
gates lint in it: the demos are Java, and their lint plans run no Kotlin
check. Prepare builds the rest from this checkout: the linux/amd64 Android
runtime archive, target `android` of `containers/runtime/Containerfile`
(`forge-ci-runtime-android-35`), and the kaniko job, signer and Buildah job
images of every prepare. A prepare without the Java flavour is refused before
any image build. `gitlab`, every lane of the forge, includes the lane.

Prepare the lane alone, or add its name to the prepare of the other lanes,
then run its cases, or one of them:

```bash
just test-live-prepare ~/src/forge-lab ~/prepared/android \
  k3s-private-android-gitlab job-image=java:<local image>
FORGE_CI_LIVE_CONSUMER_VISIBILITY=private FORGE_CI_LIVE_ADMIN_REPRODUCIBLE_BUILDS=true \
  just test-live-prepared ~/prepared/android gitlab '^TestGitLabLayer_ConsumerTrial$/^private-android$'
FORGE_CI_LIVE_CONSUMER_VISIBILITY=private FORGE_CI_LIVE_ADMIN_REPRODUCIBLE_BUILDS=true \
  just test-live-prepared ~/prepared/android gitlab '^TestGitLabLayer_ConsumerTrial$/^private-android$/^hello-android-library$/^pull-request$/^kaniko$/^private$'
```

The lane has gates alone. No row judges what a development build of the app
keeps: the product writes no receipt for an Android build.

The gates run without production signing or store credentials. The app's
debug APKs use the normal Android debug signing, and its release APK and AAB
stay unsigned; the library builds its release AAR, which is never signed with
a keystore.

The Android runtime image is the Java runtime image with the Android SDK on
top, and a run holds it three times: in the local image store, as the
archive in the prepared directory and in the lab registry. Each gate also
downloads its Gradle distribution, the Android Gradle Plugin and their
dependencies into the job. Budget that disk before preparing or starting the
lane; the base lanes' headroom estimate does not include Android.

## Trying a consumer

`just test-live-consumer forgejo` answers one question before an engine commit is
published anywhere: does this repository still pass its own workflow with the
layer of this checkout?

```text
just test-live-consumer forgejo <consumer-checkout> <pull-request|release|lifecycle> [setting ...]

just test-live-consumer forgejo /absolute/myapp pull-request CI_JOB_IMAGE=<digest reference>
just test-live-consumer forgejo /absolute/myapp release
just test-live-consumer forgejo /absolute/hello-maven-service/consumer lifecycle CI_JOB_IMAGE=<nanolinter java image, digest reference>
```

`lifecycle` is the Maven service demo's linked flow, pull request to merge to
development snapshot to release, `TestForgejoLayer_ConsumerLifecycle`, in the
scratch repository `fci-consumer-lifecycle` with the layer served as below.
Its `CI_JOB_IMAGE` is a nanolinter image of the java flavour, the one image
the demo's lint and build jobs run in and the snapshot builds the service
with; [Lifecycle](#lifecycle) describes its phases and limits.

It is `TestForgejoLayer_ConsumerTrial` behind `just test-live`'s script, so
the contract, the lock, the preflight and the log are the same. The test
skips unless the recipe names a checkout, and is no part of a full run.

**The consumer** is HEAD of the checkout, named by absolute path. The checkout
is only read: a checkout that differs from HEAD is refused with the files, and
the commits are written in a temporary repository that borrows its objects.
HEAD's tree goes to `main` of the scratch repository `fci-consumer-trial` as
one commit without history, in the checkout's own object format. The layer
under test is served from a second repository, `fci-consumer-trial-layer`,
because the layer's history is SHA-1 and a consumer's may be SHA-256.

Public and private consumer cases both run by default. Set
`FORGE_CI_LIVE_CONSUMER_VISIBILITY=public` or `private` for a focused rerun; unset or
`both` runs the full pair. Other values refuse before creating trial
resources. The same variable applies to the GitLab MR and consumer-release
recipes. Visibility is supplied in the initial creation request and checked
through the provider before any consumer source is pushed. Private trials also
require an anonymous repository API read to be refused.

A run that asks for `private` alone makes every repository that holds the
code under test private: the consumer, scenario, policy and publication
repositories. The repositories that stand in for the public forge-ci
distribution or the lab's infrastructure stay public in every run, each with
its reason (`NewPublicScratchRepo`), and their scenarios run: an engine
layer, whose source and binary package the product's jobs fetch anonymously,
a job image the runner pulls without the job's credentials, a release asset
an in-runner probe downloads, and the pages and packages whose anonymous
read is the subject. A scenario whose subject is one
visibility, such as the private job-token probes, is not covered in a run
that asks for the other. `test/workflowguard` refuses a literal visibility,
a public repository without a reason, or a creating request outside these
helpers.

The small fixtures keep the coverage axes explicit without rebuilding every
consumer for every hash combination:

| Case | What it proves |
|---|---|
| `TestRepository_ObjectFormatRoundTrip` | All four combinations of public/private and SHA-1/SHA-256; push, metadata-driven checkout, exact annotated tag/commit and file bytes. No runner build. |
| Forgejo layer PR and minimal release | Two runner cases, `public/sha1` and `private/sha256`, using the SHA-1 engine fixture. The SHA-256 case is explicitly mixed-format. The axes are independent, so the round trip above tells a failure apart. |
| GitLab check and signed-release fixtures | Native job-token checkout and policy/signing boundaries; real SHA-256 consumers provide mixed-format MR/release coverage. |
| Actual consumer PR/MR and release | The consumer's own committed CI/build commands and native Git object format, in both visibilities. |

Private consumer access must use the admitted account/job/deploy credential;
anonymous repository access must fail. Visibility does not itself make release
packages private: GitLab's separately configured publication project and
Forgejo's owner-scoped packages have their own access policy. These scenarios
do not claim private artifact publication or private hosting of every helper.
The Forgejo layer fetches its helper source and engine package without
credentials, so those fixtures must remain readable. GitLab private helper
projects additionally need the appropriate component access and job-token
allowlist; that combination is not established by a private-consumer result.

What differs for a private consumer, as observed on the lab:

| | Public | Private |
|---|---|---|
| Clone in a job | anonymous | the job's own token (GitLab `CI_JOB_TOKEN`, Forgejo's run token) |
| `platform checkout` from a shell | anonymous | `--token-file`; on GitLab `$GITLAB_TOKEN` alone reaches the API and not Git |
| GitLab release asset links | readable by anyone | a signed-in browser only; a token reads the same file through the uploads API, `/api/v4/projects/<id>/uploads/<secret>/<file>` |
| Forgejo release assets | readable by anyone | readable with the account's token at the same address |

Git histories are not converted between object formats; mixed delivery uses
separate repositories, and a cross-format push into one repository is not a
supported migration route. GitHub.com remains a separate, unproven live tier.

```text
FORGE_CI_LIVE_CONSUMER_VISIBILITY=private just test-live-consumer forgejo /absolute/myapp pull-request CI_JOB_IMAGE=<digest reference>
FORGE_CI_LIVE_CONSUMER_VISIBILITY=private just test-live-consumer forgejo /absolute/myapp release
```

**What is rewritten** is read from the consumer's workflows under
`.forgejo/workflows/`, and applied as text to every tracked text file, because
a consumer holds its policy tests, its documentation and its release gate to
the pin its workflows use:

| In the consumer | Becomes |
|---|---|
| `https://<host>/<owner>/<repository>/.forgejo/actions/` | the same path of the layer under test on the lab |
| `<owner>/<repository>/.forgejo/workflows/` | the same path of the layer under test |
| the 40-character commit every such reference ends in | the commit of the layer under test, wherever the old one stands, a gate that compares a variable with it included |
| the digest a step passes as `expected-sha256` | the digest of the layer's binary at that commit |
| `forge-ci-repository: <literal>` | the repository that serves the layer under test |
| release only: the `signer-image` literal and the `cosign-public-key-sha256` literal | the lab's signer image and the digest of the trial's key, wherever the literals stand |
| release only: a literal `release-git-url` or `release-git-host-key-fingerprint` | the scratch repository's SSH route, from the contract |

A consumer that names the layer in two repositories or at two commits is
refused, and so is a rendering that still fetches some layer from elsewhere,
by a branch name for example: the trial would run a published layer and
report on this one.

**What is set** on the scratch repository:

- `CI_RUNNER_LABEL` and `FORGE_CI_REPOSITORY`, the layer's own variables,
  and for a release `RELEASE_REGISTRY_USER`, the user of the registry token:
  the scratch repository's owner, or the external registry's user below.
- For a release, the image repository on the lab's own registry, under the
  scratch repository's owner, wherever the consumer names its
  `image-repository`, a literal such as `ghcr.io/owner/app` included, so a
  trial never writes to the consumer's real registry. An external registry
  below keeps the consumer's names.
- For a release, a throwaway Cosign key pair, SSH bot key, registered as the
  repository's deploy key, and GPG key. Each reaches the consumer under the
  name the consumer reads it by: where a workflow passes
  `cosign-public-key: ${{ vars.NAME }}` or
  `RELEASE_TOKEN: ${{ secrets.NAME }}`, the trial sets `NAME`. The two tokens
  are the lab account's. Except for the narrow repository defaults described
  below, a fact passed in another form, a key written into the workflow or a
  fallback expression, is refused with the line.
- The caller's settings, each `key=value`:

  | Setting | Meaning |
  |---|---|
  | `<VARIABLE>=<value>` | A repository variable, name in capitals. `@layer-commit` and `@request-commit` as the value stand for the commit of the layer under test and for the commit a release requests, which no caller can know beforehand. |
  | `token=<SECRET>` | A repository secret that receives the lab account's token, for a token the consumer reads outside a call of the layer. |
  | `workflow=<file>` | The workflow to judge. `pullrequest.yml` or `release.yml` when left out. |
  | `version=<X.Y.Z>` | The version a release requests. `0.0.1` when left out, and `0.1.0` on GitHub, which is the version the demos declare: a GitHub release or lifecycle of a demo that bumps proves the version bump only with another version, such as `version=0.2.0`. A consumer whose release does not bump must already declare it, so a demo that declares `0.1.0` asks for `version=0.1.0`. |
  | `next-version=<X.Y.Z>` | Release only: request a greater second version in the same repository with the same signing authority. Cannot accompany `interrupt-after`. |
  | `image=<registry>/<repository>[:<tag>]` | Release only: an image on an external registry that must verify against the trial's Cosign key after the release: the repository's `v<version>` tag, or the tag written, such as a flavour's `v0.0.1-go`. Needs the external registry's token below. |
  | `interrupt-after=<job name>` | Release only: cancel the run once a job whose name starts with this has succeeded, as a release interrupted after that step would be, for example `interrupt-after=Build go flavor image`. The trial then checks that the run ended cancelled and that no release was published. Request the same version again to see what the rerun does with what the cancelled run left: it must replace the staging images and succeed. |

  No setting carries a secret value, and a name the trial sets itself is
  refused.

  For consecutive releases, the consumer workflow must configure its own version
  bump and tool image. The trial adds a fresh candidate after the first release's
  `[skip ci]` commit and waits for a run newer than the first. For a root npm
  project it also compares each signed manifest/lockfile with the downloaded
  package and Build SBOM versions. Example: `version=2.0.1 next-version=2.0.2`.

  **An external image registry** is the one credential from outside the lab a
  trial may carry: for a consumer that publishes its images to a registry such
  as GHCR while its release stays on the lab's Forgejo. Point
  `FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE` at a file only you can read (mode
  0600) holding the token, and set `FORGE_CI_LIVE_EXTERNAL_REGISTRY_USER` to the user
  it logs in as. The token becomes the value of the consumer's secret behind
  the layer's `RELEASE_REGISTRY_TOKEN`; it is never a setting, never logged, and
  the run log records only the file's path and the user. The repository the
  consumer publishes to is its own variable, given as an ordinary setting,
  such as `IMAGE_REPOSITORY=ghcr.io/<namespace>/<unique name>`, with
  `image=` naming the tag to verify. The lab's job containers must reach that
  registry. Packages a token creates
  on GHCR are private until made public in each package's settings; the
  trial verifies with the token, and the base check, which runs anonymously,
  rebuilds instead of reusing private bases.

  **Separate base and final repositories.** On the local registry the trial
  binds `base-repository` to the scratch consumer's repository plus `-base`,
  separately from `image-repository` and `publication-image-repository`.
  These repository inputs may use a plain variable or the narrow default form
  `${{ vars.NAME || 'registry/owner/repository' }}`. A base variable forwarded
  as `image-repository` keeps its base binding. Arbitrary expressions, dynamic
  defaults and fallback trust/key inputs remain refused. Caller settings cannot
  override a repository variable the trial already bound, including with a
  production registry coordinate. This rendering is test-harness behavior and
  does not change the engine runtime API or its recorded binary hashes.

  **A reused signing key.** Each release trial signs with a key of its own,
  so a second release cannot verify the bases the first one signed. To try
  what an organization's second release does, set `FORGE_CI_LIVE_SIGNING_KEY_DIR`
  to a durable directory only you can read containing the original
  `lab-cosign.key`, `lab-cosign.pub`, and `lab-cosign.passphrase`.
  Run `just test-live-signing-inputs` before minting a contract. It checks only
  file metadata and requires existing nonempty inputs; configured reuse never
  silently generates a replacement key. Launcher preflight and key use repeat
  that check, while actual cryptographic validation remains the signer's job.
  Keep private inputs out of temporary-file cleanup and retain only their
  protected directory and the public fingerprint in evidence. Unset the
  variable only for scenarios intentionally using fresh ephemeral authority,
  not for a trial claiming reuse of already-signed bases.


**Release-file retention** has a separate, short live scenario that needs no
container-building workflow:

```text
just test-live '^TestRelease_AssetRetentionPreservesEvidence$/forgejo$'
```

It creates two releases in a scratch repository, proves the default dry run
changes nothing, then applies retention and checks the served bytes: only the
older archive disappears; both releases, their notes, checksums, SBOMs and
provenance remain. The scenario also supports GitLab when selected and ready.
The ordinary live-tier contract and scratch cleanup apply.

**The trigger.** A pull request into `main` from a branch one commit ahead.
That commit adds one file, has a Conventional subject, a sign-off and no line
over 72 characters, and is signed with an SSH key made for it, because a
consumer may lint its commits and require a signature on each. Or, for a
release, an SSH-signed annotated tag `release-request/v<version>` at the head of
`main`. The trial commits the disposable requester's public allowed-signers
entry and maps the workflow's allowed-signers path to that trial file; its
private key stays on the host. The hello fixtures enable request-signature
verification. Actions are off in the repository until everything is in place, so a
workflow the consumer starts on a push to `main` does not run.

**The verdict** is generic. The run of the named workflow succeeded; every
job is printed with its status, and the end of the log of each that failed.
For a release the host also checks that `main` advanced by one commit signed
by the bot's key, that the final tag is signed and points at it, and that the
release, draft or published as the consumer asked, offers at least one Cosign
bundle and each verifies against the trial's public key. Job names and asset
counts are the consumer's and are not judged. The wait is up to 150 minutes,
long enough for a release that rebuilds its base images.

What the caller brings:

- **Images.** A pull-request job runs in the image the consumer's variable
  names, and a lab runner pulls only from the lab. Build it and publish it
  with Forge Lab's `just publish-image --account forgeci <package> <local image>`,
  and pass the reference it prints, as `CI_JOB_IMAGE=` for the consumers
  here. A release needs `FORGE_CI_LIVE_SIGNER_IMAGE` as the release scenarios do.
- **A checkout that builds on the lab.** A consumer whose build reaches
  outside its repository, through a `replace` to a sibling directory for
  example, is tried from a throwaway clone that carries what it needs.
- **A runner that can do what the workflow does.** A release that builds
  images needs a runner that lets a job build one. Forge Lab's runner under
  the contract's label does not; on Compose, try the two image-build cells
  below.

In order, for a release on a prepared road, from this checkout unless said
otherwise:

```text
# 1. The signer image of this commit; keep the reference it prints.
export FORGE_CI_LIVE_SIGNER_IMAGE="$(just test-live-image signer /absolute/forge-lab)"

# 2. In Forge Lab: the consumer's job image; pass what it prints as CI_JOB_IMAGE=.
just publish-image --account forgeci <package> <local image>

# 3. The trial, using a consumer checkout prepared for the lab.
just test-live-consumer forgejo /absolute/consumer release
# For a consumer whose workflow supports platform selection:
just test-live-consumer forgejo /absolute/consumer release RELEASE_PLATFORMS=linux/amd64
```

**Image builds** on Compose are tried in two cells, each public and private
like every trial, and each needs `FORGE_CI_LIVE_SIGNER_IMAGE` as above:

- **Privileged runner, Buildah.** Start Forge Lab's runners with the
  full-release acknowledgement,
  `COMPOSE_FORGEJO_FULL_RELEASE_ACK=trusted-local-full-release just compose-runners forgejo`
  in its checkout, and mint the contract again. The trial sets
  `CI_RUNNER_LABEL_CONTAINER` to the tag of the contract's build runner pool
  unless `CI_IMAGE_BUILDER=kaniko`, so the image builds go there:

  ```text
  just test-live-consumer forgejo /absolute/consumer release
  ```

- **Restricted runner, kaniko.** Nothing more to start. The image builds run
  on the restricted runner in the kaniko job image of this commit, which
  `just test-live-image kaniko-job` builds and publishes:

  ```text
  just test-live-consumer forgejo /absolute/consumer release CI_IMAGE_BUILDER=kaniko \
    CI_IMAGE_BUILDER_JOB_IMAGE="$(just test-live-image kaniko-job /absolute/forge-lab)"
  ```

  A run admits the job image only when its source label names the repository
  the layer runs from. The recipe labels it with the trial's scratch layer,
  read from the contract, so a demo trial, which serves the layer from another
  repository, does not admit it.

The layer is served from `HEAD`. After a change to Go source, `go.mod` or
`go.sum`, run `just refresh-engine-binary` and commit before a trial; it
refuses a checksum that is not this tree's build and a `.forgejo/` that differs
from `HEAD`.

**Maven tests that use Testcontainers.** A plain Maven job has no Docker API,
so a consumer's original tests that start services through Testcontainers
cannot run in it. Do not skip them. A Forgejo `release` trial can give the
build job a Docker engine of its own instead: set
`FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE` to a digest-pinned Maven and JDK
image, which binds the consumer to the Maven application fixture, and
`FORGE_CI_LIVE_MAVEN_TESTCONTAINERS_IMAGE` to a reviewed, digest-pinned
Docker-in-Docker image. The trial fails unless the lab is native amd64, no
demo is selected, and the contract names one build runner tag.

```bash
FORGE_CI_LIVE_CONSUMER_VISIBILITY=private \
FORGE_CI_LIVE_MAVEN_APPLICATION_IMAGE='docker.io/library/maven:3.9-eclipse-temurin-25@sha256:96f175708f766ea049ab6c794a85335d238894c75f8eb364bddd98086a5b7cfd' \
FORGE_CI_LIVE_MAVEN_TESTCONTAINERS_IMAGE='docker.io/library/docker@sha256:2a232a42256f70d78e3cc5d2b5d6b3276710a0de0596c145f627ecfae90282ac' \
just test-live-consumer forgejo /absolute/myapp release version=4.0.1
```

Only the build job changes: it runs on the build runner with a privileged
`docker` service and `DOCKER_HOST` pointing at it. Preparation, signing and
publication keep their jobs and credentials, and no host socket is mounted.
The service's Docker API is unencrypted and reachable on the runner's
network, which on the Compose runner is the shared lab network, so use it only
for trusted, disposable consumers and run one such trial at a time. The image
above is Docker 28, which serves the Docker API that Testcontainers Java
1.21.4 uses; Docker 29 raises the minimum API version and does not work with
it.

Do not publish an image while a trial runs: a scenario that checks the owner's
packages are unchanged then fails. The log's header records the checkout, the
event, the settings and the visibility, so a run can be repeated from it, and
the launcher ends with one `Live result:` line for the whole run.

A failing trial keeps nothing; `FORGE_CI_LIVE_KEEP_SCRATCH=1` keeps both
repositories and the packages for inspection.

Retained Forgejo repositories are archived at teardown, with archive state read
back by repository identity. This stops future scheduled workflows while keeping
the Actions unit and its logs available for inspection. The transient engine
binary package is still retired when the trial ends, so retained fixtures are
not self-contained environments for later runs. An archive failure fails cleanup
and names the repository: its schedules may still run until guarded lab cleanup.
The next trial deletes and recreates its scratch repositories normally.

**Diagnosing a failing in-runner scenario.** A failing scenario prints the tail
of the job log itself, on both forges, because a bare conclusion is the same
defect as a missing tool discovered mid-run: right answer, useless vocabulary.

A job the lab failed to run says nothing about the product, so every place
that judges a failed job first asks why it failed. On GitLab that is the job's
`failure_reason`: `runner_system_failure`, `stuck_or_timeout_failure`,
`scheduler_failure`, `api_failure`, `data_integrity_failure` and
`runner_unsupported` are the lab's, and every other reason, or none, is the
product's; only a job with no reason at all is read for the log's
`Job failed (system failure)` line. On Forgejo it is a job with `runs-on` that
no runner took (`task_id` 0). When the lab caused every failure, the test ends
with a `FORGE_CI_LIVE_INFRASTRUCTURE <test> <job>: <reason>` line; one product
failure among them keeps the product's verdict. Nothing is retried: release
scenarios are not idempotent, and a retry would hide a flaky lab.

When the log is not enough, because the job passed but produced the wrong artifact,
or the failure is in state teardown is about to delete, `FORGE_CI_LIVE_KEEP_SCRATCH=1`
keeps the scratch repository so it can be inspected on the forge:

```text
FORGE_CI_LIVE_KEEP_SCRATCH=1 just test-live 'TestInRunner_.*'
```

It is an environment variable rather than a flag on purpose, awkward enough
that nobody leaves it on. Nothing accumulates either way: the next run deletes
the repository before recreating it, so what is kept is one generation.

It is **never in PR CI**: it is live, destructive, and human-invoked against a
disposable lab.

## Showing a consumer

A trial answers a question and leaves nothing. A demo shows a consumer's
release and pull request to people and keeps them, until a person deletes it.
The demos ship in `test/live/fixtures/consumers/<name>`: a small consumer for each
ecosystem or feature worth showing, rendered for all three forges by `just
test-unit` so none drifts out of what the live tier accepts.
[test/live/fixtures/consumers/README.md](../test/live/fixtures/consumers/README.md) says how one is
laid out. `checkout=/absolute/path` shows any other consumer instead.

Each forge runs the demo's jobs in images the lab must have. For the
`hello-maven` demo:

```text
# The Java lint image, built in a Nanolinter checkout and published to the lab
# in Forge Lab; it prints the reference to pass as CI_JOB_IMAGE on Forgejo.
NANOLINTER_CONTAINER_FLAVOR=java just build-image-local          # in Nanolinter
just publish-image --account demo nanolinter-java localhost/nanolinter:latest  # in Forge Lab
podman save --format docker-archive -o /absolute/images/lint.tar localhost/nanolinter:latest

# The GitLab job image: the runtime image of the demo's toolchain, built from
# this tree, run as its nonroot user. A demo that builds an image in its
# GitLab release, such as hello-maven-service, also takes the Buildah job
# image as buildah-archive=; hello-cargo-container, whose build is a
# Containerfile, takes it as build-archive= too.
just runtime-image-archive java /absolute/images                 # writes forge-ci-runtime-java.tar
just buildah-job-image-archive /absolute/images                  # writes forge-ci-buildah-job.tar

# The signer image of this commit (Forgejo takes the reference it prints,
# GitLab an archive of it).
export FORGE_CI_LIVE_SIGNER_IMAGE=$(just test-live-image signer /absolute/forge-lab)
podman save --format docker-archive -o /absolute/images/signer.tar localhost/forge-ci-live-signer:$(git rev-parse HEAD)
```

The whole lifecycle is then four commands:

```text
# 1. In Forge Lab: the demo account and its contract, with the runners a demo's
#    workflows run on (GitHub needs none).
just lab --account demo --runners gitlab forgejo

# 2. Here: make a demo, the release, then a pull or merge request on the released main.
just demo forgejo hello-maven version=0.2.0 CI_JOB_IMAGE=<lint image reference>
just demo gitlab hello-maven version=0.2.0 job-user=65532:65532 \
  job-archive=/absolute/images/forge-ci-runtime-java.tar build-archive=/absolute/images/forge-ci-runtime-java.tar \
  signer-archive=/absolute/images/signer.tar lint-archive=/absolute/images/lint.tar
just github run --org <org> --ack-org <org> --token-file <pat> --prefix fc- --fixtures none \
  -- just --justfile /absolute/forge-ci/justfile demo github hello-maven org=<id> version=0.2.0 \
  nanolinter-image=<public Java lint image reference>

# 3. Here: see what demos exist, and delete one when it is done with.
just demo-list forgejo
just demo-delete forgejo hello-maven              # shows the set, asks for the name

# 4. In Forge Lab, once no lab demo is left: retire the account's token.
just targets-revoke ~/.local/state/forge-lab/targets/demo.json
```

A GitLab merge-request gate that includes the nanolinter component names its
image `$CI_LINT_IMAGE`, and the demo then needs `lint-archive=` too: a
nanolinter flavour image. It is a separate image because the job image also
runs the release's own jobs, and those verify the signed release request with
`ssh-keygen`, which a nanolinter image does not carry. On Forgejo the lint and
build jobs can share `CI_JOB_IMAGE`: a nanolinter java image carries a JDK and
Maven. GitHub runners pull the lint image themselves, so it must be public.

A gate that checks its parts in images of their own names each further one
`$CI_JOB_IMAGE_<NAME>` as a check's `job-image`, `NAME` in capitals, beside
`$CI_JOB_IMAGE`: a demo takes its archive as `job-archive-<name>=`, a trial as
`FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_<NAME>`. `hello-monorepo` checks its
frontend in `$CI_JOB_IMAGE_NODE`, the Node runtime image.

On GitHub, `demo-list` and `demo-delete` run inside the same hosted session as
`demo`, with `org=<id>`. Every demo repository is named `demo-<name>` on every
forge; trials never use the name.

|  | Trial | Kept for diagnosis | Demo |
|---|---|---|---|
| Recipe | `test-live-consumer forgejo` | the same, with `FORGE_CI_LIVE_KEEP_SCRATCH=1` | `demo <forge>`; `demo-list`, `demo-delete` |
| Lab account | `forgeci` | `forgeci` | `demo` |
| Repository | `fci-consumer-trial` | `fci-consumer-trial`, archived | Forgejo: `demo/demo-<name>` and its layer `demo/demo-<name>-forge-ci`; GitLab: `demo/demo-<name>` and its helpers `demo/demo-<name>-release-*`; GitHub: `<prefix>demo-<name>` in the test organization |
| Afterwards | deleted | kept until the next trial resets it | kept until `demo-delete` |
| Same name again | reset | reset | refused |
| History | one parentless commit | one parentless commit | the consumer's history, then one commit of rendered pins |
| Visibility | public and private | as the trial | public |
| Repository secrets | gone with the repository | left in place | deleted when the run ends |

The two accounts stay apart by construction: demo mode acts only as `demo`, and
every other run of this tier refuses a `demo` contract, so no trial can reset a
demo. Making a demo deletes nothing. `demo-delete` deletes one demo's set
only: it lists the repositories by name and ID, asks for the demo's name (or
takes `confirm=<name>`), and deletes each repository by the lab client or the
GitHub client only while the forge still gives its name the listed ID. On
Forgejo the layer binary package the demos share goes with the last demo; on
GitHub a demo's container packages outlive it and `demo-delete` says so.

On Forgejo the pull request needs the consumer's `pullrequest.yml`, judged
with the same repository variables as the release; on GitLab the merge
request runs the consumer's `.gitlab/merge-request.yml`, which the policy
admits for merge requests.

A GitHub demo lives in the test organization, so it is public on github.com,
and it pins `<prefix>forge-ci`, which every later trial force-pushes: after
the next trial its workflows cannot be rerun, while its runs, release and pull
request stay. Forge Lab's session cleanup deletes only the repositories it
created, by ID, so it never removes a demo. A GitHub demo has no lab account;
its token is the session's personal access token, which the owner revokes.

## Lightweight prerelease API checks

For admission, native release metadata and immutable-retry checks without
runner jobs or runtime-image builds, use the focused lab cases:

```text
just test-live 'TestRelease_(Policy_IsWhatTheForgeRecords|PublishReconcile_RefusesPublishedChanges)$'
```

For GitHub, use a Forge Lab hosted session with no prepared fixtures, then
run `test-live-github-release-api <org-id>` inside it. This creates tiny public
and private scratch repositories, publishes a prerelease asset, retries it,
rejects changed bytes, and deletes the repositories by their acknowledged IDs.
It does not push an engine or run the consumer's release workflow.

```text
# In Forge Lab; supply the acknowledged test organization, PAT path and ID.
just github run --org <org> --ack-org <org> --token-file /private/github.pat \
  --prefix fci- --fixtures none -- \
  just --justfile /absolute/forge-ci/justfile test-live-github-release-api <org-id>
```

These checks prove provider publication behavior, not runner-driven signing,
SBOM generation or the full release job graph. Local workflow/handoff tests
and the full consumer trials cover those separate contracts.

## SARIF ingestion on GitHub

Only GitHub ingests SARIF, so the lab covers the capability by its absence
(`PAR-CAP-2`). `TestGitHubSecurity_SARIFUploadAPI` is the positive hosted
check. In the same kind of session, with no prepared fixtures, it creates a
tiny scratch repository for each visibility, seeds one commit and uploads one
fixture report through the product's own upload (the use case and GitHub
adapter behind `security report upload-sarif`, not an upload action) for that
exact repository, commit, ref and category. Code Scanning must then list
exactly that analysis, processed without error, and its one open alert by
rule, file and commit. A second upload with a random credential GitHub
rejects must be denied, its diagnostics must not carry the credential, and it
must leave no analysis. The repositories are deleted by their acknowledged
IDs. No runner, engine push or scanner is involved, and the CLI's flag and
provider-detection layer is not exercised.

```text
# From the forge-ci checkout, inside `just github run ... --fixtures none --`.
FORGE_CI_LIVE_GITHUB_ORG_ID=<org-id> GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  go test -tags=live -count=1 -timeout=15m -v \
  -run='^TestGitHubSecurity_SARIFUploadAPI$' ./test/live/githubconsumer/
```

Code Scanning is free on a public repository and needs GitHub Advanced
Security on a private one. Where the plan, the repository or the session
credential's Code scanning alerts permission prohibits it, the case is
reported not covered with that reason: it fails a full run and is skipped
only with `FORGE_CI_LIVE_PROFILE=focused`, never passed.
`FORGE_CI_LIVE_CONSUMER_VISIBILITY=public` selects the public case alone.

## Minimal preparation and retention on GitHub

For the standalone and multi-artifact preparation paths without rebuilding the
runtime-image family, use Forge Lab's hosted session and the focused recipe:

```text
just github run --org <org> --ack-org <org> --token-file /private/github.pat \
  --prefix fcm- --fixtures none -- \
  just --justfile /absolute/forge-ci/justfile test-live-github-minimal \
    <org-id> /private/packages.pat
```

This publishes the current production-source working tree into new public test
repositories, including current uncommitted code. It uploads one temporary CLI
asset, pins its checksum in fixture-only bootstraps and reuses existing published
toolchain images. It does not publish runtime images. The npm fixture refuses
signing-key variables in its version hook; the multi-artifact fixture also checks
an engine-owned Gradle text edit. The controller verifies one signed child commit
and, for the multi-artifact path, the signed final tag.

The Packages PAT inspects an existing `fci-prepublication*` package in the same
organization. Retention protects a real digest, refuses a missing pin and compares
version identities before/after its read-only preview. Every created repository
has its ID logged, is deleted through the scoped GitHub client and has deletion
confirmed. This is preparation/retention acceptance, not a full release-asset,
container-publication, recovery or multi-platform trial.

## Self-release scheduling on GitHub

Forge CI's own self-release wrapper has a separate hosted scheduling regression:
run `just test-live-github-self-release <org-id>` inside the same guarded
Forge Lab `github run --fixtures none` session. It projects the candidate's
actual `self-release.yml` dependency graph and conditions onto inert job effects.
GitHub must run both gates whatever the repository variables say, execute
binary publication after a successful release, skip the release and the binary
publication after a failed gate, and skip the binary publication after a failed
release. Exact-ID scratch cleanup is
required. This scheduling projection does not claim to build real binaries.
The same recipe also runs the actual candidate `release-binary.yml` in a separate
disposable repository, with an initially empty release and test-owned request/final
tags. It requires exactly 10 nonempty uploaded assets: three platform archives,
three SBOMs, checksums and their bundle, and provenance and its bundle. It downloads
all six payloads, matches the signed checksums, and verifies both signatures
against the exact workflow/request-tag identity. GitHub-generated source archive
links are not uploaded assets and do not count. Native execution of cross-built
macOS/arm64 archives is not claimed. Scheduling and binary publication run
independently with a concurrency limit of two; no SDK/runtime-image rebuild is
needed. No production repository or release is modified.

## Trying a consumer on GitHub

See [Invariants from native trials](#invariants-from-native-trials) for the
rules behind source preservation, parallel isolation, revision forwarding
and package acceptance.

This hosted trial supports pull requests, independent development builds and stable releases. It checks the
selected consumer workflow and the release postconditions below, including
keyless or key-based artifact verification. It is separate from the local
`PAR-*` conformance suite and does not run every Forgejo/GitLab failure or
recovery scenario. With `image=<name>`, private-consumer container signature
verification pulls from GHCR on the controller with the session's
management/Packages token, which the trial hands to that verification alone
through its own temporary `DOCKER_CONFIG`; no host registry login is needed,
and it must pass rather than silently skip.
Stable-release GHCR packages remain for separate cleanup; disposable service
snapshot trials register exact snapshot-package cleanup as described below.
Availability of this route is not evidence that a particular consumer or signing
backend passed it.

The GitHub layer has no local lab, so its consumer trial runs in a disposable
hosted organization that Forge Lab's hosted session authorizes. Run inside that
session (`just github run --org <login> --prefix <prefix> --fixtures none` in
Forge Lab; the trial creates its own repositories, so the session prepares
none). The session writes the private context `FORGE_LAB_GITHUB_CONTEXT` names.

For parallel consumers, [prepare the shared engine once, then reuse it](#invariants-from-native-trials)
inside that same guarded session, with `FORGE_CI_LIVE_ENGINE_MODE=prepare` and
then `reuse`. Without a mode, each call prepares the engine.

```text
just test-live-consumer github /absolute/myapp pull-request org=<org-id>
just test-live-consumer github /absolute/myapp dev-release org=<org-id>
just test-live-consumer github /absolute/myapp release org=<org-id> version=0.1.0 image=myapp
just test-live-consumer github /absolute/workspace release org=<org-id> version=0.1.0 image=worker,keytool,bff
just test-live-consumer github /absolute/myapp release org=<org-id> RELEASE_PLATFORMS=linux/amd64,linux/arm64
```

A trial builds `linux/amd64` only unless `RELEASE_PLATFORMS=` states otherwise,
as in the lab trials: `linux/arm64` or both. The consumer repository gets exactly
that list as
`RELEASE_PLATFORMS`. The engine repository gets it before its push with
`linux/amd64` always included, because the orchestrators' jobs run on amd64
runners in its runtime images. Every platform builds natively on GitHub's runner
of that architecture (`ubuntu-26.04` or `ubuntu-26.04-arm`). When the trial
ends, the engine repository is set back to `linux/amd64`.

`dev-release` dispatches `release-dev.yml` (or `workflow=<file>`) once on `main`,
without creating stable-release signing secrets. Its workflow must succeed and
produce downloadable artifacts. The trial downloads each artifact without
forwarding its API credential to the signed download URL, checks available
SHA256 metadata and ZIP contents within a 1 GiB compressed/expanded budget per
artifact, and requires unchanged `main`, no tags and no releases. This proves
artifact transport, not application-specific versions, package installation or
container publication, except where the fixture snapshots the service
snapshot's declaration (`.forge-ci/service-development.yml`), as the service
demos' `release-dev.yml` does: the trial then stages that declaration and
judges the run by the snapshot oracle every forge shares
(`livetest.RequireSnapshotImages`), which also holds the manifest at each
published digest to an index of its planned platforms and no other child.
`version=` and `image=` are
stable-release-only settings.

`lifecycle` runs the three fixtures in one disposable repository: a pull
request from `development` judged by `pullrequest.yml`, its native merge
through the API, `release-dev.yml` dispatched on the merged `main` with
`source-branch: development`, judged as a snapshotting `dev-release` is, and
a signed
release request at the merged `main`, judged as `release` is, with the
release secrets set only then. The repository must then hold exactly those
three runs, two tags and one release. It takes `version=` and `image=`, not
`workflow=`; the development workflow must take a `source-branch` input and
run on dispatch only, as the demo's does, so pushing `development` starts no
run. The `hello-maven-service` demo is the consumer it was built
for; [Lifecycle](#lifecycle) describes the phases on every forge.

The shipped demos' cells, each with the settings it needs, are the list
`livetest.GitHubCells`, and `just test-live-github-cells` runs a set of
them from a prepared directory with the engine prepared once; see
[Hosted GitHub cells](#hosted-github-cells).

A variant is a fixture root of its own, named with `fixtures=`, so that an
ordinary trial never installs a second workflow for the same event. The
`hello-go-cli` and `hello-npm-package` demos carry these:

```text
just test-live-consumer github /absolute/hello-go-cli release org=<org-id> fixtures=test/live/github-no-sboms workflow=release-no-sboms.yml
just test-live-consumer github /absolute/hello-go-cli release org=<org-id> fixtures=test/live/github-no-changelog workflow=release-no-changelog.yml
just test-live-consumer github /absolute/hello-go-cli release org=<org-id> fixtures=test/live/github-gpg workflow=release-gpg.yml
just test-live-consumer github /absolute/hello-npm-package pull-request org=<org-id> fixtures=test/live/github-fork head=fork
```

Both release variants are the GPG release with one input changed: files,
commit and tag are signed with the trial's disposable GPG key, so neither
writes to a public transparency log.

No hosted trial writes to a public transparency log. A shipped GitHub release
is keyless, as its example is, and the product then writes every signature to
the public Sigstore log, permanently. The trial therefore signs such a release
with a Cosign key made for it and `sign.transparency: none`: it hands the
release workflow the key and commits the artifacts config with that sign
block, so files, images, SBOM attestations and provenance are still signed
and verified, against the trial's key. A release that signs with GPG or
already sets `sign.transparency: none` runs as written, and a workflow the
trial cannot hand the key is refused. The keyless path is the lab's to
prove, against its own Fulcio: `TestInRunner_KeylessSigningAgainstTheLabCA`
runs when the lab's opt-in `sigstore` profile is up and its contract names
that Fulcio, and reports itself not covered otherwise.

- `release.sboms: none` (`github-no-sboms`): the release must offer no
  `*-sboms.zip`. Every other release whose policy and artifacts share a Build
  or analyzed-artifact layer must offer exactly one.
- An empty `changelog-creator` (`github-no-changelog`), or
  `changelog.skipversionbump: true`: the release prepares no commit, so `main`
  must stay at the pushed commit, with no changelog or project file touched,
  and the final tag must name it.
- `head=fork` (`github-fork`, pull requests only): the change is pushed to a
  fork of the consumer in the same organization, deleted by its ID afterwards,
  and opened against the consumer. The fixture's build enables private
  dependencies, so the run must fail in the build's first step, `Refuse
  private dependency access from fork pull requests`, with the checkout and
  every later workflow step skipped. A private consumer is reported not
  covered: GitHub runs a private fork's pull request workflows only where the
  organization's policy sends them.

By default the controller checkout's `HEAD` is also the engine under test.
`FORGE_CI_LIVE_ENGINE_CHECKOUT=/absolute/committed-engine` selects a separate
committed engine checkout so controller-only fixes can be tried against an
existing engine snapshot. The trial logs both checkout identities.

What it does, in order:

1. **The engine.** `HEAD` of this checkout is force-pushed to `main` of
   `<prefix>forge-ci` in the organization, created public if absent and never
   deleted. The variable `FORGE_CI_REPOSITORY` names it, the `v0.90.0-pre`
   prerelease is made if missing, and the trial waits up to 75 minutes for
   Self Runtime Container to succeed at the commit and move the prerelease to it.
2. **The consumer.** For each visibility (`FORGE_CI_LIVE_CONSUMER_VISIBILITY` selects
   one, as on the lab) a repository `<prefix><checkout name>-<visibility>-<session>` gets
   the consumer's `HEAD`, with `.github/workflows` replaced by its fixtures
   (`test/live/github/workflows` or `tests/live/github/workflows`, or
   `fixtures=<dir>`, naming the root above `workflows/`). Rendering pins every
   `diggsweden/forge-ci` reusable workflow to the engine at its commit, `forge-ci-binary-ref` to
   `v0.90.0-pre`, and `ghcr.io/diggsweden/forge-ci-runtime*` images to the
   organization's `sha-<commit>` tag; a workflow that still calls another engine
   is refused. Only an image a fixture names can be rewritten, so a fixture
   that calls an orchestrator passes `runtime-image` and the image of each
   project type it builds, such as `runtime-image-java`: the orchestrator's
   defaults name released images, which an unreleased engine has not built, and
   a call without `runtime-image` is refused before anything is pushed. `nanolinter-image=` and `megalinter-image=` replace a value
   wherever a fixture workflow passes it, for a mirror. The value must be
   pinned by a full `@sha256:` digest, and a setting no workflow passes is
   refused. A pull-request workflow that still pins its lint image by the
   examples' all-zero placeholder digest is refused before anything is
   created: pass `nanolinter-image=` with an image GitHub's runners can pull.
3. **The verdict.** A pull request: a change branch and a pull request, and the
   `pullrequest.yml` run (or `workflow=`) must succeed. The change is the one
   the local trials make: one added file, a Conventional subject, a sign-off
   and an SSH signature by a key made for it, because the consumer lints the
   commits it reviews. A release: an ephemeral
   passphrase-protected GPG key becomes `RELEASE_GPG_PRIVATE_KEY`,
   `RELEASE_GPG_PASSPHRASE` and `RELEASE_GPG_PUBLIC_KEY`; the separate credential
   file supplies `RELEASE_TOKEN` (never the hosted context's management/Packages
   token), and a signed annotated `release-request/vX.Y.Z` tag starts
   `release.yml`. After it succeeds, `main` must be one signed commit ahead, the
   final tag and a release with assets must exist, and every `*.bundle` asset
   must verify with `cosign verify-blob` against the engine's keyless identity.
   The trial fetches the final tag and `main` and verifies them with `git`: the
   tag must name `main`'s head, and the tag and the release commit must carry
   a signature of the declared `git-signing` method by the release's own key,
   the trial's GPG key or a principal of the `.forge-ci/allowed_signers` the
   trial pushed. A signature of the other method, or by another key, fails.
   A `.bundle` without its offered subject fails, and every nonsignature
   subject must have a verified Cosign bundle or a signature by the trial's
   GPG key. Signature
   sidecars (`.bundle`, `.asc`, `.sig`, `.pem`) are not subjects themselves.
   A fixture whose `release.yml` pins `cosign-public-key-sha256` signs with a
   Cosign key instead: the trial makes a passphrase-protected key pair, sets
   `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE` and the
   `COSIGN_PUBLIC_KEY` variable, writes the key's digest into the workflow,
   and verifies against the public key. With `image=<name>`, `ghcr.io/<org>/<repo>/<name>` at the version, with or
   without its `v`, must verify with `cosign verify` too. A public
   consumer's image is verified with no credential of the trial's. For a
   private consumer the trial writes the hosted context's management/Packages
   token, under the context's actor, as the `ghcr.io` entry of a `config.json`
   (mode 0600) in a new temporary directory (mode 0700) and passes that
   directory as `DOCKER_CONFIG` to the verifying `cosign` only; the directory
   is removed when the test ends. Do not log the host in to GHCR for this. The
   credential is not forwarded to consumer workflows. The snapshot oracle of a
   development build reads the images the same way: a public consumer's
   anonymously, a private consumer's with that actor and token, in process and
   never from the host's Docker configuration.
4. **Cleanup.** The disposable consumer repository is deleted by its ID.
   Service-snapshot `dev-release` and lifecycle trials register removal of their
   exact snapshot container package before dispatch, using the hosted management
   credential and checking package/repository identity; unreadable identity
   refuses deletion and cleanup errors fail the test. This is not a sweep of
   stable-release packages: those remain for the organization's sweep, such as
   Forge Tidy's. The session in the repository's name keeps the next trial clear
   of them: a package outlives its repository, and GitHub denies a new repository of the
   same name a push to it.

Kept demos are separate: use [`demo-list` and `demo-delete`](#showing-a-consumer)
for their guarded named resources, not to sweep disposable trial packages.
`FORGE_CI_LIVE_KEEP_SCRATCH=1` retains lab scratch for diagnosis; it does not
turn a disposable trial into a protected demo.

Each run prints the jobs of the judged run and, for each failed job, the
lines of its log up to the first error, not its cleanup steps.

What the caller brings:

- **A token that can do it.** The context's token creates and administers
  repositories, writes contents, workflows, Actions variables and secrets,
  opens pull requests, and cancels and reruns workflow runs (Actions: write).
- **A separate release credential for `release` and `lifecycle`.** Set
  `FORGE_CI_LIVE_GITHUB_RELEASE_TOKEN_FILE` to an owner-private regular file
  containing a bounded fine-grained PAT. There is no fallback to the context's
  management/Packages token. PR, development and engine-only `prepare` runs do
  not require it. Token-kind admission does not prove provider permissions;
  see [release credential admission](#execution-scopes-and-locks).
- **Public engine images.** A consumer's job pulls the engine's runtime images
  from GHCR without credentials, and the API cannot change a package's
  visibility, so after the engine's first build the owner makes its
  `forge-ci-runtime*` packages public once in the organization's settings.
- **`cosign`, `gpg` and `git` on `PATH`.** The release checks verify locally.
- **A committed checkout.** Both checkouts are read at `HEAD`, never the
  working tree. The engine's `main` in the organization is overwritten.

### Lint image for hosted GitHub runs

GitHub's runners pull the lint image themselves, so a hosted pull-request
trial needs a public Nanolinter image of the flavour the consumer lints with
(`go`, `java`, `rust` and so on; `core` has the shared checks alone). Build it
in a Nanolinter checkout at the commit you want:

```sh
TEST_PLATFORMS=linux/amd64 CONTAINER_ENGINE=podman \
  NANOLINTER_CONTAINER_FLAVOR=rust \
  NANOLINTER_CONTAINER_IMAGE=localhost/nanolinter-ci:rust-source \
  NANOLINTER_BUILT_BY=local \
  just --justfile <nanolinter-checkout>/justfile build-image-local
podman run --rm --network=none localhost/nanolinter-ci:rust-source --version
```

Push it to a public repository you own, such as the test organization's
package registry. A package pushed from the command line starts private:
make it public in GitHub's package settings, then check that it pulls
anonymously by its digest. The local image ID is not the registry's manifest
digest. Pass the published reference to the trial:
`nanolinter-image=ghcr.io/<namespace>/<image>@sha256:<digest>`, which
replaces the image wherever a fixture workflow passes one.

## Service development on Forgejo

A demo's development snapshot is phase 3 of the Forgejo lifecycle
(`TestForgejoLayer_ConsumerLifecycle`; [Lifecycle](#lifecycle)): the
harness's service development workflow
(`test/live/internal/livetest/testdata/service-development.yml.tmpl`) dispatched on the
merged `main`, snapshotting `development` with the shared
`snapshot-images.yml`, which builds the declaration's producers in the image
the harness sets for each producer family. The standalone
`published-unsigned-snapshot` event and its `test-live-forgejo-snapshot` recipe
retired into it; the lifecycle's acceptance is the one they had.

The service snapshot's declaration is the one the consumer's release builds
its images from, `.forge-ci/release-images.yml` when it has one, else
`.forge-ci/artifacts.yml`, overlaid for the trial: platforms narrowed to
`linux/amd64`, unsigned snapshot policy, nothing else changed; the original
release declaration and signing inputs stay where they are. What the snapshot
builds is the planner's rule, read by planning the overlay
(`snapshotimage.New`); the trial's own scope is one container of the root
Containerfile and context with native amd64 declared. A declaration either
refuses is refused before anything is created, never silently reduced.

For the artifact-first service (`hello-maven-service`), the shared snapshot
workflow builds the declared Maven application in the reviewed Java job image
(the `SNAPSHOT_ARTIFACT_JOB_IMAGE_JAVA` repository variable, which the harness
sets to the lifecycle's `CI_JOB_IMAGE`), gathers its bound
`target` JAR input and only then builds the original jar-wrapping Containerfile;
`context-artifact` stays empty. For a container-only declaration, compilation
belongs in the original Containerfile and no unused artifact producer is
scheduled. The snapshot's own producer build is the build; nothing is built a
second time.

The readiness job runs the published digest with Podman on the contract's
container/readiness runner: it needs the Buildah setup helper and apt, trust
in the lab registry configured in the fixture, and rootless port forwarding;
the readiness URL names the container port and Podman maps it to a random
loopback host port. For a private consumer the job logs in with the
development registry credential into a job-local auth file, pulls the digest
and deletes the file before any later step runs. An optional runtime fixture
script (`FORGE_CI_LIVE_SERVICE_FIXTURE`, an absolute regular CI script) then
runs in the readiness job at the selected source, holding no secret, and
writes `trial-runtime/runtime.env` plus optional files, mounted read-only at
`/trial` and passed with `--env-file`; only disposable, nonsecret trial
material belongs there. Readiness makes at most 60
HTTP requests, each bounded to two seconds with two-second retries.

The offline guard for source preservation and the declaration's admission is:

```sh
env GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test ./test/live/internal/livetest \
  -run '^TestService(Development|Snapshot)' -count=1
```

The engine bundles no such script: it is the operator's, written for the
service under trial. One may, for example, generate a one-day key with
`keytool` in the job image and supply inert settings with `example.invalid`
URLs.

## GitLab live acceptance

How the [GitLab layer](gitlab.md)'s components are accepted on a real
instance.

### Live verification

The documented `just test-live` launcher owns the lab boundary and cleanup.
`TestGitLabBootstrap_PublishesAndDownloadsTheRecordedBinary` exercises private
and native job-token publication in separate scratch projects, including
anonymous checksum-verified read-back. Existing native artifact tests prove
job-to-job transport independently of the Actions artifact API.

`TestGitLabCheck_ChecksOutTheConsumerPipelineCommit` includes the actual check
component from a separate scratch project by its GitLab commit SHA. The clean
job verifies the consumer checkout's HEAD and fetched base, reads its tracked
pipeline file and runs the downloaded engine. Provider-free tests exercise
secret and mutable-pin refusals before any checkout.

The private-consumer scenario also checks exact HEAD/base identity, the plain
remote URL and the absence of persisted authentication headers.

GitLab SHA-256 repositories require the `support_sha256_repositories` feature
flag when the project is created. An administrator can enable it for one
account rather than the whole instance; Forge Lab does so for each account it
mints a contract for. Without it, GitLab can create SHA-1
despite a SHA-256 creation request. The check component derives its format
from `CI_COMMIT_SHA`. Direct CLI callers can set `CHECKOUT_OBJECT_FORMAT=sha256`
or pass `platform checkout --object-format sha256` to avoid metadata discovery.

### Trying a consumer MR gate

```text
just test-live-consumer gitlab /absolute/clean-consumer pull-request job-archive=/absolute/job-image.tar [lint-archive=/absolute/lint-image.tar]
```

This command and its `release` and `lifecycle` events run both public and private
consumer cases by default. `FORGE_CI_LIVE_CONSUMER_VISIBILITY=public|private` narrows a
development rerun; unset or `both` runs the pair. Each repository is created with
the selected visibility, verified before
source publication, and private trials must refuse anonymous repository reads.

The consumer must be a clean committed checkout with its own `.gitlab-ci.yml`.
If it keeps the gate in `.gitlab/merge-request.yml`, the standalone MR trial
selects that rendered file as the scratch project's CI configuration path and
verifies the setting before pushing source. Release trials instead retain the
separate release policy, which includes the gate for MR pipelines.
Both paths wait for the native-pushed source branch. GitLab can still briefly
reject MR creation after its branch-read API sees the commit; the harness retries
only the definite HTTP 400 `source_branch: does not exist` rejection. Transport
errors and other uncertain POST outcomes are not replayed.
The focused trial accepts one full-SHA-pinned `check` component and, beside
it, at most one `nanolinter` component, each matching `forge-ci-repository` and
leaving its image to the trial: `job-image: $CI_JOB_IMAGE` and
`nanolinter-image: $CI_LINT_IMAGE`. A gate with the nanolinter component needs
a nanolinter flavour image archive: the recipe's third argument, or
`FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE`; the release recipe takes it as the setting
`lint-archive=`. The
trial rewrites those coordinates to the lab's engine commit and served image
digests, preserving the consumer's commands and other files.
Deployment outside the trial requires the published image digest, as in
[Source-check component](gitlab.md#source-check-component).

Build components run `forge-ci` from their runtime image, bound to
`$CI_JOB_IMAGE` in a trial, so the job image decides which engine a GitLab
build runs. To trial a candidate, build the image from the candidate's tree:

```text
just runtime-image-archive go /absolute/images   # prints /absolute/images/forge-ci-runtime-go.tar
```

It carries the engine the tree records, checked against
`.engine/bin/forge-ci-linux-<arch>.sha256` as published images are, for this
machine's architecture. The image is `localhost/forge-ci-runtime-go:<commit>`,
so commit the tree's image inputs first. A published runtime image carries the
engine of its own release; pairing it with a newer candidate trials an older
engine.

Prepare the trusted job image separately and export it as a Docker image
archive (`podman save --format=docker-archive`). The trial publishes it into
its own disposable GitLab project using the existing confined registry
transport. It builds no job image and changes no runner profile.

The harness opens a real MR with an ephemeral signed change. An inert,
incorrectly scoped signing-key canary must fail before checkout and leave the MR
blocked. The same value is then protected and scoped to `release-signing`;
a new MR pipeline at the same source commit runs the consumer's checks.
The result must be a successful MR pipeline, bound to that exact commit,
with successful-pipeline-required and skipped-pipeline-refusal settings.
Scratch projects, packages and images are cleaned up by the normal launcher
lifecycle. Account credentials are still revoked through Forge Lab afterward.

### Base images

The row `base-graph/hello-base-images` of the GitLab table releases the
demo's graph through the policy's base stages, from a directory prepared for
the lane `base-graph-gitlab`: `just test-live-prepared <dir> gitlab
'^TestGitLabRelease_ConsumerTrial$/^base-graph$'`. It requires the three promoted images under their
content-addressed final tags, each with a signature and attestations that
verify against the release's key. It then releases 0.2.1, which changes no
base input, and requires every stage to be reused: each `Base: sign and
promote` job says the stage was already promoted and verified, and the
release hands on the digests and input IDs the first did, verified again.

### Consumer release acceptance

For a clean committed consumer with `.gitlab/check-release.sh` and
`.gitlab/build-release.sh`, use:

```sh
just test-live-consumer gitlab /absolute/clean-consumer release \
  job-archive=/absolute/check-image.tar build-archive=/absolute/build-image.tar \
  signer-archive=/absolute/signer-image.tar

# Maven consumer: put the requested version in the prepared release commit.
just test-live-consumer gitlab /absolute/maven-consumer release \
  job-archive=/absolute/check-image.tar build-archive=/absolute/build-image.tar \
  signer-archive=/absolute/signer-image.tar version=0.2.0

# Maven service demo: the lifecycle, merge request to merge to development
# snapshot to release (TestGitLabLayer_ConsumerLifecycle; docs/live-runs.md,
# Lifecycle). The build image is also the Java image the snapshot builds with.
just test-live-consumer gitlab /absolute/hello-maven-service/consumer lifecycle \
  job-archive=/absolute/java-image.tar build-archive=/absolute/java-image.tar \
  signer-archive=/absolute/signer-image.tar lint-archive=/absolute/lint-java.tar \
  version=0.2.0 job-user=65532:65532

# Cargo container demo: the same lifecycle, container-first; the snapshot
# builds the runtime image of .forge-ci/release-images.yml, as the release does.
just test-live-consumer gitlab /absolute/hello-cargo-container/consumer lifecycle \
  job-archive=/absolute/rust-image.tar build-archive=/absolute/rust-image.tar \
  signer-archive=/absolute/signer-image.tar lint-archive=/absolute/lint-rust.tar \
  version=0.2.0 job-user=65532:65532
```

The image archives are settings, as for `just demo gitlab`: `job-archive=`
(the non-root check image), `build-archive=` (the release builder) and
`signer-archive=` (the signer image) are required, and `lint-archive=` names
the lint image when the gate lints.

`version=X.Y.Z` selects the requested stable version (the default is `0.0.1`).
The release bumps what the plan of the consumer's declaration bumps, each
project with its native tool where it has one (docs/gitlab.md, Version
bump); a consumer without a declaration must already declare the requested
version. A build using
`--expect-version` correctly refuses a stale POM rather than silently packing
the wrong version. These lowercase settings configure the policy and are not
build environment variables.

`version=X.Y.Z next-version=X.Y.Z` requests a second, greater version in the
same protected repository and policy, signed at the `main` the first release
left. For a root npm package the plan bumps, the oracle also checks each
signed tag's manifest and lockfile, the published tarball and the Build SBOM
version.

Optional trailing uppercase `KEY=value` arguments set literal environment variables only
for the key-free consumer build command. Their meaning belongs
to the consumer, except `RELEASE_PLATFORMS`: a consumer trial on Forgejo or
GitLab sets it to the ordinary runner pool's platform when it is not given, and refuses a
selection of another architecture before anything is pushed, because the lab's
runners build and run what is selected and a release never emulates. On
GitLab it is also the policy's `release-platforms` when the consumer declares
release images. Duplicate
or malformed names are refused;
values are shell-quoted as data. These settings are recorded in the live log
and do not enter the independent preparation, signing, or publication commands.

Which runner a build needs depends on the images it builds. On Forge Lab's
Compose road the ordinary, unprivileged GitLab runner builds images for the
host's architecture in a job (observed for an amd64 release). It cannot run a
`RUN` step of another architecture: emulation registered on the host does not
reach a build nested in a job container, and the step ends with `exec format
error`. Image builds of another architecture than the runner's are refused for
that reason; a lab on hardware of that architecture tries them natively. A
release larger than GitLab's 100 MB artifact default also needs the limit
raised first, with `just compose-gitlab-artifact-limit` or
`just k3s-gitlab-artifact-limit` in Forge Lab. Prepare all three image archives first;
the harness publishes them by digest and takes an exact parentless snapshot of
the admitted consumer tree, preserving SHA-1 or SHA-256 object format. It installs
the signing policy in a separate pinned project and configures protected,
environment-scoped fixture credentials. The consumer build must emit the release
manifest, notes, distro packages and image ledger expected by the policy.
The check script selects the consumer's own language and release-candidate
checks; it runs in the non-root check image before preparation. Both scripts
execute only in key-free jobs. The separately pinned policy retains signing
and publication authority.

For the empty consumer project, the harness installs its main-branch protection
before the initial source push. GitLab permits that first push by the project
owner, and its asynchronous default-protection worker then keeps the existing
rule. This avoids racing the worker by replacing protection after the push.

The host checks the signed Git release and every published Cosign bundle and
GPG package sidecar. It resolves the publication ledger's final tags and verifies
their immutable digests, image signatures and CycloneDX/SLSA attestations with
the fixture public key. These checks complement the build's package/platform
matrix; a generic trial does not establish a consumer-specific output count.
Scratch cleanup and credential-redacted evidence use the normal live launcher.
The wrapper restores the normal runner profile; revoke the fresh batch's
credentials through Forge Lab afterward.

Before a full consumer build, validate the prepared Buildah job image with
the focused `TestGitLabRelease_BuildImageCapability` scenario, the lane
`build-capability-gitlab`: `just test-live-prepared <dir> gitlab
'^TestGitLabRelease_BuildImageCapability$'`
([Scenario lanes on GitLab](#scenario-lanes-on-gitlab)). This
exercises storage setup with package installation disabled and a real `FROM
scratch` / `RUN` build in the runner; it does not publish the built probe
image.

### Image builds with kaniko

A lab with no runner that permits user namespaces, Forge Lab's k3s road for
one, builds GitLab release and development images with kaniko on the ordinary
runner. `FORGE_CI_LIVE_GITLAB_IMAGE_BUILDER=kaniko` selects it; `buildah`, the
default, keeps the privileged runner. On GitLab the builder is the release
policy's `image-builder` input, so it is this trial setting and not a consumer
variable: `CI_IMAGE_BUILDER=` among the consumer settings is refused.

A consumer releases images on GitLab when its `.forge-ci/artifacts.yml`
declares containers: the harness then sets `release-images: true`, leaves
`artifacts-config` empty, the release component's default, and passes the
lab's one platform as `release-platforms`. A consumer whose artifacts.yml
carries a shape release images refuse, such as `extract`, declares them in
`.forge-ci/release-images.yml` instead, the file its Forgejo release names as
`artifacts-config`, and the harness names it too. A consumer with both files
whose artifacts.yml would serve, or with release-images.yml and no containers
in artifacts.yml, is refused: one release reads one declaration. The release's image jobs
run in the builder's job image of this commit, a saved archive: under Buildah,
the default, the Buildah job image named by
`FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE`
(`bash scripts/ci/publish-live-image.sh buildah-job <forge-lab> <archive>`
builds and saves it); under kaniko the kaniko job image named by
`FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE`. The build archive stays the
consumer's build tools. A `job-user=65532:65532` setting runs the release's
`job-image` jobs as the runtime images' `nonroot`. `just test-live-image kaniko-job` builds
`localhost/forge-ci-live-kaniko-job:<commit>`. A run admits a kaniko job image
only when its source label names the engine project the layer runs from, a
scratch project of each scenario's own, so the harness labels the image with
that project as it pushes it; the source label the recipe gives it for the
Forgejo trial does not matter here.

```text
just test-live-image kaniko-job /absolute/forge-lab >/dev/null
podman save --format docker-archive -o /absolute/images/kaniko-job.tar \
  "localhost/forge-ci-live-kaniko-job:$(git rev-parse HEAD)"

FORGE_CI_LIVE_GITLAB_IMAGE_BUILDER=kaniko \
FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE=/absolute/images/kaniko-job.tar \
  just test-live-consumer gitlab /absolute/clean-consumer release \
  job-archive=/absolute/check-image.tar build-archive=/absolute/build-image.tar \
  signer-archive=/absolute/signer-image.tar
```

The release policy then takes `image-builder: kaniko`, the pushed kaniko job
image as `builder-job-image`, and the selected pool's tags in the exact
per-platform build input, just as with Buildah. Kaniko does not require a
Buildah-capable pool, but does not omit the build selector. The lab's runners
are of one architecture; native arm64 release-image acceptance remains not
covered. A development scenario that builds images
runs its job in its consumer image archive, which must then be a kaniko job
image, untagged, with `CI_IMAGE_BUILDER` and `CI_IMAGE_BUILDER_JOB_IMAGE`
added to that job alone. The base-image fixture builds its base in the
release's export jobs with the selected builder (`multiarch-image build-arch
--stage`), and its build job only scans and uploads the staged layout when no
verified base exists. A consumer build that extracts a binary with `forge-ci
container build`, such as hello-cargo-container's, belongs to its export
script. The Go CLI integration fixture's checks are Buildah's own
integration tests, so they still need the build runner pool, and are not
covered without it; `TestGitLabRelease_BuildImageCapability` probes a
Buildah builder. The minimal release builds no image.

The snapshot scenarios (`TestForgejoSnapshotArtifactFirst`,
`TestGitLabSnapshotArtifactFirst`) build with the run's builder too: on
GitLab this setting, on Forgejo `FORGE_CI_LIVE_FORGEJO_IMAGE_BUILDER`, which
the snapshot workflow receives as the consumer's `CI_IMAGE_BUILDER` and
`CI_IMAGE_BUILDER_JOB_IMAGE`. Under kaniko they push the kaniko job image of
`FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE`, labelled with their engine
project, and build on the ordinary runner pool on Forgejo. Under Buildah, and
on GitLab under either builder, they build on the build runner pool: GitLab's
ordinary pool takes untagged jobs and the product asks for a build tag. A lab that cannot run the selected builder
reports them not covered. A Forgejo consumer trial selects its builder with
`CI_IMAGE_BUILDER` and refuses a different Forgejo setting.

The Forgejo container, Rust and npm application development
trials also use `FORGE_CI_LIVE_FORGEJO_IMAGE_BUILDER` (Buildah by default).
Kaniko uses `FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE`, labelled for and served
from the trial's existing public scratch layer. The consumer keeps its selected
visibility; the builder image does not become a consumer publication.

The native container and Rust trials run the consumer's own development
workflow. A Kaniko run admits its image-building job before creating remote
resources: it must use the pinned `setup-image-builder` action, forward
`vars.CI_IMAGE_BUILDER` and `vars.CI_IMAGE_BUILDER_JOB_IMAGE` to the same-named
job (or workflow) environment entries, and take its job container from
`vars.CI_IMAGE_BUILDER_JOB_IMAGE` (container trial) or `vars.CI_RUST_BUILD_IMAGE`
(Rust trial). Both scalar `container:` and `container.image:` bindings are
supported. This is a bounded fixture contract, not a general workflow validator;
an old workflow that explicitly prepares Buildah is refused with an actionable
error rather than silently running another builder.

For Rust, the archive must be the complete development job image built FROM
`containers/kaniko-job`, including the required Rust toolchain. The trial binds
that one admitted image to both image variables; `CI_RUST_BUILD_IMAGE` is only
needed separately for a Buildah run. Native container recipes likewise need all
their pre-build tools in the archive. Tools used after a Kaniko build, including
Trivy when scanning in the same job, must survive its root-filesystem cleanup
under `/kaniko`; see `containers/kaniko-job/Containerfile`.

The npm application and Maven service fixtures call the shared snapshot
workflow, which already selects the builder and job container. Maven still
needs the build runner pool for its Podman service-readiness check with
either builder; it keeps the existing single container-runner binding. The
registry/signer smoke remains an explicit Buildah storage-capability probe and
is not covered by a Kaniko run. Conflicting consumer-plan/runtime bindings fail
before a consumer push or workflow start; the error names the variable without
printing its value.

## Package publication scenarios

These scenarios publish packages to the lab forges' registries and read them
back independently of the publisher. Each runs with
`just test-live '^<test>$'`.

| Test | What it checks |
|---|---|
| `TestInRunner_ExportedMavenPublications` | Stable and repeated snapshot uploads of exported Maven publications on Forgejo and GitLab runners, read back through the registry API, Maven metadata and archive contents. Needs `workflow-runs` and `packages`. |
| `TestInRunner_ForgePackages_MavenDeployReachesTheRegistry` | A native Maven build with a project-owned profile deploys main, source and Javadoc JARs; a checksum receipt frozen before publication must match the four files downloaded from the registry. A `-snapshot` subtest per forge deploys the same build as `-SNAPSHOT`. |
| `TestInRunner_ForgePackages_GradleLibraryReachesAFreshConsumer` | On Forgejo, a [two-build fixture](../test/live/fixtures/libraries/gradle-jvm/README.md) exports and uploads a Gradle library; a second build with no token and an empty cache resolves it, runs its code and compares what it resolved with the exported files. |

They do not cover GitHub Packages or Sonatype; the Central lifecycle tests use
local HTTP collaborators.

`TestForgejoLayer_GoModuleReleaseReachesAFreshConsumer` runs the minimal
signed binary release in a repository whose `go.mod` declares its own path,
then has a downstream module resolve the final tag with `GOPROXY=direct`, no
checksum database, no credential and an empty module cache, and run the
library's code. It needs the signer image, and skips on a forge reached on a
port, which a Go module path cannot name:

```sh
export FORGE_CI_LIVE_SIGNER_IMAGE="$(just test-live-image signer /path/to/forge-lab)"
just test-live '^TestForgejoLayer_GoModuleReleaseReachesAFreshConsumer$'
```

`TestForgejoLayer_CargoBinaryRelease` runs the same release with a
dependency-free Cargo crate built artifact-first with `build-cargo`, and
requires the `Prepare: version rust` job, a release commit that changes exactly
`CHANGELOG.md`, `Cargo.toml` and `Cargo.lock`, and the version in the published
binary. It needs the signer image and `FORGE_CI_LIVE_CARGO_RELEASE_IMAGE`, a
digest-pinned Rust job image, and skips without it.

For the external Rust workspace's role declaration and publication layout, see
the [Rust development fixture contract](../test/live/fixtures/development/rust-development/README.md).

## What the conformance scenarios cover

[Providers](providers.md) says what each forge *supports*; this says what the
conformance scenarios check against a real GitLab and Forgejo:

| Group | What it settles |
|---|---|
| `TOK-1..4` | the run's own credential validates; a refusal is classified as a refusal and not as an outage; bot permissions match real access |
| `REL-1..5` | create with assets, verified through the raw forge API; asset upload as its own role; re-release of an existing tag; `release publish`; a 40 MiB asset served back with a matching digest |
| `REG-1..5` | registry fixture; image ledger against a real registry; cleanup deletes staging without disturbing the release; promotion rollback; `ResolveRegistryAuth` |
| `SIGN-1..3` | `container ledger sign` against a real registry; GitLab and Forgejo keyless OIDC against the lab's own Fulcio; `container base-images sign` accepted by `base-images promote`, and refused for another workflow |
| `ART-1..3` | a forge without an artifact store refuses and says what is missing; one with a store does not refuse as though it lacked one; run-artifact round trip |
| `PKG-1..2` | `publish forge-packages` for npm and Maven, where the auth schemes diverge (Job-Token on GitLab, `Authorization: token` on Forgejo) and both are proven |
| `CAP-1..3` | the published matrix is rendered from the adapters and fails on drift; SARIF and provenance refusals |
| `RUN-1..5` | Forgejo resolves as Forgejo and not GitHub on a real runner; runtime self-report; annotation dialect; step summaries; output-file writes |
| `CHK-1` | `platform checkout` |
| `UX-1..4` | printed links resolve; `--dry-run` mutates nothing while the same verb does mutate when real; identical `--json` shape; identical exit code per failure class |
| `TestForgejoLayer_*` | the Forgejo delivery layer on a real runner: an artifact handed between two jobs that set the layer's binary up from a pinned commit; `prepare-release.yml` stopping on a missing secret before it fetches or pushes anything; a consumer pull request built, tested and passed through `check-l3-isolation` with nothing published; a signed, published release verified from the host; an image built in a job, pushed, recorded in the ledger, signed and verified as a base; a release request whose signer the consumer's allowed-signers file does not cover, refused before the bot's key is loaded |

GitHub has no conformance scenarios: the
[GitHub consumer trial](#trying-a-consumer-on-github) runs real workflows in
the test organization and checks what it states. SARIF upload and the GitHub
attestation API have their own positive checks; a successful release does not
prove either.

Keyless verification needs the selected issuer's trust root: Cosign cannot
infer a private CA root from the certificate it has just received. Supply that
root explicitly and verify the configured issuer and signing identity.

## Shared dependencies

Provider separation alone does not prove independence. Target rotation, runner
reconciliation, package coordinates, local image builds and backing services can
be shared. In particular, the Compose runner's shared network and a `docker`
service alias can collide across jobs. Serialize that resource or choose cases
that do not require it; keep other ready lanes moving. Keep each lane's source
frozen and its evidence separate.

## Invariants from native trials

Native consumer trials have shown these rules. Each one holds for every
consumer and forge unless the rule names a forge, and most have a
regression test.

**Running trials side by side**

- Prepare the engine once, then reuse it. `FORGE_CI_LIVE_ENGINE_MODE=prepare`
  prepares the engine without running a consumer, and `reuse` only checks
  the prepared engine. Prepare every image platform first, and keep the
  engine unchanged until every consumer has finished.
- `prepare` reruns the failed jobs of a failed or cancelled engine build
  once and judges that attempt. `reuse` never reruns
  (`TestPrepareRerunsAFailedEngineBuildOnce`,
  `TestReuseNeverRerunsAFailedEngineBuild`).
- Give each consumer, visibility, event and workflow its own repository.
  Unsigned and test-signed development workflows collide otherwise.
- Name several images in one comma-separated `image=` setting. A repeated
  setting is refused
  (`TestParseGitHubTrialSettings_RejectsRepeatedImageSelectors`).
- Bound concurrency, keep one log and exit status per case, and wait for
  every case and its cleanup before the session ends. A cancelled run is a
  final result, not a run to wait for.
- Local lab trials share fixed resources behind an account lock. Parallel
  hosted work is no reason to bypass it.
- A multi-release GitLab fixture seeds its branch only once
  (`TestPrepareGitLabTagsSeedsOnlyTheFirstRelease`).
- A second GitLab release needs a new admitted candidate. The release
  commit carries `[skip ci]`, so a request signed at that commit gets a
  skipped pipeline, and a skipped pipeline is not a release.

**Contracts between workflows, jobs and the CLI**

- A hosted trial exports the committed source tree, including tracked files
  that match `.gitignore`, and nothing uncommitted
  (`TestGitHubConsumerPreservesTrackedIgnoredSource`).
- Pass required CLI values through their declared flags or environment
  variables, and run the real workflow step through the real CLI where you
  can. A list of flag names cannot catch a stray positional argument.
- Forward `forge-ci-binary-ref` through every reusable workflow, so a
  branch or fork test uses the engine the outer workflow selected.
- GitLab dotenv outputs use upper-case keys (`IMAGE=`, `DIGEST=`). Test
  hand-off scripts with bytes from the real provider output.
- A job that calls a reusable workflow caps the permissions of every job
  below it. GitHub checks this before any job starts and ends the run as
  `startup_failure` with no log
  (`TestInternalCallersGrantWhatTheCalledWorkflowsAsk`).
- Declare the release-file manifest once, after the last unsigned assembly
  step. Keep the write-once guard.

**Builds and packages**

- Under the policy `enforce-application-tests: true`, failing npm tests stop the job
  before build, SBOM and pack. A missing test script is reported as
  skipped, not as passed (`internal/app/build/npmreleasebuild_test.go`).
- Test an install or deployment from the packed artifact, outside the
  source tree. A source checkout hides missing files.
- An extracted binary must be nonempty before any file is renamed
  (`TestSuffixExtractedBinaries_EmptyBinaryRefusesBeforeAnyRename`).
- Record the tool version that actually ran, not the configured one.
- When forge-ci passes `--timestamp` to Buildah, it removes an inherited
  `SOURCE_DATE_EPOCH` from the child environment
  (`internal/adapters/buildah/timestamp_test.go`).
- Maven projects need a fixed `project.build.outputTimestamp` to build
  reproducibly.
- Generated SARIF `automationDetails.id` values end in `/`. GitHub reads
  the text before the last slash as the category, so an id without one has
  an empty category.

**Publication and privacy**

- A successful upload is not an install. Resolve the package from a fresh
  cache and compare the bytes with what was built.
- Check private denial anonymously at the same paths an authenticated read
  succeeded on. Only HTTP 401, 403 or 404 counts as denial.
- Registry redirects never forward credentials, and a login page is never
  accepted as an artifact.
- Strip ANSI sequences before redacting credentials, under a fixed byte
  locale (`scripts/ci/livescript_test.go`).

