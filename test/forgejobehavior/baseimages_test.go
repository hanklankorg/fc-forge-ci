// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	baseStageParent = `{"ref":"registry.example/owner/base@sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `","input_id":"` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `"}`
	baseStageRef    = "registry.example/owner/base@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// The plan step names the stage by the parent input alone: a root runs the
// root-base verb with no parent, a child the child-base verb with the
// caller's parent. The step decides no architecture itself: every runner
// input reaches the engine's admission as the caller passed it, empty or not.
func TestBaseImagesWorkflowPlansTheStageItsParentNames(t *testing.T) {
	t.Parallel()

	run := workflowStep(t, "base-images.yml", "plan", "Admit the base plan").Run

	for _, tc := range []struct {
		name, parent, amd64, arm64, stage string
	}{
		{name: "root", amd64: `["builder"]`, stage: "root-base"},
		{name: "child", parent: baseStageParent, amd64: `["builder", "x64"]`, arm64: `["builder-arm"]`, stage: "child-base"},
		{name: "no build runner", arm64: "[]", stage: "root-base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "base-plan")
			got := box.run(run, "HELPERS_DIR="+box.root, "GITHUB_WORKSPACE="+box.root, "RUNNER_TEMP="+box.root,
				"IMAGE_PLAN_JSON={\"builds\":[]}", "PARENT_BASE_JSON="+tc.parent, "IMAGE_REPOSITORY=registry.example/owner/base",
				"RELEASE_SHA=cccc", "FORGEJO_SERVER=https://forge.example", "FORGEJO_REPO=owner/app",
				`RUNS_ON=["ordinary"]`, "BUILD_RUNS_ON_LINUX_AMD64="+tc.amd64, "BUILD_RUNS_ON_LINUX_ARM64="+tc.arm64)
			argv := box.argv()

			if got.status != 0 || len(argv) < 4 || argv[2] != tc.stage || argv[3] != "plan" {
				t.Fatalf("status=%d argv=%v stderr=%s", got.status, argv, got.stderr)
			}

			var inputs []string

			for at, arg := range argv {
				if arg == "--runner-input" && at+1 < len(argv) {
					inputs = append(inputs, argv[at+1])
				}
			}

			want := []string{`runs-on=["ordinary"]`, "build-runs-on-linux-amd64=" + tc.amd64, "build-runs-on-linux-arm64=" + tc.arm64}
			if !slices.Equal(inputs, want) || slices.Contains(argv, "--runner-archs") {
				t.Fatalf("runner inputs: argv=%v, want %v", argv, want)
			}

			at := slices.Index(argv, "--parent-base-json")
			if (at >= 0) != (tc.parent != "") || (at >= 0 && argv[at+1] != tc.parent) {
				t.Fatalf("parent: argv=%v, want %q", argv, tc.parent)
			}
		})
	}
}

// Base images take the administrator's vulnerability setting exactly as
// release images do, and the publication of a root and of a child passes it
// to the engine: without it the gate could only report.
func TestBaseImagesWorkflowPassesTheVulnerabilitySettingToPublication(t *testing.T) {
	t.Parallel()

	type inputs struct {
		On struct {
			WorkflowCall struct {
				Inputs map[string]map[string]any `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
	}

	var base, release inputs

	loadYAML(t, ".forgejo/workflows/base-images.yml", &base)
	loadYAML(t, ".forgejo/workflows/release-images.yml", &release)

	const setting = "enforce-vulnerabilities"
	if want, got := release.On.WorkflowCall.Inputs[setting], base.On.WorkflowCall.Inputs[setting]; want == nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("base-images.yml declares %s as %v, release-images.yml as %v", setting, got, want)
	}

	publish := workflowStep(t, "base-images.yml", "publish", "Scan and publish the unsigned candidate")
	if publish.Env["BASE_IMAGES_ADMIN_VULNERABILITIES"] != "${{ inputs['enforce-vulnerabilities'] }}" {
		t.Fatalf("the publication step does not read the input: %v", publish.Env)
	}

	for stage, parent := range map[string]string{"root-base": "", "child-base": baseStageParent} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "base-publish")
			box.write(".engine/bin/forge-ci", `#!/usr/bin/env bash
set -euo pipefail
case "$2" in
login) cat >/dev/null ;;
base-images)
  printf '%s\0' "$@" >"${FAKE_ARGV_LOG:?}"
  exit 65
  ;;
*) exit 99 ;;
esac
`, 0o700)

			if err := os.Mkdir(filepath.Join(box.root, "source"), 0o700); err != nil {
				t.Fatal(err)
			}

			env := []string{
				"HELPERS_DIR=" + box.root, "GITHUB_WORKSPACE=" + box.root, "RUNNER_TEMP=" + box.root,
				"IMAGE_PLAN_JSON={}", "PARENT_BASE_JSON=" + parent, "IMAGE_REPOSITORY=registry.example/owner/base", "RELEASE_SHA=cccc",
				"REGISTRY_TOKEN=fixture-token", "REGISTRY_USER=", "FORGEJO_SERVER=https://forge.example", "FORGEJO_REPO=owner/app",
			}

			if got := box.run(publish.Run, env...); got.status == 0 || box.argv() != nil {
				t.Fatalf("publication ran without the setting: status=%d argv=%v", got.status, box.argv())
			}

			got := box.run(publish.Run, slices.Concat(env, []string{"BASE_IMAGES_ADMIN_VULNERABILITIES=true"})...)
			argv := box.argv()

			if got.status != 65 || len(argv) < 4 || argv[2] != stage || argv[3] != "publish" || !slices.Contains(argv, "--enforce-vulnerabilities=true") {
				t.Fatalf("status=%d argv=%v stderr=%s", got.status, argv, got.stderr)
			}
		})
	}
}

// With expected-ref the verification step authenticates exactly one base and
// hands on only what the engine printed. It never falls back to discovery,
// and it refuses to run without the registry token.
func TestBaseVerificationWorkflowAuthenticatesOneExactParent(t *testing.T) {
	t.Parallel()

	run := workflowStep(t, "verify-base-container-images.yml", "verify-base-container-images", "Verify existing base images").Run

	for _, mode := range []string{"authenticated", "refused", "no-token"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			box := newSandbox(t, "base-parent")
			box.write(".engine/bin/forge-ci", `#!/usr/bin/env bash
set -euo pipefail
test -z "${REGISTRY_TOKEN:-}"
case "$2" in
login)
  cat >/dev/null
  while (($#)); do if [ "$1" = --auth-file ]; then printf fixture-auth >"$2"; fi; shift; done
  ;;
base-images)
  printf '%s\0' "$@" >"${FAKE_ARGV_LOG:?}"
  test "$MODE" != refused || exit 65
  printf '%s\n' "$PARENT"
  ;;
*) exit 99 ;;
esac
`, 0o700)

			for _, dir := range []string{"private", "consumer"} {
				if err := os.Mkdir(filepath.Join(box.root, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			token := "fixture-token"
			if mode == "no-token" {
				token = ""
			}

			inputs := `[{"flavor":"root","base_input_id":"` + strings.Repeat("b", 64) + `"}]`
			got := box.run(run, "HELPERS_DIR="+box.root, "SIGNER_WORKSPACE="+filepath.Join(box.root, "private"),
				"CONSUMER_WORKSPACE="+filepath.Join(box.root, "consumer"), "MODE="+mode, "PARENT="+baseStageParent,
				"TRUSTED_KEY_INPUT=fixture-key", "TRUSTED_KEY_SHA256_INPUT=fixture-digest",
				"EXPECTED_REPOSITORY=registry.example/owner/base", "FORGEJO_SERVER=https://forge.example", "FORGEJO_REPO=owner/app",
				"REGISTRY_TOKEN="+token, "REGISTRY_USER=", "EXPECTED_REF="+baseStageRef, "BASE_INPUTS_JSON="+inputs)
			argv, output := box.argv(), box.output()

			if mode != "authenticated" {
				if got.status == 0 || strings.Contains(output, "parent_base_json") {
					t.Fatalf("mode=%s status=%d outputs=%q", mode, got.status, output)
				}

				if mode == "no-token" && argv != nil {
					t.Fatalf("authentication ran without a registry token: %v", argv)
				}

				return
			}

			if got.status != 0 || output != "parent_base_json="+baseStageParent+"\n" {
				t.Fatalf("status=%d outputs=%q stderr=%s", got.status, output, got.stderr)
			}

			if len(argv) < 3 || argv[2] != "authenticate-parent" || !slices.Contains(argv, "--auth-file") {
				t.Fatalf("argv=%v", argv)
			}

			for flag, want := range map[string]string{"--expected-ref": baseStageRef, "--base-inputs-json": inputs} {
				if at := slices.Index(argv, flag); at < 0 || argv[at+1] != want {
					t.Fatalf("%s: argv=%v", flag, argv)
				}
			}
		})
	}
}
