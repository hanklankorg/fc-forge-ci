# Administrator check enforcement

Forge CI separates **running a check**, **reporting its findings**, and
**blocking progression**. The administrator owns four settings, all `false`
by default in the engine and every forge's public entry points:

| Setting | Findings it decides |
|---|---|
| `enforce-lint` | Lint and source-check findings |
| `enforce-vulnerabilities` | Dependency and image vulnerability findings |
| `enforce-application-tests` | Recognized application test assertion failures |
| `enforce-reproducible-builds` | Missing declared reproducible-build settings |

The default `false` permits that class of recognized findings to be
reported without failing the gate. It does not skip the check and does not turn
findings into a clean pass. Compilation, packaging, scanner startup, invalid
reports, cancellation, credentials, signatures, source binding and digest
verification are outside these exceptions.

## Who sets them, and how

The engine carries an embedded default, `internal/domain/enforcement/policy.json`,
with every setting `false`. A setting is replaced at run time, without
rebuilding the engine, by a flag on the command line that runs the gate:

```bash
forge-ci policy nanolinter --enforce-lint=false --enforce-vulnerabilities=true -- \
  nanolinter verify --require secrets,sast --report sarif --out -
```

The flags `--enforce-lint`, `--enforce-vulnerabilities`,
`--enforce-application-tests` and `--enforce-reproducible-builds` are global,
so every gate honours them. They take exactly `true` or `false`; any other
spelling, an empty value or the same flag given twice is refused. A setting
not given keeps the embedded default.

Set a setting explicitly to `true` to make its findings block. Ordinary callers
can also opt into enforcement. Organisation-wide required policy belongs in
the forge's protected/required entry point described below.

There is deliberately no second source. The flags have no environment
variable; no plan file, policy-file path, artifact, file in the checkout or
mutation command sets one; and a file named `policy.json` in a consumer
checkout has no effect. A gate whose environment has a variable named like a
setting, `ENFORCE_LINT` or any name ending in `_ENFORCE_LINT` (likewise for
the others, in any letter case), refuses to run instead of ignoring it. On
GitLab every project, group and instance variable reaches the job's
environment, so a variable set there to relax a gate stops it, visibly.

### A flag is not yet administrator policy

The engine cannot tell who wrote its command line, and neither can the
workflows, actions and components: each takes the settings as inputs, and
whoever writes the call can pass them. A consumer that calls a reusable
workflow or includes a component with `enforce-lint: false` in its own file
gets a green job. A setting is the administrator's only when it is written in
an **entry point the consumer cannot change** and that entry point's jobs are
the ones the forge requires. For organisation-mandated policy, the consumer's
own files do not supply these settings and its own jobs are not the required
gate. The examples, demos and
`forge-ci init` output set none; a guard keeps it so.

| Forge | The administrator's entry point | Where the settings are written |
|---|---|---|
| GitLab | A pipeline execution policy in inject mode (`inject_policy`), from the group's security policy project. Weaker: the project's external CI configuration path naming the policy project's file. | The policy's own YAML includes `nanolinter`, `check` and the `build-*` gates, with the `enforce-*` inputs as literals. |
| GitHub | An organization ruleset that requires a workflow from the administrator's repository. | That workflow calls the orchestrators with the `enforce-*` inputs as literals. |
| Forgejo | The consumer's `.forgejo/workflows/`, on a protected branch, with required review by the administrator's code owners. | Literals in those workflow files; `check-l3-isolation` refuses an expression. |

#### GitLab

Prefer a pipeline execution policy in inject mode where the tier allows it.
The policy's jobs run beside the project's own pipeline, are evaluated apart
from the project's configuration, and a project maintainer cannot remove the
policy. The policy's YAML includes the gate components itself, with the
settings as literals, and takes from the consumer only the commands they run,
through a script in the consumer's repository:

```yaml
include:
  - component: gitlab.example.com/team/ci/forge-ci/nanolinter@0.90.0
    inputs:
      forge-ci-repository: team/ci/forge-ci
      server-url: https://gitlab.example.com
      nanolinter-image: "ghcr.io/itiquette/nanolinter:go@sha256:PIN_DIGEST"
      enforce-lint: true
      enforce-vulnerabilities: true
  - component: gitlab.example.com/team/ci/forge-ci/check@PIN_FORGE_CI_SHA
    inputs:
      forge-ci-repository: team/ci/forge-ci
      server-url: https://gitlab.example.com
      job-image: registry.example.com/team/check-tools@sha256:PIN_DIGEST
      commands:
        - bash .gitlab/merge-request-checks.sh
      enforce-lint: true
      enforce-vulnerabilities: true
      enforce-application-tests: false
      enforce-reproducible-builds: true
```

An external CI configuration path that names the policy project's file is
weaker. The project's own `.gitlab-ci.yml` does not run, but a project
maintainer can change the path back, and a consumer file the policy includes
is merged into the same configuration: a job it defines with a gate's name is
merged under the gate's. Include the consumer's file **before** the gate
components, and keep the gates the policy's own includes, not the consumer
file's. The source-only `check`, lint and `build-*` gates explicitly replace
earlier `rules`, `needs`, `cache`, `allow_failure`, `before_script`, `script`
and `after_script` values. They run on success whenever included and explicitly
declare `needs: []` and `cache: []`. Native source jobs explicitly
request a checkout; `check` keeps its own verified checkout instead.

Select pipeline events in the administrator's `workflow: rules` or
`include: rules`, rather than relying on an earlier same-named job to select or
disable a gate. Included gates also participate in merge-request pipelines;
signing-enabled builds belong only in the policy's authorized event includes.
Any required build dependencies or caches must be declared in the
administrator-owned job configuration, not inherited from the consumer.
Mapping keys still deep-merge: unrelated job variables and
other omitted keys survive, and higher-precedence project/pipeline variables
are not sealed by these declarations. Conflicting remaining keys may also make
a merged pipeline invalid. This hardens the declared gate fields; it does not
turn include-mode merging into configuration isolation or make the external CI
configuration path immutable. Use policy injection for that authority boundary.

The release component already has this shape: the policy includes it, every
`enforce-*` input defaults to `false`, and each job assigns them to read-only values
in its script. A project, group or instance variable cannot supply or change
one.

#### GitHub

The entry point is an organization ruleset that requires a workflow from the
administrator's repository on the consumer's branches.

The selected GitHub plan must support ruleset workflows, and the source
repository's visibility/access must permit running them in the consumers.
Configure the actual workflow rule; matching a consumer-controlled status-check
name does not establish the same ownership boundary. See GitHub's
[ruleset workflow requirements](https://docs.github.com/en/enterprise-cloud@latest/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets#require-workflows-to-pass-before-merging).

That workflow calls the orchestrators with the settings written out:

```yaml
jobs:
  quality:
    uses: diggsweden/forge-ci/.github/workflows/pullrequest-orchestrator.yml@PIN_SHA
    with:
      project-type: maven
      enforce-lint: true
      enforce-vulnerabilities: true
```

The reusable workflows read the settings only from their own `inputs`, never
from `vars.*`, repository variables or the environment. A consumer's own
workflow that calls an orchestrator or a leaf with `enforce-*: false` is the
consumer's job, not the required one; required checks must name the ruleset
workflow's jobs. Workflow-dispatch inputs the consumer controls and workflow
files a pull request modifies are not on the protected path; see
[Threat model](threat-model.md).

#### Forgejo

Forgejo has no required workflows. The settings are the administrator's only
where the consumer's `.forgejo/workflows/` is on a protected branch and a
change to it needs the review of the administrator's code owners. Write each
setting out as `true` or `false` in those files; the `check-l3-isolation`
gate refuses an expression or a variable wherever a job or step passes one
to the layer. Forgejo cannot separate a consumer maintainer who may change the
branch protection or the code owners from these settings: such a maintainer
can relax a gate, as they could remove it.

An organization that records its values in a policy repository of its own
needs there: the four values per forge and road, the entry point that writes
them (the GitLab policy YAML, the GitHub ruleset workflow, the Forgejo code
owners and branch protection), a check that the consumer repositories' files
set no `enforce-*`, and the receipts it expects (`source=flag` with those
values, see below).

### Commands a job runs from its inputs

GitLab's `check` job and the release's checks and build jobs run commands
their caller lists, often a script of the consumer's repository. The job puts
a `forge-ci` first on `PATH` that carries the four settings as flags and
points `FORGE_CI_BIN` at it, so `forge-ci build maven run` and
`"$FORGE_CI_BIN" build maven run` both gate under them; given a second time,
a setting is refused. The bare binary is checksummed before the wrapper is
written.

This narrows the ways in; it is not a boundary. Code that runs in the job can
always reach the verified binary itself, or decide not to run a gate, or
ignore its status. That is why the receipt names the mechanism, `flag`, not
an owner: the settings are not proof that a gate ran under them, and the
gate's receipt in the job the forge requires is.

Changing the embedded default remains possible for an organization that
wants a different default everywhere: change `policy.json` in its
independently controlled Forge CI distribution, review it, build the engine
and runtime images, and update the protected pipeline pins and binary/image
digests together. A malformed embedded policy fails gates closed; omitted
keys default to `false`; unknown keys, duplicate keys and non-boolean values
are refused.

## Safe native release-test selections (Go/Cargo, GitHub)

Release tests remain mandatory; `skip-tests: true` is refused even when an
administrator supplies a selection or uses report-only enforcement. Do not fix
a hosted exclusion by merely deleting that flag: the legacy Cargo default is
`cargo test --locked --all-targets`, which can cross a product's hosted boundary.

The existing Go/Cargo release commands accept `--test-selection-json`. The
GitHub build leaves accept `test-selection-json`; the release orchestrator and
build stage accept `go-test-selections-json` / `cargo-test-selections-json`, JSON
objects keyed by artifact name. These inputs belong in the **administrator's
protected entry point**, under the same ownership requirements as enforcement.
They are not read from artifacts.yml, environment defaults, consumer scripts or
dispatch parameters. The engine cannot authenticate who wrote a command line.
Review the required workflow and its pins, not merely the status-check name.

Only omitted/blank whole selections retain legacy behavior for compatibility.
An explicitly supplied `{}` is refused when that ecosystem has native artifacts.
Every supplied map must contain exactly the selected build
artifact names for that ecosystem. `plan validate-test-selections` validates the
whole object in Go before any build matrix leaf can run: arrays, scalars, null,
duplicate/extra/missing keys, wrong artifact types and null/false/empty entries
are refused. Only validated, canonical maps reach the leaves. A leaf's empty
default likewise retains legacy behavior. These defaults are **not suitable for
a product with hosted exclusions**. Omission is never Nano-safe: migration to an
explicit reviewed map is mandatory before its skip flag is removed. This does
not remove the existing omitted generic legacy path.

Go selection example (the administrator must review its package coverage):

```json
{"packages":["./internal/...","./cmd/..."],"run":"TestSafe","gate":"policy"}
```

`run` is optional, but explicit null is invalid. A Go selection requires a typed
`application` or `policy` gate for its single invocation, with no implicit default
(the same classification rule as Cargo). Mixed Gommi application/policy suites
must use `policy` for the whole invocation; application report-only enforcement
cannot waive their policy failures. Selected Go tests always use `-json -count=1 -race
-buildvcs=false`; existing build tags are retained. Packages must be
workspace-relative, not flags or external paths. There is no extra-arguments or
shell-command escape hatch. A regexp that executes no tests fails closed.

Cargo has only two target kinds: `package-bin` and `package-test`. Every entry
requires a package, one exact executable name, and a typed gate: `application`
or `policy`. **Policy checks always block**, independently of application-test
enforcement. Only application assertions can become report-only findings. The
administrator owns this classification; there are no product-name rules.

Each entry runs a separate `cargo test --locked --package NAME --bin NAME` or
`--test NAME` invocation and requires exactly one completed libtest summary.
Grouped `workspace-bins`, `package-bins`, `package-tests` and `all-targets`
selections are refused: a single assertion summary from an early failed
executable cannot stand in for later unrun executables. Under report-only
application enforcement, later entries still run and each needs its own fresh
report before progression is permitted. A blocking failure can stop the build.

For example, an explicitly enumerated Nano hosted selection is expressible as:

```json
{"runs":[
  {"kind":"package-bin","package":"nanolinter","name":"nanolinter","gate":"application"},
  {"kind":"package-bin","package":"nanolinter-ci","name":"nanolinter-ci","gate":"policy"},
  {"kind":"package-test","package":"nanolinter-ci","name":"basegraph_provenance","gate":"policy"},
  {"kind":"package-test","package":"nanolinter-ci","name":"go_parity","gate":"policy"},
  {"kind":"package-test","package":"nanolinter-ci","name":"imageplan_contract","gate":"policy"},
  {"kind":"package-test","package":"nanolinter-ci","name":"release_plan","gate":"policy"},
  {"kind":"package-test","package":"nanolinter","name":"architecture_guards","gate":"policy"},
  {"kind":"package-test","package":"nanolinter","name":"configuration_doc_schema","gate":"policy"},
  {"kind":"package-test","package":"nanolinter","name":"dependency_guards","gate":"policy"},
  {"kind":"package-test","package":"nanolinter","name":"docs_workflow_guards","gate":"policy"},
  {"kind":"package-test","package":"nanolinter","name":"workflow_policy","gate":"policy"}
]}
```

This is a reviewed enumeration, not dynamic workspace discovery: administrators
must update it when approved executables change. Product PR graphs remain
product-owned; this selection does not replace them. Review the enumeration and
gate classification against the frozen product source before removing the
obsolete skip flag in a separate consumer migration.

Each selected invocation gets a new bounded native-output wrapper and must
report at least one executed (not merely ignored/skipped) test and complete
native termination, including on exit zero. No checkout report file is trusted.
Missing, malformed, truncated, zero-test and infrastructure reports block under
both enforcement settings. Valid **application** assertion failures still go
through the existing enforcement policy; report-only findings stay visible.
Policy assertion failures never receive that waiver. These are
native Go JSON / Rust libtest protocols, not arbitrary custom harnesses. Source
review must still establish the honesty and adequacy of the tests themselves.
This bounded extension does not migrate consumer fixtures or add GitLab/Forgejo
selection inputs; their existing command owners can use the CLI contract.

### Reusing selected tests in check jobs

Check jobs can invoke the same native test owners without metadata resolution,
dependency fetching, SBOM generation, compilation of release artifacts or signing:

```sh
forge-ci build go test --working-dir . \
  --test-selection-json '{"packages":["./test/policy"],"gate":"policy"}'
forge-ci build cargo test --working-dir . \
  --test-selection-json '{"runs":[{"kind":"package-test","package":"app","name":"guard","gate":"policy"}]}'
```

These commands require an explicit, nonempty selection; they never fall back to
workspace-wide test discovery. They reuse the release commands' fixed native
arguments, fresh reports and policy classification. Selection coverage still
requires review by the owner of the check job.

The selectors are Go/Cargo adapters, not a universal test schema. Shared policy,
handoff and publication mechanisms remain ecosystem-neutral. The existing
`policy tests --protocol … -- <native command>` interface continues to support
Go JSON, Cargo, Maven, Gradle, Node and XCTest application-test protocols.

## The receipt

### Live harness administrator inputs

The live controller passes the same four settings independently, all `false` by
default. Each controller variable accepts exactly `true` or `false`:

| Existing administrator input | Live controller variable |
|---|---|
| `enforce-lint` | `FORGE_CI_LIVE_ADMIN_LINT` |
| `enforce-vulnerabilities` | `FORGE_CI_LIVE_ADMIN_VULNERABILITIES` |
| `enforce-application-tests` | `FORGE_CI_LIVE_ADMIN_APPLICATION_TESTS` |
| `enforce-reproducible-builds` | `FORGE_CI_LIVE_ADMIN_REPRODUCIBLE_BUILDS` |

For a single scenario, use invocation-only assignments:

```bash
FORGE_CI_LIVE_ADMIN_LINT=true \
FORGE_CI_LIVE_ADMIN_APPLICATION_TESTS=true \
just test-live '^TestGitLabRelease_MinimalSignedArtifacts$'
```

The other two settings remain report-only. These variables belong to the controller;
it translates them into the existing literal inputs/flags, not job variables.

The controller writes literal inputs in the Forgejo/GitHub wrappers it generates
and the independent GitLab policy projects it pins. It reads the called engine's
input declarations, so a leaf receives only the settings it supports. A copied
consumer wrapper that already supplies an enforcement setting is refused. Ordinary
GitLab consumer CI rendering carries no enforcement settings; the controller owns
the separately rendered and pinned gate policy instead. Source checkouts and the
engine's embedded default are not edited. There is no additional policy mode.

These scratch wrappers exercise input propagation. GitHub organization-required
workflow enforcement and Forgejo administrator protection remain the deployment
requirements above; a successful scratch wrapper is not proof of either.

### Effective policy output

Inspect the effective policy with:

```bash
forge-ci policy show
forge-ci policy show --enforce-lint=false
```

The output gives each setting, its source (`embedded-default` or `flag`) and
the SHA-256 of the embedded default as `default-sha256`. The GitLab jobs that
run caller-listed commands print it at their start. Every finding decision
prints the check class, actual findings, whether enforcement is enabled and
where that came from:

```text
Enforcement policy default-sha256:<digest>: lint result=findings count=3 enforcement=false source=flag blocking-count=3
```

`count` is the total observed findings; `blocking-count` is the subset eligible
for the gate. `result=clean` means no observed findings, not merely no blocking
ones. Container scans retain their severity and fixability rules: two unfixed
HIGH findings at a CRITICAL threshold produce `result=findings count=2` and
`blocking-count=0`, even with enforcement enabled. Only eligible findings can
block when enforcement is enabled; report-only mode still reports that subset.
Other gates using a single count treat all their findings as eligible. Raw JSON
and its derived SARIF/GitLab reports retain the scan findings independently.

`flag` records that the value was given on the engine's command line, by
whatever wrote it; that the writer was the administrator's entry point is the
pipeline's to show. The digest identifies the embedded default only: with a
`flag` source, the effective setting is the one printed.

## Boundaries that stay

The legacy `skip-tests=true` input is refused, including `SKIP_TESTS=true`;
report-only tests must still run. Maven options that skip,
ignore or narrow tests (`skipTests`, `maven.test.skip[.exec]`,
`maven.test.failure.ignore`, fail-never, rerunning failed tests, and test
selection such as `-Dtest` or `-Dgroups`, in any spelling Maven accepts) and Gradle task exclusions are refused at the engine's build
gates. A project's own POM and `.mvn/maven.config` stay its responsibility.
Every exported or published layout is scanned; there is no consumer switch to
turn the scan off.
Forgejo lint/build actions checksum-verify their pinned engine even when a
consumer supplies `FORGE_CI_BIN`. GitLab's Nanolinter component bootstraps and
verifies that engine before lint execution, independently of optional SAST upload.

The protected pipeline must select the administrator's immutable engine and
workflow. GitLab uses the independently pinned external CI policy; GitHub and
Forgejo administrators must protect their wrappers, bootstrap coordinates and
credential-bearing workflow environments. A consumer-controlled engine build is
not administrator policy. Keep the same reviewed distribution in producer and
trusted signing jobs: the signer independently scans the delivered image bytes.
Producer variables and reports never become signing-policy authority.

## Lint and dependency findings

The shipped Nanolinter callers run once with full SARIF on stdout, then apply
the two policies independently:

```bash
forge-ci policy nanolinter --enforce-lint=true --enforce-vulnerabilities=true \
  --sarif-file nanolinter-security.sarif -- \
  nanolinter verify --require secrets,sast --report sarif --out -
```

OSV, Trivy filesystem, Go vulnerability and Rust audit findings use
`enforce-vulnerabilities`. Known lint/source checks use `enforce-lint`.
Unclassified security or external findings conservatively require both policies
to allow continuation. Disabling lint therefore cannot waive an independently
enforced vulnerability gate. Nanolinter configuration/runtime failures and a
failed invocation without classified findings remain blocking.
Structured operational-failure categories and the supported older report's
`tool failed` diagnostics remain blocking even if Nanolinter itself exits `1`.
The optional SARIF destination contains security results for existing provider upload steps;
its previous regular-file report is retired before execution, so a failed run
cannot upload that stale report as current evidence.

The check-selection floor still belongs in the administrator's trusted caller.
Enforcement is not proof that a consumer's test code is honest, that its chosen
plan covers every dependency, or that an arbitrary editable workflow executed
the approved gates. Provider protection and source review remain the authority
for those contracts. Unintegrated custom lint engines keep their own blocking
behavior; these settings are not a generic `continue-on-error` switch.

## Reproducible builds

`enforce-reproducible-builds: true` is strict reproducible builds.
`validate jvm-reproducibility` (part of `validate prerequisites`) then stops
the release when a planned Maven artifact's `pom.xml` lacks a non-empty
`<project.build.outputTimestamp>`, or a Gradle/Android build script lacks
`preserveFileTimestamps = false` and `reproducibleFileOrder = true`. With
the default `false`, the same check still runs, prints each missing setting with its fix
and the `reproducible-builds` policy decision, and annotates a warning instead
of an error. An unreadable or malformed `pom.xml`, a missing Gradle build
script and an unsafe working directory remain blocking. Cargo's `Cargo.lock`
and toolchain-pin requirements are not part of this setting.

## Application tests

The Go, Maven, npm, Cargo and Gradle/Android build paths apply the application
test policy after running tests. A non-enforcing engine only continues after an
ordinary native test-failure status with a recognized fresh summary. Unknown,
missing or malformed output stays blocking. The summary is recognized while the
tests run, so the size of a suite's output does not matter; a single output
line longer than 1 MiB stays blocking. Go's summary is read from the
`go test -json` events on stdout, and its stderr may only report module
downloads; the other tools' summaries are read from stdout and stderr. Maven
reports containing test setup errors are not assertion-only waivers; for
example a missing Testcontainers socket remains an error. Compilation and packaging invocations
are never waived. Where packaging needs a follow-up after failed tests, that
follow-up does not repeat the already reported tests.

When a non-enforcing engine stops anyway, the log says why, beside the test
tool's own error:

```text
Enforcement policy default-sha256:<digest>: application-tests result=not-waivable enforcement=false source=flag reason="no complete assertion-failure summary: test errors, setup and compile failures are never waived"
```

The other reasons name an exit status that is not the tool's assertion-failure
status, an output line over 1 MiB, or a command that failed without a test
exit status. A build blocked under report-only with no such line did not reach
the test gate.

Gradle invokes tests separately before packaging; Android retains its selected
module's test aggregate even when packaging tasks are overridden. A successful
invocation can report cached, up-to-date or `NO-SOURCE` tasks. Inspect native task
outcomes and test reports for physical execution and counts rather than treating
an invocation or summary checkmark as that evidence.

Supported Gradle project/system properties and global build options are passed
to both test and packaging invocations. Unknown or task-specific options (such
as `--tests`) are refused before execution rather than silently testing a
different configuration. `--dry-run`/`-m` applies only to packaging. Report-only
Gradle failures require matching assertion-only terminal task diagnostics and a
completed build-failure footer; daemon loss and unknown terminal causes block.

Custom trusted callers can use the same gate, including an iOS simulator job,
passing the administrator's setting as the shipped callers do:

```bash
forge-ci policy tests --enforce-application-tests=false --protocol go-json -- go test -json ./...
forge-ci policy tests --protocol xctest -- \
  xcodebuild test -scheme Example -destination 'platform=iOS Simulator,name=iPhone 17'
```

Protocols are `go-json`, `maven`, `cargo`, `node` (Node test-runner/TAP summary
with a native assertion diagnostic),
`gradle` and `xctest`. Other npm test frameworks remain blocking when their
failure output cannot be classified; disabling enforcement does not authorize
guessing that an unknown tool error is an assertion failure. An archive-only
Xcode build does not itself claim simulator-test coverage. Select the protocol
and command literally in the trusted caller, not from consumer-produced data.

Read the policy decision and original findings alongside native execution
reports, including absent/skipped and cached/no-source outcomes. An invocation
or summary checkmark alone does not prove physical test execution. A successful
non-enforcing job is not evidence of a findings-free application.
