// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/enforcement"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

type runner struct {
	status int
	err    error
	called bool
	body   string
}

func (r *runner) Run(_ context.Context, out, _ io.Writer, _ []string) (int, error) {
	r.called = true
	_, _ = io.WriteString(out, r.body)

	return r.status, r.err
}

type testExitError int

func (code testExitError) Error() string { return "native test result" }
func (code testExitError) ExitCode() int { return int(code) }

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestDefaultFindingsRemainVisibleAndExplicitTrueBlocks(t *testing.T) {
	for _, check := range enforcement.Checks {
		var out bytes.Buffer
		if err := Findings(t.Context(), &out, check, 2); err != nil {
			t.Fatalf("default %s findings blocked: %v", check, err)
		}

		if !strings.Contains(out.String(), "result=findings count=2 enforcement=false source=embedded-default") {
			t.Fatalf("default %s findings were hidden: %s", check, &out)
		}

		out.Reset()

		ctx := WithAdmin(t.Context(), enforcement.Admin{check: "true"})
		if err := Findings(ctx, &out, check, 2); !errors.Is(err, errs.ErrValidation) {
			t.Fatalf("explicit %s enforcement did not block: %v", check, err)
		}

		if !strings.Contains(out.String(), "enforcement=true source=flag") {
			t.Fatalf("explicit %s source was lost: %s", check, &out)
		}
	}
}

func TestLintRunsAndKeepsInfrastructureFailuresBlocking(t *testing.T) {
	for _, enforced := range []bool{true, false} {
		for _, status := range []int{0, 1, 64, 69, 70, 78, 125, 130} {
			body := `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"nanolinter"}},"results":[]}]}`
			if status == 1 {
				body = `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"nanolinter"}},"results":[{"ruleId":"format","properties":{"source":"curated","check_id":"format","class":"formatter","status":"fail"}}]}]}`
			}

			r := &runner{status: status, body: body}

			var out bytes.Buffer

			err := nanolinter(t.Context(), enforcement.Policy{EnforceLint: enforced}, r, &out, io.Discard, []string{"nanolinter", "verify"}, "")
			if !r.called {
				t.Fatal("check was skipped")
			}

			wantError := status != 0 && (status != 1 || enforced)
			if (err != nil) != wantError {
				t.Fatalf("enforced=%t status=%d: %v", enforced, status, err)
			}

			if status == 1 && !strings.Contains(out.String(), "result=findings") {
				t.Fatalf("failure relabelled as pass: %s", &out)
			}
		}
	}

	if err := report(failedWriter{}, enforcement.Decision{Check: enforcement.Lint, Findings: 1}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("report failure suppressed: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := nanolinter(ctx, enforcement.Policy{}, &runner{status: 1}, io.Discard, io.Discard, []string{"nanolinter"}, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation suppressed: %v", err)
	}
}

// One check's runtime failure fails the command, but only after the findings
// of the others are written and reported.
func TestFindingsArePublishedBeforeACheckFailureIsReturned(t *testing.T) {
	body := `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"nanolinter"}},"results":[
{"ruleId":"sast","properties":{"source":"curated","check_id":"sast","class":"security","status":"fail","failure_reason_category":"network"}},
{"ruleId":"CVE-EXAMPLE","properties":{"source":"curated","check_id":"osv","class":"security","status":"fail"}}]}]}`

	for _, enforced := range []bool{false, true} {
		sarif := filepath.Join(t.TempDir(), "security.sarif")

		var out bytes.Buffer

		err := nanolinter(t.Context(), enforcement.Policy{EnforceVulnerabilities: enforced}, &runner{status: 1, body: body}, &out, io.Discard, []string{"nanolinter", "verify"}, sarif)
		if !errors.Is(err, errs.ErrPrerequisite) || errors.Is(err, errs.ErrValidation) != enforced {
			t.Fatalf("enforced=%t: %v", enforced, err)
		}

		written, readErr := os.ReadFile(sarif)
		if readErr != nil || !strings.Contains(string(written), "CVE-EXAMPLE") {
			t.Fatalf("security SARIF not written: %v\n%s", readErr, written)
		}

		for _, want := range []string{"vulnerabilities result=findings count=1", "lint result=incomplete count=0", "blocking-count=0 could-not-complete=sast\n"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("receipt lacks %q:\n%s", want, out.String())
			}
		}
	}
}

// A security check that was skipped did not find the code clean: the command
// fails whatever the enforcement, and never prints result=clean for its gate.
func TestSkippedSecurityCheckIsNeitherCleanNorWaived(t *testing.T) {
	const document = `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"nanolinter"}},"results":[{"ruleId":"osv","properties":{"source":"curated","check_id":"osv","class":"security","status":"%s"}}]}]}`

	for _, enforced := range []bool{false, true} {
		var out bytes.Buffer

		err := nanolinter(t.Context(), enforcement.Policy{EnforceVulnerabilities: enforced}, &runner{body: fmt.Sprintf(document, "skip")}, &out, io.Discard, []string{"nanolinter", "verify"}, "")
		if !errors.Is(err, errs.ErrPrerequisite) {
			t.Fatalf("enforced=%t: a skipped scan passed: %v", enforced, err)
		}

		if !strings.Contains(out.String(), "vulnerabilities result=incomplete count=0") || !strings.Contains(out.String(), "could-not-complete=osv\n") {
			t.Fatalf("skipped scan not reported as incomplete:\n%s", out.String())
		}

		out.Reset()

		if err := nanolinter(t.Context(), enforcement.Policy{EnforceVulnerabilities: enforced}, &runner{body: fmt.Sprintf(document, "n/a")}, &out, io.Discard, []string{"nanolinter", "verify"}, ""); err != nil {
			t.Fatalf("enforced=%t: a check that does not apply failed: %v", enforced, err)
		}

		if !strings.Contains(out.String(), "vulnerabilities result=not-applicable count=0") || !strings.Contains(out.String(), "not-applicable=osv\n") || !strings.Contains(out.String(), "lint result=clean") {
			t.Fatalf("inapplicable scan not reported as such:\n%s", out.String())
		}
	}
}

func TestOnlyFreshRecognizedTestFailuresMayContinue(t *testing.T) {
	for _, test := range []struct {
		name, body string
		err        error
		continued  bool
	}{
		{"assertions", "Tests run: 4, Failures: 1, Errors: 0, Skipped: 0\n[ERROR] Failed to execute goal org.apache.maven.plugins:maven-surefire-plugin:3.5.4:test on project app: There are test failures.", testExitError(1), true},
		{"mixed reactor failure", "Tests run: 4, Failures: 1, Errors: 0, Skipped: 0\n[ERROR] Failed to execute goal org.apache.maven.plugins:maven-surefire-plugin:3.5.4:test on project app: There are test failures.\n[ERROR] Failed to execute goal org.apache.maven.plugins:maven-compiler-plugin:3.14.0:compile on project other: Compilation failure", testExitError(1), false},
		{"missing socket", "Tests run: 4, Failures: 0, Errors: 1, Skipped: 0", testExitError(1), false},
		{"no report", "", testExitError(1), false},
		{"tool missing", "Tests run: 4, Failures: 1, Errors: 0, Skipped: 0", errs.ErrPrerequisite, false},
		{"cancelled", "Tests run: 4, Failures: 1, Errors: 0, Skipped: 0", context.Canceled, false},
		{"signal", "Tests run: 4, Failures: 1, Errors: 0, Skipped: 0", testExitError(137), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer

			continued, err := testRun(t.Context(), enforcement.Policy{}, &out, io.Discard, enforcement.Maven, func(stdout, _ io.Writer) error {
				_, _ = io.WriteString(stdout, test.body)

				return test.err
			})
			if continued != test.continued || (err == nil) != test.continued {
				t.Fatalf("continued=%t err=%v", continued, err)
			}

			if err != nil && !errors.Is(err, test.err) {
				t.Fatalf("original error lost: %v", err)
			}

			// Report-only must say why it did not continue past a failed run.
			explained := strings.Contains(out.String(), "application-tests result=not-waivable enforcement=false source=embedded-default reason=")
			if want := !test.continued; explained != want {
				t.Fatalf("not-waivable line present=%t, want %t:\n%s", explained, want, out.String())
			}
		})
	}
}

func TestTestOptOutCannotBecomeReportOnly(t *testing.T) {
	if err := RequireTests(t.Context(), true); !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("skip accepted: %v", err)
	}

	if err := RequireTests(t.Context(), false); err != nil {
		t.Fatal(err)
	}
}

func TestReportOnlyGradleRequiresAssertionOnlyTerminal(t *testing.T) {
	const (
		summary   = "> Task :app:testDebugUnitTest FAILED\n4 tests completed, 2 failed\n"
		assertion = "Execution failed for task ':app:testDebugUnitTest'.\n> There were failing tests. See the report at: file:///fixture/report.html"
	)
	for _, tc := range []struct {
		name, terminal string
		continued      bool
	}{
		{"assertions", assertion, true},
		{"daemon", "Gradle build daemon disappeared unexpectedly (it may have been killed or may have crashed)", false},
		{"runtime", "Execution failed for task ':app:testDebugUnitTest'.\n> Process 'Gradle Test Executor 1' finished with non-zero exit value 1", false},
		{"unknown", "Execution failed for task ':app:testDebugUnitTest'.\n> Could not write test results", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer

			body := summary + "FAILURE: Build failed with an exception.\n\n* What went wrong:\n" + tc.terminal + "\n\n* Try:\n> Run with --stacktrace\n\nBUILD FAILED in 2s\n"

			continued, err := testRun(t.Context(), enforcement.Policy{}, &out, io.Discard, enforcement.Gradle, func(stdout, _ io.Writer) error {
				_, _ = io.WriteString(stdout, body)

				return testExitError(1)
			})
			if continued != tc.continued || (err == nil) != tc.continued {
				t.Fatalf("continued=%t err=%v; want continued=%t", continued, err, tc.continued)
			}

			if tc.continued {
				if !strings.Contains(out.String(), "result=findings count=2 enforcement=false") {
					t.Fatalf("assertion findings lost: %s", &out)
				}
			} else if !errors.Is(err, testExitError(1)) || strings.Contains(out.String(), "result=findings") || !strings.Contains(out.String(), "result=not-waivable") {
				t.Fatalf("runtime failure was waived or lost: %v\n%s", err, &out)
			}
		})
	}
}

func TestReportOnlyGoTestsStream(t *testing.T) {
	const assertion = "{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}\n"

	passing := "{\"Action\":\"output\",\"Package\":\"app\",\"Test\":\"TestOther\",\"Output\":\"    other_test.go:9: " + strings.Repeat("x", 200) + "\\n\"}\n" +
		"{\"Action\":\"pass\",\"Package\":\"app\",\"Test\":\"TestOther\"}\n"

	for _, test := range []struct {
		name      string
		stdout    string
		stderr    string
		continued bool
		reason    string
	}{
		// More output than any whole-output buffer would hold.
		{"large suite", strings.Repeat(passing, (9<<20)/len(passing)) + assertion, "", true, ""},
		// stderr arrives while stdout is mid-line, from another goroutine.
		{"concurrent stderr", strings.Repeat(passing, 200) + assertion, strings.Repeat("go: downloading example.com/module v1.0.0\n", 200), true, ""},
		{"overlong line", "{\"Action\":\"output\",\"Package\":\"app\",\"Output\":\"" + strings.Repeat("x", enforcement.MaxTestLineBytes) + "\"}\n" + assertion, "", false, "a test output line exceeded 1 MiB"},
		{"panic", passing + "{\"Action\":\"output\",\"Package\":\"app\",\"Test\":\"TestValue\",\"Output\":\"panic: boom\\n\"}\n" + assertion, "", false, "no complete assertion-failure summary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer

			continued, err := testRun(t.Context(), enforcement.Policy{}, &out, io.Discard, enforcement.GoJSON, func(stdout, stderr io.Writer) error {
				done := make(chan struct{})

				go func() {
					defer close(done)

					write(stderr, test.stderr)
				}()

				write(stdout, test.stdout)
				<-done

				return testExitError(1)
			})
			if continued != test.continued || (err == nil) != test.continued {
				t.Fatalf("continued=%t err=%v\n%s", continued, err, &out)
			}

			if test.continued && !strings.Contains(out.String(), "result=findings count=1 enforcement=false") {
				t.Fatalf("assertion finding lost: %s", &out)
			}

			if !test.continued && !strings.Contains(out.String(), "result=not-waivable enforcement=false source=embedded-default reason=\""+test.reason) {
				t.Fatalf("reason lost: %s", &out)
			}
		})
	}
}

// write sends body in small pieces, as a pipe would.
func write(writer io.Writer, body string) {
	for chunk := range slices.Chunk([]byte(body), 4096-7) {
		_, _ = writer.Write(chunk)
	}
}

func TestFlagReachesTheGateAndItsReceipt(t *testing.T) {
	ctx := WithAdmin(t.Context(), enforcement.Admin{enforcement.Lint: "false"})

	var out bytes.Buffer
	if err := Findings(ctx, &out, enforcement.Lint, 3); err != nil {
		t.Fatalf("admin report-only lint still blocked: %v", err)
	}

	if !strings.Contains(out.String(), "lint result=findings count=3 enforcement=false source=flag blocking-count=3\n") {
		t.Fatalf("receipt does not show the admin input:\n%s", out.String())
	}

	out.Reset()

	if err := Findings(ctx, &out, enforcement.Vulnerabilities, 1); err != nil {
		t.Fatalf("an unset setting lost its report-only default: %v", err)
	}

	if !strings.Contains(out.String(), "vulnerabilities result=findings count=1 enforcement=false source=embedded-default blocking-count=1\n") {
		t.Fatalf("receipt does not show the default:\n%s", out.String())
	}

	if got := AdminArgs(ctx); !slices.Equal(got, []string{"--enforce-lint=false"}) {
		t.Fatalf("a child invocation would not get the admin input: %v", got)
	}

	if got := AdminArgs(t.Context()); len(got) != 0 {
		t.Fatalf("no admin input rendered as %v", got)
	}
}

func TestFindingsCountsWriteErrorsRemainBlocking(t *testing.T) {
	for _, enforced := range []string{"false", "true"} {
		ctx := WithAdmin(t.Context(), enforcement.Admin{enforcement.Vulnerabilities: enforced})
		for _, eligible := range []int{0, 1} {
			if err := FindingsCounts(ctx, failedWriter{}, enforcement.Vulnerabilities, 2, eligible); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("enforced=%s eligible=%d: %v", enforced, eligible, err)
			}
		}
	}
}

func TestShowPrintsEachSettingWithItsSource(t *testing.T) {
	var out bytes.Buffer
	if err := Show(WithAdmin(t.Context(), enforcement.Admin{enforcement.ApplicationTests: "false"}), &out); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{`"enforce-application-tests":false`, `"enforce-application-tests":"flag"`, `"enforce-lint":"embedded-default"`, `"default-sha256":"` + enforcement.Digest() + `"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("policy show lacks %s:\n%s", want, out.String())
		}
	}
}

// No gate takes a setting from the environment: one named like a setting is
// refused, whatever its value, with or without an admin input.
func TestGatesRefuseASettingFromTheEnvironment(t *testing.T) {
	t.Setenv("RC_ENFORCE_LINT", "false")

	for _, ctx := range []context.Context{t.Context(), WithAdmin(t.Context(), enforcement.Admin{enforcement.Lint: "true"})} {
		var out bytes.Buffer
		if err := Findings(ctx, &out, enforcement.Lint, 0); !errors.Is(err, errs.ErrUsage) || out.Len() != 0 {
			t.Fatalf("an environment setting was not refused: %v\n%s", err, out.String())
		}

		if err := Show(ctx, &out); !errors.Is(err, errs.ErrUsage) {
			t.Fatalf("policy show accepted an environment setting: %v", err)
		}

		if _, err := TestRun(ctx, &out, io.Discard, enforcement.GoJSON, func(io.Writer, io.Writer) error {
			t.Fatal("tests ran under a refused environment")

			return nil
		}); !errors.Is(err, errs.ErrUsage) {
			t.Fatalf("the test gate accepted an environment setting: %v", err)
		}
	}
}
