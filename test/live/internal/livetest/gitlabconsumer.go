// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// The consumer's own merge-request gate. Without a policy GitLab runs
// .gitlab-ci.yml; once the project's CI configuration path names a release
// policy, GitLab runs only the policy, which includes the gate file for
// merge-request pipelines (docs/gitlab.md, The merge-request gate under a
// policy). A consumer that keeps its gate in that file renders it there.
const (
	gitlabCIFile = ".gitlab-ci.yml"

	// GitLabGateFile is the consumer's merge-request gate file, which the
	// release policy includes for merge-request pipelines.
	GitLabGateFile = ".gitlab/merge-request.yml"
	// GitLabAdminGateFile is the gate snapshot in the independent policy project.
	GitLabAdminGateFile = "merge-request.yml"
	// GitLabAdminPipelineFile preserves a development consumer's root pipeline.
	GitLabAdminPipelineFile = "pipeline.yml"
)

// WithGitLabGateFromSource imports an admitted regular source blob into the
// private consumer index. The original checkout and application remain intact.
func (consumer Consumer) WithGitLabGateFromSource(tb TB, path string) Consumer {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	body, err := githubWorkflow(ctx, consumer, path)
	if err != nil {
		tb.Fatalf("read admitted GitLab gate: %v", err)
	}

	if err := consumer.stageFile(ctx, "100644", GitLabGateFile, body); err != nil {
		tb.Fatalf("stage admitted GitLab gate: %v", err)
	}
	// The original MR gate statically checks its source Forgejo release caller
	// against the current engine contract. Apply the same narrow destination
	// spelling migration as PlanConsumer, only in this private scratch index.
	for name, workflow := range consumer.Workflows(tb) {
		migrated, err := migratePublicationRepository(workflow)
		if err != nil {
			tb.Fatalf("scratch caller contract: %v", err)
		}

		if migrated != workflow {
			if err := consumer.stageFile(ctx, "100644", consumerWorkflows+"/"+name, migrated); err != nil {
				tb.Fatalf("stage scratch caller contract: %v", err)
			}
		}
	}

	commit, err := consumer.commit(ctx, consumerBaseMessage)
	if err != nil {
		tb.Fatalf("commit scratch GitLab gate: %v", err)
	}

	consumer.Commit = commit

	return consumer
}

// PushGitLab stages the consumer's own MR pipeline, replacing only its
// components' coordinates and job image with the trial's. Other source files,
// including the consumer's existing Forgejo policy, stay byte-for-byte intact.
// The gate is .gitlab/merge-request.yml when the consumer has one, and
// otherwise .gitlab-ci.yml.
func (consumer Consumer) PushGitLab(tb TB, repo string, layer Layer, images GitLabGateImages) string {
	tb.Helper()

	if consumer.target.Forge != provider.ForgeGitLab {
		tb.Fatalf("livetest: this consumer trial requires GitLab")
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	file := gitlabCIFile
	if consumer.HasGitLabGate(tb) {
		file = GitLabGateFile
	}

	if err := consumer.stageGitLabGate(ctx, file, layer, images); err != nil {
		tb.Fatalf("livetest: stage the consumer pipeline: %v", err)
	}

	commit, err := consumer.commit(ctx, consumerBaseMessage)
	if err == nil {
		err = consumer.git.push(ctx, consumer.stage, scratchRemote(consumer.target, repo), commit, defaultBranch)
	}

	if err != nil {
		tb.Fatalf("livetest: push the GitLab consumer: %v", err)
	}

	return commit
}

// PushGitLabRelease publishes the admitted tree for a release under a policy,
// as PushSnapshot does, with one file rendered: the merge-request gate, when
// the consumer has one, so the merge requests the policy admits run the layer
// under test.
func (consumer Consumer) PushGitLabRelease(tb TB, repo string, layer Layer, images GitLabGateImages) string {
	tb.Helper()
	requireAccepted(tb, consumer.target)

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	if _, err := consumer.git.run(ctx, consumer.stage, "read-tree", consumer.Commit); err != nil {
		tb.Fatalf("livetest: restore the consumer tree: %v", err)
	}

	if consumer.HasGitLabGate(tb) {
		if err := consumer.stageGitLabGate(ctx, GitLabGateFile, layer, images); err != nil {
			tb.Fatalf("livetest: stage the consumer's merge-request gate: %v", err)
		}
	}

	commit, err := consumer.commit(ctx, consumerBaseMessage)
	if err == nil {
		err = consumer.git.push(ctx, consumer.stage, scratchRemote(consumer.target, repo), commit, defaultBranch)
	}

	if err != nil {
		tb.Fatalf("livetest: push the GitLab consumer to %s/%s: %v", consumer.target.Owner, repo, err)
	}

	return commit
}

// HasGitLabGate reports whether the consumer keeps its merge-request gate in
// .gitlab/merge-request.yml.
func (consumer Consumer) HasGitLabGate(tb TB) bool {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	listed, err := consumer.git.run(ctx, consumer.stage, "ls-tree", "--name-only", consumer.Commit, "--", GitLabGateFile)
	if err != nil {
		tb.Fatalf("livetest: read the consumer tree: %v", err)
	}

	return listed == GitLabGateFile
}

// stageGitLabGate renders the gate file's components (check, and nanolinter
// when included) and their image with the trial's, and stages it.
func (consumer Consumer) stageGitLabGate(ctx context.Context, file string, layer Layer, images GitLabGateImages) error {
	body, err := consumer.git.output(ctx, consumer.stage, nil, "cat-file", "blob", consumer.Commit+":"+file)
	if err != nil {
		return fmt.Errorf("read the consumer's committed %s: %w", file, err)
	}

	rendered, err := renderGitLabConsumer(body, layer, images)
	if err != nil {
		return fmt.Errorf("render the GitLab coordinates of %s: %w", file, err)
	}

	return consumer.stageFile(ctx, "100644", file, rendered)
}

// GitLabAdminPolicy renders the admitted gate into an independent administrator
// policy. Enforcement literals are written here, never into the consumer index.
// The caller publishes this file in its separate policy project and pins it.
func (consumer Consumer) GitLabAdminPolicy(tb TB, layer Layer, images GitLabGateImages) string {
	tb.Helper()

	file := gitlabCIFile
	if consumer.HasGitLabGate(tb) {
		file = GitLabGateFile
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	body, err := consumer.git.output(ctx, consumer.stage, nil, "cat-file", "blob", consumer.Commit+":"+file)
	if err != nil {
		tb.Fatalf("livetest: read policy gate source: %v", err)
	}

	rendered, err := renderGitLabAdminPolicy(body, layer, images)
	if err != nil {
		tb.Fatalf("livetest: render administrator gate: %v", err)
	}

	return rendered
}

func renderGitLabAdminPolicy(body []byte, layer Layer, images GitLabGateImages) (string, error) {
	if layer.admin == nil || layer.admin.commit != layer.Commit {
		return "", fmt.Errorf("administrator gate requires captured policy: %w", errs.ErrValidation)
	}

	rendered, err := renderGitLabConsumer(body, layer, images)
	if err != nil {
		return "", err
	}

	document, entries, err := gitlabGateComponents([]byte(rendered))
	if err != nil {
		return "", err
	}

	for _, entry := range entries {
		_, name, _, _ := gitlabGateComponent(gitlabMappingValue(entry, "component"))

		declared, err := layer.admin.declaredInputs(filepath.Join("templates", name+".yml"))
		if err != nil {
			return "", err
		}

		if _, err := layer.admin.bindInputs(entry, declared, "inputs"); err != nil {
			return "", err
		}
	}

	return encodeYAML(document)
}

// GitLabDevelopmentPolicy preserves root workflow admission and development
// jobs while its local gate include resolves inside the pinned policy project.
func (consumer Consumer) GitLabDevelopmentPolicy(tb TB, layer Layer, images GitLabGateImages) (string, string) {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	root, err := consumer.git.output(ctx, consumer.stage, nil, "cat-file", "blob", consumer.Commit+":"+gitlabCIFile)
	if err != nil {
		tb.Fatalf("livetest: read development root policy source: %v", err)
	}

	gate, err := consumer.git.output(ctx, consumer.stage, nil, "cat-file", "blob", consumer.Commit+":"+GitLabGateFile)
	if err != nil {
		tb.Fatalf("livetest: read development gate policy source: %v", err)
	}

	pipeline, renderedGate, err := renderGitLabDevelopmentPolicy(root, gate, layer, images)
	if err != nil {
		tb.Fatalf("livetest: render development administrator policy: %v", err)
	}

	return pipeline, renderedGate
}

func renderGitLabDevelopmentPolicy(root, gate []byte, layer Layer, images GitLabGateImages) (string, string, error) {
	renderedGate, err := renderGitLabAdminPolicy(gate, layer, images)
	if err != nil {
		return "", "", err
	}

	var document yaml.Node
	if err := yaml.Unmarshal(root, &document); err != nil {
		return "", "", err
	}

	if len(document.Content) == 0 {
		return "", "", fmt.Errorf("development root pipeline is empty: %w", errs.ErrValidation)
	}

	includes := gitlabMappingValue(document.Content[0], "include")
	if includes == nil {
		return "", "", fmt.Errorf("development root pipeline must include its gate: %w", errs.ErrValidation)
	}

	entries := []*yaml.Node{includes}
	if includes.Kind == yaml.SequenceNode {
		entries = includes.Content
	}

	foundGate := false

	for _, entry := range entries {
		local := entry
		if entry.Kind == yaml.MappingNode {
			local = gitlabMappingValue(entry, "local")
		}

		if local != nil && local.Kind == yaml.ScalarNode && local.Value == GitLabGateFile {
			local.Value = GitLabAdminGateFile
			foundGate = true

			continue
		}

		name, _, err := bindGitLabGateComponent(entry, layer, images)
		if err != nil {
			return "", "", fmt.Errorf("development policy include: %w", err)
		}

		declared, err := layer.admin.declaredInputs(filepath.Join("templates", name+".yml"))
		if err != nil {
			return "", "", err
		}

		if _, err := layer.admin.bindInputs(entry, declared, "inputs"); err != nil {
			return "", "", err
		}
	}

	if !foundGate {
		return "", "", fmt.Errorf("development root pipeline must include %s: %w", GitLabGateFile, errs.ErrValidation)
	}

	pipeline, err := encodeYAML(&document)

	return pipeline, renderedGate, err
}

func renderGitLabConsumer(body []byte, layer Layer, images GitLabGateImages) (string, error) {
	document, entries, err := gitlabGateComponents(body)
	if err != nil {
		return "", err
	}

	if !gitlabRunsInMergeRequests(document.Content[0]) {
		return "", fmt.Errorf("the merge-request gate must run in merge-request pipelines; GitLab adds no job without rules to one. "+
			"Add workflow: rules: - if: '$CI_PIPELINE_SOURCE == \"merge_request_event\"': %w", errs.ErrValidation)
	}

	if err := bindGitLabGateComponents(entries, layer, images); err != nil {
		return "", err
	}

	for _, name := range slices.Sorted(maps.Keys(images.JobTags)) {
		job, err := gitlabTrialJob(document.Content[0], name)
		if err != nil {
			return "", err
		}

		if gitlabMappingValue(job, "tags") != nil {
			return "", fmt.Errorf("trial job tags conflict with consumer configuration: %w", errs.ErrValidation)
		}

		sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, tag := range images.JobTags[name] {
			sequence.Content = append(sequence.Content, gitlabYAMLString(tag))
		}

		job.Content = append(job.Content, gitlabYAMLString("tags"), sequence)
	}

	for _, name := range slices.Sorted(maps.Keys(images.JobVariables)) {
		if err := addGitLabTrialVariables(document.Content[0], name, images.JobVariables[name]); err != nil {
			return "", err
		}
	}

	return encodeYAML(document)
}

// gitlabYAMLString is a string scalar the trial adds to a consumer's YAML.
func gitlabYAMLString(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// gitlabTrialJob returns the named job of the consumer's configuration, added
// empty when the consumer defines none of that name.
func gitlabTrialJob(root *yaml.Node, name string) (*yaml.Node, error) {
	job := gitlabMappingValue(root, name)
	if job == nil {
		job = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, gitlabYAMLString(name), job)
	}

	if job.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("trial job %s is not a job of the consumer configuration: %w", name, errs.ErrValidation)
	}

	return job, nil
}

// addGitLabTrialVariables adds the trial's variables to the named job, beside
// the consumer's own; a name the consumer sets there is refused.
func addGitLabTrialVariables(root *yaml.Node, name string, variables map[string]string) error {
	job, err := gitlabTrialJob(root, name)
	if err != nil {
		return err
	}

	mapping := gitlabMappingValue(job, "variables")
	if mapping == nil {
		mapping = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		job.Content = append(job.Content, gitlabYAMLString("variables"), mapping)
	}

	if mapping.Kind != yaml.MappingNode {
		return fmt.Errorf("the variables of trial job %s are not a mapping: %w", name, errs.ErrValidation)
	}

	for _, key := range sortedKeys(variables) {
		if gitlabMappingValue(mapping, key) != nil {
			return fmt.Errorf("trial variable %s conflicts with the consumer's own in %s: %w", key, name, errs.ErrValidation)
		}

		mapping.Content = append(mapping.Content, gitlabYAMLString(key),
			gitlabYAMLString(variables[key]))
	}

	return nil
}

// bindGitLabGateComponents binds every included component. nanolinter
// appears once; check may run as several jobs, one per job image, all from
// one engine project; a build component may run as several jobs, and must
// come from the project the check component names, having no
// forge-ci-repository input of its own. GitLab merges two jobs of one name
// into one, so each include names its own job: job-name, which defaults to
// the component's name.
func bindGitLabGateComponents(entries []*yaml.Node, layer Layer, images GitLabGateImages) error {
	seen := map[string]bool{}
	jobs := map[string]bool{}

	var checkProject string

	builds := map[string]string{} // build component entry → engine project

	for _, entry := range entries {
		name, project, bindErr := bindGitLabGateComponent(entry, layer, images)
		if bindErr != nil {
			return bindErr
		}

		// check and a build component may run as several jobs; nanolinter once.
		if seen[name] && name != gitlabCheckComponent && !gitlabGateImageInputs[name].build {
			return fmt.Errorf("the gate includes the %s component twice: %w", name, errs.ErrValidation)
		}

		seen[name] = true

		job := gitlabGateJobName(entry, name)
		if jobs[job] {
			return fmt.Errorf("two includes name the job %s, which GitLab merges into one; give each its own job-name: %w", job, errs.ErrValidation)
		}

		jobs[job] = true

		switch {
		case name == gitlabCheckComponent && checkProject != "" && project != checkProject:
			return fmt.Errorf("the check components come from %s and %s: %w", checkProject, project, errs.ErrValidation)
		case name == gitlabCheckComponent:
			checkProject = project
		case gitlabGateImageInputs[name].build:
			builds[fmt.Sprintf("%s (include %d)", name, len(builds)+1)] = project
		}
	}

	if !seen[gitlabCheckComponent] {
		return fmt.Errorf("the MR trial requires one pinned check component: %w", errs.ErrValidation)
	}

	for _, build := range sortedKeys(builds) {
		if builds[build] != checkProject {
			return fmt.Errorf("the %s component comes from %s, not the check component's %s: %w", build, builds[build], checkProject, errs.ErrValidation)
		}
	}

	return nil
}

// gitlabGateJobName is the name of the job an include of the component name
// makes: its job-name input, which defaults to the component's name.
func gitlabGateJobName(entry *yaml.Node, name string) string {
	if value := gitlabMappingValue(gitlabMappingValue(entry, "inputs"), "job-name"); value != nil {
		return value.Value
	}

	return name
}

// GitLabGateImages are the images a trial gives the consumer's gate: Job for
// the check component, Lint for the nanolinter component. They differ because
// the job image also runs the release's own jobs, which need tools a lint
// image does not carry (ssh-keygen verifies the signed release request).
type GitLabGateImages struct {
	Job, Lint string
	// JobTags overrides only the named image-building trial job, not lint or signing.
	JobTags map[string][]string
	// JobVariables adds variables to the named image-building trial job alone,
	// the image builder it selects for example.
	JobVariables map[string]map[string]string
	// ExtraJobs are the further job images of a gate that checks its parts in
	// images of their own, keyed by NAME: a check component whose job-image is
	// $CI_JOB_IMAGE_NODE takes ExtraJobs["NODE"].
	ExtraJobs map[string]string
}

// gitlabExtraJobPlaceholder is a check component's job-image placeholder for
// a job image other than the trial's own, $CI_JOB_IMAGE_<NAME>.
var gitlabExtraJobPlaceholder = regexp.MustCompile(`^\$CI_JOB_IMAGE_([A-Z][A-Z0-9]*)$`)

// GitLabGateExtraJobImages lists the names in the $CI_JOB_IMAGE_<NAME>
// placeholders the consumer's gate gives its check components, once each and
// sorted, so a trial knows which further images to push.
func (consumer Consumer) GitLabGateExtraJobImages(tb TB) []string {
	tb.Helper()

	if !consumer.HasGitLabGate(tb) {
		return nil
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	body, err := consumer.git.output(ctx, consumer.stage, nil, "cat-file", "blob", consumer.Commit+":"+GitLabGateFile)
	if err != nil {
		tb.Fatalf("livetest: read the consumer's merge-request gate: %v", err)
	}

	names, err := gitlabGateExtraJobImages(body)
	if err != nil {
		tb.Fatalf("livetest: read the consumer's merge-request gate: %v", err)
	}

	return names
}

func gitlabGateExtraJobImages(body []byte) ([]string, error) {
	_, entries, err := gitlabGateComponents(body)
	if err != nil {
		return nil, err
	}

	var names []string

	for _, entry := range entries {
		if _, name, _, known := gitlabGateComponent(gitlabMappingValue(entry, "component")); !known || name != gitlabCheckComponent {
			continue
		}

		image := gitlabMappingValue(gitlabMappingValue(entry, "inputs"), "job-image")
		if image == nil {
			continue
		}

		if match := gitlabExtraJobPlaceholder.FindStringSubmatch(image.Value); match != nil && !slices.Contains(names, match[1]) {
			names = append(names, match[1])
		}
	}

	slices.Sort(names)

	return names, nil
}

// gitlabGateLints reports whether a gate includes the nanolinter component,
// which lints in the trial's lint image, $CI_LINT_IMAGE.
func gitlabGateLints(body []byte) (bool, error) {
	_, entries, err := gitlabGateComponents(body)
	if err != nil {
		return false, err
	}

	return slices.ContainsFunc(entries, func(entry *yaml.Node) bool {
		_, name, _, known := gitlabGateComponent(gitlabMappingValue(entry, "component"))

		return known && name == gitlabLintComponent
	}), nil
}

// gitlabGateImage is, for each component a gate may include, the input that
// takes its image, the placeholder the consumer writes there, and the image
// the trial binds. A build component runs forge-ci from its runtime image, so
// it takes the job image, which carries the engine under test.
type gitlabGateImage struct {
	input, placeholder string
	image              func(GitLabGateImages) string
	// build components have no forge-ci-repository input and may be pinned
	// by release version; check is checksum-bound and pinned by commit.
	build bool
}

// The check and lint components, the placeholder of the job image, and the input that
// takes a build component's image.
const (
	gitlabCheckComponent = "check"
	gitlabLintComponent  = "nanolinter"
	gitlabJobPlaceholder = "$CI_JOB_IMAGE"
	// gitlabServerPlaceholder stands where a gate states the instance's URL,
	// the literal the jobs hold CI_SERVER_URL to: the trial's lab.
	gitlabServerPlaceholder = "$CI_SERVER_URL"
	gitlabRuntimeInput      = "runtime-image"
)

func gitlabJobImage(images GitLabGateImages) string { return images.Job }

var gitlabGateImageInputs = map[string]gitlabGateImage{
	gitlabCheckComponent:   {input: "job-image", placeholder: gitlabJobPlaceholder, image: gitlabJobImage},
	gitlabLintComponent:    {input: "nanolinter-image", placeholder: "$CI_LINT_IMAGE", image: func(images GitLabGateImages) string { return images.Lint }},
	"build-go":             {input: gitlabRuntimeInput, placeholder: gitlabJobPlaceholder, image: gitlabJobImage, build: true},
	"build-maven":          {input: gitlabRuntimeInput, placeholder: gitlabJobPlaceholder, image: gitlabJobImage, build: true},
	"build-npm":            {input: gitlabRuntimeInput, placeholder: gitlabJobPlaceholder, image: gitlabJobImage, build: true},
	"build-gradle":         {input: gitlabRuntimeInput, placeholder: gitlabJobPlaceholder, image: gitlabJobImage, build: true},
	"build-gradle-android": {input: gitlabRuntimeInput, placeholder: gitlabJobPlaceholder, image: gitlabJobImage, build: true},
	"build-cargo":          {input: gitlabRuntimeInput, placeholder: gitlabJobPlaceholder, image: gitlabJobImage, build: true},
}

// releaseVersionPin is a component pinned by catalog release, X.Y.Z.
var releaseVersionPin = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// bindGitLabGateComponent points one included component at the layer under
// test and its image input at the trial's image, and returns the component's
// name and the engine project it names. The consumer leaves the image to the
// trial with the component's placeholder: $CI_JOB_IMAGE for check and the
// build components, $CI_LINT_IMAGE for nanolinter. check is pinned by full
// SHA; nanolinter and the build components by full SHA or release version.
// check and nanolinter name the engine project in forge-ci-repository and
// the instance in server-url, $CI_SERVER_URL, which the trial binds to its lab.
// gitlabGateCoordinatesBindable reports whether a gate component is pinned
// as the trial binds it and names the engine project and the instance by
// the coordinates the trial replaces.
func gitlabGateCoordinatesBindable(name, pin, project string, forgeCIRepository, serverURL *yaml.Node) bool {
	pinned := fixtureObjectID(pin) || (name != gitlabCheckComponent && releaseVersionPin.MatchString(pin))

	return pinned && (forgeCIRepository == nil || project == forgeCIRepository.Value) &&
		(serverURL == nil || serverURL.Value == gitlabServerPlaceholder)
}

func bindGitLabGateComponent(entry *yaml.Node, layer Layer, images GitLabGateImages) (string, string, error) {
	component := gitlabMappingValue(entry, "component")
	inputs := gitlabMappingValue(entry, "inputs")

	project, name, pin, known := gitlabGateComponent(component)
	if !known {
		return "", "", fmt.Errorf("the gate may include only pinned check, nanolinter and build components: %w", errs.ErrValidation)
	}

	use := gitlabGateImageInputs[name]

	jobImage := gitlabMappingValue(inputs, use.input)
	fields := []*yaml.Node{jobImage}

	var forgeCIRepository, serverURL *yaml.Node
	if !use.build {
		forgeCIRepository = gitlabMappingValue(inputs, "forge-ci-repository")
		serverURL = gitlabMappingValue(inputs, "server-url")
		fields = append(fields, forgeCIRepository, serverURL)
	}

	for _, field := range fields {
		if field == nil || field.Kind != yaml.ScalarNode {
			return "", "", fmt.Errorf("%s component coordinates must be scalars: %w", name, errs.ErrValidation)
		}
	}

	image, placeholder, missing := gitlabGateBoundImage(name, jobImage.Value, use, images)

	if !gitlabGateCoordinatesBindable(name, pin, project, forgeCIRepository, serverURL) || !placeholder {
		return "", "", fmt.Errorf("pin the %s component (check by full SHA), match forge-ci-repository, use %s: %s and server-url: %s: %w",
			name, use.input, use.placeholder, gitlabServerPlaceholder, errs.ErrValidation)
	}

	if image == "" {
		return "", "", fmt.Errorf("the gate's %s runs in %s; give the trial its image (%s): %w", name, jobImage.Value, missing, errs.ErrValidation)
	}

	component.Value = strings.TrimPrefix(layer.ServerURL, "https://") + "/" + layer.Repository + "/" + name + "@" + layer.Commit
	jobImage.Value = image

	if forgeCIRepository != nil {
		forgeCIRepository.Value = layer.Repository
		serverURL.Value = layer.ServerURL
	}

	return name, project, nil
}

// gitlabGateBoundImage is the trial's image for a component's image input
// value, whether that value is a placeholder the trial binds, and the setting
// that gives the image when the trial has none. A check may run in a further
// job image of the trial's, by NAME.
func gitlabGateBoundImage(name, value string, use gitlabGateImage, images GitLabGateImages) (string, bool, string) {
	if extra := gitlabExtraJobPlaceholder.FindStringSubmatch(value); name == gitlabCheckComponent && extra != nil {
		return images.ExtraJobs[extra[1]], true, "job-archive-" + strings.ToLower(extra[1]) + "="
	}

	missing := "job-archive="
	if name == gitlabLintComponent {
		missing = "lint-archive="
	}

	return use.image(images), value == use.placeholder, missing
}

// gitlabGateComponent splits an include's component reference,
// <server>/<project>/<name>@<pin>, and reports whether it names a component a
// gate may include.
func gitlabGateComponent(component *yaml.Node) (string, string, string, bool) {
	if component == nil || component.Kind != yaml.ScalarNode {
		return "", "", "", false
	}

	coordinate, pin, pinned := strings.Cut(component.Value, "@")
	_, path, _ := strings.Cut(coordinate, "/")

	slash := strings.LastIndex(path, "/")
	if !pinned || slash < 0 {
		return "", "", "", false
	}

	name := path[slash+1:]
	_, known := gitlabGateImageInputs[name]

	return path[:slash], name, pin, known
}

// gitlabRunsInMergeRequests reports whether a pipeline's workflow rules admit
// merge-request pipelines: the check component's job carries no rules of its
// own, so without them GitLab creates no pipeline for a merge request.
func gitlabRunsInMergeRequests(root *yaml.Node) bool {
	rules := gitlabMappingValue(gitlabMappingValue(root, "workflow"), "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return false
	}

	for _, rule := range rules.Content {
		if condition := gitlabMappingValue(rule, "if"); condition != nil && strings.Contains(condition.Value, "merge_request_event") {
			return true
		}
	}

	return false
}

// gitlabGateComponents reads a gate's one document and its included
// components.
func gitlabGateComponents(body []byte) (*yaml.Node, []*yaml.Node, error) {
	var document yaml.Node

	decoder := yaml.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, err
	}

	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("expected one GitLab pipeline document: %w", errs.ErrValidation)
	}

	if len(document.Content) != 1 {
		return nil, nil, fmt.Errorf("expected one GitLab pipeline document: %w", errs.ErrValidation)
	}

	includes := gitlabMappingValue(document.Content[0], "include")
	if includes == nil || includes.Kind != yaml.SequenceNode || len(includes.Content) == 0 {
		return nil, nil, fmt.Errorf("the MR trial requires one pinned check component: %w", errs.ErrValidation)
	}

	return &document, includes.Content, nil
}

func gitlabMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}

	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}

	return nil
}

// PushReleaseRequest signs a request tag with a throwaway caller-provided key.
// The private key stays in the staging directory; only its signed tag is pushed.
func (consumer Consumer) PushReleaseRequest(tb TB, repo, commit, tag, keyPath string) {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	if _, err := consumer.git.run(ctx, consumer.stage, "-c", "gpg.format=ssh", "-c", "user.signingkey="+keyPath,
		"tag", "-s", "-m", "Request "+tag, tag, commit); err != nil {
		tb.Fatalf("livetest: sign release request: %v", err)
	}

	if err := consumer.git.runPush(ctx, consumer.stage, scratchRemote(consumer.target, repo), "refs/tags/"+tag); err != nil {
		tb.Fatalf("livetest: push release request: %v", err)
	}
}
