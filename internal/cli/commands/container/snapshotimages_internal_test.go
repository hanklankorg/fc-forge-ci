// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	appsnapshot "github.com/diggsweden/forge-ci/internal/app/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/snapshotimage"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestSnapshotPlanCLIEmitsRunnerOutputsAndPinsManualBranch(t *testing.T) {
	for _, runner := range []string{"github", "forgejo"} {
		t.Run(runner, func(t *testing.T) {
			env := testenv.New(t)
			t.Chdir(env.Temp)
			repo, err := git.PlainInit(env.Temp, false)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile("artifacts.yml", []byte("artifacts:\n  - name: server\n    project-type: go\ncontainers:\n  - name: app\n    from: [server]\n    platforms: linux/amd64,linux/arm64\n"), 0o600))
			tree, err := repo.Worktree()
			require.NoError(t, err)
			_, err = tree.Add("artifacts.yml")
			require.NoError(t, err)
			sha, err := tree.Commit("fixture", &git.CommitOptions{Author: &object.Signature{Name: "Fixture", Email: "fixture@example.test", When: time.Unix(1, 0)}})
			require.NoError(t, err)
			_, err = repo.CreateTag("v1.2.3", sha, nil)
			require.NoError(t, err)
			env.Setenv("GITHUB_ACTIONS", "true")
			if runner == "github" {
				env.Setenv("GITHUB_OUTPUT", env.Path("outputs"))
			} else {
				env.Setenv("FORGEJO_ACTIONS", "true")
				env.Setenv("FORGEJO_OUTPUT", env.Path("outputs"))
				gitBinary, err := exec.LookPath("git")
				require.NoError(t, err)
				require.NoError(t, os.Mkdir(env.Path("without-jq"), 0o700))
				require.NoError(t, os.Symlink(gitBinary, env.Path("without-jq/git")))
				env.Setenv("PATH", env.Path("without-jq"))
				_, err = exec.LookPath("jq")
				require.Error(t, err)
			}
			env.Setenv("GITHUB_REPOSITORY", "team/app")
			env.Setenv("GITHUB_SERVER_URL", "https://github.com")
			env.Setenv("GITHUB_REF_NAME", "event-main")
			err = New().Run(t.Context(), []string{"container", "snapshot-images", "plan", "--file", "artifacts.yml", "--branch", "manual-branch", "--source-sha", sha.String(), "--image-repository", "registry.example/team/app", "--output", "snapshot-plan.json"})
			require.NoError(t, err)
			p, err := appsnapshot.ReadPlan("snapshot-plan.json")
			require.NoError(t, err)
			require.Equal(t, "manual-branch", p.SourceBranch)
			require.Len(t, p.Builds, 2)
			body, err := os.ReadFile(env.Path("outputs"))
			require.NoError(t, err)
			require.Contains(t, string(body), "plan-json=")
			require.Contains(t, string(body), "builds=")
			require.Contains(t, string(body), "images=")
			require.Contains(t, string(body), "artifacts=")
			require.Contains(t, string(body), "source-sha="+sha.String()+"\n")
			require.Contains(t, string(body), "enabled=true\n")
			require.Contains(t, string(body), "has-artifacts=true\n")
			require.Contains(t, string(body), "has-arm64=true\n")
			require.Len(t, p.Artifacts, 1)
			var transferJSON string
			for _, line := range strings.Split(string(body), "\n") {
				if value, ok := strings.CutPrefix(line, "artifact-transfer-plan-json="); ok {
					transferJSON = value
				}
			}
			transfers, err := pipeline.ParseArtifactTransferPlan(transferJSON)
			require.NoError(t, err)
			require.Len(t, transfers.Items, 1)
			require.Equal(t, "server-go-build-artifacts", transfers.Items[0].Name)
			require.Equal(t, "_snapshot-downloads/server", transfers.Items[0].Path)
			require.True(t, transfers.Items[0].Required)
			for _, arch := range []string{"amd64", "arm64"} {
				require.NoError(t, os.MkdirAll("dist/linux-"+arch, 0o700))
				require.NoError(t, os.WriteFile("dist/linux-"+arch+"/server-linux-"+arch, []byte("producer artifact "+arch), 0o700))
				raw := filepath.Join(snapshotimage.DefaultDownloadDir, "server", "linux-"+arch)
				require.NoError(t, os.MkdirAll(raw, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(raw, "server-linux-"+arch), []byte("producer artifact "+arch), 0o700))
			}
			require.NoError(t, New().Run(t.Context(), []string{"container", "snapshot-images", "build-artifact", "--plan", "snapshot-plan.json", "--name", "server", "--from-artifact", "_snapshot-downloads/server", "--output-dir", "single-producer"}))
			require.NoError(t, New().Run(t.Context(), []string{"container", "snapshot-images", "prepare-inputs", "--plan", "snapshot-plan.json"}))
			require.NoError(t, New().Run(t.Context(), []string{"container", "snapshot-images", "gather-context", "--plan", "snapshot-plan.json", "--output", "automatic-bundle"}))
			require.FileExists(t, "automatic-bundle/files/snapshot-dependencies.json")
			require.NoError(t, New().Run(t.Context(), []string{"container", "snapshot-images", "context", "--plan", "snapshot-plan.json", "--output", "context.json"}))
			var inventory snapshotimage.DependencyInventory
			require.NoError(t, appsnapshot.ReadJSON("context.json", &inventory))
			require.Equal(t, sha.String(), inventory.SourceSHA)
			require.Len(t, inventory.Dependencies, 2)
			require.Equal(t, "server-go-build-artifacts", inventory.Dependencies[0].ArtifactName)
			require.Len(t, inventory.Dependencies[0].Files[0].SHA256, 64)
			bundle := snapshotCLIBundle(t, sha.String(), inventory)
			require.NoError(t, os.WriteFile("dist/linux-amd64/server-linux-amd64", []byte("substituted"), 0o700))
			err = New().Run(t.Context(), []string{"container", "snapshot-images", "build", "--plan", "snapshot-plan.json", "--name", "app", "--arch", runtime.GOARCH, "--metadata-dir", "metadata", "--dependencies-file", "context.json"})
			require.ErrorContains(t, err, "inventory")
			require.NoError(t, New().Run(t.Context(), []string{"container", "snapshot-images", "restore-context", "--plan", "snapshot-plan.json", "--input", bundle}))
			restored, err := os.ReadFile("dist/linux-amd64/server-linux-amd64")
			require.NoError(t, err)
			require.Equal(t, "producer artifact amd64", string(restored))
			outside := env.Path("outside-canary")
			require.NoError(t, os.WriteFile(outside, []byte("outside unchanged"), 0o600))
			require.NoError(t, os.Remove("dist/linux-amd64/server-linux-amd64"))
			require.NoError(t, os.Symlink(outside, "dist/linux-amd64/server-linux-amd64"))
			err = New().Run(t.Context(), []string{"container", "snapshot-images", "restore-context", "--plan", "snapshot-plan.json", "--input", bundle})
			require.Error(t, err)
			canary, err := os.ReadFile(outside)
			require.NoError(t, err)
			require.Equal(t, "outside unchanged", string(canary))
		})
	}
}

func snapshotCLIBundle(t *testing.T, sha string, inventory snapshotimage.DependencyInventory) string {
	t.Helper()
	bundle := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bundle, "files"), 0o700))
	plan, err := os.ReadFile("snapshot-plan.json")
	require.NoError(t, err)
	dependencies, err := os.ReadFile("context.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "snapshot-plan.json"), plan, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "files", snapshotimage.DefaultDependenciesFile), dependencies, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "source-sha"), []byte(sha), 0o600))
	for _, dep := range inventory.Dependencies {
		for _, file := range dep.Files {
			body, err := os.ReadFile(file.Path)
			require.NoError(t, err)
			target := filepath.Join(bundle, "files", file.Path)
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
			require.NoError(t, os.WriteFile(target, body, 0o700))
		}
	}
	manifest := fmt.Sprintf("%x  snapshot-plan.json\n%x  files/snapshot-dependencies.json\n", sha256.Sum256(plan), sha256.Sum256(dependencies))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "manifest.sha256"), []byte(manifest), 0o600))
	return bundle
}
