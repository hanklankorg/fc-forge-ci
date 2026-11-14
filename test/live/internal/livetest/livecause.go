// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"regexp"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// engineError is the line the engine ends a failed command on, as a job's
// trace holds it: after the forge's timestamp and stream mark, if any.
var engineError = regexp.MustCompile(`^ {8}(?:\S+Z (?:\d\d[OE] )?)?Error: .+$`)

// jobSection is the line a test prints before a GitLab job's trace, with the
// job's status.
var jobSection = regexp.MustCompile(`^ {8}=== job .+ \((\w+)\) ===$`)

// engineCause reads a line of a failed job's trace. It reports whether the
// line is the engine's error, and the cause it gives: "infrastructure:
// <kind> (<host>)" when the engine classified its failure as a dependency
// that could not be reached or that limited it, with the kind and host that
// same line states, and "" for every other class. The harness reads the
// engine's own classification and matches no other text of a trace: a
// program a job ran can print anything.
func engineCause(line string) (string, bool) {
	line = strings.TrimRight(line, " \t\r")
	if !engineError.MatchString(line) {
		return "", false
	}

	limited := strings.HasSuffix(line, ": "+errs.ErrRateLimited.Error())
	if !limited && !strings.HasSuffix(line, ": "+errs.ErrDependencyUnavailable.Error()) {
		return "", true
	}

	kind, host, found := errs.TransportEvidence(line)

	switch {
	case limited:
		return "infrastructure: " + errs.ErrRateLimited.Error(), true
	case !found:
		return "infrastructure: " + errs.ErrDependencyUnavailable.Error(), true
	case host == "":
		return "infrastructure: " + kind, true
	default:
		return "infrastructure: " + kind + " (" + host + ")", true
	}
}

// jobTraceLine reports whether a line of a run's log is a line of a forge
// job's trace. A test prints a trace as the lines after the first of one log
// entry, which testing indents twice; what the harness says of its own
// calls to the forge is the entry's first line and is not a job's.
func jobTraceLine(line string) bool {
	return strings.HasPrefix(line, "        ")
}
