// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabpipeline

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// BaseStage is one stage of the release's base image graph, from the
// base-stages policy input: a root, one flavor without a parent, or the
// children of one parent, one flavor or siblings.
type BaseStage struct {
	// Flavors are the base images the stage builds.
	Flavors []string
	// Parent is the flavor every image of the stage builds on; empty for a
	// root.
	Parent string
}

// Name is the stage's name in its jobs' names and as their argument.
func (s BaseStage) Name() string { return strings.Join(s.Flavors, "+") }

var baseFlavorPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

const imageBuilderKaniko = "kaniko"

// ParseBaseStages reads the base-stages policy input: stages separated by
// spaces, each <flavor> for a root or <flavor>[+<flavor>...]:<parent> for
// the children of one parent, which an earlier stage builds. A child builds
// with Buildah only: its build job holds no registry credential, and kaniko
// has no local store to load the verified parent into, so a kaniko policy
// with a child stage is refused here, before anything runs.
func ParseBaseStages(input, imageBuilder string) ([]BaseStage, error) {
	var stages []BaseStage

	known := map[string]bool{}

	for spec := range strings.FieldsSeq(input) {
		flavors, parent, child := strings.Cut(spec, ":")
		stage := BaseStage{Flavors: strings.Split(flavors, "+"), Parent: parent}

		for _, flavor := range append(slices.Clone(stage.Flavors), parent) {
			if flavor == parent && !child {
				continue
			}

			if !baseFlavorPattern.MatchString(flavor) {
				return nil, fmt.Errorf("base-stages entry %q: %q is no base flavor, a lowercase name of letters, digits and hyphens: %w", spec, flavor, errs.ErrUsage)
			}
		}

		switch {
		case !child && len(stage.Flavors) != 1:
			return nil, fmt.Errorf("base-stages entry %q: a root stage is one flavor; siblings name their parent as <flavor>+<flavor>:<parent>: %w", spec, errs.ErrUsage)
		case child && !known[parent]:
			return nil, fmt.Errorf("base-stages entry %q: the parent %q is no flavor of an earlier stage: %w", spec, parent, errs.ErrUsage)
		case child && imageBuilder == imageBuilderKaniko:
			return nil, fmt.Errorf("base-stages entry %q builds on the verified parent %q, which needs image-builder buildah: the build job holds no registry credential, and kaniko has no local store to load the parent into. Select buildah, on a runner that permits user namespaces: %w", spec, parent, errs.ErrUnsupported)
		}

		for _, flavor := range stage.Flavors {
			if known[flavor] {
				return nil, fmt.Errorf("base-stages names the flavor %q twice: %w", flavor, errs.ErrUsage)
			}

			known[flavor] = true
		}

		stages = append(stages, stage)
	}

	return stages, nil
}

// The base stage's jobs. A stage's jobs carry its name; the gate is one job.
const (
	jobBasePlan    = "Base: plan "
	jobBaseParent  = "Base: parent "
	jobBaseBuild   = "Base: build "
	jobBasePublish = "Base: publish "
	jobBasePromote = "Base: sign and promote "
	jobBaseGate    = "Base: all promoted"
	jobPrepare     = "Prepare: release commit and tag"
)

// baseStageJobNames are the names of a stage's jobs, in the order they run.
func baseStageJobNames(stage BaseStage) []string {
	names := []string{jobBasePlan + stage.Name()}
	if stage.Parent != "" {
		names = append(names, jobBaseParent+stage.Name())
	}

	return append(names, jobBaseBuild+stage.Name(), jobBasePublish+stage.Name(), jobBasePromote+stage.Name())
}

// baseJobNames are the generated base jobs: each stage's, then the gate.
func baseJobNames(stages []BaseStage) []string {
	var names []string

	for _, stage := range stages {
		names = append(names, baseStageJobNames(stage)...)
	}

	if len(stages) > 0 {
		names = append(names, jobBaseGate)
	}

	return names
}

func need(job string, artifacts bool) map[string]any {
	return map[string]any{"job": job, "artifacts": artifacts}
}

// addBaseJobs renders the stages' jobs. Each extends its hidden job of
// templates/release-jobs.yml, which holds its role, image, runner and
// secrets, and adds what only the graph knows: its needs, and one script
// line with the stage and its parent as literals. A child's plan needs the
// promotion of the stage that built its parent, and its build the parent
// job, so no child builds before its parent is promoted and verified. The
// gate needs every promotion and hands their projections on; Build: release
// files waits for it.
func addBaseJobs(jobs map[string]any, stages []BaseStage) {
	builtBy := map[string]string{}
	gate := make([]any, 0, len(stages))

	for _, stage := range stages {
		name := stage.Name()
		arguments := " '" + name + "' '" + stage.Parent + "'"
		plan, parent := need(jobBasePlan+name, true), need(jobBaseParent+name, true)

		planNeeds := []any{need(jobPrepare, false)}
		buildNeeds := []any{plan}
		publishNeeds := []any{plan, need(jobBaseBuild+name, true)}

		if stage.Parent != "" {
			promoted := need(jobBasePromote+builtBy[stage.Parent], true)
			planNeeds = append(planNeeds, promoted)
			buildNeeds = append(buildNeeds, parent)
			publishNeeds = append(publishNeeds, parent)
			jobs[jobBaseParent+name] = map[string]any{
				keyExtends: ".forge-ci-release-base-parent", keyNeeds: []any{plan, promoted},
				keyScript: []string{"rc_base_parent" + arguments},
			}
		}

		jobs[jobBasePlan+name] = map[string]any{keyExtends: ".forge-ci-release-base-plan", keyNeeds: planNeeds, keyScript: []string{"rc_base_plan" + arguments}}
		jobs[jobBaseBuild+name] = map[string]any{keyExtends: ".forge-ci-release-base-build", keyNeeds: buildNeeds, keyScript: []string{"rc_base_build" + arguments}}
		jobs[jobBasePublish+name] = map[string]any{keyExtends: ".forge-ci-release-base-publish", keyNeeds: publishNeeds, keyScript: []string{"rc_base_publish" + arguments}}
		jobs[jobBasePromote+name] = map[string]any{
			keyExtends: ".forge-ci-release-base-promote", keyNeeds: []any{plan, need(jobBasePublish+name, true)},
			keyScript: []string{"rc_base_promote" + arguments},
		}

		for _, flavor := range stage.Flavors {
			builtBy[flavor] = name
		}

		gate = append(gate, need(jobBasePromote+name, true))
	}

	if len(stages) > 0 {
		jobs[jobBaseGate] = map[string]any{keyExtends: ".forge-ci-release-base-gate", keyNeeds: gate}
	}
}
