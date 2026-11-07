// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabpipeline

import (
	"fmt"
	"regexp"
	"strings"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
)

// SnapshotPipelineOptions are trusted policy inputs, never consumer job variables.
type SnapshotPipelineOptions struct {
	Project, Pin, Server, Registry, Pipeline, Context string
	JobImage, BuildImage, Builder, BuilderImage       string
	Tags, AMD64Tags, ARM64Tags                        []string
	Images                                            RuntimeImages
	ApplicationTests, Vulnerabilities                 bool
}

var (
	enginePinPattern  = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)
	pipelineIDPattern = regexp.MustCompile(`^[0-9]+$`)
	jobNamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// SnapshotPipeline renders native jobs from the frozen graph. GitLab scheduling
// remains in the emitted YAML; this owner decides graph validity and bindings.
func SnapshotPipeline(plan snapshotimage.Plan, in SnapshotPipelineOptions) (map[string]any, error) {
	if !enginePinPattern.MatchString(in.Pin) || !pipelineIDPattern.MatchString(in.Pipeline) {
		return nil, fmt.Errorf("snapshot pipeline requires immutable engine pin and numeric parent pipeline: %w", errs.ErrUsage)
	}

	if in.Project == "" || strings.ContainsAny(in.Project, "\r\n") {
		return nil, fmt.Errorf("snapshot template project required: %w", errs.ErrUsage)
	}

	if in.Builder == "" {
		in.Builder = "buildah"
	}

	if in.Builder != "buildah" && in.Builder != "kaniko" {
		return nil, fmt.Errorf("unknown snapshot builder: %w", errs.ErrUsage)
	}

	if in.Builder == "kaniko" && (!domaincontainer.ValidDigestReference(in.BuilderImage) || in.BuildImage != in.BuilderImage) {
		return nil, fmt.Errorf("kaniko build image must match pinned builder-job-image: %w", errs.ErrUsage)
	}

	for _, image := range []string{in.JobImage, in.BuildImage} {
		if !domaincontainer.ValidDigestReference(image) {
			return nil, fmt.Errorf("snapshot job images must be digest-pinned: %w", errs.ErrUsage)
		}
	}

	if in.Context == "Snapshot: plan" {
		in.Context = ""
	}

	parent := map[string]any{"pipeline": in.Pipeline, "job": "Snapshot: plan", "artifacts": true}
	inputs := map[string]any{"engine-sha": in.Pin, "forge-ci-repository": in.Project, "server-url": in.Server, "registry": in.Registry,
		"job-image": in.JobImage, "build-image": in.BuildImage, "runner-tags": in.Tags, "image-builder": in.Builder, "builder-job-image": in.BuilderImage,
		"enforce-application-tests": in.ApplicationTests, "enforce-vulnerabilities": in.Vulnerabilities}
	out := map[string]any{"include": []any{map[string]any{"project": in.Project, "ref": in.Pin, "file": "/templates/snapshot-image-jobs.yml", "inputs": inputs}},
		"stages": []string{"artifacts", "context", "build", "publish", "collect"}}
	buildNeeds := []any{parent}
	contextNeeds := []any{parent}
	publishNeeds := []any{parent}
	add := func(name string, job map[string]any) error {
		if _, exists := out[name]; exists {
			return fmt.Errorf("duplicate snapshot job %s: %w", name, errs.ErrValidation)
		}

		out[name] = job

		return nil
	}

	if in.Context == "" {
		for _, artifact := range plan.Artifacts {
			if !jobNamePattern.MatchString(artifact.Name) {
				return nil, fmt.Errorf("invalid snapshot artifact name: %w", errs.ErrValidation)
			}

			image := ""

			switch artifact.ProjectType {
			case "go":
				image = in.Images.Go
			case "maven", "gradle":
				image = in.Images.Java
			case "npm":
				image = in.Images.Node
			case "cargo":
				image = in.Images.Rust
			default:
				return nil, fmt.Errorf("unsupported snapshot artifact family %s: %w", artifact.ProjectType, errs.ErrValidation)
			}

			if image == "" {
				image = in.JobImage
			}

			if !domaincontainer.ValidDigestReference(image) {
				return nil, fmt.Errorf("snapshot artifact image must be digest-pinned: %w", errs.ErrUsage)
			}

			name := "Snapshot: artifact " + artifact.Name
			if err := add(name, map[string]any{"extends": ".forge-ci-snapshot-produce", "stage": "artifacts", "tags": in.AMD64Tags, "image": map[string]any{"name": image, "entrypoint": []string{""}}, "script": []string{"snapshot_gitlab_produce " + snapshotArguments(artifact.Name)}, "needs": []any{parent}}); err != nil {
				return nil, err
			}

			contextNeeds = append(contextNeeds, map[string]any{"job": name, "artifacts": true})
		}

		if len(plan.Images) > 0 {
			out["Snapshot: context"] = map[string]any{"extends": ".forge-ci-snapshot-context", "stage": "context", "needs": contextNeeds}
		}
	}

	images := map[string]bool{}
	for _, image := range plan.Images {
		if !jobNamePattern.MatchString(image.Name) || images[image.Name] {
			return nil, fmt.Errorf("invalid or duplicate snapshot image: %w", errs.ErrValidation)
		}

		images[image.Name] = true
	}

	builtImages := map[string]bool{}

	for _, build := range plan.Builds {
		if !images[build.Name] || (build.Arch != "amd64" && build.Arch != "arm64") {
			return nil, fmt.Errorf("snapshot build names unknown image or architecture: %w", errs.ErrValidation)
		}

		tags := in.AMD64Tags
		if build.Arch == "arm64" {
			tags = in.ARM64Tags
			if len(tags) == 0 {
				return nil, fmt.Errorf("arm64 snapshot builds require native arm64 runner tags: %w", errs.ErrUsage)
			}
		}

		need := map[string]any{"job": "Snapshot: context", "artifacts": true}
		if in.Context != "" {
			need = map[string]any{"pipeline": in.Pipeline, "job": in.Context, "artifacts": true}
		}

		name := "Snapshot: build " + build.Name + " " + build.Arch
		if err := add(name, map[string]any{"extends": ".forge-ci-snapshot-build", "stage": "build", "tags": tags, "script": []string{"snapshot_gitlab_build " + snapshotArguments(build.Name, build.Arch, in.Context)}, "needs": []any{parent, need}}); err != nil {
			return nil, err
		}

		buildNeeds = append(buildNeeds, map[string]any{"job": name, "artifacts": true})
		builtImages[build.Name] = true
	}

	for _, image := range plan.Images {
		if !builtImages[image.Name] {
			return nil, fmt.Errorf("snapshot image %s has no native builds: %w", image.Name, errs.ErrValidation)
		}

		name := "Snapshot: publish " + image.Name
		out[name] = map[string]any{"extends": ".forge-ci-snapshot-publish", "stage": "publish", "script": []string{"snapshot_gitlab_publish " + snapshotArguments(image.Name)}, "needs": buildNeeds}
		publishNeeds = append(publishNeeds, map[string]any{"job": name, "artifacts": true})
	}

	out["Snapshot: collect"] = map[string]any{"extends": ".forge-ci-snapshot-collect", "stage": "collect", "needs": publishNeeds}

	return out, nil
}

func snapshotArguments(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}

	return strings.Join(quoted, " ")
}
