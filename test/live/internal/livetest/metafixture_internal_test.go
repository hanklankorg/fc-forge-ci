// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The metadata-only demo ships scripts and configuration and builds nothing.
// Staged as the hosted trial stages it, every source file is the committed
// blob, and its release reads as the profile the oracle must judge: a
// changelog, no prepared commit, so the final tag names the pushed commit,
// and no SBOM bundle.
func TestMetaGitHubFixtureStagesItsSourceAndReadsAsAChangelogOnlyRelease(t *testing.T) {
	root, git := stageTestRepo(t, "sha1")
	demo := filepath.Join(reporoot.Path(t), "test/live/fixtures/consumers/hello-meta/consumer")

	err := filepath.WalkDir(demo, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(demo, path)
		if err != nil {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		stageTestWrite(t, root, rel, string(body), info.Mode().Perm())

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	stageTestCommit(t, git, root)

	trial := GitHubTrial{tb: t}
	dir, _ := trial.stageConsumer(root, "test/live/github", trialEngine, nil)

	for _, path := range []string{"install.sh", "lib/greeting.sh", ".config/hello/defaults.conf", ".forge-ci/artifacts.yml", "README.md"} {
		want := mustLayerGit(t, git, root, "rev-parse", "HEAD:"+path)
		if got := mustLayerGit(t, git, dir, "rev-parse", "HEAD:"+path); got != want {
			t.Fatalf("staging changed source blob %s", path)
		}
	}

	for _, name := range []string{"release.yml", "pullrequest.yml"} {
		body := mustLayerGit(t, git, dir, "show", "HEAD:.github/workflows/"+name)
		if !strings.Contains(body, trialEngine.Owner+"/"+trialEngine.Repository+"/.github/workflows/") || !strings.Contains(body, "@"+trialEngine.Commit) {
			t.Fatalf("%s does not call the engine under test", name)
		}
	}

	want := githubReleaseProfile{GitSigning: config.GitSignGPG, Changelog: true, Prepares: false, SBOMBundle: sbomBundleWithheld, NoFiles: true}
	if got, err := githubReleaseProfileOf(dir, "release.yml"); err != nil || got != want {
		t.Fatalf("profile = %+v, %v; want %+v", got, err, want)
	}
}
