// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const (
	parityKanikoImage  = "registry.invalid/team/kaniko-job@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	parityBuildahImage = "registry.invalid/team/buildah-job@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

const parityBuildImage = "registry.invalid/team/build@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// The GitLab release image stage and the Forgejo release-images workflow are
// one stage on two forges: for the same containers: declaration both plan,
// build every planned architecture natively, publish after every build, hand
// the result to signing, and run the builds with the same builder.
func TestReleaseImageStageMatchesTheForgejoWorkflow(t *testing.T) {
	t.Parallel()

	plan := parityPlan(t)

	archs := []string{}
	for _, build := range plan.Builds {
		if !slices.Contains(archs, build.Arch) {
			archs = append(archs, build.Arch)
		}
	}

	if !reflect.DeepEqual(archs, []string{"amd64", "arm64"}) {
		t.Fatalf("the fixture plans %v", archs)
	}

	forgejo := forgejoReleaseImagesJobs(t)

	wantForgejo := map[string][]string{
		"plan":         {"coordinates"},
		"fetch-inputs": {"coordinates", "plan"},
		"build":        {"coordinates", "plan", "fetch-inputs"},
		"publish":      {"coordinates", "plan", "build"},
		"collect":      {"coordinates", "plan", "publish"},
	}
	for name, needs := range wantForgejo {
		if !reflect.DeepEqual(forgejo.needs[name], needs) {
			t.Fatalf("Forgejo %s needs %v, want %v", name, forgejo.needs[name], needs)
		}
	}

	if forgejo.matrix != "${{ fromJSON(needs.plan.outputs.builds) }}" {
		t.Fatalf("Forgejo builds are not one per planned build: %s", forgejo.matrix)
	}

	for _, builder := range []string{"buildah", "kaniko"} {
		t.Run(builder, func(t *testing.T) {
			t.Parallel()

			gitlab := renderGitLabImageStage(t, "true", builder, `["arm64"]`)

			// The same stages, in the same order: Forgejo's build matrix is one
			// job per planned build, GitLab's one job per planned architecture,
			// each handing off the directory the next job reads.
			want := map[string]gitlabImageJob{
				"Images: plan":           {stage: "images", needs: []map[string]any{{"job": "Prepare: release commit and tag", "artifacts": false}}, artifacts: []string{"_release-images-plan/"}},
				"Images: build amd64":    {stage: "images", needs: needsOf("Images: plan", "Build: release files"), artifacts: []string{"_release-images-amd64/"}},
				"Images: build arm64":    {stage: "images", needs: needsOf("Images: plan", "Build: release files"), artifacts: []string{"_release-images-arm64/"}},
				"Images: publish":        {stage: "images", needs: publishNeeds, artifacts: []string{"_release-images/"}},
				"Sign: files and images": {stage: "sign", needs: needsOf("Images: publish"), artifacts: []string{"_release-sign/"}},
			}
			if len(gitlab) != len(want) {
				t.Fatalf("GitLab renders %d image stage jobs, want %d", len(gitlab), len(want))
			}

			for name, job := range want {
				got, ok := gitlab[name]
				if !ok {
					t.Fatalf("GitLab renders no %s", name)
				}

				if got.stage != job.stage || !reflect.DeepEqual(got.needs, job.needs) || !reflect.DeepEqual(got.artifacts, job.artifacts) {
					t.Fatalf("GitLab %s is stage %s, needs %v, hands off %v; want %s, %v, %v",
						name, got.stage, got.needs, got.artifacts, job.stage, job.needs, job.artifacts)
				}
			}

			// The same builder selection: the builder each forge's build jobs
			// run, in the image it runs in, on a native runner of the arch.
			forgejoBuilder, forgejoContainer := forgejoBuilderSelection(t, builder)

			// GitLab runs every job in an image: the builder's job image.
			wantImage, wantAMD64Tags := parityBuildahImage, []string{"privileged"}
			if builder == "kaniko" {
				wantImage, wantAMD64Tags = parityKanikoImage, nil
			}

			for arch, tags := range map[string][]string{"amd64": wantAMD64Tags, "arm64": {"arm64"}} {
				job := gitlab["Images: build "+arch]
				if job.builder != forgejoBuilder {
					t.Fatalf("GitLab %s builds with %s, Forgejo with %s", arch, job.builder, forgejoBuilder)
				}

				if job.image != wantImage || (job.image == parityKanikoImage) != (forgejoContainer == parityKanikoImage) {
					t.Fatalf("GitLab %s runs in %s, want %s; Forgejo builds in %q", arch, job.image, wantImage, forgejoContainer)
				}

				if !reflect.DeepEqual(job.tags, tags) {
					t.Fatalf("GitLab %s runs on tags %v, want %v", arch, job.tags, tags)
				}
			}
		})
	}
}

// Without an arm64 runner there is no arm64 build, and publishing, whose need
// of it is optional, runs after the amd64 build alone.
func TestReleaseImageStageWithoutAnARM64Runner(t *testing.T) {
	t.Parallel()

	gitlab := renderGitLabImageStage(t, "true", "buildah", "[]")

	if _, ok := gitlab["Images: build arm64"]; ok {
		t.Fatal("GitLab renders an arm64 build without an arm64 runner")
	}

	if _, ok := gitlab["Images: build amd64"]; !ok {
		t.Fatal("GitLab renders no amd64 build")
	}

	if got := gitlab["Images: publish"].needs; !reflect.DeepEqual(got, publishNeeds) {
		t.Fatalf("publishing needs %v, want %v", got, publishNeeds)
	}
}

// Without release images there is no image stage, and signing reads the
// build's hand-off.
func TestReleaseImageStageIsAbsentWithoutReleaseImages(t *testing.T) {
	t.Parallel()

	gitlab := renderGitLabImageStage(t, "false", "buildah", `["arm64"]`)

	sign, ok := gitlab["Sign: files and images"]
	if len(gitlab) != 1 || !ok {
		t.Fatalf("GitLab renders %v, want only signing", slices.Collect(maps.Keys(gitlab)))
	}

	if !reflect.DeepEqual(sign.needs, needsOf("Build: release files")) {
		t.Fatalf("signing needs %v, want the build alone", sign.needs)
	}
}

// Each hand-off directory the image jobs upload is the one the next job's
// function in release.sh reads.
func TestReleaseImageHandoffsAreTheOnesTheirReadersRead(t *testing.T) {
	t.Parallel()

	for reader, reads := range map[string][]string{
		"rc_images_build":   {`"${CI_PROJECT_DIR}/_release-images-plan/plan.json"`, `"${CI_PROJECT_DIR}/_release-build/image-context"`},
		"rc_images_publish": {`"${CI_PROJECT_DIR}/_release-images-plan/plan.json"`, `"amd64=${CI_PROJECT_DIR}/_release-images-amd64"`, `"arm64=${CI_PROJECT_DIR}/_release-images-arm64"`, `rc_read_handoff "${CI_PROJECT_DIR}/_release-build"`},
		"rc_sign_release":   {`rc_read_handoff "${CI_PROJECT_DIR}/_release-images"`, `rc_read_handoff "${CI_PROJECT_DIR}/_release-build"`},
	} {
		body := releaseShellFunction(t, reader)
		for _, read := range reads {
			if !strings.Contains(body, read) {
				t.Errorf("%s does not read %s", reader, read)
			}
		}
	}

	for writer, handoff := range map[string]string{
		"rc_images_plan":    `rc_new_handoff "${CI_PROJECT_DIR}/_release-images-plan"`,
		"rc_images_build":   `handoff="${CI_PROJECT_DIR}/_release-images-${arch}"`,
		"rc_images_publish": `handoff="${CI_PROJECT_DIR}/_release-images"`,
	} {
		if !strings.Contains(releaseShellFunction(t, writer), handoff) {
			t.Errorf("%s does not write %s", writer, handoff)
		}
	}
}

// releaseShellFunction is the body of one function of release.sh.
func releaseShellFunction(t *testing.T, name string) string {
	t.Helper()

	text := string(reporoot.ReadFile(t, ".gitlab/bin/release.sh"))

	_, body, found := strings.Cut(text, "\n"+name+"() {\n")
	body, _, closed := strings.Cut(body, "\n}\n")

	if !found || !closed {
		t.Fatalf("release.sh has no %s", name)
	}

	return body
}

func parityPlan(t *testing.T) pipeline.ReleaseImagesPlan {
	t.Helper()

	dir := t.TempDir()
	config := filepath.Join(dir, "artifacts.yml")

	if err := os.WriteFile(config, []byte("containers:\n  - name: app\n    container-file: Containerfile\n    context: .\n    platforms: linux/amd64,linux/arm64\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := appconfig.ResolveReleaseImagesPlan(appconfig.EmitReleaseImagesPlanInput{
		Config: appconfig.EmitConfigPlanInput{Path: config},
		Images: pipeline.ReleaseImagesInput{ImageRepository: "registry.invalid/group/app", Repository: "group/app", ReleaseTag: "v1.2.3"},
	}, output.Annotator{})
	if err != nil {
		t.Fatal(err)
	}

	return plan
}

type gitlabImageJob struct {
	stage, image, builder string
	needs                 []map[string]any
	tags, artifacts       []string
}

// needsOf is the needs of a job that downloads the artifacts of each job.
func needsOf(jobs ...string) []map[string]any {
	needs := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		needs = append(needs, map[string]any{"job": job, "artifacts": true})
	}

	return needs
}

// publishNeeds is what publishing needs whether or not an arm64 build runs:
// that build is optional, so a policy without an arm64 runner still
// publishes the amd64 image.
var publishNeeds = append(needsOf("Images: plan", "Build: release files", "Images: build amd64"),
	map[string]any{"job": "Images: build arm64", "artifacts": true, "optional": true})

// renderGitLabImageStage interpolates the release jobs' inputs, as GitLab
// does, for a policy whose arm64 runner tags are arm64Tags, and keeps the
// image stage and signing jobs whose rules admit them, their extends resolved.
func renderGitLabImageStage(t *testing.T, releaseImages, builder, arm64Tags string) map[string]gitlabImageJob {
	t.Helper()

	text := string(reporoot.ReadFile(t, "templates/release-jobs.yml"))
	text = text[strings.Index(text, "\n---\n")+5:]

	builderImage := map[string]string{"buildah": parityBuildahImage, "kaniko": parityKanikoImage}[builder]

	for name, value := range map[string]string{
		"release-images": releaseImages, "image-builder": builder, "builder-job-image": builderImage,
		"build-image": parityBuildImage, "build-runner-tags": `["privileged"]`, "build-runner-tags-arm64": arm64Tags,
	} {
		text = strings.ReplaceAll(text, "$[[ inputs."+name+" ]]", value)
	}

	var body map[string]any
	if err := yaml.Unmarshal([]byte(text), &body); err != nil {
		t.Fatal(err)
	}

	jobs := map[string]gitlabImageJob{}

	for name, raw := range body {
		if !strings.HasPrefix(name, "Images: ") && name != "Sign: files and images" {
			continue
		}

		fields := gitlabrender.ResolveExtends(t, body, raw.(map[string]any))
		if !gitlabrender.RulesAdmit(fields["rules"]) {
			continue
		}

		job := gitlabImageJob{stage: fields["stage"].(string), tags: yamlStrings(fields["tags"])}

		for _, need := range fields["needs"].([]any) {
			job.needs = append(job.needs, need.(map[string]any))
		}

		if artifacts, ok := fields["artifacts"].(map[string]any); ok {
			job.artifacts = yamlStrings(artifacts["paths"])
		}

		if image, ok := fields["image"].(map[string]any); ok {
			job.image, _ = image["name"].(string)
		}

		if script, ok := fields["script"].([]any); ok && strings.HasPrefix(script[0].(string), "rc_images_build ") {
			job.builder = strings.Trim(strings.Fields(script[0].(string))[2], "'")
		}

		jobs[name] = job
	}

	return jobs
}

func yamlStrings(value any) []string {
	list, _ := value.([]any)
	if len(list) == 0 {
		return nil
	}

	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.(string))
	}

	return out
}

type forgejoImageJobs struct {
	needs  map[string][]string
	matrix string
}

func forgejoReleaseImagesJobs(t *testing.T) forgejoImageJobs {
	t.Helper()

	var workflow struct {
		Jobs map[string]struct {
			Needs    any `yaml:"needs"`
			Strategy struct {
				Matrix map[string]string `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/workflows/release-images.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	jobs := forgejoImageJobs{needs: map[string][]string{}, matrix: workflow.Jobs["build"].Strategy.Matrix["build"]}

	for name, job := range workflow.Jobs {
		switch needs := job.Needs.(type) {
		case string:
			jobs.needs[name] = []string{needs}
		case []any:
			for _, need := range needs {
				jobs.needs[name] = append(jobs.needs[name], need.(string))
			}
		}
	}

	return jobs
}

// forgejoBuilderSelection runs the workflow's coordinates step for builder and
// returns the builder and container its build jobs take from it.
func forgejoBuilderSelection(t *testing.T, builder string) (string, string) {
	t.Helper()

	var workflow struct {
		Jobs map[string]struct {
			Container string            `yaml:"container"`
			Env       map[string]string `yaml:"env"`
			Steps     []struct {
				ID  string `yaml:"id"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/workflows/release-images.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	build := workflow.Jobs["build"]
	if build.Container != "${{ needs.coordinates.outputs.builder_job_image }}" ||
		build.Env["CI_IMAGE_BUILDER"] != "${{ needs.coordinates.outputs.builder }}" {
		t.Fatalf("Forgejo builds do not take the coordinates' builder: %+v", build)
	}

	var script string

	for _, step := range workflow.Jobs["coordinates"].Steps {
		if step.ID == "validate" {
			script = step.Run
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	outputs := filepath.Join(t.TempDir(), "outputs")
	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-c", script)
	command.Env = []string{
		"PATH=/usr/bin:/bin", "FORGEJO_OUTPUT=" + outputs, "SERVER_URL=https://forge.invalid",
		"HELPERS_REPOSITORY_INPUT=team/engine", "BUILDER_INPUT=" + builder, "BUILDER_JOB_IMAGE_INPUT=" + parityKanikoImage,
	}

	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("coordinates refused %s: %v\n%s", builder, err, out)
	}

	values := map[string]string{}

	body, err := os.ReadFile(outputs)
	if err != nil {
		t.Fatal(err)
	}

	for line := range strings.SplitSeq(strings.TrimSpace(string(body)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		values[key] = value
	}

	return values["builder"], values["builder_job_image"]
}
