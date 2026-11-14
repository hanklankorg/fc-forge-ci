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
  nanolinter verify --require secrets --report sarif --out -
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

### A flag alone is not administrator policy

The engine cannot tell who wrote its command line, and neither can the
workflows, actions and components: each takes the settings as inputs, and
whoever writes the call can pass them. A consumer that calls a reusable
workflow or includes a component with `enforce-lint: false` in its own file
gets a green job. A setting is the administrator's only when it is written in
an **entry point the consumer cannot change** and that entry point's jobs are
the ones the forge requires. For organisation-mandated policy, the consumer's
own files do not supply these settings and its own jobs are not the required
gate. The examples and `forge-ci init` output set none.

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
merged with the gate. Include the consumer's file **before** the gate
components. The gates set their own `rules`, `needs`, `cache`,
`allow_failure` and scripts, so those keys from an earlier job of the same
name do not survive, but other keys and project variables still merge in.
Select events in the policy's `workflow: rules` or `include: rules`. Prefer
inject mode where the tier allows it.

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
      # Engine commit and image digests: see the Advanced Pinning Recipe.
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

An organization that keeps its settings in a policy repository records
there: the four values for each forge, the entry point that writes them (the
GitLab policy YAML, the GitHub ruleset workflow, the Forgejo code owners and
branch protection), a check that consumer repositories set no `enforce-*`,
and the log lines it expects (`source=flag` with those values, see
[Effective policy output](#effective-policy-output)).

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

## Release test selections (Go and Cargo, GitHub)

Release tests always run: `skip-tests: true` is refused. Without a selection,
Go runs `go test ./...` and Cargo runs `cargo test --locked --all-targets`.
A project whose full suite must not run on hosted runners lists the tests to
run instead, and the administrator reviews that list.

The GitHub release orchestrator takes `go-test-selections-json` and
`cargo-test-selections-json`, JSON objects keyed by artifact name; the
`build-go` and `build-cargo` workflows take `test-selection-json`. Set them in
the **administrator's protected entry point**, like the enforcement settings;
they are never read from `artifacts.yml`, the environment or consumer
scripts. A map must name exactly the release's artifacts of that ecosystem;
`{}`, missing or extra names and empty entries are refused before any build
runs.

A Go selection names packages, an optional `-run` pattern and a gate:

```json
{"packages":["./internal/...","./cmd/..."],"run":"TestSafe","gate":"policy"}
```

The tests run as `go test -json -count=1 -race -buildvcs=false`. Packages must
be paths inside the workspace.

A Cargo selection lists each test executable, as `package-bin` or
`package-test`, with its package, exact name and gate; `"ignored": true` runs
that executable's ignored tests:

```json
{"runs":[
  {"kind":"package-bin","package":"my-cli","name":"my-cli","gate":"application"},
  {"kind":"package-test","package":"my-cli","name":"config_schema","gate":"policy"},
  {"kind":"package-test","package":"my-cli","name":"slow_network","gate":"policy","ignored":true}
]}
```

Each entry runs as its own `cargo test --locked --package NAME --bin NAME` or
`--test NAME`. The gate is `application` or `policy`: only application
assertion failures can be report-only; **policy failures always block**. Each
invocation must run at least one test and produce a complete summary;
anything else blocks. Review the list when the project's test executables
change.

## Effective policy output

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
A derived report that cannot be written fails the scan with enforcement on
or off: the reports are the evidence the step hands on.

A lint-runner check that could not complete is not a finding and is never
waived: its gate prints `result=incomplete` with
`could-not-complete=<check ids>` unless the gate has findings, and the command
fails after the findings of the other checks are reported and the security
SARIF is written. A security check the runner reports as skipped, whether for
a network outage or in offline mode, could not complete either. One that does
not apply to the repository passes and is listed as
`not-applicable=<check ids>`; the gate prints `result=not-applicable` when
none of its checks ran. The gate reads the status of each check's row in the
runner's SARIF and nothing else: a check with nothing to scan must be
reported `n/a`, not `skip`. Nanolinter does not report skipped checks, so a
skipped or inapplicable check is absent from its report and neither fails nor
is listed.

A job that cannot run a security check leaves it out with the lint job's
`disable-checks` input, on each forge. The organisation's required checks
(`required-lints`) win over it: a check named there runs and blocks whatever
`disable-checks` says.

`flag` records that the value was given on the engine's command line, by
whatever wrote it; that the writer was the administrator's entry point is the
pipeline's to show. The digest identifies the embedded default only: with a
`flag` source, the effective setting is the one printed.

## Boundaries that stay

The `skip-tests=true` input is refused, including `SKIP_TESTS=true`;
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
  nanolinter verify --require secrets --report sarif --out -
```

OSV, Trivy filesystem, Go vulnerability and Rust audit findings use
`enforce-vulnerabilities`. Known lint/source checks use `enforce-lint`.
Unclassified security or external findings conservatively require both policies
to allow continuation. Disabling lint therefore cannot waive an independently
enforced vulnerability gate. Nanolinter configuration/runtime failures and a
failed invocation without classified findings remain blocking, as do
operational failures such as `tool failed`, even when Nanolinter exits `1`.
The `--sarif-file` holds the security results for the forge's upload step; an
old file there is removed before the run, so a failed run cannot upload it.

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
