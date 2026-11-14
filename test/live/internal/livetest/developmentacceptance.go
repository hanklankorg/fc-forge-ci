// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ValidateDevelopmentPush judges a native Forgejo development push against the
// complete run listing. Dispatch workflows and GitLab pipelines have separate
// acceptance contracts.
func ValidateDevelopmentPush(run WorkflowRun, runs []WorkflowRun, workflow string) error {
	switch {
	case run.Status != "success":
		return fmt.Errorf("development push status = %q, want success: %w", run.Status, errs.ErrValidation)
	case run.Event != "push":
		return fmt.Errorf("development push event = %q, want push: %w", run.Event, errs.ErrValidation)
	case run.TriggerEvent != "push":
		return fmt.Errorf("development push trigger event = %q, want push: %w", run.TriggerEvent, errs.ErrValidation)
	case run.WorkflowID != workflow:
		return fmt.Errorf("development push workflow = %q, want %q: %w", run.WorkflowID, workflow, errs.ErrValidation)
	case len(runs) != 1:
		return fmt.Errorf("development push run count = %d, want one: %w", len(runs), errs.ErrValidation)
	case runs[0].ID != run.ID:
		return fmt.Errorf("development push listed run ID = %d, want %d: %w", runs[0].ID, run.ID, errs.ErrValidation)
	default:
		return nil
	}
}

// SelectDevelopmentArtifact requires the sole native artifact to have the
// caller's exact source-qualified name and compressed byte bound. Payload and
// expanded archive validation remain with the caller.
func SelectDevelopmentArtifact(artifacts []RunArtifact, name string, maxBytes int64) (RunArtifact, error) {
	if len(artifacts) != 1 {
		return RunArtifact{}, fmt.Errorf("development artifact count = %d, want one: %w", len(artifacts), errs.ErrValidation)
	}

	artifact := artifacts[0]

	switch {
	case artifact.Name != name:
		return RunArtifact{}, fmt.Errorf("development artifact name = %q, want %q: %w", artifact.Name, name, errs.ErrValidation)
	case artifact.Expired:
		return RunArtifact{}, fmt.Errorf("development artifact %q is expired: %w", name, errs.ErrValidation)
	case artifact.SizeInBytes <= 0 || artifact.SizeInBytes > maxBytes:
		return RunArtifact{}, fmt.Errorf("development artifact %q size = %d, want 1..%d bytes: %w", name, artifact.SizeInBytes, maxBytes, errs.ErrValidation)
	default:
		return artifact, nil
	}
}
