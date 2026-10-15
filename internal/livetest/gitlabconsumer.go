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

// PushGitLab stages the consumer's own MR pipeline, replacing only its check
// component coordinates and job image with the trial's. Other source files,
// including the consumer's existing Forgejo policy, stay byte-for-byte intact.
func (consumer Consumer) PushGitLab(tb TB, repo string, layer Layer, image string) string {
	tb.Helper()

	if consumer.target.Forge != provider.ForgeGitLab {
		tb.Fatalf("livetest: this consumer trial requires GitLab")
	}

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	body, err := consumer.git.output(ctx, consumer.stage, nil, "cat-file", "blob", consumer.Commit+":.gitlab-ci.yml")
	if err != nil {
		tb.Fatalf("livetest: read the consumer's committed .gitlab-ci.yml: %v", err)
	}

	rendered, err := renderGitLabConsumer(body, layer, image)
	if err != nil {
		tb.Fatalf("livetest: render the consumer's GitLab coordinates: %v", err)
	}

	if stageErr := consumer.stageFile(ctx, "100644", ".gitlab-ci.yml", rendered); stageErr != nil {
		tb.Fatalf("livetest: stage the consumer pipeline: %v", stageErr)
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

func renderGitLabConsumer(body []byte, layer Layer, image string) (string, error) {
	document, entry, err := gitlabCheckComponent(body)
	if err != nil {
		return "", err
	}

	component := gitlabMappingValue(entry, "component")
	inputs := gitlabMappingValue(entry, "inputs")
	project := gitlabMappingValue(inputs, "engine-project")

	jobImage := gitlabMappingValue(inputs, "job-image")
	for _, field := range []*yaml.Node{component, project, jobImage} {
		if field == nil || field.Kind != yaml.ScalarNode {
			return "", fmt.Errorf("check component coordinates must be scalars: %w", errs.ErrValidation)
		}
	}

	coordinate, pin, found := strings.Cut(component.Value, "@")

	_, path, _ := strings.Cut(coordinate, "/")
	if !found || !fixtureObjectID(pin) || path != project.Value+"/check" || jobImage.Value != "$CI_JOB_IMAGE" {
		return "", fmt.Errorf("pin the check component by full SHA, match engine-project and use job-image: $CI_JOB_IMAGE: %w", errs.ErrValidation)
	}

	component.Value = strings.TrimPrefix(layer.ServerURL, "https://") + "/" + layer.Repository + "/check@" + layer.Commit
	project.Value = layer.Repository
	jobImage.Value = image

	var encoded bytes.Buffer

	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	err = encoder.Encode(document)

	return encoded.String(), err
}

func gitlabCheckComponent(body []byte) (*yaml.Node, *yaml.Node, error) {
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
	if includes == nil || includes.Kind != yaml.SequenceNode || len(includes.Content) != 1 {
		return nil, nil, fmt.Errorf("the MR trial requires one pinned check component: %w", errs.ErrValidation)
	}

	return &document, includes.Content[0], nil
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

	if _, err := consumer.git.run(ctx, consumer.stage, eventPush, gitQuiet, scratchRemote(consumer.target, repo), "refs/tags/"+tag); err != nil {
		tb.Fatalf("livetest: push release request: %v", err)
	}
}
