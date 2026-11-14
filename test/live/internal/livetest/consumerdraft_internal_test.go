// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestConsumerDraftRendersCoordinatesAndPolicyBeforeServing(t *testing.T) {
	t.Parallel()

	const marker = "PIN_FORGE_CI_SHA"

	source := strings.NewReplacer(consumerPinned, marker, consumerSignerImage, "PIN_SIGNER_IMAGE_DIGEST", consumerKeyDigest, "PIN_COSIGN_PUBLIC_KEY_SHA256").Replace(consumerWorkflow(t, "hello-cli-release.yml"))
	layer := sampleLayer()

	plan, err := layer.PlanConsumer(map[string]string{"release.yml": source}, releaseFacts())
	if err != nil {
		t.Fatal(err)
	}

	rendered := plan.Render(source)
	for _, gone := range []string{marker, "PIN_SIGNER_IMAGE_DIGEST", "PIN_COSIGN_PUBLIC_KEY_SHA256", "examplescope/forge-ci/.forgejo"} {
		if strings.Contains(rendered, gone) {
			t.Errorf("lab rendering retained draft coordinate %q", gone)
		}
	}

	policy := `const engine = "` + marker + `"`
	if got := plan.Render(policy); !strings.Contains(got, layer.Commit) || strings.Contains(got, marker) {
		t.Fatalf("draft policy did not follow the frozen lab layer: %s", got)
	}
}

func TestConsumerDraftAcceptsUniformPinAcrossWorkflows(t *testing.T) {
	t.Parallel()

	const marker = "PIN_FORGE_CI_SHA"

	pin, err := ReadConsumerPin(map[string]string{
		"pullrequest.yml": "uses: https://forge.example/owner/engine/.forgejo/actions/setup-forge-ci@" + marker + "\n",
		"release.yml":     "uses: owner/engine/.forgejo/workflows/sign-and-publish-release.yml@" + marker + "\n",
	})
	if err != nil || pin.Commit != marker {
		t.Fatalf("uniform draft pin = %+v, %v", pin, err)
	}
}

func TestConsumerDraftRejectsMixedPinsAndUnknownMarkers(t *testing.T) {
	t.Parallel()

	const use = "uses: https://forge.example/owner/engine/.forgejo/actions/setup-forge-ci@"

	for name, workflows := range map[string]map[string]string{
		"mixed draft and commit": {"a.yml": use + "PIN_FORGE_CI_SHA\n", "b.yml": use + strings.Repeat("a", 40) + "\n"},
		"unknown marker":         {"a.yml": use + "PIN_ANOTHER_ENGINE\n"},
		"marker suffix":          {"a.yml": use + "PIN_FORGE_CI_SHA_OTHER\n"},
		"branch":                 {"a.yml": use + "main\n"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := ReadConsumerPin(workflows); !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("invalid lab template accepted: %v", err)
			}
		})
	}
}

func TestConsumerPinSupportsFullSHA256LayerCommits(t *testing.T) {
	t.Parallel()

	commit := strings.Repeat("a", 64)

	pin, err := ReadConsumerPin(map[string]string{"pr.yml": "uses: https://forge.example/owner/engine/.forgejo/actions/setup-forge-ci@" + commit + "\n"})
	if err != nil || pin.Commit != commit {
		t.Fatalf("full SHA-256 layer pin = %+v, %v", pin, err)
	}
}
