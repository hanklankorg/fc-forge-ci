// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"fmt"
	"maps"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// GitLabImageBuilderEnv selects the image builder of the GitLab scenarios that
// build images: buildah, the default, on the contract's privileged runner, or
// kaniko, on the ordinary runner in a kaniko job image. On GitLab the builder
// is the release policy's input, so it is the trial's setting and no consumer
// variable; Forgejo trials select theirs with the consumer variables
// CI_IMAGE_BUILDER and CI_IMAGE_BUILDER_JOB_IMAGE.
const GitLabImageBuilderEnv = "FORGE_CI_LIVE_GITLAB_IMAGE_BUILDER"

// ForgejoImageBuilderEnv selects, as GitLabImageBuilderEnv does on GitLab,
// the image builder of the Forgejo scenarios that build images and take no
// consumer settings, such as the snapshot scenarios. A Forgejo consumer trial
// selects its builder with CI_IMAGE_BUILDER, and refuses this setting when the
// two differ.
const ForgejoImageBuilderEnv = "FORGE_CI_LIVE_FORGEJO_IMAGE_BUILDER"

// KanikoJobImageArchiveEnv names the saved kaniko job image a scenario pushes
// and labels with the layer it runs from when the run selects kaniko.
const KanikoJobImageArchiveEnv = "FORGE_CI_LIVE_KANIKO_JOB_IMAGE_ARCHIVE"

// GitLabSnapshotBuilderArchiveEnv names the saved job image required by the
// snapshot's selected builder. The caller must validate the builder selection.
func GitLabSnapshotBuilderArchiveEnv(builder string) string {
	if builder == ImageBuilderKaniko {
		return KanikoJobImageArchiveEnv
	}

	return "FORGE_CI_LIVE_BUILDAH_JOB_IMAGE_ARCHIVE"
}

// The image builders a scenario can select.
const (
	ImageBuilderBuildah = "buildah"
	ImageBuilderKaniko  = "kaniko"
)

// GitLabKaniko reports whether GitLabImageBuilderEnv selects kaniko. Any value
// but buildah, kaniko or none fails the test.
func GitLabKaniko(tb TB) bool {
	tb.Helper()

	return SelectedImageBuilder(tb, provider.ForgeGitLab) == ImageBuilderKaniko
}

// SelectedImageBuilder is the image builder the run selects for the forge's
// scenarios: buildah unless its setting says kaniko. Any other value fails
// the test.
func SelectedImageBuilder(tb TB, forge provider.ForgeAPI) string {
	tb.Helper()

	env := ImageBuilderEnv(forge)
	if env == "" {
		tb.Fatalf("livetest: no image builder is selectable on %s", forge)

		return ""
	}

	value := os.Getenv(env)
	if selected, valid := imageBuilderValue(value); valid {
		return selected
	}

	tb.Fatalf("livetest: %s must be %s or %s, got %q", env, ImageBuilderBuildah, ImageBuilderKaniko, value)

	return ""
}

func imageBuilderValue(value string) (string, bool) {
	switch value {
	case "", ImageBuilderBuildah:
		return ImageBuilderBuildah, true
	case ImageBuilderKaniko:
		return ImageBuilderKaniko, true
	default:
		return "", false
	}
}

// ForgejoConsumerImageBuilder resolves the consumer recipe's own builder,
// refusing a conflicting explicit run selector before any resources are created.
// An unset selector leaves consumer cases free to select either valid builder.
func ForgejoConsumerImageBuilder(variables map[string]string, selector string) (string, error) {
	builder, valid := imageBuilderValue(variables["CI_IMAGE_BUILDER"])
	if !valid {
		return "", fmt.Errorf("the trial's CI_IMAGE_BUILDER must be buildah or kaniko: %w", errs.ErrValidation)
	}

	if selector != "" && selector != builder {
		return "", fmt.Errorf("%s conflicts with the trial's CI_IMAGE_BUILDER; give the builder once: %w", ForgejoImageBuilderEnv, errs.ErrValidation)
	}

	return builder, nil
}

// ImageBuilderEnv is the setting that selects the forge's image builder, or ""
// for a forge whose scenarios select none.
func ImageBuilderEnv(forge provider.ForgeAPI) string {
	switch forge {
	case provider.ForgeGitLab:
		return GitLabImageBuilderEnv
	case provider.ForgeForgejo:
		return ForgejoImageBuilderEnv
	case provider.ForgeGitHub, provider.ForgeLocal:
	}

	return ""
}

// ImageBuilderChoice is where and with what a scenario's image builds run:
// Buildah on the contract's build pool, whose runners permit user namespaces,
// or kaniko in a kaniko job image.
type ImageBuilderChoice struct {
	// Builder is ImageBuilderBuildah or ImageBuilderKaniko.
	Builder string
	// RunnerLabels are the one label (GitLab tag) the image builds run on:
	// the build pool's, except for kaniko on Forgejo, which needs no user
	// namespaces and runs on the ordinary pool. GitLab's ordinary pool takes
	// untagged jobs and the product asks for a build tag, so kaniko builds
	// there carry the build pool's too.
	RunnerLabels []string

	kanikoArchive string
}

// ChooseImageBuilder resolves the run's builder selection for a scenario that
// builds images, and reports the scenario not covered when the lab cannot run
// it: Buildah without the contract's privileged runner, or kaniko without a
// saved kaniko job image. Call it before creating any resource.
func ChooseImageBuilder(tb TB, target Target) ImageBuilderChoice {
	tb.Helper()

	env := ImageBuilderEnv(target.Forge)

	if SelectedImageBuilder(tb, target.Forge) == ImageBuilderKaniko {
		archive := os.Getenv(KanikoJobImageArchiveEnv)
		if archive == "" {
			NotCoveredf(tb, "the run selects kaniko (%s=kaniko), whose image builds need %s, a saved kaniko job image", env, KanikoJobImageArchiveEnv)
		}

		if target.Forge == provider.ForgeForgejo {
			return ImageBuilderChoice{Builder: ImageBuilderKaniko, RunnerLabels: []string{RunnerLabel(tb, target)}, kanikoArchive: archive}
		}

		label := PrivilegedRunnerLabel(tb, target)
		if label == "" {
			NotCoveredf(tb, "the run selects kaniko (%s=kaniko) on GitLab, whose build jobs need a tag, and the contract publishes no build runner pool to take it from", env)
		}

		return ImageBuilderChoice{Builder: ImageBuilderKaniko, RunnerLabels: []string{label}, kanikoArchive: archive}
	}

	label := PrivilegedRunnerLabel(tb, target)
	if label == "" {
		NotCoveredf(tb, "the run selects Buildah (%s unset or buildah), whose image builds need the contract's build runner pool, whose runners permit user namespaces, and the lab publishes none; select kaniko with %s=kaniko", env, env)
	}

	return ImageBuilderChoice{Builder: ImageBuilderBuildah, RunnerLabels: []string{label}}
}

// ContainerRunnerLabel is the label of the contract's runner that permits
// user namespaces, for a scenario that runs a container itself with Podman,
// such as a service's readiness, whichever builder the run selects builds
// the image: running one needs the namespaces as rootless Buildah does. A
// lab that names no such runner reports the scenario not covered, with why
// the scenario runs a container.
func ContainerRunnerLabel(tb TB, target Target, why string) string {
	tb.Helper()

	label := PrivilegedRunnerLabel(tb, target)
	if label == "" {
		NotCoveredf(tb, "%s, which needs the contract's build runner pool, whose runners permit user namespaces, whichever builder builds the image", why)
	}

	return label
}

// JobImage is the builder's job image for the image builds of a layer served
// from layer: for kaniko, the saved kaniko job image pushed to repo and
// labelled with that layer, which admits it; for Buildah, none, since Buildah
// builds in the job's own image.
func (c ImageBuilderChoice) JobImage(tb TB, target Target, repo string, layer Layer) string {
	tb.Helper()

	if c.Builder != ImageBuilderKaniko {
		return ""
	}

	return PushKanikoJobImage(tb, target, repo, c.kanikoArchive, layer)
}

// ValidateKanikoDevelopmentJob admits the explicit builder bindings of a native
// development fixture before the trial creates remote resources. This checks
// the supported fixture shape, not arbitrary workflow or shell semantics.
func ValidateKanikoDevelopmentJob(workflow, jobName, imageVariable string) error {
	var document struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Name      string            `yaml:"name"`
			Container yaml.Node         `yaml:"container"`
			Env       map[string]string `yaml:"env"`
			Steps     []struct {
				Uses string `yaml:"uses"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		return fmt.Errorf("parse development workflow: %w", err)
	}

	matched := 0

	for _, job := range document.Jobs {
		if job.Name != jobName {
			continue
		}

		matched++

		env := make(map[string]string, len(document.Env)+len(job.Env))
		maps.Copy(env, document.Env)
		maps.Copy(env, job.Env)

		image := job.Container.Value
		if job.Container.Kind == yaml.MappingNode {
			var container struct {
				Image string `yaml:"image"`
			}
			if err := job.Container.Decode(&container); err != nil {
				return fmt.Errorf("parse development job container: %w", err)
			}

			image = container.Image
		}

		prepared := false

		for _, step := range job.Steps {
			if strings.Contains(step.Uses, "/actions/setup-buildah@") {
				return fmt.Errorf("development job %q explicitly prepares Buildah; use setup-image-builder for kaniko: %w", jobName, errs.ErrValidation)
			}

			prepared = prepared || strings.Contains(step.Uses, "/actions/setup-image-builder@")
		}

		if consumerVariableName(env["CI_IMAGE_BUILDER"]) != "CI_IMAGE_BUILDER" || consumerVariableName(env["CI_IMAGE_BUILDER_JOB_IMAGE"]) != "CI_IMAGE_BUILDER_JOB_IMAGE" || consumerVariableName(image) != imageVariable || !prepared {
			return fmt.Errorf("development job %q needs env.CI_IMAGE_BUILDER and env.CI_IMAGE_BUILDER_JOB_IMAGE from their repository variables, container image from vars.%s, and setup-image-builder for kaniko: %w", jobName, imageVariable, errs.ErrValidation)
		}
	}

	if matched != 1 {
		return fmt.Errorf("kaniko development requires exactly one job named %q: %w", jobName, errs.ErrValidation)
	}

	return nil
}

func consumerVariableName(expression string) string {
	if match := variableExpression.FindStringSubmatch(expression); match != nil {
		return match[1]
	}

	return ""
}
