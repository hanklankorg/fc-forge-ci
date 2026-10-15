// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// These tests drive run-live-tests.sh offline: a fake go on PATH records its
// argv and environment and plays the preflight/build roles and emits a compiled
// suite with a configurable exit status. Fake host tools stand in for
// the pinned toolchain. No lab, network or real go toolchain is touched. What
// the real preflight prints and refuses is tested in internal/livetest. Plain
// argument and tool-check flow is left to shellcheck and to reading the script;
// these tests pin what a reader cannot see: the redacted log, the suite status
// surviving the tee, the pruning and the run lock.

// liveCommandBound only catches a hang; a run finishes in well under a second.
const liveCommandBound = 2 * time.Minute

const fakeGoScript = `#!/usr/bin/env bash
set -euo pipefail
# The layer build comes through scripts/ci/forgejo-binary.sh, which runs go
# in a closed environment and from the repository root: it is played, and
# not recorded.
if [[ -n "${FAKE_GO_ARGV:-}" ]]; then printf '%s\n' "$*" >>"$FAKE_GO_ARGV"; fi
if [[ "$1" == -C ]]; then shift 2; fi
case "$1" in
env)
  [[ "$2" == GOVERSION ]] || exit 99
  printf '%s\n' 'FAKE_PINNED_GO'
  ;;
run)
  printf '%s\n' "$FAKE_PREFLIGHT_REPORT"
  ;;
build)
  output=''
  while (($#)); do
    if [[ "$1" == -o ]]; then output=$2; break; fi
    shift
  done
  printf '#!/usr/bin/env sh\nexit 0\n' >"$output"
  chmod 700 "$output"
  ;;
test)
  if { : >&9; } 2>/dev/null; then
    printf 'fake go: the run lock descriptor reached test compilation\n' >&2
    exit 98
  fi
  [[ "$2" == -c ]] || exit 99
  while [[ "$1" != -o ]]; do shift; done
  output="${2%/}/${FAKE_GO_PACKAGE##*/}.test"
  printf '%s\n' "$FAKE_GO_SUITE" >"$output"
  chmod 700 "$output"
  ;;
list)
  printf '%s\n' "$FAKE_GO_PACKAGE"
  ;;
*) exit 99 ;;
esac
`

const fakeCompiledSuite = `#!/usr/bin/env bash
set -euo pipefail
if { : >&9; } 2>/dev/null; then
  printf 'the run lock descriptor reached the suite\n' >&2
  exit 98
fi
printf 'suite %s\n' "$*" >>"$FAKE_GO_ARGV"
env >"$FAKE_GO_ENV"
printf '%s\n' "${FAKE_GO_TEST_OUTPUT:-}"
exit "${FAKE_GO_TEST_STATUS:-0}"
`

// liveRun is one offline invocation of run-live-tests.sh with its owned state.
type liveRun struct {
	evidenceRoot string
	stateHome    string
	argvFile     string
	envFile      string
	env          []string
}

// newLiveRun prepares a hermetic environment: fake go and pinned tools first
// on PATH, symlinks to the real host utilities the script needs after them,
// and owned HOME, XDG, evidence and temporary directories. extraEnv adds
// FAKE_GO_* settings.
func newLiveRun(t *testing.T, extraEnv ...string) liveRun {
	t.Helper()
	fakeDir := t.TempDir()

	_, version, found := strings.Cut(string(reporoot.ReadFile(t, "go.mod")), "\ngo ")
	if !found {
		t.Fatal("go.mod does not pin a Go version")
	}

	version, _, _ = strings.Cut(version, "\n")
	writeExecutable(t, filepath.Join(fakeDir, "go"), strings.ReplaceAll(fakeGoScript, "FAKE_PINNED_GO", "go"+version))
	// The pinned toolchain the script checks for but never runs here; git,
	// sha256sum and base64 are real host utilities because the script uses
	// their output.
	for _, tool := range []string{"cosign", "jq", "syft", "buildah"} {
		writeExecutable(t, filepath.Join(fakeDir, tool), "#!/usr/bin/env sh\nexit 0\n")
	}

	hostDir := t.TempDir()

	for _, utility := range []string{
		"bash", "sh", "env", "sed", "tee", "install", "find", "date", "mktemp",
		"cut", "chmod", "rm", "dirname", "sha256sum", "base64", "git", "flock",
	} {
		resolved, err := exec.LookPath(utility)
		if err != nil {
			t.Fatalf("host utility %s: %v", utility, err)
		}

		if err := os.Symlink(resolved, filepath.Join(hostDir, utility)); err != nil {
			t.Fatal(err)
		}
	}

	state := t.TempDir()
	run := liveRun{
		evidenceRoot: filepath.Join(state, "evidence"),
		stateHome:    t.TempDir(),
		argvFile:     filepath.Join(state, "go-argv"),
		envFile:      filepath.Join(state, "go-env"),
	}
	run.env = append([]string{
		"PATH=" + fakeDir + string(os.PathListSeparator) + hostDir,
		"HOME=" + t.TempDir(),
		"XDG_STATE_HOME=" + run.stateHome,
		"TMPDIR=" + t.TempDir(),
		"LC_ALL=C",
		"RC_LIVE_EVIDENCE_ROOT=" + run.evidenceRoot,
		"FAKE_GO_ARGV=" + run.argvFile,
		"FAKE_GO_ENV=" + run.envFile,
		"FAKE_GO_PACKAGE=" + t.TempDir(),
		"FAKE_GO_SUITE=" + fakeCompiledSuite,
		"FAKE_PREFLIGHT_REPORT=" + fakePreflightReport,
	}, extraEnv...)

	return run
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { //nolint:gosec // Fake tools must be executable; the directory is private.
		t.Fatal(err)
	}
}

// exec runs the script and returns its stdout, stderr and exit status.
func (run liveRun) exec(t *testing.T, args ...string) (string, string, int) {
	t.Helper()

	script, err := filepath.Abs("run-live-tests.sh")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), liveCommandBound)
	defer cancel()

	command := exec.CommandContext(ctx, "bash", append([]string{script}, args...)...) //nolint:gosec // fixed local script with test-authored arguments.
	command.Env = run.env

	var out, errOut strings.Builder

	command.Stdout = &out
	command.Stderr = &errOut
	err = command.Run()
	status := 0

	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		status = exit.ExitCode()
	default:
		t.Fatalf("run script: %v", err)
	}

	if ctx.Err() != nil {
		t.Fatalf("script hung\nstdout:\n%s\nstderr:\n%s", out.String(), errOut.String())
	}

	return out.String(), errOut.String(), status
}

// goCalls returns the argv the fake go and compiled suite recorded.
func (run liveRun) goCalls(t *testing.T) []string {
	t.Helper()

	body, err := os.ReadFile(run.argvFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		t.Fatal(err)
	}

	return strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
}

// goTestEnv returns the environment the compiled suite saw, as NAME=value lines.
func (run liveRun) goTestEnv(t *testing.T) map[string]string {
	t.Helper()

	body, err := os.ReadFile(run.envFile)
	if err != nil {
		t.Fatal(err)
	}

	env := map[string]string{}

	for line := range strings.SplitSeq(strings.TrimSuffix(string(body), "\n"), "\n") {
		name, value, _ := strings.Cut(line, "=")
		env[name] = value
	}

	return env
}

// logFile returns the path of the one live-run.log under the evidence root
// and its body.
func (run liveRun) logFile(t *testing.T) (string, string) {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(run.evidenceRoot, "20??????T??????Z-*", "live-run.log"))
	if err != nil {
		t.Fatal(err)
	}

	if len(matches) != 1 {
		t.Fatalf("want exactly one live-run.log, got %v", matches)
	}

	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}

	return matches[0], string(raw)
}

func requireNoEvidence(t *testing.T, run liveRun) {
	t.Helper()

	if _, err := os.Stat(run.evidenceRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("evidence root was created: stat err %v", err)
	}
}

func TestRunLiveTests_HappyPathRunsTheSuiteAndKeepsARedactedLog(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args     []string
		wantTest string
		scenario string
	}{
		"full": {
			args:     []string{"full"},
			wantTest: "suite -test.paniconexit0 -test.count=1 -test.timeout=90m -test.v",
			scenario: "scenario=''",
		},
		"focused": {
			args:     []string{"focused", "TestConformance/Publish"},
			wantTest: "suite -test.paniconexit0 -test.count=1 -test.timeout=90m -test.v -test.run TestConformance/Publish",
			scenario: "scenario=TestConformance/Publish",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run := newLiveRun(t, "FAKE_GO_TEST_OUTPUT="+fakeSuiteOutput,
				"RC_LIVE_CONSUMER=/absolute/consumer", "RC_LIVE_CONSUMER_EVENT=release", "RC_LIVE_CONSUMER_VISIBILITY=private")

			stdout, stderr, status := run.exec(t, tc.args...)
			if status != 0 {
				t.Fatalf("status %d\nstdout:\n%s\nstderr:\n%s", status, stdout, stderr)
			}

			requireGoCalls(t, run.goCalls(t), tc.wantTest)
			requireHandover(t, run.goTestEnv(t), tc.args[0])

			// A consumer recipe's inputs are in the log, so the run can be repeated from it.
			logPath := requireRedactedLog(t, run, stdout, "profile="+tc.args[0], tc.scenario, fakePreflightReport+"\n",
				"rc_live_consumer=/absolute/consumer\n", "rc_live_consumer_event=release\n", "rc_live_consumer_visibility=private\n")
			if !strings.HasPrefix(stdout, fakePreflightReport+"\n") {
				t.Fatalf("stdout does not start with the preflight report:\n%s", stdout)
			}

			if !strings.HasSuffix(stdout, "Live result: passed\nLive log: "+logPath+" (retained for 30 days)\n") {
				t.Fatalf("stdout does not end with the verdict and the log line:\n%s", stdout)
			}
		})
	}
}

// fakePreflightReport stands in for what the real preflight prints on
// acceptance: its summary line and the module's explanation of the contract,
// ending in the run identity. The script must show it and keep it in the log.
const fakePreflightReport = "live preflight: 2 provider(s), generation fake-generation, namespace rc-\n\n" +
	"generation fake-generation\n" +
	"identity for rc-: run=fake-generation|targets=forgejo@https://forgejo.compose.forgelab:8443/fixture-user#resources=rc-"

// fakeSuiteOutput is what the compiled fixture prints: ordinary verbose output
// with two credential-shaped lines the log must not keep.
const fakeSuiteOutput = "=== RUN TestFake\ntoken glpat-ABCDEFGHIJKLMNOPQRSTUVWX\nAuthorization: Bearer abc.def\n--- PASS: TestFake\nok\tfake\t0.01s" // gitleaks:allow -- synthetic redaction canaries.

// requireGoCalls requires preflight, four product builds, test compilation,
// package discovery and direct suite execution, in that order.
func requireGoCalls(t *testing.T, calls []string, wantTest string) {
	t.Helper()

	if len(calls) != 8 || calls[0] != "run ./internal/livetest/preflight" || calls[7] != wantTest {
		t.Fatalf("go calls:\n%s", strings.Join(calls, "\n"))
	}

	if !strings.HasPrefix(calls[5], "test -c -tags=live -buildvcs=false -o ") ||
		!strings.HasSuffix(calls[5], "/tests/ ./internal/livetest/...") ||
		!strings.HasPrefix(calls[6], "list -tags=live -f ") || !strings.HasSuffix(calls[6], " ./internal/livetest/...") {
		t.Fatalf("test compilation/discovery calls: %v", calls[5:7])
	}

	for i, target := range []string{
		"forge-ci-host ./cmd/forge-ci",
		"forge-ci-runner ./cmd/forge-ci",
		"credential-proxy ./internal/livetest/credentialproxy",
		"probe-json ./internal/livetest/probejson",
	} {
		if call := calls[1+i]; !strings.HasPrefix(call, "build -trimpath -buildvcs=false -o ") || !strings.HasSuffix(call, "/"+target) {
			t.Fatalf("build call %d: %q", i, call)
		}
	}
}

// requireHandover requires the profile and the built binaries to have reached
// the suite's environment.
func requireHandover(t *testing.T, env map[string]string, profile string) {
	t.Helper()

	if env["RC_LIVE_PROFILE"] != profile {
		t.Fatalf("RC_LIVE_PROFILE=%q", env["RC_LIVE_PROFILE"])
	}

	for _, name := range []string{"RC_LIVE_BIN", "RC_LIVE_RUNNER_BIN", "RC_LIVE_CREDENTIAL_PROXY_BIN", "RC_LIVE_PROBE_JSON_BIN", "RC_LIVE_LAYER_BIN"} {
		if !filepath.IsAbs(env[name]) {
			t.Fatalf("%s=%q is not an absolute path", name, env[name])
		}
	}

	if !container.ValidSHA256Hex(env["RC_LIVE_RUNNER_SHA256"]) {
		t.Fatalf("RC_LIVE_RUNNER_SHA256=%q", env["RC_LIVE_RUNNER_SHA256"])
	}
}

// requireRedactedLog requires a private log holding the metadata header and
// the suite output with the credentials redacted, and returns its path.
func requireRedactedLog(t *testing.T, run liveRun, stdout string, header ...string) string {
	t.Helper()
	logPath, log := run.logFile(t)

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if mode := info.Mode().Perm(); mode&0o177 != 0 {
		t.Fatalf("log mode %o is wider than 0600", mode)
	}

	for _, want := range append(header, "started_at=", "source_commit=", "--- PASS: TestFake", "ok\tfake") {
		if !strings.Contains(log, want) {
			t.Fatalf("log lacks %q:\n%s", want, log)
		}
	}

	for _, secret := range []string{"glpat-ABCDEFGHIJKLMNOPQRSTUVWX", "abc.def"} { // gitleaks:allow -- synthetic redaction canaries.
		if strings.Contains(log, secret) || strings.Contains(stdout, secret) {
			t.Fatalf("secret %q leaked\nlog:\n%s\nstdout:\n%s", secret, log, stdout)
		}
	}

	if !strings.Contains(log, "token [REDACTED]") || !strings.Contains(log, "Authorization: Bearer [REDACTED]") {
		t.Fatalf("log lacks the redaction markers:\n%s", log)
	}

	return logPath
}

func TestRunLiveTests_PropagatesTheSuiteStatus(t *testing.T) {
	t.Parallel()
	run := newLiveRun(t, "FAKE_GO_TEST_STATUS=7", "FAKE_GO_TEST_OUTPUT=FAIL fake")

	stdout, stderr, status := run.exec(t, "full")
	if status != 7 {
		t.Fatalf("status %d\nstdout:\n%s\nstderr:\n%s", status, stdout, stderr)
	}

	if _, log := run.logFile(t); !strings.Contains(log, "FAIL fake") {
		t.Fatalf("log lacks the suite output:\n%s", log)
	}

	if !strings.Contains(stdout, "Live result: failed\n") {
		t.Fatalf("stdout lacks the failed verdict:\n%s", stdout)
	}
}

func TestRunLiveTests_PrunesOnlyOldRunDirectories(t *testing.T) {
	t.Parallel()
	run := newLiveRun(t)
	old := filepath.Join(run.evidenceRoot, "20200101T000000Z-1")
	recent := filepath.Join(run.evidenceRoot, "20200101T000000Z-2")

	foreign := filepath.Join(run.evidenceRoot, "notes")
	for _, dir := range []string{old, recent, foreign} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		writeExecutable(t, filepath.Join(dir, "live-run.log"), "kept\n")
	}

	stale := time.Now().Add(-40 * 24 * time.Hour)
	for _, dir := range []string{old, foreign} {
		if err := os.Chtimes(dir, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	if stdout, stderr, status := run.exec(t, "full"); status != 0 {
		t.Fatalf("status %d\nstdout:\n%s\nstderr:\n%s", status, stdout, stderr)
	}

	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old run directory survived: stat err %v", err)
	}

	for _, dir := range []string{recent, foreign} {
		if _, err := os.Stat(filepath.Join(dir, "live-run.log")); err != nil {
			t.Fatalf("%s was pruned: %v", dir, err)
		}
	}
}

func TestRunLiveTests_RefusesWhileAnotherRunHoldsTheLock(t *testing.T) {
	t.Parallel()
	run := newLiveRun(t)

	lockDir := filepath.Join(run.stateHome, "forge-ci")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}

	held, err := os.OpenFile(filepath.Join(lockDir, "live.lock"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = held.Close() })

	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	_, stderr, status := run.exec(t, "full")
	if status != 2 {
		t.Fatalf("status %d, stderr:\n%s", status, stderr)
	}

	if !strings.Contains(stderr, "another live run holds") {
		t.Fatalf("stderr does not name the held lock:\n%s", stderr)
	}

	if calls := run.goCalls(t); len(calls) != 1 || calls[0] != "run ./internal/livetest/preflight" {
		t.Fatalf("expected only the preflight call, got %v", calls)
	}

	requireNoEvidence(t, run)
}
