// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"fmt"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/version"
)

// SBOMsNone is the sboms input spelling for "no SBOM layers": what an empty
// effective intersection resolves to, and the snapshot default.
const SBOMsNone = "none"

// changelogCreatorGitCliff is the one changelog renderer the GitHub release
// runs.
const changelogCreatorGitCliff = "git-cliff"

// releasePolicyInputs is the typed envelope of everything the policy
// resolver reads from. Unexported: it is an internal helper input shape
// for resolveReleasePolicy; public callers use ReleasePlanInput.
type releasePolicyInputs struct {
	ReleasePolicy                   string // stable (default) | stable-prerelease
	ReleasePublisher                string // "github-cli" | "" | …
	ReleaseRequireAllowlistedSigner bool
	ReleaseDraft                    bool
	ReleaseSBOMs                    string // "all" | "none" | "build,…"
	ReleaseSignArtifacts            bool
	ChangelogCreator                string // "git-cliff" | "" (no changelog)
	ChangelogSkipVersionBump        bool
	RefName                         string // CI_REF_NAME (tag name)
	PipelineSBOMs                   string // union from parse-artifacts-config
	AnyRequireAuthorization         bool   // any artifact's require_authorization
	HasContainers                   bool   // CONTAINERS != []
}

// resolveReleasePolicy computes the typed release policy from workflow
// and config-plan inputs. Pure: no env, no logging, no warnings.
//
// Invalid policies, supplied versions and SBOM selections return errors.
// An empty policy preserves the stable-only default.
func resolveReleasePolicy(in releasePolicyInputs) (ReleasePolicy, error) {
	if err := version.ValidateReleasePolicy(in.ReleasePolicy); err != nil {
		return ReleasePolicy{}, err
	}

	// The GitHub release renders with git-cliff and nothing else. Any other
	// name would plan no version bump and no changelog without a word.
	if in.ChangelogCreator != "" && in.ChangelogCreator != changelogCreatorGitCliff {
		return ReleasePolicy{}, fmt.Errorf("changelog-creator must be git-cliff or empty (got %q): the GitHub release renders its changelog with git-cliff only: %w", in.ChangelogCreator, errs.ErrUsage)
	}

	var parsed version.Semver

	if in.RefName != "" {
		var err error

		parsed, err = version.ValidateReleaseTag(in.RefName, in.ReleasePolicy)
		if err != nil {
			return ReleasePolicy{}, err
		}
	}

	policy := ReleasePolicy{
		HasContainers:            in.HasContainers,
		SignArtifacts:            in.ReleaseSignArtifacts,
		CreateRelease:            in.ReleasePublisher == "github-cli",
		RequireAllowlistedSigner: in.AnyRequireAuthorization || in.ReleaseRequireAllowlistedSigner,
		RunVersionBump:           VersionBumpRuns(in.ChangelogCreator == changelogCreatorGitCliff, in.ChangelogSkipVersionBump),
		MakeLatest:               parsed.Prerelease == "",
		Prerelease:               parsed.Prerelease != "",
		CreateDraftRelease:       in.ReleaseDraft,
	}

	effective, conflict, err := computeEffectiveSBOMs(in.ReleaseSBOMs, in.PipelineSBOMs)
	if err != nil {
		return ReleasePolicy{}, err
	}

	policy.SBOMs = effective
	policy.SBOMConflict = conflict

	return policy, nil
}

// computeEffectiveSBOMs intersects release-cap with pipeline-union. Both
// are expanded to layer sets; intersection in canonical layer order.
//
// Returns ("none", nil, nil) when the intersection is empty AND either
// side is already "none" (intentional). Returns ("none", &Conflict, nil)
// when both sides are non-"none" but disagree (misconfig).
func computeEffectiveSBOMs(releaseSBOMs, pipelineSBOMs string) (string, *ReleaseSBOMConflict, error) {
	releaseSet, err := config.ExpandSBOMs(releaseSBOMs)
	if err != nil {
		return "", nil, err
	}

	pipelineSet, err := config.ExpandSBOMs(pipelineSBOMs)
	if err != nil {
		return "", nil, err
	}

	inRelease := layerSet(releaseSet)
	inPipeline := layerSet(pipelineSet)

	canonical := []config.SBOMLayer{
		config.SBOMLayerBuild,
		config.SBOMLayerAnalyzedArtifact,
		config.SBOMLayerAnalyzedContainer,
	}

	parts := make([]string, 0, len(canonical))
	for _, l := range canonical {
		if inRelease[l] && inPipeline[l] {
			parts = append(parts, string(l))
		}
	}

	if len(parts) == 0 {
		var conflict *ReleaseSBOMConflict
		// Surface a conflict only when both sides selected layers: a missing
		// overlap when either side is "none" is the operator's explicit
		// choice, not a misconfig. The expanded sets decide, so " none" and
		// "none\n" are the same choice as "none".
		if len(releaseSet) > 0 && len(pipelineSet) > 0 {
			conflict = &ReleaseSBOMConflict{
				ReleaseSBOMs:  releaseSBOMs,
				PipelineSBOMs: pipelineSBOMs,
			}
		}

		return SBOMsNone, conflict, nil
	}

	return strings.Join(parts, ","), nil, nil
}

func layerSet(layers []config.SBOMLayer) map[config.SBOMLayer]bool {
	m := make(map[config.SBOMLayer]bool, len(layers))
	for _, l := range layers {
		m[l] = true
	}

	return m
}
