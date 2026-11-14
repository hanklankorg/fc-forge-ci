// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/diggsweden/forge-ci/internal/adapters/httpretry"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	domaingit "github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/validate"
)

// eventWorkflowCall is the event name Forgejo gives the jobs of a reusable
// workflow.
const eventWorkflowCall = "workflow_call"

// ResolveWorkflowCallEvent recovers the original trigger from the authenticated
// current-run record. Forgejo services/actions/context.go intentionally replaces
// EventName with workflow_call for inner jobs, but keeps their run ID/repository
// and source SHA. Nested calls are jobs of that same run, not new runs to chase.
//
// This is deliberately separate from offline ResolveContext: only the privileged
// event gate needs network evidence, not source builds or metadata generation.
// Gitea's SDK ActionWorkflowRun cannot express Forgejo's trigger_event/commit_sha,
// so this boundary reads the Forgejo-native response rather than guessing from
// the incompatible SDK fields or a caller-controlled event payload.
func (p *Provider) ResolveWorkflowCallEvent(ctx context.Context) (string, error) {
	get := func(suffix string) string {
		return firstNonEmpty(p.envFunc(), "FORGEJO_"+suffix, "GITHUB_"+suffix)
	}

	refuse := func(reason string) (string, error) {
		return "", fmt.Errorf("forgejo workflow_call original trigger: %s: %w", reason, errs.ErrValidation)
	}
	if get("EVENT_NAME") != eventWorkflowCall {
		return refuse("runner is not executing a reusable workflow")
	}

	runID, err := strconv.ParseInt(get("RUN_ID"), 10, 64)
	if err != nil || runID <= 0 || !domaingit.ValidCommitSHA(get("SHA")) {
		return refuse("missing or invalid runner run ID/source SHA")
	}

	repo := get("REPOSITORY")

	owner, name, err := splitRepo(repo)
	if err != nil {
		return refuse("missing or invalid runner repository")
	}

	run, reason, err := p.fetchWorkflowRun(ctx, owner, name, runID)
	if err != nil {
		return "", err
	}

	if reason != "" {
		return refuse(reason)
	}

	if run.ID != runID || run.Repository.FullName != repo || run.Repository.ID <= 0 || run.CommitSHA != get("SHA") {
		return refuse("run/repository/source identity mismatch")
	}

	if repositoryID := get("REPOSITORY_ID"); repositoryID != "" && repositoryID != strconv.FormatInt(run.Repository.ID, 10) {
		return refuse("runner repository ID mismatch")
	}

	trigger, reason := originalWorkflowTrigger(run.Event, run.TriggerEvent, run.IsForkPullRequest)
	if reason != "" {
		return refuse(reason)
	}

	return trigger, nil
}

// originalWorkflowTrigger classifies the run record's webhook event and
// trigger_event. It returns the original trigger, or a non-empty refusal
// reason when the record cannot prove one.
func originalWorkflowTrigger(rawEvent json.RawMessage, triggerEvent string, isForkPullRequest bool) (string, string) {
	// trigger_event is Forgejo's authoritative original workflow trigger.
	// Manual runs can omit their webhook event (or emit an empty string).
	// A present event must still be a string, not null or a different type.
	event := ""
	if len(rawEvent) != 0 && (string(rawEvent) == "null" || json.Unmarshal(rawEvent, &event) != nil) {
		return "", "invalid webhook event metadata"
	}
	// PR webhook variants (e.g. pull_request_sync) must never be disguised by
	// a permissive trigger field. PR-target runs can have a different source
	// SHA and are already refused by the identity check above as well.
	if isForkPullRequest || strings.HasPrefix(event, "pull_request") || validate.IsPullRequestEvent(event) || strings.HasPrefix(triggerEvent, "pull_request") || validate.IsPullRequestEvent(triggerEvent) {
		return "pull_request", ""
	}

	if triggerEvent == "" || event == eventWorkflowCall || triggerEvent == eventWorkflowCall {
		return "", "missing or ambiguous original trigger"
	}

	if event != "" && event != triggerEvent {
		return "", "webhook and original trigger disagree"
	}

	return triggerEvent, ""
}

// workflowRunRecord is the part of Forgejo's native run response the
// original-trigger gate reads.
type workflowRunRecord struct {
	ID                int64           `json:"id"`
	CommitSHA         string          `json:"commit_sha"`
	Event             json.RawMessage `json:"event"`
	TriggerEvent      string          `json:"trigger_event"`
	IsForkPullRequest bool            `json:"is_fork_pull_request"`
	Repository        struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// fetchWorkflowRun reads the authenticated current-run record. It returns a
// non-empty refusal reason when the record cannot be read or trusted, and an
// error only when the forge server URL cannot be resolved.
func (p *Provider) fetchWorkflowRun(ctx context.Context, owner, name string, runID int64) (workflowRunRecord, string, error) {
	var run workflowRunRecord

	server, err := p.serverURL()
	if err != nil {
		return run, "", err
	}

	token := p.credential().For(server)
	if token == "" {
		return run, "authenticated current-run metadata requires the forge job token", nil
	}

	endpoint := fmt.Sprintf("%s/api/v1/repos/%s/%s/actions/runs/%d", server, owner, name, runID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return run, "invalid run endpoint", nil
	}

	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := httpretry.SameAuthority(p.httpClient(), "Forgejo").Do(req)
	if err != nil {
		return run, "cannot read authenticated current-run metadata", nil
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return run, fmt.Sprintf("run metadata returned HTTP %d", resp.StatusCode), nil
	}

	const limit = 4 << 20

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit || json.Unmarshal(body, &run) != nil {
		return run, "invalid or oversized run metadata", nil
	}

	return run, "", nil
}
