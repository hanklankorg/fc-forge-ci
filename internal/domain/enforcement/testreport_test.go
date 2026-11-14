// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package enforcement

import (
	"io"
	"math/rand/v2"
	"strings"
	"testing"
)

// FailedTests classifies one complete output, as if the tool wrote all of it
// to stdout.
func FailedTests(protocol TestProtocol, body string) int {
	output := NewTestOutput(protocol)
	_, _ = io.WriteString(output.Stdout(), body)

	return output.FailedTests()
}

// write feeds body to writer in chunks of at most size bytes; size 0 picks
// random sizes.
func write(writer io.Writer, body string, size int, random *rand.Rand) {
	for body != "" {
		n := size
		if n == 0 {
			n = 1 + random.IntN(64)
		}

		n = min(n, len(body))
		_, _ = io.WriteString(writer, body[:n])
		body = body[n:]
	}
}

func TestTestOutputIgnoresWriteBoundaries(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))

	for _, test := range nativeTestFixtures {
		for _, size := range []int{1, 2, 7, 0, len(test.body) + 1} {
			output := NewTestOutput(test.protocol)
			write(output.Stdout(), test.body, size, random)

			if got := output.FailedTests(); got != test.count {
				t.Errorf("%s in %d-byte writes: count=%d want %d", test.name, size, got, test.count)
			}
		}
	}
}

func TestTestOutputKeepsStreamsApart(t *testing.T) {
	const events = "{\"Action\":\"output\",\"Package\":\"app\",\"Output\":\"=== RUN   TestValue\\n\"}\n" +
		"{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}\n"

	// Each stream is split on its own: stderr written between the halves of a
	// stdout line neither breaks that line nor is read as Go events.
	const noise = "go: downloading example.com/module v1.0.0\n"

	output := NewTestOutput(GoJSON)
	for index := range len(events) {
		_, _ = io.WriteString(output.Stdout(), events[index:index+1])
		_, _ = io.WriteString(output.Stderr(), noise[index%len(noise):index%len(noise)+1])
	}

	if got := output.FailedTests(); got != 1 {
		t.Fatalf("interleaved stderr: count=%d want 1", got)
	}

	// Gradle prints task results on stdout and the terminal cause on stderr.
	output = NewTestOutput(Gradle)
	stdout := strings.SplitAfter("> Task :test FAILED\n4 tests completed, 2 failed\n", "\n")
	stderr := strings.SplitAfter(gradleFailure, "\n")

	for index := range max(len(stdout), len(stderr)) {
		if index < len(stderr) {
			_, _ = io.WriteString(output.Stderr(), stderr[index])
		}

		if index < len(stdout) {
			_, _ = io.WriteString(output.Stdout(), stdout[index])
		}
	}

	if got := output.FailedTests(); got != 2 {
		t.Fatalf("gradle across streams: count=%d want 2", got)
	}

	// A section left open on one stream is never closed by the other.
	output = NewTestOutput(Gradle)
	_, _ = io.WriteString(output.Stdout(), "> Task :test FAILED\n4 tests completed, 2 failed\n* What went wrong:\nExecution failed for task ':test'.\n> There were failing tests.\n")
	_, _ = io.WriteString(output.Stderr(), "* Try:\nBUILD FAILED in 2s\n")

	if got := output.FailedTests(); got != 0 {
		t.Fatalf("section closed by the other stream: count=%d", got)
	}
}

func TestTestOutputRefusesOverlongLines(t *testing.T) {
	const assertion = "{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}\n"

	long := "{\"Action\":\"output\",\"Package\":\"app\",\"Output\":\"" + strings.Repeat("x", MaxTestLineBytes) + "\"}\n"

	for name, write := range map[string]func(*TestOutput){
		"stdout":       func(output *TestOutput) { _, _ = io.WriteString(output.Stdout(), long+assertion) },
		"unterminated": func(output *TestOutput) { _, _ = io.WriteString(output.Stdout(), assertion+long[:len(long)-1]) },
		// Go does not read stderr, but the line bound holds for every stream.
		"stderr": func(output *TestOutput) {
			_, _ = io.WriteString(output.Stdout(), assertion)
			_, _ = io.WriteString(output.Stderr(), strings.Repeat("y", MaxTestLineBytes+1))
		},
	} {
		output := NewTestOutput(GoJSON)
		write(output)

		if got := output.FailedTests(); got != 0 || !output.Overlong() {
			t.Errorf("%s: count=%d overlong=%t", name, got, output.Overlong())
		}
	}

	// A line of exactly the bound is still read.
	output := NewTestOutput(Cargo)
	line := "test result: FAILED. 1 passed; 2 failed;"
	_, _ = io.WriteString(output.Stdout(), line+strings.Repeat(" ", MaxTestLineBytes-len(line))+"\n")

	if got := output.FailedTests(); got != 2 || output.Overlong() {
		t.Fatalf("line at the bound: count=%d overlong=%t", got, output.Overlong())
	}
}

func TestTestOutputUnknownProtocol(t *testing.T) {
	output := NewTestOutput("unknown")
	_, _ = io.WriteString(output.Stdout(), "test result: FAILED. 1 passed; 2 failed;\n")

	if got := output.FailedTests(); got != 0 {
		t.Fatalf("count=%d", got)
	}
}

func TestTestOutputGoStderr(t *testing.T) {
	const events = "{\"Action\":\"fail\",\"Package\":\"app\",\"Test\":\"TestValue\"}\n{\"Action\":\"fail\",\"Package\":\"app\"}\n"

	for stderr, want := range map[string]int{
		"": 1,
		"go: downloading example.com/module v1.0.0\n\n":                   1,
		"go: downloading go1.27.3 (linux/amd64)\n":                        1,
		"go: writing coverage profile: permission denied\n":               0,
		"go: downloading example.com/module v1.0.0\nwarning: something\n": 0,
		"unterminated": 0,
	} {
		output := NewTestOutput(GoJSON)
		_, _ = io.WriteString(output.Stdout(), events)
		_, _ = io.WriteString(output.Stderr(), stderr)

		if got := output.FailedTests(); got != want {
			t.Errorf("stderr %q: count=%d want %d", stderr, got, want)
		}
	}
}

func TestTestOutputXCTestSummaryOnOneStream(t *testing.T) {
	// Which stream's unterminated summary is the last one cannot be known.
	output := NewTestOutput(XCTest)
	_, _ = io.WriteString(output.Stdout(), "** TEST FAILED **\n")
	_, _ = io.WriteString(output.Stderr(), "Executed 3 tests, with 1 failure (0 unexpected)")
	_, _ = io.WriteString(output.Stdout(), "Executed 3 tests, with 2 failures (1 unexpected)")

	if got := output.FailedTests(); got != 0 {
		t.Fatalf("summaries on both streams: count=%d", got)
	}

	output = NewTestOutput(XCTest)
	_, _ = io.WriteString(output.Stderr(), "** TEST FAILED **\n")
	_, _ = io.WriteString(output.Stdout(), "Executed 3 tests, with 1 failure (0 unexpected)\n")

	if got := output.FailedTests(); got != 1 {
		t.Fatalf("verdict and summary on different streams: count=%d", got)
	}
}

func TestTestOutputRefusesWrappedSummaries(t *testing.T) {
	for _, test := range []struct {
		protocol TestProtocol
		body     string
	}{
		// The Errors count of the second module is on a continuation line.
		{Maven, "Tests run: 4, Failures: 2, Errors: 0, Skipped: 0\nTests run: 4,\n Failures: 0, Errors: 1, Skipped: 0" + mavenFailure},
		{Maven, "Tests run: 4, Failures: 2, Errors: 0, Skipped: 0 Tests run: 4, Failures: 0,\nErrors: 1, Skipped: 0" + mavenFailure},
		{Cargo, "test result: FAILED. 1 passed; 2 failed;\ntest result: FAILED.\n 1 passed; 2 failed;"},
	} {
		if got := FailedTests(test.protocol, test.body); got != 0 {
			t.Errorf("%s %q: count=%d", test.protocol, test.body, got)
		}
	}
}

func TestTestOutputGradleSectionIsBounded(t *testing.T) {
	output := NewTestOutput(Gradle)
	_, _ = io.WriteString(output.Stderr(), "> Task :test FAILED\n* What went wrong:\n")

	line := strings.Repeat("x", 100) + "\n"
	for range 10000 {
		_, _ = io.WriteString(output.Stderr(), line)
	}

	gradle, _ := output.classifier.(*gradleTests)
	if held := len(gradle.sections[stderrStream].lines); held > 2 {
		t.Fatalf("an unclosed section holds %d lines", held)
	}

	if got := output.FailedTests(); got != 0 {
		t.Fatalf("count=%d", got)
	}
}
