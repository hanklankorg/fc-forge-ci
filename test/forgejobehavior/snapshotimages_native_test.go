// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejobehavior

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"gopkg.in/yaml.v3"
)

func TestSnapshotNativeContextDelegatesRestoreAndPropagatesRefusal(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) {
			dir := t.TempDir()
			for _, sub := range []string{"context", "snapshot-source"} {
				if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			program := `set -euo pipefail
source "$1"
snapshot_engine() {
  test "$PWD" = "$TEST_ROOT/snapshot-source"
  test "$#" = 7
  test "$1 $2 $3" = "container snapshot-images restore-context"
  test "$4" = --plan && test "$5" = "$TEST_ROOT/plan.json"
  test "$6" = --input && test "$7" = "$TEST_ROOT/context"
  printf 'restore-in-checkout\n'
  if [ "$REFUSE" = true ]; then return 78; fi
}
snapshot_apply_context context plan.json
printf 'ready-for-build\n'
`
			cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "TEST_ROOT="+dir, fmt.Sprintf("REFUSE=%t", refused))
			output, err := cmd.CombinedOutput()
			if refused {
				if err == nil || strings.TrimSpace(string(output)) != "restore-in-checkout" {
					t.Fatalf("restore refusal did not stop build/publication: %v\n%s", err, output)
				}
			} else if err != nil || strings.TrimSpace(string(output)) != "restore-in-checkout\nready-for-build" {
				t.Fatalf("restore delegation: %v\n%s", err, output)
			}
		})
	}
}

func TestSnapshotNativePlanResolvesSelectedLogicalBranchOnce(t *testing.T) {
	dir := t.TempDir()
	program := `set -euo pipefail
source "$1"
snapshot_engine() {
  case "$1 $2" in
    "validate event-context") ;;
    "platform checkout")
      printf '%s\n' "$@" > "$TEST_LOG/checkout"
      git init --quiet snapshot-source
      git -C snapshot-source -c user.name=Fixture -c user.email=fixture@example.invalid commit --quiet --allow-empty -m fixture
      ;;
    "container snapshot-images") printf '%s\n' "$@" > "$TEST_LOG/plan-args" ;;
    *) return 99 ;;
  esac
}
snapshot_plan
git -C snapshot-source rev-parse HEAD
`
	cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TEST_LOG="+dir, "SNAPSHOT_BRANCH=feature/chosen", "GITHUB_REF_NAME=wrong-trigger", "CI_COMMIT_REF_NAME=wrong-trigger", "SNAPSHOT_REPOSITORY=org/app", "SNAPSHOT_SERVER=https://forge.example", "SNAPSHOT_IMAGE_REPOSITORY=forge.example/org/app")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("plan source: %v\n%s", err, output)
	}
	sha := strings.TrimSpace(string(output))
	checkout, err := os.ReadFile(filepath.Join(dir, "checkout"))
	if err != nil || !strings.Contains(string(checkout), "--ref\nrefs/heads/feature/chosen\n") {
		t.Fatalf("checkout did not select the logical branch: %v\n%s", err, checkout)
	}
	args, err := os.ReadFile(filepath.Join(dir, "plan-args"))
	if err != nil || !strings.Contains(string(args), "--branch\nfeature/chosen\n--source-sha\n"+sha+"\n") {
		t.Fatalf("plan did not freeze branch plus exact commit: %v\n%s", err, args)
	}
}

func TestSnapshotNativePreparedPlanUsesProducerCommitWithoutResolvingBranchAgain(t *testing.T) {
	dir := t.TempDir()
	program := `set -euo pipefail
source "$1"
git init --quiet snapshot-source
git -C snapshot-source -c user.name=Fixture -c user.email=fixture@example.invalid commit --quiet --allow-empty -m fixture
sha=$(git -C snapshot-source rev-parse HEAD)
printf '{"source_branch":"feature/chosen","source_sha":"%s"}\n' "$sha" > producer-plan.json
snapshot_checkout() { test "$1" = "$sha"; printf '%s\n' "$1" > checkout-ref; }
snapshot_engine() {
  if [ "$3" = read-plan ]; then
    if [ "$7" = source-branch ]; then printf 'feature/chosen\n'; else printf '%s\n' "$sha"; fi
  else
    test "$1 $2 $3" = "container snapshot-images plan"
    cp ../producer-plan.json ../snapshot-plan.json
  fi
}
snapshot_plan producer-plan.json
test "$(<checkout-ref)" = "$sha"
`
	cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SNAPSHOT_BRANCH=feature/chosen", "SNAPSHOT_IMAGE_REPOSITORY=forge.example/org/app")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepared plan source: %v\n%s", err, output)
	}
}

func TestSnapshotNativeBuildDoesNotReceivePublicationCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "snapshot-source"), 0o700); err != nil {
		t.Fatal(err)
	}
	program := `set -euo pipefail
source "$1"
snapshot_engine() {
  if [ "$1 $2 $3" = "container snapshot-images build" ]; then
    test -z "${FORGEJO_TOKEN:-}${GITHUB_TOKEN:-}${CI_JOB_TOKEN:-}${CI_REGISTRY_PASSWORD:-}${REGISTRY_TOKEN:-}${REGISTRY_PASSWORD:-}${SNAPSHOT_REGISTRY_PASSWORD:-}"
    printf 'native-build\n'
  fi
}
snapshot_build api amd64
`
	cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SNAPSHOT_SERVER=https://forge.example", "SNAPSHOT_REPOSITORY=org/app", "FORGEJO_TOKEN=private", "CI_JOB_TOKEN=private", "SNAPSHOT_REGISTRY_PASSWORD=private")
	output, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "native-build" {
		t.Fatalf("credential reached build: %v\n%s", err, output)
	}
}

func TestSnapshotNativeArtifactProducerUsesSharedCLIWithoutPublicationCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "snapshot-source"), 0o700); err != nil {
		t.Fatal(err)
	}
	program := `set -euo pipefail
source "$1"
snapshot_engine() {
  test "$PWD" = "$TEST_ROOT/snapshot-source"
  test "$1 $2 $3" = "container snapshot-images build-artifact"
  test -z "${FORGEJO_TOKEN:-}${CI_JOB_TOKEN:-}${CI_REGISTRY_PASSWORD:-}${SNAPSHOT_REGISTRY_PASSWORD:-}${COSIGN_KEY:-}${RELEASE_COSIGN_PRIVATE_KEY:-}"
  printf '%s\n' "$@"
}
snapshot_build_artifact web
`
	cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TEST_ROOT="+dir, "FORGEJO_TOKEN=private", "CI_JOB_TOKEN=private", "CI_REGISTRY_PASSWORD=private", "SNAPSHOT_REGISTRY_PASSWORD=private", "COSIGN_KEY=private", "RELEASE_COSIGN_PRIVATE_KEY=private")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--plan\n../snapshot-plan.json\n--name\nweb\n--output-dir\n_snapshot-artifacts\n") {
		t.Fatalf("producer dispatch or credential boundary: %v\n%s", err, output)
	}
}

func TestSnapshotForgejoAutomaticProducerAndGatherOrdering(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Needs    any
			If       string
			Strategy struct{ Matrix map[string]string }
			Steps    []struct {
				Run string
				Env map[string]string
			}
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/workflows/snapshot-images.yml"), &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Jobs["produce"].Strategy.Matrix["artifact"] != "${{ fromJSON(needs.plan.outputs.artifacts) }}" {
		t.Fatal("producers must use the shared unique artifact projection")
	}
	if !strings.Contains(fmt.Sprint(workflow.Jobs["context"].Needs), "produce") || !strings.Contains(workflow.Jobs["context"].If, "!failure()") {
		t.Fatal("gather must wait for every producer to succeed")
	}
	if !strings.Contains(fmt.Sprint(workflow.Jobs["build"].Needs), "context") || !strings.Contains(workflow.Jobs["build"].If, "needs.context.result == 'success'") {
		t.Fatal("image builds must wait for validated gathered context")
	}
	for _, name := range []string{"produce", "context"} {
		pinned := false
		for _, step := range workflow.Jobs[name].Steps {
			if step.Env["SOURCE_SHA"] == "${{ needs.plan.outputs.source-sha }}" && strings.Contains(step.Run, `snapshot_checkout "${SOURCE_SHA}"`) {
				pinned = true
			}
			for key := range step.Env {
				if strings.Contains(key, "PASSWORD") || strings.Contains(key, "COSIGN") {
					t.Fatalf("source/gather job %s receives publication credential %s", name, key)
				}
			}
		}
		if !pinned {
			t.Fatalf("%s does not check out the frozen source SHA", name)
		}
	}
}

func TestSnapshotForgejoWorkflowUsesFrozenPlanAndWholeMatrixBarrier(t *testing.T) {
	for _, file := range []string{".forgejo/workflows/snapshot-images.yml", ".forgejo/bin/snapshot-images.sh"} {
		if strings.Contains(string(reporoot.ReadFile(t, file)), "jq ") {
			t.Fatalf("%s reintroduced an undeclared jq dependency; native transport uses the typed engine", file)
		}
	}
	var workflow struct {
		On struct {
			Call struct {
				Inputs map[string]struct{ Default any } `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Needs any
			Steps []struct {
				Run string
				Env map[string]string
			}
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/workflows/snapshot-images.yml"), &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.On.Call.Inputs["publish-containers"].Default != true {
		t.Fatal("snapshot containers must default on")
	}
	needs := workflow.Jobs["publish"].Needs.([]any)
	if len(needs) != 3 || needs[2] != "build" {
		t.Fatal("publisher must wait for whole build matrix")
	}
	for _, step := range workflow.Jobs["build"].Steps {
		for key := range step.Env {
			if strings.Contains(key, "PASSWORD") || strings.Contains(key, "COSIGN") {
				t.Fatalf("build step receives publisher credential %s", key)
			}
		}
	}
}
