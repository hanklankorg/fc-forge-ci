// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/livetest"
	"github.com/diggsweden/forge-ci/internal/retry"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
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
# The layer build comes through scripts/ci/engine-binary.sh, which runs go
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
  if [[ "${3:-}" == classify-run ]]; then
    if [[ -n "${FAKE_REAL_CLASSIFIER:-}" ]]; then
      exec "$FAKE_REAL_CLASSIFIER" -test.run '^TestLiveClassifierProcessFixture$' -- "$4" "$5"
    fi
    case "$4" in
      0) printf '%s\n' "${FAKE_LIVE_CLASSIFICATION:-0 passed}" ;;
      130) printf '130 interrupted\n' ;;
      *) printf '%s failed\n' "$4" ;;
    esac
  elif [[ "${3:-}" == runner-arch ]]; then
    printf '%s\n' arm64
  else
    printf '%s\n' "$FAKE_PREFLIGHT_REPORT"
  fi
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
  if { : >&9; } 2>/dev/null || { : >&7; } 2>/dev/null; then
    printf 'fake go: the run lock descriptor reached test compilation\n' >&2
    exit 98
  fi
  [[ "$2" == -c ]] || exit 99
  while [[ "$1" != -o ]]; do shift; done
  while IFS= read -r package; do
    output="${2%/}/${package##*/}.test"
    printf '%s\n' "$FAKE_GO_SUITE" >"$output"
    chmod 700 "$output"
  done <<<"$FAKE_GO_PACKAGE"
  ;;
list)
  printf '%s\n' "$FAKE_GO_PACKAGE"
  ;;
*) exit 99 ;;
esac
`

const fakeCompiledSuite = `#!/usr/bin/env bash
set -euo pipefail
if { : >&9; } 2>/dev/null || { : >&7; } 2>/dev/null; then
  printf 'the run lock descriptor reached the suite\n' >&2
  exit 98
fi
printf 'suite %s\n' "$*" >>"$FAKE_GO_ARGV"
env >"$FAKE_GO_ENV"
if [[ -n "${FAKE_LIVE_READY:-}" ]]; then
  : >"$FAKE_LIVE_READY"
  while [[ ! -e "$FAKE_LIVE_RELEASE" ]]; do sleep 0.05; done
fi
if [[ "$PWD" == "${FAKE_GO_SECOND_PACKAGE:-}" ]]; then
  printf '%s\n' "${FAKE_GO_SECOND_OUTPUT:-}"
else
  printf '%s\n' "${FAKE_GO_TEST_OUTPUT:-}"
fi
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

	for _, utility := range []string{"sh", "env", "tee", "git", "flock", "date", "mktemp", "cut", "chmod", "rm", "dirname", "base64", "sleep"} {
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
		"PATH=" + fakeDir + string(os.PathListSeparator) + testenv.GNUPath(t,
			"bash", "sed", "install", "find", "sha256sum") + string(os.PathListSeparator) + hostDir,
		"HOME=" + t.TempDir(),
		"XDG_STATE_HOME=" + run.stateHome,
		"TMPDIR=" + t.TempDir(),
		"LC_ALL=C",
		"FORGE_CI_LIVE_EVIDENCE_ROOT=" + run.evidenceRoot,
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

	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
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

	command := exec.CommandContext(ctx, "bash", append([]string{script}, args...)...)
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
				"FORGE_CI_LIVE_CONSUMER=/absolute/consumer", "FORGE_CI_LIVE_CONSUMER_EVENT=release", "FORGE_CI_LIVE_CONSUMER_VISIBILITY=private")

			stdout, stderr, status := run.exec(t, tc.args...)
			if status != 0 {
				t.Fatalf("status %d\nstdout:\n%s\nstderr:\n%s", status, stdout, stderr)
			}

			requireGoCalls(t, run.goCalls(t), tc.wantTest)
			requireHandover(t, run.goTestEnv(t), tc.args[0])

			// A consumer recipe's inputs are in the log, so the run can be repeated from it.
			logPath := requireRedactedLog(t, run, stdout, "profile="+tc.args[0], tc.scenario, fakePreflightReport+"\n",
				"forge_ci_live_consumer=/absolute/consumer\n", "forge_ci_live_consumer_event=release\n", "forge_ci_live_consumer_visibility=private\n")
			if !strings.HasPrefix(stdout, fakePreflightReport+"\n") ||
				!strings.Contains(stdout, "\nlive: building runner binaries for linux/arm64, the lab runners architecture\n") {
				t.Fatalf("stdout does not start with the preflight report and name the runner architecture:\n%s", stdout)
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
const fakePreflightReport = "live preflight: 2 provider(s), generation fake-generation, namespace fci-\n\n" +
	"generation fake-generation\n" +
	"identity for fci-: run=fake-generation|targets=forgejo@https://forgejo.compose.forgelab:8443/fixture-user#resources=fci-"

// fakeSuiteOutput is what the compiled fixture prints: ordinary verbose output
// with credential-shaped lines, including a colored GitLab field separator.
const fakeSuiteOutput = "=== RUN TestFake\ntoken glpat-ABCDEFGHIJKLMNOPQRSTUVWX\nAuthorization: Bearer abc.def\ntoken\x1b[0;m=inert-ansi-secret\n--- PASS: TestFake\nok\tfake\t0.01s" // gitleaks:allow -- synthetic redaction canaries.

// requireGoCalls requires preflight, the runner architecture, four product
// builds, test compilation, package discovery and direct suite execution, in
// that order.
func requireGoCalls(t *testing.T, calls []string, wantTest string) {
	t.Helper()

	if len(calls) != 10 || calls[0] != "run ./internal/livetest/preflight" ||
		calls[1] != "run ./internal/livetest/preflight runner-arch" || calls[8] != wantTest {
		t.Fatalf("go calls:\n%s", strings.Join(calls, "\n"))
	}
	if !strings.HasPrefix(calls[9], "run ./internal/livetest/preflight classify-run 0 ") {
		t.Fatalf("missing Go result classification: %v", calls[9])
	}

	if !strings.HasPrefix(calls[6], "test -c -tags=live -buildvcs=false -o ") ||
		!strings.HasSuffix(calls[6], "/tests/ ./internal/livetest/...") ||
		!strings.HasPrefix(calls[7], "list -tags=live -f ") || !strings.HasSuffix(calls[7], " ./internal/livetest/...") {
		t.Fatalf("test compilation/discovery calls: %v", calls[6:8])
	}

	for index, target := range []string{
		"forge-ci-host ./cmd/forge-ci",
		"forge-ci-runner ./cmd/forge-ci",
		"credential-proxy ./internal/livetest/credentialproxy",
		"probe-json ./internal/livetest/probejson",
	} {
		if call := calls[2+index]; !strings.HasPrefix(call, "build -trimpath -buildvcs=false -o ") || !strings.HasSuffix(call, "/"+target) {
			t.Fatalf("build call %d: %q", index, call)
		}
	}
}

// requireHandover requires the profile and the built binaries to have reached
// the suite's environment.
func requireHandover(t *testing.T, env map[string]string, profile string) {
	t.Helper()

	if env["FORGE_CI_LIVE_PROFILE"] != profile {
		t.Fatalf("FORGE_CI_LIVE_PROFILE=%q", env["FORGE_CI_LIVE_PROFILE"])
	}

	for _, name := range []string{"FORGE_CI_LIVE_BIN", "FORGE_CI_LIVE_RUNNER_BIN", "FORGE_CI_LIVE_CREDENTIAL_PROXY_BIN", "FORGE_CI_LIVE_PROBE_JSON_BIN", "FORGE_CI_LIVE_LAYER_BIN"} {
		if !filepath.IsAbs(env[name]) {
			t.Fatalf("%s=%q is not an absolute path", name, env[name])
		}
	}

	if !container.ValidSHA256Hex(env["FORGE_CI_LIVE_RUNNER_SHA256"]) {
		t.Fatalf("FORGE_CI_LIVE_RUNNER_SHA256=%q", env["FORGE_CI_LIVE_RUNNER_SHA256"])
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

	for _, secret := range []string{"glpat-ABCDEFGHIJKLMNOPQRSTUVWX", "abc.def", "inert-ansi-secret"} { // gitleaks:allow -- synthetic redaction canaries.
		if strings.Contains(log, secret) || strings.Contains(stdout, secret) {
			t.Fatalf("secret %q leaked\nlog:\n%s\nstdout:\n%s", secret, log, stdout)
		}
	}

	if !strings.Contains(log, "token [REDACTED]") || !strings.Contains(log, "Authorization: Bearer [REDACTED]") || !strings.Contains(log, "token=[REDACTED]") {
		t.Fatalf("log lacks the redaction markers:\n%s", log)
	}

	return logPath
}

func TestRunLiveTests_RedactsColoredFieldsInSwedishLocale(t *testing.T) {
	t.Parallel()

	locales, err := exec.CommandContext(t.Context(), "locale", "-a").Output()
	if err != nil || !strings.Contains(strings.ToLower(string(locales)), "sv_se.utf8") {
		t.Skip("Swedish UTF-8 locale is not installed")
	}

	run := newLiveRun(t, "LC_ALL=sv_SE.UTF-8", "FAKE_GO_TEST_OUTPUT="+fakeSuiteOutput)

	stdout, stderr, status := run.exec(t, "full")
	if status != 0 {
		t.Fatalf("status %d\nstdout:\n%s\nstderr:\n%s", status, stdout, stderr)
	}

	requireRedactedLog(t, run, stdout)
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

// A run whose scenarios all skipped is not a pass: go test passes a test whose
// subtests skipped, so the verdict reads the results, not the exit status.
func TestRunLiveTests_ReportsARunInWhichNothingRanAsSkipped(t *testing.T) {
	t.Parallel()
	run := newLiveRun(t, "FAKE_GO_TEST_OUTPUT==== RUN   TestTrial\n=== RUN   TestTrial/public\n"+
		"    --- SKIP: TestTrial/public (0.00s)\n--- PASS: TestTrial (0.00s)\nPASS",
		"FAKE_LIVE_CLASSIFICATION=1 skipped: no scenario ran; the log says why")

	stdout, stderr, status := run.exec(t, "focused", "TestTrial")
	if status != 1 {
		t.Fatalf("status %d\nstdout:\n%s\nstderr:\n%s", status, stdout, stderr)
	}

	if !strings.Contains(stdout, "Live result: skipped: no scenario ran; the log says why\n") {
		t.Fatalf("stdout lacks the skipped verdict:\n%s", stdout)
	}
}

// Use the real Go classifier behind the launcher's inert preflight collaborator.
// Only the test data below contains scenario markers; this fixture is not a live
// scenario and must never register itself.
func TestLiveClassifierProcessFixture(t *testing.T) {
	if os.Getenv("FAKE_REAL_CLASSIFIER") == "" {
		return
	}
	status, err := strconv.Atoi(os.Args[len(os.Args)-2])
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.Open(os.Args[len(os.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	result, err := livetest.ClassifyLiveRun(log, status)
	_ = log.Close()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%d %s\n", result.ExitCode, result.Verdict)
	os.Exit(0)
}

func TestRunLiveTests_RealClassifierRequiresRegisteredScenario(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         int
	}{
		{"offline pass and live skip", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestOffline\n--- PASS: TestOffline (0s)\n--- SKIP: TestTrial (0s)", 1},
		{"offline pass and skipped children", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestOffline\n=== RUN   TestTrial/child\n--- PASS: TestOffline (0s)\n--- SKIP: TestTrial/child (0s)\n--- PASS: TestTrial (0s)", 1},
		{"offline only", "=== RUN   TestOffline\n--- PASS: TestOffline (0s)", 1},
		{"real leaf", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/child\n--- PASS: TestTrial/child (0s)\n--- PASS: TestTrial (0s)", 0},
		{"literal slash", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/sha1/application\n--- PASS: TestTrial/sha1/application (0s)\n--- PASS: TestTrial (0s)", 0},
		{"quoted result and skipped root", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n    trial_test.go:10: captured output:\n        --- PASS: TestTrial/reported-child (0s)\n--- SKIP: TestTrial (0s)", 1},
		{"missing RUN", "FORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newLiveRun(t, "FAKE_REAL_CLASSIFIER="+os.Args[0], "FAKE_GO_TEST_OUTPUT="+tc.output)
			stdout, stderr, status := run.exec(t, "focused", "TestTrial|TestOffline")
			if status != tc.want {
				t.Fatalf("status=%d, want=%d\n%s\n%s", status, tc.want, stdout, stderr)
			}
			_, log := run.logFile(t)
			begin := strings.Index(log, "FORGE_CI_LIVE_PACKAGE_BEGIN ")
			body := strings.Index(log, tc.output)
			end := strings.Index(log, "FORGE_CI_LIVE_PACKAGE_END ")
			if begin < 0 || body <= begin || end <= body {
				t.Fatalf("suite output is not bounded by package records:\n%s", log)
			}
		})
	}
}

func TestRunLiveTests_RealClassifierSeparatesPackages(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	run := newLiveRun(t, "FAKE_REAL_CLASSIFIER="+os.Args[0], "FAKE_GO_PACKAGE="+first+"\n"+second,
		"FAKE_GO_TEST_OUTPUT==== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- SKIP: TestTrial (0s)",
		"FAKE_GO_SECOND_PACKAGE="+second, "FAKE_GO_SECOND_OUTPUT==== RUN   TestTrial\n--- PASS: TestTrial (0s)")
	stdout, stderr, status := run.exec(t, "focused", "TestTrial")
	if status != 1 || !strings.Contains(stdout, "Live result: skipped:") {
		t.Fatalf("another package's offline pass qualified a skipped live scenario: status=%d\n%s\n%s", status, stdout, stderr)
	}
	_, log := run.logFile(t)
	for _, pkg := range []string{first, second} {
		if !strings.Contains(log, "FORGE_CI_LIVE_PACKAGE_BEGIN "+pkg+"\n") ||
			!strings.Contains(log, "FORGE_CI_LIVE_PACKAGE_END "+pkg+"\n") {
			t.Fatalf("missing boundaries for %s:\n%s", pkg, log)
		}
	}
}

func TestRunLiveTests_PrunesOnlyOldRunDirectories(t *testing.T) {
	t.Parallel()
	run := newLiveRun(t, "FAKE_GO_TEST_OUTPUT="+fakeSuiteOutput)
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

func TestRunLiveTests_ProviderLanesOverlapAndConflictingScopesRefuse(t *testing.T) {
	root := t.TempDir()
	ready, release := filepath.Join(root, "ready"), filepath.Join(root, "release")
	first := newLiveRun(t, "FORGE_CI_LIVE_PROVIDER=forgejo", "FAKE_GO_TEST_OUTPUT="+fakeSuiteOutput,
		"FAKE_LIVE_READY="+ready, "FAKE_LIVE_RELEASE="+release)
	script, err := filepath.Abs("run-live-tests.sh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", script, "focused", "TestFake")
	command.Env = first.env
	var out, errOut strings.Builder
	command.Stdout, command.Stderr = &out, &errOut
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Errorf("held provider lane: %v\n%s\n%s", err, out.String(), errOut.String())
		}
	}()
	if err := retry.Run(ctx, nil, 2000, 10*time.Millisecond, func() error {
		_, err := os.Stat(ready)
		return err
	}, retry.WithBackoff(retry.Constant)); err != nil {
		t.Fatalf("first provider lane did not reach its held scenario: %v", err)
	}
	for _, scope := range []string{"gitlab", "forgejo", "all"} {
		second := newLiveRun(t, "FORGE_CI_LIVE_PROVIDER="+scope, "FAKE_GO_TEST_OUTPUT="+fakeSuiteOutput,
			"XDG_STATE_HOME="+first.stateHome)
		stdout, stderr, status := second.exec(t, "focused", "TestFake")
		want := 2
		if scope == "gitlab" {
			want = 0
		}
		if status != want {
			t.Fatalf("scope %s while Forgejo is active: status=%d, want=%d\n%s\n%s", scope, status, want, stdout, stderr)
		}
	}
}

func TestRunLiveTests_ScopedLaneRespectsLegacyExclusiveLock(t *testing.T) {
	for _, scope := range []string{"forgejo", "gitlab"} {
		t.Run(scope, func(t *testing.T) {
			run := newLiveRun(t, "FORGE_CI_LIVE_PROVIDER="+scope)
			dir := filepath.Join(run.stateHome, "forge-ci")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			held, err := os.OpenFile(filepath.Join(dir, "live.lock"), os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = held.Close() }()
			if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			_, stderr, status := run.exec(t, "focused", "TestFake")
			if status != 2 || !strings.Contains(stderr, "another live run holds") {
				t.Fatalf("legacy lock bypassed: %d, %s", status, stderr)
			}
			requireNoEvidence(t, run)
		})
	}
}
