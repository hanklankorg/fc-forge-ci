// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diggsweden/forge-ci/internal/livetest"
)

// A small repository/checkout contract, independent of any consumer release
// build: the server and client must retain the selected Git hash algorithm.
func TestRepository_ObjectFormatRoundTrip(t *testing.T) {
	for _, forge := range forgesClaiming(t, alwaysValidatesTokens, "repository metadata") {
		t.Run(string(forge), func(t *testing.T) {
			target := livetest.Accept(t, forge)
			for _, visibility := range livetest.ConsumerVisibilities(t) {
				t.Run(visibility.Name, func(t *testing.T) {
					for _, format := range []string{"sha1", "sha256"} {
						t.Run(format, func(t *testing.T) { checkObjectFormatRoundTrip(t, target, format, visibility.Private) })
					}
				})
			}
		})
	}
}

func checkObjectFormatRoundTrip(t *testing.T, target livetest.Target, format string, private bool) {
	t.Helper()
	repo := livetest.NewBareScratchRepo(t, target, "object-format-"+format, format, private)
	facts := livetest.SeedObjectFormatFixture(t, target, repo, format, t.TempDir())
	workspace := filepath.Join(t.TempDir(), "checkout")
	args := []string{
		"platform", "checkout",
		"--repository", livetest.RepoSlug(target, repo), "--server-url", target.BaseURL(),
		"--ref", facts.Tag, "--fetch-tags", "--workspace", workspace,
	}
	// A private clone names its credential. GitLab's ambient GITLAB_TOKEN is an
	// API token only, so Git would otherwise ask anonymously; the file is the
	// route both forges document.
	if private {
		args = append(args, "--token-file", writeToken(t, target.Token))
	}

	result := livetest.CLI(t, target, repo, args...)
	if result.ExitCode != 0 {
		t.Fatalf("%s checkout failed: %s", format, result.Stderr)
	}

	if got := git(t, workspace, "rev-parse", "--show-object-format"); got != format {
		t.Fatalf("checkout object format %q, expected %q", got, format)
	}

	if got := git(t, workspace, "rev-parse", "HEAD"); got != facts.TagCommit || got == facts.HeadCommit {
		t.Fatalf("checkout selected %s instead of exact tag commit %s", got, facts.TagCommit)
	}

	if got := git(t, workspace, "rev-parse", "refs/tags/"+facts.Tag); got != facts.TagObject {
		t.Fatalf("annotated tag object changed: %s, expected %s", got, facts.TagObject)
	}

	if body, err := os.ReadFile(filepath.Join(workspace, "README.md")); err != nil || string(body) != "Object-format acceptance.\n" {
		t.Fatalf("checkout bytes changed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workspace, afterTagFile)); !os.IsNotExist(err) {
		t.Fatalf("checkout did not stop at the requested tag: %v", err)
	}

	t.Logf("%s push/fetch verified: tagged commit=%s annotated tag=%s later main=%s", format, facts.TagCommit, facts.TagObject, facts.HeadCommit)
}
