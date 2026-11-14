// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package conformance_test

import (
	"regexp"
	"strings"
	"testing"
)

// GitLab frames output with a UTC timestamp and stream identifier. Reuse the
// failed-trace reader's terminal escape handling, without trimming job output:
// commands, section headers and longer messages are not readiness markers.
var gitlabTraceNoise = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z \d\d[OE][+ ]|\x1b\[[0-9;]*[A-Za-z]`)

func gitlabTraceLine(line string) string {
	return gitlabTraceNoise.ReplaceAllString(strings.TrimSuffix(line, "\r"), "")
}

func gitlabRecoveryCheckpointReady(trace string) bool {
	for line := range strings.SplitAfterSeq(trace, "\n") {
		// An unfinished trace chunk is not a complete output line.
		if strings.HasSuffix(line, "\n") && gitlabTraceLine(strings.TrimSuffix(line, "\n")) == "fci-live-candidate-checkpoint-ready" {
			return true
		}
	}

	return false
}

// gitlabTraceEnd is the last lines of a trace without that noise and without
// GitLab's section markers.
func gitlabTraceEnd(trace string) string {
	const keep = 40

	var lines []string

	for line := range strings.SplitSeq(trace, "\n") {
		line = strings.TrimSpace(gitlabTraceLine(line))
		if line == "" || strings.HasPrefix(line, "section_start:") || strings.HasPrefix(line, "section_end:") {
			continue
		}

		lines = append(lines, line)
	}

	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}

	return "the end of the failed job's trace:\n" + strings.Join(lines, "\n")
}

func TestGitLabTraceEndRetainsErrorBeforeTimestampedBlankLines(t *testing.T) {
	const (
		prefix  = "2026-11-04T08:48:02.284379Z 01O "
		message = "ERROR: candidate publication failed"
	)

	for _, tc := range []struct{ name, ending string }{{"LF", "\n"}, {"CRLF", "\r\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			trace := prefix + message + tc.ending + strings.Repeat(prefix+tc.ending, 45)
			if got, want := gitlabTraceEnd(trace), "the end of the failed job's trace:\n"+message; got != want {
				t.Fatalf("trace tail = %q, want %q", got, want)
			}
		})
	}
}

func TestGitLabRecoveryCheckpointTrace(t *testing.T) {
	const (
		marker       = "fci-live-candidate-checkpoint-ready"
		prefix       = "2026-11-04T08:48:02.284379Z 01O "
		continuation = "2026-11-04T08:48:02.284379Z 01O+"
	)
	for _, tc := range []struct {
		name, trace string
		want        bool
	}{
		{"actual runner trace", "previous line\n" + prefix + marker + "\n", true},
		{"runner CRLF", prefix + marker + "\r\n", true},
		{"continuation complete LF", continuation + marker + "\n", true},
		{"continuation complete CRLF", continuation + marker + "\r\n", true},
		{"continuation incomplete", continuation + marker, false},
		{"continuation echo command", continuation + "$ echo " + marker + "\n", false},
		{"continuation quoted marker", continuation + "'" + marker + "'\n", false},
		{"continuation progress message", continuation + "waiting for " + marker + "\n", false},
		{"continuation carriage return progress", continuation + "waiting\r" + marker + "\n", false},
		{"plain LF", "previous line\n" + marker + "\n", true},
		{"plain CRLF", "previous line\r\n" + marker + "\r\n", true},
		{"first line", marker + "\n", true},
		{"runner terminal formatting", prefix + "\x1b[0K\x1b[32m" + marker + "\x1b[0m\n", true},
		{"empty", "", false},
		{"no marker", prefix + "still waiting\n", false},
		{"echo command", prefix + "$ echo " + marker + "\n", false},
		{"quoted marker", prefix + "'" + marker + "'\n", false},
		{"unrelated message", prefix + "waiting for " + marker + "\n", false},
		{"suffix", prefix + marker + "-not-ready\n", false},
		{"leading space", prefix + " " + marker + "\n", false},
		{"trailing space", prefix + marker + " \n", false},
		{"not a timestamp", "arbitraryZ 01O " + marker + "\n", false},
		{"not a stream", "2026-11-04T08:48:02.284379Z 01X " + marker + "\n", false},
		{"missing separator", "2026-11-04T08:48:02.284379Z 01O" + marker + "\n", false},
		{"section text", prefix + "section_start:123:step\r\x1b[0K" + marker + "\n", false},
		{"unknown control", prefix + "\x1b]0;" + marker + "\x07\n", false},
		{"carriage return rewrite", prefix + "not ready\r" + marker + "\n", false},
		{"incomplete line", prefix + marker, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitlabRecoveryCheckpointReady(tc.trace); got != tc.want {
				t.Fatalf("checkpoint ready = %t, want %t for %q", got, tc.want, tc.trace)
			}
		})
	}
}
