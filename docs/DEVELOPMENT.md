# Development Guide

## Project identity

| Surface | Name |
|---|---|
| Engine repository and executable | `forge-ci` |
| Engine Go module | `github.com/diggsweden/forge-ci` |
| External black-box suite repository | `forge-ci-blackbox-tests` |
| External black-box suite Go module | `github.com/diggsweden/forge-ci-blackbox-tests` |
| Configuration and build metadata directory | `.forge-ci` |
| Product environment prefix | `FORGE_CI_*` |
| Engine binary under test | `FORGE_CI_TEST_BINARY` |
| Workflow input prefix | `forge-ci-*` |
| Linux engine delivery | `canonical-cli-bootstrap.yml` and `consume-canonical-cli` |
| macOS engine build | `build-darwin-cli` |
| Layer setup action | `setup-forge-ci` |
| Layer binary checksums | `.engine/bin/forge-ci-linux-amd64.sha256`, `.engine/bin/forge-ci-linux-arm64.sha256` |
| Package and image prefix | `forge-ci` |
| Development branch | `main` |
| Placeholder release in examples and snippets | `v0.90.0` |
| Rolling prerelease channel | `v0.90.0-pre` |

The Go module has no major-version suffix. Project-owned headers and REUSE
metadata use `2026 Digg - Agency for Digital Government`; third-party licence
notices keep their own attribution.

The engine's own workflows pin the black-box suite by a reviewed commit and
select the candidate binary with `FORGE_CI_TEST_BINARY`.

## Source checkout

Use a reviewed checkout of the revision you want to build. [`go.mod`](../go.mod)
requires only published modules, among them
`codefloe.com/itiquette/forge-lab-contract`, so a checkout builds from the
module proxy and needs no sibling directory or `replace` directive. Install the
pinned Go toolchain first (see the Linux or macOS prerequisites below). Then
install the CLI from the forge-ci checkout and check that it runs:

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

The GitHub workflows build the engine from the full 40-character commit in
`forge-ci-binary-ref`; [Engine delivery](engine-delivery.md) describes how.

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
3. Check that MacPorts Bash and the prefixed GNU utilities are available.
   Keep `/opt/local/libexec/gnubin` out of the general `PATH`: the test fixtures
   expose GNU semantics privately, while native tests continue to exercise the
   macOS tools.

   ```bash
   command -v bash                    # /opt/local/bin/bash
   bash --version                     # GNU bash 5.x
   gcp --version
   grealpath --version
   gfind --version
   gsed --version
   gsha256sum --version
   command -v flock xmllint
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

Container and VM runtime evidence must match the execution host: run AMD64
jobs on AMD64 hosts, and ARM64 jobs on ARM64 hosts, such as an ARM64 Mac.
Cross-compilation is separate from execution. Linux-only integration requires
a native Linux environment; the external suite's strict profile is a Linux gate.

### Host capability matrix

| Host | Native local gates | Cross-compilation | Container/runtime evidence | Strict external suite |
|---|---|---|---|---|
| Linux AMD64 | Full unit, lint, smoke and integration gates | Linux/macOS, AMD64/ARM64 binaries | Native `linux/amd64` only | Supported when every prerequisite is installed |
| Linux ARM64 | Full unit, lint, smoke and integration gates | Linux/macOS, AMD64/ARM64 binaries | Native `linux/arm64` only | Supported; architecture-specific exclusions are reported by the suite |
| macOS ARM64 (M1 or later) | Unit, lint, smoke and permissive integration with the MacPorts tools above | Linux/macOS, AMD64/ARM64 binaries | Native macOS checks only; Linux container rows may skip | Not supported; use native Linux for strict evidence |

`just build-all` proves compilation of release targets; it does not execute the
foreign binaries. `just lint` type-checks and applies the Go architecture rules
to both released operating systems (`linux` and `darwin`) regardless of host.
Skipped runtime rows remain **NOT PROVEN**, not successful evidence.

## Available Commands

Run `just` to see all available commands.

`just verify` runs local lint, tagged compilation and integration tests,
including the untagged unit tests. It can use containers and network access;
optional integration evidence may skip and is reported. CI has additional
workflow-contract, smoke/fuzz and external black-box strict gates; see
[Testing conventions](testing.md) for the scope and full live prerequisites.
`just lint-fix` formats the justfile and runs nanolinter's supported fixers.
`just lint-go-deadcode` lists the Go functions no build of the tree reaches,
tests and every build tag included, with the pinned deadcode; it is not part
of `just lint`, and the pull-request gate runs it as a step of its own.

`just release-snapshot` validates the GoReleaser configuration and produces
unsigned local archives, SBOMs and checksums. Snapshot mode disables publication;
the recipe also explicitly disables signing, which snapshot mode alone does not.

`just clean` removes `bin/` and `dist/` before asking whether to clear caches.
Declining keeps the caches only. `clean-caches` clears the configured Go
module/build/test/fuzz caches, shared across checkouts by default, and the
configured golangci-lint cache. Just and the `.mise.toml` environment default
that cache to `.golangci-lint-cache/` in each checkout so linked worktrees do not replay
each other's diagnostic paths. `GOLANGCI_LINT_CACHE` overrides the location.

### Known lint gaps

The pinned nanolinter 0.8.6 drives two tools with flags they no longer
take, so both checks report rather than block:

- Commits: it passes gommitlint `--gommitconfig`, which gommitlint 0.93
  calls `--config`. Run `gommitlint validate --base-branch main` instead.
- SAST: it passes opengrep `--no-secrets-validation`, which opengrep
  removed in 1.27.1. Run `opengrep scan --config p/default .` instead.

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

### Changing a command, a flag or help text

The CLI's text is generated into files the tests hold to the code. After a
change to a command, a flag, help text or a workflow step that runs the
binary, run in this order:

```bash
just generate              # docs/cli-reference.md, the schemas, internal/cli/runby.json
just test-unit
just test-smoke            # drives the built binary; the pull-request workflow runs it
just refresh-engine-binary # last: the binary embeds the generated reference
```

Commit the generated files and the two `.engine/bin/*.sha256` lines with the
change. A guide edit under `docs/` needs only the last step.

## Path checks

- `release gpg sign-packages` signs the `.deb`, `.rpm` and `.apk` files in its
  directory, and zero matches are valid. A consumer that promises packages
  checks its outputs at the build boundary, before signing.
- `pathsafe.Relative` is the shared lexical primitive; each caller (artifact
  extraction, configuration, working directories, attachment globs, signer
  metadata) keeps its own checks and confines file access through `os.Root`.
  On Linux `internal/pathsafe` opens ancestors search-only, so only the
  destination needs read access. User-placed symlinks are refused.
- Go and Cargo builds refuse a linked `dist` directory. `just clean-build`
  accepts only the default `bin/` and `dist/`; its regression,
  `bash scripts/ci/test-clean-build.sh`, is part of `just test-unit`.
- When `pathsafe.ArtifactStaging` cannot restore replaced files, the error
  names the kept `.forge-ci-artifact-backup-*` directory; recover from it
  before retrying.

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
| Running an external binary | `safeexec.WrapError` | A non-exit failure is the tool never having run, so `ErrPrerequisite` (78), *"x not found in $PATH"*, not a crash and not an outage a retry could pass |
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

Note the sentinels are project-chosen, not BSD `sysexits` throughout:
`ErrUsage` is **2** (POSIX CLI convention), not 64.
`TestExitCodeConstants_AlignToBSDSysexits` in `internal/domain/errs` pins the
codes; [the exit-code contract](cli-black-box.md#exit-code-contract-asserted-throughout)
lists them.

`test/archguard` enforces the exec-adapter half at test time. The rest is
enforced by tests asserting the sentinel: `errors.Is(err, errs.ErrX)`, never
a bare `err != nil`, which passes whatever the classification turns into.

## Test discipline

[Testing conventions](testing.md) holds the test rules, among them
[the rules every test follows](testing.md#rules-every-test-follows).

## Workflow Refactor Checklist

When changing workflows or workflow-facing binary behaviour, follow
[workflow-design-policy.md](workflow-design-policy.md), keep public workflow
contracts stable unless the change is versioned, and run `just lint-actions`,
`just test-unit` (which runs `TestWorkflowInputContract`) and `bash -n` on
touched bootstrap scripts. Add or update Go tests for behaviour changes.

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
`internal/`, `docs/`, `go.mod`, `go.sum`): the binary embeds the user-facing
pages under `docs/` for `forge-ci docs`, so a guide edit changes it too. Run it
last, after `just generate`, since the generated CLI reference is one of those
pages. A change to workflows or tests names the same binary and needs nothing.

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

[Branch testing](runtime-images.md#branch-testing) describes how to test
reusable workflows from a branch: a reviewed workflow commit, published
runtime images and a matching CLI ref.

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
   builds signed CLI archives, publishes the GitHub release). The black-box
   and evidence gates always run; a gate that fails skips the release, and
   no repository variable waives one.

For recovery from a failed release, see
[Failed Release Recovery](failed-release-recovery.md).
