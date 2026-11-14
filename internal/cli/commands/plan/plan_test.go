// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package plan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/cli"
	plancmd "github.com/diggsweden/forge-ci/internal/cli/commands/plan"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/domain/projecttype"
	domainrelease "github.com/diggsweden/forge-ci/internal/domain/release"
	"github.com/diggsweden/forge-ci/internal/testutil/ghaenv"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
)

func TestLocalPlanComposition_ExtractsTheConfigEnvelope(t *testing.T) {
	env := testenv.New(t)

	path := env.Path("artifacts.yml")
	if err := os.WriteFile(path, []byte("artifacts:\n  - name: cli\n    project-type: go\n    config:\n      build-mode: artifact-first\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	producer := cli.New(cli.BuildInfo{})
	producer.Writer, producer.ErrWriter = &stdout, &stderr

	if err := producer.Run(t.Context(), []string{"forge-ci", "--provider", "local", "--runner", "local", "--json", "config", "parse-artifacts", "--file", path}); err != nil {
		t.Fatal(err)
	}

	var configOutput map[string]string

	if err := json.Unmarshal(stdout.Bytes(), &configOutput); err != nil {
		t.Fatalf("config output is not one JSON object: %s: %v", stdout.String(), err)
	}

	configPlan := configOutput["config-plan-json"]
	if _, err := pipeline.DecodeConfigPlan(configPlan); err != nil {
		t.Fatalf("the extracted config-plan-json string must be consumable: %v", err)
	}

	for _, command := range []struct {
		name string
		args []string
		key  string
	}{
		{"release", []string{"--tag", "v1.2.3", "--branch", "main", "--repository-visibility", "public"}, "release-plan-json"},
		{"snapshot-release", []string{"--project-type", "go", "--branch", "main"}, "snapshot-release-plan-json"},
	} {
		t.Run(command.name, func(t *testing.T) {
			env.Setenv("CONFIG_PLAN_JSON", configPlan)
			stdout.Reset()

			consumer := cli.New(cli.BuildInfo{})
			consumer.Writer, consumer.ErrWriter = &stdout, &stderr

			args := append([]string{"forge-ci", "--provider", "local", "--runner", "local", "--json", "plan", command.name}, command.args...)
			if err := consumer.Run(t.Context(), args); err != nil {
				t.Fatal(err)
			}

			var outputs map[string]string

			if err := json.Unmarshal(stdout.Bytes(), &outputs); err != nil {
				t.Fatalf("plan output is not JSON: %s: %v", stdout.String(), err)
			}

			if !json.Valid([]byte(outputs[command.key])) {
				t.Fatalf("missing typed %s result: %s", command.key, stdout.String())
			}
		})
	}
}

func TestReleaseCmd_UsesConfigPlanAndEmitsTypedPlans(t *testing.T) {
	env := ghaenv.Setup(t)

	cfg := &config.Config{
		Sign: config.SignConfig{Method: domainrelease.SignMethodSigstore, Transparency: domainrelease.TransparencyPublic},
		Artifacts: []config.Artifact{
			{Name: "web", ProjectType: projecttype.NPM, PublishTo: []config.PublishTarget{config.PublishForgePackages}, NPM: &config.NPMConfig{Tag: "release-{version}"}},
			{Name: "go-cli", ProjectType: projecttype.Go, Go: &config.GoConfig{BuildMode: config.GoBuildModeArtifactFirst}},
		},
		Containers: []config.Container{{Name: "image"}},
	}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	env.Setenv("CONFIG_PLAN_JSON", mustConfigPlanJSON(t, pipeline.NewConfigPlan(cfg)))
	env.Setenv("GITHUB_REF_NAME", "v1.2.3")
	env.Setenv("BRANCH", "main")
	env.Setenv("RELEASE_PUBLISHER", "github-cli")
	env.Setenv("RELEASE_SBOMS", "all")
	env.Setenv("RELEASE_SIGN_ARTIFACTS", "true")
	env.Setenv("CHANGELOG_CREATOR", "git-cliff")

	cmd := plancmd.New()
	if err := cmd.Run(context.Background(), []string{"plan", "release",
		"--runner-input", `build-runs-on-linux-amd64=["amd64"]`, "--runner-input", `build-runs-on-linux-arm64=["arm64"]`}); err != nil {
		t.Fatal(err)
	}

	if got := env.Output("release-plan-json"); !strings.Contains(got, `"version":1`) || !strings.Contains(got, `"make_latest":true`) {
		t.Errorf("release-plan-json = %s", got)
	}

	if got := env.Output("release-policy-json"); got != "" {
		t.Errorf("release-policy-json compatibility output should not be emitted, got %s", got)
	}

	if got := env.Output("build-stage-plan-json"); !strings.Contains(got, `"stage":"build"`) || !strings.Contains(got, `"go":{"runs":true`) {
		t.Errorf("build-stage-plan-json = %s", got)
	}

	if got := env.Output("publish-stage-plan-json"); !strings.Contains(got, `"containers":{"runs":true`) {
		t.Errorf("publish-stage-plan-json = %s", got)
	}
}

func TestSnapshotReleaseCmd_UsesConfigPlanFallbackAndEmitsStagePlans(t *testing.T) {
	env := ghaenv.Setup(t)

	// Derived like every real config plan: the plan contract requires the
	// defaults Derive fills in (sboms among them), and this fixture skipped it.
	cfg := &config.Config{
		Artifacts: []config.Artifact{
			{Name: "web", ProjectType: projecttype.NPM},
			{Name: "worker", ProjectType: projecttype.Go, Go: &config.GoConfig{BuildMode: config.GoBuildModeArtifactFirst}},
		},
		Containers: []config.Container{{Name: "image"}},
	}
	if err := config.Derive(cfg); err != nil {
		t.Fatal(err)
	}

	env.Setenv("CONFIG_PLAN_JSON", mustConfigPlanJSON(t, pipeline.NewConfigPlan(cfg)))
	env.Setenv("BRANCH", "feature/dev-plan")
	env.Setenv("PUBLISH_NPM", "true")
	env.Setenv("USE_CI_TOKEN", "true")
	env.Setenv("WORKING_DIRECTORY", "svc/api")

	cmd := plancmd.New()
	if err := cmd.Run(context.Background(), []string{"plan", "snapshot-release"}); err != nil {
		t.Fatal(err)
	}

	if got := env.Output("snapshot-release-plan-json"); !strings.Contains(got, `"branch":"feature/dev-plan"`) {
		t.Errorf("snapshot-release-plan-json = %s", got)
	}

	// Regression: the --working-dir flag was declared but read under a
	// different name, so the plan silently recorded ".".
	if got := env.Output("snapshot-release-plan-json"); !strings.Contains(got, `"working_directory":"svc/api"`) {
		t.Errorf("working-dir flag not wired into the plan: %s", got)
	}

	if got := env.Output("dev-context-json"); got != "" {
		t.Errorf("dev-context-json compatibility output should not be emitted, got %s", got)
	}

	if got := env.Output("snapshot-build-stage-plan-json"); !strings.Contains(got, `"stage":"dev-build"`) || !strings.Contains(got, `"go":{"runs":true`) {
		t.Errorf("snapshot-build-stage-plan-json = %s", got)
	}

	// The snapshot flow builds no containers — there is no containers target in
	// the publish plan; it is promoted to :dev on the release path by the
	// build-once/promote-many ladder.
	if got := env.Output("snapshot-publish-stage-plan-json"); strings.Contains(got, `"containers"`) {
		t.Errorf("snapshot-publish-stage-plan-json = %s", got)
	}
}

func TestPRCmd_EmitsPlanAndQualityStagePlan(t *testing.T) {
	env := ghaenv.Setup(t)
	env.Setenv("PROJECT_TYPE", "go")
	env.Setenv("LINT_ENGINE", "nanolinter")
	env.Setenv("NANOLINTER_IMAGE", prNanolinterImage)

	cmd := plancmd.New()
	if err := cmd.Run(context.Background(), []string{"plan", "pr"}); err != nil {
		t.Fatal(err)
	}

	if got := env.Output("pr-plan-json"); !strings.Contains(got, `"project_type":"go"`) || !strings.Contains(got, `"engine":"nanolinter"`) {
		t.Errorf("pr-plan-json = %s", got)
	}

	if got := env.Output("quality-stage-plan-json"); !strings.Contains(got, `"stage":"pr-quality"`) || !strings.Contains(got, `"nanolinter":{"runs":true`) {
		t.Errorf("quality-stage-plan-json = %s", got)
	}

	if got := env.Output("pr-plan-json"); !strings.Contains(got, `"nanolinter_image":"`+prNanolinterImage+`"`) {
		t.Errorf("pr-plan-json lacks the admitted image: %s", got)
	}
}

// prNanolinterImage is a nanolinter image pinned by digest.
var prNanolinterImage = "registry.example/nanolinter:go@sha256:" + strings.Repeat("b", 64)

func TestPRCmd_DisabledEngineWarns(t *testing.T) {
	// Serial: the workflow environment and process logger are test-owned.
	env := ghaenv.Setup(t)
	env.Setenv("PROJECT_TYPE", "go")
	env.Setenv("LINT_ENGINE", "none")

	var logs bytes.Buffer

	previous := slog.Default()

	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if err := plancmd.New().Run(t.Context(), []string{"plan", "pr"}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "no supported lint engine is enabled") {
		t.Fatalf("normal PR planning did not warn: %s", logs.String())
	}

	if got := env.Output("quality-stage-plan-json"); !strings.Contains(got, `"nanolinter":{"runs":false`) ||
		!strings.Contains(got, `"megalinter":{"runs":false`) {
		t.Errorf("disabled engines unexpectedly scheduled: %s", got)
	}
}

// The plan job refuses a nanolinter engine without a digest-pinned image, so
// no lint job runs a tag; another engine needs none.
func TestPRCmd_RefusesAnUnpinnedNanolinterImage(t *testing.T) {
	for _, image := range []string{"", "registry.example/nanolinter:java"} {
		env := ghaenv.Setup(t)
		env.Setenv("PROJECT_TYPE", "maven")
		env.Setenv("LINT_ENGINE", "nanolinter")
		env.Setenv("NANOLINTER_IMAGE", image)

		err := plancmd.New().Run(context.Background(), []string{"plan", "pr"})
		if err == nil || !strings.Contains(err.Error(), "ghcr.io/itiquette/nanolinter:java@sha256:<digest>") {
			t.Errorf("NANOLINTER_IMAGE=%q: %v, want a refusal naming the java flavour", image, err)
		}

		if got := env.Output("pr-plan-json"); got != "" {
			t.Errorf("NANOLINTER_IMAGE=%q emitted a plan: %s", image, got)
		}
	}

	env := ghaenv.Setup(t)
	env.Setenv("PROJECT_TYPE", "maven")
	env.Setenv("LINT_ENGINE", "megalinter")

	if err := plancmd.New().Run(context.Background(), []string{"plan", "pr"}); err != nil {
		t.Fatalf("megalinter needs no nanolinter image: %v", err)
	}
}

func TestGitlabPipelineCmds_RejectStalePlanVersion(t *testing.T) {
	env := ghaenv.Setup(t)
	env.Setenv("BUILD_STAGE_PLAN_JSON", `{"version":0,"stage":"build","targets":{}}`)
	env.Setenv("PUBLISH_STAGE_PLAN_JSON", `{"version":0,"stage":"publish","targets":{}}`)

	for _, tc := range []struct {
		verb string
		want string
	}{
		{verb: "gitlab-build-pipeline", want: "unsupported build-stage plan version"},
		{verb: "gitlab-publish-pipeline", want: "unsupported publish-stage plan version"},
	} {
		err := plancmd.New().Run(context.Background(), []string{
			"plan", tc.verb,
			"--component-base", "catalog.example/group/components",
			"--component-ref", "1.0.0",
		})
		// A plan from a different build of this tool is skew, not operator
		// misuse: ErrInvalidConfig, so the exit code points at the pipeline
		// rather than at the command line.
		if !errors.Is(err, errs.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", tc.verb, err)

			continue
		}

		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to name the stale version", tc.verb, err)
		}
	}
}

func TestGitlabPipelineCmds_RequireExplicitComponentCoordinate(t *testing.T) {
	env := ghaenv.Setup(t)
	env.Setenv("BUILD_STAGE_PLAN_JSON", `{"version":1,"stage":"build","targets":{}}`)
	env.Setenv("PUBLISH_STAGE_PLAN_JSON", `{"version":1,"stage":"publish","targets":{}}`)

	for _, tc := range []struct {
		verb string
		args []string
		want string
	}{
		{verb: "gitlab-build-pipeline", args: []string{"--component-ref", "1.0.0"}, want: `Required flag "component-base" not set`},
		{verb: "gitlab-publish-pipeline", args: []string{"--component-ref", "1.0.0"}, want: `Required flag "component-base" not set`},
		{verb: "gitlab-build-pipeline", args: []string{"--component-base", "catalog.example/components"}, want: `Required flag "component-ref" not set`},
		{verb: "gitlab-publish-pipeline", args: []string{"--component-base", "catalog.example/components"}, want: `Required flag "component-ref" not set`},
	} {
		err := plancmd.New().Run(context.Background(), append([]string{"plan", tc.verb}, tc.args...))
		if err == nil {
			t.Fatalf("%s accepted an implicit component boundary", tc.verb)
		}

		// --component-base is Required, so this is urfave's own refusal; it
		// carries no sentinel until main.go classifies it, which is the exit
		// code that actually reaches the operator.
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.verb, err, tc.want)
		}

		if got := errs.ExitCodeFromError(cli.ClassifyError(err)); got != errs.ExitCodeUsage {
			t.Errorf("%s: exit code = %d, want usage (%d)", tc.verb, got, errs.ExitCodeUsage)
		}
	}
}

func TestGitlabPipelineCmds_RejectUnknownAndTrailingContractJSON(t *testing.T) {
	env := ghaenv.Setup(t)

	for _, tc := range []struct {
		name string
		verb string
		env  string
		raw  string
	}{
		{name: "build unknown", verb: "gitlab-build-pipeline", env: "BUILD_STAGE_PLAN_JSON", raw: `{"version":1,"stage":"build","future_semantic":true,"targets":{}}`},
		{name: "build trailing", verb: "gitlab-build-pipeline", env: "BUILD_STAGE_PLAN_JSON", raw: `{"version":1,"stage":"build","targets":{}} {}`},
		{name: "publish unknown", verb: "gitlab-publish-pipeline", env: "PUBLISH_STAGE_PLAN_JSON", raw: `{"version":1,"stage":"publish","targets":{},"future_semantic":true}`},
		{name: "publish trailing", verb: "gitlab-publish-pipeline", env: "PUBLISH_STAGE_PLAN_JSON", raw: `{"version":1,"stage":"publish","targets":{}} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.Setenv(tc.env, tc.raw)

			out := filepath.Join(t.TempDir(), "child.yml")

			err := plancmd.New().Run(context.Background(), []string{
				"plan", tc.verb,
				"--component-base", "catalog.example/components",
				"--component-ref", "1.0.0",
				"--output", out,
			})
			if !errors.Is(err, errs.ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}

			if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
				t.Fatalf("invalid contract mutated output: %v", statErr)
			}
		})
	}
}

func TestGitlabPipelineCmd_DoesNotWriteUnsupportedPlan(t *testing.T) {
	env := ghaenv.Setup(t)
	env.Setenv("BUILD_STAGE_PLAN_JSON", `{"version":1,"stage":"build","targets":{"xcode_ios":{"runs":true,"items":[{"name":"ios"}]}}}`)

	out := filepath.Join(t.TempDir(), "child.yml")

	err := plancmd.New().Run(context.Background(), []string{
		"plan", "gitlab-build-pipeline",
		"--component-base", "catalog.example/group/components",
		"--component-ref", "1.0.0",
		"--output", out,
	})
	// An unsupported target in an otherwise well-formed plan is configuration
	// the generator cannot honour, not a bad flag.
	if !errors.Is(err, errs.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}

	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("unsupported plan mutated output: %v", statErr)
	}
}

func TestGitlabPipelineCmds_DoNotWriteZeroJobPipeline(t *testing.T) {
	env := ghaenv.Setup(t)

	for _, tc := range []struct {
		verb    string
		envName string
		stage   string
	}{
		{verb: "gitlab-build-pipeline", envName: "BUILD_STAGE_PLAN_JSON", stage: "build"},
		{verb: "gitlab-publish-pipeline", envName: "PUBLISH_STAGE_PLAN_JSON", stage: "publish"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			env.Setenv(tc.envName, `{"version":1,"stage":"`+tc.stage+`","targets":{}}`)

			out := filepath.Join(t.TempDir(), "child.yml")

			err := plancmd.New().Run(context.Background(), []string{
				"plan", tc.verb,
				"--component-base", "catalog.example/components",
				"--component-ref", "1.0.0",
				"--output", out,
			})
			if !errors.Is(err, errs.ErrInvalidConfig) || !strings.Contains(err.Error(), "no generated jobs") {
				t.Fatalf("err = %v, want ErrInvalidConfig naming the zero-job pipeline", err)
			}

			if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
				t.Fatalf("zero-job plan mutated output: %v", statErr)
			}
		})
	}
}

func mustConfigPlanJSON(t *testing.T, plan pipeline.ConfigPlan) string {
	t.Helper()

	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

// plan release reads each --runner-input whole, commas and all, against the
// detected forge, and refuses a planned platform whose input has no value.
func TestPlanRelease_RefusesAPlatformWithoutARunnerInput(t *testing.T) {
	env := testenv.New(t)

	path := env.Path("artifacts.yml")

	declaration := "artifacts:\n  - name: svc\n    project-type: go\n    config:\n      build-mode: container-first\n" +
		"containers:\n  - name: api\n    from: [svc]\n    platforms: linux/amd64,linux/arm64\n    enable-slsa: false\n"
	if err := os.WriteFile(path, []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	producer := cli.New(cli.BuildInfo{})
	producer.Writer, producer.ErrWriter = &stdout, &stderr

	if err := producer.Run(t.Context(), []string{"forge-ci", "--provider", "local", "--runner", "local", "--json", "config", "parse-artifacts", "--file", path}); err != nil {
		t.Fatal(err)
	}

	var configOutput map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &configOutput); err != nil {
		t.Fatal(err)
	}

	env.Setenv("CONFIG_PLAN_JSON", configOutput["config-plan-json"])

	run := func(command, forge string, inputs ...string) error {
		stdout.Reset()

		consumer := cli.New(cli.BuildInfo{})
		consumer.Writer, consumer.ErrWriter = &stdout, &stderr

		args := make([]string, 0, 16+2*len(inputs))
		args = append(args, "forge-ci", "--provider", forge, "--runner", "local", "--json", "plan", command, "--branch", "main")

		if command == "release" {
			args = append(args, "--tag", "v1.2.3", "--repository-visibility", "public", "--release-sboms", "none")
		} else {
			args = append(args, "--sboms", "none")
		}

		for _, input := range inputs {
			args = append(args, "--runner-input", input)
		}

		return consumer.Run(t.Context(), args)
	}

	for _, command := range []string{"release", "snapshot-release"} {
		for _, forge := range []string{"forgejo", "github"} {
			t.Run(command+"/"+forge, func(t *testing.T) {
				for _, missing := range []string{"", "build-runs-on-linux-arm64=", "build-runs-on-linux-arm64=[]"} {
					inputs := []string{`build-runs-on-linux-amd64=["self-hosted","amd64"]`, `runs-on=["ordinary","arm64"]`}
					if missing != "" {
						inputs = append(inputs, missing)
					}

					err := run(command, forge, inputs...)
					if !errors.Is(err, errs.ErrUsage) || !strings.Contains(err.Error(), forge+" runner input build-runs-on-linux-arm64 has no value") {
						t.Fatalf("err = %v, want the arm64 refusal", err)
					}

					if stdout.Len() != 0 {
						t.Fatalf("refused plan emitted outputs: %s", stdout.String())
					}
				}

				// A release plan is never made unchecked on a forge; a
				// snapshot-release plan still is when the layer passes none.
				if err := run(command, forge); (command == "release") != errors.Is(err, errs.ErrUsage) ||
					(command == "release" && !strings.Contains(err.Error(), "a release plan needs the layer's runner inputs")) {
					t.Fatalf("no runner input: err = %v", err)
				}

				if err := run(command, forge, `build-runs-on-linux-amd64=["self-hosted","amd64"]`, `build-runs-on-linux-arm64=["self-hosted","arm64"]`); err != nil {
					t.Fatalf("both runners passed: %v", err)
				}
			})
		}
	}
}
