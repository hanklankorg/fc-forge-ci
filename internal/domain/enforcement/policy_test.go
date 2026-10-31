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
		policy := Policy{bits&1 != 0, bits&2 != 0, bits&4 != 0, bits&8 != 0}
		for index, check := range []Check{Lint, Vulnerabilities, ApplicationTests, ReproducibleBuilds} {
			decision, err := policy.Decide(check, 2)
			if err != nil || decision.Findings != 2 || decision.Enforced != (bits&(1<<index) != 0) {
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
	if err != nil || policy.EnforceLint || !policy.EnforceVulnerabilities || !policy.EnforceApplicationTests || !policy.EnforceReproducibleBuilds {
		t.Fatalf("safe omitted defaults: %+v %v", policy, err)
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
		t.Setenv(name, "false")
	}

	if err := os.WriteFile(filepath.Join(root, "policy.json"), []byte(`{"enforce-lint":false,"enforce-vulnerabilities":false,"enforce-application-tests":false}`), 0o600); err != nil {
		t.Fatal(err)
	}

	after, err := Current()
	if err != nil || after != before || Digest() != digest {
		t.Fatalf("consumer state replaced policy: %+v %v", after, err)
	}

	after.EnforceLint = false

	unchanged, _ := Current()
	if unchanged != before {
		t.Fatal("a returned policy value mutated the engine")
	}
}

func TestNativeTestFailureClassification(t *testing.T) {
	const mavenFailure = "\n[ERROR] Failed to execute goal org.apache.maven.plugins:maven-surefire-plugin:3.5.4:test (default-test) on project app: There are test failures."
	const gradleFailure = "\nFAILURE: Build failed with an exception.\n\n* What went wrong:\nExecution failed for task ':test'.\n> There were failing tests. See the report at: file:///fixture/build/reports/tests/test/index.html\n\n* Try:\n> Run with --scan to get full insights.\n\nBUILD FAILED in 2s\n"
	for _, test := range []struct {
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
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := FailedTests(test.protocol, test.body); got != test.count {
				t.Fatalf("count=%d want %d", got, test.count)
			}
		})
	}
}
