// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package livetest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/google/go-github/v91/github"

	githubadapter "github.com/diggsweden/forge-ci/internal/adapters/github"
	domaincontainer "github.com/diggsweden/forge-ci/internal/domain/container"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const minimalNodeImage = "ghcr.io/itiquette/forge-ci-runtime-node-24@sha256:e90d3dcfdbe048329e0eb23cad813b95722fcb077b2261038da34bbca91e6d91"
const minimalBotEmail = "forge-lab-minimal@hosted.invalid"

// Current working-tree source is copied only into new test repositories. This
// route deliberately does not call EnsureEngine or publish any runtime image.
func TestGitHubMinimalPreparation(t *testing.T) {
	if os.Getenv(GitHubContextEnv) == "" || os.Getenv("FORGE_CI_LIVE_BIN") == "" || os.Getenv("FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE") == "" {
		t.Skip("select test-live-github-minimal inside Forge Lab's hosted session")
	}

	id, err := strconv.ParseInt(os.Getenv("FORGE_CI_LIVE_GITHUB_ORG_ID"), 10, 64)
	if err != nil || id <= 0 {
		t.Fatal("explicit hosted organization ID is required")
	}

	trial := OpenGitHubTrial(t, id)
	t.Run("retention-read-only", func(t *testing.T) { minimalRetentionPreview(t, OpenGitHubTrial(t, id)) })
	sourceRepo := minimalRepository(t, trial, "minimal-engine")
	source := trial.tempDir("minimal-engine")
	minimalEngineSource(t, trial, reporoot.Path(t), source)
	trial.git(source, "init", "--quiet", "--object-format=sha1", "--initial-branch=main")
	trial.git(source, "add", "--all")
	trial.commit(source, "test: snapshot minimal hosted preparation engine", "")
	commit := strings.TrimSpace(trial.git(source, "rev-parse", "HEAD"))
	trial.push(source, trial.remote(sourceRepo), "HEAD:refs/heads/main")
	minimalCurrentBinary(t, trial, sourceRepo, source, commit)
	commit = strings.TrimSpace(trial.git(source, "rev-parse", "HEAD"))
	t.Logf("temporary engine source: %s/%s at %s", trial.org, sourceRepo, commit)

	for _, multi := range []bool{false, true} {
		name := "standalone"
		if multi {
			name = "multi-artifact"
		}

		t.Run(name, func(t *testing.T) {
			minimalPreparation(t, OpenGitHubTrial(t, id), trial.org+"/"+sourceRepo, commit, multi)
		})
	}
}

func minimalCurrentBinary(t *testing.T, trial *GitHubTrial, repo, source, sourceCommit string) {
	t.Helper()

	file, err := os.Open(os.Getenv("FORGE_CI_LIVE_BIN"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = file.Close() }()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	digest := hex.EncodeToString(hash.Sum(nil))

	release, _, err := trial.api.Repositories.CreateRelease(t.Context(), trial.org, repo, sdk.CreateReleaseRequest{
		TagName: "minimal-engine", TargetCommitish: new(sourceCommit), Name: new("Disposable fixture CLI"), Prerelease: new(true), MakeLatest: new("false"),
	})
	if err != nil {
		t.Fatal(err)
	}

	asset, _, err := trial.api.Repositories.UploadReleaseAsset(t.Context(), trial.org, repo, release.GetID(), &sdk.UploadOptions{Name: "forge-ci-linux-amd64"}, file)
	if err != nil {
		t.Fatal(err)
	}
	// Existing images supply tools; this fixture-only bootstrap pins the current
	// CLI without rebuilding any image or changing the real workflow inputs.
	bootstrap := strings.NewReplacer("DOWNLOAD_URL", asset.GetBrowserDownloadURL(), "BINARY_SHA", digest).Replace(`    steps:
      - name: Install checksum-pinned current fixture CLI
        shell: bash
        run: |
          set -euo pipefail
          directory="${RUNNER_TEMP:?}/minimal-current-engine"
          mkdir -p "${directory}"
          curl --proto '=https' --fail --silent --show-error --location --retry 3 'DOWNLOAD_URL' --output "${directory}/forge-ci"
          printf 'BINARY_SHA  %s\n' "${directory}/forge-ci" | sha256sum --check -
          chmod 0700 "${directory}/forge-ci"
          printf '%s\n' "${directory}" >>"${GITHUB_PATH:?}"
`)

	for _, name := range []string{"version-bump.yml", "release-prepare-stage.yml", "generate-changelog.yml"} {
		path := filepath.Join(source, ".github/workflows", name)

		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		minimalWrite(t, source, ".github/workflows/"+name, strings.ReplaceAll(string(body), "    steps:\n", bootstrap))
	}

	trial.git(source, "add", ".github/workflows")
	trial.commit(source, "test: pin current CLI beside published fixture toolchains", "")
	trial.push(source, trial.remote(repo), "HEAD:refs/heads/main")
	t.Logf("fixture CLI sha256=%s, source=%s; no runtime images published", digest, sourceCommit)
}

func minimalEngineSource(t *testing.T, trial *GitHubTrial, root, destination string) {
	t.Helper()

	paths := trial.git(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--",
		"cmd", "internal", "scripts/bootstrap", "go.mod", "go.sum", "LICENSE", "LICENSES", "REUSE.toml")
	for relative := range strings.SplitSeq(paths, "\x00") {
		if relative == "" || strings.HasSuffix(relative, "_test.go") || strings.Contains(relative, "/testdata/") {
			continue
		}

		if strings.HasPrefix(relative, "internal/") && !slices.Contains([]string{".go", ".tmpl", ".sh"}, filepath.Ext(relative)) {
			continue
		}

		minimalCopy(t, filepath.Join(root, relative), filepath.Join(destination, relative))
	}

	for _, name := range minimalPreparationWorkflows {
		minimalCopy(t, filepath.Join(root, ".github/workflows", name), filepath.Join(destination, ".github/workflows", name))
	}
}

func minimalCopy(t *testing.T, source, destination string) {
	t.Helper()

	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("test snapshot source is not regular: %s", source)
	}

	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(destination, body, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}

func minimalWrite(t *testing.T, root, name, body string) {
	t.Helper()

	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func minimalRepository(t *testing.T, trial *GitHubTrial, name string) string {
	t.Helper()

	var id int64
	// Register before creation's deletion hook so this check runs afterwards.
	t.Cleanup(func() {
		if id == 0 {
			return
		}

		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
		defer cancel()

		_, response, err := trial.api.Repositories.GetByID(ctx, id)
		if err == nil || response == nil || response.StatusCode != http.StatusNotFound {
			t.Errorf("temporary repository %d deletion was not confirmed", id)
		} else {
			t.Logf("temporary repository %d deletion confirmed", id)
		}
	})

	repo := trial.NewConsumerRepository(name, false)

	created, _, err := trial.api.Repositories.Get(t.Context(), trial.org, repo)
	if err != nil {
		t.Fatal(err)
	}

	id = created.GetID()
	t.Logf("temporary repository: %s/%s id=%d", trial.org, repo, id)

	return repo
}

func minimalPreparation(t *testing.T, trial *GitHubTrial, engine, commit string, multi bool) {
	t.Helper()

	name, leaf := "minimal-single", "version-bump.yml"
	if multi {
		name, leaf = "minimal-multi", "release-prepare-stage.yml"
	}

	repo := minimalRepository(t, trial, name)
	trial.SetVariable(repo, "FORGE_CI_REPOSITORY", engine)
	dir := trial.tempDir(name)
	keyDir := trial.tempDir(name + "-ssh")
	private, public := SSHKey(t, keyDir)
	trial.SetSecret(repo, "RELEASE_SSH_SIGNING_KEY", private)
	trial.SetSecret(repo, "RELEASE_TOKEN", trial.Token())
	trial.DeleteSecretsAfter(repo, []string{"RELEASE_SSH_SIGNING_KEY", "RELEASE_TOKEN"})

	manifest := map[string]any{"name": "minimal-preparation", "version": "0.0.1", "private": true,
		"scripts": map[string]string{"preversion": `node -e 'for (const n of ["SSH_SIGNING_KEY","GPG_PRIVATE_KEY","RELEASE_TOKEN","RELEASE_SSH_SIGNING_KEY"]) if (process.env[n]) process.exit(91); console.log("NATIVE_KEYFREE_PROBE_OK")'`}}

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	minimalWrite(t, dir, "package.json", string(body)+"\n")
	minimalWrite(t, dir, "CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- Minimal preparation fixture.\n")
	minimalWrite(t, dir, "allowed_signers", minimalBotEmail+" "+public+"\n")

	with := "      project-type: npm\n"

	if multi {
		minimalWrite(t, dir, "second/gradle.properties", "version=0.0.1\n")

		plan := pipeline.ReleasePrepareStagePlan{Version: pipeline.ReleasePlanVersion, Stage: "prepare", Targets: pipeline.ReleasePrepareTargets{
			VersionBump: pipeline.TargetPlan[pipeline.PlannedArtifact]{Runs: true, Items: []pipeline.PlannedArtifact{
				{Name: "native", ProjectType: projecttype.NPM, WorkingDirectory: "."},
				{Name: "text", ProjectType: projecttype.Gradle, WorkingDirectory: "second"},
			}}}}

		planJSON, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}

		with = "      authorized-source-sha: ${{ github.sha }}\n      forge-ci-binary-ref: " + commit + "\n      prepare-stage-plan-json: '" + string(planJSON) + "'\n"
	}

	workflow := `name: Minimal version preparation
on:
  push:
    tags: ['release-request/v*']
permissions:
  contents: read
jobs:
  prepare:
    uses: ENGINE/.github/workflows/LEAF@COMMIT
    permissions:
      contents: write
      actions: read
    with:
      branch: main
      runtime-image: IMAGE
      git-signing-method: ssh
      bot-name: Forge Lab Minimal
      bot-email: EMAIL
WITH    secrets:
      RELEASE_SSH_SIGNING_KEY: ${{ secrets.RELEASE_SSH_SIGNING_KEY }}
      RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}
`
	workflow = strings.NewReplacer("ENGINE", engine, "LEAF", leaf, "COMMIT", commit, "IMAGE", minimalNodeImage, "EMAIL", minimalBotEmail, "WITH", with).Replace(workflow)
	minimalWrite(t, dir, ".github/workflows/release.yml", workflow)
	trial.git(dir, "init", "--quiet", "--object-format=sha1", "--initial-branch=main")
	trial.git(dir, "add", "--all")
	trial.commit(dir, "feat: add minimal version preparation fixture", "")
	initial := strings.TrimSpace(trial.git(dir, "rev-parse", "HEAD"))
	trial.push(dir, trial.remote(repo), "HEAD:refs/heads/main")
	trial.git(dir, "-c", "user.name=Forge Lab Minimal", "-c", "user.email="+minimalBotEmail,
		"-c", "gpg.format=ssh", "-c", "user.signingkey="+filepath.Join(keyDir, sshKeyFile), "tag", "-s", "release-request/v0.0.2", "-m", "Minimal release v0.0.2", initial)
	trial.push(dir, trial.remote(repo), "refs/tags/release-request/v0.0.2")
	run := trial.WaitRun(repo, "release.yml", "push", initial, 25*time.Minute)
	t.Logf("hosted scenario: %s", run.GetHTMLURL())
	trial.ReportRun(repo, run)

	if run.GetConclusion() != "success" {
		t.Fatalf("minimal preparation ended %s", run.GetConclusion())
	}

	trial.git(dir, "fetch", "--quiet", trial.remote(repo), "+refs/heads/main:refs/remotes/checked/main", "+refs/tags/*:refs/tags/*")

	head := strings.TrimSpace(trial.git(dir, "rev-parse", "refs/remotes/checked/main"))
	if parent := strings.TrimSpace(trial.git(dir, "rev-parse", head+"^")); parent != initial {
		t.Fatal("preparation did not create exactly one child commit")
	}

	trial.git(dir, "-c", "gpg.ssh.allowedSignersFile="+filepath.Join(dir, "allowed_signers"), "verify-commit", head)

	var released struct {
		Version string `json:"version"`
	}
	if json.Unmarshal([]byte(trial.git(dir, "show", head+":package.json")), &released) != nil || released.Version != "0.0.2" {
		t.Fatal("native version was not released")
	}

	if multi {
		if !strings.Contains(trial.git(dir, "show", head+":second/gradle.properties"), "version=0.0.2") {
			t.Fatal("engine-owned text version was not released")
		}

		if final := strings.TrimSpace(trial.git(dir, "rev-parse", "v0.0.2^{}")); final != head {
			t.Fatal("final tag is not the prepared commit")
		}

		trial.git(dir, "-c", "gpg.ssh.allowedSignersFile="+filepath.Join(dir, "allowed_signers"), "verify-tag", "v0.0.2")
	}

	t.Logf("verified one signed release commit %s; native and text versions checked", head)
}

func minimalRetentionPreview(t *testing.T, trial *GitHubTrial) {
	t.Helper()

	file := os.Getenv("FORGE_CI_LIVE_EXTERNAL_REGISTRY_TOKEN_FILE")
	if file == "" {
		t.Fatal("explicit Packages token file is required for the read-only preview")
	}

	token, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(token)

	packages, err := githubadapter.NewPackages(nil, strings.TrimSpace(string(token)))
	if err != nil {
		t.Fatal(err)
	}

	names, err := packages.ContainerPackages(t.Context(), trial.org, "fci-prepublication")
	if err != nil || len(names) == 0 {
		t.Fatalf("read existing test packages: %v", err)
	}

	name := names[0]

	versions, err := packages.PackageVersions(t.Context(), trial.org, name)
	if err != nil {
		t.Fatal(err)
	}

	slices.SortFunc(versions, func(a, b domaincontainer.PackageVersion) int { return a.Created.Compare(b.Created) })

	pin := ""

	for _, version := range versions {
		for _, tag := range version.Tags {
			if !strings.HasPrefix(tag, "sha256-") {
				pin = version.Digest

				break
			}
		}

		if pin != "" {
			break
		}
	}

	if pin == "" {
		t.Fatal("existing test package has no tagged image to protect")
	}

	actor, _, err := trial.api.Users.Get(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}

	args := []string{"container", "package-retention", "--repository", "ghcr.io/" + trial.org + "/" + name, "--keep-digest", pin,
		"--recent-count", "1", "--max-delete", "1000", "--registry-username", actor.GetLogin(), "--dry-run=true"}

	output, err := trial.outputEnv(t.TempDir(), []string{"GH_TOKEN=" + strings.TrimSpace(string(token))}, os.Getenv("FORGE_CI_LIVE_BIN"), args...)
	if err != nil {
		t.Fatalf("retention preview failed: %v\n%s", err, output)
	}

	if !strings.Contains(output, "consumer-pinned digest") || !strings.Contains(output, pin) {
		t.Fatal("preview did not preserve the explicit consumer pin")
	}

	missing := slices.Clone(args)
	missing[5] = "sha256:" + strings.Repeat("f", 64)

	refusal, err := trial.outputEnv(t.TempDir(), []string{"GH_TOKEN=" + strings.TrimSpace(string(token))}, os.Getenv("FORGE_CI_LIVE_BIN"), missing...)
	if err == nil || !strings.Contains(refusal, "absent from the complete package inventory") {
		t.Fatal("an absent protected pin was not refused")
	}

	after, err := packages.PackageVersions(t.Context(), trial.org, name)
	if err != nil {
		t.Fatal(err)
	}

	beforeIDs, afterIDs := map[int64]string{}, map[int64]string{}
	for _, version := range versions {
		beforeIDs[version.ID] = version.Digest
	}

	for _, version := range after {
		afterIDs[version.ID] = version.Digest
	}

	if !maps.Equal(beforeIDs, afterIDs) {
		t.Fatal("read-only preview changed the package inventory")
	}

	t.Logf("read-only retention preview: ghcr.io/%s/%s; protected %s; %d versions unchanged", trial.org, name, pin, len(after))
}
