// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestPolicySettingsAreIndependent(t *testing.T) {
	for bits := range 16 {
		policy := Policy{EnforceLint: bits&1 != 0, EnforceVulnerabilities: bits&2 != 0, EnforceApplicationTests: bits&4 != 0, EnforceReproducibleBuilds: bits&8 != 0}
		for index, check := range []Check{Lint, Vulnerabilities, ApplicationTests, ReproducibleBuilds} {
			decision, err := policy.Decide(check, 2)
			if err != nil || decision.Findings != 2 || decision.BlockingCount != 2 || decision.Enforced != (bits&(1<<index) != 0) {
				t.Fatalf("settings=%d check=%s: %+v %v", bits, check, decision, err)
			}
		}
	}

	if _, err := (Policy{}).Decide("typo", 1); !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("unknown check: %v", err)
	}

	if _, err := (Policy{}).Decide(Lint, -1); !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("negative count: %v", err)
	}
}

func TestDecideCountsValidatesEligibleSubset(t *testing.T) {
	for _, counts := range [][2]int{{-1, 0}, {1, -1}, {1, 2}} {
		if _, err := (Policy{}).DecideCounts(Vulnerabilities, counts[0], counts[1]); !errors.Is(err, errs.ErrUsage) {
			t.Fatalf("counts %v: %v", counts, err)
		}
	}
	decision, err := (Policy{EnforceVulnerabilities: true}).DecideCounts(Vulnerabilities, 2, 0)
	if err != nil || decision.Findings != 2 || decision.BlockingCount != 0 || !decision.Enforced {
		t.Fatalf("observed nonblocking findings: %+v, %v", decision, err)
	}
}

func TestPolicyDecodeRefusesAmbiguity(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `{"enforce-lint":null}`, `{"enforce-lint":"false"}`,
		`{"enforce-lint":0}`, `{"enforce-reproducible-builds":"no"}`, `{"enforce-lint":true,"enforce-lint":false}`,
		`{"enforce_lint":false}`, `{} {}`, `{"enforce-lint":false`,
	} {
		if _, err := decode([]byte(body)); !errors.Is(err, errs.ErrInvalidConfig) {
			t.Errorf("accepted %s: %v", body, err)
		}
	}

	policy, err := decode([]byte(`{"enforce-lint":false}`))
	if err != nil || policy.EnforceLint || policy.EnforceVulnerabilities || policy.EnforceApplicationTests || policy.EnforceReproducibleBuilds {
		t.Fatalf("omitted report-only defaults: %+v %v", policy, err)
	}
}

func TestShippedPolicyReportsAllClassesAndAllowsExplicitEnforcement(t *testing.T) {
	t.Parallel()

	defaults, err := Current()
	if err != nil {
		t.Fatal(err)
	}

	for _, check := range Checks {
		decision, err := defaults.Decide(check, 2)
		if err != nil || decision.Enforced || decision.Findings != 2 || decision.Source != EmbeddedDefault {
			t.Fatalf("default %s lost findings or enabled enforcement: %+v %v", check, decision, err)
		}

		enabled, err := defaults.WithAdmin(Admin{check: "true"})
		if err != nil {
			t.Fatal(err)
		}

		for _, other := range Checks {
			decision, err := enabled.Decide(other, 2)
			if err != nil || decision.Enforced != (other == check) {
				t.Fatalf("enabling %s changed %s incorrectly: %+v %v", check, other, decision, err)
			}
		}
	}
}

func TestCurrentPolicyCannotBeReplacedByConsumerState(t *testing.T) {
	before, err := Current()
	if err != nil {
		t.Fatal(err)
	}

	digest := Digest()
	root := t.TempDir()
	t.Chdir(root)

	for _, name := range []string{"ENFORCE_LINT", "ENFORCE_VULNERABILITIES", "ENFORCE_APPLICATION_TESTS", "FORGE_CI_ENFORCE_LINT", "FORGE_CI_POLICY"} {
		t.Setenv(name, "true")
	}

	if err := os.WriteFile(filepath.Join(root, "policy.json"), []byte(`{"enforce-lint":true,"enforce-vulnerabilities":true,"enforce-application-tests":true,"enforce-reproducible-builds":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	after, err := Current()
	if err != nil || after != before || Digest() != digest {
		t.Fatalf("consumer state replaced policy: %+v %v", after, err)
	}

	after.EnforceLint = !after.EnforceLint

	unchanged, _ := Current()
	if unchanged != before {
		t.Fatal("a returned policy value mutated the engine")
	}
}

const (
	mavenFailure  = "\n[ERROR] Failed to execute goal org.apache.maven.plugins:maven-surefire-plugin:3.5.4:test (default-test) on project app: There are test failures."
	gradleFailure = "\nFAILURE: Build failed with an exception.\n\n* What went wrong:\nExecution failed for task ':test'.\n> There were failing tests. See the report at: file:///fixture/build/reports/tests/test/index.html\n\n* Try:\n> Run with --scan to get full insights.\n\nBUILD FAILED in 2s\n"
)

// nativeTestFixtures are complete outputs of each protocol and the number of
// failed tests a waiver may report for them; 0 is not waivable.
var nativeTestFixtures = []struct {
	name     string
	protocol TestProtocol
	body     string
	count    int
}{
	{"maven assertion", Maven, "Tests run: 4, Failures: 2, Errors: 0, Skipped: 0" + mavenFailure, 2},
	{"maven incomplete summary", Maven, "Tests run: 4, Failures: 2, Errors: 0, Skipped: 0", 0},
	{"maven mixed reactor compiler failure", Maven, "Tests run: 4, Failures: 2, Errors: 0, Skipped: 0" + mavenFailure + "\n[ERROR] Failed to execute goal org.apache.maven.plugins:maven-compiler-plugin:3.14.0:compile on project other: Compilation failure", 0},
	{"maven mixed reactor plugin failure", Maven, "Tests run: 4, Failures: 2, Errors: 0, Skipped: 0" + mavenFailure + "\n[ERROR] Failed to execute goal org.example:plugin:1:run on project other: network unavailable", 0},
	{"maven failed module then successful module", Maven, "Tests run: 4, Failures: 2, Errors: 0, Skipped: 0\nTests run: 3, Failures: 0, Errors: 0, Skipped: 0" + mavenFailure, 2},
	{"maven missing Docker", Maven, "Tests run: 84, Failures: 0, Errors: 7, Skipped: 0", 0},
	{"maven mixed failure and setup error", Maven, "Tests run: 4, Failures: 2, Errors: 1, Skipped: 0", 0},
	{"maven reactor setup error followed by assertions", Maven, "Tests run: 4, Failures: 0, Errors: 1, Skipped: 0\nTests run: 4, Failures: 1, Errors: 0, Skipped: 0", 0},
	{"maven compile", Maven, "COMPILATION ERROR", 0},
	{"node assertions", Node, "code: 'ERR_ASSERTION'\n# tests 3\n# pass 2\n# fail 1\n", 1},
	{"node import failure with summary", Node, "Cannot find module 'test.js'\n# tests 1\n# fail 1\n", 0},
	{"node mixed import and assertion failures", Node, "code: 'ERR_ASSERTION'\ncode: 'MODULE_NOT_FOUND'\n# tests 2\n# fail 2\n", 0},
	{"node startup", Node, "Cannot find module 'test.js'", 0},
	{"cargo assertions", Cargo, "test result: FAILED. 1 passed; 2 failed;", 2},
	{"cargo compile", Cargo, "test result: FAILED. 1 passed; 2 failed;\nerror: could not compile app", 0},
	{"gradle assertions", Gradle, "> Task :test FAILED\n4 tests completed, 2 failed\n" + gradleFailure, 2},
	{"gradle Android assertions", Gradle, strings.ReplaceAll("> Task :test FAILED\n4 tests completed, 2 failed\n"+gradleFailure, ":test", ":app:testDemoUnitTest"), 2},
	{"gradle no terminal", Gradle, "> Task :test FAILED\n4 tests completed, 2 failed\n", 0},
	{"gradle truncated terminal", Gradle, "> Task :test FAILED\n4 tests completed, 2 failed\n* What went wrong:\nExecution failed for task ':test'.\n> There were failing tests.\n", 0},
	{"gradle daemon after assertions", Gradle, "> Task :test FAILED\n4 tests completed, 2 failed\nFAILURE: Build failed with an exception.\n* What went wrong:\nGradle build daemon disappeared unexpectedly (it may have been killed or may have crashed)\n* Try:\n> Run with --stacktrace\n", 0},
	{"gradle daemon after assertion terminal", Gradle, "> Task :test FAILED\n4 tests completed, 2 failed\n" + gradleFailure + "The message received from the daemon indicates that the daemon has disappeared.\n", 0},
	{"gradle runtime cause", Gradle, "> Task :test FAILED\n4 tests completed, 2 failed\n" + strings.ReplaceAll(gradleFailure, "> There were failing tests. See the report at: file:///fixture/build/reports/tests/test/index.html", "> Process 'Gradle Test Executor 1' finished with non-zero exit value 1"), 0},
	{"gradle mixed terminal causes", Gradle, "> Task :test FAILED\n4 tests completed, 2 failed\n" + gradleFailure + "\n* What went wrong:\nCould not connect to the build daemon.\n* Try:\n> Retry\n", 0},
	{"gradle terminal task mismatch", Gradle, "> Task :test FAILED\n4 tests completed, 2 failed\n" + strings.ReplaceAll(gradleFailure, ":test", ":other:test"), 0},
	{"gradle mixed compile", Gradle, "> Task :test FAILED\n4 tests completed, 2 failed\n> Task :compileKotlin FAILED\n", 0},
	{"go assertions", GoJSON, "{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}", 1},
	{"go TestMain error", GoJSON, `{"Action":"fail","Package":"app"}`, 0},
	{"go mixed build", GoJSON, "{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"other\"}", 0},
	{"go malformed", GoJSON, "not JSON", 0},
	{"go panic", GoJSON, "{\"Action\":\"output\",\"Package\":\"app\",\"Test\":\"TestValue\",\"Output\":\"panic: assignment to entry in nil map [recovered]\\n\"}\n{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}", 0},
	{"go timeout", GoJSON, "{\"Action\":\"output\",\"Package\":\"app\",\"Test\":\"TestValue\",\"Output\":\"panic: test timed out after 10m0s\\n\"}\n{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}", 0},
	{"go fatal error", GoJSON, "{\"Action\":\"output\",\"Package\":\"app\",\"Output\":\"fatal error: concurrent map writes\\n\"}\n{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}", 0},
	{"go logged panic text", GoJSON, "{\"Action\":\"output\",\"Package\":\"app\",\"Test\":\"TestValue\",\"Output\":\"    value_test.go:9: panic: expected\\n\"}\n{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}", 1},
	{"go build failure", GoJSON, "{\"Action\":\"build-fail\",\"ImportPath\":\"app [app.test]\"}\n{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}", 0},
	{"go setup failure", GoJSON, "{\"Action\":\"output\",\"Package\":\"app\",\"Output\":\"FAIL\\tapp [setup failed]\\n\"}\n{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}", 0},
	{"go surrounding blank lines", GoJSON, "\n{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}\n\n", 1},
	{"go blank line inside events", GoJSON, "{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n\n{\"Action\":\"fail\",\"Package\":\"app\"}", 0},
	{"go empty", GoJSON, "", 0},
	{"maven coloured assertion", Maven, "[\x1b[1;31mERROR\x1b[m] Tests run: 4, Failures: 2, Errors: 0, Skipped: 0\n\x1b[1;31m[ERROR] Failed to execute goal org.apache.maven.plugins:maven-surefire-plugin:3.5.4:test (default-test) on project app: There are test failures.\x1b[m\n", 2},
	{"cargo build script", Cargo, "test result: FAILED. 1 passed; 2 failed;\nerror: failed to run custom build command for `app`", 0},
	{"node two summaries", Node, "code: 'ERR_ASSERTION'\n# fail 1\n# fail 1\n", 0},
	{"gradle compile only", Gradle, "> Task :compileJava FAILED\n" + gradleFailure, 0},
	{"gradle cause on the marker line", Gradle, "> Task :test FAILED\n4 tests completed, 2 failed\n" + strings.ReplaceAll(gradleFailure, "wrong:\n", "wrong: "), 0},
	{"xctest assertions", XCTest, "Executed 3 tests, with 1 failure (0 unexpected) in 0.1 seconds\nExecuted 5 tests, with 2 failures (0 unexpected) in 0.2 seconds\n** TEST FAILED **\n", 2},
	{"xctest unexpected last", XCTest, "Executed 5 tests, with 2 failures (0 unexpected)\nExecuted 5 tests, with 2 failures (1 unexpected)\n** TEST FAILED **\n", 0},
	{"xctest no verdict", XCTest, "Executed 5 tests, with 2 failures (0 unexpected)\n", 0},
}

func TestNativeTestFailureClassification(t *testing.T) {
	for _, test := range nativeTestFixtures {
		t.Run(test.name, func(t *testing.T) {
			if got := FailedTests(test.protocol, test.body); got != test.count {
				t.Fatalf("count=%d want %d", got, test.count)
			}
		})
	}
}

func TestAdminSettingsReplaceOnlyTheDefaultsTheyName(t *testing.T) {
	defaults := Policy{EnforceLint: true, EnforceVulnerabilities: true, EnforceApplicationTests: true, EnforceReproducibleBuilds: true}

	policy, err := defaults.WithAdmin(Admin{Lint: "false", ReproducibleBuilds: "true"})
	if err != nil {
		t.Fatal(err)
	}

	if policy.EnforceLint || !policy.EnforceVulnerabilities || !policy.EnforceApplicationTests || !policy.EnforceReproducibleBuilds {
		t.Fatalf("admin settings applied wrongly: %+v", policy)
	}

	for check, want := range map[Check]Source{Lint: Flag, ReproducibleBuilds: Flag, Vulnerabilities: EmbeddedDefault, ApplicationTests: EmbeddedDefault} {
		if got := policy.Source(check); got != want {
			t.Errorf("%s source %q, want %q", check, got, want)
		}

		decision, err := policy.Decide(check, 1)
		if err != nil || decision.Source != want {
			t.Errorf("%s decision %+v %v, want source %q", check, decision, err, want)
		}
	}

	if unchanged, err := defaults.WithAdmin(nil); err != nil || unchanged != defaults {
		t.Fatalf("no admin input changed the defaults: %+v %v", unchanged, err)
	}
}

func TestAdminSettingsAreAdmittedExactly(t *testing.T) {
	for _, raw := range []string{"", "TRUE", "False", "1", "0", "t", "f", "yes", "no", " true", "true\n", "on"} {
		if _, err := (Policy{}).WithAdmin(Admin{Lint: raw}); !errors.Is(err, errs.ErrUsage) {
			t.Errorf("admitted %q: %v", raw, err)
		}
	}

	if _, err := (Policy{}).WithAdmin(Admin{"typo": "false"}); !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("admitted an unknown setting: %v", err)
	}
}

func TestEnvironmentNamedLikeASettingIsRefused(t *testing.T) {
	for _, name := range []string{
		"ENFORCE_LINT", "enforce_lint", "FORGE_CI_ENFORCE_VULNERABILITIES", "RC_ENFORCE_APPLICATION_TESTS",
		"ENFORCE_REPRODUCIBLE_BUILDS", "CI_ENFORCE_REPRODUCIBLE_BUILDS",
	} {
		if err := RefuseEnvironment([]string{"PATH=/bin", name + "=true"}); !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s was not refused: %v", name, err)
		}
	}

	// Names that only resemble a setting, and the forge's rendering of an
	// action input, which keeps its hyphen, are not settings.
	for _, name := range []string{"ENFORCE_NAMESPACE_ON", "ENFORCE_LINTER", "RC_ADMIN_LINT", "ADMIN_VULNERABILITIES", "INPUT_ENFORCE-LINT", "NOENFORCE_LINT"} {
		if err := RefuseEnvironment([]string{name + "=false"}); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
}
