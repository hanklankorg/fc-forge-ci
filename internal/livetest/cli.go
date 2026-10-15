// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// Some of what this tier must prove is not observable from an adapter.
//
// A capability the forge lacks is supposed to *degrade*: SARIF upload emits a
// notice and exits 0, keyless signing emits a warning naming the forge,
// provenance refuses. Every one of those decisions is taken above the adapter,
// in the use case and the CLI, so a scenario that only calls roles cannot see
// any of them — it would be asserting on the layer that does not decide.
//
// So the kit drives the shipped binary too, against the same guarded target.
// Which one a scenario reaches for is not a style choice: assert on a role when
// the claim is about forge state, and on the binary when the claim is about
// what the user is told and what the process exits with.

// binaryEnv names the built product under test. It is outside the
// FORGE_CI_* namespace on purpose: that namespace is the product's own
// flags, and a variable that only points a test suite at a binary must not look
// like one of them.
const binaryEnv = "RC_LIVE_BIN"

// Run is one invocation of the product binary.
type Run struct {
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
}

// Combined is stdout and stderr together, for assertions that do not care which
// stream carried the message. Prefer asserting on the specific stream: which one
// a message lands on is part of the CLI contract.
func (r Run) Combined() string { return r.Stdout + r.Stderr }

// Binary is the product under test, built and checksummed outside the tests and
// handed over by path. It is never built here: a suite that compiles its own
// binary proves something about the source it happened to see, not about the
// artifact the release flow produces.
func Binary(tb TB) string {
	tb.Helper()

	path := os.Getenv(binaryEnv)
	if !filepath.IsAbs(path) {
		tb.Fatalf("livetest: %s must be the absolute path of the built product; run this through `just test-live`", binaryEnv)
	}

	// G304: the path is the built product handed over by the recipe, and it is
	// required to be absolute and executable before anything runs it.
	info, err := os.Stat(path) //nolint:gosec
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		tb.Fatalf("livetest: %s is not an executable file: %s", binaryEnv, path)
	}

	return path
}

// LayerBinary is the reproducible delivery build supplied by the live launcher,
// distinct from the unstamped runner probe binary. Publication verifies it
// against the checksum recorded by the layer before uploading any bytes.
func LayerBinary(tb TB) string {
	tb.Helper()

	path := os.Getenv(layerBinaryEnv)
	if !filepath.IsAbs(path) {
		tb.Fatalf("livetest: %s must name the absolute path of the delivery build; use just test-live", layerBinaryEnv)
	}

	return path
}

// CLI runs the product against a guarded target and returns what the user would
// have seen. A non-zero exit is a result, not a failure: most of what this tier
// asserts about degradation is an exit code plus a message.
func CLI(tb TB, target Target, repo string, args ...string) Run {
	tb.Helper()

	return CLIIn(tb, target, repo, RunOptions{}, args...)
}

// usageErrorMarkers are how the CLI framework reports that it could not parse
// the invocation at all.
//
//nolint:gochecknoglobals // read-only table.
var usageErrorMarkers = []string{
	"flag provided but not defined",
	"for available flags",
	"unknown subcommand",
	"unknown command",
}

// RunOptions adjusts one invocation. Both fields exist because the product is
// right to care about them: several verbs record paths that a consumer resolves
// later, so they insist those paths are relative, which only means something
// against a known working directory.
type RunOptions struct {
	// Dir is the working directory. Empty means the test's own.
	Dir string

	// Env adds to the closed environment, merged last so a scenario can be
	// explicit about what the product sees.
	Env map[string]string

	// AllowUsageError opts out of the parser-rejection guard, for a scenario
	// whose subject IS how the CLI handles a bad invocation.
	AllowUsageError bool
}

// CLIIn is CLI with options, for the verbs that need a working directory or
// extra variables — registry credentials, most of all.
func CLIIn(tb TB, target Target, repo string, opts RunOptions, args ...string) Run {
	tb.Helper()
	requireAccepted(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutCLI)
	defer cancel()

	// G204: args are scenario-authored verbs and flags, never external input.
	// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	cmd := exec.CommandContext(ctx, Binary(tb), args...) //nolint:gosec

	env := cliEnv(tb, target, repo)

	for key, value := range opts.Env {
		if isTrustEnvironmentKey(key) {
			tb.Fatalf("livetest: scenario environment may not override containment variable %s", key)
		}

		env = append(env, key+"="+value)
	}

	for key, value := range credentialProxyEnvironment(tb) {
		env = append(env, key+"="+value)
	}

	cmd.Env = env
	cmd.Dir = opts.Dir

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	run := Run{Args: args, Stdout: stdout.String(), Stderr: stderr.String()}
	assertNoTargetSecret(tb, target, "stdout of "+strings.Join(args, " "), run.Stdout)
	assertNoTargetSecret(tb, target, "stderr of "+strings.Join(args, " "), run.Stderr)
	assertNotAUsageError(tb, run, opts.AllowUsageError, target)

	var exitErr *exec.ExitError

	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		run.ExitCode = exitErr.ExitCode()
	default:
		tb.Fatalf("livetest: run %q: %v", strings.Join(args, " "), err)
	}

	return run
}

// RunContextEnv is the commit and ref a pipeline would have supplied, for the
// scenarios that drive the product from the host rather than inside a job.
//
// cliEnv deliberately builds a closed environment, so a verb that describes a
// build — `release provenance` most of all — finds no commit and refuses. That
// refusal is correct: the alternative is a statement about a build nobody can
// identify.
//
// Spelled once here rather than per scenario because the names are ADAPTER
// knowledge, and each adapter resolves from its own runner's dialect: GitLab
// reads $CI_COMMIT_SHA, Forgejo reads $FORGEJO_SHA with $GITHUB_SHA as its
// act_runner alias. A scenario that sets one dialect works on one forge and
// resolves empty on the other, which surfaces as a validation error naming the
// commit rather than the missing variable. Both are set for that reason.
//
// It covers the whole context rather than the commit alone. `release provenance`
// needs a builder identity too, derived from the workflow ref or from
// server+repo+run id, and discovering that one field at a time — each surfacing
// as its own "X is required" — is how a fixture ends up with a hand-grown pile
// of variables nobody can explain. A pipeline supplies all of these together, so
// so does this.
//
// It also carries the forge's runner markers. The product trusts run context
// only when a runner vouches for it -- GITHUB_ACTIONS with FORGEJO_ACTIONS on a
// Forgejo runner, GITLAB_CI on GitLab -- and reads nothing a stray shell set
// otherwise. The markers are per forge because they also pick which dialect
// the product reads, and the target's repository is only in its own dialect.
// Runner detection itself stays pinned by FORGE_CI_RUNNER.
func RunContextEnv(target Target, commit, refName, runID string) map[string]string {
	// The canonical builder identity is <server>/<workflow ref>, so the ref is
	// spelled the way a runner spells it: <owner>/<repo>/<file>@<git ref>.
	workflowRef := "livetest/probe/.forgejo/workflows/livetest.yml@refs/heads/" + refName

	env := map[string]string{
		"CI_COMMIT_SHA":      commit,
		"FORGEJO_SHA":        commit,
		"GITHUB_SHA":         commit,
		"CI_COMMIT_REF_NAME": refName,
		"FORGEJO_REF_NAME":   refName,
		"GITHUB_REF_NAME":    refName,

		"CI_RUN_ID":      runID,
		"FORGEJO_RUN_ID": runID,
		"GITHUB_RUN_ID":  runID,
		"CI_PIPELINE_ID": runID,

		"FORGEJO_WORKFLOW_REF": workflowRef,
		"GITHUB_WORKFLOW_REF":  workflowRef,
	}

	const marker = "true"

	switch target.Forge {
	case provider.ForgeGitLab:
		// GitLab's builder identity is its pipeline URL, spelled for the
		// same synthetic livetest/probe project the workflow ref names.
		env["GITLAB_CI"] = marker
		env["CI_PIPELINE_URL"] = target.BaseURL() + "/livetest/probe/-/pipelines/" + runID
	case provider.ForgeForgejo:
		env["GITHUB_ACTIONS"], env["FORGEJO_ACTIONS"] = marker, marker
	case provider.ForgeGitHub, provider.ForgeLocal:
	}

	return env
}

// cliEnv is the whole environment the product sees: the target, plus the few
// runtime variables any process needs.
//
// It is built from nothing rather than inherited. The operator's shell during a
// live run holds a lab token, quite possibly a real GITHUB_TOKEN, and whatever
// CI variables a previous experiment left behind — and this binary's entire job
// is to detect its platform from the environment and then authenticate to it.
// Handing it the ambient environment would mean the suite could not say which
// forge the product talked to, and a stray credential could be sent to a lab
// host. So every variable it can act on is either set here or absent.
func cliEnv(tb TB, target Target, repo string) []string {
	values := map[string]string{
		// Detection is pinned rather than inferred: this runs on a laptop, and
		// leaving the product to guess would make the result depend on whose
		// shell it was.
		"FORGE_CI_PROVIDER": string(target.Forge),
		"FORGE_CI_RUNNER":   "local",

		// Containment, for every invocation rather than for the scenarios that
		// remember. cosign publishes to the PUBLIC Rekor log by default, and a
		// Rekor entry is permanent and append-only — a run cannot take one
		// back. Setting it here means no test can publish, including tests
		// nobody has written yet, which is the property a per-scenario setting
		// cannot buy. The engine derives both halves from this one value, so
		// signing writes no entry and verifying stops demanding one.
		"FORGE_CI_COSIGN_TRANSPARENCY": "none",
	}

	// The same target mapping the adapters get, so the binary and a role call
	// address the identical instance.
	targetValues := targetEnv(target, repo)
	for _, key := range targetEnvKeys(target) {
		if value := targetValues(key); value != "" {
			values[key] = value
		}
	}

	for key, value := range targetTrustEnvironment(target) {
		values[key] = value
	}

	// Runtime only. TLS variables come only from the accepted target above,
	// never from ambient process trust.
	for key, value := range runtimeEnvironment(tb) {
		values[key] = value
	}

	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}

	return env
}

// runtimeEnvironment is what a subprocess needs to run at all: PATH because
// the product shells out to git and cosign, TMPDIR for temp files, and a HOME.
// HOME is an empty directory removed with the test rather than the operator's,
// whose git config, Docker credentials and cosign state would otherwise decide
// what the product, git and cosign do.
func runtimeEnvironment(tb TB) map[string]string {
	tb.Helper()

	home, err := os.MkdirTemp("", "livetest-home-")
	if err != nil {
		tb.Fatalf("livetest: create a private HOME: %v", err)
	}

	tb.Cleanup(func() { _ = os.RemoveAll(home) })

	values := map[string]string{"HOME": home}

	for _, key := range []string{"PATH", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			values[key] = value
		}
	}

	return values
}

// targetEnvKeys lists the variables targetEnv answers for a forge, so cliEnv can
// materialise them without the closure leaking its map.
func targetEnvKeys(target Target) []string {
	switch target.Forge {
	case provider.ForgeForgejo:
		return []string{"FORGEJO_TOKEN", "GITEA_TOKEN", "FORGEJO_SERVER_URL", "FORGEJO_REPOSITORY"}
	case provider.ForgeGitLab:
		return []string{"GITLAB_TOKEN", "CI_SERVER_URL", "CI_PROJECT_PATH"}
	case provider.ForgeGitHub, provider.ForgeLocal:
		return nil
	}

	return nil
}

// assertNotAUsageError fails the test when the CLI could not parse the
// invocation, rather than letting a scenario assert against the parser's
// complaint.
//
// That is a scenario bug, not a product result, and it hides well: a usage error
// exits non-zero and its text names the command, so a check for "it failed and
// said something about auth" passes on it. PAR-TOK-3 did exactly that from the
// day it was written — it drove `validate auth tokens`, which does not exist,
// and matched "auth" inside "Run 'forge-ci validate auth --help'". It
// reported a covered surface that was never exercised.
//
// Enforced in the kit rather than per scenario so it also covers the ones nobody
// has written yet. A scenario whose subject IS how the CLI handles a bad
// invocation opts out with RunOptions.AllowUsageError.
func assertNotAUsageError(tb TB, run Run, allow bool, target Target) {
	tb.Helper()

	if allow {
		return
	}

	lower := strings.ToLower(run.Stderr)
	for _, marker := range usageErrorMarkers {
		if strings.Contains(lower, marker) {
			tb.Fatalf("livetest: %q was rejected by the argument parser, so this scenario exercised the CLI rather than the forge\nstderr: %s",
				strings.Join(run.Args, " "), redactTargetSecrets(target, run.Stderr))
		}
	}
}
