// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

func TestConsumerDraftLayerBindsPolicyAndGateOnlyInPrivateIndex(t *testing.T) {
	t.Parallel()

	for _, scenario := range []string{"draft", "published", "mixed", "no native workflows", "documentation only"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			git, root := newConsumerCheckout(t, consumerSHA256)
			pin := consumerPinned
			if scenario == "draft" || scenario == "mixed" {
				pin = "PIN_FORGE_CI_SHA"
			}
			workflow := "jobs:\n  check:\n    steps:\n      - uses: https://forge.example/owner/forge-ci/.forgejo/actions/setup-forge-ci@" + pin + "\n"
			if scenario == "published" {
				workflow += "# A draft uses the suffix @PIN_FORGE_CI_SHA.\n"
			}
			writeLayerFile(t, root, consumerWorkflows+"/ci.yml", workflow)
			writeLayerFile(t, root, "policy/pin.sh", "pin="+pin+"\n")
			gate := "include:\n  - component: $CI_SERVER_FQDN/example/engine/check@" + pin + "\n" +
				"script:\n  - forge-ci validate isolation --single-pin-subject owner/forge-ci\n" +
				"  - forge-ci validate isolation --single-pin-subject unrelated/engine\n" +
				"# Upstream: owner/forge-ci\n"
			writeLayerFile(t, root, GitLabGateFile, gate)
			if scenario == "mixed" {
				writeLayerFile(t, root, consumerWorkflows+"/other.yml", strings.ReplaceAll(workflow, pin, consumerPinned))
			}
			if scenario == "no native workflows" || scenario == "documentation only" {
				mustLayerGit(t, git, root, "rm", "-f", consumerWorkflows+"/ci.yml")
			}
			if scenario == "documentation only" {
				writeLayerFile(t, root, consumerWorkflows+"/README.md", "GitLab is the only CI provider.\n")
			}
			mustLayerGit(t, git, root, "add", "--all")
			mustLayerGit(t, git, root, "commit", "--quiet", "--message", "test: stage provider draft")

			consumer, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			layer := sampleLayer()
			bound, err := consumer.bindDraftLayer(t.Context(), layer)
			if scenario == "mixed" {
				if !errors.Is(err, errs.ErrValidation) || bound.Commit != consumer.Commit {
					t.Fatalf("mixed draft was not refused without rebinding: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "draft" {
				if bound.Commit != consumer.Commit {
					t.Fatal("non-draft consumer was rewritten")
				}
				return
			}

			for _, path := range []string{consumerWorkflows + "/ci.yml", "policy/pin.sh", GitLabGateFile} {
				staged := mustLayerGit(t, git, bound.stage, "show", bound.Commit+":"+path)
				if strings.Contains(staged, pin) || !strings.Contains(staged, layer.Commit) {
					t.Fatalf("draft not bound in %s: %s", path, staged)
				}
				original := mustLayerGit(t, git, root, "show", "HEAD:"+path)
				if !strings.Contains(original, pin) {
					t.Fatalf("original source changed at %s", path)
				}
				if path == GitLabGateFile {
					want := strings.ReplaceAll(gate, pin, layer.Commit)
					want = strings.Replace(want, "--single-pin-subject owner/forge-ci", "--single-pin-subject "+layer.Repository, 1)
					if staged != strings.TrimSpace(want) || original != strings.TrimSpace(gate) {
						t.Fatalf("gate subject binding or source preservation failed: staged=%q source=%q", staged, original)
					}
				}
			}
			if status := mustLayerGit(t, git, root, "status", "--porcelain"); status != "" {
				t.Fatalf("source checkout changed: %s", status)
			}
		})
	}
}
