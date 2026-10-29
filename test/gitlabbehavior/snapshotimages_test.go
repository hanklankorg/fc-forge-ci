// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

func generateSnapshot(t *testing.T, plan string, extra ...string) (map[string]any, string, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", `set -euo pipefail; source "$1"; snapshot_gitlab_generate plan.json child.json`, "snapshot", filepath.Join(reporoot.Path(t), ".gitlab/bin/snapshot-images.sh"))
	cmd.Dir = dir
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir,
		"CI_PIPELINE_ID=42", "SNAPSHOT_LAYER_PROJECT=org/engine", "SNAPSHOT_ENGINE_SHA=" + strings.Repeat("a", 40),
		"SNAPSHOT_JOB_IMAGE=registry.example/tools@sha256:" + strings.Repeat("b", 64),
		"SNAPSHOT_BUILD_IMAGE=registry.example/build@sha256:" + strings.Repeat("c", 64),
		`SNAPSHOT_ARM64_TAGS=json:["native-arm64"]`, `SNAPSHOT_AMD64_TAGS=json:["native-amd64"]`,
	}, extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, string(out), err
	}
	body, err := os.ReadFile(filepath.Join(dir, "child.json"))
	if err != nil {
		t.Fatal(err)
	}
	var jobs map[string]any
	if err := json.Unmarshal(body, &jobs); err != nil {
		t.Fatal(err)
	}
	return jobs, string(out), nil
}

func TestSnapshotNativeChildPipelineHasWholeBuildBarrierAndBoundContext(t *testing.T) {
	jobs, output, err := generateSnapshot(t, `{"builds":[{"name":"api","arch":"amd64"},{"name":"worker","arch":"arm64"}],"images":[{"name":"api"},{"name":"worker"}]}`, "SNAPSHOT_CONTEXT_JOB=Build: snapshot dependencies")
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, output)
	}
	for _, name := range []string{"api", "worker"} {
		job := jobs["Snapshot: publish "+name].(map[string]any)
		needs := job["needs"].([]any)
		if len(needs) != 3 {
			t.Fatalf("%s must wait on plan and both builds: %#v", name, needs)
		}
		for i, want := range []string{"Snapshot: plan", "Snapshot: build api amd64", "Snapshot: build worker arm64"} {
			if got := needs[i].(map[string]any)["job"]; got != want {
				t.Fatalf("need %d = %v, want %s", i, got, want)
			}
		}
	}
	arm := jobs["Snapshot: build worker arm64"].(map[string]any)
	if arm["tags"].([]any)[0] != "native-arm64" {
		t.Fatal("arm64 did not route to native runner")
	}
	context := arm["needs"].([]any)[1].(map[string]any)
	if context["pipeline"] != "42" || context["job"] != "Build: snapshot dependencies" || context["artifacts"] != true {
		t.Fatalf("context must come from this parent pipeline: %#v", context)
	}
	include := jobs["include"].([]any)[0].(map[string]any)
	if include["ref"] != strings.Repeat("a", 40) {
		t.Fatal("child template not pinned to same engine")
	}
}

func TestSnapshotNativeEmptyPlanStillCollectsAndMissingArmRunnerRefuses(t *testing.T) {
	jobs, output, err := generateSnapshot(t, `{"builds":[],"images":[]}`, "SNAPSHOT_CONTEXT_JOB=Snapshot: plan")
	if err != nil {
		t.Fatalf("empty: %v\n%s", err, output)
	}
	if len(jobs) != 3 || jobs["Snapshot: collect"] == nil {
		t.Fatalf("empty plan should only include templates, stages and collector: %#v", jobs)
	}
	collector := jobs["Snapshot: collect"].(map[string]any)
	if len(collector["needs"].([]any)) != 1 {
		t.Fatal("empty plan must not request a nonexistent context producer")
	}
	_, output, err = generateSnapshot(t, `{"builds":[{"name":"api","arch":"arm64"}],"images":[{"name":"api"}]}`, "SNAPSHOT_ARM64_TAGS=[]")
	if err == nil || !strings.Contains(output, "native arm64 runner") {
		t.Fatalf("missing arm runner must refuse: %v\n%s", err, output)
	}
}

func TestSnapshotNativeAutomaticArtifactsUseToolchainFamiliesAndCompleteGatherBarrier(t *testing.T) {
	goImage := "registry.example/go@sha256:" + strings.Repeat("1", 64)
	javaImage := "registry.example/java@sha256:" + strings.Repeat("2", 64)
	nodeImage := "registry.example/node@sha256:" + strings.Repeat("3", 64)
	rustImage := "registry.example/rust@sha256:" + strings.Repeat("4", 64)
	jobs, output, err := generateSnapshot(t, `{
	  "artifacts":[{"name":"binary","project_type":"go"},{"name":"crate","project_type":"cargo"},{"name":"jar","project_type":"maven"},{"name":"gradle","project_type":"gradle"},{"name":"web","project_type":"npm"}],
	  "builds":[{"name":"api","arch":"amd64"},{"name":"worker","arch":"amd64"}],
	  "images":[{"name":"api"},{"name":"worker"}]
	}`, "SNAPSHOT_ARTIFACT_IMAGE_GO="+goImage, "SNAPSHOT_ARTIFACT_IMAGE_JAVA="+javaImage, "SNAPSHOT_ARTIFACT_IMAGE_NODE="+nodeImage, "SNAPSHOT_ARTIFACT_IMAGE_RUST="+rustImage)
	if err != nil {
		t.Fatalf("automatic graph: %v\n%s", err, output)
	}
	for name, image := range map[string]string{"binary": goImage, "crate": rustImage, "jar": javaImage, "gradle": javaImage, "web": nodeImage} {
		producer := jobs["Snapshot: artifact "+name].(map[string]any)
		if producer["image"].(map[string]any)["name"] != image || producer["tags"].([]any)[0] != "native-amd64" {
			t.Fatalf("producer %s did not select its trusted native toolchain: %#v", name, producer)
		}
		needs := producer["needs"].([]any)
		if len(needs) != 1 || needs[0].(map[string]any)["pipeline"] != "42" || needs[0].(map[string]any)["job"] != "Snapshot: plan" {
			t.Fatalf("producer %s lost exact parent plan: %#v", name, needs)
		}
	}
	context := jobs["Snapshot: context"].(map[string]any)
	if got := len(context["needs"].([]any)); got != 6 {
		t.Fatalf("gather must require plan and ALL 5 unique producers; got %d", got)
	}
	for _, image := range []string{"api", "worker"} {
		build := jobs["Snapshot: build "+image+" amd64"].(map[string]any)
		need := build["needs"].([]any)[1].(map[string]any)
		if need["job"] != "Snapshot: context" || need["artifacts"] != true {
			t.Fatalf("missing/failed producer could bypass gather for %s: %#v", image, need)
		}
		if _, ok := build["when"]; ok {
			t.Fatal("image build must retain success-only dependency scheduling")
		}
	}
	if len(jobs) != 13 { // include, stages, 5 producers, gather, 2 builds, 2 publishers, collector
		t.Fatalf("unexpected duplicate artifact builds: %d jobs/keys", len(jobs))
	}
}

func TestSnapshotNativeContainerFirstStillGathersAnEmptyBoundContext(t *testing.T) {
	jobs, output, err := generateSnapshot(t, `{"artifacts":[],"builds":[{"name":"api","arch":"amd64"}],"images":[{"name":"api"}]}`)
	if err != nil {
		t.Fatalf("container-first: %v\n%s", err, output)
	}
	context := jobs["Snapshot: context"].(map[string]any)
	if len(context["needs"].([]any)) != 1 {
		t.Fatal("container-first context must need only the frozen plan")
	}
}

func TestSnapshotNativeGatherUsesFrozenSourceAndDoesNotPublishAfterFailure(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "missing-producer"}[refused], func(t *testing.T) {
			dir := t.TempDir()
			sha := strings.Repeat("a", 40)
			if err := os.WriteFile(filepath.Join(dir, "snapshot-plan.json"), []byte(`{"source_sha":"`+sha+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			program := `set -euo pipefail
source "$1/.forgejo/bin/snapshot-images.sh"
source "$1/.gitlab/bin/snapshot-images.sh"
snapshot_checkout() {
  test "$1" = "$EXPECTED_SHA"
  test ! -e snapshot-source
  mkdir snapshot-source
}
snapshot_engine() {
  if [ "$3" = read-plan ]; then printf '%s\n' "$EXPECTED_SHA"; return; fi
  test "$PWD" = "$TEST_ROOT/snapshot-source"
  test "$1 $2 $3" = "container snapshot-images gather-context"
  test "$6" = --input-dir && test "$7" = "$TEST_ROOT/_snapshot-artifacts"
  if [ "$REFUSE" = true ]; then printf 'missing-producer\n'; return 78; fi
  mkdir snapshot-context
  printf 'verified-bundle\n' > snapshot-context/source-sha
}
snapshot_gitlab_gather
test -f snapshot-context/source-sha
test ! -e snapshot-source/snapshot-context
printf 'ready-for-images\n'
`
			cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", program, "snapshot", reporoot.Path(t))
			cmd.Dir = dir
			value := "false"
			if refused {
				value = "true"
			}
			cmd.Env = append(os.Environ(), "TEST_ROOT="+dir, "EXPECTED_SHA="+sha, "REFUSE="+value)
			output, err := cmd.CombinedOutput()
			if refused {
				if err == nil || strings.TrimSpace(string(output)) != "missing-producer" {
					t.Fatalf("failed gather reached image stage: %v\n%s", err, output)
				}
			} else if err != nil || strings.TrimSpace(string(output)) != "ready-for-images" {
				t.Fatalf("frozen gather/transport: %v\n%s", err, output)
			}
		})
	}
}

func TestSnapshotGitLabPolicyDoesNotEnterReleasePath(t *testing.T) {
	decoder := yaml.NewDecoder(bytes.NewReader(reporoot.ReadFile(t, "templates/snapshot-images.yml")))
	var spec struct {
		Spec struct {
			Inputs map[string]struct{ Default any } `yaml:"inputs"`
		} `yaml:"spec"`
	}
	if err := decoder.Decode(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.Spec.Inputs["publish-containers"].Default != true {
		t.Fatal("snapshot containers must default on")
	}
	var jobs map[string]any
	if err := decoder.Decode(&jobs); err != nil {
		t.Fatal(err)
	}
	execute := jobs["Snapshot: execute"].(map[string]any)
	if execute["stage"] == ".post" {
		t.Fatal("a standalone snapshot component cannot consist solely of .pre/.post jobs")
	}
	need := execute["needs"].([]any)[0].(map[string]any)
	if need["job"] != "$[[ inputs.context-job ]]" {
		t.Fatal("child creation must wait for the selected context producer, not only the plan")
	}
	body := string(reporoot.ReadFile(t, "templates/snapshot-image-jobs.yml"))
	if strings.Contains(body, "release-request/") || strings.Contains(body, "RELEASE_COSIGN") || strings.Contains(body, "release-images promote") {
		t.Fatal("snapshot jobs must not enter release authorization/signing/promotion")
	}
	if !strings.Contains(body, "CI_MERGE_REQUEST_IID") || !strings.Contains(body, "snapshot/registry") {
		t.Fatal("native MR refusal or scoped publisher missing")
	}
}

func TestSnapshotCheckoutUsesPinnedGitLabEventPolicyBeforeTransport(t *testing.T) {
	for _, refuse := range []string{"false", "true"} {
		t.Run(refuse, func(t *testing.T) {
			program := `set -euo pipefail
source "$1/.forgejo/bin/snapshot-images.sh"
run_forge_ci() {
  if [ "$1 $2" = 'validate event-context' ]; then
    test "$#" = 6
    test "$3" = --event-name && test -z "$4"
    test "$5" = --allowed-events
    test "$6" = push,workflow_dispatch,api,schedule,parent_pipeline
    if [ "$REFUSE" = true ]; then return 78; fi
  else
    test "$1 $2" = 'platform checkout'
    printf 'checkout\n'
  fi
}
snapshot_checkout refs/heads/development
`
			cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", program, "snapshot", reporoot.Path(t))
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "FORGE_CI_PROVIDER=gitlab", "REFUSE=" + refuse,
				"ALLOWED_EVENTS=pull_request,trigger,pipeline", "GITHUB_EVENT_NAME=push",
				"SNAPSHOT_REPOSITORY=org/app", "SNAPSHOT_SERVER=https://gitlab.example"}
			out, err := cmd.CombinedOutput()
			if refuse == "true" {
				if err == nil || len(out) != 0 {
					t.Fatalf("refused event reached checkout: %v %s", err, out)
				}
			} else if err != nil || string(out) != "checkout\n" {
				t.Fatalf("admitted event failed transport: %v %s", err, out)
			}
		})
	}
}
