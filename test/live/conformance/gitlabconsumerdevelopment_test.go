// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	"github.com/diggsweden/forge-ci/internal/domain/provider"
	"github.com/diggsweden/forge-ci/test/live/internal/livetest"
)

// developmentAcceptance is what a development push of a consumer with its
// own development job is judged by: the job, the line its log confirms the
// pushed commit with, and the checks of what the job kept and published.
type developmentAcceptance struct {
	jobName, smokeMarker, imageVariable, artifactPrefix string
	maxArchive                                          int64
	checkArchive                                        func(*testing.T, []byte, string)
	checkPublication                                    func(*testing.T, []byte, string, livetest.Target, string)
	// buildsImages says the development job builds images: with Buildah on
	// the contract's runner that permits user namespaces, or with kaniko
	// (FORGE_CI_LIVE_GITLAB_IMAGE_BUILDER) on the ordinary runner.
	buildsImages bool
}

// runGitLabDevelopment pushes the consumer's development branch under the
// rendered gate, once for each visibility, and judges its one push pipeline
// by the acceptance. It never selects the MR config or opens an MR. The lint
// image is pushed once, before the visibilities; the job image is served
// from each visibility's layer project and, for kaniko, is labelled with
// that layer.
func runGitLabDevelopment(t *testing.T, root string, acceptance developmentAcceptance) {
	t.Helper()

	visibilities := livetest.ConsumerVisibilities(t)

	if !livetest.Requires(t, provider.ForgeGitLab, livetest.NeedsInRunner) {
		livetest.NotCoveredf(t, "the development consumer needs a GitLab runner")
	}

	target := livetest.Accept(t, provider.ForgeGitLab)

	var lintImage string
	if lint := os.Getenv("FORGE_CI_LIVE_LINT_IMAGE_ARCHIVE"); lint != "" {
		lintImage = livetest.PushJobImage(t, target, livetest.NewPublicScratchRepo(t, target, "gitlab-development-lint", livetest.ReadJobImageAnonymously), lint)
	}

	for _, visibility := range visibilities {
		t.Run(visibility.Name, func(t *testing.T) {
			runGitLabDevelopmentVisibility(t, root, visibility.Private, acceptance, lintImage)
		})
	}
}

func runGitLabDevelopmentVisibility(t *testing.T, root string, private bool, acceptance developmentAcceptance, lintImage string) {
	t.Helper()

	target := livetest.Accept(t, provider.ForgeGitLab)

	consumer := livetest.OpenConsumer(t, target, root)
	if !consumer.HasGitLabGate(t) {
		t.Fatal("the development consumer must include .gitlab/merge-request.yml from its branch .gitlab-ci.yml")
	}

	engine := livetest.NewPublicScratchRepo(t, target, "gitlab-development-layer", livetest.ReadLayerAnonymously)
	layer := livetest.PushLayer(t, target, engine)

	archive := os.Getenv("FORGE_CI_LIVE_CONSUMER_IMAGE_ARCHIVE")

	images := livetest.GitLabGateImages{Lint: lintImage}

	switch {
	case acceptance.buildsImages && livetest.GitLabKaniko(t):
		// kaniko builds on the ordinary runner, in the job's own image, which
		// must be a kaniko job image; the job alone is told to use it.
		images.Job = livetest.PushKanikoJobImage(t, target, engine, archive, layer)
		images.JobVariables = map[string]map[string]string{acceptance.jobName: {
			"CI_IMAGE_BUILDER": livetest.ImageBuilderKaniko, "CI_IMAGE_BUILDER_JOB_IMAGE": images.Job,
		}}
	case acceptance.buildsImages:
		images.Job = livetest.PushJobImage(t, target, engine, archive)

		label := livetest.PrivilegedRunnerLabel(t, target)
		if label == "" {
			livetest.NotCoveredf(t, "image development requires the contract's privileged runner tag")
		}

		images.JobTags = map[string][]string{acceptance.jobName: {label}}
	default:
		images.Job = livetest.PushJobImage(t, target, engine, archive)
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
	installGitLabDevelopmentPolicy(t, client, target, project, consumer, layer, images, private)
	main := consumer.PushGitLab(t, repo, layer, images)
	requireObjectFormats(t, consumer.ObjectFormat, main, layer.Commit)

	before := livetest.BranchCommitSHA(t, target, repo, "main")
	if before != main {
		t.Fatalf("main before development = %s, want %s", before, main)
	}
	// Check these even if pipeline or artifact acceptance fails.
	defer func() {
		after := livetest.BranchCommitSHA(t, target, repo, "main") //nolint:contextcheck // helper derives its bounded request context from the test owner.
		t.Logf("development main before=%s after=%s", before, after)

		if after != before {
			t.Error("development advanced main")
		}

		tags, _, tagsErr := client.Tags.ListTags(project, nil, gitlabapi.WithContext(ctx))
		if tagsErr != nil {
			t.Error(tagsErr)
		} else if len(tags) != 0 {
			t.Errorf("development created %d tags", len(tags))
		}

		releases, _, releasesErr := client.Releases.ListReleases(project, nil, gitlabapi.WithContext(ctx))
		if releasesErr != nil {
			t.Error(releasesErr)
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
		livetest.FailOnRunConclusionf(t, "native development push pipeline %d ended %s", run.ID, run.Status)
	}

	if !strings.Contains(trace, acceptance.smokeMarker+head) {
		t.Fatal("development job did not confirm smoke at the expected source commit")
	}

	jobs, _, err := client.Jobs.ListPipelineJobs(project, run.ID, &gitlabapi.ListJobsOptions{
		PerPage: 100,
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
		if job.ArtifactsFile.Size > acceptance.maxArchive {
			t.Fatal("development native artifact exceeds acceptance bound")
		}

		body := livetest.Download(t, target, url, acceptance.maxArchive)
		if acceptance.checkArchive != nil {
			acceptance.checkArchive(t, body, head)
		}

		if acceptance.checkPublication != nil {
			acceptance.checkPublication(t, body, head, target, repo)
		}
	}

	if found != 1 {
		t.Fatalf("found %d development jobs, want one", found)
	}
}
