// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClassifyLiveRunRequiresAPassingLeaf(t *testing.T) {
	for _, tc := range []struct {
		name, log, verdict string
		status, want       int
	}{
		{"leaf", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/public\n    --- PASS: TestTrial/public (1s)\n--- PASS: TestTrial (1s)"), "passed", 0, 0},
		{"entrypoint leaf", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (1s)"), "passed", 0, 0},
		{"skipped children", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/public\n--- SKIP: TestTrial/public (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"offline pass and live skip", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestOffline\n--- PASS: TestOffline (0s)\n--- SKIP: TestTrial (0s)"), "skipped:", 0, 1},
		{"offline pass and skipped children", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/public\n=== RUN   TestOffline\n--- PASS: TestOffline (0s)\n--- SKIP: TestTrial/public (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"unregistered", livePackageLog("one", "=== RUN   TestOffline\n--- PASS: TestOffline (0s)"), "skipped:", 0, 1},
		{"unbounded", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)", "skipped:", 0, 1},
		{"same name another package", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- SKIP: TestTrial (0s)") + livePackageLog("two", "=== RUN   TestTrial\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"same package another invocation", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- SKIP: TestTrial (0s)") + livePackageLog("one", "=== RUN   TestTrial\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"other package descendants", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)") + livePackageLog("two", "=== RUN   TestTrial/child\n--- SKIP: TestTrial/child (0s)"), "passed", 0, 0},
		{"name prefix is not ancestry", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrialOffline\n--- SKIP: TestTrial (0s)\n--- PASS: TestTrialOffline (0s)"), "skipped:", 0, 1},
		{"quoted PASS and skipped entrypoint", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n    trial_test.go:10: captured output:\n        --- PASS: TestTrial/reported-child (0.00s)\n--- SKIP: TestTrial (0.00s)\nPASS"), "skipped:", 0, 1},
		{"missing RUN", livePackageLog("one", "FORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"missing entrypoint RUN", livePackageLog("one", "FORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/child\n--- PASS: TestTrial/child (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"missing child RUN", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial/child (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"quoted RUN is not a RUN", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n        === RUN   TestTrial/child\n        --- PASS: TestTrial/child (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"missing entrypoint result", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/child\n--- PASS: TestTrial/child (0s)"), "skipped:", 0, 1},
		{"skipped entrypoint with passing child", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/child\n--- PASS: TestTrial/child (0s)\n--- SKIP: TestTrial (0s)"), "skipped:", 0, 1},
		{"missing actual ancestor result", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/group\n=== RUN   TestTrial/group/child\n--- PASS: TestTrial/group/child (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"skipped actual ancestor", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/group\n=== RUN   TestTrial/group/child\n--- PASS: TestTrial/group/child (0s)\n--- SKIP: TestTrial/group (0s)\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"literal slash is not an invented ancestor", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/sha1/application\n    --- PASS: TestTrial/sha1/application (0s)\n--- PASS: TestTrial (0s)"), "passed", 0, 0},
		{"successful actual ancestors", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/group\n=== RUN   TestTrial/group/child\n--- PASS: TestTrial/group/child (0s)\n--- PASS: TestTrial/group (0s)\n--- PASS: TestTrial (0s)"), "passed", 0, 0},
		{"RUN without child result prevents parent leaf", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/child\n--- PASS: TestTrial (0s)"), "skipped:", 0, 1},
		{"duplicate terminal result", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- SKIP: TestTrial (0s)\n--- PASS: TestTrial (0s)"), "failed", 0, 1},
		{"duplicate RUN", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial\n--- PASS: TestTrial (0s)"), "failed", 0, 1},
		{"nested boundaries", "FORGE_CI_LIVE_PACKAGE_BEGIN one\n" + livePackageLog("two", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)") + "FORGE_CI_LIVE_PACKAGE_END one\n", "failed", 0, 1},
		{"orphan end after valid pass", livePackageLog("one", "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)") + "FORGE_CI_LIVE_PACKAGE_END one\n", "failed", 0, 1},
		{"unclosed package", "FORGE_CI_LIVE_PACKAGE_BEGIN one\nFORGE_CI_LIVE_SCENARIO TestTrial\n--- PASS: TestTrial (0s)", "failed", 0, 1},
		{"wrong end", "FORGE_CI_LIVE_PACKAGE_BEGIN one\nFORGE_CI_LIVE_PACKAGE_END two\n", "failed", 0, 1},
		{"package only", "PASS\nok example/package", "skipped:", 0, 1},
		{"failed despite zero status", "--- PASS: TestOne (0s)\n--- FAIL: TestTwo (0s)", "failed", 0, 1},
		{"suite failure", "--- PASS: TestOne (0s)", "failed", 7, 7},
		{"lab failed every failure", livePackageLog("one", liveInfrastructureLog), "infrastructure", 1, 1},
		{"lab and product failures", livePackageLog("one", liveInfrastructureLog+"\n=== RUN   TestOther\n--- FAIL: TestOther (0s)"), "failed", 1, 1},
		{"lab failure and an unfinished test", livePackageLog("one", liveInfrastructureLog+"\n=== RUN   TestOther"), "failed", 1, 1},
		{"lab marker of another test", livePackageLog("one", strings.Replace(liveInfrastructureLog, "INFRASTRUCTURE TestTrial/public", "INFRASTRUCTURE TestElse", 1)), "failed", 1, 1},
		{"lab marker in another package", livePackageLog("one", "FORGE_CI_LIVE_INFRASTRUCTURE TestTrial/public Build: runner_system_failure") + livePackageLog("two", strings.Replace(liveInfrastructureLog, "FORGE_CI_LIVE_INFRASTRUCTURE TestTrial/public Build: runner_system_failure\n", "", 1)), "failed", 1, 1},
		{"lab marker on a clean exit", livePackageLog("one", liveInfrastructureLog), "failed", 0, 1},
		{"interrupted lab failure", livePackageLog("one", liveInfrastructureLog), "interrupted", 130, 130},
		{"interrupted", "--- PASS: TestOne (0s)", "interrupted", 130, 130},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ClassifyLiveRun(strings.NewReader(tc.log), tc.status)
			if err != nil || got.ExitCode != tc.want || !strings.HasPrefix(got.Verdict, tc.verdict) {
				t.Fatalf("classification: %+v, %v", got, err)
			}
		})
	}

	if got, err := ClassifyLiveRun(failedLiveLog{}, 0); err == nil || got.ExitCode == 0 {
		t.Fatalf("unreadable evidence was accepted: %+v, %v", got, err)
	}
}

// A focused run's caller selected each scenario, so one that skipped itself
// or every subtest leaves the run not covered though another passed. A full
// run, where a scenario may skip for what the lab lacks, still passes.
func TestReadLiveFocusedRunRequiresAPassingLeafOfEachScenario(t *testing.T) {
	t.Parallel()

	const passing = "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/private\n    --- PASS: TestTrial/private (1s)\n--- PASS: TestTrial (1s)\n"

	for _, tc := range []struct {
		name, log, verdict string
		want               int
	}{
		{"every scenario ran", livePackageLog("one", passing), verdictPassed, 0},
		{"skipped subtests beside a pass", livePackageLog("one", passing+"=== RUN   TestOther\nFORGE_CI_LIVE_SCENARIO TestOther\n=== RUN   TestOther/public\n=== RUN   TestOther/private\n    --- SKIP: TestOther/public (0s)\n    --- SKIP: TestOther/private (0s)\n--- PASS: TestOther (0s)"), verdictNotCovered, 1},
		{"skipped scenario beside a pass", livePackageLog("one", passing+"=== RUN   TestOther\nFORGE_CI_LIVE_SCENARIO TestOther\n--- SKIP: TestOther (0s)"), verdictNotCovered, 1},
		{"skipped in another package", livePackageLog("one", passing) + livePackageLog("two", "=== RUN   TestOther\nFORGE_CI_LIVE_SCENARIO TestOther\n--- SKIP: TestOther (0s)"), verdictNotCovered, 1},
		{"nothing ran", livePackageLog("one", "=== RUN   TestOther\nFORGE_CI_LIVE_SCENARIO TestOther\n--- SKIP: TestOther (0s)"), verdictSkipped, 1},
	} {
		got, err := ReadLiveFocusedRun(strings.NewReader(tc.log), 0)
		if err != nil || got.Verdict != tc.verdict || got.ExitCode != tc.want || !strings.HasPrefix(got.Result().Verdict, tc.verdict) {
			t.Errorf("%s: focused: %s, exit %d, %v", tc.name, got.Verdict, got.ExitCode, err)
		}

		if full, err := ReadLiveRun(strings.NewReader(tc.log), 0); err != nil || (tc.verdict == verdictNotCovered && full.Verdict != verdictPassed) {
			t.Errorf("%s: full: %s, %v", tc.name, full.Verdict, err)
		}
	}
}

// The run script marks the process of a table row it ran alone. There the
// row's case is a subtest of the scenario, so a scenario that passed with
// its case alone, the pattern or the visibility having left the case no
// leaf, is not covered, and neither is a row that ran no scenario. Without
// the mark the same records pass.
func TestReadLiveFocusedRunRequiresALeafBelowARowsCase(t *testing.T) {
	t.Parallel()

	const (
		row     = "FORGE_CI_LIVE_ROW passed 0m07s ^TestTrial$/^lane$/^(public)$"
		passing = "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/lane/demo\n=== RUN   TestTrial/lane/demo/public\n" +
			"        --- PASS: TestTrial/lane/demo/public (1s)\n    --- PASS: TestTrial/lane/demo (1s)\n--- PASS: TestTrial (1s)\n"
		empty = "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/lane/demo\n    --- PASS: TestTrial/lane/demo (0s)\n--- PASS: TestTrial (0s)\n"
	)

	for _, tc := range []struct {
		name, log, verdict string
	}{
		{"a row with a leaf", livePackageLog("one", passing+row), verdictPassed},
		{"a row whose case ran no leaf", livePackageLog("one", empty+row), verdictSkipped},
		{"such a row beside a passing row", livePackageLog("one", empty+row) + livePackageLog("one", passing+row), verdictNotCovered},
		{"a row that ran no scenario", livePackageLog("one", "PASS\n"+row) + livePackageLog("one", passing+row), verdictNotCovered},
		{"the same case outside a row", livePackageLog("one", empty), verdictPassed},
		{"a mark of another package", livePackageLog("one", passing+row) + livePackageLog("one", empty), verdictPassed},
	} {
		got, err := ReadLiveFocusedRun(strings.NewReader(tc.log), 0)
		if err != nil || got.Verdict != tc.verdict || (got.ExitCode == 0) != (tc.verdict == verdictPassed) {
			t.Errorf("%s: %s, exit %d, %v", tc.name, got.Verdict, got.ExitCode, err)
		}
	}
}

func livePackageLog(name, body string) string {
	return livePackageBegin + name + "\n" + body + "\n" + livePackageEnd + name + "\n"
}

type failedLiveLog struct{}

func (failedLiveLog) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// liveInfrastructureLog is one package's run whose only failure the lab
// caused: the leaf is marked, and its parent fails because the leaf did.
const liveInfrastructureLog = "=== RUN   TestTrial\nFORGE_CI_LIVE_SCENARIO TestTrial\n=== RUN   TestTrial/public\n" +
	"FORGE_CI_LIVE_INFRASTRUCTURE TestTrial/public Build: runner_system_failure\n" +
	"    --- FAIL: TestTrial/public (2.50s)\n--- FAIL: TestTrial (3.00s)"

// liveVerdictFixtureLog is a retained log as run-live-tests.sh writes it: the
// header with its preflight report, then the packages' -v output. The header
// is for the reader; the verdict takes the run's fields from the script.
const liveVerdictFixtureLog = `started_at=2026-11-01T10:00:00Z
source_commit=e1166295
profile=focused
provider=gitlab
scenario=TestTrial\|TestOffline
forge_ci_live_consumer=/work/consumer
forge_ci_live_consumer_settings=''
live preflight: contract accepted
  profile=not-a-header-field
FORGE_CI_LIVE_PACKAGE_BEGIN /src/test/live/conformance
=== RUN   TestTrial
FORGE_CI_LIVE_SCENARIO TestTrial
=== RUN   TestTrial/public
    trial_test.go:40: job "Check: lint" log (tail):
        Admin policy sha256:` + liveVerdictPolicy + `: lint result=fail count=2 enforcement=true
        Admin policy sha256:` + liveVerdictPolicy + `: secrets result=not-waivable enforcement=true reason="required"
=== RUN   TestTrial/private
FORGE_CI_LIVE_INFRASTRUCTURE TestTrial/private Build: release files: stuck_or_timeout_failure
    trial_test.go:40: job "Check: lint" log (tail):
        Enforcement policy default-sha256:` + liveVerdictPolicy + `: lint result=fail count=2 enforcement=true
    --- PASS: TestTrial/public (61.25s)
    --- FAIL: TestTrial/private (12.00s)
--- FAIL: TestTrial (73.25s)
=== RUN   TestOffline
--- SKIP: TestOffline (0.00s)
FAIL
FORGE_CI_LIVE_PACKAGE_END /src/test/live/conformance
`

const liveVerdictPolicy = "abababababababababababababababababababababababababababababababab"

func TestReadLiveRun_ObservedFindingsReceipt(t *testing.T) {
	t.Parallel()

	for _, enforced := range []bool{false, true} {
		line := fmt.Sprintf("Enforcement policy default-sha256:%s: vulnerabilities result=findings count=2 enforcement=%t source=flag blocking-count=0\n", liveVerdictPolicy, enforced)
		verdict, err := ReadLiveRun(strings.NewReader(line), 0)

		want := []LiveAdminPolicyDecision{{PolicySHA256: liveVerdictPolicy, Check: "vulnerabilities", Result: "findings", Enforcement: enforced}}
		if err != nil || !reflect.DeepEqual(verdict.AdminPolicy, want) {
			t.Fatalf("receipt not retained: %+v, %v", verdict.AdminPolicy, err)
		}
	}
}

func TestReadLiveRun_WritesTheVerdictFromTheRetainedLog(t *testing.T) {
	t.Parallel()

	verdict, err := ReadLiveRun(strings.NewReader(liveVerdictFixtureLog), 1)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := WriteLiveRunVerdict(dir, verdict); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "verdict.json")

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("verdict.json: %v, mode %v", err, info)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var got LiveRunVerdict
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("verdict.json is not JSON: %v\n%s", err, body)
	}

	conformance := "/src/test/live/conformance"
	want := LiveRunVerdict{
		Verdict:  "infrastructure",
		ExitCode: 1,
		Tests: []LeafTestResult{
			{Name: "TestTrial/public", Package: conformance, Result: "pass", ElapsedSeconds: 61.25},
			{Name: "TestTrial/private", Package: conformance, Result: "fail", ElapsedSeconds: 12},
			{Name: "TestOffline", Package: conformance, Result: "skip"},
		},
		Infrastructure: []LiveInfrastructureFailure{
			{Test: "TestTrial/private", Package: conformance, Detail: "Build: release files: stuck_or_timeout_failure"},
		},
		AdminPolicy: []LiveAdminPolicyDecision{
			{PolicySHA256: liveVerdictPolicy, Check: "lint", Result: "fail", Enforcement: true},
			{PolicySHA256: liveVerdictPolicy, Check: "secrets", Result: "not-waivable", Enforcement: true},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verdict.json:\n%s\nwant %+v", body, want)
	}

	if result := got.Result(); result.ExitCode != 1 || result.Verdict != "infrastructure" {
		t.Fatalf("Live result: %+v", result)
	}
}

// A run in which nothing ran still writes a verdict a tool can read: an empty
// list of tests, not null, and the human line's wording kept out of it.
func TestWriteLiveRunVerdict_SkippedRunHasNoTests(t *testing.T) {
	t.Parallel()

	verdict, err := ReadLiveRun(strings.NewReader("started_at=2026-11-01T10:00:00Z\n"), 0)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := WriteLiveRunVerdict(dir, verdict); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "verdict.json"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(body), `"verdict": "skipped"`) || !strings.Contains(string(body), `"tests": []`) {
		t.Fatalf("verdict.json:\n%s", body)
	}

	if result := verdict.Result(); result.ExitCode != 1 || !strings.HasPrefix(result.Verdict, "skipped: ") {
		t.Fatalf("Live result: %+v", result)
	}
}

// The run's header comes from the script's values, and of the inputs only
// those the log's header records: another FORGE_CI_LIVE_* variable may hold
// a credential.
func TestLiveRunHeaderFrom_TakesOnlyTheRecordedInputs(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"FORGE_CI_LIVE_RUN_STARTED_AT":    "2026-11-01T10:00:00Z",
		"FORGE_CI_LIVE_RUN_SOURCE_COMMIT": "e1166295",
		"FORGE_CI_LIVE_PROFILE":           "focused",
		"FORGE_CI_LIVE_PROVIDER":          "gitlab",
		"FORGE_CI_LIVE_RUN_SCENARIO":      "TestTrial|TestOffline $'x'",
		"FORGE_CI_LIVE_CONSUMER":          "/work/consumer",
		"FORGE_CI_LIVE_SIGNING_KEY_DIR":   "/secret/keys",
		"FORGE_CI_LIVE_REGISTRY_TOKEN":    "glpat-not-recorded",
	}

	got := LiveRunHeaderFrom(func(name string) string { return env[name] })
	want := LiveRunHeader{
		StartedAt: "2026-11-01T10:00:00Z", SourceCommit: "e1166295", Profile: "focused", Provider: "gitlab",
		Scenario: "TestTrial|TestOffline $'x'", Inputs: map[string]string{"forge_ci_live_consumer": "/work/consumer"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("header %+v, want %+v", got, want)
	}
}
