// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// RerunJob asks Forgejo to run a finished job again, with every job that
// needs it, in the job's own run: POST
// /repos/{owner}/{repo}/actions/jobs/{job_id}/rerun, which Forgejo 17 added
// (NeedsForgejo17Actions). The forge answers 204. A job it reruns keeps its
// id and is given its next attempt; the jobs it does not rerun keep theirs.
func RerunJob(tb TB, target Target, repo string, job RunJob) {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	if err := discard(ctx, target, http.MethodPost, actionsURL(target, repo, "/actions/jobs/"+strconv.FormatInt(job.ID, 10)+"/rerun"),
		nil, http.StatusNoContent); err != nil {
		tb.Fatalf("livetest: rerun job %q (%d) of run %d: %v", job.Name, job.ID, job.RunID, err)
	}
}

// WaitForRerun returns run once the rerun of its job, which RerunJob asked
// for, is over, whatever its status.
//
// A rerun starts no run: the run keeps its id, and so do its jobs. So the
// wait first reads the run and the job until the run is no longer over and
// the job is at a later attempt than the one rerun, which it is once a
// runner has taken it. Before that a run that is over may be the cancelled
// one, which the forge has not recomputed yet.
// It then waits for the run as WaitForConsumerRun does, among the runs from
// this one on. A second run of the workflow would be one nobody accounts
// for, and fails the wait.
func WaitForRerun(tb TB, target Target, repo, file, event string, run WorkflowRun, job RunJob) WorkflowRun {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutWorkflowPoll)
	defer cancel()

	runURL := actionsURL(target, repo, "/actions/runs/"+strconv.FormatInt(run.ID, 10))
	seen, seenRun := job, run

	for {
		var (
			current WorkflowRun
			jobs    []RunJob
		)

		// A failed read is "not yet", as in the wait for a run.
		if _, err := decodeQuiet(ctx, target, runURL, &current); err == nil {
			seenRun = current
		}

		if _, err := decodeQuiet(ctx, target, runURL+"/jobs", &jobs); err == nil {
			if at := slices.IndexFunc(jobs, func(listed RunJob) bool { return listed.ID == job.ID }); at >= 0 {
				seen = jobs[at]
			}
		}

		if seenRun.ID == run.ID && !runIsOver(seenRun.Status) && seen.Attempt > job.Attempt {
			break
		}

		select {
		case <-ctx.Done():
			tb.Fatalf("livetest: after its rerun was accepted, job %q was last seen %s at attempt %d (rerun from attempt %d) and run %d %s: %v",
				job.Name, seen.Status, seen.Attempt, job.Attempt, run.ID, seenRun.Status, ctx.Err())

			return WorkflowRun{}
		case <-time.After(workflowPollInterval):
		}
	}

	rerun := waitForRunOf(tb, target, repo, file, event, timeoutConsumerRun, "", run.ID-1)
	if rerun.ID != run.ID {
		tb.Fatalf("livetest: the rerun of job %q ended in run %d, not in its own run %d", job.Name, rerun.ID, run.ID)
	}

	return rerun
}
