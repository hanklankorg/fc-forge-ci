// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"bytes"
	"context"
	"errors"
	"io"
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
		{"tool missing", "Tests run: 4, Failures: 1, Errors: 0, Skipped: 0", errs.ErrDependencyUnavailable, false},
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
		})
	}
}

func TestTestOptOutCannotBecomeReportOnly(t *testing.T) {
	if err := RequireTests(true); !errors.Is(err, errs.ErrUsage) {
		t.Fatalf("skip accepted: %v", err)
	}

	if err := RequireTests(false); err != nil {
		t.Fatal(err)
	}
}

func TestReportOnlyGradleRequiresAssertionOnlyTerminal(t *testing.T) {
	const summary = "> Task :app:testDebugUnitTest FAILED\n4 tests completed, 2 failed\n"
	const assertion = "Execution failed for task ':app:testDebugUnitTest'.\n> There were failing tests. See the report at: file:///fixture/report.html"
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
			} else if !errors.Is(err, testExitError(1)) || strings.Contains(out.String(), "Admin policy sha256:") {
				t.Fatalf("runtime failure was waived or lost: %v\n%s", err, &out)
			}
		})
	}
}
