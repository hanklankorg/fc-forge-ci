// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"strings"
	"testing"
)

func TestGitLabGateImportPreservesSourceAndUsesAdmittedBlob(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root, git := stageTestRepo(t, format)
			body := "workflow:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"merge_request_event\"'\n"
			stageTestWrite(t, root, "examples/gitlab/gate.yml", body, 0644)
			stageTestWrite(t, root, "application.txt", "original\n", 0644)
			caller := "jobs:\n  sign:\n    uses: fixtures/engine/.forgejo/workflows/sign-and-publish-release.yml@" + strings.Repeat("a", 40) + "\n    with:\n      publication-image-repository: '${{ vars.IMAGE_DESTINATION }}' # keep authority\n"
			stageTestWrite(t, root, ".forgejo/workflows/release.yml", caller, 0644)
			stageTestCommit(t, git, root)
			consumer, err := openConsumer(t.Context(), git, root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			fixture := consumer.WithGitLabGateFromSource(t, "examples/gitlab/gate.yml")
			got, err := git.run(t.Context(), fixture.stage, "show", fixture.Commit+":"+GitLabGateFile)
			if err != nil || got != body[:len(body)-1] {
				t.Fatalf("gate bytes changed: %q %v", got, err)
			}
			got, err = git.run(t.Context(), fixture.stage, "show", fixture.Commit+":application.txt")
			if err != nil || got != "original" {
				t.Fatal("application source changed")
			}
			got, err = git.run(t.Context(), fixture.stage, "show", fixture.Commit+":.forgejo/workflows/release.yml")
			if err != nil || !strings.Contains(got, "image-repository: '${{ vars.IMAGE_DESTINATION }}' # keep authority") || strings.Contains(got, "publication-image-repository:") {
				t.Fatal("scratch caller destination migration changed authority or was not applied")
			}
			if err := git.requireClean(t.Context(), root, "."); err != nil {
				t.Fatal("original checkout modified")
			}
		})
	}
}
