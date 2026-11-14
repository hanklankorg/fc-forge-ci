// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	liveScenarioPrefix = "FORGE_CI_LIVE_SCENARIO "
	livePackageBegin   = "FORGE_CI_LIVE_PACKAGE_BEGIN "
	livePackageEnd     = "FORGE_CI_LIVE_PACKAGE_END "
	liveRowPrefix      = "FORGE_CI_LIVE_ROW "
	liveRerunPrefix    = "FORGE_CI_LIVE_RERUN "
	liveRunLogName     = "live-run.log"
	liveVerdictFailed  = "failed"
	testPass           = "PASS"
	testFail           = "FAIL"
)

// The results of a test as verdict.json spells them (liveTestResult).
const (
	leafResultPass       = "pass"
	leafResultFail       = "fail"
	leafResultUnfinished = "unfinished"
)

// The verdicts of a live run. verdictSkipped is the one whose human line
// says more than its name.
const (
	verdictPassed         = "passed"
	verdictInterrupted    = "interrupted"
	verdictInfrastructure = "infrastructure"
	verdictSkipped        = "skipped"
	verdictUnadmitted     = "unadmitted"
	verdictNotCovered     = "not-covered"
)

// RegisterLiveScenario identifies a real live-test entrypoint, including its
// subtests. Registration alone is not execution: a passing leaf is still required.
// Keep this explicit in scenarios; guard helpers are also used by offline tests.
func RegisterLiveScenario(t interface{ Name() string }) {
	fmt.Printf("%s%s\n", liveScenarioPrefix, t.Name())
	skipUnadmittedInPreflight(t)
}

// LiveRunResult distinguishes real scenario execution from Go's package-level
// PASS when the selected tests or all of their subtests skipped.
type LiveRunResult struct {
	ExitCode int
	Verdict  string
}

// ClassifyLiveRun preserves process failures and requires a successful leaf
// scenario before calling a clean process exit a passing live run.
func ClassifyLiveRun(log io.Reader, exitCode int) (LiveRunResult, error) {
	verdict, err := ReadLiveRun(log, exitCode)

	return verdict.Result(), err
}

// LiveRunVerdict is the machine-readable account of one live run that
// verdict.json holds beside the run's log. Its run header is what the run
// script hands over, limited to the inputs the log's header records; the rest
// is read from the retained log, which the run script has already redacted.
// So it can hold no more than the log does.
type LiveRunVerdict struct {
	// Verdict is passed, failed, interrupted, skipped or infrastructure: the
	// lab, not the product, failed every test that failed.
	Verdict  string        `json:"verdict"`
	ExitCode int           `json:"exit_code"`
	Run      LiveRunHeader `json:"run"`
	// Tests are the leaf tests that ran, in the order they started. Those of
	// a row's first attempt that its rerun replaced are in Reruns instead.
	Tests []LeafTestResult `json:"tests"`
	// Reruns are the rows that ran twice: each failed on an unavailable
	// dependency and was run again, once. A run with a rerun did not pass
	// at the first attempt, whatever its verdict.
	Reruns []LiveRowRerun `json:"reruns,omitempty"`
	// Admitted are the scenarios a preflight run admitted.
	Admitted       []string                    `json:"admitted,omitempty"`
	Infrastructure []LiveInfrastructureFailure `json:"infrastructure,omitempty"`
	AdminPolicy    []LiveAdminPolicyDecision   `json:"admin_policy,omitempty"`
}

// LiveRunHeader is what the run was asked to do, as the run script holds it
// and writes it into the log's header.
type LiveRunHeader struct {
	StartedAt    string `json:"started_at,omitempty"`
	SourceCommit string `json:"source_commit,omitempty"`
	Profile      string `json:"profile,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Scenario     string `json:"scenario,omitempty"`
	// Inputs are the consumer inputs of LiveRunInputs that were set, keyed
	// in lower case as the log's header has them.
	Inputs map[string]string `json:"inputs,omitempty"`
}

// LiveRunInputs are the consumer inputs a run records, the same names the run
// script's header loop logs: each names a setting, a file or a user and never
// carries a secret. An external registry's token stays in its file.
var LiveRunInputs = []string{
	"FORGE_CI_LIVE_DEMO", "FORGE_CI_LIVE_CONSUMER", "FORGE_CI_LIVE_CONSUMER_EVENT",
	"FORGE_CI_LIVE_CONSUMER_SETTINGS", "FORGE_CI_LIVE_CONSUMER_VISIBILITY", "FORGE_CI_LIVE_IMAGE_PLAN_JOB_IMAGE",
	"FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE", "FORGE_CI_LIVE_EXTERNAL_REGISTRY_USER", "FORGE_CI_LIVE_GITLAB_IMAGE_BUILDER", "FORGE_CI_LIVE_FORGEJO_IMAGE_BUILDER",
	"FORGE_CI_LIVE_ADMIN_LINT", "FORGE_CI_LIVE_ADMIN_VULNERABILITIES", "FORGE_CI_LIVE_ADMIN_APPLICATION_TESTS", "FORGE_CI_LIVE_ADMIN_REPRODUCIBLE_BUILDS",
	"FORGE_CI_LIVE_SET", "FORGE_CI_LIVE_AFTER", "FORGE_CI_LIVE_PREPARED_ENV_SHA256",
}

// LiveRunHeaderFrom reads the run's header from the environment the run
// script hands the classifier: the run fields it passes, and the inputs of
// LiveRunInputs, no others.
func LiveRunHeaderFrom(getenv func(string) string) LiveRunHeader {
	header := LiveRunHeader{
		StartedAt:    getenv("FORGE_CI_LIVE_RUN_STARTED_AT"),
		SourceCommit: getenv("FORGE_CI_LIVE_RUN_SOURCE_COMMIT"),
		Profile:      getenv("FORGE_CI_LIVE_PROFILE"),
		Provider:     getenv("FORGE_CI_LIVE_PROVIDER"),
		Scenario:     getenv("FORGE_CI_LIVE_RUN_SCENARIO"),
	}

	for _, name := range LiveRunInputs {
		if value := getenv(name); value != "" {
			if header.Inputs == nil {
				header.Inputs = map[string]string{}
			}

			header.Inputs[strings.ToLower(name)] = value
		}
	}

	return header
}

// LeafTestResult is one leaf test: pass, fail or skip, or unfinished when
// the log has its start and no result.
type LeafTestResult struct {
	Name           string  `json:"name"`
	Package        string  `json:"package"`
	Result         string  `json:"result"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	// Cause names the network as what a failed test's forge job stopped on,
	// as "infrastructure: <kind> (<host>)", when the engine ended that job
	// on a dependency it could not reach and no failed job of the test on
	// another error. It is empty otherwise, and changes neither the result
	// nor the verdict.
	Cause string `json:"cause,omitempty"`
	// Attempt is 2 for a test of a row's rerun, and left out otherwise.
	Attempt int `json:"attempt,omitempty"`
}

// LiveRowRerun is a table row the launcher ran twice, by the row's pattern.
// Result is how the rerun ended, which is the row's result.
type LiveRowRerun struct {
	Row      string           `json:"row"`
	Package  string           `json:"package"`
	Result   string           `json:"result"`
	Attempts []LiveRowAttempt `json:"attempts"`
}

// LiveRowAttempt is one process of a row that ran twice: how the launcher
// says it ended and how long it took (its FORGE_CI_LIVE_ROW line), and its
// lines in the run's log. The first attempt has the cause it was run again
// for and its tests; the rerun's tests are the verdict's, marked attempt 2.
type LiveRowAttempt struct {
	Result   string           `json:"result"`
	Duration string           `json:"duration"`
	Cause    string           `json:"cause,omitempty"`
	Log      string           `json:"log"`
	Tests    []LeafTestResult `json:"tests,omitempty"`
}

// LiveInfrastructureFailure is one FORGE_CI_LIVE_INFRASTRUCTURE line.
type LiveInfrastructureFailure struct {
	Test    string `json:"test"`
	Package string `json:"package"`
	Detail  string `json:"detail"`
}

// LiveAdminPolicyDecision is one admin-policy decision line a job log in the
// run's log reported.
type LiveAdminPolicyDecision struct {
	PolicySHA256 string `json:"policy_sha256"`
	Check        string `json:"check"`
	Result       string `json:"result"`
	Enforcement  bool   `json:"enforcement"`
}

// ReadLiveRun classifies a run from its exit status and its log, and records
// what the log says of it. A nil log, as for a run whose log cannot be
// opened, is classified from the status alone.
func ReadLiveRun(log io.Reader, exitCode int) (LiveRunVerdict, error) {
	return readLiveRun(log, exitCode, false)
}

// ReadLivePreflightRun is ReadLiveRun for a preflight run, which passes when
// every scenario that ran was admitted and none failed.
func ReadLivePreflightRun(log io.Reader, exitCode int) (LiveRunVerdict, error) {
	return readLiveRun(log, exitCode, true)
}

// FocusedRun reports whether this live run is a focused one, whose caller
// selected its scenarios and where NotCoveredf therefore skips.
func FocusedRun() bool {
	return os.Getenv(liveProfileEnv) == profileFocused
}

// ReadLiveFocusedRun is ReadLiveRun for a focused run. Its caller selected
// each scenario, so one that skipped itself or every subtest (NotCoveredf)
// leaves the run not covered, though another scenario passed.
func ReadLiveFocusedRun(log io.Reader, exitCode int) (LiveRunVerdict, error) {
	scan := newLiveRunScan()
	scan.focused = true

	return scan.read(log, exitCode)
}

func readLiveRun(log io.Reader, exitCode int, preflight bool) (LiveRunVerdict, error) {
	scan := newLiveRunScan()
	scan.preflight = preflight

	return scan.read(log, exitCode)
}

// read consumes log and classifies the run.
func (scan *liveRunScan) read(log io.Reader, exitCode int) (LiveRunVerdict, error) {
	if log != nil {
		scanner := bufio.NewScanner(log)
		scanner.Buffer(make([]byte, 4096), 16<<20)

		for scanner.Scan() {
			scan.consume(scanner.Text())
		}

		if err := scanner.Err(); err != nil {
			// Unread evidence cannot pass, nor tell the lab from the product.
			scan.settle()

			verdict := scan.verdict
			verdict.ExitCode, verdict.Verdict = max(exitCode, 1), liveVerdictFailed

			if exitCode == 130 {
				verdict.Verdict = verdictInterrupted
			}

			return verdict, fmt.Errorf("read live scenario results: %w", err)
		}
	}

	scan.settle()

	verdict := scan.verdict
	verdict.ExitCode, verdict.Verdict = scan.classify(exitCode)

	return verdict, nil
}

// RerunRows are the rows of a run's log that the launcher runs again: each
// as its package, its pattern and the cause it is run again for,
// tab-separated, in the order they ran. See livererun.go for the rule.
func RerunRows(log io.Reader) ([]string, error) {
	scan := newLiveRunScan()
	scanner := bufio.NewScanner(log)
	scanner.Buffer(make([]byte, 4096), 16<<20)

	for scanner.Scan() {
		scan.consume(scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read live scenario results: %w", err)
	}

	scan.settle()

	var rows []string

	for _, segment := range scan.segments {
		if segment.unavailable != "" && !segment.replaced {
			rows = append(rows, segment.pkg+"\t"+segment.row+"\t"+segment.unavailable)
		}
	}

	return rows, nil
}

// WriteLiveRunVerdict writes verdict as verdict.json in dir, readable by its
// owner alone, as the log beside it is.
func WriteLiveRunVerdict(dir string, verdict LiveRunVerdict) error {
	if verdict.Tests == nil {
		verdict.Tests = []LeafTestResult{}
	}

	body, err := json.MarshalIndent(verdict, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the run verdict: %w", err)
	}

	path := filepath.Join(dir, "verdict.json")
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("write the run verdict: %w", err)
	}

	return nil
}

// Result is the verdict as the run script prints it: its exit status, and
// the Live result line's wording.
func (verdict LiveRunVerdict) Result() LiveRunResult {
	switch verdict.Verdict {
	case verdictSkipped:
		return LiveRunResult{verdict.ExitCode, "skipped: no scenario ran; the log says why"}
	case verdictUnadmitted:
		return LiveRunResult{verdict.ExitCode, "unadmitted: a scenario that ran was not admitted; the log says why"}
	case verdictNotCovered:
		return LiveRunResult{verdict.ExitCode, "not-covered: a selected scenario skipped itself or every subtest; the log says why"}
	case verdictPassed:
		if len(verdict.Reruns) != 0 {
			return LiveRunResult{verdict.ExitCode, fmt.Sprintf("passed: %d row(s) on a rerun after an unavailable dependency; verdict.json has both attempts", len(verdict.Reruns))}
		}
	}

	return LiveRunResult{verdict.ExitCode, verdict.Verdict}
}

// classify decides the verdict and its exit status. A failed process stays
// failed, except that a run whose every failure the lab caused is called
// infrastructure, still with its non-zero status.
func (scan *liveRunScan) classify(exitCode int) (int, string) {
	if exitCode != 0 {
		return scan.classifyFailedExit(exitCode)
	}

	switch {
	case scan.preflight && !scan.failed && scan.active == "":
		return scan.classifyPreflight()
	case scan.failed || scan.active != "":
		return 1, liveVerdictFailed
	case scan.passed && scan.focused && scan.uncovered:
		return 1, verdictNotCovered
	case scan.passed:
		return 0, verdictPassed
	default:
		return 1, verdictSkipped
	}
}

// classifyFailedExit is the verdict of a run that ended with another exit
// code than 0: interrupted, infrastructure where nothing else failed, and
// otherwise failed.
func (scan *liveRunScan) classifyFailedExit(exitCode int) (int, string) {
	switch {
	case exitCode == 130:
		return 130, verdictInterrupted
	case scan.onlyInfrastructure():
		return exitCode, verdictInfrastructure
	default:
		return exitCode, liveVerdictFailed
	}
}

// classifyPreflight passes a clean preflight run when it registered a
// scenario and every scenario it registered reached its admission.
func (scan *liveRunScan) classifyPreflight() (int, string) {
	if len(scan.registered) == 0 {
		return 1, verdictSkipped
	}

	for _, name := range scan.registered {
		if !scan.reachedAdmission(name) {
			return 1, verdictUnadmitted
		}
	}

	return 0, verdictAdmitted
}

// reachedAdmission reports whether test name reached Admitted itself, or ran
// subtests that each reached it: a scenario with one subtest per visibility
// is admitted only when every visibility was.
func (scan *liveRunScan) reachedAdmission(name string) bool {
	if slices.Contains(scan.verdict.Admitted, name) {
		return true
	}

	children := 0

	for run := range scan.runs {
		child, isChild := strings.CutPrefix(run, name+"/")
		if !isChild || strings.Contains(child, "/") {
			continue
		}

		children++

		if !scan.reachedAdmission(run) {
			return false
		}
	}

	return children != 0
}

// onlyInfrastructure reports whether the lab caused every failure in the
// log: some test failed, every failed test is marked or the ancestor of a
// marked test, and nothing else went wrong -- no test left unfinished, no
// broken package boundary.
func (scan *liveRunScan) onlyInfrastructure() bool {
	if scan.broken || scan.active != "" || len(scan.failures) == 0 || len(scan.verdict.Infrastructure) == 0 {
		return false
	}

	for _, test := range scan.verdict.Tests {
		if test.Result == leafResultUnfinished {
			return false
		}
	}

	for _, failed := range scan.failures {
		covered := false

		for _, marked := range scan.verdict.Infrastructure {
			if marked.Package == failed.Package && (marked.Test == failed.Name || strings.HasPrefix(marked.Test, failed.Name+"/")) {
				covered = true
			}
		}

		if !covered {
			return false
		}
	}

	return true
}

// liveRunScan accumulates the package, scenario and test records of one live
// run log, line by line.
type liveRunScan struct {
	lineResult, lineRun, lineOutput, lineAdminPolicy *regexp.Regexp

	// writing is the test whose output the log is at, and passedJob whether
	// the log is in the trace of a job that did not fail. causes holds, for
	// each test, the cause its failed jobs' engine errors give, and defects
	// the tests with an engine error of another class.
	writing   string
	passedJob bool
	causes    map[string]string
	defects   map[string]bool

	// What the rule of a row's rerun reads in the open process (livererun.go):
	// each test's evidence, the job's log the log is in, and whether it is
	// in an entry of GitLab traces.
	evidence map[string]*rerunEvidence
	job      *liveJobLog
	traces   bool

	active    string
	results   map[string]string
	elapsed   map[string]float64
	scenarios map[string]bool
	runs      map[string]bool
	order     []string

	verdict  LiveRunVerdict
	failures []LeafTestResult
	policies map[LiveAdminPolicyDecision]bool

	// registered are the scenarios that ran, as they registered.
	registered []string

	// uncovered: a scenario that ran has no passing leaf; focused: the run's
	// caller selected its scenarios.
	passed, failed, broken, preflight, focused, uncovered bool

	// row: the open package's records are of one table row, which the run
	// script ran as a process of its own and marked (liveRowPrefix).
	row bool

	// line is the number of the log line being read. segment is the open
	// package's process and segments those that ended, in the log's order;
	// while one is open, passed, failed and uncovered are its own.
	line     int
	segment  *liveSegment
	segments []*liveSegment
}

// liveSegment is one process of a package in the log, from its begin record
// to its end record, with what it adds to the run. The launcher runs a table
// row as a process of its own, so a row that ran twice is two segments.
type liveSegment struct {
	pkg         string
	began, last int
	tests       []LeafTestResult
	failures    []LeafTestResult

	passed, failed, uncovered bool
	// outer holds the run's passed, failed and uncovered while the segment
	// is open.
	outer [3]bool
	// marked is how many infrastructure marks the log had when it began.
	marked int

	// row, ended and duration are the launcher's line of a row's process,
	// and rerun its mark of a row's second process (liveRerunPrefix).
	row, ended, duration string
	rerun                bool
	// unavailable is the cause a row's first process is run again for, and
	// replaced whether the log has the rerun that replaces it.
	unavailable string
	replaced    bool
}

func newLiveRunScan() *liveRunScan {
	return &liveRunScan{
		lineResult: regexp.MustCompile(`^\s*--- (PASS|FAIL|SKIP): (\S+)(?: \(([0-9.]+)s\))?`),
		// testing writes RUN records at column zero, even for nested subtests.
		// t.Log's quoted/multiline output is indented and cannot introduce a run.
		lineRun: regexp.MustCompile(`^=== RUN[ \t]+(\S+)[ \t]*$`),
		// testing names the test whose output follows when it starts,
		// continues or interleaves with another.
		lineOutput: regexp.MustCompile(`^=== (?:RUN|CONT|NAME)[ \t]+(\S+)[ \t]*$`),
		causes:     map[string]string{},
		defects:    map[string]bool{},
		// As internal/app/enforcement prints it, from a job's log the
		// scenario printed or retained; a not-waivable line has no count.
		lineAdminPolicy: regexp.MustCompile(`(?:Admin policy sha256|Enforcement policy default-sha256):([0-9a-f]{64}): (\S+) result=(\S+)(?: count=\d+)? enforcement=(true|false)`),
		policies:        map[LiveAdminPolicyDecision]bool{},
	}
}

// consume records one log line.
func (scan *liveRunScan) consume(line string) {
	scan.line++
	scan.consumeAdminPolicy(line)
	scan.consumeRowRecord(line)
	scan.consumeConclusion(line)

	switch {
	case strings.HasPrefix(line, livePackageBegin):
		scan.beginPackage(strings.TrimPrefix(line, livePackageBegin))
	case strings.HasPrefix(line, livePackageEnd):
		scan.endPackage(strings.TrimPrefix(line, livePackageEnd))
	case strings.HasPrefix(line, liveScenarioPrefix):
		name := strings.TrimPrefix(line, liveScenarioPrefix)
		if scan.active != "" && scan.runs[name] {
			scan.scenarios[name] = true
			scan.registered = append(scan.registered, name)
		}
	case strings.HasPrefix(line, liveAdmittedPrefix):
		if name := strings.TrimPrefix(line, liveAdmittedPrefix); scan.active != "" && scan.runs[name] {
			scan.verdict.Admitted = append(scan.verdict.Admitted, name)
		}
	case strings.HasPrefix(line, liveInfrastructurePrefix):
		if test, detail, ok := strings.Cut(strings.TrimPrefix(line, liveInfrastructurePrefix), " "); ok && scan.active != "" {
			scan.verdict.Infrastructure = append(scan.verdict.Infrastructure,
				LiveInfrastructureFailure{Test: test, Package: scan.active, Detail: detail})
		}
	case strings.HasPrefix(line, liveRowPrefix):
		scan.row = true
	default:
		scan.consumeTestRecord(line)
	}
}

// consumeRowRecord records what the launcher says of the open process of a
// row: its line when it ended, and its mark of a rerun.
func (scan *liveRunScan) consumeRowRecord(line string) {
	if scan.segment == nil {
		return
	}

	if strings.HasPrefix(line, liveRerunPrefix) {
		scan.segment.rerun = true
	}

	if fields := strings.SplitN(strings.TrimPrefix(line, liveRowPrefix), " ", 3); len(fields) == 3 && strings.HasPrefix(line, liveRowPrefix) {
		scan.segment.ended, scan.segment.duration, scan.segment.row = fields[0], fields[1], fields[2]
	}
}

// beginPackage opens a package's records; one already open, or a nameless
// one, breaks the log's structure.
func (scan *liveRunScan) beginPackage(name string) {
	if scan.active != "" || name == "" {
		scan.failed, scan.broken = true, true
	}

	scan.closeSegment(false)
	scan.active = name
	scan.results, scan.scenarios = map[string]string{}, map[string]bool{}
	scan.runs, scan.elapsed, scan.order = map[string]bool{}, map[string]float64{}, nil
	scan.writing, scan.causes, scan.defects = "", map[string]string{}, map[string]bool{}
	scan.evidence, scan.job, scan.traces = map[string]*rerunEvidence{}, nil, false
	scan.row = false

	scan.segment = &liveSegment{
		pkg: name, began: scan.line, marked: len(scan.verdict.Infrastructure),
		outer: [3]bool{scan.passed, scan.failed, scan.uncovered},
	}
	scan.passed, scan.failed, scan.uncovered = false, false, false
}

// endPackage closes the open package, which must be the one named.
func (scan *liveRunScan) endPackage(name string) {
	ended := scan.active != "" && name == scan.active
	if !ended {
		scan.failed, scan.broken = true, true
	} else if passingLiveLeaf(scan.results, scan.scenarios, scan.runs, scan.row) {
		scan.passed = true
	}

	// A row the run selected that ran no scenario is not covered either.
	scan.uncovered = scan.uncovered || (scan.row && len(scan.scenarios) == 0)

	for scenario := range scan.scenarios {
		if !passingLiveLeaf(scan.results, map[string]bool{scenario: true}, scan.runs, scan.row) {
			scan.uncovered = true
		}
	}

	scan.closeSegment(ended)
	scan.active = ""
}

// closeSegment ends the open package's process, if any: it takes the tests
// and what the process adds to the run, and the run's own state is again
// what it was. ended is whether the process's end record closed it.
func (scan *liveRunScan) closeSegment(ended bool) {
	segment := scan.segment
	if segment == nil {
		return
	}

	scan.closeJobLog()

	unavailable := scan.flush()
	if ended && !scan.broken {
		segment.unavailable = segment.rerunCause(unavailable, len(scan.verdict.Infrastructure))
	}

	segment.last = scan.line
	segment.passed, segment.failed, segment.uncovered = scan.passed, scan.failed, scan.uncovered
	scan.passed, scan.failed, scan.uncovered = segment.outer[0], segment.outer[1], segment.outer[2]
	scan.segments, scan.segment = append(scan.segments, segment), nil
}

// rerunCause is the cause the launcher runs a row's process again for, or
// nothing. The process is a row's first, the launcher says it failed, and
// an unreached host explains every test of it that failed (unavailable,
// which flush decides by the rule of livererun.go); the lab marked none of
// them (marks is the log's count of marks now). A rerun is never run again.
func (segment *liveSegment) rerunCause(unavailable string, marks int) string {
	if segment.row == "" || segment.rerun || segment.ended != liveVerdictFailed || marks != segment.marked {
		return ""
	}

	return unavailable
}

// settle ends the reading of the log. A row's rerun replaces the row's first
// process where that process was to be run again (liveSegment.unavailable):
// the first attempt is recorded in the verdict's reruns and adds nothing
// else to the run. A rerun of any other process replaces nothing, so what
// failed stays failed. Every other process adds its tests and its results.
func (scan *liveRunScan) settle() {
	scan.closeSegment(false)

	for at, segment := range scan.segments {
		if !segment.rerun || segment.row == "" {
			continue
		}

		for _, first := range scan.segments[:at] {
			if first.pkg != segment.pkg || first.row != segment.row || first.unavailable == "" || first.replaced {
				continue
			}

			first.replaced = true

			for i := range segment.tests {
				segment.tests[i].Attempt = 2
			}

			scan.verdict.Reruns = append(scan.verdict.Reruns, LiveRowRerun{
				Row: segment.row, Package: segment.pkg, Result: segment.ended,
				Attempts: []LiveRowAttempt{first.attempt(), segment.attempt()},
			})

			break
		}
	}

	for _, segment := range scan.segments {
		if segment.replaced {
			continue
		}

		scan.passed, scan.failed, scan.uncovered = scan.passed || segment.passed, scan.failed || segment.failed, scan.uncovered || segment.uncovered
		scan.verdict.Tests = append(scan.verdict.Tests, segment.tests...)
		scan.failures = append(scan.failures, segment.failures...)
	}
}

// attempt is the segment as an attempt of a row that ran twice. Only a first
// attempt's tests are listed with it: a rerun's are the verdict's.
func (segment *liveSegment) attempt() LiveRowAttempt {
	attempt := LiveRowAttempt{
		Result: segment.ended, Duration: segment.duration, Cause: segment.unavailable,
		Log: fmt.Sprintf("%s:%d-%d", liveRunLogName, segment.began, segment.last),
	}
	if segment.replaced {
		attempt.Tests = segment.tests
	}

	return attempt
}

// consumeAdminPolicy records an admin-policy decision once, wherever in the
// log its line appears.
func (scan *liveRunScan) consumeAdminPolicy(line string) {
	match := scan.lineAdminPolicy.FindStringSubmatch(line)
	if match == nil {
		return
	}

	decision := LiveAdminPolicyDecision{PolicySHA256: match[1], Check: match[2], Result: match[3], Enforcement: match[4] == "true"}
	if !scan.policies[decision] {
		scan.policies[decision] = true
		scan.verdict.AdminPolicy = append(scan.verdict.AdminPolicy, decision)
	}
}

// consumeTestRecord records a testing RUN or result line.
func (scan *liveRunScan) consumeTestRecord(line string) {
	if match := scan.lineRun.FindStringSubmatch(line); match != nil && scan.active != "" {
		if scan.runs[match[1]] {
			scan.failed, scan.broken = true, true
		}

		scan.runs[match[1]] = true
		scan.order = append(scan.order, match[1])
	}

	if match := scan.lineOutput.FindStringSubmatch(line); match != nil {
		scan.closeJobLog()

		scan.writing, scan.passedJob, scan.traces = match[1], false, false
	} else if scan.active != "" && scan.writing != "" {
		scan.consumeTrace(line)
		scan.consumeRerunEvidence(line)
	}

	match := scan.lineResult.FindStringSubmatch(line)
	if match == nil {
		return
	}

	if match[1] == testFail {
		scan.failed = true
	}

	if scan.active == "" {
		if match[1] == testFail {
			scan.broken = true
		}

		return
	}

	if _, duplicate := scan.results[match[2]]; duplicate {
		scan.failed, scan.broken = true, true
	}

	scan.results[match[2]] = match[1]

	if seconds, err := strconv.ParseFloat(match[3], 64); err == nil {
		scan.elapsed[match[2]] = seconds
	}
}

// consumeTrace reads a line of the output of the test the log is at. Only
// the engine's error in the trace of a job that failed gives a cause; a job
// that passed, or whose engine recovered, gives none.
func (scan *liveRunScan) consumeTrace(line string) {
	if !jobTraceLine(line) {
		// A new log entry: a trace without a job section is a failed job's.
		scan.passedJob = false

		return
	}

	if match := jobSection.FindStringSubmatch(line); match != nil {
		scan.passedJob = match[1] != liveVerdictFailed

		return
	}

	if scan.passedJob {
		return
	}

	switch cause, found := engineCause(line); {
	case !found:
	case cause == "":
		scan.defects[scan.writing] = true
	default:
		scan.causes[scan.writing] = cause
	}
}

// flush records the tests of the process that is ending in its segment:
// every leaf that ran, and every test that failed. It returns the causes of
// the failed leaves, joined, when an unreached host is all that failed the
// process: every failed leaf has that cause by the rule of livererun.go,
// every other failed test failed with a test below it and logged nothing
// of its own, and no test is unfinished, as those of a process
// that ran into its time limit are. It returns nothing otherwise.
func (scan *liveRunScan) flush() string {
	segment := scan.segment

	var causes []string

	only := true

	for _, name := range scan.order {
		test := LeafTestResult{Name: name, Package: scan.active, Result: liveTestResult(scan.results[name]), ElapsedSeconds: scan.elapsed[name]}
		leaf := liveLeaf(name, scan.runs, scan.results)

		if test.Result == leafResultFail {
			if !scan.defects[name] {
				test.Cause = scan.causes[name]
			}

			segment.failures = append(segment.failures, test)

			cause, unavailable := scan.unavailableFailure(test, leaf)
			only = only && unavailable

			if cause != "" && !slices.Contains(causes, cause) {
				causes = append(causes, cause)
			}
		}

		only = only && test.Result != leafResultUnfinished

		if leaf {
			segment.tests = append(segment.tests, test)
		}
	}

	// A FAIL with no RUN still fails the run, and must be accounted for.
	for name, result := range scan.results {
		if result == testFail && !scan.runs[name] {
			segment.failures = append(segment.failures, LeafTestResult{Name: name, Package: scan.active, Result: leafResultFail})
			only = false
		}
	}

	scan.order = nil

	if !only {
		return ""
	}

	return strings.Join(causes, "; ")
}

// unavailableFailure reports whether an unreached host explains a failed
// test of the open process, and the cause if the test is a leaf.
func (scan *liveRunScan) unavailableFailure(test LeafTestResult, leaf bool) (string, bool) {
	if leaf {
		cause := scan.evidence[test.Name].cause()

		return cause, cause != ""
	}

	// A test with tests below it fails because one of them did; a log entry
	// of its own, before or after them, may be a failure of its own.
	return "", failedBelow(test.Name, scan.results) && (scan.evidence[test.Name] == nil || !scan.evidence[test.Name].logged)
}

// failedBelow reports whether a test below name failed.
func failedBelow(name string, results map[string]string) bool {
	for other, result := range results {
		if result == testFail && strings.HasPrefix(other, name+"/") {
			return true
		}
	}

	return false
}

// belowSubtest reports whether a subtest of scenario that ran is an ancestor
// of the test name.
func belowSubtest(name, scenario string, runs map[string]bool) bool {
	for other := range runs {
		if strings.HasPrefix(other, scenario+"/") && strings.HasPrefix(name, other+"/") {
			return true
		}
	}

	return false
}

// liveTestResult spells a testing result for verdict.json.
func liveTestResult(result string) string {
	if result == "" {
		return leafResultUnfinished
	}

	return strings.ToLower(result)
}

// liveLeaf reports whether no other test of the package ran, or reported,
// under name.
func liveLeaf(name string, runs map[string]bool, results map[string]string) bool {
	for other := range runs {
		if strings.HasPrefix(other, name+"/") {
			return false
		}
	}

	for other := range results {
		if strings.HasPrefix(other, name+"/") {
			return false
		}
	}

	return true
}

// passingLiveLeaf reports whether a scenario passed with a passing leaf. In
// a row's process the leaf must be below a subtest of the scenario: the
// row's case is a subtest itself, and passes with nothing in it when the
// run's pattern or visibility leaves it no leaf to run.
func passingLiveLeaf(results map[string]string, scenarios, runs map[string]bool, row bool) bool {
	for name := range runs {
		if results[name] != testPass {
			continue
		}

		leaf, ancestorsPassed := true, true
		// A child result without its RUN record cannot qualify, but must
		// still stop a parent PASS from masquerading as a passing leaf.
		for other := range results {
			if strings.HasPrefix(other, name+"/") {
				leaf = false
			}
		}

		for other := range runs {
			if strings.HasPrefix(other, name+"/") {
				leaf = false
			}
			// Only actual RUN identities are ancestors. A slash in a t.Run
			// name does not imply an intermediate test was ever executed.
			if strings.HasPrefix(name, other+"/") && results[other] != testPass {
				ancestorsPassed = false
			}
		}

		if leaf && ancestorsPassed && leafOfPassedScenario(name, results, scenarios, runs, row) {
			return true
		}
	}

	return false
}

// leafOfPassedScenario reports whether the test name is a scenario that
// passed or a test below one, and in a row's process below a subtest of it.
func leafOfPassedScenario(name string, results map[string]string, scenarios, runs map[string]bool, row bool) bool {
	for scenario := range scenarios {
		if results[scenario] == testPass && (name == scenario || strings.HasPrefix(name, scenario+"/")) && (!row || belowSubtest(name, scenario, runs)) {
			return true
		}
	}

	return false
}
