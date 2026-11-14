// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// TestProtocol is selected by engine code, not by a consumer policy value.
type TestProtocol string

// Native result protocols supported by the application-test gate.
const (
	GoJSON TestProtocol = "go-json"
	Maven  TestProtocol = "maven"
	Cargo  TestProtocol = "cargo"
	Node   TestProtocol = "node"
	Gradle TestProtocol = "gradle"
	XCTest TestProtocol = "xctest"
)

// MaxTestLineBytes bounds one line of test output, the only output a
// TestOutput holds. A longer line means the run's summary is not trusted.
const MaxTestLineBytes = 1 << 20

const (
	gradleCauseStart = "* What went wrong:"
	gradleCauseEnd   = "* Try:"
)

var (
	ansi             = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	mavenSummary     = regexp.MustCompile(`Tests run:[ \t]*[0-9]+,[ \t]*Failures:[ \t]*([0-9]+),[ \t]*Errors:[ \t]*([0-9]+),[ \t]*Skipped:`)
	mavenTotals      = regexp.MustCompile(`^[ \t]*(?:\[(?:INFO|ERROR)\][ \t]*)?Tests run:[ \t]*[0-9]+,[ \t]*Failures:[ \t]*([0-9]+),[ \t]*Errors:[ \t]*([0-9]+),[ \t]*Skipped:[ \t]*[0-9]+[ \t]*$`)
	mavenGoalFailure = regexp.MustCompile(`^\[ERROR\] Failed to execute goal[^\r\n]*`)
	cargoSummary     = regexp.MustCompile(`test result: FAILED\.[ \t]*[0-9]+ passed;[ \t]*([0-9]+) failed;`)
	nodeSummary      = regexp.MustCompile(`^# fail ([0-9]+)\s*$`)
	gradleSummary    = regexp.MustCompile(`([0-9]+) tests completed, ([0-9]+) failed`)
	gradleFailedTask = regexp.MustCompile(`^> Task (:[^\s]+) FAILED\s*$`)
	gradleTaskCause  = regexp.MustCompile(`^Execution failed for task '(:[^'\r\n]+)'\.$`)
	gradleBuildEnd   = regexp.MustCompile(`^BUILD FAILED in [^\r\n]+\r?$`)
	xctestSummary    = regexp.MustCompile(`Executed [0-9]+ tests?, with ([0-9]+) failures? \(([0-9]+) unexpected\)`)
)

// TestOutput recognizes a fresh invocation's complete native test summary
// while the test command writes it. Unknown output and compiler/setup errors
// are not findings and fail closed. This classifies tool results; it cannot
// prove that consumer-written tests are honest or comprehensive, which remains
// a source-review responsibility.
//
// Stdout and stderr are split into lines separately, so concurrent writes to
// the two streams never meet inside a line. Only the classifier's state and
// one partial line per stream are kept, so memory does not grow with output.
type TestOutput struct {
	mu         sync.Mutex
	classifier lineClassifier
	partial    [2][]byte
	overlong   bool
}

type testStream int

const (
	stdoutStream testStream = iota
	stderrStream
)

// lineClassifier holds what a protocol needs from the lines seen so far.
// Rules over the whole output ("exactly one", "the last", "every") are kept as
// state; once a line rules out a waiver, the classifier stays at zero.
type lineClassifier interface {
	observe(stream testStream, line string)
	failed() int
}

// NewTestOutput classifies output of the given protocol; an unknown protocol
// never reports failed tests.
func NewTestOutput(protocol TestProtocol) *TestOutput {
	output := &TestOutput{}

	switch protocol {
	case GoJSON:
		output.classifier = &goTests{testFailures: map[string]bool{}, packageFailures: map[string]bool{}}
	case Maven:
		output.classifier = &mavenTests{}
	case Cargo:
		output.classifier = &cargoTests{}
	case Node:
		output.classifier = &nodeTests{}
	case Gradle:
		output.classifier = &gradleTests{failedTasks: map[string]bool{}, causes: map[string]bool{}}
	case XCTest:
		output.classifier = &xcTests{}
	}

	return output
}

// Stdout receives the test command's standard output. It never fails a write.
func (output *TestOutput) Stdout() io.Writer { return streamWriter{output, stdoutStream} }

// Stderr receives the test command's standard error. It never fails a write.
func (output *TestOutput) Stderr() io.Writer { return streamWriter{output, stderrStream} }

// Overlong reports whether a line exceeded MaxTestLineBytes.
func (output *TestOutput) Overlong() bool {
	output.mu.Lock()
	defer output.mu.Unlock()

	return output.overlong
}

// FailedTests classifies the final unterminated lines, if any, and returns the
// number of failed tests in a recognized assertion-only summary; 0 means the
// run is not waivable. Call it once the test command has finished.
func (output *TestOutput) FailedTests() int {
	output.mu.Lock()
	defer output.mu.Unlock()

	if output.overlong || output.classifier == nil {
		return 0
	}

	for stream, line := range output.partial {
		if len(line) > 0 {
			output.observe(testStream(stream), string(line))
			output.partial[stream] = nil
		}
	}

	return output.classifier.failed()
}

type streamWriter struct {
	output *TestOutput
	stream testStream
}

func (writer streamWriter) Write(body []byte) (int, error) {
	writer.output.write(writer.stream, body)

	return len(body), nil
}

func (output *TestOutput) write(stream testStream, body []byte) {
	output.mu.Lock()
	defer output.mu.Unlock()

	for !output.overlong && len(body) > 0 {
		end := bytes.IndexByte(body, '\n')
		if end < 0 {
			end = len(body)
		}

		if len(output.partial[stream])+end > MaxTestLineBytes {
			output.overlong = true
			output.partial = [2][]byte{}

			return
		}

		if end == len(body) {
			output.partial[stream] = append(output.partial[stream], body...)

			return
		}

		line := string(body[:end])
		if len(output.partial[stream]) > 0 {
			line = string(output.partial[stream]) + line
			output.partial[stream] = output.partial[stream][:0]
		}

		body = body[end+1:]

		output.observe(stream, line)
	}
}

func (output *TestOutput) observe(stream testStream, line string) {
	if output.classifier == nil {
		return
	}

	if strings.Contains(line, "\x1b") {
		line = ansi.ReplaceAllString(line, "")
	}

	output.classifier.observe(stream, line)
}

// mavenTests sums the reactor's test totals when every summary is free of
// errors and every failed goal is a surefire/failsafe test failure.
type mavenTests struct {
	summary, goal, rejected bool
	count                   int
}

func (tests *mavenTests) observe(_ testStream, line string) {
	// A summary is read within one line. One that does not fit there is
	// refused, so an Errors count cannot hide on a continuation line.
	summaries := mavenSummary.FindAllStringSubmatch(line, -1)
	if strings.Count(line, "Tests run:") != len(summaries) {
		tests.rejected = true
	}

	for _, match := range summaries {
		tests.summary = true

		if number(match[2]) != 0 {
			tests.rejected = true
		}
	}

	if goal := mavenGoalFailure.FindString(line); goal != "" {
		tests.goal = true

		if (!strings.Contains(goal, "maven-surefire-plugin:") && !strings.Contains(goal, "maven-failsafe-plugin:")) ||
			!strings.Contains(goal, "There are test failures") {
			tests.rejected = true
		}
	}

	if total := mavenTotals.FindStringSubmatch(line); total != nil {
		tests.count += number(total[1])
	}
}

func (tests *mavenTests) failed() int {
	if tests.rejected || !tests.summary || !tests.goal {
		return 0
	}

	return tests.count
}

type cargoTests struct {
	rejected bool
	count    int
}

func (tests *cargoTests) observe(_ testStream, line string) {
	if strings.Contains(line, "could not compile") || strings.Contains(line, "failed to run custom build command") {
		tests.rejected = true
	}

	// As for Maven, a summary that does not fit on its line is refused.
	summaries := cargoSummary.FindAllStringSubmatch(line, -1)
	if strings.Count(line, "test result: FAILED.") != len(summaries) {
		tests.rejected = true
	}

	for _, match := range summaries {
		tests.count += number(match[1])
	}
}

func (tests *cargoTests) failed() int {
	if tests.rejected {
		return 0
	}

	return tests.count
}

// nodeTests requires an assertion diagnostic: file/import/syntax failures also
// produce '# fail', so that summary alone is not eligible for a continuation.
type nodeTests struct {
	assertion, rejected bool
	summaries, count    int
}

func (tests *nodeTests) observe(_ testStream, line string) {
	if strings.Contains(line, "ERR_ASSERTION") {
		tests.assertion = true
	}

	for _, failure := range []string{"ERR_MODULE_NOT_FOUND", "MODULE_NOT_FOUND", "ERR_TEST_FAILURE", "SyntaxError"} {
		if strings.Contains(line, failure) {
			tests.rejected = true
		}
	}

	if match := nodeSummary.FindStringSubmatch(line); match != nil {
		tests.summaries++
		tests.count = number(match[1])
	}
}

func (tests *nodeTests) failed() int {
	if !tests.assertion || tests.rejected || tests.summaries != 1 {
		return 0
	}

	return tests.count
}

// gradleTests requires every failed task to be a test task and every terminal
// cause to name a failed test task with assertion findings. A summary printed
// before a daemon crash or another runtime failure is not a completed
// assertion-only invocation, even when the process also exits 1.
type gradleTests struct {
	failedTasks map[string]bool
	causes      map[string]bool
	sections    [2]gradleSection
	count       int
	buildFailed bool
	rejected    bool
}

// gradleSection is one stream's open "* What went wrong:" section, which
// Gradle closes with "* Try:".
type gradleSection struct {
	open  bool
	lines []string
	blank int // blank lines after the cause so far; they count if more follows
}

func (tests *gradleTests) observe(stream testStream, line string) {
	if tests.rejected {
		return
	}

	lower := strings.ToLower(line)
	if strings.Contains(lower, "daemon disappeared") || strings.Contains(lower, "daemon has disappeared") {
		tests.rejected = true
	}

	if task := gradleFailedTask.FindStringSubmatch(line); task != nil {
		name := task[1][strings.LastIndex(task[1], ":")+1:]
		if name != "test" && (!strings.HasPrefix(name, "test") || !strings.HasSuffix(name, "UnitTest")) {
			tests.rejected = true
		}

		tests.failedTasks[task[1]] = true
	}

	if gradleBuildEnd.MatchString(line) {
		tests.buildFailed = true
	}

	for _, match := range gradleSummary.FindAllStringSubmatch(line, -1) {
		tests.count += number(match[2])
	}

	tests.terminal(&tests.sections[stream], line)
}

func (tests *gradleTests) terminal(section *gradleSection, line string) {
	trimmed := strings.TrimSpace(line)

	switch {
	case strings.Contains(line, gradleCauseStart):
		if trimmed != gradleCauseStart || section.open {
			tests.rejected = true
		}

		*section = gradleSection{open: true}
	case !section.open:
	case strings.Contains(line, gradleCauseEnd):
		if trimmed != gradleCauseEnd {
			tests.rejected = true
		}

		tests.cause(section.lines)
		*section = gradleSection{}
	case trimmed == "":
		if len(section.lines) > 0 {
			section.blank++
		}
	default:
		// A cause is exactly two lines; a longer one is refused before it is
		// stored, so an unclosed section cannot grow.
		if len(section.lines)+section.blank >= 2 {
			tests.rejected = true

			return
		}

		for ; section.blank > 0; section.blank-- {
			section.lines = append(section.lines, "")
		}

		section.lines = append(section.lines, trimmed)
	}
}

// cause accepts exactly a failed-task line and its failing-tests detail.
func (tests *gradleTests) cause(lines []string) {
	if len(lines) != 2 {
		tests.rejected = true

		return
	}

	task := gradleTaskCause.FindStringSubmatch(lines[0])
	if len(task) != 2 {
		tests.rejected = true

		return
	}

	if lines[1] != "> There were failing tests." && !strings.HasPrefix(lines[1], "> There were failing tests. See the report at: ") {
		tests.rejected = true

		return
	}

	tests.causes[task[1]] = true
}

func (tests *gradleTests) failed() int {
	if tests.rejected || !tests.buildFailed || len(tests.failedTasks) == 0 || len(tests.causes) != len(tests.failedTasks) {
		return 0
	}

	for _, section := range tests.sections {
		if section.open {
			return 0
		}
	}

	for task := range tests.causes {
		if !tests.failedTasks[task] {
			return 0
		}
	}

	return tests.count
}

// xcTests trusts only the last summary, which covers the whole invocation.
// "Last" is only defined within one stream, so summaries on both are refused.
type xcTests struct {
	testFailed           bool
	summaryStreams       [2]bool
	failures, unexpected int
}

func (tests *xcTests) observe(stream testStream, line string) {
	if strings.Contains(line, "** TEST FAILED **") {
		tests.testFailed = true
	}

	for _, match := range xctestSummary.FindAllStringSubmatch(line, -1) {
		tests.summaryStreams[stream] = true
		tests.failures, tests.unexpected = number(match[1]), number(match[2])
	}
}

func (tests *xcTests) failed() int {
	summary := tests.summaryStreams[stdoutStream] || tests.summaryStreams[stderrStream]
	if !tests.testFailed || !summary || tests.summaryStreams == [2]bool{true, true} || tests.unexpected != 0 {
		return 0
	}

	return tests.failures
}

// goTests reads the test2json events go test -json writes to stdout. Build
// and setup failures are events there too. Stderr carries the go command's
// own messages: module download progress is expected there, anything else
// is an unrecognized failure.
type goTests struct {
	testFailures    map[string]bool // packages with a failed test
	packageFailures map[string]bool
	count           int
	started, blank  bool
	rejected        bool
}

func (tests *goTests) observe(stream testStream, line string) {
	if tests.rejected {
		return
	}

	if stream == stderrStream {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "go: downloading ") {
			tests.rejected = true
		}

		return
	}

	// Blank lines around the event stream are tolerated, not inside it.
	if strings.TrimSpace(line) == "" {
		tests.blank = tests.started

		return
	}

	if tests.blank {
		tests.rejected = true

		return
	}

	tests.started = true

	var event struct {
		Action  string `json:"Action"`
		Package string `json:"Package"`
		Test    string `json:"Test"`
		Output  string `json:"Output"`
	}
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		tests.rejected = true

		return
	}

	if event.Action == "build-fail" || strings.Contains(event.Output, "[build failed]") {
		tests.rejected = true

		return
	}

	// A panic, a runtime fatal error or the test timeout kills the test
	// binary: the running test is reported failed and every test after it in
	// the package never ran. That is a test error, not an assertion.
	if crashed(event.Output) {
		tests.rejected = true

		return
	}

	if event.Action == failedStatus {
		if event.Test != "" {
			tests.testFailures[event.Package] = true
			tests.count++
		} else {
			tests.packageFailures[event.Package] = true
		}
	}
}

func (tests *goTests) failed() int {
	if tests.rejected || len(tests.packageFailures) == 0 {
		return 0
	}

	for name := range tests.packageFailures {
		if !tests.testFailures[name] {
			return 0
		}
	}

	return tests.count
}

// crashed reports whether a line of test output is the Go runtime's report
// of a panic or fatal error, which starts at the beginning of a line; t.Log
// and t.Error output is indented.
func crashed(output string) bool {
	return strings.HasPrefix(output, "panic: ") || strings.HasPrefix(output, "fatal error: ") ||
		strings.Contains(output, "[setup failed]")
}

func number(raw string) int {
	value, _ := strconv.Atoi(raw)

	return value
}
