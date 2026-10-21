// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/internal/livetest"
)

// The consumer's root config admits development pushes and includes the rendered
// gate. Unlike ConsumerTrial this never selects the MR config or opens an MR.
func TestGitLabLayer_ConsumerDevelopment(t *testing.T) {
	root := os.Getenv(consumerCheckoutEnv)
	if root == "" {
		t.Skip("name a clean development consumer with FORGE_CI_LIVE_CONSUMER and its FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE")
	}
	if livetest.DemoName() != "" {
		t.Fatal("the development trial requires disposable scratch repositories")
	}
	for _, visibility := range livetest.ConsumerVisibilities(t) {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabConsumerDevelopment(t, root, visibility.Private)
		})
	}
}

type developmentAcceptance struct {
	jobName, smokeMarker, imageVariable, artifactPrefix string
	maxArchive                                          int64
	checkArchive                                        func(*testing.T, []byte, string)
}

func packageDevelopmentAcceptance() developmentAcceptance {
	return developmentAcceptance{
		jobName: "Check: development package", smokeMarker: "development package smoke passed at ",
		imageVariable: "CI_NODE_JOB_IMAGE", artifactPrefix: "fc-api-dev-",
		maxArchive: 32 << 20, checkArchive: requireDevelopmentArchive,
	}
}

func runGitLabConsumerDevelopment(t *testing.T, root string, private bool) {
	t.Helper()
	acceptance := packageDevelopmentAcceptance()
	acceptance.maxArchive = maxReleaseFile
	runGitLabDevelopment(t, root, private, acceptance)
}

func runGitLabDevelopment(t *testing.T, root string, private bool, acceptance developmentAcceptance) {
	t.Helper()
	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "the development consumer needs a GitLab runner")
	}
	target := livetest.Accept(t, provider.ForgeGitLab)
	consumer := livetest.OpenConsumer(t, target, root)
	if !consumer.HasGitLabGate(t) {
		t.Fatal("the development consumer must include .gitlab/merge-request.yml from its branch .gitlab-ci.yml")
	}
	engine := livetest.NewScratchRepo(t, target, "gitlab-development-layer")
	layer := livetest.PushLayer(t, target, engine)
	images := livetest.GitLabGateImages{
		Job: livetest.PushJobImage(t, target, engine, os.Getenv("FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE")),
	}
	if archive := os.Getenv("FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE"); archive != "" {
		images.Lint = livetest.PushJobImage(t, target, livetest.NewScratchRepo(t, target, "gitlab-development-lint"), archive)
	}
	repo := livetest.NewBareScratchRepo(t, target, "gitlab-consumer-development", consumer.ObjectFormat, private)
	client, err := gitlabapi.NewClient(target.Token, gitlabapi.WithBaseURL(target.BaseURL()+"/api/v4/"),
		gitlabapi.WithHTTPClient(livetest.HTTPClient(t, target, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(livetest.Context(t), 60*time.Minute)
	defer cancel()
	project := livetest.RepoSlug(target, repo)
	main := consumer.PushGitLab(t, repo, layer, images)
	requireObjectFormats(t, consumer.ObjectFormat, main, layer.Commit)
	before := livetest.BranchCommitSHA(t, target, repo, "main")
	if before != main {
		t.Fatalf("main before development = %s, want %s", before, main)
	}
	// Check these even if pipeline or artifact acceptance fails.
	defer func() {
		after := livetest.BranchCommitSHA(t, target, repo, "main")
		t.Logf("development main before=%s after=%s", before, after)
		if after != before {
			t.Error("development advanced main")
		}
		tags, _, err := client.Tags.ListTags(project, nil, gitlabapi.WithContext(ctx))
		if err != nil {
			t.Error(err)
		} else if len(tags) != 0 {
			t.Errorf("development created %d tags", len(tags))
		}
		releases, _, err := client.Releases.ListReleases(project, nil, gitlabapi.WithContext(ctx))
		if err != nil {
			t.Error(err)
		} else if len(releases) != 0 {
			t.Errorf("development created %d releases", len(releases))
		}
	}()

	head := consumer.PushChange(t, repo, "development", main)
	t.Logf("consumer %s at %s; development %s; layer %s; job image %s", root, consumer.Commit, head, layer.Commit, images.Job)
	run := waitGitLabPushPipeline(t, ctx, client, project, "development", head)
	jobName := acceptance.jobName
	trace := gitlabTrialTrace(t, ctx, client, project, run.ID, jobName)
	if run.Status != "success" {
		logGitLabTrace(t, trace)
		t.Fatalf("native development push pipeline %d ended %s", run.ID, run.Status)
	}
	if !strings.Contains(trace, acceptance.smokeMarker+head) {
		t.Fatal("development job did not confirm smoke at the expected source commit")
	}
	jobs, _, err := client.Jobs.ListPipelineJobs(project, run.ID, &gitlabapi.ListJobsOptions{
		ListOptions: gitlabapi.ListOptions{PerPage: 100},
	}, gitlabapi.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, job := range jobs {
		if job.Name != jobName {
			continue
		}
		found++
		if job.Status != "success" || job.Commit == nil || job.Commit.ID != head || job.ArtifactsFile.Size == 0 {
			t.Fatal("development job lacks successful exact-source native artifacts")
		}
		url := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/artifacts", target.BaseURL(), gitlabapi.PathEscape(project), job.ID)
		acceptance.checkArchive(t, livetest.Download(t, target, url, acceptance.maxArchive), head)
	}
	if found != 1 {
		t.Fatalf("found %d development jobs, want one", found)
	}
}

func requireDevelopmentArchive(t *testing.T, body []byte, commit string) {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	packages, sboms := 0, 0
	var version, sbomVersion string
	for _, file := range archive.File {
		isPackage := strings.HasSuffix(file.Name, ".tgz")
		if !isPackage && file.Name != "bom.json" {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(stream, maxReleaseFile+1))
		_ = stream.Close()
		if err != nil || len(data) > maxReleaseFile {
			t.Fatalf("read bounded development artifact %s: %v", file.Name, err)
		}
		if isPackage {
			packages++
			version = npmPackageVersion(t, data)
		} else {
			sboms++
			sbomVersion = buildSBOMVersion(t, data)
		}
	}
	if packages != 1 || sboms != 1 || version != sbomVersion || !strings.Contains(version, "-dev.") || !strings.HasSuffix(version, "."+commit[:12]) {
		t.Fatalf("development archive: packages=%d SBOMs=%d package version=%q SBOM version=%q", packages, sboms, version, sbomVersion)
	}
	t.Logf("native development archive: package and Build SBOM agree at %s (%d bytes)", version, len(body))
}
