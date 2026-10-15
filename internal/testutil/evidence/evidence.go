// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// Package evidence accounts for the evidence a test run did not produce.
//
// Go prints a skip next to the test that took it, in output `go test`
// discards entirely for a package that passes without -v, and the tier still
// ends in "ok". A run that exercised real buildah, real trivy and real
// ssh-keygen and a run that exercised none of them therefore read identically
// at the only place anyone looks. Every skip that stands for absent evidence
// goes through RequireTool or NotProvenf instead, both record into a tally,
// and Main states the shortfall once at the end of the binary.
//
// The distinction this package exists to keep is between a tool that is
// ABSENT and one that is BROKEN. Absence is a fact about the machine: the
// default profile lets a developer without podman-stack or trivy installed
// still run what they can. A tool that is present and then fails -- a store
// it cannot create, a root it may not write, any non-zero exit -- is a fact
// about the thing under test, and must fail the test. Nothing here will skip
// for that, and a caller that reaches for NotProvenf to quiet such a failure
// is asking a misconfiguration to report as green.
//
// The strict profile (FORGE_CI_TEST_PROFILE=strict) removes the first
// allowance too: there, an absent tool is an error, because a gate run that
// may skip its evidence is not a gate. A profile value that is neither empty
// nor "strict" is refused rather than read as the default -- a typo in the
// one variable that turns the accounting on must not silently turn it off.
package evidence

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	// ProfileEnv selects how a missing prerequisite is treated: unset (or
	// empty) skips and tallies, StrictProfile fails.
	ProfileEnv = "FORGE_CI_TEST_PROFILE"

	// StrictProfile is the only accepted non-empty value of ProfileEnv.
	StrictProfile = "strict"

	// ReportFileEnv names a file the end-of-run summary is appended to. The
	// justfile points it at bin/ and prints the result, because `go test`
	// without -v throws away everything a passing package wrote -- which is
	// exactly the run whose gaps nobody would otherwise see. When it is
	// unset, GITHUB_STEP_SUMMARY is used if the runner set one.
	ReportFileEnv = "FORGE_CI_TEST_NOT_PROVEN_FILE"

	// profileExitCode is returned by Main when ProfileEnv cannot be read. It
	// is distinct from 1 so a refused profile is not mistaken for a test
	// failure.
	profileExitCode = 2
)

var errProfile = errors.New("invalid " + ProfileEnv)

// errNotProven is the strict profile's refusal to skip.
var errNotProven = errors.New("strict profile: evidence may not be skipped")

// The run's profile and report destination are read when the test binary
// loads, not when a skip happens.
//
// internal/testutil/isolatedenv clears every FORGE_CI_ and GITHUB_
// variable inside an isolated test -- rightly, because the product reads
// those and must not inherit a developer's -- and the tests that need this
// accounting most are exactly the ones that isolate hardest. A profile read
// through os.Getenv from inside them comes back empty, so a strict run would
// quietly demote itself to the permissive default in the tests it was turned
// on for. The profile is a property of the run, so it is captured once, where
// no test can reach it.
//
//nolint:gochecknoglobals // captured at load: see above, a test's scrubbed environment must not change the run's profile.
var (
	runProfile = os.Getenv(ProfileEnv)

	// runReportFile is where the summary is appended. GITHUB_STEP_SUMMARY is
	// the fallback because CI already publishes its result tables there.
	runReportFile = firstSet(os.Getenv(ReportFileEnv), os.Getenv("GITHUB_STEP_SUMMARY"))
)

func firstSet(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

// RequireTool returns the resolved path of the named command, or gives up on
// the test: a skip in the default profile, a failure in the strict one.
//
// Only absence is handled here. What the tool then does is the test's
// business, and a failure from a tool this function found is a real failure.
func RequireTool(t *testing.T, name string) string {
	t.Helper()

	path, err := exec.LookPath(name)
	if err == nil {
		return path
	}

	NotProvenf(t, "%s is not on PATH", name)

	return ""
}

// NotProvenf records that the run cannot produce a piece of evidence, then
// skips (default profile) or fails (strict profile).
//
// The reason is the tally key, so it must name the missing prerequisite and
// nothing run-specific: "trivy is not on PATH", not "trivy is not on PATH for
// /tmp/TestX123". A reason carrying a temporary path makes every occurrence
// its own line in the summary and the counts meaningless.
func NotProvenf(t *testing.T, format string, args ...any) {
	t.Helper()

	reason := fmt.Sprintf(format, args...)
	notProven.record(reason)

	if err := skipRefusal(runProfile, reason); err != nil {
		t.Fatal(err)
	}

	t.Skip(reason)
}

// Main validates the profile, runs the package's tests, and reports the
// shortfall once. Packages that use RequireTool or NotProvenf wire it up as
//
//	func TestMain(m *testing.M) { os.Exit(evidence.Main(m)) }
func Main(m *testing.M) int {
	if _, err := parseProfile(runProfile); err != nil {
		fmt.Fprintln(os.Stderr, err)

		return profileExitCode
	}

	code := m.Run()

	if err := report(os.Stderr, runReportFile, notProven.render()); err != nil {
		fmt.Fprintf(os.Stderr, "not-proven summary: %v\n", err)

		if code == 0 {
			return 1
		}
	}

	return code
}

// parseProfile reads ProfileEnv. An unrecognised value is an error rather
// than a fallback to the default: a run asked for strict accounting and got
// permissive accounting would be the worst of the three outcomes.
func parseProfile(value string) (bool, error) {
	switch strings.TrimSpace(value) {
	case "":
		return false, nil
	case StrictProfile:
		return true, nil
	default:
		return false, fmt.Errorf("%w: must be empty or %q, got %q", errProfile, StrictProfile, value)
	}
}

// skipRefusal returns nil when the profile permits skipping for reason, and
// the refusal when it does not.
func skipRefusal(profile, reason string) error {
	strict, err := parseProfile(profile)
	if err != nil {
		return err
	}

	if strict {
		return fmt.Errorf("%w: %s", errNotProven, reason)
	}

	return nil
}

// tally counts the tests each unproven cause took with it.
type tally struct {
	mu     sync.Mutex
	counts map[string]int
}

//nolint:gochecknoglobals // one tally per test binary: the helpers record, Main summarises.
var notProven = tally{counts: map[string]int{}}

func (t *tally) record(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.counts[reason]++
}

// render returns what the run did not prove, or "" when it proved everything
// it set out to -- a complete run should not have to say so.
func (t *tally) render() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.counts) == 0 {
		return ""
	}

	reasons := make([]string, 0, len(t.counts))
	skipped := 0

	for reason, count := range t.counts {
		reasons = append(reasons, reason)
		skipped += count
	}

	slices.Sort(reasons)

	var out strings.Builder

	fmt.Fprintf(&out, "%s: %d test(s) skipped for %d unproven cause(s):\n", binaryName(), skipped, len(reasons))

	for _, reason := range reasons {
		fmt.Fprintf(&out, "  %s (%d test(s))\n", reason, t.counts[reason])
	}

	return out.String()
}

// binaryName names the package in the summary. Under `go test` argv[0] is the
// compiled test binary, so the base name is "buildah.test" and the reader
// knows which package fell short without a -v run to tell them.
func binaryName() string {
	return filepath.Base(os.Args[0])
}

// report publishes the shortfall to out and, when the runner named a file, to
// that file as well.
func report(out io.Writer, path, summary string) error {
	if summary == "" {
		return nil
	}

	if _, err := fmt.Fprintf(out, "\nNOT PROVEN: %s", summary); err != nil {
		return err
	}

	if path == "" {
		return nil
	}

	return appendSummary(path, summary)
}

// appendSummary adds one record to the shared report file. Every test binary
// in the run appends to the same file, so it is opened for append and written
// in a single call rather than read, extended and rewritten.
func appendSummary(path, summary string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // report path comes from the test runner, not from input under test.
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}

	if _, err := io.WriteString(file, "NOT PROVEN: "+summary); err != nil {
		_ = file.Close()

		return fmt.Errorf("write %s: %w", path, err)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}

	return nil
}
