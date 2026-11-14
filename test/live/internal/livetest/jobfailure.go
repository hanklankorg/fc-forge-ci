// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"strings"
)

// liveInfrastructurePrefix opens the line that marks a test ended by the lab
// rather than by the product, for the run's verdict to read back.
const liveInfrastructurePrefix = "FORGE_CI_LIVE_INFRASTRUCTURE "

// JobFailure is why one job failed: the lab, or the product.
//
// A live scenario judges the product by what its jobs did. A job the lab
// failed to run -- no runner took it, the runner's system broke -- did
// nothing, so its failure says nothing about the product and must not be
// reported as if it did. Nor is it retried: release scenarios are not
// idempotent, and a retry would hide a flaky lab.
type JobFailure struct {
	Job string
	// Infrastructure is set only when the forge itself says the lab failed
	// the job. Anything it does not say so of is the product's, so a
	// classification can never hide a real failure.
	Infrastructure bool
	Reason         string
}

// gitlabInfrastructureReasons are GitLab's failure_reason values that put a
// job's failure on the runner, the scheduler or GitLab itself. Others,
// script_failure and unknown_failure among them, are the product's;
// no_matching_runner and job_execution_timeout stay there too, because a
// template's tags or a hanging step cause them as readily as the lab does.
var gitlabInfrastructureReasons = map[string]bool{
	"api_failure":              true,
	"data_integrity_failure":   true,
	"runner_system_failure":    true,
	"runner_unsupported":       true,
	"scheduler_failure":        true,
	"stuck_or_timeout_failure": true,
}

// gitlabSystemFailureMarker is how a GitLab job log ends when the runner
// failed to prepare the job; read only when the API gave no reason.
const gitlabSystemFailureMarker = "Job failed (system failure)"

// ClassifyGitLabJob judges one failed GitLab job by its structured
// failure_reason, falling back to the end of its log, which may be "", only
// when the API reports no reason at all.
func ClassifyGitLabJob(name, failureReason, log string) JobFailure {
	if failureReason != "" {
		return JobFailure{Job: name, Infrastructure: gitlabInfrastructureReasons[failureReason], Reason: failureReason}
	}

	if strings.Contains(log, gitlabSystemFailureMarker) {
		return JobFailure{Job: name, Infrastructure: true, Reason: "log: " + gitlabSystemFailureMarker}
	}

	return JobFailure{Job: name, Reason: "the forge reports no failure reason"}
}

// ClassifyForgejoJob judges one failed Forgejo job. Forgejo has no failure
// reason; what it does say is whether a runner ever took the job. A job with
// runs-on that no runner took never ran. A job without runs-on calls a
// reusable workflow, runs nowhere, and fails when what it called did, so it
// is the product's, as is a job from a forge that does not report the task.
func ClassifyForgejoJob(job RunJob) JobFailure {
	if job.TaskID != nil && *job.TaskID == 0 && len(job.RunsOn) > 0 {
		return JobFailure{Job: job.Name, Infrastructure: true, Reason: fmt.Sprintf("no runner took the job (runs-on %v)", job.RunsOn)}
	}

	return JobFailure{Job: job.Name, Reason: "the job ran or the forge does not say"}
}

// ForgejoJobFailures classifies the failed jobs of a run.
func ForgejoJobFailures(jobs []RunJob) []JobFailure {
	var failures []JobFailure

	for _, job := range jobs {
		if job.Status == "failure" {
			failures = append(failures, ClassifyForgejoJob(job))
		}
	}

	return failures
}

// OnlyInfrastructure reports whether there are failures and the lab caused
// every one of them. One product failure among them is the verdict.
func OnlyInfrastructure(failures []JobFailure) bool {
	for _, failure := range failures {
		if !failure.Infrastructure {
			return false
		}
	}

	return len(failures) > 0
}

// StopOnInfrastructure ends the test when the lab caused every failure,
// marking it so the run's verdict reads infrastructure, not failed. It
// returns, doing nothing, otherwise: the caller judges the product as before.
func StopOnInfrastructure(tb TB, failures []JobFailure) {
	tb.Helper()

	if !OnlyInfrastructure(failures) {
		return
	}

	name := ""
	if named, ok := tb.(interface{ Name() string }); ok {
		name = named.Name()
	}

	reasons := make([]string, 0, len(failures))

	for _, failure := range failures {
		// Printed, not logged, as RegisterLiveScenario's line is: the marker
		// must open its line for the verdict to read it.
		fmt.Printf("%s%s %s: %s\n", liveInfrastructurePrefix, name, failure.Job, failure.Reason)
		reasons = append(reasons, fmt.Sprintf("%q (%s)", failure.Job, failure.Reason))
	}

	tb.Fatalf("livetest: the lab, not the product, failed %s; this says nothing about the product -- the lab is what to look at",
		strings.Join(reasons, ", "))
}
