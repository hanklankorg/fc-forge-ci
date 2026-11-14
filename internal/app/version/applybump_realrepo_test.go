//go:build integration

// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	gitadapter "github.com/diggsweden/forge-ci/internal/adapters/git"
	appversion "github.com/diggsweden/forge-ci/internal/app/version"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/testutil/fakeoutputsink"
	"github.com/diggsweden/forge-ci/internal/testutil/isolatedgit"
)

// The patch here is made the way prepare-release.yml's key-free bump job makes
// it, with real git on both sides, so the parser and git apply agree on the
// format git actually writes.
func TestApplyBump_RealGitRoundTrip(t *testing.T) {
	repo := isolatedgit.NewRepo(t)
	root := strings.ReplaceAll(applyBumpPOM, "%s", "0.1.9")
	module := "<project>\n  <parent>\n    <groupId>se.digg</groupId>\n    <artifactId>app</artifactId>\n    <version>0.1.9</version>\n  </parent>\n\n  <artifactId>core</artifactId>\n</project>\n"

	repo.AddFile("pom.xml", root, "chore: root")
	repo.AddFile("core/pom.xml", module, "chore: module")

	// What `mvn versions:set -DnewVersion=0.2.0 -DprocessAllModules=true` does.
	for file, body := range map[string]string{"pom.xml": root, "core/pom.xml": module} {
		require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, file), []byte(strings.ReplaceAll(body, "0.1.9", "0.2.0")), 0o600))
	}

	patch := repo.Git("diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
		"--src-prefix=a/", "--dst-prefix=b/")
	repo.Git("checkout", "--", ".")

	patchPath := filepath.Join(t.TempDir(), "version-bump.patch")
	require.NoError(t, os.WriteFile(patchPath, []byte(patch+"\n"), 0o600))

	sum := sha256.Sum256([]byte(patch + "\n"))
	pathsFile := filepath.Join(t.TempDir(), "paths.txt")

	t.Chdir(repo.Dir)

	got, err := appversion.ApplyBump(context.Background(), gitadapter.New(), &bytes.Buffer{}, appversion.ApplyBumpInput{
		PatchPath:   patchPath,
		SHA256:      hex.EncodeToString(sum[:]),
		ProjectType: projecttype.Maven,
		Version:     "v0.2.0",
		WorkingDir:  ".",
		PathsFile:   pathsFile,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"core/pom.xml", "pom.xml"}, got.Paths)

	for file := range map[string]bool{"pom.xml": true, "core/pom.xml": true} {
		body, readErr := os.ReadFile(filepath.Join(repo.Dir, file))
		require.NoError(t, readErr)
		require.Contains(t, string(body), "<version>0.2.0</version>")
		require.NotContains(t, string(body), "0.1.9")
	}

	paths, err := os.ReadFile(pathsFile)
	require.NoError(t, err)
	require.Equal(t, "core/pom.xml\npom.xml\n", string(paths))
}

// The release commit's job copies the rendered changelog and commit-msg.txt
// into the checkout, then applies the version jobs' patch. What it commits is
// what bump-plan says it wrote, never a scan of the working tree: with real
// git, staging those paths beside CHANGELOG.md leaves commit-msg.txt out.
func TestBumpPlan_RealGitPathsFileIsTheReleaseCommit(t *testing.T) {
	repo := isolatedgit.NewRepo(t)
	pom := strings.ReplaceAll(applyBumpPOM, "%s", "0.1.9")

	repo.AddFile("CHANGELOG.md", "# Changes\n", "chore: changelog")
	repo.AddFile("services/backend/pom.xml", pom, "chore: backend")
	repo.AddFile("apps/tool/gradle.properties", "version=0.1.9\n", "chore: tool")

	// The key-free version job: the native tool, then the patch of its files.
	backend := filepath.Join(repo.Dir, "services/backend/pom.xml")
	require.NoError(t, os.WriteFile(backend, []byte(strings.ReplaceAll(pom, "0.1.9", "0.2.0")), 0o600))

	patch := repo.Git("diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
		"--src-prefix=a/", "--dst-prefix=b/", "--", "services/backend/pom.xml") + "\n"
	repo.Git("checkout", "--", ".")

	scratch := t.TempDir()
	patchPath, pathsFile := filepath.Join(scratch, "version-bump.patch"), filepath.Join(scratch, "bump-paths")
	require.NoError(t, os.WriteFile(patchPath, []byte(patch), 0o600))

	// The release commit's job: the rendered files laid into the checkout.
	for name, body := range map[string]string{"CHANGELOG.md": "# Changes\n\n## 0.2.0\n", "commit-msg.txt": "chore(release): 0.2.0\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(repo.Dir, name), []byte(body), 0o600))
	}

	plan, err := json.Marshal(pipeline.ReleasePrepareStagePlan{Version: pipeline.ReleasePlanVersion, Stage: "prepare",
		Targets: pipeline.ReleasePrepareTargets{VersionBump: pipeline.TargetPlan[pipeline.PlannedArtifact]{Runs: true, Items: []pipeline.PlannedArtifact{
			{Name: "backend", ProjectType: projecttype.Maven, WorkingDirectory: "services/backend"},
			{Name: "tool", ProjectType: projecttype.Gradle, WorkingDirectory: "apps/tool"},
		}}}})
	require.NoError(t, err)

	t.Chdir(repo.Dir)

	sum := sha256.Sum256([]byte(patch))
	require.NoError(t, appversion.BumpPlan(t.Context(), appversion.BumpOps{Git: gitadapter.New()}, fakeoutputsink.New(t), io.Discard, io.Discard,
		output.Annotator{}, appversion.BumpPlanInput{PlanJSON: string(plan), Version: "0.2.0", ChangelogFile: "CHANGELOG.md",
			PatchPath: patchPath, PatchSHA256: hex.EncodeToString(sum[:]), PathsFile: pathsFile}))

	body, err := os.ReadFile(pathsFile)
	require.NoError(t, err)

	paths := strings.Fields(string(body))
	require.Equal(t, []string{"services/backend/pom.xml", "services/backend/CHANGELOG.md", "apps/tool/CHANGELOG.md", "apps/tool/gradle.properties"}, paths)

	repo.Git(append([]string{"add", "--", "CHANGELOG.md"}, paths...)...)
	require.Equal(t, "?? commit-msg.txt", repo.Git("status", "--porcelain", "--untracked-files=all", "--", "commit-msg.txt"))
	require.ElementsMatch(t, append([]string{"CHANGELOG.md"}, paths...), strings.Fields(repo.Git("diff", "--cached", "--name-only")))
}
