// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"strings"
)

// WithDraftLayer binds a consumer's unpublished native layer draft in its
// private index before another provider runs its repository-policy checks.
// Published consumers are unchanged; mixed pins retain the normal refusal.
func (consumer Consumer) WithDraftLayer(tb TB, layer Layer) Consumer {
	tb.Helper()

	ctx, cancel := context.WithTimeout(Context(tb), timeoutLayerPush)
	defer cancel()

	bound, err := consumer.bindDraftLayer(ctx, layer)
	if err != nil {
		tb.Fatalf("livetest: bind consumer draft: %v", err)
	}

	return bound
}

func (consumer Consumer) bindDraftLayer(ctx context.Context, layer Layer) (Consumer, error) {
	listed, err := consumer.git.output(ctx, consumer.stage, nil, "ls-tree", "-z", "--name-only", consumer.Commit, consumerWorkflows+"/")
	if err != nil {
		return consumer, err
	}

	hasWorkflow := false

	for path := range strings.SplitSeq(string(listed), "\x00") {
		if isWorkflowFile(path) {
			hasWorkflow = true

			break
		}
	}

	if !hasWorkflow {
		return consumer, nil
	}

	workflows, err := consumer.workflows(ctx)
	if err != nil {
		return consumer, err
	}

	draft := false
	for _, workflow := range workflows {
		draft = draft || strings.Contains(workflow, "@PIN_FORGE_CI_SHA")
	}

	if !draft {
		return consumer, nil
	}

	pin, err := ReadConsumerPin(workflows)
	if err != nil {
		return consumer, err
	}

	if pin.Commit != "PIN_FORGE_CI_SHA" {
		return consumer, nil
	}

	plan, err := layer.PlanConsumer(workflows, ConsumerFacts{
		Inputs: map[string]string{"forge-ci-repository": layer.Repository},
	})
	if err != nil {
		return consumer, err
	}

	commit, err := consumer.commitRendered(ctx, plan)
	if err != nil {
		return consumer, fmt.Errorf("render admitted draft: %w", err)
	}

	consumer.Commit = commit

	return consumer, nil
}
