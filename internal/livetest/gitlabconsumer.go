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

func renderGitLabConsumer(body []byte, layer Layer, images GitLabGateImages) (string, error) {
	const stringTag = "!!str"

	document, entries, err := gitlabGateComponents(body)
	if err != nil {
		return "", err
	}

	if !gitlabRunsInMergeRequests(document.Content[0]) {
		return "", fmt.Errorf("the merge-request gate must run in merge-request pipelines; GitLab adds no job without rules to one. "+
			"Add workflow: rules: - if: '$CI_PIPELINE_SOURCE == \"merge_request_event\"': %w", errs.ErrValidation)
	}

	seen := map[string]bool{}

	for _, entry := range entries {
		name, bindErr := bindGitLabGateComponent(entry, layer, images)
		if bindErr != nil {
			return "", bindErr
		}

		if seen[name] {
			return "", fmt.Errorf("the gate includes the %s component twice: %w", name, errs.ErrValidation)
		}

		seen[name] = true
	}

	if !seen["check"] {
		return "", fmt.Errorf("the MR trial requires one pinned check component: %w", errs.ErrValidation)
	}

	for name, tags := range images.JobTags {
		job := gitlabMappingValue(document.Content[0], name)
		if job == nil {
			job = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			document.Content[0].Content = append(document.Content[0].Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: stringTag, Value: name}, job)
		}

		if job.Kind != yaml.MappingNode || gitlabMappingValue(job, "tags") != nil {
			return "", fmt.Errorf("trial job tags conflict with consumer configuration: %w", errs.ErrValidation)
		}

		sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, tag := range tags {
			sequence.Content = append(sequence.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: stringTag, Value: tag})
		}

		job.Content = append(job.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: stringTag, Value: "tags"}, sequence)
	}

	var encoded bytes.Buffer

	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	err = encoder.Encode(document)

	return encoded.String(), err
}

// GitLabGateImages are the images a trial gives the consumer's gate: Job for
// the check component, Lint for the nanolinter component. They differ because
// the job image also runs the release's own jobs, which need tools a lint
// image does not carry (ssh-keygen verifies the signed release request).
type GitLabGateImages struct {
	Job, Lint string
	// JobTags overrides only the named image-building trial job, not lint or signing.
	JobTags map[string][]string
}

// gitlabGateImage is, for each component a gate may include, the input that
// takes its image, the placeholder the consumer writes there, and the image
// the trial binds.
type gitlabGateImage struct {
	input, placeholder string
	image              func(GitLabGateImages) string
}

var gitlabGateImageInputs = map[string]gitlabGateImage{
	"check":      {input: "job-image", placeholder: "$CI_JOB_IMAGE", image: func(images GitLabGateImages) string { return images.Job }},
	"nanolinter": {input: "nanolinter-image", placeholder: "$CI_LINT_IMAGE", image: func(images GitLabGateImages) string { return images.Lint }},
}

// bindGitLabGateComponent points one included component at the layer under
// test and its image input at the trial's image, and returns the component's
// name. The consumer must pin the component by full SHA, name the engine
// project the component comes from, and leave the image to the trial with the
// component's placeholder: $CI_JOB_IMAGE for check, $CI_LINT_IMAGE for
// nanolinter.
func bindGitLabGateComponent(entry *yaml.Node, layer Layer, images GitLabGateImages) (string, error) {
	component := gitlabMappingValue(entry, "component")
	inputs := gitlabMappingValue(entry, "inputs")

	project, name, pin, known := gitlabGateComponent(component)
	if !known {
		return "", fmt.Errorf("the gate may include only pinned check and nanolinter components: %w", errs.ErrValidation)
	}

	use := gitlabGateImageInputs[name]
	forgeCIRepository := gitlabMappingValue(inputs, "forge-ci-repository")

	jobImage := gitlabMappingValue(inputs, use.input)
	for _, field := range []*yaml.Node{forgeCIRepository, jobImage} {
		if field == nil || field.Kind != yaml.ScalarNode {
			return "", fmt.Errorf("%s component coordinates must be scalars: %w", name, errs.ErrValidation)
		}
	}

	if !fixtureObjectID(pin) || project != forgeCIRepository.Value || jobImage.Value != use.placeholder {
		return "", fmt.Errorf("pin the %s component by full SHA, match forge-ci-repository and use %s: %s: %w",
			name, use.input, use.placeholder, errs.ErrValidation)
	}

	image := use.image(images)
	if image == "" {
		return "", fmt.Errorf("the gate includes the %s component; give the trial its image (lint-archive=): %w", name, errs.ErrValidation)
	}

	component.Value = strings.TrimPrefix(layer.ServerURL, "https://") + "/" + layer.Repository + "/" + name + "@" + layer.Commit
	forgeCIRepository.Value = layer.Repository
	jobImage.Value = image

	return name, nil
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
