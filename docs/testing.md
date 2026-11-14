# Testing conventions

Test architecture for the Go runtime. Follow these patterns when adding new
code; PR review checks that the layers are respected and the helpers are
used.

For what each project kind and variant is proven on per forge, see the
generated [Testing matrices](testing-matrices.md) and the live marks in
`test/live/evidence.txt`. Test layers below describe what
can run; they do not establish that a feature has passed on a real forge.

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
| **black-box suite** | the binary against real toolchains and fixtures in the external testsuite | `blackbox`, in that repo | minutes | yes | real tools; forge endpoints faked; package downloads may use network |
| **live / conformance** | the same scenario against configured real lab forges (GitLab and Forgejo), plus hosted GitHub trials | `live` | minutes | per-forge subtests only (`-p 1`) | a real lab |

Run them as:

```text
go test ./...                          # domain + fast adapter + repo guards
go test -tags=integration ./...        # + full-stack adapter
go test -tags=smoke ./cmd/...          # + CLI smoke tier
```

Install the toolchain as [DEVELOPMENT.md](DEVELOPMENT.md#prerequisites---linux)
describes. The untagged tests also need `bash`, `jq` and Git (`mockbinary`
records subprocess calls through a shell fixture, and fixtures run
`git init`); they need no real GPG or containers. Integration needs real
GnuPG 2 and Git, and its recipes refuse early when `gpg` is missing.

`just verify` is the local lint plan plus tagged compilation and integration
tests (which include untagged tests). Integration can run containers, fetch
images and skip optional evidence.

### In this repository's own CI

The pull-request workflow runs:

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

### Isolated npm/Cargo release-version evidence

The existing key-free bump-job/text-and-digest handoff also supports `npm` and
`cargo`. The signer invokes no package/build tool. npm root manifest and active
lockfile versions, or Cargo's root/shared-workspace version and local workspace
lock entries, must agree. Decoded document comparison refuses script,
dependency, workspace membership and unrelated metadata changes even when they
can be written as a version-shaped string substitution. Existing lockfiles must
participate; the Maven handoff retains its existing behavior.

`TestIsolatedVersionBump_TwoNativeNPMCargoRounds` uses real npm/Cargo and Git in
owned repositories for two consecutive bumps/applies/commits. It runs with
`-tags=integration`, controlled empty npm configs, an isolated Cargo cache and
offline dependency-free fixtures. A separate real-Git negative case disguises a
script change as a version replacement and requires refusal without publishing
release paths or advancing HEAD. These checks are not whole-forge releases.

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
fixture sets, and each must fail; `requireBuildahStore` says why a buildah
that cannot create a container fails.

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

### What the architecture guards can see

The architecture guards analyse source; they do not prove executed behaviour.

- The source-walking guards (credential, exec-wrap, forge identity) parse with
  `go/parser`, which ignores build constraints, so they also read a
  `//go:build !linux` file on Linux.
- The environment guard loads the Linux and the native build through the
  installed Go compiler's export data and a populated module cache, with
  downloads off. Like golangci-lint's depguard layering rules, it honours build
  constraints. It resolves stdlib identities, string constants and function
  values written once in a package.
- Neither follows a lookup carried through a struct field, a reassigned
  variable or a parameter, and the environment guard cannot fail closed on
  them: an injected lookup from the composition root is the sanctioned seam,
  and the fixtures `injected lookup` and `shadowed package parameter` pin it
  as permitted. Exec-path analysis likewise does not follow command values
  through fields, interfaces or closures.

`TestGuardEligibility_EveryProductFileIsJudged` lets a product file that
neither release build (Linux, Darwin) compiles import nothing at all, so it can
neither read the environment nor cross a layer; a variant that needs an import
is linted and guarded under a GOOS that builds it. The same test fails if a
generated product file appears.

`TestEveryCommandGroupHasALiveDisposition` maps every command group under
`internal/cli/commands` either to one conformance scenario or to its coverage
gap. It fails on an unmapped group, a stale entry, or a named scenario that is
gone or no longer holds its group's name as a string literal.

### The engine layer tier

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
CLI there instead: `scripts/ci/engine-binary.sh build` builds it from the tree
once per test binary, and the step's own shell runs against it. A renamed verb
or flag, a refusal the workflow relies on, or a manifest the signer step would
finalize differently fails here and not inside a release job
(`TestForgeCICLIContract` checks every call in `.forgejo/`, the Forgejo and
GitLab examples under `examples/`, `.github/workflows/`, `templates/` and
`.gitlab/bin/` against the built CLI's own help). Nothing in the tier
reaches a forge, a registry or a container runtime.

The tier builds the engine for the host's architecture, amd64 or arm64, and
runs it natively; on another architecture it reports "not proven" rather
than running under emulation, and the strict profile fails.
`test/forgejobehavior` is built for Linux only, so on macOS/BSD it reports
one "not proven" entry instead. The checksum comparison and publish tests
build both architectures and only read their bytes, so they run on any Linux
host.

The same builds are the reference for the binaries the layer runs:
`TestRecordedEngineChecksumIsABuildOfThisTree` fails when a line of
`.engine/bin/forge-ci-linux-<amd64|arm64>.sha256` is not the digest of its
build, and is not proven under a Go other than the one `go.mod` pins.
[The engine binary](DEVELOPMENT.md#the-engine-binary) says how the lines are
recorded and which gate runs the test strictly.

The download itself is tested against a local HTTPS server standing in for
the package registry: the address per context, the choice of binary by a
faked `uname -m` and the refusal of other architectures, the refusals, the
strict mode, and `engine-binary.sh publish`.

What this tier cannot show is the runner: how Forgejo expands a called
workflow, resolves `vars`, or schedules the jobs. The
[live tests](live-runs.md#scenarios-are-repeatable-by-construction) do: their
`TestForgejoLayer_*` scenarios serve this checkout's `.forgejo/` from a scratch
repository on the lab, and `just test-live-consumer forgejo` runs a real
consumer's own workflow against it; see
[Trying a consumer](live-runs.md#trying-a-consumer).

### The black-box tier lives in another repository

`cmd/forge-ci/smoke_test.go` is the in-repo smoke harness; the bulk of the
black-box scenarios are in the external suite repository.
[CLI black-box testing](cli-black-box.md) is the catalogue for both and says
which scenario goes where.

### The live tier

The live tests run Forge CI against real forges: a Forgejo and a GitLab in a
local lab, and a GitHub test organization. They are destructive and started by
a person, never by CI. [Live runs](live-runs.md) is their manual.

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

| Package | Guards | Reads |
|---|---|---|
| `test/archguard` | where code may live: which layer may read the environment, mint a credential, or branch on the platform (import direction, ADR 0004, is the depguard `layer-*` rules in `development/golangci-lint/golangci.yml`) | Go source under `internal/` |
| `test/forgejoguard` | the Forgejo layer's signing boundary and consumer contract: secrets per job, signer bootstrap order, coordinate preflight, routing, strict YAML, no organization named, workflows that start only when called, actions that call no sibling by path and take no credential | `.forgejo/`, `containers/signer/`, the Forgejo examples under `examples/` |
| `test/lexiconguard` | a spelling, regex, or pattern literal is declared once | every file in the repo |
| `test/syncguard` | a generated file still matches the Go it is generated from, or the tree it describes | `docs/`, `.forge-ci/`, `test/live/` |
| `test/workflowguard` | the workflow contract adopters code against | `.github/workflows/`, `examples/` |

The rule: a guard that reads files outside its own package goes in one of
these; a test of `internal/cli` stays in `internal/cli`.

`internal/testutil/reporoot` resolves the repository root for all of them, so
none of them carries its own copy of that logic.
The sync guards read their inputs with `reporoot.ReadFile` and
`reporoot.ReadDir`, which refuse symlink components, and report a stale
generated file with a bounded difference and the refresh command. The
shell-source guard reads `.sh`, `.bash`, `.sh.tmpl` and extensionless files
with an `sh` or `bash` shebang; its printf rule is a lexical alarm, not a shell
parser.

Two habits keep a guard useful:

- **Explain the fix, not the rule.** The failure message should say what to do
  next; the contributor can already see what they did.
- **Poison it once.** A guard that has never been seen to fail is a guard that
  may not work. Several carry a companion test that feeds them a violation on
  purpose (`TestRuntimeImageTagGuardFindsPins`).

Repository-wide guards live under `test/`; Go behavior tests stay next to
the code they exercise. The default `go test ./...` gate discovers both.
Shared helpers stay in `internal/testutil` so colocated tests can import them.
The shell bootstrap scripts are tested under `scripts/...` with Go tests.

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
underscore (`TestRecordedEngineChecksumIsABuildOfThisTree`), because a guard
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

Older files do not all follow this: some files with the `integration` build
tag are not named `_integration`, and some in-package test files are not named
`_internal`. Inside `internal/`, `_guard` names a walk of the command tree or
the source in some files (`internal/cli/cienv_guard_test.go`) and an ordinary
refusal test in others (`internal/adapters/forgejo/authority_guard_test.go`).
Name a new file by the table; rename an old one when you change it.

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
be reported even when another fails; the four preflight test files do. `t.Helper()` on
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

| Package | Purpose |
|---|---|
| `cliflags` | Reads urfave/cli flag metadata for the guards that audit the CLI surface |
| `evidence` | Tool lookup that tallies what a run could not prove, plus the strict profile |
| `fakegitserver` | In-memory transport (`Client()`) with route registration for the GitHub API (`New`) and the GitLab API (`NewGitLab`, preserves `%2F` in project paths); no listener |
| `fakejobresultstore` | In-memory `ci.JobResultStore`; `Seed` preloads records from earlier jobs |
| `fakemanifestsink` | In-memory `ci.ManifestSink` capturing stage JSON bodies |
| `fakeoutputsink` | In-memory `ci.OutputSink` capturing scalar + multiline |
| `fakeprovider` | In-memory `provider.Provider` with call recorder |
| `ghaenv` | Tempfile-backed `GITHUB_OUTPUT`/`STEP_SUMMARY` with heredoc reader |
| `gitlabrender` | Renders this repository's GitLab components and child pipelines without a GitLab, for structural checks |
| `glabenv` | GitLab-CI env (`CI_*`) + dotenv-style `CI_OUTPUT` reader |
| `golden` | Golden-file equality with `-update` flag |
| `gpgkey` | Throwaway ed25519 GPG key in isolated GNUPGHOME |
| `isolatedenv` | Scrubs the process environment (HOME, Git, SSH, GPG, CI variables) for one test |
| `isolatedgit` | Throwaway repo with repo-local identity, optional bare remote; `Main` isolates the test process from TestMain |
| `mockbinary` | Shell stubs that record argv/stdin/cwd; hand the code `Path(name)`, or call `OnPath()` (serial) when it looks a bare name up |
| `reporoot` | The repository root, for the repo-wide guards |
| `repositorycases` | Shared repository-addressing cases for forge adapters and bootstrap scripts |
| `sshkey` | Throwaway SSH keys for signing-key and host-key tests |
| `testenv` | Canonical isolated env wrapper for env-sensitive tests, built on `isolatedenv` |
| `testfs` | Thin real-FS and memory-FS helper for file-heavy tests |
| `toolrecorder` | One recorder for every external build tool the ecosystem builds drive |

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

These are per-layer coverage targets for reviews and local checks. No CI
job enforces them.

| Layer | Target |
|---|---|
| `internal/domain/...` | ≥ 90% |
| `internal/adapters/...` | ≥ 80% |
| `internal/app/...` | ≥ 80% |
| `internal/cli/...` | ≥ 60% (lots of urfave wiring) |
| `cmd/forge-ci/...` | ≥ 50% |
| **Project total** | ≥ 80% |

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

When you change tests:

- Replace a list of cases only under a parity test that reads both lists.
- A rewritten guard keeps every assertion of the old one, or names the one
  it drops and why. A test with the same name is not proof.
- Before blaming the environment for a failure, run the control that
  differs in that one respect.
- What a run proves comes from its own test names and results.
- Fixtures name no real host or downstream project. Use `registry.invalid`
  and the shipped demos.

## Rules every test follows

1. **Keep the subject under test real.** Domain tests use pure inputs; adapter
   tests may fake external HTTP transports with `fakegitserver` or executables
   with `mockbinary`; application tests pass fake ports rather than mocking
   the use case itself.
2. **Every helper takes `t *testing.T` first** and registers
   `t.Cleanup` itself. No `defer cleanup()`, no global state, no `init()`.
3. **`testdata/` is a contract.** Don't generate fixtures at test time;
   commit them. Refresh with `-update`. Reviewer approves the diff.
4. **No flaky tests.** A failure is a real bug or a missing guarantee; "run it
   again" is not a fix. Skip the test with a tracking reason
   (`t.Skip("…tracking <issue>")`) until it is fixed or deleted.
5. **Time is injected.** Where output depends on the clock, the production
   code takes a `now func() time.Time` and tests pass a fixed one. When
   `SOURCE_DATE_EPOCH` is set the same injection point honours it; every
   reader parses it with `domain/build.ParseSourceDateEpoch`, and a set but
   unusable value is a usage error, never a fallback to the wall clock.
