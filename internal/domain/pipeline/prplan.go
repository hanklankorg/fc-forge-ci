// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pipeline

import (
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
)

// PRPlanVersion is the current pull-request plan contract version.
const PRPlanVersion = 1

// PRPlanInput contains workflow inputs for PR quality planning.
type PRPlanInput struct {
	ProjectType      projecttype.Type
	BaseBranch       string
	ForgeCIBinaryRef string
	Engine           LintEngine
	NanolinterImage  string
	SwiftFormat      bool
	SwiftLint        bool
}

// PRPlan is the top-level typed contract for PR quality workflows.
type PRPlan struct {
	Version int          `json:"version"`
	Context PRContext    `json:"context"`
	Policy  PRPolicy     `json:"policy"`
	Stages  PRStagePlans `json:"stages"`
}

// PRContext is workflow context consumed by PR quality jobs.
type PRContext struct {
	ProjectType      projecttype.Type `json:"project_type"`
	BaseBranch       string           `json:"base_branch"`
	ForgeCIBinaryRef string           `json:"forge_ci_binary_ref"`
}

// PRPolicy is the pull-request quality gate policy. Engine selects the single
// general lint engine; NanolinterImage is the image its nanolinter job runs in
// (the caller's override, or the flavour matching the project type); the swift
// flags are orthogonal language-specific checks.
type PRPolicy struct {
	Engine          LintEngine `json:"engine"`
	NanolinterImage string     `json:"nanolinter_image"`
	SwiftFormat     bool       `json:"swift_format"`
	SwiftLint       bool       `json:"swift_lint"`
	Swift           bool       `json:"swift"`
}

// PRStagePlans contains PR stage-specific plans.
type PRStagePlans struct {
	Quality PRQualityStagePlan `json:"quality"`
}

// PRQualityStagePlan describes PR quality-stage targets.
type PRQualityStagePlan struct {
	Version int              `json:"version"`
	Stage   string           `json:"stage"`
	Targets PRQualityTargets `json:"targets"`
}

// PRQualityTargets are the PR quality jobs. Nanolinter and Megalinter are the
// mutually-exclusive lint-engine jobs (at most one runs); the quality stage
// dispatches each from a static `uses:`, gated on its own `runs`.
type PRQualityTargets struct {
	Nanolinter TargetPlan[string] `json:"nanolinter"`
	Megalinter TargetPlan[string] `json:"megalinter"`
	Swift      TargetPlan[string] `json:"swift"`
}

// NewPRPlan builds the PR plan contract from workflow inputs.
func NewPRPlan(in PRPlanInput) PRPlan {
	policy := PRPolicy{
		Engine:          in.Engine,
		NanolinterImage: NanolinterImage(in.NanolinterImage, in.ProjectType),
		SwiftFormat:     in.SwiftFormat,
		SwiftLint:       in.SwiftLint,
		Swift:           in.SwiftFormat || in.SwiftLint,
	}
	quality := PRQualityStagePlan{
		Version: PRPlanVersion,
		Stage:   "pr-quality",
		Targets: PRQualityTargets{
			Nanolinter: singletonTargetPlan("nanolinter", policy.Engine == LintEngineNanolinter),
			Megalinter: singletonTargetPlan("megalinter", policy.Engine == LintEngineMegalinter),
			Swift:      singletonTargetPlan("swift", policy.Swift),
		},
	}

	return PRPlan{
		Version: PRPlanVersion,
		Context: PRContext{
			ProjectType:      in.ProjectType,
			BaseBranch:       in.BaseBranch,
			ForgeCIBinaryRef: in.ForgeCIBinaryRef,
		},
		Policy: policy,
		Stages: PRStagePlans{Quality: quality},
	}
}
