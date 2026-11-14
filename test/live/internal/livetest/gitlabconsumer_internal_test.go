// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

func TestGitLabConsumerPreservesChecksAndChangesOnlyTrialCoordinates(t *testing.T) {
	t.Parallel()

	pipeline := `# Consumer licence header.
workflow:
  rules:
    - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
include:
  - component: $CI_SERVER_FQDN/team/engine/check@` + strings.Repeat("a", 40) + `
    inputs:
      forge-ci-repository: team/engine
      server-url: $CI_SERVER_URL
      job-image: $CI_JOB_IMAGE
      commands:
        - 'nanolinter verify --require secrets'
        - 'go test -race ./...'
`
	layer := Layer{ServerURL: "https://gitlab.k3s.forgelab", Repository: "forgeci/fci-engine", Commit: strings.Repeat("b", 40)}
	image := "registry.gitlab.k3s.forgelab/forgeci/fci-engine@sha256:" + strings.Repeat("c", 64)

	rendered, err := renderGitLabConsumer([]byte(pipeline), layer, GitLabGateImages{Job: image})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(rendered, "# Consumer licence header.") {
		t.Fatal("rendering lost the consumer's licence header")
	}

	var parsed struct {
		Include []struct {
			Component string `yaml:"component"`
			Inputs    struct {
				ForgeCIRepository string   `yaml:"forge-ci-repository"`
				JobImage          string   `yaml:"job-image"`
				Commands          []string `yaml:"commands"`
			} `yaml:"inputs"`
		} `yaml:"include"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
		t.Fatal(err)
	}

	entry := parsed.Include[0]
	if entry.Component != "gitlab.k3s.forgelab/forgeci/fci-engine/check@"+layer.Commit ||
		entry.Inputs.ForgeCIRepository != layer.Repository || entry.Inputs.JobImage != image {
		t.Fatal("the trial did not bind all three coordinates")
	}

	commands := entry.Inputs.Commands
	if len(commands) != 2 || commands[0] != "nanolinter verify --require secrets" || commands[1] != "go test -race ./..." {
		t.Fatal("the trial changed the consumer's check commands")
	}

	tagged, err := renderGitLabConsumer([]byte(pipeline), layer, GitLabGateImages{
		Job: image, JobTags: map[string][]string{"Check: Rust development": {"privileged"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var jobs map[string]any
	if err := yaml.Unmarshal([]byte(tagged), &jobs); err != nil {
		t.Fatal(err)
	}

	job, ok := jobs["Check: Rust development"].(map[string]any)
	if !ok || len(job) != 1 || strings.Count(tagged, "privileged") != 1 || jobs["default"] != nil {
		t.Fatal("privileged tags were not confined to the image-building job")
	}

	kaniko := map[string]map[string]string{"Check: Rust development": {"CI_IMAGE_BUILDER": "kaniko", "CI_IMAGE_BUILDER_JOB_IMAGE": image}}
	own := pipeline + "\n'Check: Rust development':\n  variables:\n    CARGO_TERM_COLOR: never\n"

	selected, err := renderGitLabConsumer([]byte(own), layer, GitLabGateImages{Job: image, JobVariables: kaniko})
	if err != nil {
		t.Fatal(err)
	}

	var builder struct {
		Job struct {
			Variables map[string]string `yaml:"variables"`
		} `yaml:"Check: Rust development"`
	}
	if err := yaml.Unmarshal([]byte(selected), &builder); err != nil {
		t.Fatal(err)
	}

	if variables := builder.Job.Variables; len(variables) != 3 || variables["CI_IMAGE_BUILDER"] != "kaniko" ||
		variables["CI_IMAGE_BUILDER_JOB_IMAGE"] != image || variables["CARGO_TERM_COLOR"] != "never" || strings.Count(selected, "CI_IMAGE_BUILDER:") != 1 {
		t.Fatalf("the builder was not added to the image-building job beside its own variables: %v", variables)
	}

	if _, err := renderGitLabConsumer([]byte(pipeline+"\n'Check: Rust development':\n  variables:\n    CI_IMAGE_BUILDER: buildah\n"), layer,
		GitLabGateImages{Job: image, JobVariables: kaniko}); err == nil {
		t.Fatal("trial silently replaced a consumer-owned variable")
	}

	conflict := pipeline + "\n'Check: Rust development':\n  tags: [consumer-runner]\n"
	if _, err := renderGitLabConsumer([]byte(conflict), layer, GitLabGateImages{
		Job: image, JobTags: map[string][]string{"Check: Rust development": {"privileged"}},
	}); err == nil {
		t.Fatal("trial silently replaced consumer-owned scheduling")
	}

	for _, bad := range []string{
		pipeline + "\n---\nunexpected: job\n",
		strings.ReplaceAll(pipeline, strings.Repeat("a", 40), "main"),
		strings.ReplaceAll(pipeline, "forge-ci-repository: team/engine", "forge-ci-repository: other/engine"),
		strings.ReplaceAll(pipeline, "job-image: $CI_JOB_IMAGE", "job-image: unreviewed:latest"),
	} {
		if _, err := renderGitLabConsumer([]byte(bad), layer, GitLabGateImages{Job: image}); err == nil {
			t.Fatal("a mutable or inconsistent trial input was accepted")
		}
	}
}

// A gate without merge-request rules gets no pipeline on GitLab, so it is
// refused before anything is pushed, with the line to add.
func TestGitLabConsumerRefusesAGateThatNeverRunsForMergeRequests(t *testing.T) {
	t.Parallel()

	pipeline := `include:
  - component: $CI_SERVER_FQDN/team/engine/check@` + strings.Repeat("a", 40) + `
    inputs:
      forge-ci-repository: team/engine
      server-url: $CI_SERVER_URL
      job-image: $CI_JOB_IMAGE
      commands: ['go test ./...']
`
	layer := Layer{ServerURL: "https://gitlab.k3s.forgelab", Repository: "forgeci/fci-engine", Commit: strings.Repeat("b", 40)}

	_, err := renderGitLabConsumer([]byte(pipeline), layer, GitLabGateImages{Job: "registry.example/x@sha256:" + strings.Repeat("c", 64)})
	if err == nil || !strings.Contains(err.Error(), "merge_request_event") {
		t.Fatalf("renderGitLabConsumer() = %v, want a refusal naming the rule to add", err)
	}
}

// A gate may add the nanolinter component beside the check component; the
// trial binds both to the layer under test, each to its own image, and refuses
// any other component, an image the consumer fixed itself, or a lint gate the
// trial has no lint image for.
func TestGitLabConsumerBindsTheNanolinterComponentToo(t *testing.T) {
	t.Parallel()

	pipeline := `workflow:
  rules:
    - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
include:
  - component: $CI_SERVER_FQDN/team/engine/nanolinter@` + strings.Repeat("a", 40) + `
    inputs:
      forge-ci-repository: team/engine
      server-url: $CI_SERVER_URL
      job-name: "Check: lint"
      nanolinter-image: $CI_LINT_IMAGE
  - component: $CI_SERVER_FQDN/team/engine/check@` + strings.Repeat("a", 40) + `
    inputs:
      forge-ci-repository: team/engine
      server-url: $CI_SERVER_URL
      job-image: $CI_JOB_IMAGE
      commands: ['mvn verify']
`
	layer := Layer{ServerURL: "https://gitlab.k3s.forgelab", Repository: "forgeci/fci-engine", Commit: strings.Repeat("b", 40)}
	images := GitLabGateImages{
		Job:  "registry.gitlab.k3s.forgelab/forgeci/fci-engine@sha256:" + strings.Repeat("c", 64),
		Lint: "registry.gitlab.k3s.forgelab/forgeci/fci-lint@sha256:" + strings.Repeat("d", 64),
	}

	rendered, err := renderGitLabConsumer([]byte(pipeline), layer, images)
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Include []struct {
			Component string         `yaml:"component"`
			Inputs    map[string]any `yaml:"inputs"`
		} `yaml:"include"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
		t.Fatal(err)
	}

	lint, check := parsed.Include[0], parsed.Include[1]
	if lint.Component != "gitlab.k3s.forgelab/forgeci/fci-engine/nanolinter@"+layer.Commit ||
		lint.Inputs["forge-ci-repository"] != layer.Repository || lint.Inputs["nanolinter-image"] != images.Lint ||
		lint.Inputs["server-url"] != layer.ServerURL || check.Inputs["server-url"] != layer.ServerURL ||
		lint.Inputs["job-name"] != "Check: lint" || check.Inputs["job-image"] != images.Job {
		t.Fatalf("the components were not bound: %+v", parsed.Include)
	}

	nanolinter := `  - component: $CI_SERVER_FQDN/team/engine/nanolinter@` + strings.Repeat("a", 40)
	for _, bad := range []string{
		strings.ReplaceAll(pipeline, "nanolinter-image: $CI_LINT_IMAGE", "nanolinter-image: unreviewed:latest"),
		strings.ReplaceAll(pipeline, "nanolinter-image: $CI_LINT_IMAGE", "nanolinter-image: $CI_JOB_IMAGE"),
		strings.Replace(pipeline, "server-url: $CI_SERVER_URL", "server-url: https://elsewhere.invalid", 1),
		strings.ReplaceAll(pipeline, "team/engine/nanolinter@", "team/engine/megalinter@"),
		strings.ReplaceAll(pipeline, "team/engine/check@", "team/engine/nanolinter@"),
		strings.ReplaceAll(pipeline, nanolinter, strings.ReplaceAll(nanolinter, strings.Repeat("a", 40), "main")),
	} {
		if _, err := renderGitLabConsumer([]byte(bad), layer, images); err == nil {
			t.Fatalf("an unbindable gate was accepted:\n%s", bad)
		}
	}

	if _, err := renderGitLabConsumer([]byte(pipeline), layer, GitLabGateImages{Job: images.Job}); err == nil ||
		!strings.Contains(err.Error(), "lint-archive=") {
		t.Fatalf("a lint gate without a lint image: %v; want a refusal naming lint-archive=", err)
	}
}

// The shipped GitLab examples include a build component beside check, pinned
// by catalog release as nanolinter is; it runs forge-ci from its runtime
// image, so the trial binds that to the job image carrying the engine under
// test. A build component may run as several jobs.
func TestGitLabConsumerBindsBuildComponents(t *testing.T) {
	t.Parallel()

	pipeline := `workflow:
  rules:
    - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
include:
  - component: $CI_SERVER_FQDN/team/engine/nanolinter@0.90.0
    inputs:
      forge-ci-repository: team/engine
      server-url: $CI_SERVER_URL
      nanolinter-image: $CI_LINT_IMAGE
  - component: $CI_SERVER_FQDN/team/engine/check@` + strings.Repeat("a", 40) + `
    inputs:
      forge-ci-repository: team/engine
      server-url: $CI_SERVER_URL
      job-image: $CI_JOB_IMAGE
      commands: ['cargo test']
  - component: $CI_SERVER_FQDN/team/engine/build-cargo@0.90.0
    inputs:
      job-name: build-cli
      runtime-image: $CI_JOB_IMAGE
  - component: $CI_SERVER_FQDN/team/engine/build-cargo@0.90.0
    inputs:
      job-name: build-worker
      runtime-image: $CI_JOB_IMAGE
`
	layer := Layer{ServerURL: "https://gitlab.k3s.forgelab", Repository: "forgeci/fci-engine", Commit: strings.Repeat("b", 40)}
	images := GitLabGateImages{
		Job:  "registry.gitlab.k3s.forgelab/forgeci/fci-engine@sha256:" + strings.Repeat("c", 64),
		Lint: "registry.gitlab.k3s.forgelab/forgeci/fci-lint@sha256:" + strings.Repeat("d", 64),
	}

	rendered, err := renderGitLabConsumer([]byte(pipeline), layer, images)
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Include []struct {
			Component string         `yaml:"component"`
			Inputs    map[string]any `yaml:"inputs"`
		} `yaml:"include"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
		t.Fatal(err)
	}

	for _, index := range []int{2, 3} {
		build := parsed.Include[index]
		if build.Component != "gitlab.k3s.forgelab/forgeci/fci-engine/build-cargo@"+layer.Commit || build.Inputs["runtime-image"] != images.Job {
			t.Fatalf("build component %d was not bound: %+v", index, build)
		}
	}

	if lint := parsed.Include[0]; lint.Component != "gitlab.k3s.forgelab/forgeci/fci-engine/nanolinter@"+layer.Commit {
		t.Fatalf("version-pinned nanolinter was not bound: %+v", lint)
	}

	for _, bad := range []string{
		strings.ReplaceAll(pipeline, "check@"+strings.Repeat("a", 40), "check@0.90.0"),
		strings.Replace(pipeline, "team/engine/build-cargo@0.90.0", "other/engine/build-cargo@0.90.0", 1),
		strings.Replace(pipeline, "runtime-image: $CI_JOB_IMAGE", "runtime-image: ghcr.io/example/runtime:latest", 1),
		strings.Replace(pipeline, "build-cargo@0.90.0", "build-cargo@main", 1),
		strings.Replace(pipeline, "build-cargo@0.90.0", "build-rust@0.90.0", 1),
	} {
		if _, err := renderGitLabConsumer([]byte(bad), layer, images); err == nil {
			t.Fatalf("an unbindable gate was accepted:\n%s", bad)
		}
	}
}

// A gate may check its parts in images of their own: each check component is
// a job of its own, and $CI_JOB_IMAGE_<NAME> takes the trial's further image
// of that name. One it was not given is named in the refusal.
func TestGitLabConsumerBindsAFurtherJobImagePerCheck(t *testing.T) {
	t.Parallel()

	check := func(name, image, project string) string {
		return `  - component: $CI_SERVER_FQDN/` + project + `/check@` + strings.Repeat("a", 40) + `
    inputs:
      forge-ci-repository: ` + project + `
      server-url: $CI_SERVER_URL
      job-name: ` + name + `
      job-image: ` + image + `
      job-user: "65532:65532"
      commands:
        - make ` + name + `
`
	}
	pipeline := "workflow:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"merge_request_event\"'\ninclude:\n" +
		check("check-backend", "$CI_JOB_IMAGE", "team/engine") + check("check-frontend", "$CI_JOB_IMAGE_NODE", "team/engine")
	layer := Layer{ServerURL: "https://gitlab.k3s.forgelab", Repository: "forgeci/fci-engine", Commit: strings.Repeat("b", 40)}
	java, node := "registry.invalid/java@sha256:"+strings.Repeat("c", 64), "registry.invalid/node@sha256:"+strings.Repeat("d", 64)

	if names, err := gitlabGateExtraJobImages([]byte(pipeline)); err != nil || len(names) != 1 || names[0] != "NODE" {
		t.Fatalf("further job images = %v, %v; want [NODE]", names, err)
	}

	rendered, err := renderGitLabConsumer([]byte(pipeline), layer, GitLabGateImages{Job: java, ExtraJobs: map[string]string{"NODE": node}})
	if err != nil {
		t.Fatal(err)
	}

	var parsed struct {
		Include []struct {
			Inputs map[string]any `yaml:"inputs"`
		} `yaml:"include"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
		t.Fatal(err)
	}

	if len(parsed.Include) != 2 || parsed.Include[0].Inputs["job-image"] != java || parsed.Include[1].Inputs["job-image"] != node ||
		parsed.Include[1].Inputs["job-user"] != "65532:65532" {
		t.Fatalf("the checks did not each get their image: %+v", parsed.Include)
	}

	if _, err := renderGitLabConsumer([]byte(pipeline), layer, GitLabGateImages{Job: java}); err == nil || !strings.Contains(err.Error(), "job-archive-node=") {
		t.Fatalf("a missing further image was not refused by name: %v", err)
	}

	other := strings.Replace(pipeline, check("check-frontend", "$CI_JOB_IMAGE_NODE", "team/engine"), check("check-frontend", "$CI_JOB_IMAGE_NODE", "team/other"), 1)
	if _, err := renderGitLabConsumer([]byte(other), layer, GitLabGateImages{Job: java, ExtraJobs: map[string]string{"NODE": node}}); err == nil {
		t.Fatal("check components of two engine projects were bound")
	}

	// GitLab would merge two jobs of one name, the default check among them,
	// and run only one of the checks.
	for _, twice := range []string{
		strings.Replace(pipeline, "job-name: check-frontend", "job-name: check-backend", 1),
		strings.ReplaceAll(strings.ReplaceAll(pipeline, "      job-name: check-backend\n", ""), "      job-name: check-frontend\n", ""),
		strings.Replace(pipeline, "job-name: check-frontend", "job-name: nanolinter", 1) + `  - component: $CI_SERVER_FQDN/team/engine/nanolinter@` + strings.Repeat("a", 40) + `
    inputs:
      forge-ci-repository: team/engine
      server-url: $CI_SERVER_URL
      nanolinter-image: $CI_LINT_IMAGE
`,
	} {
		_, err := renderGitLabConsumer([]byte(twice), layer, GitLabGateImages{Job: java, Lint: node, ExtraJobs: map[string]string{"NODE": node}})
		if !errors.Is(err, errs.ErrValidation) || !strings.Contains(err.Error(), "own job-name") {
			t.Errorf("two jobs of one name were bound: %v\n%s", err, twice)
		}
	}
}

// The binder takes a component's job name, when the include names none, to
// be the component's name: each gate component's job-name input defaults to it.
func TestGitLabGateComponentsDefaultTheirJobNameToTheirName(t *testing.T) {
	t.Parallel()

	for name := range gitlabGateImageInputs {
		var template struct {
			Spec struct {
				Inputs struct {
					JobName struct {
						Default string `yaml:"default"`
					} `yaml:"job-name"`
				} `yaml:"inputs"`
			} `yaml:"spec"`
		}

		body := reporoot.ReadFile(t, "templates/"+name+".yml")

		header, _, _ := strings.Cut(string(body), "\n---")
		if err := yaml.Unmarshal([]byte(header), &template); err != nil {
			t.Fatalf("templates/%s.yml: %v", name, err)
		}

		if got := template.Spec.Inputs.JobName.Default; got != name {
			t.Errorf("templates/%s.yml: job-name defaults to %q, want %q", name, got, name)
		}
	}
}
