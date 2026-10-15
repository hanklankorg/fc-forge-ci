// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package release

import (
	"fmt"
	"io"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/git"
	"github.com/diggsweden/forge-ci/internal/domain/provenance"
)

// ProvenanceProfile selects the release statement shape. The empty profile is
// the generic forge-ci profile; ForgejoActions preserves forgejo-ci's
// shipped release-blob provenance contract for existing downstream verifiers.
type ProvenanceProfile string

const (
	// ProvenanceProfileGeneric is the default forge-neutral release
	// provenance profile.
	ProvenanceProfileGeneric ProvenanceProfile = ""
	// ProvenanceProfileForgejoActions preserves forgejo-ci's shipped
	// release-blob provenance contract for existing downstream verifiers.
	ProvenanceProfileForgejoActions ProvenanceProfile = "forgejo-actions"
)

// ProvenanceInput drives GenerateProvenance. The CLI resolves the context
// (provider EventContext, builder/invocation identifiers, reproducible
// timestamp) and opens the readers; this use case is pure orchestration over
// the forge-neutral provenance domain so it stays testable without env, git,
// or the network.
type ProvenanceInput struct {
	// The statement's subjects, from exactly one of two sources: Checksums in
	// GoReleaser checksums-file format, or Subjects for a caller that already
	// holds names and digests.
	Checksums io.Reader
	Subjects  []provenance.Subject

	GoSum         io.Reader // optional: go.sum for resolved module deps (nil → none)
	RepositoryURL string    // <server>/<owner>/<repo>
	Ref           string    // source ref name, e.g. the tag v1.2.3
	SHA           string    // commit SHA
	BuilderID     string    // generic profile: forge-neutral build identity URI; forgejo-actions names its own
	InvocationID  string    // forge-neutral run/job URL
	StartedOn     string    // RFC3339 UTC; commit-derived for reproducibility
	Profile       ProvenanceProfile
	Workflow      string // forgejo-actions: workflow filename/path; generic: caller workflow, recorded verbatim

	// ExternalParameters are caller-declared extra
	// buildDefinition.externalParameters. Computed keys are reserved —
	// a collision is ErrValidation, never an override.
	ExternalParameters map[string]any
}

// GenerateProvenance parses the checksums (+ optional go.sum) and builds the
// signed-ready in-toto Statement JSON. The default predicate is forge-neutral;
// the forgejo-actions profile is an explicit compatibility shape for existing
// forgejo-ci release verifiers.
func GenerateProvenance(in ProvenanceInput) ([]byte, error) {
	// The ref-name grammar is the same in every refs/ namespace.
	if !git.ValidRefName("refs/tags/" + in.Ref) {
		return nil, fmt.Errorf("provenance: source ref name %q is not a git ref name: %w", in.Ref, errs.ErrUsage)
	}

	if !git.ValidCommitSHA(in.SHA) {
		return nil, fmt.Errorf("provenance: commit SHA must be a 40- or 64-character lowercase hex digest: %q: %w", in.SHA, errs.ErrValidation)
	}

	if err := validateWorkflow(in.Workflow); err != nil {
		return nil, err
	}

	subjects, err := provenanceSubjects(in)
	if err != nil {
		return nil, err
	}

	deps := []provenance.Dependency{provenance.SourceDependency(in.RepositoryURL, in.Ref, in.SHA)}

	if in.GoSum != nil {
		mods, perr := provenance.ParseGoSum(in.GoSum)
		if perr != nil {
			return nil, perr
		}

		deps = append(deps, mods...)
	}

	input := provenance.Input{
		Subjects:           subjects,
		BuildType:          provenance.ReleaseBuildType,
		BuilderID:          in.BuilderID,
		SourceURI:          "git+" + in.RepositoryURL,
		Ref:                in.Ref,
		InvocationID:       in.InvocationID,
		StartedOn:          in.StartedOn,
		FinishedOn:         in.StartedOn,
		ResolvedDeps:       deps,
		ExternalParameters: in.ExternalParameters,
	}

	if profileErr := applyProvenanceProfile(&input, in); profileErr != nil {
		return nil, profileErr
	}

	stmt, err := provenance.Build(input)
	if err != nil {
		return nil, err
	}

	return stmt.JSON()
}

// provenanceSubjects returns the statement's subjects from the one source the
// caller supplied.
func provenanceSubjects(in ProvenanceInput) ([]provenance.Subject, error) {
	switch {
	case in.Checksums != nil && len(in.Subjects) > 0:
		return nil, fmt.Errorf("provenance: subjects come from checksums or from a subject list, not both: %w", errs.ErrUsage)
	case in.Checksums != nil:
		return provenance.ParseChecksums(in.Checksums)
	case len(in.Subjects) == 0:
		return nil, fmt.Errorf("provenance: no subjects: %w", errs.ErrUsage)
	default:
		return in.Subjects, nil
	}
}

// applyProvenanceProfile shapes the statement input for the selected profile.
// The forgejo-actions profile names the workflow at the source ref as both its
// workflow object and its builder, so the two cannot disagree.
func applyProvenanceProfile(input *provenance.Input, in ProvenanceInput) error {
	switch in.Profile {
	case ProvenanceProfileGeneric:
		// A base-image signer names its caller: the base verifiers match
		// the workflow filename it records in the lineage.
		input.CallerWorkflow = in.Workflow
	case ProvenanceProfileForgejoActions:
		if in.Workflow == "" {
			return fmt.Errorf("provenance: --workflow is required for forgejo-actions profile: %w", errs.ErrUsage)
		}

		path := forgejoActionsWorkflowPath(in.Workflow)

		input.BuildType = provenance.ForgejoActionsWorkflowBuildType
		input.BuilderID = strings.TrimRight(in.RepositoryURL, "/") + "/" + path + "@" + in.Ref
		input.Workflow = &provenance.WorkflowExternalParameters{
			Ref:        in.Ref,
			Repository: in.RepositoryURL,
			Path:       path,
		}
		input.InternalParameters = map[string]string{"runner": "forgejo-actions"}
	default:
		return fmt.Errorf("provenance: unknown profile %q: %w", in.Profile, errs.ErrUsage)
	}

	return nil
}

// validateWorkflow refuses a workflow value that could not name a workflow
// file inside a repository: an absolute path, a parent-directory step, or a
// control character. The value is recorded in the statement and, for the
// forgejo-actions profile, in the builder identity, so it is held to the
// rule its verifiers hold before either is built. An empty value is not
// judged here; the forgejo-actions profile requires one itself.
func validateWorkflow(workflow string) error {
	if workflow == "" {
		return nil
	}

	if !provenance.ValidWorkflowPath(workflow) {
		return fmt.Errorf("provenance: --workflow must be a relative workflow path without '..' or control characters: %q: %w", workflow, errs.ErrUsage)
	}

	return nil
}

// forgejoActionsWorkflowPath is the repository path of a Forgejo Actions
// workflow named by filename or by its full .forgejo/workflows/ path.
func forgejoActionsWorkflowPath(workflow string) string {
	if strings.HasPrefix(workflow, ".forgejo/workflows/") {
		return workflow
	}

	return ".forgejo/workflows/" + workflow
}
