#!/usr/bin/env just --justfile

# SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
#
# SPDX-License-Identifier: CC0-1.0

# Dev-loop automation for forge-ci.
#
# Lint/security orchestration is delegated to nanolinter (pinned in
# .mise.toml, plan in nanolinter.toml). `just lint` runs the verify plan;
# every per-check recipe (`lint-yaml`, `lint-go-vet`, …) is a thin
# `*args`-forwarding wrapper around `nanolinter lint <check>`.
#
# Quick start:
#   mise trust               # allow this checkout's pinned toolchain
#   just install-dev-tools   # install nanolinter + every check tool
#   just verify-fix          # apply safe fixes, then lint and test
#   just verify              # local lint + tagged compile/integration tests
# Builds/tests write caches and may fetch modules; integration may run containers
# and download images. For inspection only, use just --show RECIPE.

# Run every recipe line under bash with pipefail; just's default `sh -cu` has none.
set shell := ["bash", "-euo", "pipefail", "-c"]
set positional-arguments
set lazy

# golangci-lint caches absolute diagnostic paths. Sharing its result cache
# between linked worktrees can replay another checkout's findings.
export GOLANGCI_LINT_CACHE := env_var_or_default("GOLANGCI_LINT_CACHE", justfile_directory() / ".golangci-lint-cache")

# Build paths and binary identity
bin := "./bin"
dist := "./dist"
executable := "forge-ci"
tool_args_help := 'Extra arguments forwarded to nanolinter; use its --help for options'
# mise installs nanolinter's Linux release only; elsewhere it is built from source.
nanolinter_hint := 'Install: just install-dev-tools; on macOS/BSD build it from source as docs/DEVELOPMENT.md describes'

# Dynamic build metadata. Mirrors .goreleaser.yml's ldflags so local builds
# report the same `--version` shape as released binaries. Backtick failures
# fall through to the sentinel values cmd/forge-ci/main.go defaults to.
version := `git describe --tags --dirty --always --abbrev=12 2>/dev/null || echo dev`
commit := `git rev-parse HEAD 2>/dev/null || echo none`
build_date := `date -u +'%Y-%m-%dT%H:%M:%SZ'`

# Color codes used by default, _require-tool and clean-build messages.
CYAN := "\\033[1;36m"
GREEN := "\\033[0;32m"
RED := "\\033[1;31m"
NC := "\\033[0m"

# ==================================================================================== #
# DEFAULT - Show available recipes
# ==================================================================================== #

[private]
default:
    @printf ' {{ CYAN }}Forge CI recipes{{ NC }}\n\n Quick start: {{ GREEN }}mise trust · just install-dev-tools · just verify{{ NC }}\n Apply fixes: just verify-fix\n Argument help: just --usage <recipe>\n ▪ main job of its group\n\n'
    @"{{ just_executable() }}" --justfile "{{ justfile() }}" --list --unsorted --list-heading ''

[arg('recipe', help='Recipe name; omit to list recipes. Prints help without running the selected job.')]
[doc('Show recipe argument help')]
[group('help')]
help recipe="":
    #!/usr/bin/env bash
    if [[ -n "$1" ]]; then
        {{ quote(just_executable()) }} --justfile {{ quote(justfile()) }} --usage="$1"
    else
        {{ quote(just_executable()) }} --justfile {{ quote(justfile()) }} --list --unsorted
    fi

# ==================================================================================== #
# SETUP - Toolchain
# ==================================================================================== #

# ▪ Install the pinned development tools; network
[group('setup')]
install-dev-tools: (_job "install-dev-tools" "tools=.mise.toml; scope=user installation; network=allowed")
    @mise install go aqua:astral-sh/uv
    @mise exec --no-deps go aqua:astral-sh/uv -- mise install

# Lists the checks in nanolinter's verify plan, the tool each one needs,
# and whether that tool is installed.
[doc('Show the lint plan and its tools')]
[group('setup')]
doctor: (_require-tool "nanolinter" nanolinter_hint)
    @nanolinter doctor

# ==================================================================================== #
# DEVELOPMENT - Composite gates
# ==================================================================================== #

# Local nanolinter lint plus tagged compilation and integration (including unit
# tests). CI separately adds workflow-contract validation, smoke/fuzz jobs and
# companion strict compatibility; release CI also requires strict integration.
[doc('▪ Local gate; container, network')]
[group('development')]
verify: (_job "verify" "mode=non-fixing; checks=lint + tagged compile-checks + unit/integration; integration may use containers and network") lint test

# Fix files, then run local checks; container, network
[doc('Fix, then verify; container, network')]
[group('development')]
verify-fix: (_job "verify-fix" "mode=apply fixes, then check; checks=lint + tagged compile-checks + unit/integration; integration may use containers and network") lint-fix (lint "--allow-dirty") test

# ==================================================================================== #
# TEST - Go test suites
# ==================================================================================== #
#
# The tier map. docs/testing.md defines the layers themselves; this says which
# recipe covers which of them, and where they overlap.
#
#   test-unit          every untagged package; Bash, jq and Git for local fixtures
#                      (including git init), no real gpg or container required
#   test-integration   the same packages with -tags=integration, so it is
#                      test-unit PLUS the tagged files, never a disjoint set
#   test-smoke         -tags=smoke over ./cmd/... alone: the built CLI driven
#                      as a black box
#   test-fuzz          the committed fuzz seed corpora, replayed as ordinary
#                      tests
#   test-forgejo       the two packages of the Forgejo layer, which test-unit
#                      and test-integration already include: the narrow loop
#                      after an edit under .forgejo/
#   test-tags-compile  no tests at all -- it vets each tag, so a tier behind
#                      one cannot stop compiling unnoticed
#
# `test` runs test-tags-compile, then
# test-integration, which covers the unit tier on its way, which is why `test`
# does not also depend on test-unit. The pull-request workflow adds test-smoke
# and test-fuzz in a second job; the release workflow swaps test-integration
# for test-integration-strict. The live tier answers to neither gate and has a
# section of its own below.
#
# The leaf tiers forward their arguments to `go test`, so -v, -run, -count and
# -failfast are arguments rather than recipes of their own:
#
#   just test-unit -v
#   just test-integration -run TestReleaseImages -failfast
#   just test-pkg ./internal/cliio -v
#
# Two recipes stay fixed. `test` takes none because just flattens a variadic
# handed to a dependency into a single string, so an umbrella that forwarded
# would pass `-run X` on as one argument; type the leaf instead.
# test-integration-strict takes none because its signature is already the
# longest here, and three more columns would cost every description on
# `just --list` three of its own.

# The integration-tag invocation also includes every untagged unit test, so
# running test-unit first would execute the unit tier twice. Optional evidence
# may skip in the local profile; test-integration reports the shortfall.
[doc('▪ Test integration; container, network')]
[group('test')]
test: (_job "test" "steps=tagged compile-checks + unit/integration; containers/network=when required") test-tags-compile test-integration

# The everyday tier and the first thing to run after an edit: no //go:build
# integration tag. Needs Bash, jq and Git for local fixtures, not real gpg or
# a container engine. Compilation may populate Go caches/download modules.
[arg('args', help='Arguments passed literally to go test, for example -v or -run TestName')]
[doc('Run unit tests')]
[group('test')]
test-unit *args: (_job "test-unit" "suite=untagged unit + local fixtures; execution=native; race=enabled")
    @bash scripts/ci/test-clean-build.sh
    @go test -shuffle=on -count=1 -race -buildvcs=false "$@" ./...

# The tier to reach for once a change touches gpg, git, a container or an
# external tool. Requires real gpg and git on PATH.
#
# A tool this tier needs and cannot find (buildah, trivy, ssh-keygen) skips the
# tests that needed it. `go test` prints that skip inside output it then throws
# away for a passing package, so the tier's "ok" said nothing about how much it
# covered. The run therefore writes what it could not prove to a file and this
# recipe prints it afterwards; use test-integration-strict to refuse the skips
# outright.
[arg('args', help='Arguments passed literally to integration-tagged go test, for example -v or -run TestReleaseImages')]
[doc('Test (skips shown); container, network')]
[group('test')]
test-integration *args: (_job "test-integration" "suites=unit + integration; execution=native plus tool/container fixtures; network=allowed") (_require-tool "gpg" "Install GnuPG 2 with your package manager; on macOS: sudo port install gnupg2")
    @printf 'Integration profile: %s (default reports missing evidence; strict rejects it)\n' "${FORGE_CI_TEST_PROFILE:-default}" >&2
    @"{{ just_executable() }}" --justfile "{{ justfile() }}" _go-test-not-proven 'Run `just test-integration-strict` to make these an error.' \
        -shuffle=on -tags=integration -count=1 -race -buildvcs=false "$@" ./...

# The gate shape of the tier above: a missing tool is an error rather than a
# tallied skip, because a gate that may skip its evidence is not a gate. The
# release workflow runs this one; run it locally when a tallied "not proven"
# line has to become a failure, or before cutting a release.
[doc('Test without skips; container, network')]
[group('test')]
test-integration-strict: (_job "test-integration-strict" "suites=unit + integration; execution=native plus tool/container fixtures; profile=strict; missing evidence=error; network=allowed") (_require-tool "gpg" "Install GnuPG 2 with your package manager; on macOS: sudo port install gnupg2")
    @FORGE_CI_TEST_PROFILE=strict go test -shuffle=on -tags=integration -count=1 -race -buildvcs=false ./...

# Builds the forge-ci binary and exercises it as a black box. Reach for it
# after changing a command, a flag, an exit code or the output a user reads.
[arg('args', help='Arguments passed literally to smoke-tagged go test, for example -v or -run TestName')]
[doc('Run the CLI smoke tier')]
[group('test')]
test-smoke *args: (_job "test-smoke" "suite=CLI black-box smoke; execution=native; scope=cmd packages")
    @go test -shuffle=on -tags=smoke -count=1 -buildvcs=false "$@" ./cmd/...

# Assert every build-tagged tier still compiles.
#
# go build and go vet skip tagged files, so a tier behind //go:build can stop
# compiling while every default check stays green. Both did: the integration tier
# had not built for weeks after the credential type changed under it, and the smoke
# tier named a subcommand that had been removed. Nothing said so, because nothing
# compiled them.
#
# Vet rather than test: this answers "does it still build", in seconds, without a
# forge or a gpg keyring. The tiers themselves stay separate because they need
# those things. On a non-Linux host the Linux reference build is vetted too, so
# Linux-only test files still compile there; nothing cross-built is executed.
[doc('Compile every build-tagged tier')]
[group('test')]
test-tags-compile: (_job "test-tags-compile" "operation=vet; tags=integration,smoke,live; targets=host + Linux reference; execution=compile-only")
    @for tag in integration smoke live; do \
        printf 'vet -tags %s\n' "$tag"; \
        go vet -tags "$tag" ./... || exit 1; \
    done
    @if [[ "$(go env GOOS)" != linux ]]; then \
        for tag in "" integration smoke live; do \
            printf 'vet GOOS=linux -tags %s\n' "${tag:-(none)}"; \
            GOOS=linux go vet -tags "$tag" ./... || exit 1; \
        done; \
    fi

# CI-safe: replays the committed seed corpora only. This is the regression
# half of fuzzing, so by design it finds nothing new -- it keeps past
# crashers fixed.
[arg('args', help='Extra go test arguments for committed-seed replay, for example -v')]
[doc('Replay committed fuzz seed corpora')]
[group('test')]
test-fuzz *args: (_job "test-fuzz" "suite=committed fuzz seeds; execution=native")
    @go test -run=Fuzz "$@" ./...

# Actively fuzzes every discovered fuzz test for `time` each. The opposite of
# test-fuzz: a run is nondeterministic and a crasher is a find, so commit the
# input it leaves under testdata/fuzz -- that IS the regression test.
[arg('time', help='Go fuzz duration per target, for example 14s; use a bounded duration')]
[doc('Fuzz every target with new inputs')]
[group('test')]
test-fuzz-all time="14s": (_job "test-fuzz-all" ("suite=generated fuzz campaign; execution=native; time=" + time + " per target"))
    #!/usr/bin/env bash
    set -euo pipefail
    FUZZTIME="$1"
    # Assigned first so a package that does not list or compile fails the run.
    packages=$(go list ./...)
    while IFS= read -r pkg; do
        targets=$(go test -list '^Fuzz' "$pkg")
        while IFS= read -r testname; do
            [[ "$testname" == Fuzz* ]] || continue
            go test "$pkg" -run=^$ -fuzz="^${testname}$" -fuzztime="$FUZZTIME"
        done <<<"$targets"
    done <<<"$packages"

# The guards over .forgejo/ (test/forgejoguard) and the layer's own shell
# (test/forgejobehavior), run against fakes and, in its engine tier, against the
# CLI built from this tree by scripts/ci/engine-binary.sh. Needs Bash, Git and
# the Go toolchain; no forge, registry or container. Reach for it after editing
# a workflow or action under .forgejo/, or a verb one of them calls.
[arg('args', help='Arguments passed literally to go test, for example -v or -run TestName')]
[doc('Test the Forgejo layer')]
[group('test')]
test-forgejo *args: (_job "test-forgejo" "suite=Forgejo layer guards + fake-backed behavior; execution=native; race=enabled")
    @"{{ just_executable() }}" --justfile "{{ justfile() }}" _go-test-not-proven '' \
        -count=1 -race -buildvcs=false "$@" ./test/forgejoguard ./test/forgejobehavior

# One package, once the failure is located:
#
#   just test-pkg ./internal/cliio
[arg('pkg', help='Go package path, for example ./internal/cliio')]
[arg('args', help='Extra go test arguments, for example -v or -failfast')]
[doc('Run tests for one package')]
[group('test')]
test-pkg pkg *args:
    @"{{ just_executable() }}" --justfile "{{ justfile() }}" _job "test-pkg" "suite=untagged tests; execution=native; package=$1; race=enabled"
    @go test -count=1 -race -buildvcs=false "$1" "${@:2}"

# One test, or a family of them, by name across every package:
#
#   just test-run TestClassify
#
# test-pkg's flags with a name filter instead of a package, so the two
# selectors run the same tests the same way. No -v and no -shuffle=on: both
# shape a sweep, and neither belongs to a run the caller has already narrowed
# to what it wants to read. Pass -v when you do want the log.
[arg('args', help='Extra go test arguments, for example -v or -failfast')]
[arg('filter', help='Go test-name regular expression, for example ^TestClassify')]
[doc('Run tests matching a name filter')]
[group('test')]
test-run filter *args:
    @"{{ just_executable() }}" --justfile "{{ justfile() }}" _job "test-run" "suite=untagged tests; execution=native; filter=$1; race=enabled"
    @go test -count=1 -race -buildvcs=false -run "$1" "${@:2}" ./...

# Merged unit + integration profile in bin/coverage.out plus bin/coverage.html.
# A report for reading, not a gate: nothing here fails on a number. Reuse the
# integration recipe so missing evidence is reported even when tests fail.
[doc('Coverage (skips shown); container, network')]
[group('test')]
test-coverage: (_job "test-coverage" ("suites=unit + integration; outputs=" + bin + "/coverage.out + " + bin + "/coverage.html; containers/network=when required"))
    #!/usr/bin/env bash
    set -euo pipefail
    rm -f {{ bin }}/coverage.out {{ bin }}/coverage.html
    mkdir -p {{ bin }}
    "{{ just_executable() }}" --justfile "{{ justfile() }}" test-integration -coverprofile={{ bin }}/coverage.out
    go tool cover -html={{ bin }}/coverage.out -o={{ bin }}/coverage.html
    printf 'Coverage written to %s and %s\n' "{{ bin }}/coverage.out" "{{ bin }}/coverage.html"

# ==================================================================================== #
# LINT - nanolinter verify plan
# ==================================================================================== #
#
# `just lint` checks the justfile format and runs the configured plan
# (nanolinter.toml). The per-check recipes forward *args so flags like
# `--disable-check`, `--lang go`, `--offline`, or `--fix` reach nanolinter
# directly:
#
#   just lint --offline                 # skip network-dependent checks
#   just lint --disable-check links     # one-off skip
#   just lint-markdown --fix            # fix markdown via nanolinter

# The justfile format check, then the full nanolinter verify plan.
[arg('args', help=tool_args_help)]
[doc('▪ Run the full lint suite; network')]
[group('lint')]
lint *args: (_require-tool "nanolinter" nanolinter_hint) _lint-release-platforms
    @"{{ just_executable() }}" --justfile "{{ justfile() }}" --fmt --check
    @go mod tidy -diff
    @nanolinter verify "$@"

# The Go umbrella: vet, staticcheck, vulncheck, golangci, wsl and format.
[arg('args', help=tool_args_help)]
[doc('Run the Go lint suite')]
[group('lint')]
lint-go *args: _lint-release-platforms
    @nanolinter lint go "$@"

# Individual Go checks (each is also part of the umbrella above).

# Run go vet
[arg('args', help=tool_args_help)]
[group('lint')]
lint-go-vet *args:
    @nanolinter lint go-vet "$@"

# Run staticcheck
[arg('args', help=tool_args_help)]
[group('lint')]
lint-go-staticcheck *args:
    @nanolinter lint go-staticcheck "$@"

# Run golangci-lint
[arg('args', help=tool_args_help)]
[group('lint')]
lint-go-golangci *args: _lint-release-platforms
    @nanolinter lint go-golangci "$@"

# Build-tagged files for either released OS must not disappear from depguard.
# Cross-lint the other OS; this is analysis, not guest execution.
[private]
_lint-release-platforms:
    #!/usr/bin/env bash
    set -euo pipefail
    "{{ just_executable() }}" --justfile "{{ justfile() }}" _require-tool golangci-lint "Install: just install-dev-tools"
    host=$(go env GOOS)
    for goos in linux darwin; do
        [[ "$goos" == "$host" ]] || GOOS="$goos" nanolinter lint go-golangci
    done

# Functions no released OS build of the tree reaches, with tests and every
# build tag included. Cross-analysis does not execute foreign binaries.
# Not a nanolinter check: the pinned deadcode, built with the pinned Go.
[doc('Find unreachable Go functions')]
[group('lint')]
lint-go-deadcode: (_require-tool "deadcode" "Install: just install-dev-tools")
    #!/usr/bin/env bash
    set -euo pipefail
    found=""
    for goos in linux darwin; do
        report=$(GOOS="$goos" deadcode -test -tags live,integration,smoke ./...)
        [[ -z "$report" ]] || found+="${goos}:"$'\n'"${report}"$'\n'
    done
    if [[ -n "${found}" ]]; then
        printf '%s\n' "${found}" >&2
        printf 'Unreachable functions: delete them, or reach them from a test that means it.\n' >&2
        exit 1
    fi

# Check Go whitespace
[arg('args', help=tool_args_help)]
[group('lint')]
lint-go-wsl *args:
    @nanolinter lint go-wsl "$@"

# Check Go formatting
[arg('args', help=tool_args_help)]
[group('lint')]
lint-go-format *args:
    @nanolinter lint go-format "$@"

# Repo-hygiene and cross-language checks.

# Check CI workflows
[arg('args', help=tool_args_help)]
[group('lint')]
lint-actions *args:
    @nanolinter lint actions "$@"

# Check commit messages
[arg('args', help=tool_args_help)]
[group('lint')]
lint-commits *args:
    @nanolinter lint commits "$@"

# Check container definitions
[arg('args', help=tool_args_help)]
[group('lint')]
lint-container *args:
    @nanolinter lint container "$@"

# Check REUSE compliance
[arg('args', help=tool_args_help)]
[group('lint')]
lint-license *args:
    @nanolinter lint license "$@"

# Check links; network
[arg('args', help=tool_args_help)]
[group('lint')]
lint-links *args:
    @nanolinter lint links "$@"

# Check Markdown files
[arg('args', help=tool_args_help)]
[group('lint')]
lint-markdown *args:
    @nanolinter lint markdown "$@"

# Check shell scripts
[arg('args', help=tool_args_help)]
[group('lint')]
lint-shell *args:
    @nanolinter lint shell "$@"

# Check shell formatting
[arg('args', help=tool_args_help)]
[group('lint')]
lint-shell-format *args:
    @nanolinter lint shell-format "$@"

# Check TOML files
[arg('args', help=tool_args_help)]
[group('lint')]
lint-toml *args:
    @nanolinter lint toml "$@"

# Check spelling
[arg('args', help=tool_args_help)]
[group('lint')]
lint-typos *args:
    @nanolinter lint typos "$@"

# Check the working tree is committed
[arg('args', help=tool_args_help)]
[group('lint')]
lint-version-control *args:
    @nanolinter lint version-control "$@"

# Reusable-workflow contract check — guards against expression-shaped
# workflow_call defaults that GHA rejects at dispatch time. Not a
# nanolinter check; lives in the binary itself.
[doc('Check workflow_call input defaults')]
[group('lint')]
lint-workflow-contracts:
    @go run ./cmd/forge-ci validate workflow input-defaults

# Check XML files
[arg('args', help=tool_args_help)]
[group('lint')]
lint-xml *args:
    @nanolinter lint xml "$@"

# Check YAML files
[arg('args', help=tool_args_help)]
[group('lint')]
lint-yaml *args:
    @nanolinter lint yaml "$@"

# ==================================================================================== #
# LINT-FIX - Auto-fix linting violations
# ==================================================================================== #

# Justfile formatting plus every fix-capable check in the nanolinter plan.
[arg('args', help=tool_args_help)]
[doc('▪ Format Just; apply nanolinter fixes')]
[group('lint-fix')]
lint-fix *args: (_require-tool "nanolinter" nanolinter_hint)
    @"{{ just_executable() }}" --justfile "{{ justfile() }}" --fmt
    @nanolinter fix "$@"

# ==================================================================================== #
# SECURITY - Scans
# ==================================================================================== #

# ▪ Run all security scans; credentials
[group('security')]
security: security-secrets security-sast security-govulncheck security-osv security-trivy-fs security-scorecard

# Scan for committed secrets
[arg('args', help=tool_args_help)]
[group('security')]
security-secrets *args:
    @nanolinter lint secrets "$@"

# Opengrep with the registry's default rules.
[arg('args', help=tool_args_help)]
[doc('Scan source code (SAST); network')]
[group('security')]
security-sast *args:
    @nanolinter lint sast "$@"

# Scan Go vulnerabilities; network
[arg('args', help=tool_args_help)]
[group('security')]
security-govulncheck *args:
    @nanolinter lint go-vulncheck "$@"

# Scan dependencies (OSV); network
[arg('args', help=tool_args_help)]
[group('security')]
security-osv *args:
    @nanolinter lint osv "$@"

# Scan files with Trivy; network
[arg('args', help=tool_args_help)]
[group('security')]
security-trivy-fs *args:
    @nanolinter lint trivy-fs "$@"

# Run OpenSSF Scorecard; credentials
[arg('args', help=tool_args_help)]
[group('security')]
security-scorecard *args:
    @nanolinter security scorecard "$@"

# ==================================================================================== #
# BUILD - Local compilation
# ==================================================================================== #
#
# These recipes are for fast iteration outside the release path. The
# release binary is produced by .goreleaser.yml (see `just release-snapshot`
# to validate that config locally). LDFLAGS here mirror goreleaser's
# so `forge-ci --version` reports the same shape either way.

# Builds the host OS/arch binary into bin/ for fast dev iteration.
[doc('▪ Build the host binary')]
[group('build')]
build: (_job "build" ("target=host architecture + configured Go OS; output=" + bin + "/" + executable))
    #!/usr/bin/env bash
    set -euo pipefail
    GOARCH=$("{{ just_executable() }}" --justfile "{{ justfile() }}" _host-goarch)
    GOOS=$(go env GOOS)
    printf 'Build target: %s/%s\n' "$GOOS" "$GOARCH" >&2
    mkdir -p {{ bin }}
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build \
        -trimpath \
        -ldflags="-s -w -X main.version={{ version }} -X main.commit={{ commit }} -X main.date={{ build_date }}" \
        -o {{ bin }}/{{ executable }} ./cmd/{{ executable }}
    printf "Built %s/%s (%s/%s)\n" "{{ bin }}" "{{ executable }}" "$GOOS" "$GOARCH"

# Linux amd64/arm64 and darwin arm64 into bin/ (release-shape sanity check).
[doc('Build every released binary')]
[group('build')]
build-all: (_job "build-all" ("targets=linux/amd64,linux/arm64,darwin/arm64; output=" + bin))
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{ bin }}
    for goos in linux darwin; do
        for goarch in amd64 arm64; do
            [[ "$goos/$goarch" == darwin/amd64 ]] && continue
            out="{{ bin }}/{{ executable }}-${goos}-${goarch}"
            printf 'Build target: %s/%s | output=%s\n' "$goos" "$goarch" "$out" >&2
            CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
                -trimpath \
                -ldflags="-s -w -X main.version={{ version }} -X main.commit={{ commit }} -X main.date={{ build_date }}" \
                -o "$out" ./cmd/{{ executable }}
            printf "Built %s\n" "$out"
        done
    done

# The engine layer tracks the checksum of its binary, not the binary:
# .engine/bin/forge-ci-linux-<amd64|arm64>.sha256, by which every job
# downloads and verifies the binary of its runner's architecture. This is the
# only way to write those lines: it builds the CLI of this tree reproducibly
# for both architectures, with the Go release go.mod names, and records the
# sha256s. Commit the files with the change they follow, or after it.
#
# TestRecordedEngineChecksumIsABuildOfThisTree fails until this is done after
# a change to Go source, go.mod or go.sum, and prints the line to record.
# docs/DEVELOPMENT.md says how the binary reaches a forge.
[doc('Record the engine binary checksum')]
[group('build')]
refresh-engine-binary:
    @bash scripts/ci/engine-binary.sh refresh

# ▪ Fail if the recorded checksums are stale (no writes; CI gate before tests)
[doc('Check the engine binary checksums are fresh')]
[group('build')]
check-engine-binary:
    @bash scripts/ci/engine-binary.sh check

# ==================================================================================== #
# DOCUMENTATION - Generated files
# ==================================================================================== #

# ▪ Generate the CLI reference and schemas
[group('documentation')]
generate: gen-cli-reference gen-artifacts-schema gen-release-images-schema gen-runby gen-testing-matrices

# Renders the urfave/cli command tree.
[doc('Generate docs/cli-reference.md')]
[group('documentation')]
gen-cli-reference:
    @go run ./cmd/gen-cli-reference > docs/cli-reference.md
    @printf "Regenerated docs/cli-reference.md\n"

# Scans the reusable workflows for the RUN BY section of --help.
[doc('Generate internal/cli/runby.json')]
[group('documentation')]
gen-runby:
    @go run ./cmd/gen-runby > internal/cli/runby.json.tmp && mv internal/cli/runby.json.tmp internal/cli/runby.json
    @printf "Regenerated internal/cli/runby.json\n"

# Regenerates matrices 1 and 2 of docs/testing-matrices.md from the demos'
# declarations, the Go tables (livetest.PreparedLanes) and the live-evidence overlay
# (test/live/evidence.txt); matrix 1b and the prose stay as written.
[doc('Generate the testing matrices sections')]
[group('documentation')]
gen-testing-matrices:
    @go run ./test/live/internal/livetest/preflight testing-matrices . > docs/testing-matrices.md.tmp && mv docs/testing-matrices.md.tmp docs/testing-matrices.md
    @printf "Regenerated docs/testing-matrices.md\n"

# Writes .forge-ci/artifacts.schema.json from the Go schema declarations.
[doc('Generate the artifacts.yml JSON schema')]
[group('documentation')]
gen-artifacts-schema:
    @go run ./cmd/gen-artifacts-schema > .forge-ci/artifacts.schema.json

# Writes docs/schemas/release-images.schema.json from the Go validator.
[doc('Generate the release-image ledger schema')]
[group('documentation')]
gen-release-images-schema:
    @go run ./cmd/gen-release-images-schema > docs/schemas/release-images.schema.json
    @printf "Regenerated docs/schemas/release-images.schema.json\n"

# Verify docs/cli-reference.md is in sync with the CLI surface.
# Thin wrapper around the TestDocsCLIReferenceInSync go-test that
# already gates PR merges via the standard test infrastructure.
[doc('Check docs/cli-reference.md is in sync')]
[group('documentation')]
check-cli-reference:
    @go test ./test/syncguard -run '^TestDocsCLIReferenceInSync$' -count=1

# Verify .forge-ci/artifacts.schema.json is in sync with the Go schema.
# Thin wrapper around the TestArtifactsSchemaInSync go-test.
[doc('Check the artifacts schema is in sync')]
[group('documentation')]
check-artifacts-schema:
    @go test ./test/syncguard -run '^TestArtifactsSchemaInSync$' -count=1

# ==================================================================================== #
# RELEASE - Pre-tag validation
# ==================================================================================== #

# Compares unsigned snapshot archives from two clean copies of committed HEAD.
[doc('Compare two clean release builds; offline')]
[group('release')]
verify-reproducible:
    @bash scripts/ci/verify-reproducible.sh

# Unsigned snapshot via goreleaser; validates .goreleaser.yml without tagging.
[doc('▪ Build a local release snapshot')]
[group('release')]
release-snapshot: (_job "release-snapshot" "steps=validate + unsigned GoReleaser snapshot; config=.goreleaser.yml; output=dist/") (_require-tool "goreleaser" "Install: mise install goreleaser  (see https://goreleaser.com/install/)")
    @goreleaser check
    @goreleaser release --clean --snapshot --skip=sign

# ==================================================================================== #
# DEPENDENCIES - Routine dependency hygiene
# ==================================================================================== #
#
# Two axes of "update":
#   1. Go modules: `upgrade-deps` (minor/patch) + `upgrade-major` (semver-major TUI)
#   2. mise tools: `upgrade-mise` (rewrites .mise.toml pins to latest, Go excepted)
#                  vs. `install-dev-tools` (reinstalls per current pin)
#
# Use `upgrade-list` first to see what's available before bumping anything.

# Run go mod tidy
[group('dependencies')]
tidy:
    @go mod tidy

# Go dependencies plus mise tool pins.
[confirm("Upgrade Go dependencies and mise tool pins?")]
[doc('▪ Upgrade dependencies and tools; network')]
[group('dependencies')]
upgrade: upgrade-deps upgrade-mise

# Direct and transitive dependencies, minor/patch only, then tidy.
[doc('Upgrade Go dependencies; network')]
[group('dependencies')]
upgrade-deps:
    @go get -u -t ./...
    @go mod tidy

# Interactive TUI for major-version Go module bumps. `upgrade-deps` will
# never cross a semver-major boundary; this is the deliberate escape hatch.
[doc('Upgrade major versions; network')]
[group('dependencies')]
upgrade-major: (_require-tool "go-mod-upgrade" "Install: go install github.com/oligot/go-mod-upgrade@latest")
    @go-mod-upgrade

# Rewrite every .mise.toml pin to the latest available version, then
# install. Review the resulting diff before committing — this is a
# different operation from `install-dev-tools`, which keeps pins and
# only installs whatever the current pin spec already allows. Only this
# repository's tools move, never the global mise config, and Go stays at
# go.mod's version: the recorded engine checksums rebuild only with it.
[doc('Upgrade mise tools; network')]
[group('dependencies')]
upgrade-mise:
    @TOOLS=$(awk '/^\[tools\]/{t=1; next} /^\[/{t=0} t && /^[^#[:space:]]/ && /=/ { sub(/[[:space:]]*=.*/, ""); gsub(/"/, ""); if ($0 != "go") print }' .mise.toml | tr '\n' ' '); \
     if [[ -n "$TOOLS" ]]; then mise upgrade --bump $TOOLS; else printf "No mise tools to upgrade\n"; fi
    @mise install

# Show available updates without modifying anything. Combines the two
# bump surfaces: pinned dev tools (mise) and Go modules.
[doc('List available updates; network')]
[group('dependencies')]
upgrade-list:
    @printf "=== mise (pinned tools) ===\n"
    @mise outdated || true
    @printf "\n=== Go modules (entries with [...] have updates available) ===\n"
    @modules=$(go list -u -m all) && { grep -E '\[' <<<"$modules" || printf "No Go module updates available.\n"; }

# ==================================================================================== #
# MAINTENANCE - Clean build outputs and caches
# ==================================================================================== #

# clean-build finishes before clean-caches asks for confirmation. Declining that
# prompt retains caches but cannot restore the already removed bin/ and dist/.
[doc('▪ Clean outputs; confirm cache deletion')]
[group('maintenance')]
clean: clean-build clean-caches

# Removes bin/ and dist/; refuses to follow symlinks or non-directories.
[doc('Remove build outputs')]
[group('maintenance')]
clean-build:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ {{ quote(bin) }} != ./bin || {{ quote(dist) }} != ./dist ]]; then
        printf 'Cleanup accepts only the default bin/dist paths; remove custom outputs explicitly.\n' >&2
        exit 2
    fi
    root=$(CDPATH='' cd -- {{ quote(justfile_directory()) }} && pwd -P)
    for dir in "$root/bin" "$root/dist"; do
        if [[ -L "$dir" || (-e "$dir" && ! -d "$dir") ]]; then
            printf "{{ RED }}✗ %s is not a regular directory; refusing to remove{{ NC }}\n" "$dir" >&2
            exit 1
        fi
    done
    rm -rf -- "$root/bin" "$root/dist"
    printf 'Removed %s and %s\n' "$root/bin" "$root/dist"

# Go's caches are shared; golangci-lint's cache is local to this checkout
# unless GOLANGCI_LINT_CACHE was explicitly supplied.
[confirm("Remove shared Go module/build/test/fuzz caches and this checkout's configured golangci-lint cache? With `just clean`, bin/ and dist/ were already removed.")]
[doc('Remove Go/lint caches; shared by default')]
[group('maintenance')]
clean-caches:
    @go clean -cache -modcache -testcache -fuzzcache
    @command -v golangci-lint >/dev/null 2>&1 && golangci-lint cache clean || true

# ==================================================================================== #
# GIT HOOK - Local pre-commit gating
# ==================================================================================== #

# The hook runs `nanolinter verify --staged-with-stash`.
[doc('▪ Install the nanolinter pre-commit hook')]
[group('hook')]
hook-install: (_require-tool "nanolinter" nanolinter_hint)
    @nanolinter hook install

# Covers both the local and the global hook.
[doc('Show whether the pre-commit hook is installed')]
[group('hook')]
hook-status: (_require-tool "nanolinter" nanolinter_hint)
    @nanolinter hook status

# Refuses when the installed hook is not nanolinter's.
[doc('Remove the nanolinter pre-commit hook')]
[group('hook')]
hook-uninstall: (_require-tool "nanolinter" nanolinter_hint)
    @nanolinter hook remove

# ==================================================================================== #
# LIVE - Live-forge conformance tier
# ==================================================================================== #

# Run the live-forge conformance tier against real Forgejo and GitLab instances.
#
# Gated by the `live` build tag, so `just test` never reaches it. It needs one
# input: a neutral live-target contract for this suite's lab account, `forgeci`,
# which the lab writes to the contract module's default path,
# $XDG_STATE_HOME/forge-lab/targets/forgeci.json (~/.local/state when unset):
#
#   just lab --account forgeci --fixtures --runners gitlab forgejo  # in Forge Lab
#   just test-live                                  # here: the complete profile
#   just test-live 'TestRegistry_.*'                # an explicitly partial profile
#
# First follow docs/live-runs.md, Running it: human host setup, durable fixtures,
# runners and Fulcio on the selected road, then fresh forgeci targets. Bare just lab
# neither selects forgeci nor prepares fixtures, runners or Fulcio. Full requires
# both GitLab and Forgejo with workflow-runs and Fulcio issuer mappings.
#
# LAB_TARGETS_FILE overrides the path. No confirmation is typed: on lab hosts
# the compiled host allowlist, the compiled `fci-` prefix and the per-account
# credential are the authorization. The preflight prints what the run will
# touch, run identity included (`forge-labctl targets explain` shows the same),
# and a missing or stale contract is refused with where it looked.
#
# Everything else -- owners, hosts, registries, runners, Fulcio -- is read from
# the contract. The product is built and checksummed by the script, outside the
# tests, and handed over by path. A credential-redacted log of each run is kept
# under the XDG state directory; the script prints its path at the end.
#
# Credentials are the producer's to retire, and the suite refuses a stale one.
# Other sessions may hold the shared account contract, and emitting it again in
# place revokes their tokens: mint a contract of your own beside it in Forge Lab
# with `LAB_TARGETS_FILE=/absolute/path/own.json just targets --account forgeci
# gitlab forgejo` (LAB_ROAD=k3s on that road) and set LAB_TARGETS_FILE to it.
# Replace the recipe shell so Just forwards signals to the cleanup-aware launcher.
[arg('scenario', help='Optional Go test-name regular expression; omit for the full live profile. Mutates disposable lab resources.')]
[doc('Live; container, network, destructive')]
[group('live')]
test-live scenario="":
    @if [ -z "$1" ]; then exec bash scripts/ci/run-live-tests.sh full; else exec bash scripts/ci/run-live-tests.sh focused "$1"; fi

# Preflight the live run the same arguments start, and write nothing to the
# lab: the contract and its CA, the credentials of the endpoints the run uses,
# the host tools and the build, then each scenario up to its admission, the
# checks it makes before its first effect, where it stops. A scenario
# that declares no admission boundary does not run and fails the preflight as
# unadmitted. It takes no live lock. Run it before Forge Lab's full-release
# wrapper, which swaps the runner in before the run's own preflight can refuse;
# a refusal here costs no runner swap.
#
#   just test-live-preflight
#   just test-live-preflight 'TestGitLabLayer_ConsumerTrial'
[arg('scenario', help='Optional Go test-name regular expression, as for test-live; omit for the full live profile')]
[doc('Live; preflight a run, no lab writes')]
[group('live')]
test-live-preflight scenario="":
    @if [ -z "$1" ]; then FORGE_CI_LIVE_PREFLIGHT=1 exec bash scripts/ci/run-live-tests.sh full; else FORGE_CI_LIVE_PREFLIGHT=1 exec bash scripts/ci/run-live-tests.sh focused "$1"; fi

# Metadata-only: run before minting a contract for a signed-base reuse trial.
[doc('Live; check restored signing inputs, no provider')]
[group('live')]
test-live-signing-inputs:
    @exec go run ./test/live/internal/livetest/preflight signing-inputs

# Build the signer or kaniko job image of this commit and publish it to the
# lab registry, under the `forgeci` account, through Forge Lab's own image
# command. It needs podman, the forgeci contract `just test-live` needs, and a
# Forge Lab checkout to run `just publish-image` in. Standard output is the one
# digest-pinned reference; everything else goes to standard error. A second
# run at the same commit reuses the local build and publishes the same digest.
#
# signer: the release scenarios run their signer jobs in the image a
# consumer's organization would build, containers/signer/Containerfile. A test
# does not build it; this recipe does, once per commit:
#
#   export FORGE_CI_LIVE_SIGNER_IMAGE="$(just test-live-image signer ~/src/forge-lab)"
#   just test-live 'TestForgejoLayer_MinimalBinaryRelease'
#
# kaniko-job: a consumer trial with CI_IMAGE_BUILDER=kaniko runs its image
# builds in containers/kaniko-job/Containerfile, the way it runs on a runner
# pool without user namespaces. A run admits the image only when its source
# label names the repository the layer runs from, so the image is labelled
# with the trial's scratch layer on the lab in the contract:
#
#   just test-live-consumer forgejo <consumer-checkout> release CI_IMAGE_BUILDER=kaniko \
#     CI_IMAGE_BUILDER_JOB_IMAGE="$(just test-live-image kaniko-job ~/src/forge-lab)"
#
# A demo trial uses other layer repositories, which this image does not name.
# A GitLab trial takes a saved archive of the local build and labels it
# itself: FORGE_CI_LIVE_GITLAB_IMAGE_BUILDER=kaniko in docs/live-runs.md.
[arg('kind', help='signer or kaniko-job')]
[arg('forge_lab', help='Absolute path to the Forge Lab checkout used to publish the image')]
[doc('Live; publish a lab signer or kaniko-job image')]
[group('live')]
test-live-image kind forge_lab budget="" cpus="" memory_mib="" compiler_jobs="":
    #!/usr/bin/env bash
    set -euo pipefail
    source scripts/ci/image-build-settings.sh
    image_build_settings '' image-build-settings "$3" "$4" "$5" "$6" || exit 2
    case "$1" in
    signer | kaniko-job) exec bash scripts/ci/publish-live-image.sh "$1" "$2" ;;
    *) printf 'kind must be signer or kaniko-job, got %q\n' "$1" >&2 && exit 2 ;;
    esac

# Try a consumer repository against this engine checkout, before the engine
# commit is published anywhere:
#
#   just test-live-consumer <forgejo|gitlab|github> <consumer-checkout> <event> [setting ...]
#
# The consumer is HEAD of a clean checkout, named by absolute path, and is
# only read. Wherever its files name the layer it pins, the copy under test
# names the layer of this checkout; docs/live-runs.md, Trying a consumer, lists
# what is rewritten and what is set. Public and private consumer cases both run
# by default; FORGE_CI_LIVE_CONSUMER_VISIBILITY=public|private selects one for
# a focused rerun. The harness checks provider visibility before pushing source
# and verifies anonymous reads are refused. On the lab, the lock, the preflight
# and the log are those of `just test-live`.
#
# forgejo, events pull-request, release, lifecycle and snapshot, on the lab.
# A setting is key=value:
#
#   <VARIABLE>=<value>   a repository variable, name in capitals. The values
#                        @layer-commit and @request-commit stand for the
#                        commit of the layer under test and for the commit a
#                        release requests, which no caller can know before.
#   token=<SECRET>       a repository secret that receives the lab account's
#                        token, for a token the consumer reads under a name of
#                        its own. No other secret value can be given.
#   workflow=<file>      the workflow to judge; pullrequest.yml or release.yml
#                        when left out.
#   version=<X.Y.Z>      the version a release requests; 0.0.1 when left out.
#   image=<reference>    release only: an image on an external registry that
#                        must verify against the trial's key afterwards.
#   interrupt-after=<job>  release only: cancel the run once a job whose name
#                        starts with this succeeds, and check nothing was
#                        released. Request the same version again to rerun.
#
#   just test-live-consumer forgejo /absolute/myapp pull-request CI_JOB_IMAGE=<digest reference>
#   just test-live-consumer forgejo /absolute/myapp release RELEASE_PLATFORMS=linux/amd64
#
# A Forgejo release also needs FORGE_CI_LIVE_SIGNER_IMAGE from
# `just test-live-image signer`, ssh-keygen and gpg.
#
# lifecycle is the linked flow in one repository, for a demo with a
# development route (a service snapshot of its one container): a pull request
# on branch development, its native merge, the service development snapshot
# dispatched on the merged main, and a signed release request from it. It
# takes the release's settings but workflow=, next-version= and
# interrupt-after=, needs what a release needs and the container/readiness
# runner, and selects its builder with CI_IMAGE_BUILDER as the other events
# do. CI_JOB_IMAGE, pinned by digest, is a nanolinter image of the demo's
# toolchain flavour (nanolinter, its check tools and the toolchain): the
# demo's lint and build jobs run in it, and the snapshot builds the demo's
# producers in it. A plain runtime image fails the lint job on the forge.
# docs/live-runs.md, Lifecycle, has the rest.
#
#   just test-live-consumer forgejo /absolute/hello-maven-service/consumer lifecycle CI_JOB_IMAGE=<nanolinter java image, digest reference> RELEASE_PLATFORMS=linux/amd64
#   just test-live-consumer forgejo /absolute/hello-cargo-container/consumer lifecycle CI_JOB_IMAGE=<nanolinter rust image, digest reference> RELEASE_PLATFORMS=linux/amd64
#
# snapshot is the lifecycle's development phase alone, over every image the
# planner plans from the demo's declaration (hello-cargo-workspace): the
# harness's service development workflow dispatched on main, judged by the
# frozen plan and each planned image's receipt; nothing is released. It takes
# CI_JOB_IMAGE and CI_IMAGE_BUILDER as the lifecycle does, and none of the
# release's settings. Snapshot-only trials probe no readiness, even for one image.
#
#   just test-live-consumer forgejo /absolute/hello-cargo-workspace/consumer snapshot CI_JOB_IMAGE=<nanolinter rust image, digest reference> CI_IMAGE_BUILDER=buildah
#
# gitlab, on the lab, with prepared Docker image archives as settings, as for
# `just demo gitlab`. pull-request runs the consumer's own MR gate and takes
# job-archive= (the MR job image) and lint-archive= (default
# FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE) alone. release runs the consumer's
# .gitlab/build-release.sh under the pinned release policy; it signs distro
# packages and needs the guarded full-release profile. It takes job-archive=
# (the non-root check image), build-archive= (the release builder) and
# signer-archive=, plus lint-archive= when the gate lints, KEY=value build
# variables and scenario=<exact consumer release test name>; see
# docs/live-runs.md. lifecycle is the linked flow in one project under
# one policy, for a demo with a development route on GitLab, a service
# snapshot of its one container from the declaration its release builds its
# images from (hello-maven-service, hello-cargo-container): a merge request on
# branch development, its native merge, the snapshot-images component on the
# merged main, and a signed release request from it. It takes the release's
# archives and settings but scenario= and next-version=; the build image is
# also the image the snapshot builds the service's producer with.
#
#   just test-live-consumer gitlab /absolute/hello-maven-service/consumer lifecycle job-archive=<path> build-archive=<path> signer-archive=<path> lint-archive=<path> version=0.2.0 job-user=65532:65532
#   just test-live-consumer gitlab /absolute/hello-cargo-container/consumer lifecycle job-archive=<path> build-archive=<path> signer-archive=<path> lint-archive=<path> version=0.2.0 job-user=65532:65532
#
# snapshot is the snapshot-images component alone, in one project under one
# policy, on a pipeline the harness requests on main, judged by the frozen
# plan and each planned image's receipt (hello-cargo-workspace,
# hello-monorepo). It takes job-archive= and, for a variant of the
# consumer's declaration, snapshot-declaration=<relative declaration>; each
# producer builds in the runtime image of its toolchain family,
# FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_<FAMILY>, and the images with the lane's
# builder, FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE or the kaniko one.
#
#   just test-live-consumer gitlab /absolute/hello-monorepo/consumer snapshot job-archive=<path>
#
# github, events pull-request, dev-release, release and lifecycle, on hosted GitHub. Runs
# inside Forge Lab's hosted session (`just github run --org ... --prefix ...
# --fixtures none` there), which writes the private context
# FORGE_LAB_GITHUB_CONTEXT names. org=<id> (required) is the numeric
# organization ID authorized for this run. HEAD of this checkout is
# force-pushed to main of <prefix>forge-ci in the organization, which builds
# its runtime images and moves v0.90.0-pre by default.
# For parallel consumers, use FORGE_CI_LIVE_ENGINE_MODE=prepare once, then
# FORGE_CI_LIVE_ENGINE_MODE=reuse; see docs/live-runs.md, Invariants from
# native trials. Release and lifecycle also need
# FORGE_CI_LIVE_GITHUB_RELEASE_TOKEN_FILE, separate from the session credential.
# The consumer's GitHub fixture workflows, pinned to that engine, run in a disposable
# <prefix><name>-<visibility>-<session> repository, deleted afterwards; the
# two visibilities run side by side, unless the release publishes a package
# the organization names apart from the repository (npm, Maven or an
# image-name), when they run one after the other. Other
# settings: workflow=<file> (not lifecycle), fixtures=<dir>, version=X.Y.Z and
# image=<name> (release and lifecycle), RELEASE_PLATFORMS=<linux/amd64 and/or
# linux/arm64> (default linux/amd64), nanolinter-image= and
# megalinter-image=<reference>. The lifecycle runs the consumer's
# pullrequest.yml, release-dev.yml and release.yml fixtures in one repository.
# docs/live-runs.md, Trying a consumer on GitHub, has the prerequisites.
[arg('repo', help='Absolute path to a clean consumer checkout, for example /absolute/myapp')]
[arg('forge', help='forgejo, gitlab or github')]
[arg('event', help='pull-request, release, lifecycle or snapshot; github pull-request, dev-release, release or lifecycle')]
[arg('settings', help='key=value settings; gitlab: job-archive=, build-archive=, signer-archive=, lint-archive=; github: org=<id>; see docs/live-runs.md')]
[doc('Live; run a consumer against this engine')]
[group('live')]
test-live-consumer forge repo event *settings:
    #!/usr/bin/env bash
    set -euo pipefail
    forge=$1 event=$3
    export FORGE_CI_LIVE_CONSUMER="$2" FORGE_CI_LIVE_CONSUMER_EVENT="$3"
    shift 3
    # Only what this command line names counts: a value left in the shell must
    # not stand in for the organization or an image archive. The lint archive
    # keeps its FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE default.
    unset FORGE_CI_LIVE_GITHUB_ORG_ID FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE \
        FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE FORGE_CI_LIVE_SIGNER_IMAGE_ARCHIVE
    settings=()
    for word in "$@"; do
        case "$forge:$word" in
        gitlab:job-archive=*) export FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE="${word#*=}" ;;
        gitlab:build-archive=*) export FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE="${word#*=}" ;;
        gitlab:signer-archive=*) export FORGE_CI_LIVE_SIGNER_IMAGE_ARCHIVE="${word#*=}" ;;
        gitlab:lint-archive=*) export FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE="${word#*=}" ;;
        github:org=*) export FORGE_CI_LIVE_GITHUB_ORG_ID="${word#*=}" ;;
        *) settings+=("$word") ;;
        esac
    done
    FORGE_CI_LIVE_CONSUMER_SETTINGS="$(printf '%s\n' "${settings[@]}")"
    export FORGE_CI_LIVE_CONSUMER_SETTINGS
    case "$forge:$event" in
    forgejo:lifecycle | forgejo:snapshot)
        # Only lifecycle probes the demo's service; snapshot judges publication
        # regardless of image count and needs no readiness configuration.
        scenario=TestForgejoLayer_Snapshot
        if [[ $event == lifecycle ]]; then
            export FORGE_CI_LIVE_SERVICE_READINESS_URL=http://127.0.0.1:8080/health FORGE_CI_LIVE_SERVICE_FIXTURE=''
            scenario=TestForgejoLayer_ConsumerLifecycle
        fi
        exec bash scripts/ci/run-live-tests.sh focused "^${scenario}$"
        ;;
    forgejo:*)
        exec bash scripts/ci/run-live-tests.sh focused '^TestForgejoLayer_ConsumerTrial$'
        ;;
    gitlab:pull-request)
        if [[ -z "${FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE:-}" || -n "${FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE:-}${FORGE_CI_LIVE_SIGNER_IMAGE_ARCHIVE:-}" ||
            ${#settings[@]} != 0 ]]; then
            printf 'a GitLab merge request takes job-archive=<path> and lint-archive=<path> alone\n' >&2
            exit 2
        fi
        export FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE="$FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE"
        unset FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE
        exec bash scripts/ci/run-live-tests.sh focused '^TestGitLabLayer_ConsumerTrial$'
        ;;
    gitlab:snapshot)
        # The trial itself refuses a setting other than snapshot-declaration=.
        if [[ -z "${FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE:-}" || -n "${FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE:-}${FORGE_CI_LIVE_SIGNER_IMAGE_ARCHIVE:-}" ]]; then
            printf 'a GitLab snapshot takes job-archive=<path> and no other archive\n' >&2
            exit 2
        fi
        exec bash scripts/ci/run-live-tests.sh focused '^TestGitLabLayer_Snapshot$'
        ;;
    gitlab:release | gitlab:lifecycle)
        for name in job build signer; do
            variable="FORGE_CI_LIVE_${name^^}_IMAGE_ARCHIVE"
            [[ -n "${!variable:-}" ]] || { printf 'a GitLab %s needs %s-archive=<path>\n' "$event" "$name" >&2; exit 2; }
        done
        if [[ $event == release ]]; then
            scenario=$(go run ./test/live/internal/livetest/preflight gitlab-consumer-scenario)
        else
            go run ./test/live/internal/livetest/preflight gitlab-lifecycle-settings
            scenario=TestGitLabLayer_ConsumerLifecycle
        fi
        exec bash scripts/ci/run-live-tests.sh focused "^${scenario}$"
        ;;
    github:*)
        [[ -n "${FORGE_CI_LIVE_GITHUB_ORG_ID:-}" ]] || { printf 'a GitHub trial needs org=<numeric organization ID>\n' >&2; exit 2; }
        if [[ -n "${FORGE_LAB_GITHUB_CONTEXT:-}" && "$FORGE_LAB_GITHUB_CONTEXT" != /* ]]; then
            export FORGE_LAB_GITHUB_CONTEXT="{{ invocation_directory() }}/$FORGE_LAB_GITHUB_CONTEXT"
        fi
        scenario=TestGitHubLayer_ConsumerTrial
        [[ $event != lifecycle ]] || scenario=TestGitHubLayer_ConsumerLifecycle
        GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off exec go test -tags=live -count=1 -buildvcs=false -timeout=5h -parallel=2 -v \
            -run="^${scenario}$" ./test/live/githubconsumer/
        ;;
    gitlab:*)
        printf 'a GitLab trial event is pull-request, release, lifecycle or snapshot, got %q\n' "$event" >&2
        exit 2
        ;;
    *)
        printf 'forge must be forgejo, gitlab or github, got %q\n' "$forge" >&2
        exit 2
        ;;
    esac

# Show a consumer and keep it: a demo. `just demo-list` finds demos and
# `just demo-delete` removes one.
#
# The consumer's release, then a pull or merge request on the released main,
# kept as demo-<name>: demo/demo-<name> on the lab (account `demo`), or
# <prefix>demo-<name> in the GitHub test organization. Nothing is deleted, an
# existing demo is refused, the consumer's history is pushed, and the
# repository secrets the run set are deleted when it ends.
#
# The consumer is the built-in demo test/live/fixtures/consumers/<name>, committed as one
# commit, or any clean checkout named by checkout=/absolute/path. Settings
# are the release trial's, plus per forge:
#
#   forgejo  version=<X.Y.Z>, CI_JOB_IMAGE= and other repository variables
#   gitlab   job-archive=, build-archive=, signer-archive= (the image archives;
#            lint-archive= too when the merge-request gate lints,
#            job-archive-<name>= for each $CI_JOB_IMAGE_<NAME> it checks in,
#            buildah-archive= when the release builds images),
#            job-user=<uid[:gid]> for the release's job-image jobs,
#            version=<X.Y.Z>, version-bump=maven and KEY=value variables for
#            the release build
#   github   org=<numeric organization ID>, run inside Forge Lab's
#            `just github run ... -- just --justfile <this> demo github ...`
#
#   just lab --account demo --runners gitlab forgejo    # in Forge Lab, first
#   just demo forgejo hello-maven version=0.2.0 CI_JOB_IMAGE=<reference>
#   just demo gitlab hello-maven-service checkout=/abs/myapp \
#     job-archive=/abs/job.tar build-archive=/abs/build.tar signer-archive=/abs/signer.tar
#   just targets-revoke ~/.local/state/forge-lab/targets/demo.json   # when over
#
# docs/live-runs.md, Showing a consumer, lists the images each forge needs.
[arg('name', help='Demo name: a directory of test/live/fixtures/consumers, or the name for checkout=')]
[arg('forge', help='forgejo, gitlab or github')]
[arg('settings', help='checkout=/abs for another consumer; release-trial key=value settings; gitlab: job-archive=, build-archive=, signer-archive=, lint-archive=, job-archive-<name>=, buildah-archive=; github: org=<id>')]
[doc('Live; show a consumer and keep it')]
[group('demo')]
demo forge name *settings:
    #!/usr/bin/env bash
    set -euo pipefail
    forge=$1 name=$2
    shift 2
    case "$forge" in
    forgejo) scenario='^TestForgejoLayer_ConsumerTrial$' ;;
    gitlab) scenario='^TestGitLabRelease_ConsumerTrial$' ;;
    github) scenario='^TestGitHubLayer_ConsumerTrial$' ;;
    *) printf 'forge must be forgejo, gitlab or github, got %q\n' "$forge" >&2 && exit 2 ;;
    esac
    if [[ ! "$name" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
        printf 'demo name must be lowercase letters, digits and hyphens, got %q\n' "$name" >&2
        exit 2
    fi
    checkout='' settings=()
    for word in "$@"; do
        case "$forge:$word" in
        *:checkout=*) checkout="${word#*=}" ;;
        gitlab:job-archive=*) export FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE="${word#*=}" ;;
        gitlab:build-archive=*) export FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE="${word#*=}" ;;
        gitlab:signer-archive=*) export FORGE_CI_LIVE_SIGNER_IMAGE_ARCHIVE="${word#*=}" ;;
        gitlab:lint-archive=*) export FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE="${word#*=}" ;;
        gitlab:buildah-archive=*) export FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE="${word#*=}" ;;
        gitlab:job-archive-*=*)
            key=${word%%=*}
            key=${key#job-archive-}
            if [[ ! "$key" =~ ^[a-z][a-z0-9]*$ ]]; then
                printf 'want job-archive-<name>=, name lowercase letters and digits, got %q\n' "$word" >&2
                exit 2
            fi
            export "FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE_${key^^}=${word#*=}"
            ;;
        github:org=*) export FORGE_CI_LIVE_GITHUB_ORG_ID="${word#*=}" ;;
        *) settings+=("$word") ;;
        esac
    done
    # A built-in demo becomes a repository of one commit, which the trial
    # reads like any consumer checkout and the run removes when it ends.
    if [[ -z "$checkout" ]]; then
        if [[ ! -d test/live/fixtures/consumers/"$name"/consumer ]]; then
            printf 'no demo %q in test/live/fixtures/consumers; name another consumer with checkout=/absolute/path\n' "$name" >&2
            exit 2
        fi
        checkout=$(mktemp -d "${TMPDIR:-/tmp}/forge-ci-demo.XXXXXXXX")
        trap 'rm -rf -- "$checkout"' EXIT
        bash scripts/ci/demo-checkout.sh "$name" "$checkout"
    fi
    export FORGE_CI_LIVE_DEMO="$name" FORGE_CI_LIVE_CONSUMER="$checkout" FORGE_CI_LIVE_CONSUMER_EVENT=release
    FORGE_CI_LIVE_CONSUMER_SETTINGS="$(printf '%s\n' "${settings[@]}")"
    export FORGE_CI_LIVE_CONSUMER_SETTINGS
    if [[ "$forge" != github ]]; then
        bash scripts/ci/run-live-tests.sh focused "$scenario"
        exit
    fi
    if [[ -n "${FORGE_LAB_GITHUB_CONTEXT:-}" && "$FORGE_LAB_GITHUB_CONTEXT" != /* ]]; then
        export FORGE_LAB_GITHUB_CONTEXT="{{ invocation_directory() }}/$FORGE_LAB_GITHUB_CONTEXT"
    fi
    GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -tags=live -count=1 -buildvcs=false -timeout=5h -v \
        -run="$scenario" ./test/live/githubconsumer/

# Build one runtime image of this tree for this machine's architecture and
# save it as <directory>/forge-ci-runtime-<target>.tar, the archive a GitLab
# trial takes as job-archive=, job-archive-<name>= or build-archive=. Its engine is the build this
# tree records, checked against .engine/bin/forge-ci-linux-<arch>.sha256 as
# the published images are, so GitLab's build components run the candidate's
# engine, as Forgejo's do, and not whatever a published image carries.
# Prints the archive's path.
[arg('target', help='Runtime target of containers/runtime/images.json, e.g. go')]
[arg('directory', help='Absolute directory to write the archive to')]
[doc('Build a candidate runtime image archive; network')]
[group('build')]
runtime-image-archive target directory budget="" cpus="" memory_mib="" compiler_jobs="":
    #!/usr/bin/env bash
    set -euo pipefail
    target=$1 directory=$2
    source scripts/ci/image-build-settings.sh
    declare -a image_build_args
    image_build_settings image_build_args image-build-settings "$3" "$4" "$5" "$6" || exit 2
    arch=$("{{ just_executable() }}" --justfile "{{ justfile() }}" _host-goarch)
    if ! jq -e --arg target "$target" --arg arch "$arch" \
        'any(.[]; .target == $target and (.archs | index($arch)))' containers/runtime/images.json >/dev/null; then
        printf 'no %s runtime target %q in containers/runtime/images.json\n' "$arch" "$target" >&2
        exit 2
    fi
    if [[ "$directory" != /* ]]; then
        printf 'the archive directory must be an absolute path, got %q\n' "$directory" >&2
        exit 2
    fi
    # The image is keyed by the commit, as test-live-image's are: the lab's
    # GitLab runner shares this host's image store and starts a job image it
    # finds here by its local name, so a build for another commit must not
    # take that name. Its inputs must be that commit's; the engine is checked
    # against .engine/bin below.
    inputs=(containers/runtime scripts/bootstrap templates .engine/bin)
    if [[ -n "$(git status --porcelain --untracked-files=all -- "${inputs[@]}")" ]]; then
        printf 'the %s runtime image inputs have uncommitted changes; commit or stash them first:\n' "$target" >&2
        git status --short --untracked-files=all -- "${inputs[@]}" >&2
        exit 2
    fi
    commit=$(git rev-parse --verify HEAD)
    image="localhost/forge-ci-runtime-$target:$commit"
    # The image's whole build context, and nothing of the tree beyond it.
    context=$(mktemp -d "${TMPDIR:-/tmp}/forge-ci-runtime-image.XXXXXXXX")
    trap 'rm -rf -- "$context"' EXIT
    mkdir -p -- "$context/scripts" "$context/cli-prebuilt"
    bash scripts/ci/engine-binary.sh build-checked "$arch" "$context/cli-prebuilt/forge-ci" >&2
    cp -R -- scripts/bootstrap "$context/scripts/bootstrap"
    cp -R -- templates "$context/templates"
    podman build "${image_build_args[@]}" --file containers/runtime/Containerfile --target "$target" --tag "$image" "$context" >&2
    mkdir -p -- "$directory"
    archive="$directory/forge-ci-runtime-$target.tar"
    rm -f -- "$archive"
    podman save --quiet --format docker-archive --output "$archive" "$image"
    printf '%s\n' "$archive"

# Build the Buildah job image of this tree, containers/buildah-job, for this
# machine's architecture and save it as <directory>/forge-ci-buildah-job.tar:
# the archive a GitLab trial takes as buildah-archive=, the release's
# builder-job-image, and as build-archive= for a build that runs only
# `forge-ci container build`. Prints the archive's path.
[arg('directory', help='Absolute directory to write the archive to')]
[doc('Build the Buildah job image archive; network')]
[group('build')]
buildah-job-image-archive directory budget="" cpus="" memory_mib="" compiler_jobs="":
    #!/usr/bin/env bash
    set -euo pipefail
    directory=$1
    source scripts/ci/image-build-settings.sh
    declare -a image_build_args
    image_build_settings image_build_args image-build-settings "$2" "$3" "$4" "$5" || exit 2
    if [[ "$directory" != /* ]]; then
        printf 'the archive directory must be an absolute path, got %q\n' "$directory" >&2
        exit 2
    fi
    # Keyed by the commit, as the runtime image archive is.
    if [[ -n "$(git status --porcelain --untracked-files=all -- containers/buildah-job)" ]]; then
        printf 'the Buildah job image inputs have uncommitted changes; commit or stash them first:\n' >&2
        git status --short --untracked-files=all -- containers/buildah-job >&2
        exit 2
    fi
    commit=$(git rev-parse --verify HEAD)
    image="localhost/forge-ci-buildah-job:$commit"
    podman build "${image_build_args[@]}" --file containers/buildah-job/Containerfile \
        --label "org.opencontainers.image.revision=$commit" \
        --tag "$image" containers/buildah-job >&2
    mkdir -p -- "$directory"
    archive="$directory/forge-ci-buildah-job.tar"
    rm -f -- "$archive"
    podman save --quiet --format docker-archive --output "$archive" "$image"
    printf '%s\n' "$archive"

# Build the kaniko job image of this tree, containers/kaniko-job, for this
# machine's architecture and save it as <directory>/forge-ci-kaniko-job.tar:
# the archive a trial pushes and labels for the layer it runs from
# (FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE). Prints the archive's path.
[arg('directory', help='Absolute directory to write the archive to')]
[doc('Build the kaniko job image archive; network')]
[group('build')]
kaniko-job-image-archive directory budget="" cpus="" memory_mib="" compiler_jobs="":
    #!/usr/bin/env bash
    set -euo pipefail
    directory=$1
    source scripts/ci/image-build-settings.sh
    declare -a image_build_args
    image_build_settings image_build_args image-build-settings "$2" "$3" "$4" "$5" || exit 2
    if [[ "$directory" != /* ]]; then
        printf 'the archive directory must be an absolute path, got %q\n' "$directory" >&2
        exit 2
    fi
    # Keyed by the commit, as the runtime image archive is.
    if [[ -n "$(git status --porcelain --untracked-files=all -- containers/kaniko-job)" ]]; then
        printf 'the kaniko job image inputs have uncommitted changes; commit or stash them first:\n' >&2
        git status --short --untracked-files=all -- containers/kaniko-job >&2
        exit 2
    fi
    commit=$(git rev-parse --verify HEAD)
    image="localhost/forge-ci-kaniko-job:$commit"
    podman build "${image_build_args[@]}" --file containers/kaniko-job/Containerfile \
        --label "org.opencontainers.image.revision=$commit" \
        --tag "$image" containers/kaniko-job >&2
    mkdir -p -- "$directory"
    archive="$directory/forge-ci-kaniko-job.tar"
    rm -f -- "$archive"
    podman save --quiet --format docker-archive --output "$archive" "$image"
    printf '%s\n' "$archive"

# List the demos `just demo` made: each demo with its repositories, IDs and
# links. On the lab it reads lab account demo's contract; on GitHub it runs
# inside Forge Lab's `just github run` and takes org=<id>.
[arg('forge', help='forgejo, gitlab or github')]
[arg('settings', help='github: org=<numeric organization ID>')]
[doc('Live; list the kept demos')]
[group('demo')]
demo-list forge *settings:
    #!/usr/bin/env bash
    set -euo pipefail
    # A relative context path is the caller's; just runs this in the justfile's directory.
    if [[ -n "${FORGE_LAB_GITHUB_CONTEXT:-}" && "$FORGE_LAB_GITHUB_CONTEXT" != /* ]]; then
        export FORGE_LAB_GITHUB_CONTEXT="{{ invocation_directory() }}/$FORGE_LAB_GITHUB_CONTEXT"
    fi
    GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off exec go run -buildvcs=false ./test/live/internal/livetest/demoadmin list "$@"

# Delete one demo for good. It shows exactly what goes, then asks for the
# demo's name (or takes confirm=<name>); each repository is deleted by the ID
# it was listed with. When the lab has no demo left it says how to retire the
# account's token in Forge Lab.
[arg('name', help='The demo, as `just demo-list` names it, for example hello-maven-service')]
[arg('forge', help='forgejo, gitlab or github')]
[arg('settings', help='confirm=<name> instead of typing it; github: org=<numeric organization ID>')]
[doc('Live; delete a kept demo')]
[group('demo')]
demo-delete forge name *settings:
    #!/usr/bin/env bash
    set -euo pipefail
    # A relative context path is the caller's; just runs this in the justfile's directory.
    if [[ -n "${FORGE_LAB_GITHUB_CONTEXT:-}" && "$FORGE_LAB_GITHUB_CONTEXT" != /* ]]; then
        export FORGE_LAB_GITHUB_CONTEXT="{{ invocation_directory() }}/$FORGE_LAB_GITHUB_CONTEXT"
    fi
    GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off exec go run -buildvcs=false ./test/live/internal/livetest/demoadmin delete "$@"

# Minimal preparation checks: public test snapshots and a temporary CLI asset,
# existing toolchain images, throwaway SSH keys and read-only package inspection.
[arg('org', help='Numeric GitHub organization ID authorized by Forge Lab')]
[arg('token_file', help='Absolute private Packages PAT path; read-only retention preview only')]
[doc('Live GitHub; minimal preparation and retention')]
[group('live')]
test-live-github-minimal org token_file:
    #!/usr/bin/env bash
    set -euo pipefail
    state=$(mktemp -d "${TMPDIR:-/tmp}/forge-ci-minimal.XXXXXXXX")
    trap 'rm -rf -- "$state"' EXIT
    GOTOOLCHAIN=local CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$state/forge-ci" ./cmd/forge-ci
    FORGE_CI_LIVE_GITHUB_ORG_ID="$1" FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE="$2" FORGE_CI_LIVE_BIN="$state/forge-ci" \
        GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -tags=live -count=1 -buildvcs=false -timeout=75m -v \
        -run='^TestGitHubMinimalPreparation$' ./test/live/internal/livetest/

# API-only prerelease admission/retry checks; no engine image build or runner jobs.
[arg('org', help='Numeric GitHub organization ID explicitly authorized by the Forge Lab session')]
[doc('Live GitHub; prerelease API round trips')]
[group('live')]
test-live-github-release-api org:
    FORGE_CI_LIVE_GITHUB_ORG_ID="$1" GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -tags=live -count=1 -timeout=10m -v -run='^TestGitHubRelease_PrereleaseAPI$' ./test/live/githubconsumer/

# Real scheduling semantics plus the actual CLI binary-publication workflow.
# Uses separate disposable repositories; no engine image build is needed.
[arg('org', help='Numeric GitHub organization ID explicitly authorized by the Forge Lab session')]
[doc('Live GitHub; self-release scheduling and assets')]
[group('live')]
test-live-github-self-release org:
    FORGE_CI_LIVE_GITHUB_ORG_ID="$1" GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -tags=live -count=1 -parallel=2 -buildvcs=false -timeout=75m -v -run='^TestGitHubSelfRelease_Binary(Scheduling|Assets)$' ./test/live/internal/livetest/

# Exercise signed native GitLab release jobs with caller-prepared image archives.
[arg('job', help='Path to the prepared Docker archive for the release job image')]
[arg('signer', help='Path to the prepared Docker archive for the signer image')]
[doc('Live GitLab; minimal signed release')]
[group('live')]
test-live-gitlab-release job signer:
    @FORGE_CI_LIVE_JOB_IMAGE_ARCHIVE="$1" FORGE_CI_LIVE_SIGNER_IMAGE_ARCHIVE="$2" \
        exec bash scripts/ci/run-live-tests.sh focused '^TestGitLabRelease_MinimalSignedArtifacts$'

# Check first-push protection with a real clean consumer; no job images needed.
[arg('repo', help='Absolute path to a clean reviewed consumer checkout')]
[doc('Live GitLab; consumer branch protection')]
[group('live')]
test-live-gitlab-branch-protection repo:
    @FORGE_CI_LIVE_CONSUMER="$1" exec bash scripts/ci/run-live-tests.sh focused '^TestGitLabRelease_ConsumerBranchProtection$'

# This specifically probes Buildah RUN capability on the privileged runner.
[arg('build', help='Prepared Docker archive of the reviewed Buildah-capable build image; requires the privileged runner')]
[doc('Live GitLab; Buildah RUN capability')]
[group('live')]
test-live-gitlab-buildah build:
    @FORGE_CI_LIVE_BUILD_IMAGE_ARCHIVE="$1" exec bash scripts/ci/run-live-tests.sh focused '^TestGitLabRelease_BuildImageCapability$'

# Observe the ready marker in GitLab's job trace, then interrupt this launcher.
[doc('Live GitLab; interrupt an active job (exit 130)')]
[group('live')]
test-live-gitlab-interrupted-job:
    @FORGE_CI_LIVE_INTERRUPT_PROBE=gitlab-workflow exec bash scripts/ci/run-live-tests.sh focused '^TestGitLabWorkflow_InterruptedJob$'

# Prepare a directory for live runs: everything the named lanes take, built
# from HEAD once.
#
# Publishes the signer image and each named nanolinter job image (package
# nanolinter-<flavor>) with Forge Lab, saves those images, the Buildah and
# kaniko job images and each runtime image the named lanes' cases and the
# GitLab gates of their demos take as archives, exports every demo of
# test/live/fixtures/consumers as a one-commit checkout under <dir>/demos, and writes
# <dir>/prepared.env: image references, archive paths and PREPARED_COMMIT, no
# secrets. A dirty checkout or a stale engine checksum is refused. A lane is
# forgejo or gitlab, every lane of the forge, or one lane of
# livetest.PreparedLanes by name; gitlab includes the Android lane, whose
# gates lint in job-image=java:<image> and build in the Android runtime image.
#
#   just test-live-prepare ~/src/forge-lab ~/prepared/k3s \
#     k3s-private-forgejo k3s-private-gitlab \
#     job-image=go:localhost/nanolinter-go:dev job-image=java:localhost/nanolinter-java:dev
#
# docs/live-runs.md, Prepare and run, has the whole run.
[arg('dir', help='Absolute prepared directory: new, empty, or an earlier one to replace')]
[arg('lanes', help='forgejo, gitlab or a lane name per lane to prepare for, and optional job-image=<flavor>:<local image>, published as nanolinter-<flavor>')]
[arg('forge_lab', help='Absolute path to the Forge Lab checkout used to publish the images')]
[doc('Live; prepare the images and demos of this commit')]
[group('live')]
test-live-prepare forge_lab dir +lanes:
    @exec bash scripts/ci/prepare-live.sh "$@"

# Run cases of the Go tables (livetest.ForgejoPreparedCases,
# GitLabPreparedCases) on the demos and images of a prepared directory. A
# case is the subtest <test>/<lane>/<demo>/<event>/<builder>/<visibility>, so
# the `go test -run` pattern selects a whole lane or one cell;
# livetest.PreparedLanes lists the lanes and the scenarios each runs beside
# its cases. Visibility and administrator inputs are the caller's
# (FORGE_CI_LIVE_CONSUMER_VISIBILITY, FORGE_CI_LIVE_ADMIN_<CHECK>). Refused
# unless HEAD is the prepared commit with nothing on top. set=small or
# set=medium runs only the cases of that run set among those the pattern
# selects, each with the visibility it has in the set. after=small or
# after=medium, a set below set=, runs what the set runs and that set did
# not; small, medium after small and full after medium on one commit are
# one full run. docs/live-runs.md, Run sets.
#
#   FORGE_CI_LIVE_CONSUMER_VISIBILITY=private just test-live-prepared ~/prepared/k3s forgejo '^TestForgejoLayer_ConsumerTrial$/^private$'
#   just test-live-prepared ~/prepared/k3s forgejo '^TestForgejoLayer_ConsumerLifecycle$/^lifecycle$/^hello-maven-service$/^lifecycle$/^kaniko$/^public$'
#   just test-live-prepared ~/prepared/k3s forgejo '^TestForgejoLayer_' set=small
#   just test-live-prepared ~/prepared/k3s forgejo '^TestForgejoLayer_' set=medium after=small
[arg('dir', help='Absolute prepared directory from just test-live-prepare')]
[arg('run', help='go test -run pattern: <test>/<lane>[/<demo>/<event>/<builder>/<visibility>]')]
[arg('set', help='set=small, set=medium or set=full: the run set whose cases run')]
[arg('after', help='after=small or after=medium, a set below set=: run only what that set did not')]
[arg('forge', help='forgejo or gitlab')]
[doc('Live; run prepared demo cases from the Go tables; network, destructive')]
[group('live')]
test-live-prepared dir forge run set="set=full" after="":
    @FORGE_CI_LIVE_PREPARED="$1" FORGE_CI_LIVE_PROVIDER="$2" FORGE_CI_LIVE_SET="${4#set=}" FORGE_CI_LIVE_AFTER="${5#after=}" exec bash scripts/ci/run-live-tests.sh focused "$3"

# Print the live rows the commits after <base> can affect, and run nothing:
# for each path of `git diff <base>..HEAD`, the rows of the table
# livetest.liveEffects (test/live/internal/livetest/affectedrows.go). On
# forgejo and gitlab a line is a pattern for `just test-live-prepared`; on
# github it is `<demo> <event> [setting ...]`, a cell of livetest.GitHubCells
# that `just test-live-consumer github` runs. A path the table has no entry for can affect every row, and
# standard error names each path that does. The full matrix still runs before
# a release; docs/live-runs.md, Rows a change can affect.
#
# With set=small or set=medium the rows are those of that run set alone; give
# `just test-live-prepared` the same set.
#
#   just test-live-affected 1fcb2a12 forgejo
#   just test-live-affected 1fcb2a12 forgejo set=small
[arg('set', help='set=small, set=medium or set=full: the run set whose rows are printed')]
[arg('base', help='The commit whose rows are already run; the change is base..HEAD')]
[arg('forge', help='forgejo, gitlab or github')]
[doc('Print the live rows the commits since a base can affect; runs nothing')]
[group('live')]
test-live-affected base forge set="set=full":
    #!/usr/bin/env bash
    set -euo pipefail
    # A rename is a change of both paths. The diff's own failure ends the
    # recipe, so a mistyped commit never reads as a change of nothing.
    paths=$(git diff --name-only --no-renames "$1..HEAD" --)
    go run ./test/live/internal/livetest/preflight affected-rows "$2" "${3#set=}" <<<"$paths"

# Run the hosted GitHub cells of a run set (livetest.GitHubCells), one after
# another, on the demos of a prepared directory: the engine is prepared once
# (FORGE_CI_LIVE_ENGINE_MODE=prepare), then each cell is one `just
# test-live-consumer github` call that reuses it, with the cell's settings,
# the settings given here (one lint image a flavor the cells pin, as
# nanolinter-image=<flavor>:<digest reference>) and, in a
# set below the full one, the cell's one visibility. A cell with both runs
# them side by side, unless its release publishes an organization package
# (npm, Maven or an image-name), which the two would share. after=small or
# after=medium right after the set runs the cells and visibilities of the
# set that the set after did not. Run it inside Forge
# Lab's hosted session, as a single trial: it starts no session. Before
# anything runs it refuses a setting a cell does not take, a lint flavor
# with no image, and a set with a
# release or a lifecycle when FORGE_CI_LIVE_GITHUB_RELEASE_TOKEN_FILE is
# unset. The run directory, under the live runs' root, gets a log of the
# engine and of each cell, and cells.txt: the run set and, of a delta run,
# the set after, then a line a cell: passed, failed or not-covered. No cell
# is run again, and any that did not pass fails the recipe. docs/live-runs.md, Hosted GitHub cells.
#
#   just test-live-github-cells ~/prepared/k3s <org-id> set=medium nanolinter-image=java:<digest reference> nanolinter-image=go:<digest reference> ...
#   just test-live-github-cells ~/prepared/k3s <org-id> set=full after=medium nanolinter-image=java:<digest reference> ...
[arg('dir', help='Absolute prepared directory from just test-live-prepare')]
[arg('org', help='Numeric ID of the organization the hosted session authorizes')]
[arg('set', help='set=small, set=medium or set=full: the run set whose cells run')]
[arg('settings', help='after=small or after=medium first for a delta run, then nanolinter-image=<flavor>:<digest reference> for each lint flavor the cells pin, and key=value settings for every cell: megalinter-image=, RELEASE_PLATFORMS=')]
[doc('Live; run the hosted GitHub cells of a run set in the hosted session')]
[group('live')]
test-live-github-cells dir org set="set=full" *settings:
    @JUST="{{ just_executable() }}" exec bash scripts/ci/run-github-cells.sh "$@"

# ==================================================================================== #
# INTERNAL
# ==================================================================================== #

_job name details:
    #!/usr/bin/env bash
    printf '== Job: %s | host=%s/%s\n   %s\n' "$1" "$(uname -s)" "$(uname -m)" "$2" >&2

# Runs `go test ARGS`, then lists the tests that reported missing evidence
# ("not proven"), followed by HINT when it is not empty. Keeps go test's status.
[private]
_go-test-not-proven hint *args:
    #!/usr/bin/env bash
    set -uo pipefail
    hint=$1
    shift
    mkdir -p {{ bin }}
    rm -f {{ bin }}/not-proven.txt
    FORGE_CI_TEST_NOT_PROVEN_FILE={{ quote(absolute_path(bin / "not-proven.txt")) }} go test "$@"
    status=$?
    if [[ -s {{ bin }}/not-proven.txt ]]; then
        printf '\n'
        cat {{ bin }}/not-proven.txt
        [[ -z "$hint" ]] || printf '%s\n' "$hint"
    fi
    exit "$status"

# Fail fast with an actionable hint when a required external tool is missing.
# Use as a recipe prerequisite: `release-snapshot: (_require-tool "goreleaser" "Install: …")`.
_require-tool tool hint="":
    #!/usr/bin/env bash
    if command -v "$1" >/dev/null 2>&1; then
        exit 0
    fi
    printf "{{ RED }}✗ %s not found{{ NC }}\n" "$1" >&2
    if [[ -n "${2:-}" ]]; then
        printf "  %s\n" "$2" >&2
    fi
    exit 1

# Print the host's Go GOARCH (amd64 / arm64); exit non-zero on unsupported arches.
# Use as `GOARCH=$("{{ just_executable() }}" --justfile "{{ justfile() }}" _host-goarch)`.
_host-goarch:
    #!/usr/bin/env bash
    case "$(uname -m)" in
        x86_64|amd64) printf "amd64" ;;
        aarch64|arm64) printf "arm64" ;;
        *) printf "{{ RED }}✗ Unsupported host architecture: %s{{ NC }}\n" "$(uname -m)" >&2; exit 1 ;;
    esac
