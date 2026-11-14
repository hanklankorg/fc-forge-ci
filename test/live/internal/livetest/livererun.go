// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// The rule of the launcher's one rerun of a row. It is stricter than the
// cause a failed test is given (engineCause), and when in doubt it says no.
//
// A failed leaf test is one an unreached host explains when all of this is
// in its own output:
//
//   - the harness marked the conclusion of a forge run as the test's first
//     failure (FailOnRunConclusionf), so no oracle of the test had failed
//     before it and none ran after it;
//   - the harness listed at least one job as failed, and printed the log of
//     each of them under a heading of its own that names the job;
//   - in every such log, and in every other log the harness printed as a job
//     that did not succeed, the last error of the engine is "dependency
//     unavailable" with transport evidence of a host that could not be
//     reached (errs.TransportEvidence), and the runner's exit status 69,
//     the engine's for that class, follows it.
//
// Nothing else counts: not a line under a heading that names no failed job,
// as the end of a trace or the output of a command, and not a job that
// passed. A failed test with tests below it must have logged nothing of its
// own once one of them had started.

// liveConclusionPrefix opens the harness's mark of a run's conclusion.
const liveConclusionPrefix = "FORGE_CI_LIVE_CONCLUSION "

// The harness's lines the rule reads: a log entry's first line, a job's
// status on Forgejo and on GitLab, the heading of a failed job's log on
// Forgejo, of a job's log kept at cleanup on GitLab, and of a job's trace on
// GitLab, and a runner's line of a step's or job's exit status, as GitLab's
// "ERROR: Job failed: exit code 69" and the Forgejo runner's
// "exitcode '69': failure". A Forgejo job that calls a reusable workflow is
// printed as a caller and is no failed job here: the called jobs are.
var (
	logEntry         = regexp.MustCompile(`^ {4}\S+:\d+:(?: (.*))?$`)
	forgejoJobStatus = regexp.MustCompile(`^(\S+) +job "(.*)"$`)
	gitlabJobStatus  = regexp.MustCompile(`^job (.+): (\w+)$`)
	forgejoJobLog    = regexp.MustCompile(`^job "(.*)" log \(tail\):$`)
	gitlabJobLog     = regexp.MustCompile(`^job \d+ log \(tail\):$`)
	gitlabJobTrace   = regexp.MustCompile(`^ {8}=== job (.+) \((\w+)\) ===$`)
	runnerExit       = regexp.MustCompile(`(?i)\bexit ?(?:code|status)\W{0,3}(\d+)\b`)
)

// FailOnRunConclusionf ends the test at a forge run that did not end as the
// test wants. When nothing of the test had failed before, it first marks the
// conclusion as the test's first failure; it is then the only one, since the
// test ends here. The launcher's rerun of a row needs the mark.
func FailOnRunConclusionf(tb interface {
	TB
	Failed() bool
	Name() string
}, format string, args ...any,
) {
	tb.Helper()

	if !tb.Failed() {
		// Printed, not logged: the mark must open its line.
		fmt.Printf("%s%s\n", liveConclusionPrefix, tb.Name())
	}

	tb.Fatalf(format, args...)
}

// rerunEvidence is what a test's output says of why its forge run failed.
type rerunEvidence struct {
	// concluded: the harness marked the run's conclusion as the first
	// failure. doubt: the output has something the rule does not accept.
	// logged: the test has a log entry of its own.
	concluded, doubt, logged bool
	// failed counts the jobs the harness listed as failed, by name, and
	// explained the logs under each name that an unreached host explains:
	// a log covers one failed job. causes are what the logs give.
	failed, explained map[string]int
	causes            []string
}

// cause is the cause the test's row is run again for, or nothing.
func (evidence *rerunEvidence) cause() string {
	if evidence == nil || !evidence.concluded || evidence.doubt || len(evidence.failed) == 0 {
		return ""
	}

	for job, failed := range evidence.failed {
		if evidence.explained[job] < failed {
			return ""
		}
	}

	return strings.Join(evidence.causes, "; ")
}

// liveJobLog is the log of a job that did not succeed, as the harness prints
// it under a heading: the job's name if the heading has it, the engine's
// last error, and whether the runner's exit status 69 followed it.
type liveJobLog struct {
	evidence *rerunEvidence
	name     string
	final    string
	exited69 bool
}

// evidenceOf is the evidence of the test name in the open process.
func (scan *liveRunScan) evidenceOf(name string) *rerunEvidence {
	if scan.evidence[name] == nil {
		scan.evidence[name] = &rerunEvidence{failed: map[string]int{}, explained: map[string]int{}}
	}

	return scan.evidence[name]
}

// consumeConclusion records the harness's mark of a run's conclusion.
func (scan *liveRunScan) consumeConclusion(line string) {
	if name, marked := strings.CutPrefix(line, liveConclusionPrefix); marked && scan.active != "" && scan.runs[name] {
		scan.evidenceOf(name).concluded = true
	}
}

// consumeRerunEvidence reads a line of the output of the test the log is at.
func (scan *liveRunScan) consumeRerunEvidence(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}

	evidence := scan.evidenceOf(scan.writing)

	if !jobTraceLine(line) {
		scan.closeJobLog()

		scan.traces = false

		entry := logEntry.FindStringSubmatch(line)
		if entry == nil {
			return
		}

		evidence.logged = true
		scan.openJobLog(evidence, entry[1])

		return
	}

	// The traces of a GitLab pipeline's jobs are one entry, each under its
	// heading. A heading inside a job's own log is the job's output.
	if heading := gitlabJobTrace.FindStringSubmatch(line); heading != nil && scan.traces {
		scan.closeJobLog()

		if heading[2] == liveVerdictFailed {
			scan.job = &liveJobLog{evidence: evidence, name: heading[1]}
		}

		return
	}

	if scan.job != nil {
		scan.job.read(strings.TrimRight(line, " \t\r"))
	}
}

// openJobLog reads the first line of a log entry of the harness: a job's
// status, or the heading of a job's log.
func (scan *liveRunScan) openJobLog(evidence *rerunEvidence, entry string) {
	switch {
	case entry == "":
		scan.traces = true
	case forgejoJobLog.MatchString(entry):
		scan.job = &liveJobLog{evidence: evidence, name: forgejoJobLog.FindStringSubmatch(entry)[1]}
	case gitlabJobLog.MatchString(entry):
		scan.job = &liveJobLog{evidence: evidence}
	case forgejoJobStatus.MatchString(entry):
		if status := forgejoJobStatus.FindStringSubmatch(entry); status[1] == "failure" {
			evidence.failed[status[2]]++
		}
	case gitlabJobStatus.MatchString(entry):
		if status := gitlabJobStatus.FindStringSubmatch(entry); status[2] == liveVerdictFailed {
			evidence.failed[status[1]]++
		}
	}
}

// read takes a line of the job's log. The engine's last error is the reason
// the job failed only if the runner's last exit status after it is 69: a
// job that went on and failed on something else exits otherwise.
func (job *liveJobLog) read(line string) {
	if engineError.MatchString(line) {
		job.final, job.exited69 = line, false
	} else if exit := runnerExit.FindStringSubmatch(line); exit != nil {
		job.exited69 = job.final != "" && exit[1] == "69"
	}
}

// closeJobLog ends the job's log the scan is in, if any, and judges it: an
// unreached host explains the job, or the test's evidence is in doubt.
func (scan *liveRunScan) closeJobLog() {
	job := scan.job
	if job == nil {
		return
	}

	scan.job = nil

	cause := unreachedCause(job.final)
	if cause == "" || !job.exited69 {
		job.evidence.doubt = true

		return
	}

	if job.name != "" {
		job.evidence.explained[job.name]++
	}

	if !slices.Contains(job.evidence.causes, cause) {
		job.evidence.causes = append(job.evidence.causes, cause)
	}
}

// unreachedCause is the cause an engine's error line gives when the engine
// classified its failure as an unavailable dependency and the same line has
// transport evidence of a host that could not be reached, and nothing
// otherwise: a server that answered with an error, an empty answer and a
// limited rate are unavailable dependencies too, and waiting does not help.
func unreachedCause(line string) string {
	if _, _, found := errs.TransportEvidence(line); !found || !strings.HasSuffix(line, ": "+errs.ErrDependencyUnavailable.Error()) {
		return ""
	}

	cause, _ := engineCause(line)

	return cause
}
