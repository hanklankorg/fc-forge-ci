# Testing conventions

Test architecture for the Go runtime. Follow these patterns when adding new
code; PR review checks that the layers are respected and the helpers are
used.

> Current testing policy for the Go codebase.

## Layers

Tests sit in one of the buckets below. Each has a different shape, build
tag, and CI gate. Two of them live outside this repository's `go test ./...`:
the black-box suite is its own repository, and the live tier needs a lab.

| Layer | What it tests | Build tag | Speed | Parallel | Network / CLI |
|---|---|---|---|---|---|
| **domain** | pure logic: parsers, transforms, decisions | (default) | <50ms | yes | no |
| **adapter** | I/O: real CLIs (`gpg`, `git`, `trivy`), real HTTP | (default); `integration` for full-stack | <2s | yes (per package) | yes |
| **repo guards** | rules about the tree itself, not about behaviour | (default) | <1s | yes | no |
| **Forgejo layer** | the shell of `.forgejo/` workflows and actions, against fakes and against the CLI built from the tree | (default; Linux) | seconds, plus one CLI build | yes | builds binary |
| **CLI smoke** | the binary as a black box, in this repo | `smoke` | <5s | no | builds binary |
| **black-box suite** | the binary against real toolchains and fixtures in the companion testsuite | `blackbox`, in that repo | minutes | yes | real tools; forge endpoints faked; package downloads may use network |
| **live / conformance** | the same scenario against configured real lab forges (currently GitLab and Forgejo, not GitHub) | `live` | minutes | per-forge subtests only (`-p 1`) | a real lab |

Run them as:

```text
go test ./...                          # domain + fast adapter + repo guards
go test -tags=integration ./...        # + full-stack adapter
go test -tags=smoke ./cmd/...          # + CLI smoke tier
```

The provider-free tiers require `bash` and `jq` because `mockbinary` records
subprocess calls through a shell fixture. Untagged tests also use Git for local
fixtures, including `git init`; they do not require real GPG or containers.
`jq` and the rest of the project
toolchain are pinned in `.mise.toml`; run `mise install` before the gates.

`just verify` is the local lint plan plus tagged compilation and integration
tests (which include untagged tests). Integration can run containers, fetch
images and skip optional evidence; builds/tests also write caches and may fetch
modules. CI additionally runs standalone workflow-input-default validation,
CLI smoke/fuzz jobs and companion strict compatibility, with strict integration
in release CI. See the workflow files for each exact gate.

### Skipped evidence is accounted for

A tier that skipped half of itself and one that ran all of it both end in
`ok`, and `go test` throws away what a passing package printed, so the skip
that explains the difference is not in the log anyone reads. Every skip that
stands for evidence the run could not produce therefore goes through
`internal/testutil/evidence`:

```go
binary := evidence.RequireTool(t, "trivy")     // absent tool: tally, then skip
evidence.NotProvenf(t, "a reason, without run-specific text")
```

Both record into a per-binary tally, and `evidence.Main` — wired up as the
package's `TestMain` — prints it once at the end:

```text
NOT PROVEN: buildah.test: 3 test(s) skipped for 1 unproven cause(s):
  buildah is not on PATH (3 test(s))
```

`just test-integration` (also used by `just test-coverage`) points
`FORGE_CI_TEST_NOT_PROVEN_FILE` at
`bin/not-proven.txt` and prints it after the run, because a summary on stderr
is discarded for a package that passes. In CI the summary is appended to
`GITHUB_STEP_SUMMARY` when the runner set one.

**A tool that is absent may skip; a tool that is present and then fails must
not.** Absence is a fact about the machine — a developer without buildah
still runs the rest. A buildah that IS installed and cannot create a
container, a trivy that exits non-zero on a scan that needs no network, a
fixture directory that is empty: those are broken environments or broken
fixture sets, and each must fail. The buildah integration test used to skip
on any buildah error, so a storage or permission misconfiguration reported
as green; it now fails, and `requireBuildahStore` says why.

The strict profile removes the first allowance too, which is the shape a gate
wants:

```text
just test-integration-strict            # FORGE_CI_TEST_PROFILE=strict
```

There, a missing tool is an error rather than a tallied skip. Any value of
`FORGE_CI_TEST_PROFILE` other than empty or `strict` is refused with exit
code 2 — a typo in the variable that turns the accounting on must not turn it
off instead. The profile is read when the test binary loads, not when a skip
happens, because `isolatedenv` clears every `FORGE_CI_` variable inside an
isolated test and reading it there would silently downgrade a strict run.

Architecture guards use bounded source analysis, not proof of executed behavior.
The environment-read guard uses the matching installed Go compiler's export
metadata and a populated module cache, with downloads disabled. It follows the
selected build configuration; it does not scan every platform/tag variant.
Its compiled working-directory check supplies the effective module cache before
replacing HOME and uses a fresh owned build cache. Exec-path analysis rejects
unsupported tracked paths but does not discover arbitrary command values carried
through fields, interfaces or captured closures.

The environment guard's model is bounded on purpose, and the boundary is not
where you might guess. It resolves stdlib object identity, string constants
across files and imports, and function values written once in the package. It
does not follow a lookup carried through a struct field, a reassigned variable
or a parameter — and it cannot be made to fail closed on those, which is the
part worth writing down. A rule that treated any `func(string) string` callee as
an environment read was built and reverted: it flags the sanctioned seam, where
a use case accepts an injected lookup from the composition root, and two
fixtures (`injected lookup`, `shadowed package parameter`) pin that pattern as
permitted. Nothing local separates a lookup handed in by the composition root
from one smuggled through a field. Distinguishing them needs interprocedural
analysis; until something does, "the guard reports the flows it models" is the
supported claim, not "no unmodelled flow exists".

Which files a guard is eligible to read follows from how it reads them, and the
two mechanisms disagree. The source-walking guards (credential, exec-wrap,
provider-switch) parse with `go/parser`, which ignores build constraints, so
they read a `//go:build !linux` file on Linux. The environment guard loads
through export metadata, which honours them, and so does golangci-lint, which
runs the depguard layering rules, so the same file is invisible to both.
Neither is wrong; neither announces itself. Every build-constrained product
file is therefore declared in
`TestGuardEligibility_ConstrainedAndGeneratedProductFilesAreDeclared`, and an
undeclared or changed one fails until someone says how the scanners treat it. A
variant the current build excludes must contain no direct environment read and
no internal import: the check that would judge it cannot see it, and the fix
for a variant that needs one is to run that check under a matching GOOS, not to
widen the list. No product file in this repository is machine generated; a
guard fails if one appears, because a violation inside generated code is a
report against the generator rather than something a contributor can fix in
place.

`TestEveryCommandGroupHasALiveDisposition` maps every command group under
`internal/cli/commands` either to one conformance scenario or to its coverage
gap. It fails on an unmapped group, a stale entry, or a named scenario that is
gone or no longer holds its group's name as a string literal. Missing `version`
live coverage and eight other group-specific gaps remain visible; a gap entry
is debt, not evidence of live parity.

### The Forgejo layer tier

The workflows and actions under `.forgejo/` are YAML with shell in it, and the
shell is what a release runs. Two packages test it, both in the default
`go test ./...` and both behind `just test-forgejo`:

- `test/forgejoguard` reads the YAML as a document and holds its structure:
  which job receives which secret, the bootstrap order of every privileged job,
  the coordinate preflight, consumer routing, strict YAML, that no file names
  an organization, and that every workflow starts only when called. It runs the
  preflight scripts against fixed inputs and nothing else.
- `test/forgejobehavior` extracts a step's shell and runs it under a minimal
  environment. Most tests put a recording fake in the engine's place and
  assert the argv, the environment and the status handling.

The **engine tier** is the part of `test/forgejobehavior` that puts the real
CLI there instead: `scripts/ci/forgejo-binary.sh build` builds it from the tree
once per test binary, and the step's own shell runs against it. A renamed verb
or flag, a refusal the workflow relies on, or a manifest the signer step would
finalize differently fails here and not inside a release job
(`TestForgeCICLIContract` checks every call in `.forgejo/`, the Forgejo and
GitLab examples under `examples/`, `.github/workflows/`, `templates/` and
`.gitlab/bin/` against the built CLI's own help). Nothing in the tier
reaches a forge, a registry or a container runtime.

The same build is the reference for the binary the layer runs, which is not
in the tree: a job downloads it by the checksum in
`.forgejo/bin/forge-ci.sha256`.
`TestRecordedEngineChecksumIsABuildOfThisTree` fails when that line is not
the digest of this build, which is what happens after a change to Go source,
`go.mod` or `go.sum` until `just refresh-forgejo-binary` is run, and its
message prints the line to record. The build names no commit, so the test
needs no Git history and follows the working tree as it is. It is not
proven, through `evidence.NotProvenf`, in one case: the local Go toolchain is
not the release `go.mod` pins. The strict profile turns that into a failure.

A workstation keeps that allowance. This repository's gates do not: the
pull-request workflow has a job, `forgejo-layer-binary`, that sets up exactly
the Go of `go.mod` and runs the test with `FORGE_CI_TEST_PROFILE=strict`,
so a checksum that is not a build of the tree fails every pull request.
`TestSelfGatesProveTheForgejoLayerBinary` holds the job. The release gate
runs the whole tier strictly.

The download itself is tested against a local HTTPS server standing in for
the package registry: the address per context, the refusals, the strict
mode, and `forgejo-binary.sh publish`.

What this tier cannot show is the runner: how Forgejo expands a called
workflow, resolves `vars`, or schedules the jobs. That is acceptance on a real
instance, and the [live tier](#the-live-tier) runs it: its `LAYER` scenarios
serve this checkout's `.forgejo/` from a scratch repository on the lab and run
consumer-shaped workflows against it, and `just test-live-consumer` runs a
real consumer checkout's own workflow against it; see
[Trying a consumer](#trying-a-consumer).

### The black-box tier lives in another repository

`cmd/forge-ci/smoke_test.go` is a smoke harness: it builds the binary and
checks the root contract (help, version, flag parsing, the exit-code ladder).
The bulk of the black-box scenarios (every ecosystem's real toolchain, the
signing round-trips, reproducibility, host isolation) are in the companion
repository, which drives the built binary against committed fixtures.

`docs/cli-black-box.md` is the catalogue for both. When adding a scenario,
the split is: if it needs a real toolchain or a fixture project, it belongs
in the testsuite repo; if it is about the CLI's own surface and needs nothing
installed, it can stay here.

Note the vocabulary: "e2e" is reserved for a real runner talking to a real
forge, which is neither of these. The in-repo tier is named for what it does,
`smoke`, so the word is free for the thing that earns it.

### The live tier

`internal/livetest/` holds a kit and `internal/livetest/conformance/` the
scenarios (`PAR-*`). One scenario body runs against every configured lab forge
that *claims* the capability it needs, so parity is enforced by shape rather
than by discipline. The alternative, a suite per forge, is how parity rots.

It exists because every other layer verifies an adapter against a fake written
from the same assumptions as the adapter, so an assumption that is wrong is
wrong in both places and both agree. Only a real forge breaks that tie. It is
what found GitLab's missing delete-then-create on re-release, the registry
adapter reporting permanent failures as retryable, and that Forgejo drops
untagged manifests while GitLab keeps them.

#### Operator card

The four commands an operator types, in order; the sections below say what
each one needs and what it leaves behind.

```bash
# In Forge Lab: providers, fixtures, runners and the forgeci contract.
just lab --account forgeci --fixtures --runners gitlab forgejo
# Here: the contract and CA check alone, in seconds, before anything heavier.
just test-live-preflight full
# A consumer's own workflow against this checkout, both visibilities.
just test-live-consumer /absolute/consumer pull-request CI_JOB_IMAGE=<digest reference>
# A kept demo: make one, see what exists, delete one when done with.
just demo forgejo hello-maven version=0.2.0 CI_JOB_IMAGE=<reference>
just demo-list forgejo
just demo-delete forgejo hello-maven
```

Before any launch, check that no other live run holds the lock and no
`run-live-tests.sh` or `forge-labctl release run` process exists; two runs on
one lab account clobber each other, and the full-release wrapper swaps the
runner. [Running it](#running-it) prepares the lab, [Trying a
consumer](#trying-a-consumer) explains the trial, [Showing a
consumer](#showing-a-consumer) the demo.

#### Running it

The tier needs one input: a neutral live-target contract, version 4, for this
suite's lab account, `forgeci`. Forge Lab writes it to the contract module's default
path, `$XDG_STATE_HOME/forge-lab/targets/forgeci.json` (`~/.local/state` when
unset), as JSON data, never sourced, owner-only on disk. Everything else is
read from it.

Prepare a road using Forge Lab's README and
`docs/modules/ROOT/pages/how-to.adoc` first. Human
`just setup-lab compose` or `just setup-lab k3s` supplies host DNS/trust and
providers; it does not supply fixtures, runners or Fulcio. Read the k3s runner
security guide before using its invocation-only DinD acknowledgement. Then,
in the Forge Lab checkout, choose **one** road:

```bash
# Compose: prepare both full-profile providers, fixtures and runners.
just lab --account forgeci --fixtures --runners gitlab forgejo
just compose --profile gitlab --profile forgejo --profile sigstore up -d --no-recreate
just ready --wait gitlab forgejo
just targets --account forgeci gitlab forgejo

# Or k3s, after its host/cluster prerequisites and pinned runner images:
K3S_PRIVILEGED_DIND_ACK=trusted-local-fixtures LAB_ROAD=k3s \
  just lab --account forgeci --fixtures --runners gitlab forgejo
just k3s-apply --wait sigstore
just k3s-ready --wait gitlab forgejo
LAB_ROAD=k3s just targets --account forgeci gitlab forgejo
```

The final target mint probes Fulcio and records its issuer mappings only when
reachable. Inspect with `just targets-explain --account forgeci`; if `fulcio` is null,
diagnose the selected road's Fulcio deployment and mint again once it is ready.
The `ready` commands above check providers, not Fulcio. Full needs GitLab
and Forgejo, both with `workflow-runs`, fixture manifests, OCI registry facts
and their Fulcio issuer mappings. Bare `just lab` prepares none of those
optional components and does not select account `forgeci`.

Back in the forge-ci checkout, provide the pinned Go toolchain and the
launcher's host tools: `base64`, `cosign`, `flock`, `jq`, `syft`, `buildah`,
`git` and `sha256sum`. Buildah must work for the current user. The suite builds
host and Linux/amd64 runner binaries, writes caches/evidence, runs containers
and trusted runner jobs, may fetch modules/images, and deletes/recreates
`fci-` resources on the lab.

```text
just test-live                            # the complete profile
just test-live 'TestRegistry_.*'          # an explicitly partial run
```

The launcher compiles each package's test binary, then runs them serially
under the account lock. The live suite handles `SIGINT` and `SIGTERM` by
canceling work through `livetest.Context(t)`, then allowing the existing
`t.Cleanup` callbacks a separate, shared two-minute grace period. Archive
reads and registry uploads follow cancellation too. Cleanup is registered
before scratch creation, so a canceled creation can still be reclaimed.
The launcher drains the redacted log before releasing its lock and returns
130 for interruption. When supervising the launcher directly with systemd,
allow at least `TimeoutStopSec=180` so it does not kill this cleanup early.
When using Forge Lab's full-release wrapper, run `just test-live-preflight`
first: the wrapper swaps the privileged runner in before the launcher's own
preflight runs, so a stale contract refused inside it costs two runner swaps
instead of seconds. The wrapper's child shutdown grace must
cover the suite's two-minute cleanup. Use the current three-minute default
and allow `TimeoutStopSec=1200` for child termination and the wrapper's
separate ten-minute normal-profile restoration budget.
Inspect cleanup errors before restarting; forced termination cannot complete
cleanup, and an interrupted run is never acceptance evidence. The next run
uses the same guarded delete-before-create lifecycle for retained fixtures.

For the focused active-job interruption check, set
`FORGE_CI_LIVE_INTERRUPT_PROBE=gitlab-workflow` and run
`just test-live '^TestGitLabWorkflow_InterruptedJob$'` under a named supervisor
with `TimeoutStopSec=180`. The normal GitLab runner is sufficient. Observe
`fci-live-interruption-ready` in the scratch job's trace, then send SIGTERM to
that launcher. The expected result is exit130, a completed redacted log and
scratch cleanup before the grace expires, followed by absence of the job pod
and scratch project. The fixture fails if allowed to finish normally. Revoke
the fresh credential batch afterward; do not report interruption as a release
or whole-suite pass.

Fixtures and runners belong to the road, not to an account, so they are
prepared once per road; every account on that road (this suite's `forgeci`, and the
other suites' own) then only mints its contract. A mint replaces, and
revokes, the generation already at its destination, and a contract names one
road's hosts: to hold one per road, set `LAB_TARGETS_FILE` to a separate
absolute path per road, both when minting and when running.

`LAB_TARGETS_FILE` is optional and overrides the path. No typed confirmation is
needed on lab hosts. The kit's transports and proxies enforce the host
allowlist; repository lifecycle helpers enforce the credential-owner and `fci-`
prefix checks. Direct SDK and native CLI operations need scenario-owned resource
scoping; see [the contract boundaries](#what-the-contract-is-and-what-it-is-not).
Before anything is built the preflight prints what the run will touch
(endpoints, capabilities and credential lifetime) and the run log keeps it;
`forge-labctl targets explain` shows the same for a contract on disk. A missing
contract is refused with where the suite looked; after the preparation above,
`just targets --account forgeci gitlab forgejo` in Forge Lab mints one (with
`LAB_ROAD=k3s` for that road).

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
binary payload bound to the pipeline and commit. It establishes transport,
not a complete consumer release.

Mint a fresh generation when the suite refuses a stale credential (`just
targets --account forgeci all` again). Nothing here revokes a credential: the
producer that minted it does, when the next generation is minted, and the
suite refuses a non-expiring credential older than a day and an expiring one
that is not between five minutes and 31 days from expiry.

The script builds the product and the runner-side helpers outside the tests
and hands them over by path, because a suite that compiles its own binary
proves something about the source it happened to see, not about the artifact
the release flow produces. It keeps a credential-redacted log of each run
under `${XDG_STATE_HOME:-$HOME/.local/state}/forge-ci/live-runs/<run>` for
30 days (`FORGE_CI_LIVE_EVIDENCE_ROOT` moves it) and prints the path at the end.
The tier is human-invoked and has no owning workflow, so there is no CI
artifact to retrieve instead.

One live run per lab account runs at a time. Scenarios delete and recreate
fixed `fci-<scenario>` repositories, so two concurrent runs would clobber each
other; the script takes a non-blocking `flock` on
`${XDG_STATE_HOME:-$HOME/.local/state}/forge-ci/live.lock` and a second run
exits with status 2. The lock does not follow `FORGE_CI_LIVE_EVIDENCE_ROOT`, and
`go test` runs without the descriptor, so a lingering gpg-agent or buildah
child cannot hold it. It covers runs that share one `$HOME`: a run from a
container with its own `HOME` does not see it.

#### What the contract is, and what it is not

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
fixtures under `internal/livetest/testdata/`; ordinary untagged tests project
them onto targets, exercise the guard and test the preflight without reaching a
provider.

#### Scenarios are repeatable by construction

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
in a fixture under `internal/livetest/conformance/testdata/layer`. A job
runs the binary that commit's checksum names, downloaded as the layer
delivers it: `just test-live` builds it with `scripts/ci/forgejo-binary.sh`,
and the scenario uploads it to the scratch owner's generic packages and
deletes it afterwards. A tree whose checksum is not that build is refused
before anything is pushed. Uncommitted changes under `.forgejo/` are refused; the
rest of the tree may be dirty. They wait on the workflow run, for up to 20
minutes, and a failure prints every job with its status and the end of the log
of each that did not succeed. The release scenario needs a signer image:

```text
export FORGE_CI_LIVE_SIGNER_IMAGE="$(just test-live-signer-image /path/to/forge-lab)"
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

Its release request is the one signed request of the tier: an annotated tag
signed with a throwaway SSH key and pushed with git, a
`.forgejo/release-allowed-signers` in the consumer that names the key, and
`verify-release-request-signature: 'true'` in the fixture. The minimal
release keeps the default, an unsigned tag created through the forge's API.

Whether a job may build an image is the runner's to allow, and the contract
does not say, so the operator does. Nothing set means it may, and a build
that fails is then a failure: the fixture's first job, `Check that a job can
build an image`, is the layer's `setup-buildah` alone, so the failure names
the runner and not the release. `FORGE_CI_LIVE_IMAGE_BUILDS=0` says that this
runner allows none, and the scenario skips with that reason. On Forge Lab's
Compose road the ordinary runner builds the image. On its k3s road the
ordinary runner does not: run the tier there with `FORGE_CI_LIVE_IMAGE_BUILDS=0`,
and this scenario inside the privileged session, without the variable:

```text
K3S_PRIVILEGED_DIND_ACK=trusted-local-fixtures \
K3S_FORGEJO_FULL_RELEASE_ACK=trusted-local-full-release \
  just k3s-full-release -- /usr/bin/env \
    LAB_ROAD=k3s LAB_TARGETS_FILE=/absolute/fci-k3s.json FORGE_CI_LIVE_SIGNER_IMAGE=<reference> \
    /absolute/just --justfile /absolute/forge-ci/justfile \
    --working-directory /absolute/forge-ci \
    test-live 'TestForgejoLayer_RegistrySignerSmoke$'
```

The first line is Forge Lab's, run in its checkout; its how-to says what the
session permits and that it restores the ordinary runner itself.

#### Trying a consumer

`just test-live-consumer` answers one question before an engine commit is
published anywhere: does this repository still pass its own workflow with the
layer of this checkout?

```text
just test-live-consumer <consumer-checkout> <pull-request|release> [setting ...]

just test-live-consumer ~/src/gommitlint pull-request CI_JOB_IMAGE=<digest reference>
just test-live-consumer ~/src/gommitlint release
```

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
require an anonymous repository API read to be refused. Helper, image and
policy fixtures keep their separately defined visibility.

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
The Forgejo layer currently fetches its helper source and engine package without
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
FORGE_CI_LIVE_CONSUMER_VISIBILITY=private just test-live-consumer /absolute/gommitlint pull-request CI_JOB_IMAGE=<digest reference>
FORGE_CI_LIVE_CONSUMER_VISIBILITY=private just test-live-consumer /absolute/gommitlint release
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

- `CI_RUNNER_LABEL` and `FORGE_CI_REPOSITORY`, the layer's own two
  variables.
- For a release, a throwaway Cosign key pair, SSH bot key, registered as the
  repository's deploy key, and GPG key. Each reaches the consumer under the
  name the consumer reads it by: where a workflow passes
  `cosign-public-key: ${{ vars.NAME }}` or
  `RELEASE_TOKEN: ${{ secrets.NAME }}`, the trial sets `NAME`. The two tokens
  are the lab account's. A fact passed in another form, a key written into
  the workflow or an expression with a fallback, is refused with the line.
- The caller's settings, each `key=value`:

  | Setting | Meaning |
  |---|---|
  | `<VARIABLE>=<value>` | A repository variable, name in capitals. `@layer-commit` and `@request-commit` as the value stand for the commit of the layer under test and for the commit a release requests, which no caller can know beforehand. |
  | `token=<SECRET>` | A repository secret that receives the lab account's token, for a token the consumer reads outside a call of the layer. |
  | `workflow=<file>` | The workflow to judge. `pullrequest.yml` or `release.yml` when left out. |
  | `version=<X.Y.Z>` | The version a release requests. `0.0.1` when left out. |
  | `image=<registry>/<repository>[:<tag>]` | Release only: an image on an external registry that must verify against the trial's Cosign key after the release: the repository's `v<version>` tag, or the tag written, such as a flavour's `v0.0.1-go`. Needs the external registry's token below. |
  | `interrupt-after=<job name>` | Release only: cancel the run once a job whose name starts with this has succeeded, as a release interrupted after that step would be, for example `interrupt-after=Build go flavor image`. The trial then checks that the run ended cancelled and that no release was published. Request the same version again to see what the rerun does with what the cancelled run left: it must replace the staging images and succeed. |

  No setting carries a secret value, and a name the trial sets itself is
  refused.

  **An external image registry** is the one credential from outside the lab a
  trial may carry: for a consumer that publishes its images to a registry such
  as GHCR while its release stays on the lab's Forgejo. Point
  `FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE` at a file only you can read (mode
  0600) holding the token, and set `FORGE_CI_LIVE_EXTERNAL_REGISTRY_USER` to the user
  it logs in as. The token becomes the value of the consumer's secret behind
  the layer's `RELEASE_REGISTRY_TOKEN`; it is never a setting, never logged, and
  the run log records only the file's path and the user. The repository the
  consumer publishes to is its own variable, given as an ordinary setting:

  **A reused signing key.** Each release trial signs with a key of its own,
  so a second release cannot verify the bases the first one signed. To try
  what an organization's second release does, set `FORGE_CI_LIVE_SIGNING_KEY_DIR`
  to a directory only you can read: the first trial makes the key pair and its
  passphrase there, and later trials sign with the same key.

  ```text
  FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE=$HOME/.config/forge-lab/hanklankorg-packages.pat \
  FORGE_CI_LIVE_EXTERNAL_REGISTRY_USER=<github user> \
    just test-live-consumer /absolute/nanolinter-clone release RELEASE_PLATFORMS=linux/amd64 \
      IMAGE_REPOSITORY=ghcr.io/hanklankorg/<unique name> \
      image=ghcr.io/hanklankorg/<unique name>:v0.0.1-go
  ```

  The lab's job containers must reach that registry. Packages a token creates
  on GHCR are private until made public in each package's settings; the
  trial verifies with the token, and the base check, which runs anonymously,
  rebuilds instead of reusing private bases.

  **A release of chosen images only.** Nanolinter's release builds the
  flavours and standard images its request commit selects, so a lean trial is
  a throwaway clone with one commit that selects them: here the Go flavour and
  no standard image, on amd64 by the setting above.

  ```text
  git clone /absolute/nanolinter /absolute/nanolinter-clone
  printf 'go\n' > /absolute/nanolinter-clone/packaging/container/release-flavors.list
  : > /absolute/nanolinter-clone/packaging/container/release-standard-images.list
  git -C /absolute/nanolinter-clone add packaging/container
  git -C /absolute/nanolinter-clone commit -s -m 'test: select the Go flavor for the lab release'
  ```

  **The release sequence** the 2026-10-15 GHCR simulation ran, each step a
  trial with the same key directory: a cold release (`version=0.0.1`, every
  base built), a warm one (`version=0.0.2`, bases reused), an interrupted one
  (`version=0.0.3 interrupt-after=Build go flavor image`), and the same
  version again (`version=0.0.3`), which must succeed over what the
  interrupted run left. Each trial logs the release's size; the registry side
  is read with `forge-ci container package-retention --repository <image>`,
  whose dry run lists every version and why it stays.

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
release, the annotated tag `release-request/v<version>` at the head of
`main`. Actions are off in the repository until everything is in place, so a
workflow the consumer starts on a push to `main` does not run.

**The verdict** is generic. The run of the named workflow succeeded; every
job is printed with its status, and the end of the log of each that failed.
For a release the host also checks that `main` advanced by one commit signed
by the bot's key, that the final tag is signed and points at it, and that the
release, draft or published as the consumer asked, offers at least one Cosign
bundle and each verifies against the trial's public key. Job names and asset
counts are the consumer's and are not judged. The wait is up to 75 minutes.

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
  images needs a runner that lets a job build one, as the smoke scenario
  above does. Forge Lab's Compose runner built Gommitlint's release images;
  on its k3s road the trial runs inside the privileged session, wrapped the
  same way.

In order, for a release on a prepared road, from this checkout unless said
otherwise:

```text
# 1. The signer image of this commit; keep the reference it prints.
export FORGE_CI_LIVE_SIGNER_IMAGE="$(just test-live-signer-image /absolute/forge-lab)"

# 2. In Forge Lab: the consumer's job image; pass what it prints as CI_JOB_IMAGE=.
just publish-image --account forgeci <package> <local image>

# 3. The trial. Gommitlint, then Nanolinter from a clone with its lab selection.
just test-live-consumer /absolute/gommitlint release
just test-live-consumer /absolute/nanolinter-clone release RELEASE_PLATFORMS=linux/amd64
```

The layer is served from `HEAD`. After a change to Go source, `go.mod` or
`go.sum`, run `just refresh-forgejo-binary` and commit before a trial; it
refuses a checksum that is not this tree's build and a `.forgejo/` that differs
from `HEAD`.

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

In this repository's own CI, the pull-request workflow runs:

- `just test` (unit + integration, default profile)
- `just test-smoke`
- `just test-fuzz` (seed corpus only; not active mutation fuzzing)

The release workflow runs `just test-integration-strict` instead, in a job the
release depends on. The two gates differ on purpose. A contributor without
buildah or trivy should still get a run that tells them something, so the
pull-request tier skips what their machine cannot prove and tallies it. A
release may not: a runner that lost a tool would otherwise publish a release
whose gate proved less than the last one did, with nothing in the log saying
so. `TestReleaseGateRunsTheStrictIntegrationTier` pins both halves, including
that the pull-request gate stays permissive.

Only the GitHub definition runs this repository's own tests: `.gitlab-ci.yml`
and the workflows under `.forgejo/` do not run them.

#### Showing a consumer

A trial answers a question and leaves nothing. A demo shows a consumer's
release and pull request to people and keeps them, until a person deletes it.
The demos ship in `test/live/demos/<name>`: a small consumer for each
ecosystem or feature worth showing, rendered for all three forges by `just
test-unit` so none drifts out of what the live tier accepts.
[test/live/demos/README.md](../test/live/demos/README.md) says how one is
laid out. `checkout=/absolute/path` shows any other consumer instead.

Each forge runs the demo's jobs in images the lab must have. For the
`hello-maven` demo:

```text
# The Java lint image, built in a Nanolinter checkout and published to the lab
# in Forge Lab; it prints the reference to pass as CI_JOB_IMAGE on Forgejo.
NANOLINTER_CONTAINER_FLAVOR=java just build-image-local          # in Nanolinter
just publish-image --account demo nanolinter-java localhost/nanolinter:latest  # in Forge Lab
podman save --format docker-archive -o /absolute/images/lint.tar localhost/nanolinter:latest

# The GitLab job image, from the demo's job.Containerfile.
just demo-image hello-maven /absolute/images                     # writes hello-maven-job.tar

# The signer image of this commit (Forgejo takes the reference it prints,
# GitLab an archive of it).
export FORGE_CI_LIVE_SIGNER_IMAGE=$(just test-live-signer-image /absolute/forge-lab)
podman save --format docker-archive -o /absolute/images/signer.tar localhost/forge-ci-live-signer:$(git rev-parse HEAD)
```

The whole lifecycle is then four commands:

```text
# 1. In Forge Lab: the demo account and its contract, with the runners a demo's
#    workflows run on (GitHub needs none).
just lab --account demo --runners gitlab forgejo

# 2. Here: make a demo, the release, then a pull or merge request on the released main.
just demo forgejo hello-maven version=0.2.0 CI_JOB_IMAGE=<lint image reference>
just demo gitlab hello-maven version=0.2.0 version-bump=maven \
  job-archive=/absolute/images/hello-maven-job.tar build-archive=/absolute/images/hello-maven-job.tar \
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

On GitHub, `demo-list` and `demo-delete` run inside the same hosted session as
`demo`, with `org=<id>`. Every demo repository is named `demo-<name>` on every
forge; trials never use the name.

|  | Trial | Kept for diagnosis | Demo |
|---|---|---|---|
| Recipe | `test-live-consumer` | the same, with `FORGE_CI_LIVE_KEEP_SCRATCH=1` | `demo <forge>`; `demo-list`, `demo-delete` |
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

#### Trying a consumer on GitHub

The GitHub layer has no local lab, so its consumer trial runs in a disposable
hosted organization that Forge Lab's hosted session authorizes. Inside that
session (`just github run --org <login> --prefix <prefix> --fixtures none` in
Forge Lab; the trial creates its own repositories, so the session prepares
none), which writes the private context `FORGE_LAB_GITHUB_CONTEXT` names:

```text
just test-live-github-consumer <org-id> /absolute/gommitlint pull-request
just test-live-github-consumer <org-id> /absolute/gommitlint release version=0.1.0 image=gommitlint
```

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
   `fixtures=<dir>`). Rendering pins every `diggsweden/forge-ci` reusable
   workflow to the engine at its commit, `forge-ci-binary-ref` to
   `v0.90.0-pre`, and `ghcr.io/diggsweden/forge-ci-runtime*` images to the
   organization's `sha-<commit>` tag; a workflow that still calls another engine
   is refused. Only an image a fixture names can be rewritten, so a fixture
   that calls an orchestrator passes `runtime-image` and the image of each
   project type it builds, such as `runtime-image-java`: the orchestrator's
   defaults name released images, which an unreleased engine has not built, and
   a call without `runtime-image` is refused before anything is pushed. `nanolinter-image=` and `megalinter-image=` replace a value
   wherever a fixture workflow passes it, for a mirror. The value must be
   pinned by a full `@sha256:` digest, and a setting no workflow passes is
   refused.
3. **The verdict.** A pull request: a change branch and a pull request, and the
   `pullrequest.yml` run (or `workflow=`) must succeed. The change is the one
   the local trials make: one added file, a Conventional subject, a sign-off
   and an SSH signature by a key made for it, because the consumer lints the
   commits it reviews. A release: an ephemeral
   passphrase-protected GPG key becomes `RELEASE_GPG_PRIVATE_KEY`,
   `RELEASE_GPG_PASSPHRASE` and `RELEASE_GPG_PUBLIC_KEY`, the context token
   `RELEASE_TOKEN`, and a signed annotated `release-request/vX.Y.Z` tag starts
   `release.yml`. After it succeeds, `main` must be one signed commit ahead, the
   final tag and a release with assets must exist, and every `*.bundle` asset
   must verify with `cosign verify-blob` against the engine's keyless identity.
   A fixture whose `release.yml` pins `cosign-public-key-sha256` signs with a
   Cosign key instead: the trial makes a passphrase-protected key pair, sets
   `RELEASE_COSIGN_PRIVATE_KEY`, `RELEASE_COSIGN_PASSPHRASE` and the
   `COSIGN_PUBLIC_KEY` variable, writes the key's digest into the workflow,
   and verifies against the public key. With `image=<name>`, `ghcr.io/<org>/<repo>/<name>` at the version, with or
   without its `v`, must verify with `cosign verify` too; for a private
   consumer that check is logged as not run, since the package needs registry
   credentials.
4. **Cleanup.** The consumer repository is deleted by its ID. Its GHCR
   packages stay: a fine-grained token cannot reach GitHub's packages API, so
   they are left for the organization's sweep, such as Forge Tidy's. The
   session in the repository's name keeps the next trial clear of them: a
   package outlives its repository, and GitHub denies a new repository of the
   same name a push to it.

Each run prints the jobs of the judged run and, for each failed job, the
lines of its log up to the first error, not its cleanup steps.

What the caller brings:

- **A token that can do it.** The context's token creates and administers
  repositories, writes contents, workflows, Actions variables and secrets, and
  opens pull requests.
- **Public engine images.** A consumer's job pulls the engine's runtime images
  from GHCR without credentials, and the API cannot change a package's
  visibility, so after the engine's first build the owner makes its
  `forge-ci-runtime*` packages public once in the organization's settings.
- **`cosign`, `gpg` and `git` on `PATH`.** The release checks verify locally.
- **A committed checkout.** Both checkouts are read at `HEAD`, never the
  working tree. The engine's `main` in the organization is overwritten.

#### GitLab live acceptance

How the [GitLab layer](gitlab.md)'s components are accepted on a real
instance.

##### Live verification

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

##### Trying a consumer MR gate

```text
just test-live-gitlab-consumer /absolute/clean-consumer /absolute/job-image.tar
```

This command and `test-live-gitlab-consumer-release` run both public and private
consumer cases by default. `FORGE_CI_LIVE_CONSUMER_VISIBILITY=public|private` narrows a
development rerun; unset or `both` runs the pair. Each repository is created with
the selected visibility, verified before
source publication, and private trials must refuse anonymous repository reads.

The consumer must be a clean committed checkout with its own `.gitlab-ci.yml`.
The focused trial accepts one full-SHA-pinned `check` component and, beside
it, at most one `nanolinter` component, each matching `forge-ci-repository` and
leaving its image to the trial: `job-image: $CI_JOB_IMAGE` and
`nanolinter-image: $CI_LINT_IMAGE`. A gate with the nanolinter component needs
`FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE`, a nanolinter flavour image archive. The
trial rewrites those coordinates to the lab's engine commit and served image
digests, preserving the consumer's commands and other files.
Deployment outside the trial requires the published image digest, as in
[Source-check component](gitlab.md#source-check-component).

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

##### Base images fixture

The focused `just test-live-gitlab-base-images <check-archive> <build-archive>
<signer-archive>` fixture runs two releases under the guarded GitLab full-release
profile. It publishes one tiny amd64 base, independently verifies its signature
and attestations, then requires verified warm reuse with the same final digest.

##### Consumer release acceptance

For a clean committed consumer with `.gitlab/check-release.sh` and
`.gitlab/build-release.sh`, use:

```sh
just test-live-gitlab-consumer-release /absolute/clean-consumer \
  /absolute/check-image.tar /absolute/build-image.tar /absolute/signer-image.tar
```

Optional trailing `KEY=value` arguments set literal environment variables only
for the key-free consumer build command. Their meaning belongs
to the consumer; the harness imposes no architecture default. For example, a
consumer that supports platform selection can be tried with
`RELEASE_PLATFORMS=linux/amd64`. Duplicate or malformed names are refused;
values are shell-quoted as data. These settings are recorded in the live log
and do not enter the independent preparation, signing, or publication commands.

Which runner a build needs depends on the images it builds. On Forge Lab's
Compose road the ordinary, unprivileged GitLab runner builds images for the
host's architecture in a job (observed for an amd64 release). It cannot run a
`RUN` step of another architecture: emulation registered on the host does not
reach a build nested in a job container, and the step ends with `exec format
error`. A release that builds arm64 images on an amd64 host therefore runs
inside Forge Lab's documented GitLab full-release wrapper on the k3s road. A
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

Before a full consumer build, validate a prepared builder with the focused
`TestGitLabRelease_BuildImageCapability` scenario. Set
`FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE` to its absolute archive path and run
`just test-live '^TestGitLabRelease_BuildImageCapability$'` inside the same
guarded full-release wrapper. This exercises storage setup with package
installation disabled and a real `FROM scratch` / `RUN` build in the runner;
it does not publish the built probe image.

##### 0.90.0 acceptance goals

Before enabling a release policy at new publication coordinates:

- Verify the recorded binary can be published and read back anonymously.
- Exercise public/private checkout and required MR gates with native job tokens.
- Verify policy isolation, variable scopes and publication-registry restrictions.
- Run signed Git and artifact/image verification for the chosen consumer shape.
- Exercise cold/warm base reuse, release contention, promotion retry and active
  job cancellation where those paths are enabled.
- Confirm scratch cleanup, normal runner restoration and credential revocation.

Use the exact revision, binary digest and selected runner profile in the retained
evidence. A focused scenario proves its selected path, not every provider or
consumer configuration.

#### What the live tier actually covers

`providers.md` says what each forge *supports*. This says what has been
*observed* against a real one, a different axis, and the one that decides how
much a green suite is worth.

The full matrix covers GitLab and Forgejo on both supported roads:

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
| `LAYER-1..5` | the Forgejo delivery layer on a real runner, Forgejo only: an artifact handed between two jobs that set the layer's binary up from a pinned commit; `prepare-release.yml` stopping on a missing secret before it fetches or pushes anything; a consumer pull request built, tested and passed through `check-l3-isolation` with nothing published; a release request taken to a signed, published release whose commit, tag, checksums, provenance and Cosign bundles are verified from the host; an image built in a job, pushed, recorded in the ledger and signed, and the same image signed and verified as a base, both verified from the host with `cosign verify` |

For release acceptance, retain the exact source/binary identities, target
generation, selected scenarios, result and cleanup outcome. Evidence from one
road or provider does not establish another road or provider.

**The standing gap: GitHub is not in the lab.** There is no GitHub target, so
every scenario above is two-forge. GitHub's *exclusive* positive paths, SARIF
upload and the attestation API, are verified only against fakes written from
the same assumptions as the adapter, which is precisely the tie this tier exists
to break. It is the largest asymmetry in the suite and it does not close without
a real GitHub organisation.

Keyless verification needs the selected issuer's trust root: Cosign cannot
infer a private CA root from the certificate it has just received. Supply that
root explicitly and verify the configured issuer and signing identity.

## Tests live next to source

```text
internal/domain/tagrule/apply.go
internal/domain/tagrule/apply_test.go         ← unit tests
internal/domain/tagrule/parse_test.go         ← unit + property + fuzz
internal/adapters/github/provider.go
internal/adapters/github/provider_test.go      ← real provider, in-memory HTTP transport
internal/adapters/github/release_integration_test.go   ← //go:build integration
internal/app/container/compute_metadata.go
internal/app/container/compute_metadata_test.go       ← fake ports
cmd/forge-ci/smoke_test.go                 ← //go:build smoke
```

### Except the repo-wide guards

A guard that walks the whole tree is not a test of the package it happens to
sit in. It lives in one of the packages below, named for what it is
answerable for. This table is the list; the package docs do not repeat it.

`TestGuardPackagesAreDocumented` in `test/syncguard` fails when a
`test/*guard` package is missing a row here, so adding a package without
saying what it guards does not compile past CI.

| Package | Guards | Reads |
|---|---|---|
| `test/archguard` | where code may live: which layer may read the environment, mint a credential, or branch on the platform (import direction, ADR 0004, is the depguard `layer-*` rules in `.golangci.yml`) | Go source under `internal/` |
| `test/forgejoguard` | the Forgejo layer's signing boundary and consumer contract: secrets per job, signer bootstrap order, coordinate preflight, routing, strict YAML, no organization named, workflows that start only when called, actions that call no sibling by path and take no credential | `.forgejo/`, `containers/signer/`, the Forgejo examples under `examples/` |
| `test/lexiconguard` | a spelling, regex, or pattern literal is declared once | every file in the repo |
| `test/syncguard` | a generated file still matches the Go it is generated from | `docs/`, `.forge-ci/` |
| `test/workflowguard` | the workflow contract adopters code against | `.github/workflows/`, `examples/` |

The rule: a guard that reads files outside its own package goes in one of
these; a test of `internal/cli` stays in `internal/cli`.

`internal/testutil/reporoot` resolves the repository root for all of them, so
none of them carries its own copy of that logic.

Two habits keep a guard useful:

- **Explain the fix, not the rule.** The failure message should say what to do
  next; the contributor can already see what they did.
- **Poison it once.** A guard that has never been seen to fail is a guard that
  may not work. Several carry a companion test that feeds them a violation on
  purpose (`TestJobLevelPlanGuardCatchesPoison`,
  `TestRuntimeImageTagGuardCatchesDivergence`).

Repository-wide guards live under `test/`; Go behavior tests stay next to
the code they exercise. The default `go test ./...` gate discovers both.
Shared helpers stay in `internal/testutil` so colocated tests can import them.
The legacy Bats harness is retired; the remaining shell-native bootstrap
surface is tested under `scripts/...` with Go tests.

## Black-box by default, `_internal_test.go` for the rest

New test files use `package foo_test`, a black-box test from outside
the package. This catches accidental coupling to private API, makes the
test file act as runnable documentation of the public contract, and is
what the rest of the codebase expects.

Carve-out: when a test legitimately needs to touch an unexported helper,
state machine, or sentinel, use `package foo` (same package as the
production code) and put it in a file named `<thing>_internal_test.go`.
The suffix makes the intent visible to reviewers without having to open
the file.

```text
internal/domain/parser/parse.go
internal/domain/parser/parse_test.go              ← package parser_test
internal/domain/parser/parse_internal_test.go     ← package parser
```

Keep the carve-out narrow: only the cases that *cannot* be tested from
outside. If you find yourself reaching for the carve-out, ask first
whether the missing access is a hint that the API needs widening
(elevate to exported) or a hint that the unit under test is too
coarse-grained (split it). The carve-out is the third option, not the
first.

## Test naming conventions

Outer test names follow `Test<Component>_<Method>_<Scenario>`:

```go
func TestApply_RawValue_EmitsLiteral(t *testing.T) { ... }
func TestApply_RefBranch_DoesNotFireOnTag(t *testing.T) { ... }
```

The guard packages under `test/` name a test as one sentence, with no
underscore (`TestTrackedEngineBinaryIsABuildOfThisTree`), because a guard
states a rule about the tree and has no component and method to name.

Subtest names are plain words that say the case. snake_case is not required:

```go
t.Run("raw value emits literal", func(t *testing.T) { ... })
```

### What a test file's suffix says

A suffix before `_test.go` says what kind of test the file holds:

| Suffix | The file holds |
|---|---|
| `_internal` | in-package tests (`package foo`), see above |
| `_integration` | tests behind `//go:build integration`, which the default tier does not run |
| `_boundary` | the edges of one reader, parser or stream: size limits, links, grammar corners |
| `_preflight` | refusals that must come before any effect: no file written, no tool started |
| `_contract` | the whole observable result of one operation, as its callers rely on it |
| `_controls` | the refused and the accepted inputs of one validator, side by side |
| `_duplicates` | a config plan with a duplicate member is refused before the consumer acts |
| `_root` | one command driven through the root command: flags, environment, status |
| `_guard` | under `test/`, a walk of the tree; inside a package, see below |

The tree does not hold to this everywhere yet. Sixteen files with the
`integration` build tag are not named `_integration`, so their name does not
show that the default tier skips them. Eighteen in-package test files are not
named `_internal`. Inside `internal/`, `_guard` names a walk of the command
tree or the source in some files (`internal/cli/cienv_guard_test.go`) and an
ordinary refusal test in others
(`internal/adapters/forgejo/authority_guard_test.go`). Name a new file by the
table; rename an old one when you change it.

## Table-driven everything

Each test is a slice of cases. The loop variable is `tc`. The struct fields
are `name` / `given` / `want` / `wantErr` / `errContains`:

```go
func TestApply_TagRules(t *testing.T) {
    t.Parallel()
    tests := []struct {
        name  string
        given tagrule.Rule
        evt   provider.EventContext
        want  string
        fired bool
    }{
        {name: "raw value emits literal", given: ..., want: "main", fired: true},
        {name: "ref branch does not fire on tag", given: ..., fired: false},
        // ...
    }
    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()
            got, fired, err := tagrule.Apply(tc.given, &tc.evt)
            require.NoError(t, err)
            require.Equal(t, tc.fired, fired)
            if tc.fired {
                require.Equal(t, tc.want, got)
            }
        })
    }
}
```

Assert with `testify/require` or with the standard library (`t.Fatalf`,
`t.Errorf`), one style in a file: follow the file you are in.
`testify/assert` goes on after a failed check, so the first failure is buried
under the ones it causes. Use it only for the independent observations of one
refusal (nothing written, nothing called, nothing printed), where each should
be reported even when another fails; six preflight files do. `t.Helper()` on
every test helper.

A test takes its context from `t.Context()`. `context.Background()` is for
`TestMain` and for cleanup, which runs after the test's context is cancelled.
Live operations use `livetest.Context(t)` so process interruption also cancels
their work; live resource cleanup uses the kit's separate cleanup context.
Many older files still call `context.Background()` in the test body; change
them to `t.Context()` when you touch them.

Failure output reads like spec lines: `--- FAIL: TestApply_TagRules/raw_value_emits_literal`.

## Fuzz tests on parsers

A parser gets a `Fuzz<Name>` target seeded with known good and known bad
inputs. The target asserts what must hold for every input the parser accepts,
not only that it does not panic:

```go
func FuzzParseRules(f *testing.F) {
    f.Add("type=raw,value=stable")
    f.Add("type=banana,enable=true")
    f.Fuzz(func(t *testing.T, input string) {
        rules, err := container.ParseRules(input)
        if err != nil {
            return
        }

        for i, rule := range rules {
            if rule.Priority() <= 0 {
                t.Fatalf("rules[%d] has non-positive priority %d", i, rule.Priority())
            }
        }
    })
}
```

The repository's own CI runs the fuzz seed corpus via `just test-fuzz`.
Active mutation fuzzing remains a local or scheduled task (`go test -fuzz=Fuzz...
-fuzztime=30s`) per parser. Crashes get committed under
`testdata/fuzz/Fuzz<Name>/<seed>` as regression cases.

## Golden files

Use `internal/testutil/golden` for non-trivial outputs:

```go
got := metadata.AssembleLabels(...)
golden.EqualString(t, "labels.txt", got)
```

The fixture lives at `testdata/golden/labels.txt`. For an intentional change,
select the package that owns the golden:

```text
go test ./path/to/package -args -update
```

Replace `./path/to/package` with that package's directory. `-update` is a custom
test-binary flag, so the package selector must precede it. Packages that do not
use the golden helper do not register the flag; do not send it to an all-package
sweep. CI runs without `-update` and fails on differences. Review every golden
change before committing it.

## testutil packages

All test helpers live under `internal/testutil/`. Each package is small,
returns concrete types, and registers `t.Cleanup` itself, so tests never
call `defer cleanup()`.

| Package | Purpose | Replaces |
|---|---|---|
| `evidence` | Tool lookup that tallies what a run could not prove, plus the strict profile | bare `t.Skip` on a missing tool |
| `golden` | Golden-file equality with `-update` flag | legacy shell partial-output assertions |
| `mockbinary` | Shell stubs that record argv/stdin/cwd; hand the code `Path(name)`, or call `OnPath()` (serial) when it looks a bare name up | `create_mock_binary` |
| `isolatedgit` | Throwaway repo with repo-local identity, optional bare remote; `Main` isolates the test process from TestMain | `common_setup_with_isolated_git` + `init_remote_repo` |
| `testenv` | Canonical isolated env wrapper for env-sensitive tests | repeated `t.Setenv` + ad-hoc HOME/XDG isolation |
| `testfs` | Thin real-FS and memory-FS helper for file-heavy tests | repeated `t.TempDir` + `os.WriteFile` scaffolding |
| `gpgkey` | Throwaway ed25519 GPG key in isolated GNUPGHOME | per-test setup blocks |
| `ghaenv` | Tempfile-backed `GITHUB_OUTPUT`/`STEP_SUMMARY` with heredoc reader | `common_setup_with_github_env` + `get_github_output_multiline` |
| `glabenv` | GitLab-CI env (`CI_*`) + dotenv-style `CI_OUTPUT` reader | new GitLab test helper |
| `fakeprovider` | In-memory `provider.Provider` with call recorder | in-process provider fake |
| `fakeoutputsink` | In-memory `ci.OutputSink` capturing scalar + multiline | in-process output fake |
| `fakemanifestsink` | In-memory `ci.ManifestSink` capturing stage JSON bodies | in-process manifest fake |
| `fakegitserver` | In-memory transport (`Client()`) with route registration for the GitHub API (`New`) and the GitLab API (`NewGitLab`, preserves `%2F` in project paths); no listener | HTTP fake server |
| `fixtures` | `//go:embed`'d sample data + named getters | embedded test fixture files |

When a use case needs a new helper concern, add a package. Don't grow
existing ones across responsibilities.

For env-sensitive tests, prefer `internal/testutil/testenv` directly or a
helper that composes it (`ghaenv`, `glabenv`, `gpgkey`) so
HOME/XDG/git/GPG/proxy/locale isolation is applied consistently. A package
that builds scratch repositories with `isolatedgit.NewRepo` applies the same
isolation once for the whole process with
`func TestMain(m *testing.M) { os.Exit(isolatedgit.Main(m.Run)) }`; the
product's git inherits `os.Environ()`, so a per-helper scrub would not reach
it, and tests that touch nothing else can run in parallel.

Tests that need a real working directory should prefer
`testfs.NewReal(t).Chdir()` over open-coded `os.Chdir` / `t.Chdir` blocks.
Because cwd is process-global, those tests must not call `t.Parallel()` at
that scope.

## Patterns by layer

### Domain: pure, parallel

```go
package tagrule_test

func TestApply_SemverMajorMinor(t *testing.T) {
    t.Parallel()
    rule := tagrule.Rule{Type: tagrule.TypeSemver, Attrs: ...}
    evt := provider.EventContext{RefType: provider.RefTypeTag, RefName: "v2.5.7"}
    got, fired, err := tagrule.Apply(rule, &evt)
    require.NoError(t, err)
    require.True(t, fired)
    require.Equal(t, "2.5", got)
}
```

No `t.Setenv`, no `os.MkdirAll`, no `exec.Command`.

### Adapter: real binaries and HTTP

```go
package github_test

import (
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/diggsweden/forge-ci/internal/adapters/github"
    "github.com/diggsweden/forge-ci/internal/testutil/fakegitserver"
)

func TestRepo_FetchMetadata(t *testing.T) {
    t.Parallel()

    srv := fakegitserver.New(t)
    srv.OnGet("/repos/owner/repo", func(_ fakegitserver.Request) fakegitserver.Response {
        return fakegitserver.Response{
            Body: `{"description":"x","license":{"spdx_id":"Apache-2.0"}}`,
        }
    })
    p := &github.Provider{
        Env:             func(string) string { return "" },
        APIBaseOverride: srv.URL(),
        HTTPClient:      srv.Client(),
    }

    md, err := p.FetchRepoMetadata(t.Context(), "owner/repo")
    require.NoError(t, err)
    require.Equal(t, "Apache-2.0", md.LicenseSPDX)
    require.Len(t, srv.Requests(), 1)
}
```

Keep in-memory transport and `mockbinary` tests in the default tier. Use
`//go:build integration` for tests requiring integration prerequisites, such as
native GPG signing or container tooling. Elapsed time alone does not select a
build tag.

### Application: fake ports

A use case takes its ports as parameters, so a test passes fakes for the ones
the case under test reaches and nil for the rest.

```go
package container_test

func TestComputeMetadata(t *testing.T) {
    fp := fakeprovider.New(t).
        WithEventContext(provider.EventContext{
            RefType: provider.RefTypeTag, RefName: "v1.2.3",
        })
    fs := fakeoutputsink.New(t)

    _, err := container.ComputeMetadata(t.Context(), fp, nil, fs, nil,
        container.ComputeMetadataInput{
            ImageName: "ghcr.io/x/y",
            TagRules:  "type=semver,pattern={{version}},enable=true",
        })
    require.NoError(t, err)
    require.Equal(t, []string{"ghcr.io/x/y:1.2.3"}, fs.Multiline("tags"))
}
```

No subprocess, no network, no filesystem (except what `t.TempDir`
creates inside the helpers).

### CLI smoke: binary as black box

Within the existing `cmd/forge-ci` smoke suite, isolate the environment with
`testenv.New` and invoke the binary through `runBinary`. That helper supplies
the deadline, pipe-wait bound, dead proxies and owned working directory.
`buildBinary` builds without version ldflags, so this example expects the
development defaults:

```go
//go:build smoke

package main_test

import (
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestCLI_Version_Smoke(t *testing.T) {
    _ = testenv.New(t)
    bin := buildBinary(t)
    stdout, stderr, code := runBinary(t, bin, "--version")
    require.Equal(t, 0, code, stderr)
    require.Empty(t, stderr)
    require.Equal(t, "forge-ci version dev (commit none, built unknown)\n", stdout)
}
```

For commands expected to create files, select a private working directory with
`testfs.NewReal(t).Chdir()` before calling `runBinary`. Environment and cwd
changes keep these tests serial.

These are the fewest tests: only what package-level tests can't reach (signal handling,
CLI flag parsing edge cases, exit-code mapping).

## Coverage targets

These are the desired per-layer coverage targets. They are guidance for
reviews and local quality checks; not every threshold is hard-enforced in
CI today.

| Layer | Target | Failure action |
|---|---|---|
| `internal/domain/...` | ≥ 90% | fail PR |
| `internal/adapters/...` | ≥ 80% | fail PR |
| `internal/app/...` | ≥ 80% | fail PR |
| `internal/cli/...` | ≥ 60% (lots of urfave wiring) | warn |
| `cmd/forge-ci/...` | ≥ 50% | warn |
| **Project total** | ≥ 80% | fail PR |

Run locally:

```text
just test-coverage          # merged unit + integration profile + HTML report
go tool cover -func=bin/coverage.out
```

## Assertions that do not bite

A passing test is not evidence until you know what would make it fail. These
shapes recur, and each was found in this repository holding a real defect open.
When reviewing a test, check for them.

**A one-element fixture hides a loop.** All four image-ledger operations
(`Verify`, `Promote`, `Cleanup` and the promotion journal) were tested only with
`[]Entry{e}`. Truncating each loop to its first entry broke nothing, while a
release with several images would have gone half-verified, half-promoted and
half-cleaned. If the function takes a slice, one of its tests must pass at least
two elements, with the interesting one *not* first.

**Existence is not identity.** `sink.Single("version") != ""` passes on the
literal `"unknown"`, which is the value emitted when version resolution failed.
Assert the value.

**Inequality is not identity either.** Asserting two generated ids merely differ
accepts any scheme at all, including one that drops the shared prefix a consumer
groups by. Assert what each one is.

**Co-presence is not association.** Finding `"level": "error"` and
`"level": "note"` somewhere in a document does not show which finding got which:
swapping the mapping outright leaves both strings present. Read the field off
the object that owns it.

**A subset check suits prose, not data.** `strings.Contains` is fine on a log
line or a summary block. On argv, job outputs, published labels or a manifest it
cannot see an extra entry, a reordering, or two argv entries that should have
been one. Compare those whole.

**Message text is not the contract; the sentinel is.** Error strings may be
reworded freely. The wrapped `errs.*` value is what maps to an exit code, so
that is what a refusal test should assert. Where the refusal must also leave
nothing behind, assert that too: no output emitted, no file written, no tool
invoked.

**Poison the product to check the test.** Make the change the test claims to
catch and confirm it fails, then confirm the poison actually compiled and
applied first. A green run under a poison that never took is not evidence.

## Three rules

1. **Keep the subject under test real.** Domain tests use pure inputs; adapter
   tests may fake external HTTP transports with `fakegitserver` or executables
   with `mockbinary`; application tests pass fake ports rather than mocking
   the use case itself.
2. **Every helper takes `t *testing.T` first** and registers
   `t.Cleanup` itself. No `defer cleanup()`, no global state, no `init()`.
3. **`testdata/` is a contract.** Don't generate fixtures at test time;
   commit them. Refresh with `-update`. Reviewer approves the diff.
