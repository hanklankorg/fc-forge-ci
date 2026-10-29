# Development Guide

## Source checkout

Use a reviewed checkout of the revision you want to build. [`go.mod`](../go.mod)
requires only published modules, among them
`codefloe.com/itiquette/forge-lab-contract`, so a checkout builds from the
module proxy and needs no sibling directory or `replace` directive. Install the
CLI from the forge-ci checkout using the pinned toolchain, and check that it
runs:

If the pinned Go toolchain is not installed yet, follow the Linux or macOS
prerequisites below first. Building the CLI is independent of publishing the
workflow/runtime artifacts required by consumers.

```bash
mise exec -- go install ./cmd/forge-ci
forge-ci --version
```

Your Go bin directory (normally `$(go env GOPATH)/bin`) must be on `PATH`. To
install another reviewed source revision, check out that revision and run the
same command. Avoid `go install github.com/diggsweden/forge-ci/cmd/forge-ci@<ref>`
for exact-commit builds: Go can resolve a SHA to the moving pre-release tag and
reuse module contents cached before that tag moved.

Use `forge-ci --help` to inspect the local interface. `forge-ci init` still
requires a release tag and its examples on the repository it reads; it does not
copy examples from your local checkout. For pre-release workflow evaluation,
follow [Branch Testing](runtime-images.md#branch-testing) with the reviewed
pre-release images, rather than substituting a moving ref into production.

`forge-ci init <mirror URL> --dry-run` uses that same remote-release source and
temporary storage, but renders a proposal without writing project files.
Initializer unit tests inject an inert remote and use temporary project trees
to check generation parity, conflicts, and unchanged files; they need no provider.

The bootstrap installer fetches a detached Git checkout for a branch or full
40-character SHA-1 or 64-character SHA-256 commit ID
`forge-ci-binary-ref` and builds from that source, logging the resolved commit;
release tags install the signed archive instead. Self Release passes the commit
named by its release-request tag, so advancing the branch cannot change its CLI.
PR and snapshot orchestrators resolve source branches once for all host jobs;
standalone host jobs bind the build to their bootstrap checkout using
`FORGE_CI_SOURCE_SHA`. Release-tag inputs retain signed release-asset installation.
Use the complete commit ID rather than an abbreviated hash. Git source installs
select the repository's SHA-1 or SHA-256 object format before fetching.
`local`/`.` builds use the working copy and reject `FORGE_CI_SOURCE_SHA`; to build
a bound revision, pass its commit ID instead. Relative `FORGE_CI_INSTALL_DIR`
values are resolved against the invoking working directory.
An omitted binary ref resolves the current published stable release to its
concrete tag once. GitHub orchestrators use that tag for default runtime images
as well; explicit image overrides take precedence. Self Release's explicit
request commit never takes this default-discovery path.
See
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
   mise trust
   mise install just
   just install-dev-tools
   ```

4. Run quality checks:

   ```bash
   mise exec -- just verify
   ```

## Prerequisites - macOS

1. Install host tools through [MacPorts](https://www.macports.org/):

   ```bash
   sudo port install mise just bash coreutils findutils gsed flock libxml2
   export PATH="/opt/local/bin:/opt/local/sbin:$HOME/.local/bin:$PATH"
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
3. Check that MacPorts Bash is selected ahead of macOS's older `/bin/bash`:

   ```bash
   bash --version
   ```

4. Install the pinned project tools, including the uv-backed REUSE checker and
   workflow/live-tier utilities:

   ```bash
   mise trust
   just install-dev-tools
   ```

5. Nanolinter's pinned release currently supplies Linux binaries only. Build
   the pinned revision from a Nanolinter source checkout, following the native
   source-build steps in its `docs/modules/ROOT/pages/install.adoc`, and place
   it on `PATH`. Check `nanolinter --version` names that revision: a
   development build of another revision is not the pinned plan. Mise
   excludes its Linux-only release here instead of accepting a source archive
   with no executable.
6. Run native checks with the pinned tools:

   ```bash
   mise exec -- just doctor
   mise exec -- just test-unit
   ```

The unit suite's inert Linux shell fixtures require GNU coreutils/findutils,
GNU sed and `flock`. Their private command paths expose the selected real GNU
tools, including MacPorts' prefixed commands, without replacing native tools
for other tests. Keep the normal macOS utility path for native Go tests so
GNU tools do not hide portability defects. `xmllint` comes from `libxml2`.
Install success or a focused test is not a passing `just verify`.

`lint`, `lint-go` and `lint-go-golangci` also check the Linux build on a
non-Linux host, so platform-specific imports still reach depguard. The semantic
environment guard inspects Linux and the native build through compiler metadata.
Neither operation executes cross-compiled code; native runtime checks remain
separate.

Container and VM runtime evidence must match the execution host: run AMD64
jobs on AMD64 hosts, and ARM64 jobs on ARM64 hosts, including this Mac.
Cross-compilation is separate from execution. Linux-only integration requires
a native Linux environment; the companion's strict profile is a Linux gate.

## Available Commands

Run `just` to see all available commands.

`just verify` runs local lint, tagged compilation and integration tests,
including the untagged unit tests. It can use containers and network access;
optional integration evidence may skip and is reported. CI has additional
workflow-contract, smoke/fuzz and companion strict gates; see
[Testing conventions](testing.md) for the scope and full live prerequisites.
`just lint-fix` formats the justfile and runs nanolinter's supported fixers.

`just release-snapshot` validates the GoReleaser configuration and produces
unsigned local archives, SBOMs and checksums. Snapshot mode disables publication;
the recipe also explicitly disables signing, which snapshot mode alone does not.

`just clean` removes `bin/` and `dist/` before asking whether to clear caches.
Declining keeps the caches only. `clean-caches` clears the configured Go
module/build/test/fuzz caches, shared across checkouts by default, and the
configured golangci-lint cache. Just defaults that cache to
`.golangci-lint-cache/` in each checkout so linked worktrees do not replay
each other's diagnostic paths. `GOLANGCI_LINT_CACHE` overrides the location.

## Local verification tools

Project contributors get the repository's pinned toolchain with
`just install-dev-tools`, which installs Go before Go-based check tools.
Use `mise exec -- just ...` when mise is not activated in the shell. The pins
include golangci-lint, WSL, GoReleaser and REUSE's charset detector; tool health
must not depend on another checkout having installed them.
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

The depguard `layer-*` rules in `development/golangci-lint/golangci.yml` enforce this in `just lint`
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
2. Follow `docs/workflow-design-policy.md` for workflow structure and binary
   command rules.
3. Run `actionlint .github/workflows/*.yml`.
4. Parse workflow YAML and check reusable-workflow input compatibility.
5. Run `bash -n` for touched bootstrap scripts.
6. Add or update Go tests for behavior changes. For standalone shell
   scripts, keep `bash -n`, `shellcheck`, and targeted Go black-box tests
   next to the scripts when needed.

For a change under `.forgejo/`, or to a verb a Forgejo workflow or action
calls, run `just test-forgejo` as well: it holds the layer's structure and runs
its shell against the CLI built from your tree.

## The engine binary

The workflows and actions under `.forgejo/` run this repository's CLI, built
for linux/amd64 and linux/arm64. The tree tracks their SHA-256 and not the
binaries: `.engine/bin/forge-ci-linux-amd64.sha256` and
`.engine/bin/forge-ci-linux-arm64.sha256` are one line each, and a job
downloads the binary of its host's architecture by its line from the generic
packages of the forge that serves the layer, then verifies it.
[The binary in the layer](forgejo.md#the-binary-in-the-layer) describes the
download; this section is about the lines.

One recipe writes them, and nothing else may:

```bash
just refresh-engine-binary
git add .engine/bin/forge-ci-linux-amd64.sha256 .engine/bin/forge-ci-linux-arm64.sha256
```

It refuses a toolchain other than the Go release `go.mod` pins, builds the
CLI of your tree for both architectures as
[`scripts/ci/engine-binary.sh`](../scripts/ci/engine-binary.sh) defines
(cross-compiled, so any host records both), and records the digests. Run it after a change to a build input (`cmd/`,
`internal/`, `go.mod`, `go.sum`); a change to documentation, workflows or
tests names the same binary and needs nothing.

Before pushing, verify the lines are fresh with a read-only check that builds
both architectures and compares, without writing anything:

```bash
just check-engine-binary
```

It fails with the same digests `refresh-engine-binary` would record, so you
can see what changed without running the write. CI runs this check as a fast
gate before the integration suite in both the pull-request and release
workflows.

**The tip must be consistent, not every commit.** Commit the lines with the
change they follow, or in a commit after it. Two branches that both change Go
code both change the lines; after merging them, record them once more. What a
consumer pins is a commit whose lines are builds of its tree.

`TestRecordedEngineChecksumIsABuildOfThisTree` (in `just test-forgejo`, and
in `go test ./...`) rebuilds both architectures and compares, so a line cannot drift from
the tree unnoticed: it fails until the line is recorded, and its message
prints the line to write. It needs no Git history. Under another Go release than
`go.mod` names it cannot reproduce the build and says "not proven"; the
pull-request gate runs it with that release and in the strict profile, where
it can only pass or fail.
[Testing conventions](testing.md#the-engine-layer-tier) has the details.

The build names no commit and no date: `forge-ci --version` answers
`engine-layer`, and the digest identifies the build. The same sources
therefore build the same bytes at any commit, in any directory. The limits
are the toolchain, which must be the pinned Go release as the Go project
distributes it, and your module cache, which the build reads with the proxy
and the checksum database off. The second is why a forge does not take the
lines on trust: the job that publishes the binaries downloads the modules
itself and publishes only builds that match.

`scripts/ci/engine-binary.sh build <amd64|arm64> <output>` writes a binary
for local use, and the tests of `test/forgejobehavior` build theirs the same way. A
binary at `.engine/bin/forge-ci` is ignored by Git.

**Publishing.** A forge serves a commit of the layer once the binaries that
commit names are in the generic packages of the serving repository's owner.
The first job of
[`examples/forgejo/signer-image.yml`](../examples/forgejo/signer-image.yml)
does it, with `scripts/ci/engine-binary.sh publish`; the live tier does the
same for its scratch owner on the lab. Nothing is published from a
workstation.

Do not hand-edit the files, and do not build the binary any other way.

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

## Releasing forge-ci

The self-release pipelines (`self-release.yml`, `self-runtime-container.yml`)
are internal-use only.

1. **Authorise your signing key.** Add your SSH public key to
   `.forge-ci/allowed_signers` under a principal (typically your email):

   ```text
   maintainer@example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... your key
   ```

   Each line must follow the OpenSSH allowed_signers format:
   `principal [options] key-type base64-key`. Run `forge-ci doctor`
   for basic entry/key checks before committing. OpenSSH evaluates option
   values, principal matching and authorization during signature verification.

   Commit and push to `main`.

2. **Request the release.** Create a signed annotated tag and push it:

   ```bash
   git tag -s release-request/vX.Y.Z -m "Release vX.Y.Z"
   git push origin release-request/vX.Y.Z
   ```

   The tag must be signed with a key listed in `.forge-ci/allowed_signers`.

3. **CI does the rest.** The tag push triggers `self-runtime-container.yml`
   (builds runtime images, 30-60 min) and `self-release.yml` (runs black-box
   tests, evidence gates, waits for images, creates the final `vX.Y.Z` tag,
   builds signed CLI archives, publishes the GitHub release).

For recovery from a failed release, see
[Failed Release Recovery](failed-release-recovery.md).
