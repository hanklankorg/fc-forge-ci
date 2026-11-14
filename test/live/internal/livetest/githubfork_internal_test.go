// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-github/v91/github"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestParseGitHubTrialSettings_ForkHead(t *testing.T) {
	t.Parallel()

	settings, err := ParseGitHubTrialSettings(TrialPullRequest, []string{"head=fork"})
	if err != nil || !settings.Fork {
		t.Fatalf("head=fork: %+v, %v", settings, err)
	}

	if ordinary, err := ParseGitHubTrialSettings(TrialPullRequest, nil); err != nil || ordinary.Fork {
		t.Fatalf("a pull request is from a fork without being asked: %+v, %v", ordinary, err)
	}

	for event, word := range map[string]string{
		TrialPullRequest: "head=branch", TrialRelease: "head=fork", TrialDevRelease: "head=fork", TrialLifecycle: "head=fork",
	} {
		if _, err := ParseGitHubTrialSettings(event, []string{word}); !errors.Is(err, errs.ErrValidation) {
			t.Errorf("%s %s accepted: %v", event, word, err)
		}
	}
}

// The oracle finds the refusal and the checkout by the names the build
// workflows give them, so the names must be the workflows', in that order.
func TestForkRefusalStepsAreTheBuildWorkflows(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"build-npm.yml", "build-maven.yml", "build-gradle-app.yml"} {
		body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}

		refusal := strings.Index(string(body), "- name: "+forkRefusalStep+"\n")
		checkout := strings.Index(string(body), "- name: "+forkCheckoutStep+"\n")

		if refusal < 0 || checkout < refusal {
			t.Errorf("%s: steps %q then %q not found (at %d, %d)", name, forkRefusalStep, forkCheckoutStep, refusal, checkout)
		}
	}
}

func TestForkRefusalGap(t *testing.T) {
	t.Parallel()

	step := func(name, conclusion string) *github.TaskStep {
		return &github.TaskStep{Name: new(name), Conclusion: new(conclusion)}
	}
	build := func(steps ...*github.TaskStep) *github.WorkflowJob {
		return &github.WorkflowJob{Name: new("build / Build NPM Project"), Steps: steps}
	}
	bootstrap := &github.WorkflowJob{Name: new("build / resolve-bootstrap"), Steps: []*github.TaskStep{step("Set up job", "success"), step("Complete job", "success")}}
	refused := build(step("Set up job", "success"), step("Initialize containers", "success"), step(forkRefusalStep, "failure"),
		step("Guard working-directory stays inside the workspace", "skipped"), step(forkCheckoutStep, "skipped"), step("Build NPM project", "skipped"),
		step("Stop containers", "success"), step("Complete job", "success"))

	if err := forkRefusalGap("failure", []*github.WorkflowJob{bootstrap, refused}); err != nil {
		t.Fatalf("the refusal is not recognized: %v", err)
	}

	for name, tc := range map[string]struct {
		conclusion string
		jobs       []*github.WorkflowJob
	}{
		"run succeeded":         {"success", []*github.WorkflowJob{bootstrap, refused}},
		"build never started":   {"failure", []*github.WorkflowJob{bootstrap}},
		"refusal not evaluated": {"failure", []*github.WorkflowJob{build(step(forkRefusalStep, "skipped"), step(forkCheckoutStep, "success"), step("Build NPM project", "failure"))}},
		"refusal passed":        {"failure", []*github.WorkflowJob{build(step(forkRefusalStep, "success"), step(forkCheckoutStep, "success"), step("Build NPM project", "failure"))}},
		"checkout ran":          {"failure", []*github.WorkflowJob{build(step(forkRefusalStep, "failure"), step(forkCheckoutStep, "success"), step("Build NPM project", "skipped"))}},
		"build ran":             {"failure", []*github.WorkflowJob{build(step(forkRefusalStep, "failure"), step(forkCheckoutStep, "skipped"), step("Build NPM project", "failure"))}},
		"checkout first":        {"failure", []*github.WorkflowJob{build(step(forkCheckoutStep, "success"), step(forkRefusalStep, "failure"))}},
		"a step first": {"failure", []*github.WorkflowJob{build(step("Set up job", "success"), step("Log in to the registry", "success"),
			step(forkRefusalStep, "failure"), step(forkCheckoutStep, "skipped"))}},
		"refused twice": {"failure", []*github.WorkflowJob{refused, refused}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := forkRefusalGap(tc.conclusion, tc.jobs); !errors.Is(err, errReleaseEvidence) {
				t.Fatalf("a run that is not the promised refusal is accepted: %v", err)
			}
		})
	}
}
