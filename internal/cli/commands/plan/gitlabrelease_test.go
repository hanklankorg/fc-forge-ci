// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	plancmd "github.com/diggsweden/forge-ci/internal/cli/commands/plan"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

const gitlabReleaseDeclaration = `artifacts:
  - name: backend-api
    project-type: maven
    working-directory: services/backend
  - name: frontend-ui
    project-type: npm
    working-directory: packages/frontend
`

// The release plan job writes the generated child jobs, the prepare plan the
// version and release commit jobs apply, and a build job's artifact names,
// all from one declaration.
func TestGitlabReleasePipelineCmd_WritesTheReleaseJobsFromTheDeclaration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	declaration := filepath.Join(dir, "artifacts.yml")
	families, prepare, names := filepath.Join(dir, "families.yml"), filepath.Join(dir, "prepare.json"), filepath.Join(dir, "names")

	if err := os.WriteFile(declaration, []byte(gitlabReleaseDeclaration), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := plancmd.New().Run(t.Context(), []string{
		"plan", "gitlab-release-pipeline", "--file", declaration,
		"--runtime-image-java", "registry.example/java@sha256:" + strings.Repeat("1", 64),
		"--runtime-image-node", "registry.example/node@sha256:" + strings.Repeat("2", 64),
		"--output", families, "--prepare-plan-output", prepare,
		"--family", "java", "--artifacts-output", names,
	}); err != nil {
		t.Fatal(err)
	}

	var jobs map[string]map[string]string
	if body, err := os.ReadFile(families); err != nil || json.Unmarshal(body, &jobs) != nil {
		t.Fatalf("read the generated jobs: %v", err)
	}

	want := map[string]string{
		"Prepare: version java": ".forge-ci-release-version-java", "Prepare: version node": ".forge-ci-release-version-node",
		"Build: java": ".forge-ci-release-build-java", "Build: node": ".forge-ci-release-build-node",
	}
	if len(jobs) != len(want) {
		t.Fatalf("generated jobs %v, want %v", jobs, want)
	}

	for name, extends := range want {
		if jobs[name]["extends"] != extends {
			t.Errorf("job %q = %v, want extends %s", name, jobs[name], extends)
		}
	}

	body, err := os.ReadFile(prepare)
	if err != nil {
		t.Fatal(err)
	}

	var plan pipeline.ReleasePrepareStagePlan

	err = pipeline.DecodeStagePlan(string(body), "prepare plan", &plan)
	if err != nil || !plan.Targets.VersionBump.Runs || len(plan.Targets.VersionBump.Items) != 2 {
		t.Fatalf("prepare plan %+v, %v; want both projects bumped", plan, err)
	}

	if body, err := os.ReadFile(names); err != nil || string(body) != "backend-api\n" {
		t.Errorf("java artifacts = %q, %v; want backend-api", body, err)
	}

	// A job that checks which planned jobs ran names them without the images.
	jobNames := filepath.Join(dir, "job-names")
	if err := plancmd.New().Run(t.Context(), []string{"plan", "gitlab-release-pipeline", "--file", declaration, "--job-names-output", jobNames}); err != nil {
		t.Fatal(err)
	}

	if body, err := os.ReadFile(jobNames); err != nil || string(body) != "Prepare: version java\nPrepare: version node\nBuild: java\nBuild: node\n" {
		t.Errorf("job names = %q, %v", body, err)
	}

	// A version job takes its project type from the plan, without the images.
	for family, kind := range map[string]string{"java": "maven", "node": "npm"} {
		projectType := filepath.Join(dir, family+"-project-type")
		if err := plancmd.New().Run(t.Context(), []string{"plan", "gitlab-release-pipeline", "--file", declaration, "--family", family, "--project-type-output", projectType}); err != nil {
			t.Fatal(err)
		}

		if body, err := os.ReadFile(projectType); err != nil || string(body) != kind+"\n" {
			t.Errorf("%s project type = %q, %v; want %s", family, body, err, kind)
		}
	}

	// The package job is named, and its packages listed, only for an npm
	// project the declaration sends to forge-packages.
	packages := filepath.Join(dir, "packages")

	for body, want := range map[string][2]string{
		gitlabReleaseDeclaration: {"", "Build: node\n"},
		gitlabReleaseDeclaration + "    publish-to: [forge-packages]\n    config:\n      npm-tag: release-{version}\n": {"packages/frontend\n", "Build: node\nPublish: npm packages\n"},
	} {
		if err := os.WriteFile(declaration, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := plancmd.New().Run(t.Context(), []string{"plan", "gitlab-release-pipeline", "--file", declaration, "--packages-output", packages, "--job-names-output", jobNames}); err != nil {
			t.Fatal(err)
		}

		if listed, err := os.ReadFile(packages); err != nil || string(listed) != want[0] {
			t.Errorf("packages = %q, %v; want %q", listed, err, want[0])
		}

		if names, err := os.ReadFile(jobNames); err != nil || !strings.HasSuffix(string(names), want[1]) {
			t.Errorf("job names = %q, %v; want them to end %q", names, err, want[1])
		}
	}
}

func TestGitlabReleasePipelineCmd_RefusesWhatItCannotPlan(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	declaration := filepath.Join(dir, "artifacts.yml")

	if err := os.WriteFile(declaration, []byte(gitlabReleaseDeclaration), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, args := range map[string][]string{
		"no output":               {"--file", declaration},
		"family alone":            {"--file", declaration, "--family", "java", "--output", filepath.Join(dir, "a")},
		"a project type alone":    {"--file", declaration, "--project-type-output", filepath.Join(dir, "d")},
		"an unplanned family":     {"--file", declaration, "--family", "rust", "--project-type-output", filepath.Join(dir, "e")},
		"a family with no tool":   {"--file", declaration, "--family", "go", "--project-type-output", filepath.Join(dir, "f")},
		"a named file is missing": {"--file", filepath.Join(dir, "absent.yml"), "--output", filepath.Join(dir, "b")},
		"a family without image":  {"--file", declaration, "--output", filepath.Join(dir, "c")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := plancmd.New().Run(t.Context(), append([]string{"plan", "gitlab-release-pipeline"}, args...))
			if !errors.Is(err, errs.ErrUsage) && !errors.Is(err, errs.ErrMissingInput) {
				t.Fatalf("Run() = %v, want a usage or missing-input refusal", err)
			}
		})
	}
}
