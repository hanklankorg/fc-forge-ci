// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build live

package conformance_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
	gitlabapi "gitlab.com/gitlab-org/api/client-go"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/runners"
)

// Pure fixtures use the same declaration-to-plan owner as Release: plan.
// They never accept a target or invoke a provider, even with the live tag.
func gitLabRoutingPlans(t *testing.T) appconfig.ReleaseStagePlans {
	t.Helper()

	const declaration = `artifacts:
  - name: backend
    project-type: maven
    working-directory: backend
  - name: frontend
    project-type: npm
    working-directory: frontend
  - name: tool
    project-type: go
    working-directory: tool
    config:
      build-mode: artifact-first
`

	plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{
		Path: ".forge-ci/artifacts.yml",
		FS:   fstest.MapFS{".forge-ci/artifacts.yml": {Data: []byte(declaration)}},
	}, output.Annotator{}, false)
	require.NoError(t, err)

	return plans
}

func TestGitLabReleaseRunnerRoles(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		images, exports bool
	}{
		{"files", false, false},
		{"images", true, false},
		{"exports", false, true},
		{"images and exports", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			build := gitLabReleaseRunnerRole{tags: []string{"build"}, role: runners.Build}
			ordinary := gitLabReleaseRunnerRole{tags: []string{"ordinary"}, role: runners.Ordinary}

			want := map[string]gitLabReleaseRunnerRole{
				"Check: consumer checks": build, "Build: release files": build,
				"Prepare: version java": ordinary, "Prepare: version node": ordinary,
				"Build: java": build, "Build: node": build, "Build: go": build,
			}
			if tc.images {
				want["Images: build amd64"] = build
			}

			if tc.exports {
				want["Build: export amd64"] = build
			}

			got, err := gitLabReleaseRunnerRoles(build.tags, ordinary.tags, tc.images, tc.exports, "", gitLabRoutingPlans(t))
			require.NoError(t, err)
			require.Equal(t, want, got, "all and only the planned version/build jobs must be covered")
		})
	}

	// The package job holds a release credential: it is the signer runner's,
	// and no ordinary or build runner's.
	plans := gitLabRoutingPlans(t)
	plans.Build.Targets.NPM.Items[0].PublishTo = []config.PublishTarget{config.PublishForgePackages}

	got, err := gitLabReleaseRunnerRoles([]string{"build"}, []string{"ordinary"}, false, false, "", plans)
	require.NoError(t, err)
	require.NotContains(t, got, "Publish: npm packages")
}

func TestGitLabReleaseRunnerRolesSeparateVersionAndBuildPlans(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		version, build bool
		generated      []string
	}{
		{"version only", true, false, []string{"Prepare: version java", "Prepare: version node"}},
		{"build only", false, true, []string{"Build: java", "Build: node", "Build: go"}},
		{"undeclared", false, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			plans := gitLabRoutingPlans(t)

			emptyPrepare, emptyBuild := pipeline.UndeclaredReleaseStagePlans()
			if !tc.version {
				plans.Prepare = emptyPrepare
			}

			if !tc.build {
				plans.Build = emptyBuild
			}

			got, err := gitLabReleaseRunnerRoles([]string{"build"}, []string{"ordinary"}, false, false, "", plans)
			require.NoError(t, err)
			require.Equal(t, runners.Build, got["Build: release files"].role)
			require.Equal(t, runners.Build, got["Check: consumer checks"].role)
			delete(got, "Build: release files")
			delete(got, "Check: consumer checks")
			require.Len(t, got, len(tc.generated))

			for _, name := range tc.generated {
				require.Contains(t, got, name)

				role := runners.Build
				if tc.version {
					role = runners.Ordinary
				}

				require.Equal(t, role, got[name].role)
			}
		})
	}
}

func gitLabRoutingJobs(expected map[string]gitLabReleaseRunnerRole) []*gitlabapi.Job {
	jobs := make([]*gitlabapi.Job, 0, len(expected)+1)

	for name, want := range expected {
		runner := int64(2)
		if want.role == runners.Build {
			runner = 1
		}

		jobs = append(jobs, &gitlabapi.Job{Name: name, TagList: want.tags, Status: "success", Runner: gitlabapi.JobRunner{ID: runner}})
	}

	return append(jobs, &gitlabapi.Job{Name: "Sign: release", Status: "success", Runner: gitlabapi.JobRunner{ID: 3}})
}

func TestGitLabReleaseRunnerRoutingOracle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, target, mutation, refusal string
	}{
		{name: "multi-family success"},
		{name: "family on ordinary tags", target: "Build: go", mutation: "wrong tags", refusal: "requested tags"},
		{name: "version on build tags", target: "Prepare: version java", mutation: "build tags", refusal: "requested tags"},
		{name: "version on build runner", target: "Prepare: version java", mutation: "build runner", refusal: "build capability runner"},
		{name: "release files on ordinary tags", target: "Build: release files", mutation: "wrong tags", refusal: "requested tags"},
		{name: "missing family", target: "Build: node", mutation: "missing", refusal: "missing successful jobs"},
		{name: "missing version", target: "Prepare: version node", mutation: "missing", refusal: "missing successful jobs"},
		{name: "missing release files", target: "Build: release files", mutation: "missing", refusal: "missing successful jobs"},
		{name: "skipped family", target: "Build: java", mutation: "skipped", refusal: "requested tags"},
		{name: "failed family", target: "Build: java", mutation: "failed", refusal: "requested tags"},
		{name: "no family runner", target: "Build: java", mutation: "no runner", refusal: "requested tags"},
		{name: "missing image", target: "Images: build amd64", mutation: "missing", refusal: "missing successful jobs"},
		{name: "missing export", target: "Build: export amd64", mutation: "missing", refusal: "missing successful jobs"},
		{name: "duplicate planned job", target: "Build: go", mutation: "duplicate", refusal: "duplicate planned job"},
		{name: "unexpected tagged build", mutation: "unexpected build", refusal: "unrelated job"},
		{name: "unexpected untagged build capability", mutation: "unexpected capability", refusal: "build capability runner"},
		{name: "signer capability leak", target: "Sign: release", mutation: "build runner", refusal: "build capability runner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			expected, err := gitLabReleaseRunnerRoles([]string{"build"}, []string{"ordinary"}, true, true, "", gitLabRoutingPlans(t))
			require.NoError(t, err)

			jobs := gitLabRoutingJobs(expected)

			for i, job := range jobs {
				if job.Name != tc.target {
					continue
				}

				switch tc.mutation {
				case "wrong tags":
					job.TagList = []string{"ordinary"}
				case "build tags":
					job.TagList = []string{"build"}
				case "build runner":
					job.Runner.ID = 1
				case "missing":
					jobs = append(jobs[:i:i], jobs[i+1:]...)
				case "skipped":
					job.Status = "skipped"
				case "failed":
					job.Status = "failed"
				case "no runner":
					job.Runner.ID = 0
				case "duplicate":
					jobs = append(jobs, job)
				}

				break
			}

			if tc.mutation == "unexpected build" {
				jobs = append(jobs, &gitlabapi.Job{Name: "Build: unplanned", TagList: []string{"build"}, Status: "success", Runner: gitlabapi.JobRunner{ID: 1}})
			}

			if tc.mutation == "unexpected capability" {
				jobs = append(jobs, &gitlabapi.Job{Name: "Build: unplanned", Status: "success", Runner: gitlabapi.JobRunner{ID: 1}})
			}

			err = validateGitLabReleaseRunnerRouting(jobs, expected)
			if tc.refusal == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.refusal)
			}
		})
	}
}

// The oracle admits its own plan with and without images and exports.
func TestGitLabReleaseRunnerRoutingOracleAdmitsEveryPlan(t *testing.T) {
	t.Parallel()

	for _, images := range []bool{false, true} {
		for _, exports := range []bool{false, true} {
			expected, err := gitLabReleaseRunnerRoles([]string{"build"}, []string{"ordinary"}, images, exports, "", gitLabRoutingPlans(t))
			require.NoError(t, err)
			require.NoError(t, validateGitLabReleaseRunnerRouting(gitLabRoutingJobs(expected), expected), "images=%t exports=%t", images, exports)
		}
	}
}

func TestGitLabReleaseRunnerRoutingUntaggedBuildStillHasACapabilityBoundary(t *testing.T) {
	t.Parallel()

	expected, err := gitLabReleaseRunnerRoles(nil, []string{"ordinary"}, true, true, "", gitLabRoutingPlans(t))
	require.NoError(t, err)

	jobs := gitLabRoutingJobs(expected)
	require.NoError(t, validateGitLabReleaseRunnerRouting(jobs, expected))

	for _, job := range jobs {
		if job.Name == "Sign: release" {
			job.Runner.ID = 1
		}
	}

	require.ErrorContains(t, validateGitLabReleaseRunnerRouting(jobs, expected), "build capability runner")
}
