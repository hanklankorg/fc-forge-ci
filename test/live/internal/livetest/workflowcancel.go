// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

var errObservedRun = errors.New("observed workflow run refused")

// runnerCancelDrain is how long a cancel is given to reach the runner once the
// forge shows the run settled.
//
// Forgejo marks a cancelled job finished at once; its runner learns of the
// cancel only from the answer to its next task-state report, about a second
// later, and only then stops the job. A repository deleted inside that window
// takes the task with it: the runner's reports then fail with "resource does
// not exist", it never learns of the cancel, and the job keeps its runner
// slot and CPU, with its log lost, until it ends on its own.
var runnerCancelDrain = 15 * time.Second

// Cancel only the exact run already observed by this wait, before its caller's
// keep-scratch cleanup archives the repository. No run listing or prefix sweep.
func cancelObservedWorkflowRun(ctx context.Context, target Target, repo string, observed WorkflowRun) error {
	return stopObservedWorkflowRun(ctx, observed, runnerCancelDrain,
		func(ctx context.Context, id int64) (WorkflowRun, error) {
			var run WorkflowRun

			_, err := decodeQuiet(ctx, target, actionsURL(target, repo, "/actions/runs/"+strconv.FormatInt(id, 10)), &run)

			return run, err
		},
		func(ctx context.Context, id int64) error {
			return discard(ctx, target, http.MethodPost, actionsURL(target, repo, "/actions/runs/"+strconv.FormatInt(id, 10)+"/cancel"), nil, http.StatusOK, http.StatusNoContent)
		},
		func(ctx context.Context, id int64) ([]RunJob, error) {
			var jobs []RunJob

			_, err := decodeQuiet(ctx, target, actionsURL(target, repo, "/actions/runs/"+strconv.FormatInt(id, 10)+"/jobs"), &jobs)

			return jobs, err
		})
}

// stopObservedWorkflowRun cancels the observed run unless it has settled, and
// returns once it and every one of its jobs has stopped, plus drain when it
// asked to cancel, so the runner hears of the cancel before anything is
// deleted. A cancel answered 5xx counts as asked: a proxy can fail the answer
// to a cancel the forge applied.
//
// A cancel the forge fails to answer (5xx) is asked again at the next poll:
// Forgejo updates the run under an optimistic lock and answers 500 "run has
// changed" while the run's runners report, which on a busy run is often.
func stopObservedWorkflowRun(ctx context.Context, observed WorkflowRun, drain time.Duration,
	read func(context.Context, int64) (WorkflowRun, error),
	cancel func(context.Context, int64) error,
	jobs func(context.Context, int64) ([]RunJob, error),
) error {
	if observed.ID == 0 {
		return nil
	}

	cancelled, asked := false, false

	for {
		settled, err := observedRunSettled(ctx, observed, read, jobs)
		if err != nil {
			return err
		}

		if settled {
			if !asked {
				return nil
			}

			return pause(ctx, drain)
		}

		if !cancelled {
			asked = true

			// A failed top-level run can still have an independent running job.
			if cancelled, err = askToCancel(ctx, cancel, observed.ID); err != nil {
				return err
			}

			if cancelled {
				continue
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(workflowPollInterval):
		}
	}
}

// observedRunSettled reads the observed run and its jobs and reports whether
// all of them have stopped, refusing a run that is no longer the one observed.
func observedRunSettled(ctx context.Context, observed WorkflowRun,
	read func(context.Context, int64) (WorkflowRun, error),
	jobs func(context.Context, int64) ([]RunJob, error),
) (bool, error) {
	current, err := read(ctx, observed.ID)
	if err != nil {
		return false, err
	}

	if current.ID != observed.ID || current.WorkflowID != observed.WorkflowID || current.Event != observed.Event {
		return false, fmt.Errorf("observed workflow run identity changed: %w", errObservedRun)
	}

	entries, err := jobs(ctx, observed.ID)
	if err != nil {
		return false, err
	}

	return runSettled(current, entries)
}

// askToCancel reports whether the forge took the cancel. A cancel it failed
// to answer is not taken and not an error, so the caller asks again.
func askToCancel(ctx context.Context, cancel func(context.Context, int64) error, id int64) (bool, error) {
	err := cancel(ctx, id)
	if err == nil {
		return true, nil
	}

	if errors.Is(err, errServerSide) {
		return false, nil
	}

	return false, err
}

// pause waits for wait, or until ctx ends.
func pause(ctx context.Context, wait time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// runSettled reports whether the observed run and every one of its jobs has
// stopped doing work.
func runSettled(run WorkflowRun, jobs []RunJob) (bool, error) {
	settled := runIsOver(run.Status)

	for _, job := range jobs {
		if job.RunID != run.ID {
			return false, fmt.Errorf("observed workflow job belongs to another run: %w", errObservedRun)
		}

		switch job.Status {
		case "running", "waiting":
			settled = false
		case "blocked":
			// Unscheduled descendants of a failed run are not active work.
			if !runIsOver(run.Status) {
				settled = false
			}
		default:
			if !runIsOver(job.Status) {
				return false, fmt.Errorf("observed workflow job has an unknown status: %w", errObservedRun)
			}
		}
	}

	return settled, nil
}
