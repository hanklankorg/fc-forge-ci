// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/retry"
)

// A probe checks its own claim inside one job, so its conclusion is the whole
// result. A workflow of the delivery layer is several jobs that hand values
// and artifacts to each other, and what it left on the forge is part of what
// it promises. These helpers read that back through Forgejo's Actions API: the
// runs of a repository, the jobs of a run, a job's log, and a run's artifacts.
//
// They are Forgejo's alone. The layer under test is Forgejo's, and GitLab has
// neither called workflows nor a run-artifact store to read.

const (
	// maxArtifactZIP bounds one downloaded artifact archive.
	maxArtifactZIP = 32 << 20
	// maxJobLog bounds one downloaded job log.
	maxJobLog = 8 << 20
	// jobLogTail is how much of a job log a failure prints.
	jobLogTail = 4000
)

// WorkflowRun is one run as the forge lists it.
type WorkflowRun struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Event  string `json:"event"`
	// TriggerEvent is the event that started the run, where Event is the
	// one the workflow declared. They agree for every run a scenario judges.
	TriggerEvent string `json:"trigger_event"`
	WorkflowID   string `json:"workflow_id"`
}

// RunJob is one job of a run.
type RunJob struct {
	ID      int64    `json:"id"`
	RunID   int64    `json:"run_id"`
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	RunsOn  []string `json:"runs_on"`
	Attempt int64    `json:"attempt"`
}

// RunArtifact is one artifact a run left in the forge's store.
type RunArtifact struct {
	ID          int64
	Name        string
	SizeInBytes int64
	Expired     bool
}

// RunLayerWorkflow commits a workflow to the scratch repository and returns
// the run that push triggers once the run is over, whatever its status.
func RunLayerWorkflow(tb TB, target Target, repo, name, yaml string) WorkflowRun {
	tb.Helper()
	requireForgejoActions(tb, target)

	file, err := workflowPath(target.Forge, name)
	if err != nil {
		tb.Fatalf("livetest: %v", err)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	if err := commitFile(ctx, target, repo, file, "livetest: "+name, yaml); err != nil {
		tb.Fatalf("livetest: commit workflow %s: %v", file, err)
	}

	return WaitForLayerRun(tb, target, repo, name, eventPush)
}

// eventPush is the event of a run a push started.
const eventPush = "push"

// WaitForLayerRun returns the one run of workflow name that event started,
// once that run is over, whatever its status. The scenario has already done
// what triggers it: committed a file, opened a pull request, pushed a tag.
//
// RunWorkflow cannot wait for a workflow of the layer. It reads the newest
// task, and a task is one job: right for a probe, which is one job, and wrong
// for a run whose second job has no task until the first has finished, where
// it would report the first job's success as the run's. So this reads the
// run, whose status is over only when every job is.
//
// Nothing is logged here on failure: the caller has the run and asks for the
// jobs it wants named.
func WaitForLayerRun(tb TB, target Target, repo, name, event string) WorkflowRun {
	tb.Helper()

	return waitForRunOf(tb, target, repo, name+".yml", event, timeoutLayerRun, "", 0)
}

// WaitForConsumerRun is WaitForLayerRun for a workflow of a real consumer,
// named by its file: a consumer's pipeline is as long as the consumer made
// it, so the wait is the longer one. A non-empty interruptAfter cancels the
// run once a job whose name starts with it has succeeded.
func WaitForConsumerRun(tb TB, target Target, repo, file, event, interruptAfter string) WorkflowRun {
	tb.Helper()

	return waitForRunOf(tb, target, repo, file, event, timeoutConsumerRun, interruptAfter, 0)
}

// WaitForConsumerRunAfter excludes the completed first release when a trial
// requests another version in the same repository.
func WaitForConsumerRunAfter(tb TB, target Target, repo, file, event string, afterID int64) WorkflowRun {
	tb.Helper()

	return waitForRunOf(tb, target, repo, file, event, timeoutConsumerRun, "", afterID)
}

func waitForRunOf(tb TB, target Target, repo, file, event string, timeout time.Duration, interruptAfter string, afterID int64) WorkflowRun {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeout)
	defer cancel()

	progress := runProgress{started: time.Now(), reported: time.Now()}

	var observed WorkflowRun
	defer func() {
		if observed.ID == 0 {
			return
		}
		// Fatal failures can unwind before the wait's context is cancelled.
		// Settle our observed run on every exit, preserving the primary failure.
		cleanup, closeCleanup := context.WithTimeout(context.WithoutCancel(ctx), timeoutAPI)
		defer closeCleanup()

		if err := cancelObservedWorkflowRun(cleanup, target, repo, observed); err != nil {
			// Provider errors can contain private bodies: report only owned scope.
			tb.Errorf("livetest: cancellation/terminal observation failed for owned run %d of %s; inspect it before reusing the scratch repository", observed.ID, file)
		}
	}()

	for {
		var payload struct {
			Runs []WorkflowRun `json:"workflow_runs"`
		}

		// A failed read is "not yet", as in waitForRun: a run is often not
		// listed in the moment after the event that creates it.
		if _, err := decodeQuiet(ctx, target, actionsURL(target, repo, "/actions/runs"), &payload); err == nil {
			run, passedOver, err := selectRun(payload.Runs, file, event, afterID)
			if err != nil {
				tb.Fatalf("livetest: %v", err)
			}

			if run.ID != 0 {
				observed = run
			}

			if run.ID != 0 && runIsOver(run.Status) {
				for _, other := range passedOver {
					tb.Logf("livetest: run %d of %s was started by a %s, not a %s, and is not judged", other.ID, other.WorkflowID, other.Event, event)
				}

				return run
			}

			progress.report(tb, run, file)

			if interruptAt(ctx, tb, target, repo, run, interruptAfter) {
				interruptAfter = ""
			}
		}

		select {
		case <-ctx.Done():
			tb.Fatalf("livetest: the %s run of workflow %s wait ended: %v", event, file, ctx.Err())

			return WorkflowRun{}
		case <-time.After(workflowPollInterval):
		}
	}
}

// interruptAt cancels run once a job whose name starts with prefix has
// succeeded, and reports whether it did; an empty prefix interrupts nothing.
// A failed read is "not yet", as in the wait; a refused cancel is a failure,
// because the trial would otherwise judge a run it meant to interrupt.
func interruptAt(ctx context.Context, tb TB, target Target, repo string, run WorkflowRun, prefix string) bool {
	tb.Helper()

	if prefix == "" || run.ID == 0 {
		return false
	}

	var jobs []RunJob

	if _, err := decodeQuiet(ctx, target, actionsURL(target, repo, "/actions/runs/"+strconv.FormatInt(run.ID, 10)+"/jobs"), &jobs); err != nil {
		return false
	}

	for _, job := range jobs {
		if !strings.HasPrefix(job.Name, prefix) || job.Status != runSuccess {
			continue
		}

		if err := discard(ctx, target, http.MethodPost, actionsURL(target, repo, "/actions/runs/"+strconv.FormatInt(run.ID, 10)+"/cancel"),
			nil, http.StatusOK, http.StatusNoContent); err != nil {
			tb.Fatalf("livetest: cancel run %d after job %q: %v", run.ID, job.Name, err)
		}

		tb.Logf("livetest: cancelled run %d after job %q succeeded", run.ID, job.Name)

		return true
	}

	return false
}

// runProgressInterval is how often a wait says the run it awaits is not over.
const runProgressInterval = 5 * time.Minute

// runProgress shows that a long wait is alive: the run it awaits, once it is
// listed, and that run's status every few minutes. A consumer's release is
// otherwise silent for half an hour, which reads as a hang.
type runProgress struct {
	started, reported time.Time
	announced         bool
}

func (progress *runProgress) report(tb TB, run WorkflowRun, file string) {
	tb.Helper()

	if run.ID == 0 {
		return
	}

	if !progress.announced {
		progress.announced = true

		tb.Logf("livetest: waiting for run %d of %s", run.ID, file)
	}

	if time.Since(progress.reported) >= runProgressInterval {
		progress.reported = time.Now()

		tb.Logf("livetest: run %d of %s is %q after %s", run.ID, file, run.Status, time.Since(progress.started).Round(time.Minute))
	}
}

// selectRun picks, among the runs of one workflow file, the run event started.
//
// A consumer's pull-request workflow may also start on a push, and Forgejo
// then lists a push run beside the pull request's own. That run is not the
// scenario's subject and says nothing about it, so a push run is passed over
// when another event is awaited, and returned so the caller can say so. Nothing
// else is: a run of the workflow with
// any other event is a run nobody can account for. An empty event is accepted
// only for a manual dispatch with its authoritative matching trigger_event;
// and two runs of the awaited event leave no way to say which is judged.
// A nonzero afterID limits this judgment to newer runs of a consecutive trial.
func selectRun(runs []WorkflowRun, file, event string, afterID int64) (WorkflowRun, []WorkflowRun, error) {
	var (
		selected   WorkflowRun
		passedOver []WorkflowRun
	)

	for _, run := range runs {
		if run.WorkflowID != file || (afterID > 0 && run.ID <= afterID) {
			continue
		}

		switch {
		case runMatchesEvent(run, event):
			if selected.ID != 0 {
				return WorkflowRun{}, nil, fmt.Errorf("workflow %s has two %s runs, %d and %d: %w", file, event, selected.ID, run.ID, errs.ErrValidation)
			}

			selected = run
		case runMatchesEvent(run, eventPush):
			passedOver = append(passedOver, run)
		default:
			return WorkflowRun{}, nil, fmt.Errorf("workflow %s has run %d of event %q (trigger %q), want %s: %w",
				file, run.ID, run.Event, run.TriggerEvent, event, errs.ErrValidation)
		}
	}

	return selected, passedOver, nil
}

func runMatchesEvent(run WorkflowRun, event string) bool {
	return run.TriggerEvent == event && (run.Event == event || (event == "workflow_dispatch" && run.Event == ""))
}

// The statuses Forgejo and GitLab both spell this way, for a run and for a job.
const (
	runSuccess   = "success"
	runFailure   = "failure"
	runCancelled = "cancelled"
	runSkipped   = "skipped"
)

// runIsOver reports whether Forgejo will not change a run's status again.
func runIsOver(status string) bool {
	switch status {
	case runSuccess, runFailure, runCancelled, runSkipped, "error":
		return true
	default:
		return false
	}
}

// WorkflowRuns lists the repository's runs, newest first.
func WorkflowRuns(tb TB, target Target, repo string) []WorkflowRun {
	tb.Helper()

	var payload struct {
		Runs []WorkflowRun `json:"workflow_runs"`
	}

	readActions(tb, target, repo, "/actions/runs", &payload)

	return payload.Runs
}

// RunJobs lists the jobs of one run, in the forge's order.
func RunJobs(tb TB, target Target, repo string, runID int64) []RunJob {
	tb.Helper()

	var jobs []RunJob

	readActions(tb, target, repo, "/actions/runs/"+strconv.FormatInt(runID, 10)+"/jobs", &jobs)

	return jobs
}

// JobLog returns one job's whole log, or fails when the forge serves none.
// A log that holds the target's credential is reported, and what is returned
// is redacted either way, so a scenario may print from it.
func JobLog(tb TB, target Target, repo string, job RunJob) string {
	tb.Helper()

	log, err := jobLog(tb, target, repo, job)
	if err != nil {
		tb.Fatalf("livetest: read the log of job %q: %v", job.Name, err)
	}

	assertNoTargetSecret(tb, target, "the log of job "+job.Name, log)

	return redactTargetSecrets(target, log)
}

// JobLogTail returns the end of one job's log with the target's credential
// redacted, or "" when the forge serves none.
//
// Best effort, as runLogTail is and for its reason: it is read on a path that
// is already failing, and a log that cannot be fetched must not replace the
// verdict. It differs in naming the job. A run's log archive holds one file per
// job, and the first of them is the failing one only when the run has one job.
func JobLogTail(tb TB, target Target, repo string, job RunJob) string {
	tb.Helper()

	log, err := jobLog(tb, target, repo, job)
	if err != nil {
		return ""
	}

	if len(log) > jobLogTail {
		log = log[len(log)-jobLogTail:]
	}

	return redactTargetSecrets(target, log)
}

func jobLog(tb TB, target Target, repo string, job RunJob) (string, error) {
	tb.Helper()
	requireAccepted(tb, target)

	if job.Attempt == 0 {
		return "", fmt.Errorf("the job reports no attempt: %w", errs.ErrValidation)
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutWorkflowLog)
	defer cancel()

	endpoint := actionsURL(target, repo, "/actions/jobs/"+strconv.FormatInt(job.ID, 10)+
		"/logs?attempt="+strconv.FormatInt(job.Attempt, 10))

	// A finished job's log does not change, so a 5xx is the forge failing to
	// serve it, not an answer: Forgejo has answered 500 for the log of a job
	// whose run succeeded. Ask again a few times; any other answer is final.
	log, err := retry.Do(ctx, nil, jobLogAttempts, jobLogRetryDelay, func() ([]byte, error) {
		body, fetchErr := fetch(ctx, target, endpoint, maxJobLog)
		if fetchErr != nil && !errors.Is(fetchErr, errServerSide) {
			return nil, retry.Permanent(fetchErr)
		}

		return body, fetchErr
	}, retry.OnRetry(func(attempt, attempts int, wait time.Duration, err error) {
		tb.Logf("livetest: the log of job %q was not served (attempt %d/%d), asking again in %s: %v", job.Name, attempt, attempts, wait, err)
	}))

	return string(log), err
}

// jobLogAttempts and jobLogRetryDelay bound how long a scenario waits for a
// forge that fails to serve a finished job's log: about 30 seconds in all.
const (
	jobLogAttempts   = 4
	jobLogRetryDelay = 5 * time.Second
)

// RunArtifacts lists what one run left in the artifact store. An entry the
// forge lists without an id, a size or an expiry is a failure here, so a
// scenario never compares against a zero the forge did not report.
func RunArtifacts(tb TB, target Target, repo string, runID int64) []RunArtifact {
	tb.Helper()

	var listed []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		SizeInBytes *int64 `json:"size_in_bytes"`
		Expired     *bool  `json:"expired"`
	}

	readActions(tb, target, repo, "/actions/runs/"+strconv.FormatInt(runID, 10)+"/artifacts", &listed)

	artifacts := make([]RunArtifact, 0, len(listed))

	for _, entry := range listed {
		if entry.ID <= 0 || entry.SizeInBytes == nil || entry.Expired == nil {
			tb.Fatalf("livetest: %s lists artifact %q of run %d without an id, a size or an expiry", target.Forge, entry.Name, runID)
		}

		artifacts = append(artifacts, RunArtifact{
			ID: entry.ID, Name: entry.Name, SizeInBytes: *entry.SizeInBytes, Expired: *entry.Expired,
		})
	}

	return artifacts
}

// ArtifactZIP downloads one artifact as the archive the forge serves.
//
// This is the independent half of an artifact round trip: the job downloads
// through the product and compares for itself, and here the same artifact is
// read with no product in the path.
func ArtifactZIP(tb TB, target Target, repo string, artifactID int64) []byte {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	archive, err := fetch(ctx, target,
		actionsURL(target, repo, "/actions/artifacts/"+strconv.FormatInt(artifactID, 10)+"/zip"), maxArtifactZIP)
	if err != nil {
		tb.Fatalf("livetest: download artifact %d: %v", artifactID, err)
	}

	return archive
}

// ZIPTree reads an archive into the files it holds, by name.
//
// It refuses what a tree comparison would otherwise have to think about: a
// name that is absolute, unclean or climbs out of the root, an entry that is
// not a regular file, a name that occurs twice, and content beyond limit
// bytes in total. What is left is a map a scenario compares with the tree it
// expects, and an entry the expectation does not name is then a plain
// difference.
func ZIPTree(archive []byte, limit int64) (map[string][]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("read the archive: %w", err)
	}

	tree := make(map[string][]byte, len(reader.File))

	for _, file := range reader.File {
		if !cleanZIPEntryName(file.Name) || !file.Mode().IsRegular() {
			return nil, fmt.Errorf("entry %q is not a regular file at a clean relative path: %w", file.Name, errs.ErrValidation)
		}

		if _, duplicate := tree[file.Name]; duplicate {
			return nil, fmt.Errorf("entry %q occurs twice: %w", file.Name, errs.ErrValidation)
		}

		content, err := readZIPEntry(file, limit)
		if err != nil {
			return nil, err
		}

		limit -= int64(len(content))
		tree[file.Name] = content
	}

	return tree, nil
}

func cleanZIPEntryName(name string) bool {
	return name != "" && name != "." && !strings.Contains(name, `\`) && !strings.HasPrefix(name, "/") &&
		!strings.HasSuffix(name, "/") && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../")
}

// readZIPEntry reads one entry, refusing content beyond limit bytes. The
// bound is on what is read and not on the size the archive declares.
func readZIPEntry(file *zip.File, limit int64) ([]byte, error) {
	entry, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open entry %q: %w", file.Name, err)
	}

	content, readErr := io.ReadAll(io.LimitReader(entry, limit+1))
	if err := errors.Join(readErr, entry.Close()); err != nil {
		return nil, fmt.Errorf("read entry %q: %w", file.Name, err)
	}

	if int64(len(content)) > limit {
		return nil, fmt.Errorf("entry %q takes the archive past its size limit: %w", file.Name, errs.ErrValidation)
	}

	return content, nil
}

// SetRepoVariable creates an Actions variable on the scratch repository.
//
// The layer schedules every job by `vars.CI_RUNNER_LABEL`, so a workflow of
// the layer stays queued on a repository that has no such variable. It is
// created and never replaced: a scratch repository is new each time.
func SetRepoVariable(tb TB, target Target, repo, name, value string) {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	if err := discard(ctx, target, http.MethodPost, actionsURL(target, repo, "/actions/variables/"+url.PathEscape(name)),
		map[string]any{"value": value}, http.StatusCreated, http.StatusNoContent); err != nil {
		tb.Fatalf("livetest: set variable %s on %s/%s: %v", name, target.Owner, repo, err)
	}
}

// RunnerLabel is the label the contract says this endpoint's runner takes
// jobs for.
//
// The probes of this tier name `ubuntu-latest` themselves and bring their own
// image. A workflow of the layer runs in the image the label selects, so the
// label is the environment's to state, and a contract that deploys a runner
// without publishing its label cannot schedule the layer at all.
func RunnerLabel(tb TB, target Target) string {
	tb.Helper()
	requireAccepted(tb, target)

	label := runnerOf(target.endpoint).Label
	if label == "" {
		tb.Fatalf("livetest: the contract publishes no runner.label for endpoint %q, so a workflow of the layer has nothing to schedule on",
			target.endpoint.Name)
	}

	return label
}

// PrivilegedRunnerLabel is the label the contract says a runner that permits
// user namespaces takes jobs for, or "" when it names none. Rootless Buildah
// needs them, and the runner under RunnerLabel may deny them.
func PrivilegedRunnerLabel(tb TB, target Target) string {
	tb.Helper()
	requireAccepted(tb, target)

	return runnerOf(target.endpoint).PrivilegedLabel
}

// readActions decodes one Actions API answer of the scratch repository.
func readActions(tb TB, target Target, repo, route string, into any) {
	tb.Helper()
	requireForgejoActions(tb, target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutAPI)
	defer cancel()

	if _, err := decode(ctx, target, http.MethodGet, actionsURL(target, repo, route), nil, into, http.StatusOK); err != nil {
		tb.Fatalf("livetest: read %s of %s/%s: %v", route, target.Owner, repo, err)
	}
}

func actionsURL(target Target, repo, route string) string {
	return target.BaseURL() + "/api/v1/repos/" + url.PathEscape(target.Owner) + "/" + url.PathEscape(repo) + route
}

func requireForgejoActions(tb TB, target Target) {
	tb.Helper()
	requireAccepted(tb, target)

	if target.Forge != provider.ForgeForgejo {
		tb.Fatalf("livetest: reading runs, jobs and artifacts is implemented for Forgejo, not for %s", target.Forge)
	}
}
