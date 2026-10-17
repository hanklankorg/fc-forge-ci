# Development Guide

## Source checkout

Use a reviewed checkout of the revision you want to build. [`go.mod`](../go.mod)
requires only published modules, among them
`codefloe.com/itiquette/forge-lab-contract`, so a checkout builds from the
module proxy and needs no sibling directory or `replace` directive. Install the
CLI from the forge-ci checkout, and check that it runs:

```bash
go install ./cmd/forge-ci
forge-ci --version
```

Your Go bin directory (normally `$(go env GOPATH)/bin`) must be on `PATH`. To
install another reviewed source revision, check out that revision and run the
same command, or install a pushed commit or tag without a checkout:

```bash
go install github.com/diggsweden/forge-ci/cmd/forge-ci@<ref>
```

The bootstrap installer takes the same path for a branch or SHA
`forge-ci-binary-ref`; release tags install the signed archive instead. See
[CLI binary channels](runtime-images.md#cli-binary-channels).

## Prerequisites - Linux

1. Install [mise](https://mise.jdx.dev/) (manages linting tools):

   ```bash
   curl https://mise.run | sh
   ```

2. Activate mise in your shell:

   ```bash
   # For bash - add to ~/.bashrc
   eval "$(mise activate bash)"

   # For zsh - add to ~/.zshrc
   eval "$(mise activate zsh)"

   # For fish - add to ~/.config/fish/config.fish
   mise activate fish | source
   ```

   Then restart your terminal.
3. Install the pinned project tools, including the uv-backed REUSE checker and
   workflow/live-tier utilities:

   ```bash
   mise install
   ```

4. Run quality checks:

   ```bash
   just verify
   ```

## Prerequisites - macOS

1. Install [mise](https://mise.jdx.dev/) (manages linting tools):

   ```bash
   brew install mise
   ```

2. Activate mise in your shell:

   ```bash
   # For zsh - add to ~/.zshrc
   eval "$(mise activate zsh)"

   # For bash - add to ~/.bashrc
   eval "$(mise activate bash)"

   # For fish - add to ~/.config/fish/config.fish
   mise activate fish | source
   ```

   Then restart your terminal.
3. Install newer bash than macOS default:

   ```bash
   brew install bash
   ```

4. Install the pinned project tools, including the uv-backed REUSE checker and
   workflow/live-tier utilities:

   ```bash
   mise install
   ```

5. Run quality checks:

   ```bash
   just verify
   ```

## Available Commands

Run `just` to see all available commands.

`just verify` runs local lint, tagged compilation and integration tests,
including the untagged unit tests. It can use containers and network access;
optional integration evidence may skip and is reported. CI has additional
workflow-contract, smoke/fuzz and companion strict gates; see
[Testing conventions](testing.md) for the scope and full live prerequisites.
`just lint-fix` formats the justfile and runs nanolinter's supported fixers.

`just clean` removes `bin/` and `dist/` before asking whether to clear caches.
Declining keeps the caches only. `clean-caches` clears the configured Go
module/build/test/fuzz caches, shared across checkouts by default, and the
configured golangci-lint cache. Just defaults that cache to
`.golangci-lint-cache/` in each checkout so linked worktrees do not replay
each other's diagnostic paths. `GOLANGCI_LINT_CACHE` overrides the location.

## Local verification tools

Project contributors get the repository's pinned toolchain with `mise install`.
For ad-hoc verification outside that toolchain, mise's aqua backend can install
the portable release verifiers:

```bash
mise use -g aqua:sigstore/cosign
mise use -g aqua:cli/cli             # gh
mise use -g aqua:containers/skopeo
```

`gpg`, `git`, `sha256sum`, and `ssh-keygen` come from the host distribution.
Check `cosign version`, `gh --version`, and `skopeo --version` before diagnosing
a verification failure as an artifact problem.

### Local Git signature setup

SSH verification needs an `allowed_signers` entry associating a trusted public
key with a signer principal (commonly an email address). Obtain the key material
from a trusted, independently reviewed source. GitHub exposes public SSH keys at
`https://github.com/<username>.keys`; it exposes public OpenPGP keys at
`https://github.com/<username>.gpg`. Confirm identity/fingerprints out of band
before trusting either endpoint.

Use public-key files saved in an operator-owned directory. Inspect their
fingerprints and compare them with the independently trusted values before
adding any key to local trust:

```bash
ssh-keygen -lf /absolute/path/to/reviewed-signer.keys
gpg --show-keys /absolute/path/to/reviewed-signer.asc
```

Once every key in those files has been reviewed, the following installs the SSH
entries and OpenPGP keys for local inspection. Replace the example principal
with the identity whose keys you reviewed:

```bash
mkdir -p ~/.ssh
while IFS= read -r key; do
  printf '%s %s\n' 'developer@example.com' "$key"
done < /absolute/path/to/reviewed-signer.keys >> ~/.ssh/allowed_signers
git config --global gpg.ssh.allowedSignersFile ~/.ssh/allowed_signers

gpg --import /absolute/path/to/reviewed-signer.asc
```

Repository release policy uses the reviewed files under `.forge-ci/`, not a
developer's global file. The setup above is only for local inspection with
`git verify-tag`, `git verify-commit`, and `git log --show-signature`.

References: [Git signing](https://git-scm.com/book/en/v2/Git-Tools-Signing-Your-Work),
[Sigstore](https://docs.sigstore.dev/), [SLSA](https://slsa.dev/), and
[GitHub Actions hardening](https://docs.github.com/en/actions/security-guides/security-hardening-for-github-actions).

## Where code goes

`internal/` is ports and adapters. Every import points inward at `domain`:

```text
cli  ──►  app  ──►  domain  ◄──  adapters
```

| You are writing | It goes in |
|---|---|
| A business rule, or the interface a rule needs | `internal/domain/…` |
| The ordering of steps in a use case | `internal/app/…` |
| A wrapper around a real tool, API, or the environment | `internal/adapters/…` |
| Flag parsing, output rendering, adapter construction | `internal/cli/…` |
| A helper with no domain knowledge (`listval`, `safeexec`) | `internal/<name>` |

The layers are siblings on disk because the layering is expressed by import
direction, not by nesting. Two rules cover most decisions:

- **Need a tool from a use case?** Do not import the adapter. Declare a small
  interface listing only the methods you need, next to the code that uses it
  (`gitOps` in `internal/app/validate/tags.go` is the reference example), and
  construct the real adapter in `internal/cli`.
- **Adapters never import `app` or `cli`,** and `domain` imports neither
  those nor `adapters`.

The depguard `layer-*` rules in `.golangci.yml` enforce this in `just lint`
and name the fix when it fails. The full rule, including why `platform` is an
adapter and why pure OpenPGP verification lives in `internal/pgp` rather than
an exempt adapter, is in [ADR 0004](adr/0004-package-layering.md).

## Input-policy decisions

### Optional distro-package signing

`release gpg sign-packages` signs the `.deb`, `.rpm` and `.apk` files present in
its directory. Zero matches are valid; required key inputs, import and fingerprint
validation still run. The Forgejo/GitLab `sign-distro-packages` toggle enables this
operation, not a promise that the build produces distro packages.

Keep this contract. A consumer that promises particular packages must check its
expected outputs at the build/assembly boundary, before handing them to signing.
Do not infer output cardinality from the signing toggle or add a second generic
expectation schema without a concrete consumer requirement. Artifact integrity
and build-output completeness are different checks.

### Context-specific path checks

Keep `pathsafe.Relative` as the shared lexical primitive. Artifact extraction,
configuration validation, working-directory admission, attachment globs and
signer-metadata removal retain their caller-specific checks, errors and filesystem
operations. Lexical validation cannot replace confinement through `os.Root`.

Go and Cargo compilation open the real `dist` directory before removing their
selected platform outputs. A linked output directory is refused, including an
alias elsewhere in the same checkout. `just clean-build` accepts only the default
checkout-owned `bin/` and `dist/`; custom build directories remain caller-managed.
Its isolated regression is `bash scripts/ci/test-clean-build.sh` and is included
in `just test-unit`.

`pathsafe.ArtifactStaging` restores replaced files on installation failure. If
restoration also fails, the error names the retained `.forge-ci-artifact-backup-*`
directory. Inspect and recover those files before retrying; `Close` removes only
the incoming stage, never an incomplete rollback's backup.

Do not merge these contracts just because their guards look similar. For example,
attachment assembly currently rejects any `..` substring, whereas upload checks
parent components; `artifact..tgz` therefore has different acceptance between
those commands. Preserve compatibility unless a specific use case warrants a
deliberate change and behavior tests. There is no general path-policy framework
or source-pattern guard to add.

## Error classification

Every error that reaches `main` goes through `errs.ExitCodeFromError`. An error
carrying no `errs.Err*` sentinel falls through to `ExitCodeSoftware` (70),
whose meaning is *"an error we did not classify. File a bug against
forge-ci"*. That default is correct, which is exactly why an unclassified
error is a defect: it tells an adopter who mistyped a path that our tool is
broken.

**Classify at the boundary that knows the meaning; add context above it
without re-classifying.**

| The boundary | Use | Because |
|---|---|---|
| Reading an operator-supplied file | `cliio.ReadFile` | Maps absent → `ErrMissingInput` (66), unreadable → `ErrPermissionDenied` (77), a directory → `ErrMissingInput`, and bounds the read |
| Reading below an already checked root | `cliio.ReadFileInRoot` | Preserves the same file-type and size bounds without reopening through the ambient filesystem |
| Running an external binary | `safeexec.WrapError` | A non-exit failure is the tool never having run, so `ErrDependencyUnavailable` (69), *"x not found in $PATH"*, not a crash |
| An HTTP response | `errs.FromHTTPStatus` | 4xx is the request being refused, not an outage |
| Parsing operator or tool data | `ErrInvalidConfig` (78) / `ErrMalformedInput` (65) | *Their* config vs *some tool's* output |

Two rules follow from it:

- **Attach the sentinel once.** Layers above add context with a single `%w`.
  Wrapping twice reads back as `"…: invalid configuration: invalid
  configuration"` in the one line an operator sees. `domain/config.invalidConfig`
  is the idempotent form when a helper cannot know whether its caller already
  classified.
- **One mistake, one exit code.** When two guards can catch the same operator
  error, they must agree. Comparing `Value == ""` while the layer below trims
  meant `--sboms ""` and `--sboms "   "` exited differently.

`container ledger merge` classifies a missing local input path as
`ErrMissingInput` (66), including a path disappearing during discovery. This
classification belongs at the local-input boundary; network failures still map
to dependency unavailable (69).

Plan lookup and merging share `planfile.Decode`: plan and scope containers must
be JSON objects, and JSON number spellings are retained without float64 conversion.
`plan write` stores the whitespace-normalized command scope it validated.

The [Git credential contract](providers.md#git-operation-credentials) owns
file/stdin inputs, CR/LF handling, precedence and issuing-server scope.
Default keyless verification identity follows the execution runner,
independently of the forge selected for API operations.

Repository sync guards read governed inputs with `reporoot.ReadFile` and
`reporoot.ReadDir`, rejecting symlink components instead of following them.
Generated-file failures report bounded difference context, sizes, hashes and the
refresh command. Artifact vocabulary checks use named documentation blocks;
mentions elsewhere in prose cannot satisfy them.

The shell-source guard discovers `.sh`, `.bash`, `.sh.tmpl`, and extensionless
files with a supported `sh`/`bash` shebang. It skips Git metadata, dependency and
build-output directories. Embedded workflow/Go/just recipes are separate scopes;
the existing printf rule is a lexical alarm, not a complete shell parser.

Note the sentinels are project-chosen, not BSD `sysexits` throughout:
`ErrUsage` is **2** (POSIX CLI convention), not 64. `errs.ExitCodeConstants`
and its test are the reference.

`test/archguard` enforces the exec-adapter half at test time. The rest is
enforced by tests asserting the sentinel: `errors.Is(err, errs.ErrX)`, never
a bare `err != nil`, which passes whatever the classification turns into.

## Test discipline

Tests provide evidence for the behavior they exercise. Keep them reproducible
and account for missing coverage through the policies below:

- **No flaky tests.** A failure is either a real bug or a missing test
  guarantee; "just re-run it" is not an acceptable fix. Quarantine the
  test (`t.Skip("…tracking <issue>")`) until it is fixed or deleted:
  never leave a `// flaky` comment behind a green build.
- **Time is injected.** Anywhere a test output depends on the wall clock,
  the production code takes a `now func() time.Time` (see
  `internal/app/summary/*.go`, `internal/app/build/go.go::resolveBuildDate`).
  Tests pass `fixedNow()`. When `SOURCE_DATE_EPOCH` is set, the same
  injection point honours it, making reproducible-build callers and tests
  share the same code path. Security reports
  (`internal/app/security/trivy.go::reportTimestamp`) take no clock; their
  tests pin the timestamp with `SOURCE_DATE_EPOCH`. Every reader parses the
  variable with `domain/build.ParseSourceDateEpoch`. For the Go build and the
  security reports a value that is set but unusable is a usage error, never a
  fallback to the wall clock.
- **Per-test filesystem state.** `testfs.NewReal(t)` returns a fresh
  `t.TempDir()`-rooted workspace per test. No `os.Chdir` outside the
  helper. No state survives between tests in the same package.
- **Account for missing external tools.** Integration tests use
  `evidence.RequireTool(t, "cargo")` (or the tool they need). Absence is tallied
  and skipped in the default profile, but fails in the strict profile. A tool
  that is present and fails or hangs is a failure, not a skip. Follow
  [Skipped evidence is accounted for](testing.md#skipped-evidence-is-accounted-for)
  for the helper and `TestMain` wiring.
- **`t.Parallel()` is the default**, both for unit and integration tests.
  Anything that can't run in parallel needs an explanatory comment.

Pipeline-level determinism is documented in `docs/verification.md#deterministic-pipeline`.

## Workflow Refactor Checklist

When changing workflows or workflow-facing binary behavior:

1. Keep public workflow contracts stable unless the change is explicitly versioned.
2. Follow `docs/workflow-design-policy.md` for workflow structure and binary command rules.
3. Run `actionlint .github/workflows/*.yml`.
4. Parse workflow YAML and check reusable-workflow input compatibility.
5. Run `bash -n` for touched bootstrap scripts.
6. Add or update Go tests for behavior changes. For standalone shell
   scripts, keep `bash -n`, `shellcheck`, and targeted Go black-box tests
   next to the scripts when needed.

For a change under `.forgejo/`, or to a verb a Forgejo workflow or action
calls, run `just test-forgejo` as well: it holds the layer's structure and runs
its shell against the CLI built from your tree.

## The Forgejo layer's engine binary

The workflows and actions under `.forgejo/` run this repository's CLI, built
for linux/amd64. The tree tracks its SHA-256 and not the binary:
`.forgejo/bin/forge-ci.sha256` is one line, and a job downloads the binary
by it from the generic packages of the forge that serves the layer, then
verifies it. [The binary in the layer](forgejo.md#the-binary-in-the-layer)
describes the download; this section is about the line.

One recipe writes it, and nothing else may:

```bash
just refresh-forgejo-binary
git add .forgejo/bin/forge-ci.sha256
```

It refuses a toolchain other than the Go release `go.mod` pins, builds the
CLI of your tree as
[`scripts/ci/forgejo-binary.sh`](../scripts/ci/forgejo-binary.sh) defines,
and records the digest. Run it after a change to a build input (`cmd/`,
`internal/`, `go.mod`, `go.sum`); a change to documentation, workflows or
tests names the same binary and needs nothing.

**The tip must be consistent, not every commit.** Commit the line with the
change it follows, or in a commit after it. Two branches that both change Go
code both change the line; after merging them, record it once more. What a
consumer pins is a commit whose line is a build of its tree.

`TestRecordedEngineChecksumIsABuildOfThisTree` (in `just test-forgejo`, and
in `go test ./...`) rebuilds and compares, so the line cannot drift from the
tree unnoticed: it fails until the line is recorded, and its message prints
the line to write. It needs no Git history. Under another Go release than
`go.mod` names it cannot reproduce the build and says "not proven"; the
pull-request gate runs it with that release and in the strict profile, where
it can only pass or fail.
[Testing conventions](testing.md#the-forgejo-layer-tier) has the details.

The build names no commit and no date: `forge-ci --version` answers
`forgejo-layer`, and the digest identifies the build. The same sources
therefore build the same bytes at any commit, in any directory. The limits
are the toolchain, which must be the pinned Go release as the Go project
distributes it, and your module cache, which the build reads with the proxy
and the checksum database off. The second is why a forge does not take the
line on trust: the job that publishes the binary downloads the modules
itself and publishes only a build that matches.

`scripts/ci/forgejo-binary.sh build <output>` writes the binary for local
use, and the tests of `test/forgejobehavior` build theirs the same way. A
binary at `.forgejo/bin/forge-ci` is ignored by Git.

**Publishing.** A forge serves a commit of the layer once the binary that
commit names is in the generic packages of the serving repository's owner.
The first job of
[`examples/forgejo/signer-image.yml`](../examples/forgejo/signer-image.yml)
does it, with `scripts/ci/forgejo-binary.sh publish`; the live tier does the
same for its scratch owner on the lab. Nothing is published from a
workstation.

Do not hand-edit the file, and do not build the binary any other way.

## Branch Testing Reusable Workflows

The [runtime-image branch-testing guide](runtime-images.md#branch-testing) owns
the producer prerequisites, publication eligibility and caller examples.
Coordinate a reviewed workflow commit, published runtime images and a matching
plain-runner CLI ref. A workflow branch alone does not change its runtime-image
defaults or make the CLI revision installable.

GitHub resolves nested reusable workflows that use
`uses: ./.github/workflows/<name>.yml` from the same commit as the workflow that
called them. Those local nested calls are safe for branch testing and do not need
an explicit `@<ref>`.

Manual merged-image publication requires the configured prerelease branch;
`publish=true` on another branch does not establish that the required tags were
published. Confirm the published images before selecting consumer pins. Commit
refs select source; image digests select exact content. The linked guide
describes both boundaries.

## Forks and Other Branches

Serving the workflows from a fork is adopter documentation, in each forge's
guide: [GitHub](github.md#forks), with the repository variables a fork sets
and the one-time `v0.90.0-pre` release provisioning;
[GitLab](gitlab.md#forks); and [Forgejo](forgejo.md#using-the-layer-from-a-fork).
