// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabpipeline

import (
	"fmt"
	"regexp"
	"strings"

	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
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

const (
	snapshotPlanJob    = "Snapshot: plan"
	snapshotContextJob = "Snapshot: context"
)

// snapshotJobs is the rendered child pipeline: its jobs and its top-level keys.
type snapshotJobs map[string]any

// add records a job and refuses a second job of the same name.
func (jobs snapshotJobs) add(name string, job map[string]any) error {
	if _, exists := jobs[name]; exists {
		return fmt.Errorf("duplicate snapshot job %s: %w", name, errs.ErrValidation)
	}

	jobs[name] = job

	return nil
}

// SnapshotPipeline renders native jobs from the frozen graph. GitLab scheduling
// remains in the emitted YAML; this owner decides graph validity and bindings.
func SnapshotPipeline(plan snapshotimage.Plan, in SnapshotPipelineOptions) (map[string]any, error) {
	if err := normalizeSnapshotOptions(&in); err != nil {
		return nil, err
	}

	parent := map[string]any{"pipeline": in.Pipeline, keyJob: snapshotPlanJob, keyArtifacts: true}
	inputs := map[string]any{"engine-sha": in.Pin, "forge-ci-repository": in.Project, "server-url": in.Server, "registry": in.Registry,
		"job-image": in.JobImage, "build-image": in.BuildImage, "runner-tags": in.Tags, "image-builder": in.Builder, "builder-job-image": in.BuilderImage,
		"enforce-application-tests": in.ApplicationTests, "enforce-vulnerabilities": in.Vulnerabilities}
	out := snapshotJobs{"include": []any{map[string]any{"project": in.Project, "ref": in.Pin, "file": "/templates/snapshot-image-jobs.yml", "inputs": inputs}},
		"stages": []string{stageArtifacts, "context", stageBuild, stagePublish, "collect"}}

	// A context from the parent pipeline replaces this pipeline's own
	// artifact and context jobs.
	if in.Context == "" {
		if err := addSnapshotArtifactJobs(out, plan, in, parent); err != nil {
			return nil, err
		}
	}

	if err := addSnapshotFilesJobs(out, plan, in, parent); err != nil {
		return nil, err
	}

	buildNeeds, err := addSnapshotBuildJobs(out, plan, in, parent)
	if err != nil {
		return nil, err
	}

	publishNeeds := []any{parent}

	for _, image := range plan.Images {
		name := "Snapshot: publish " + image.Name
		out[name] = map[string]any{keyExtends: ".forge-ci-snapshot-publish", keyStage: stagePublish, keyScript: []string{"snapshot_gitlab_publish " + snapshotArguments(image.Name)}, keyNeeds: buildNeeds}
		publishNeeds = append(publishNeeds, map[string]any{keyJob: name, keyArtifacts: true})
	}

	out["Snapshot: collect"] = map[string]any{keyExtends: ".forge-ci-snapshot-collect", keyStage: "collect", keyNeeds: publishNeeds}

	return out, nil
}

// normalizeSnapshotOptions refuses options the jobs cannot run with and fills
// the defaults: buildah as the builder, and no context job for the plan job.
func normalizeSnapshotOptions(in *SnapshotPipelineOptions) error {
	if !enginePinPattern.MatchString(in.Pin) || !pipelineIDPattern.MatchString(in.Pipeline) {
		return fmt.Errorf("snapshot pipeline requires immutable engine pin and numeric parent pipeline: %w", errs.ErrUsage)
	}

	if in.Project == "" || strings.ContainsAny(in.Project, "\r\n") {
		return fmt.Errorf("snapshot template project required: %w", errs.ErrUsage)
	}

	if in.Builder == "" {
		in.Builder = "buildah"
	}

	if in.Builder != "buildah" && in.Builder != "kaniko" {
		return fmt.Errorf("unknown snapshot builder: %w", errs.ErrUsage)
	}

	if in.Builder == "kaniko" && (!domaincontainer.ValidDigestReference(in.BuilderImage) || in.BuildImage != in.BuilderImage) {
		return fmt.Errorf("kaniko build image must match pinned builder-job-image: %w", errs.ErrUsage)
	}

	for _, image := range []string{in.JobImage, in.BuildImage} {
		if !domaincontainer.ValidDigestReference(image) {
			return fmt.Errorf("snapshot job images must be digest-pinned: %w", errs.ErrUsage)
		}
	}

	if in.Context == snapshotPlanJob {
		in.Context = ""
	}

	return nil
}

// addSnapshotArtifactJobs adds one producing job per artifact and, when the
// plan has images, the context job that collects their outputs.
func addSnapshotArtifactJobs(out snapshotJobs, plan snapshotimage.Plan, in SnapshotPipelineOptions, parent map[string]any) error {
	contextNeeds := []any{parent}

	for _, artifact := range plan.Artifacts {
		if !jobNamePattern.MatchString(artifact.Name) {
			return fmt.Errorf("invalid snapshot artifact name: %w", errs.ErrValidation)
		}

		image, err := snapshotArtifactImage(artifact.ProjectType, in)
		if err != nil {
			return err
		}

		name := "Snapshot: artifact " + artifact.Name
		if err := out.add(name, snapshotToolchainJob(".forge-ci-snapshot-produce", "snapshot_gitlab_produce "+snapshotArguments(artifact.Name), image, parent, in)); err != nil {
			return err
		}

		contextNeeds = append(contextNeeds, map[string]any{keyJob: name, keyArtifacts: true})
	}

	if len(plan.Images) > 0 {
		out[snapshotContextJob] = map[string]any{keyExtends: ".forge-ci-snapshot-context", keyStage: "context", keyNeeds: contextNeeds}
	}

	return nil
}

// addSnapshotFilesJobs adds one job per file output. It builds the artifact
// on its own, in its family's image and with no credential as a producer
// does, and keeps the bundle as the job's artifact: under the plan's name,
// which carries the snapshot version, and for the documented retention. No
// other job needs it, and no job publishes it anywhere else.
func addSnapshotFilesJobs(out snapshotJobs, plan snapshotimage.Plan, in SnapshotPipelineOptions, parent map[string]any) error {
	for _, file := range plan.FileOutputs() {
		// GitLab expands variables in an artifact's name; the pattern admits none.
		if !jobNamePattern.MatchString(file.Name) || !jobNamePattern.MatchString(file.ArtifactName) {
			return fmt.Errorf("invalid snapshot file output name: %w", errs.ErrValidation)
		}

		image, err := snapshotArtifactImage(file.ProjectType, in)
		if err != nil {
			return err
		}

		kept := map[string]any{"name": file.ArtifactName, "paths": []string{snapshotimage.DefaultFilesDir + "/" + file.Name + "/"}, "expire_in": fmt.Sprintf("%d days", snapshotimage.FilesRetentionDays)}
		job := snapshotToolchainJob(".forge-ci-snapshot-files", "snapshot_gitlab_files "+snapshotArguments(file.Name), image, parent, in)
		job[keyArtifacts] = kept

		if err := out.add("Snapshot: files "+file.Name, job); err != nil {
			return err
		}
	}

	return nil
}

// snapshotArtifactImage is the digest-pinned runtime image that builds one
// artifact family, falling back to the job image.
func snapshotArtifactImage(projectType projecttype.Type, in SnapshotPipelineOptions) (string, error) {
	family, err := snapshotimage.ToolchainFamily(projectType)
	if err != nil {
		return "", fmt.Errorf("unsupported snapshot artifact family %s: %w", projectType, errs.ErrValidation)
	}

	var image string

	switch family {
	case snapshotimage.FamilyGo:
		image = in.Images.Go
	case snapshotimage.FamilyJava:
		image = in.Images.Java
	case snapshotimage.FamilyNode:
		image = in.Images.Node
	case snapshotimage.FamilyRust:
		image = in.Images.Rust
	}

	if image == "" {
		image = in.JobImage
	}

	if !domaincontainer.ValidDigestReference(image) {
		return "", fmt.Errorf("snapshot artifact image must be digest-pinned: %w", errs.ErrUsage)
	}

	return image, nil
}

// addSnapshotBuildJobs adds one native build job per image and architecture
// and returns the needs a publish job waits on. Every image must have a build.
func addSnapshotBuildJobs(out snapshotJobs, plan snapshotimage.Plan, in SnapshotPipelineOptions, parent map[string]any) ([]any, error) {
	images := map[string]bool{}
	for _, image := range plan.Images {
		if !jobNamePattern.MatchString(image.Name) || images[image.Name] {
			return nil, fmt.Errorf("invalid or duplicate snapshot image: %w", errs.ErrValidation)
		}

		images[image.Name] = true
	}

	need := map[string]any{keyJob: snapshotContextJob, keyArtifacts: true}
	if in.Context != "" {
		need = map[string]any{"pipeline": in.Pipeline, keyJob: in.Context, keyArtifacts: true}
	}

	buildNeeds := []any{parent}
	builtImages := map[string]bool{}

	for _, build := range plan.Builds {
		if !images[build.Name] || (build.Arch != domaincontainer.ArchAMD64 && build.Arch != domaincontainer.ArchARM64) {
			return nil, fmt.Errorf("snapshot build names unknown image or architecture: %w", errs.ErrValidation)
		}

		tags := in.AMD64Tags
		if build.Arch == domaincontainer.ArchARM64 {
			tags = in.ARM64Tags
		}

		// An untagged build could land on a runner of any platform.
		if len(tags) == 0 {
			return nil, fmt.Errorf("%s snapshot builds require native %s runner tags, build-runner-tags-linux-%s: %w", build.Arch, build.Arch, build.Arch, errs.ErrUsage)
		}

		name := "Snapshot: build " + build.Name + " " + build.Arch
		if err := out.add(name, map[string]any{keyExtends: ".forge-ci-snapshot-build", keyStage: stageBuild, "tags": tags, keyScript: []string{"snapshot_gitlab_build " + snapshotArguments(build.Name, build.Arch, in.Context)}, keyNeeds: []any{parent, need}}); err != nil {
			return nil, err
		}

		buildNeeds = append(buildNeeds, map[string]any{keyJob: name, keyArtifacts: true})
		builtImages[build.Name] = true
	}

	for _, image := range plan.Images {
		if !builtImages[image.Name] {
			return nil, fmt.Errorf("snapshot image %s has no native builds: %w", image.Name, errs.ErrValidation)
		}
	}

	return buildNeeds, nil
}

func snapshotArguments(args ...string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}

	return strings.Join(quoted, " ")
}

// snapshotToolchainJob is a job that runs the consumer's build in its
// toolchain image on an amd64 runner, after the parent's plan.
func snapshotToolchainJob(extends, script, image string, parent map[string]any, in SnapshotPipelineOptions) map[string]any {
	return map[string]any{keyExtends: extends, keyStage: stageArtifacts, "tags": in.AMD64Tags, "image": map[string]any{"name": image, "entrypoint": []string{""}}, keyScript: []string{script}, keyNeeds: []any{parent}}
}
