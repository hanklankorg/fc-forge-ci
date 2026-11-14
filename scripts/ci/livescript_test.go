// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package ci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/retry"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

// These tests drive run-live-tests.sh offline: a fake go on PATH records its
// argv and environment and plays the preflight/build roles and emits a compiled
// suite with a configurable exit status. Fake host tools stand in for
// the pinned toolchain. No lab or network is touched; the classifier cases build
// the maintained preflight CLI offline with real Go before installing fake PATH.
// What preflight prints and refuses is tested in test/live/internal/livetest. Plain
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
      exec "$FAKE_REAL_CLASSIFIER" classify-run "$4" "$5"
    fi
    case "$4" in
      0) printf '%s\n' "${FAKE_LIVE_CLASSIFICATION:-0 passed}" ;;
      130) printf '130 interrupted\n' ;;
      *) printf '%s failed\n' "$4" ;;
    esac
  elif [[ "${3:-}" == runner-arch ]]; then
    printf '%s\n' arm64
  elif [[ "${3:-}" == prepared-source ]]; then
    [[ -z "${FAKE_PREPARED_REFUSES:-}" ]] || exit 2
  elif [[ "${3:-}" == provider-account ]]; then
    printf '%s\n' "${FAKE_LAB_ACCOUNT:-forgeci}"
  elif [[ "${3:-}" == focused-rows ]]; then
    # go run gives a refusal of the program status 1, whatever its own.
    [[ -z "${FAKE_FOCUSED_ROWS_REFUSED:-}" ]] || { printf 'focused-rows: the run pattern selects no row\n' >&2; exit 1; }
    # The rows of the full run set, where a test gives them apart.
    if [[ "${FORGE_CI_LIVE_SET:-}" == full && -n "${FAKE_FOCUSED_ROWS_FULL:-}" ]]; then printf '%s\n' "$FAKE_FOCUSED_ROWS_FULL"; exit 0; fi
    [[ -z "${FAKE_FOCUSED_ROWS:-}" ]] || printf '%s\n' "$FAKE_FOCUSED_ROWS"
  elif [[ "${3:-}" == focused-scenarios ]]; then
    printf '%s\n' "${FAKE_FOCUSED_SCENARIOS:-}"
  elif [[ "${3:-}" == focused-skip ]]; then
    printf '%s\n' "${FAKE_FOCUSED_SKIP:-}"
  elif [[ "${3:-}" == rerun-rows ]]; then
    [[ -z "${FAKE_REAL_CLASSIFIER:-}" ]] || exec "$FAKE_REAL_CLASSIFIER" rerun-rows "$4"
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

// preparedEnv is the prepared.env of preparedDir.
const preparedEnv = "PREPARED_COMMIT=0123456789abcdef0123456789abcdef01234567\n"

// preparedDir is a prepared directory holding only its prepared.env, which a
// run hashes into its header.
func preparedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "prepared.env"), []byte(preparedEnv), 0o600); err != nil {
		t.Fatal(err)
	}

	return dir
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
			wantTest: "suite -test.paniconexit0 -test.count=1 -test.timeout=3h -test.v",
			scenario: "scenario=''",
		},
		"focused": {
			args:     []string{"focused", "TestConformance/Publish"},
			wantTest: "suite -test.paniconexit0 -test.count=1 -test.timeout=3h -test.v -test.run TestConformance/Publish",
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

// hostToolsPreflight is the launcher's Go check of the host tools.
const hostToolsPreflight = "run ./test/live/internal/livetest/preflight host-tools"

// requireGoCalls requires preflight, the host-tool preflight, the runner
// architecture, four product builds, test compilation, package discovery and
// direct suite execution, in that order.
func requireGoCalls(t *testing.T, calls []string, wantTest string) {
	t.Helper()

	if len(calls) < 2 || calls[1] != hostToolsPreflight {
		t.Fatalf("go calls do not check host tools second:\n%s", strings.Join(calls, "\n"))
	}

	calls = append(calls[:1:1], calls[2:]...)

	if len(calls) != 10 || calls[0] != "run ./test/live/internal/livetest/preflight" ||
		calls[1] != "run ./test/live/internal/livetest/preflight runner-arch" || calls[8] != wantTest {
		t.Fatalf("go calls:\n%s", strings.Join(calls, "\n"))
	}

	if !strings.HasPrefix(calls[9], "run ./test/live/internal/livetest/preflight classify-run 0 ") {
		t.Fatalf("missing Go result classification: %v", calls[9])
	}

	if !strings.HasPrefix(calls[6], "test -c -tags=live -buildvcs=false -o ") ||
		!strings.HasSuffix(calls[6], "/tests/ ./test/live/internal/livetest/... ./test/live/conformance ./test/live/githubconsumer") ||
		!strings.HasPrefix(calls[7], "list -tags=live -f ") || !strings.HasSuffix(calls[7], " ./test/live/internal/livetest/... ./test/live/conformance ./test/live/githubconsumer") {
		t.Fatalf("test compilation/discovery calls: %v", calls[6:8])
	}

	for index, target := range []string{
		"forge-ci-host ./cmd/forge-ci",
		"forge-ci-runner ./cmd/forge-ci",
		"credential-proxy ./test/live/internal/livetest/credentialproxy",
		"probe-json ./test/live/internal/livetest/probejson",
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

// buildLiveClassifier builds the maintained command once per parent test, before
// newLiveRun installs the launcher's inert Go collaborator. Only classify-run is
// invoked: no default preflight, target contract or provider is involved.
func buildLiveClassifier(t *testing.T) string {
	t.Helper()

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(t.TempDir(), "preflight")

	ctx, cancel := context.WithTimeout(t.Context(), liveCommandBound)
	defer cancel()

	command := exec.CommandContext(ctx, goBin, "build", "-buildvcs=false", "-o", output, "./test/live/internal/livetest/preflight")
	command.Dir = reporoot.Path(t)

	command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	if body, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build offline classifier: %v\n%s", err, body)
	}

	return output
}

func TestRunLiveTests_RealClassifierRequiresRegisteredScenario(t *testing.T) {
	classifier := buildLiveClassifier(t)

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
			run := newLiveRun(t, "FAKE_REAL_CLASSIFIER="+classifier, "FAKE_GO_TEST_OUTPUT="+tc.output)

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
	classifier := buildLiveClassifier(t)
	first, second := t.TempDir(), t.TempDir()
	run := newLiveRun(t, "FAKE_REAL_CLASSIFIER="+classifier, "FAKE_GO_PACKAGE="+first+"\n"+second,
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

// fakeRowSuite is a compiled suite of two packages: the first has the table
// test TestTrial, whose row two runs into the binary's time limit while the
// others pass, and the second has another test.
const fakeRowSuite = `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == -test.list ]]; then
  if [[ "$PWD" == "$FAKE_GO_SECOND_PACKAGE" ]]; then printf 'TestElsewhere\n'; else printf 'TestTrial\n'; fi
  exit 0
fi
printf 'suite %s %s\n' "${PWD##*/}" "$*" >>"$FAKE_GO_ARGV"
case "$*" in
*-test.skip*) printf 'testing: warning: no tests to run\nPASS\n' ;;
*two*)
  printf '=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/two\npanic: test timed out after 3h0m0s\n'
  exit 2
  ;;
*) printf '=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/row\n=== RUN   TestTrial/row/private\n        --- PASS: TestTrial/row/private (1.00s)\n    --- PASS: TestTrial/row (1.00s)\n--- PASS: TestTrial (1.00s)\nPASS\n' ;;
esac
`

// A focused run on a prepared directory runs each table row its pattern
// selects as a process of its own, in the package whose binary has the
// row's test, then whatever else the pattern selects in every package. A
// row that runs into the time limit costs that row: the rows after it run,
// each prints its line when it ends, and the run fails. A row whose test no
// binary has fails the run too.
func TestRunLiveTests_RunsEachSelectedRowAsItsOwnProcess(t *testing.T) {
	classifier := buildLiveClassifier(t)
	packages := filepath.Join(t.TempDir(), "packages")
	first, second := filepath.Join(packages, "first"), filepath.Join(packages, "second")

	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	rowRun := func(rows ...string) (liveRun, string, int) {
		run := newLiveRun(t, "FAKE_REAL_CLASSIFIER="+classifier, "FORGE_CI_LIVE_PREPARED="+preparedDir(t), "FAKE_GO_PACKAGE="+first+"\n"+second,
			"FAKE_GO_SECOND_PACKAGE="+second, "FAKE_FOCUSED_ROWS="+strings.Join(rows, "\n"), "FAKE_GO_SUITE="+fakeRowSuite)

		stdout, stderr, status := run.exec(t, "focused", "TestTrial")

		return run, stdout + stderr, status
	}

	rows := []string{"^TestTrial$/^one$", "^TestTrial$/^two$", "^TestTrial$/^three$"}

	run, output, status := rowRun(rows...)
	if status != 2 || !strings.Contains(output, "Live result: failed\n") {
		t.Fatalf("status %d\n%s", status, output)
	}

	const flags = " -test.paniconexit0 -test.count=1 -test.timeout=3h -test.v -test.run "

	want := []string{"suite first" + flags + rows[0], "suite first" + flags + rows[1], "suite first" + flags + rows[2],
		"suite first" + flags + "TestTrial -test.skip ^TestTrial$", "suite second" + flags + "TestTrial -test.skip ^TestTrial$"}
	if suites := slices.DeleteFunc(run.goCalls(t), func(call string) bool { return !strings.HasPrefix(call, "suite ") }); !slices.Equal(suites, want) {
		t.Errorf("the suite ran as\n%s\nwant\n%s", strings.Join(suites, "\n"), strings.Join(want, "\n"))
	}

	ended := regexp.MustCompile(`(?m)^FORGE_CI_LIVE_ROW (\S+) \d+m\d\ds (.*)$`).FindAllStringSubmatch(output, -1)
	if len(ended) != 3 || ended[0][1] != "passed" || ended[1][1] != "failed" || ended[2][1] != "passed" ||
		ended[0][2] != rows[0] || ended[1][2] != rows[1] || ended[2][2] != rows[2] {
		t.Errorf("the rows ended as %v\n%s", ended, output)
	}

	if _, output, status := rowRun(rows[0], "^TestNowhere$/^one$"); status != 1 || !strings.Contains(output, "live: 1 of 2 selected rows ran") {
		t.Errorf("a row whose test no binary has: status %d\n%s", status, output)
	}
}

// A run set below the full one runs its own rows, and beside them only what
// Go says the set runs, with every test of the tables kept out: a test whose
// rows are all outside the set would otherwise run whole there. When Go says
// nothing, the rows are the whole run.
func TestRunLiveTests_ARunSetLeavesTheTestsOfItsOtherRowsOut(t *testing.T) {
	packages := filepath.Join(t.TempDir(), "packages")
	first, second := filepath.Join(packages, "first"), filepath.Join(packages, "second")

	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	const (
		row   = "^TestTrial$/^one$"
		flags = " -test.paniconexit0 -test.count=1 -test.timeout=3h -test.v -test.run "
	)

	for others, want := range map[string][]string{
		"^TestScenario$": {"suite first" + flags + row, "suite first" + flags + "^TestScenario$ -test.skip ^TestTrial$|^TestElsewhere$",
			"suite second" + flags + "^TestScenario$ -test.skip ^TestTrial$|^TestElsewhere$"},
		"": {"suite first" + flags + row},
	} {
		run := newLiveRun(t, "FORGE_CI_LIVE_PREPARED="+preparedDir(t), "FORGE_CI_LIVE_SET=small", "FAKE_GO_PACKAGE="+first+"\n"+second,
			"FAKE_GO_SECOND_PACKAGE="+second, "FAKE_FOCUSED_ROWS="+row, "FAKE_FOCUSED_ROWS_FULL="+row+"\n^TestElsewhere$/^two$",
			"FAKE_FOCUSED_SCENARIOS="+others, "FAKE_GO_SUITE="+fakeRowSuite)

		stdout, stderr, status := run.exec(t, "focused", "Test")
		if status != 0 {
			t.Fatalf("others %q: status %d\n%s%s", others, status, stdout, stderr)
		}

		if suites := slices.DeleteFunc(run.goCalls(t), func(call string) bool { return !strings.HasPrefix(call, "suite ") }); !slices.Equal(suites, want) {
			t.Errorf("others %q: the suite ran as\n%s\nwant\n%s", others, strings.Join(suites, "\n"), strings.Join(want, "\n"))
		}

		if _, log := run.logFile(t); !strings.Contains(log, "forge_ci_live_set=small\n") {
			t.Errorf("the log's header does not record the run set:\n%s", log)
		}
	}
}

// A delta run of the full set runs what the pattern matches beside its rows
// less the tables' tests and the scenarios Go says its set after ran, and
// its header records the set after.
func TestRunLiveTests_ADeltaRunSkipsWhatItsSetAfterRan(t *testing.T) {
	first := filepath.Join(t.TempDir(), "packages", "first")
	if err := os.MkdirAll(first, 0o700); err != nil {
		t.Fatal(err)
	}

	const (
		row   = "^TestTrial$/^one$"
		flags = " -test.paniconexit0 -test.count=1 -test.timeout=3h -test.v -test.run "
	)

	run := newLiveRun(t, "FORGE_CI_LIVE_PREPARED="+preparedDir(t), "FORGE_CI_LIVE_SET=full", "FORGE_CI_LIVE_AFTER=medium",
		"FAKE_GO_PACKAGE="+first, "FAKE_GO_SECOND_PACKAGE=", "FAKE_FOCUSED_ROWS="+row,
		"FAKE_FOCUSED_SCENARIOS=Test", "FAKE_FOCUSED_SKIP=^TestLane$", "FAKE_GO_SUITE="+fakeRowSuite)

	stdout, stderr, status := run.exec(t, "focused", "Test")
	if status != 0 {
		t.Fatalf("status %d\n%s%s", status, stdout, stderr)
	}

	want := []string{"suite first" + flags + row, "suite first" + flags + "Test -test.skip ^TestTrial$|^TestLane$"}
	if suites := slices.DeleteFunc(run.goCalls(t), func(call string) bool { return !strings.HasPrefix(call, "suite ") }); !slices.Equal(suites, want) {
		t.Errorf("the suite ran as\n%s\nwant\n%s", strings.Join(suites, "\n"), strings.Join(want, "\n"))
	}

	_, log := run.logFile(t)
	if !strings.Contains(log, "forge_ci_live_set=full\nforge_ci_live_after=medium\n") {
		t.Errorf("the log's header does not record the delta:\n%s", log)
	}

	sum := sha256.Sum256([]byte(preparedEnv))
	if want := "forge_ci_live_prepared_env_sha256=" + hex.EncodeToString(sum[:]) + "\n"; !strings.Contains(log, want) {
		t.Errorf("the log's header does not record %q:\n%s", want, log)
	}
}

// fakeRerunSuite is a compiled suite whose rows end by their names. A row
// "flaky" fails its first process on a dependency the engine could not reach
// and passes its second, "down" fails every process so, "defect" fails on
// another engine error, "limit" runs into the time limit after the same
// error, "spoof" fails with the phrase where a program of the job printed it
// and the engine's line in the output of a command of the harness, and any
// other row passes. Each process records where
// it would keep its job logs.
const fakeRerunSuite = `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == -test.list ]]; then printf 'TestTrial\n'; exit 0; fi
printf 'suite %s\njob-logs %s\n' "$*" "$FORGE_CI_LIVE_SNAPSHOT_EVIDENCE_DIR" >>"$FAKE_GO_ARGV"
begin() { printf '=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/row\n=== RUN   TestTrial/row/private\n'; }
trace() {
  printf '    layer_test.go:210: failure   job "Build: image"\n    layer_test.go:226: job "Build: image" log (tail):\n        %s\n        %s\n' "$1" "${2:-exitcode '69': failure}"
  printf 'FORGE_CI_LIVE_CONCLUSION TestTrial/row/private\n    layer_test.go:321: run 7 of release.yml ended "failure", want success\n'
}
ended() { printf '        --- %s: TestTrial/row/private (1.00s)\n    --- %s: TestTrial/row (1.00s)\n--- %s: TestTrial (1.00s)\n%s\n' "$1" "$1" "$1" "$1"; }
unreached='2026-11-11T12:23:38.9347345Z Error: download t.tar.gz: dial tcp: lookup github.com on 10.201.0.1:53: i/o timeout: dependency unavailable'
case "$*" in
*-test.skip*) printf 'testing: warning: no tests to run\nPASS\n'; exit 0 ;;
*flaky*)
  seen=0
  while IFS= read -r call; do [[ "$call" != suite*flaky* ]] || seen=$((seen + 1)); done <"$FAKE_GO_ARGV"
  begin
  if [[ "$seen" -gt 1 ]]; then ended PASS; exit 0; fi
  trace "$unreached"
  ;;
*down*) begin; trace "$unreached" ;;
*defect*) begin; trace 'Error: artifact build-dist not found' "exitcode '1': failure" ;;
*limit*) begin; trace "$unreached"; printf '        --- FAIL: TestTrial/row/private (1.00s)\n=== RUN   TestTrial/row/public\npanic: test timed out after 3h0m0s\n'; exit 2 ;;
*spoof*) begin; trace '[INFO] the mirror said: dependency unavailable'; printf '    trial_test.go:70: git push:\n        %s\n' "$unreached" ;;
*) begin; ended PASS; exit 0 ;;
esac
ended FAIL
exit 1
`

// A row that failed on an unavailable dependency alone runs again once, after
// the run's other rows and the rest of the selection, in the same invocation.
// Its rerun is the row's result: the run passes when it does, with both
// attempts in the log and in verdict.json and the rerun's job logs in a
// directory of their own. Nothing else runs again: not a row that failed on
// something else, ran into the time limit or only printed the phrase, and
// not a rerun that failed.
func TestRunLiveTests_RerunsARowThatFailedOnAnUnavailableDependencyOnce(t *testing.T) {
	classifier := buildLiveClassifier(t)

	const flags = "suite -test.paniconexit0 -test.count=1 -test.timeout=3h -test.v -test.run "

	row := func(name string) string { return "^TestTrial$/^" + name + "$" }
	rest := flags + "TestTrial -test.skip ^TestTrial$"

	for _, tc := range []struct {
		name   string
		rows   []string
		status int
		result string
		reruns []string
		ended  []string
	}{
		{"the rerun passes", []string{"flaky", "one"}, 0, "Live result: passed: 1 row(s) on a rerun after an unavailable dependency; verdict.json has both attempts\n",
			[]string{"flaky"}, []string{"failed flaky", "passed one", "passed flaky"}},
		{"the rerun fails", []string{"down", "one"}, 1, "Live result: failed\n", []string{"down"}, []string{"failed down", "passed one", "failed down"}},
		{"only the unavailable row", []string{"defect", "limit", "spoof", "flaky"}, 1, "Live result: failed\n",
			[]string{"flaky"}, []string{"failed defect", "failed limit", "failed spoof", "failed flaky", "passed flaky"}},
		{"no row failed", []string{"one", "two"}, 0, "Live result: passed\n", nil, []string{"passed one", "passed two"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := make([]string, 0, len(tc.rows))
			want := make([]string, 0, len(tc.rows)+1+len(tc.reruns))

			for _, name := range tc.rows {
				rows = append(rows, row(name))
				want = append(want, flags+row(name))
			}

			want = append(want, rest)
			for _, name := range tc.reruns {
				want = append(want, flags+row(name))
			}

			run := newLiveRun(t, "FAKE_REAL_CLASSIFIER="+classifier, "FORGE_CI_LIVE_PREPARED="+preparedDir(t),
				"FAKE_FOCUSED_ROWS="+strings.Join(rows, "\n"), "FAKE_GO_SUITE="+fakeRerunSuite)

			stdout, stderr, status := run.exec(t, "focused", "TestTrial")
			if output := stdout + stderr; status != tc.status || !strings.Contains(output, tc.result) {
				t.Fatalf("status %d, want %d and %q\n%s", status, tc.status, tc.result, output)
			}

			calls := run.goCalls(t)
			if suites := slices.DeleteFunc(slices.Clone(calls), func(call string) bool { return !strings.HasPrefix(call, "suite ") }); !slices.Equal(suites, want) {
				t.Errorf("the suite ran as\n%s\nwant\n%s", strings.Join(suites, "\n"), strings.Join(want, "\n"))
			}

			logPath, log := run.logFile(t)

			matches := regexp.MustCompile(`(?m)^FORGE_CI_LIVE_ROW (\S+) \d+m\d\ds \^TestTrial\$/\^(\w+)\$$`).FindAllStringSubmatch(log, -1)
			ended := make([]string, 0, len(matches))

			for _, match := range matches {
				ended = append(ended, match[1]+" "+match[2])
			}

			if !slices.Equal(ended, tc.ended) {
				t.Errorf("the rows ended as %q, want %q", ended, tc.ended)
			}

			// A rerun, and nothing else, is marked and keeps its job logs apart.
			jobLogs := filepath.Join(filepath.Dir(logPath), "job-logs")
			if got := strings.Count(strings.Join(calls, "\n")+"\n", "job-logs "+filepath.Join(jobLogs, "rerun")+"\n"); got != len(tc.reruns) {
				t.Errorf("%d processes kept their job logs in rerun/, want %d\n%s", got, len(tc.reruns), strings.Join(calls, "\n"))
			}

			if got := strings.Count(log, "\nFORGE_CI_LIVE_RERUN "); got != len(tc.reruns) {
				t.Errorf("%d rerun marks in the log, want %d", got, len(tc.reruns))
			}

			// The operator is told why a row runs again, before it does.
			for _, name := range tc.reruns {
				if why := "\nLive cause: " + row(name) + ": infrastructure: name resolution (github.com); the row failed and runs again once\n" + "FORGE_CI_LIVE_PACKAGE_BEGIN "; !strings.Contains(log, why) {
					t.Errorf("the log lacks %q", why)
				}
			}

			verdict, err := os.ReadFile(filepath.Join(filepath.Dir(logPath), "verdict.json"))
			if err != nil {
				t.Fatal(err)
			}

			if got := strings.Count(string(verdict), `"cause": "infrastructure: name resolution (github.com)"`); len(tc.reruns) == 0 && (got != 0 || strings.Contains(string(verdict), `"reruns"`)) ||
				len(tc.reruns) != 0 && (!strings.Contains(string(verdict), `"reruns"`) || !strings.Contains(string(verdict), `"row": "`+row(tc.reruns[0])+`"`) || got == 0) {
				t.Errorf("verdict.json:\n%s", verdict)
			}
		})
	}
}

// A pattern the row selection refuses is a usage error: nothing is built,
// no lock is taken and no evidence is written.
func TestRunLiveTests_ARefusedPatternIsAUsageError(t *testing.T) {
	t.Parallel()

	run := newLiveRun(t, "FORGE_CI_LIVE_PREPARED="+preparedDir(t), "FAKE_FOCUSED_ROWS_REFUSED=1")

	stdout, stderr, status := run.exec(t, "focused", "^TestTrial$/^no-such-lane$")
	if status != 2 || !strings.Contains(stderr, "selects no row") {
		t.Fatalf("status %d\nstdout:\n%s\nstderr:\n%s", status, stdout, stderr)
	}

	requireNoEvidence(t, run)
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

	if calls := run.goCalls(t); len(calls) != 2 || calls[0] != "run ./test/live/internal/livetest/preflight" || calls[1] != hostToolsPreflight {
		t.Fatalf("expected only the preflight calls, got %v", calls)
	}

	requireNoEvidence(t, run)
}

// A preflight writes nothing to the lab, so it runs beside a run that holds
// the lock, and its log says it was a preflight.
func TestRunLiveTests_PreflightRunsBesideAHeldLock(t *testing.T) {
	t.Parallel()
	run := newLiveRun(t, "FORGE_CI_LIVE_PREFLIGHT=1")

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

	stdout, stderr, status := run.exec(t, "full")
	if status != 0 || !strings.Contains(stdout, "live: preflight: each scenario stops at its admission") {
		t.Fatalf("status %d\nstdout:\n%s\nstderr:\n%s", status, stdout, stderr)
	}

	if _, log := run.logFile(t); !strings.Contains(log, "\npreflight=1\n") {
		t.Fatalf("the log header does not mark the preflight:\n%s", log)
	}

	invalid := newLiveRun(t, "FORGE_CI_LIVE_PREFLIGHT=yes")
	if _, stderr, status := invalid.exec(t, "full"); status != 2 || !strings.Contains(stderr, "FORGE_CI_LIVE_PREFLIGHT must be 1 or unset") {
		t.Fatalf("an invalid preflight setting ran: %d, %s", status, stderr)
	}
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

	// The held lane lives while the other lanes run, however long a loaded
	// machine takes for them; only a hang ends it, as in exec. A deadline
	// over the whole test raced the load and killed the held lane.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	command := exec.CommandContext(ctx, "bash", script, "focused", "TestFake")
	command.Env = first.env

	var out, errOut strings.Builder

	command.Stdout, command.Stderr = &out, &errOut
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	exited := make(chan struct{})

	var waitErr error

	go func() {
		waitErr = command.Wait()

		close(exited)
	}()

	defer func() {
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Error(err)
		}

		select {
		case <-exited:
		case <-time.After(liveCommandBound):
			cancel()
			<-exited
			t.Errorf("held provider lane hung after its release\n%s\n%s", out.String(), errOut.String())

			return
		}

		if waitErr != nil {
			t.Errorf("held provider lane: %v\n%s\n%s", waitErr, out.String(), errOut.String())
		}
	}()

	if err := awaitHeldLane(ctx, exited, ready); err != nil {
		t.Fatalf("first provider lane did not reach its held scenario: %v\n%s\n%s", err, out.String(), errOut.String())
	}

	// Another provider overlaps, and so does the same provider as another
	// lab account; the same account and an all-provider run are refused.
	for _, lane := range []struct {
		scope, account string
		want           int
	}{{"gitlab", "forgeci", 0}, {"forgejo", "forgeci2", 0}, {"forgejo", "forgeci", 2}, {"all", "forgeci", 2}} {
		scope, want := lane.scope, lane.want
		second := newLiveRun(t, "FORGE_CI_LIVE_PROVIDER="+scope, "FAKE_LAB_ACCOUNT="+lane.account, "FAKE_GO_TEST_OUTPUT="+fakeSuiteOutput,
			"XDG_STATE_HOME="+first.stateHome)
		stdout, stderr, status := second.exec(t, "focused", "TestFake")

		if status != want {
			t.Fatalf("scope %s while Forgejo is active: status=%d, want=%d\n%s\n%s", scope, status, want, stdout, stderr)
		}
	}
}

// heldLanePoll is how often a test looks for the held lane's ready mark.
const heldLanePoll = 10 * time.Millisecond

// awaitHeldLane waits until the held lane marks ready, and fails at once when
// it exits first; liveCommandBound only catches a hang.
func awaitHeldLane(ctx context.Context, exited <-chan struct{}, ready string) error {
	ctx, cancel := context.WithTimeout(ctx, liveCommandBound)
	defer cancel()

	return retry.Run(ctx, nil, int(liveCommandBound/heldLanePoll), heldLanePoll, func() error {
		_, err := os.Stat(ready)
		if err == nil {
			return nil
		}

		select {
		case <-exited:
			if _, err := os.Stat(ready); err == nil {
				return nil
			}

			return retry.Permanent(errors.New("it exited first"))
		default:
			return err
		}
	}, retry.WithBackoff(retry.Constant))
}

// A run that takes prepared images hands the prepared directory, its own
// commit and its working-tree state to the Go preflight, and builds and runs
// nothing when that refuses: the images are of one commit.
func TestRunLiveTests_RunsNothingOnAPreparedDirectoryOfAnotherCommit(t *testing.T) {
	t.Parallel()
	dir := preparedDir(t)
	run := newLiveRun(t, "FORGE_CI_LIVE_PREPARED="+dir, "FAKE_PREPARED_REFUSES=1", "FAKE_GO_TEST_OUTPUT="+fakeSuiteOutput)

	stdout, stderr, status := run.exec(t, "focused", "TestFake")
	if status != 2 {
		t.Fatalf("status %d\nstdout:\n%s\nstderr:\n%s", status, stdout, stderr)
	}

	calls := run.goCalls(t)
	prepared := regexp.MustCompile(`^run ./test/live/internal/livetest/preflight prepared-source ` + regexp.QuoteMeta(dir) + ` [0-9a-f]{40}( |$)`)

	if !slices.ContainsFunc(calls, prepared.MatchString) {
		t.Errorf("the preflight was not given the directory and the commit:\n%s", strings.Join(calls, "\n"))
	}

	if slices.ContainsFunc(calls, func(call string) bool {
		return strings.HasPrefix(call, "build") || strings.HasPrefix(call, "test") || strings.HasPrefix(call, "suite")
	}) {
		t.Errorf("the refused run built or ran something:\n%s", strings.Join(calls, "\n"))
	}
}
