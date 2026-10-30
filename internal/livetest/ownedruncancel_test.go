// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

func TestForgejoCancelExactAcknowledgedRun(t *testing.T) {
	input := os.Getenv("FORGE_CI_LIVE_CANCEL_EXACT_ACK")
	if input == "" {
		t.Skip("exact owned cancellation acknowledgement required")
	}
	var ack struct {
		Name                string
		RepositoryID, RunID int64
		Workflow            string
	}
	if err := json.Unmarshal([]byte(input), &ack); err != nil || ack.RepositoryID <= 0 || ack.RunID <= 0 || ack.Workflow == "" {
		t.Fatal("invalid exact run acknowledgement")
	}
	target := Accept(t, provider.ForgeForgejo)
	if !inRunNamespace(target, ack.Name) {
		t.Fatal("repository outside owned namespace")
	}
	ctx, cancel := context.WithTimeout(Context(t), 45*time.Second)
	defer cancel()
	var repo struct {
		ID int64 `json:"id"`
	}
	if _, err := decode(ctx, target, http.MethodGet, actionsURL(target, ack.Name, ""), nil, &repo, http.StatusOK); err != nil || repo.ID != ack.RepositoryID {
		t.Fatal("owned repository identity mismatch")
	}
	var run WorkflowRun
	if _, err := decode(ctx, target, http.MethodGet, actionsURL(target, ack.Name, "/actions/runs/"+strconv.FormatInt(ack.RunID, 10)), nil, &run, http.StatusOK); err != nil || run.ID != ack.RunID || run.WorkflowID != ack.Workflow {
		t.Fatal("owned run identity mismatch")
	}
	t.Logf("cancelling exact acknowledged run=%d repository_id=%d workflow=%s", run.ID, repo.ID, run.WorkflowID)
	if err := cancelObservedWorkflowRun(ctx, target, ack.Name, run); err != nil {
		t.Fatal("owned run cancellation/settlement failed; preserve lease for reconciliation")
	}
	t.Logf("exact owned run=%d and jobs settled", run.ID)
}
