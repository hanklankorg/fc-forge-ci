// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/diggsweden/forge-ci/internal/domain/provenance"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/google/go-github/v91/github"
)

// Run the real candidate release-binary workflow, not an echo/upload stub. Only
// its trigger setup is synthetic: a disposable repository, matching final/request
// tags and an initially empty release. No runtime images or production releases
// are touched. The separate scheduling witness tests the outer self-release DAG.
func TestGitHubSelfRelease_BinaryAssets(t *testing.T) {
	t.Parallel()
	RegisterLiveScenario(t)

	if os.Getenv(GitHubContextEnv) == "" {
		t.Skip("run test-live-github-self-release inside a guarded Forge Lab session")
	}

	id, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_ID"), 10, 64)
	if err != nil || id <= 0 {
		t.Fatal("an explicit positive GitHub organization ID is required")
	}

	trial := OpenGitHubTrial(t, id)
	repo := minimalRepository(t, trial, "self-release-binary-assets")
	source := trial.tempDir("self-release-binary-assets")

	t.Logf("candidate source: %s", strings.TrimSpace(trial.git(reporoot.Path(t), "rev-parse", "HEAD")))
	trial.git(reporoot.Path(t), "clone", "--quiet", "--local", "--no-hardlinks", reporoot.Path(t), source)
	// The snapshot is test-owned. Keep only the actual binary workflow so pushing
	// fixture tags cannot start runtime builds or the real self-release wrapper.
	workflows := filepath.Join(source, ".github/workflows")

	entries, err := os.ReadDir(workflows)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() != "release-binary.yml" {
			if err := os.Remove(filepath.Join(workflows, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}

	trial.git(source, "add", ".github/workflows")
	trial.commit(source, "test: isolate real CLI binary publication workflow", "")
	commit := strings.TrimSpace(trial.git(source, "rev-parse", "HEAD"))
	t.Logf("binary-publication fixture snapshot: %s", commit)

	const tag, request = "v0.0.1", "release-request/v0.0.1"
	trial.git(source, "-c", "tag.gpgsign=false", "tag", "--force", tag, commit)
	trial.git(source, "-c", "tag.gpgsign=false", "tag", "--force", request, commit)
	t.Cleanup(func() { settleBootstrapRuns(t, trial, repo, commit, "release-binary.yml", "workflow_dispatch") })
	trial.push(source, trial.remote(repo), "HEAD:refs/heads/main", "refs/tags/"+tag, "refs/tags/"+request)
	trial.waitWorkflows(repo, workflows)

	_, _, err = trial.api.Repositories.CreateRelease(t.Context(), trial.org, repo, github.CreateReleaseRequest{
		TagName: tag, TargetCommitish: new(commit), Name: new("Disposable real CLI binary acceptance"), MakeLatest: new("false"),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = trial.api.Actions.CreateWorkflowDispatchEventByFileName(t.Context(), trial.org, repo, "release-binary.yml", github.CreateWorkflowDispatchEventRequest{
		Ref: request, Inputs: map[string]any{"release-ref": request}, ReturnRunDetails: new(true),
	})
	if err != nil {
		t.Fatalf("dispatch real binary publication once: %v", err)
	}

	run := trial.WaitRun(repo, "release-binary.yml", "workflow_dispatch", commit, 50*time.Minute)
	trial.ReportRun(repo, run)

	if run.GetConclusion() != "success" {
		t.Fatalf("real CLI binary publication ended %s: %s", run.GetConclusion(), run.GetHTMLURL())
	}

	selfReleaseVerifyUploadedAssets(t, trial, repo, tag, request)
}

func selfReleaseVerifyUploadedAssets(t *testing.T, trial *GitHubTrial, repo, tag, request string) {
	t.Helper()

	release, _, err := trial.api.Repositories.GetReleaseByTag(t.Context(), trial.org, repo, tag)
	if err != nil {
		t.Fatal(err)
	}

	assets, err := selfReleaseAssetInventory(release.Assets, strings.TrimPrefix(tag, "v"))
	if err != nil {
		t.Fatal(err)
	}

	trust := CosignTrust{identity: "^https://github\\.com/" + regexp.QuoteMeta(trial.org+"/"+repo) + "/\\.github/workflows/release-binary\\.yml@refs/tags/" + regexp.QuoteMeta(request) + "$"}
	if !trial.verifyBundles(repo, assets, trust)["checksums.txt"] {
		t.Fatal("the uploaded checksum manifest has no verified signature")
	}

	dir := trial.tempDir("self-release-assets")
	for _, name := range []string{"checksums.txt", "forge-ci.intoto.json", "forge-ci.intoto.jsonl"} {
		trial.download(repo, assets[name], filepath.Join(dir, name))
	}

	trial.run(dir, nil, HostTool(t, "cosign"), append(append([]string{"verify-blob", "--bundle", "forge-ci.intoto.jsonl"}, trust.verifyArgs()...), "forge-ci.intoto.json")...)

	manifest, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}

	subjects, err := provenance.ParseChecksums(strings.NewReader(string(manifest)))
	if err != nil || len(subjects) != 6 {
		t.Fatalf("signed manifest must cover three archives and three SBOMs: %v", err)
	}

	seen := map[string]bool{}
	for _, subject := range subjects {
		if seen[subject.Name] || assets[subject.Name] == 0 || !strings.HasPrefix(subject.Name, "forge-ci_") || filepath.Base(subject.Name) != subject.Name {
			t.Fatalf("unexpected or duplicate manifest subject %q", subject.Name)
		}

		seen[subject.Name] = true
		path := filepath.Join(dir, subject.Name)
		trial.download(repo, assets[subject.Name], path)

		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}

		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()

		if copyErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != subject.SHA256 {
			t.Fatalf("uploaded asset %s does not match its signed checksum", subject.Name)
		}
	}

	t.Logf("real self-release binary assets verified: %d uploaded files, 6 payload checksums, checksum and provenance signatures", len(assets))
}

func selfReleaseAssetInventory(assets []*github.ReleaseAsset, version string) (map[string]int64, error) {
	want := map[string]bool{"checksums.txt": true, "checksums.txt.bundle": true, "forge-ci.intoto.json": true, "forge-ci.intoto.jsonl": true}

	for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_arm64"} {
		name := "forge-ci_" + version + "_" + target + ".tar.gz"
		want[name], want[name+".cdx.sbom.json"] = true, true
	}

	if len(assets) != len(want) {
		return nil, fmt.Errorf("self-release requires exactly %d uploaded assets, got %d (GitHub-generated source archives do not count)", len(want), len(assets))
	}

	result := map[string]int64{}

	for _, asset := range assets {
		name := asset.GetName()
		if !want[name] || result[name] != 0 || asset.GetID() <= 0 || asset.GetSize() <= 0 {
			return nil, fmt.Errorf("unexpected, duplicate or empty uploaded self-release asset %q", name)
		}

		result[name] = asset.GetID()
	}

	return result, nil
}

func TestSelfReleaseAssetInventory(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"complete", "empty", "missing", "duplicate", "unexpected", "zero-size", "zero-id"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			names := make([]string, 0, 10)
			names = append(names, "checksums.txt", "checksums.txt.bundle", "forge-ci.intoto.json", "forge-ci.intoto.jsonl")

			for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_arm64"} {
				archive := "forge-ci_0.0.1_" + target + ".tar.gz"
				names = append(names, archive, archive+".cdx.sbom.json")
			}

			assets := make([]*github.ReleaseAsset, 0, len(names))

			for i, assetName := range names {
				assets = append(assets, &github.ReleaseAsset{Name: new(assetName), ID: new(int64(i + 1)), Size: new(1)})
			}

			switch name {
			case "empty":
				assets = nil
			case "missing":
				assets = assets[1:]
			case "duplicate":
				assets[0] = assets[1]
			case "unexpected":
				assets[0].Name = new("source-code.zip")
			case "zero-size":
				assets[0].Size = new(0)
			case "zero-id":
				assets[0].ID = new(int64(0))
			case "complete":
			}

			_, err := selfReleaseAssetInventory(assets, "0.0.1")
			if (err == nil) != (name == "complete") {
				t.Fatalf("inventory %s: %v", name, err)
			}
		})
	}
}
