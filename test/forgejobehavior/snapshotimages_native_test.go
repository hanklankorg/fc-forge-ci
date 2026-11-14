// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejobehavior

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
	"github.com/diggsweden/forge-ci/internal/testutil/testenv"
	"github.com/diggsweden/forge-ci/internal/testutil/testfs"
	"gopkg.in/yaml.v3"
)

func snapshotTestEnvironment(t *testing.T, extra ...string) []string {
	t.Helper()

	return append(os.Environ(), append([]string{"PATH=" + testenv.GNUPath(t, "realpath") + ":" + os.Getenv("PATH")}, extra...)...)
}

func TestSnapshotNativeBuilderCoordinatesKeepBuildahOnTheRunnerDefault(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), ".forgejo/workflows/snapshot-images.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err = yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}

	program := workflow.Jobs["coordinates"].Steps[0].Run
	image := "registry.invalid/job@sha256:" + strings.Repeat("a", 64)

	for _, tc := range []struct {
		name, builder, image, want string
		refused                    bool
	}{
		{name: "buildah default", builder: "buildah", want: "builder_job_image=\n"},
		{name: "buildah ignores inherited image", builder: "buildah", image: image, want: "builder_job_image=\n"},
		{name: "kaniko pinned image", builder: "kaniko", image: image, want: "builder_job_image=" + image + "\n"},
		{name: "kaniko missing image", builder: "kaniko", refused: true},
		{name: "kaniko mutable image", builder: "kaniko", image: "registry.invalid/job:latest", refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := testfs.NewReal(t).Root
			output := filepath.Join(dir, "output")
			cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program)
			cmd.Dir = dir
			cmd.Env = snapshotTestEnvironment(t, "HELPERS_REPOSITORY_INPUT=fixture/engine", "SERVER_URL=https://forge.example",
				"BUILDER_INPUT="+tc.builder, "BUILDER_JOB_IMAGE_INPUT="+tc.image, "FORGEJO_OUTPUT="+output,
				"ARTIFACT_JOB_IMAGE_GO_INPUT=", "ARTIFACT_JOB_IMAGE_JAVA_INPUT=", "ARTIFACT_JOB_IMAGE_NODE_INPUT=", "ARTIFACT_JOB_IMAGE_RUST_INPUT=")

			logs, runErr := cmd.CombinedOutput()
			if tc.refused {
				if _, statErr := os.Stat(output); runErr == nil || !os.IsNotExist(statErr) {
					t.Fatalf("invalid image produced partial coordinates: %v %v", runErr, statErr)
				}

				return
			}

			if runErr != nil {
				t.Fatalf("native builder admission: %v\n%s", runErr, logs)
			}

			result, readErr := os.ReadFile(output)
			if readErr != nil || !strings.Contains(string(result), tc.want) || !strings.Contains(string(result), "builder="+tc.builder+"\n") {
				t.Fatalf("wrong native coordinates: %v\n%s", readErr, result)
			}
		})
	}
}

func TestSnapshotNativeContextDelegatesRestoreAndPropagatesRefusal(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(strconv.FormatBool(refused), func(t *testing.T) {
			dir := testfs.NewReal(t).Root
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
			cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
			cmd.Dir = dir
			cmd.Env = snapshotTestEnvironment(t, "TEST_ROOT="+dir, fmt.Sprintf("REFUSE=%t", refused))

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
	dir := testfs.NewReal(t).Root
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
	cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
	cmd.Dir = dir
	cmd.Env = snapshotTestEnvironment(t, "TEST_LOG="+dir, "SNAPSHOT_BRANCH=feature/chosen", "GITHUB_REF_NAME=wrong-trigger", "CI_COMMIT_REF_NAME=wrong-trigger", "SNAPSHOT_REPOSITORY=org/app", "SNAPSHOT_SERVER=https://forge.example", "SNAPSHOT_IMAGE_REPOSITORY=forge.example/org/app")

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
	dir := testfs.NewReal(t).Root
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
	cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
	cmd.Dir = dir

	cmd.Env = snapshotTestEnvironment(t, "SNAPSHOT_BRANCH=feature/chosen", "SNAPSHOT_IMAGE_REPOSITORY=forge.example/org/app")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prepared plan source: %v\n%s", err, output)
	}
}

func TestSnapshotNativeBuildDoesNotReceivePublicationCredentials(t *testing.T) {
	dir := testfs.NewReal(t).Root
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
	cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
	cmd.Dir = dir

	cmd.Env = snapshotTestEnvironment(t, "SNAPSHOT_SERVER=https://forge.example", "SNAPSHOT_REPOSITORY=org/app", "FORGEJO_TOKEN=private", "CI_JOB_TOKEN=private", "SNAPSHOT_REGISTRY_PASSWORD=private")

	output, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "native-build" {
		t.Fatalf("credential reached build: %v\n%s", err, output)
	}
}

// On GitLab the kaniko job image is read with the job token, which a private
// registry needs; elsewhere, and before the build, anonymously and with no
// auth file left behind.
func TestSnapshotNativeKanikoAdmissionReadsWithTheJobToken(t *testing.T) {
	image := "registry.example/team/kaniko-job@sha256:" + strings.Repeat("a", 64)

	for name, tc := range map[string]struct {
		env  []string
		want string
	}{
		"gitlab": {env: []string{
			"FORGE_CI_PROVIDER=gitlab", "RC_REGISTRY=registry.example", "CI_REGISTRY=registry.attacker.invalid", "CI_JOB_TOKEN=job-token-canary",
		}, want: "login registry.example gitlab-ci-token job-token-canary\nsetup-image-builder --auth-file\n"},
		"forgejo": {env: []string{
			"FORGE_CI_PROVIDER=forgejo", "RC_REGISTRY=registry.example", "CI_REGISTRY=registry.attacker.invalid", "CI_JOB_TOKEN=job-token-canary",
		}, want: "setup-image-builder\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := testfs.NewReal(t).Root
			if err := os.Mkdir(filepath.Join(dir, "snapshot-source"), 0o700); err != nil {
				t.Fatal(err)
			}

			program := `set -euo pipefail
source "$1"
snapshot_engine() {
  case "$1 $2" in
  "container login") printf 'login %s %s %s\n' "$4" "$6" "$(cat)"; test -n "${10}" && : >"${10}"; printf '%s\n' "${10}" >auth-path ;;
  "container setup-image-builder")
    test "$3 $4" = '--image-builder kaniko'
    test "$5" = --job-image
    test "$6" = "$CI_IMAGE_BUILDER_JOB_IMAGE"
    test "$7" = --engine-repository
    test "$8" = "$FORGE_CI_REPOSITORY_URL"
    test "${*: -2}" = '--arch amd64'
    if [ "${13:-}" = --auth-file ]; then test -f "${14}"; echo 'setup-image-builder --auth-file' >&2; else echo setup-image-builder >&2; fi ;;
  esac
}
snapshot_build api amd64
if [ -f auth-path ]; then test ! -e "$(<auth-path)"; fi
`
			cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
			cmd.Dir = dir

			cmd.Env = append(snapshotTestEnvironment(t, "CI_REGISTRY=", "CI_JOB_TOKEN=", "SNAPSHOT_SERVER=https://forge.example", "SNAPSHOT_REPOSITORY=org/app",
				"CI_IMAGE_BUILDER=kaniko", "CI_IMAGE_BUILDER_JOB_IMAGE="+image, "FORGE_CI_REPOSITORY_URL=https://forge.example/team/engine.git"), tc.env...)

			output, err := cmd.CombinedOutput()
			if err != nil || string(output) != tc.want {
				t.Fatalf("admission ran %q, want %q: %v", output, tc.want, err)
			}
		})
	}
}

func TestSnapshotNativeArtifactProducerUsesSharedCLIWithoutPublicationCredentials(t *testing.T) {
	dir := testfs.NewReal(t).Root
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
	cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
	cmd.Dir = dir
	cmd.Env = snapshotTestEnvironment(t, "TEST_ROOT="+dir, "FORGEJO_TOKEN=private", "CI_JOB_TOKEN=private", "CI_REGISTRY_PASSWORD=private", "SNAPSHOT_REGISTRY_PASSWORD=private", "COSIGN_KEY=private", "RELEASE_COSIGN_PRIVATE_KEY=private")

	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--plan\n../snapshot-plan.json\n--name\nweb\n--output-dir\n_snapshot-artifacts\n") {
		t.Fatalf("producer dispatch or credential boundary: %v\n%s", err, output)
	}
}

// A file output is built from the consumer's code, so the build holds no
// token and no signing or registry secret; and the job that builds it keeps
// the bundle in the run alone, for the documented seven days.
func TestSnapshotNativeFilesAreBuiltWithoutCredentialsAndKeptInTheRun(t *testing.T) {
	dir := testfs.NewReal(t).Root
	if err := os.Mkdir(filepath.Join(dir, "snapshot-source"), 0o700); err != nil {
		t.Fatal(err)
	}

	program := `set -euo pipefail
source "$1"
snapshot_engine() {
  test "$PWD" = "$TEST_ROOT/snapshot-source"
  test "$1 $2 $3" = "container snapshot-images build-files"
  test -z "${FORGEJO_TOKEN:-}${CI_JOB_TOKEN:-}${SNAPSHOT_REGISTRY_PASSWORD:-}${RELEASE_TOKEN:-}${RELEASE_GPG_PRIVATE_KEY:-}${RELEASE_COSIGN_PRIVATE_KEY:-}"
  printf '%s\n' "$@"
}
snapshot_build_files cli
`
	cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
	cmd.Dir = dir
	cmd.Env = snapshotTestEnvironment(t, "TEST_ROOT="+dir, "FORGEJO_TOKEN=private", "CI_JOB_TOKEN=private", "SNAPSHOT_REGISTRY_PASSWORD=private", "RELEASE_TOKEN=private", "RELEASE_GPG_PRIVATE_KEY=private", "RELEASE_COSIGN_PRIVATE_KEY=private")

	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--plan\n../snapshot-plan.json\n--name\ncli\n--output-dir\nsnapshot-files\n") {
		t.Fatalf("files dispatch or credential boundary: %v\n%s", err, output)
	}

	var workflow struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/workflows/snapshot-images.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	files := workflow.Jobs["files"]
	if files.If != "${{ needs.coordinates.result == 'success' && needs.plan.result == 'success' && needs.plan.outputs.has-files == 'true' }}" {
		t.Fatalf("the files job must run only for a plan with file outputs: %q", files.If)
	}

	kept := false

	for _, step := range files.Steps {
		for key, value := range step.Env {
			if strings.Contains(value, "secrets.") || strings.Contains(key, "PASSWORD") || strings.Contains(key, "COSIGN") || strings.Contains(key, "RELEASE") {
				t.Fatalf("the files job receives %s", key)
			}

			if strings.Contains(value, ".token") && strings.Contains(step.Run, "snapshot_build_files") {
				t.Fatalf("the build step of the files job receives the run token as %s", key)
			}
		}

		if strings.Contains(step.Run, "artifact upload") {
			kept = strings.Contains(step.Run, `--name "${SNAPSHOT_NAMESPACE}-files-${FILES_ARTIFACT_NAME}"`) && strings.Contains(step.Run, "--retention-days 7") &&
				strings.Contains(step.Run, `[[ "${FILES_ARTIFACT_NAME}" == "${FILE_NAME}"-*-snapshot-* ]]`)
		}
	}

	if !kept {
		t.Fatal("the files job must keep the bundle under its snapshot name for seven days")
	}
}

// A Go or Cargo build that writes its Build SBOM needs forge-ci's generator,
// which a Forgejo job image does not carry: the release's build actions
// install it, and the snapshot's producer and files jobs must too, before
// the build and only when the plan has the build write the SBOM. Without it
// the first snapshot of a Go command stopped at "cyclonedx-gomod not found".
func TestSnapshotNativeBuildsInstallTheBuildSBOMGeneratorTheyNeed(t *testing.T) {
	helpers := testfs.NewReal(t).Root
	if err := os.MkdirAll(filepath.Join(helpers, "scripts/bootstrap"), 0o700); err != nil {
		t.Fatal(err)
	}

	for tool, function := range map[string]string{"cyclonedx-gomod": "install_cyclonedx_gomod", "cargo-cyclonedx": "install_cargo_cyclonedx"} {
		installer := "scripts/bootstrap/install-" + tool + ".sh"
		if !strings.Contains(string(reporoot.ReadFile(t, installer)), "\n"+function+"() {") {
			t.Fatalf("%s does not define %s", installer, function)
		}

		if err := os.WriteFile(filepath.Join(helpers, installer), []byte(function+"() { echo installed "+tool+"; }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	program := `set -euo pipefail
source "$1"
snapshot_engine() {
  test "$*" = "container snapshot-images read-plan --plan snapshot-plan.json --name app --field build-sbom"
  printf '%s\n' "$BUILD_SBOM"
}
snapshot_build_sbom_tool app "$PROJECT_TYPE"
`

	for _, tc := range []struct{ projectType, buildSBOM, want string }{
		{"go", "true", "installed cyclonedx-gomod\n"},
		{"cargo", "true", "installed cargo-cyclonedx\n"},
		{"go", "false", ""},
		{"maven", "true", ""},
	} {
		cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh"))
		cmd.Env = snapshotTestEnvironment(t, "SNAPSHOT_HELPERS="+helpers, "PROJECT_TYPE="+tc.projectType, "BUILD_SBOM="+tc.buildSBOM)

		if output, err := cmd.CombinedOutput(); err != nil || string(output) != tc.want {
			t.Fatalf("%s build, Build SBOM %s: %v, output %q, want %q", tc.projectType, tc.buildSBOM, err, output, tc.want)
		}
	}

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/workflows/snapshot-images.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	// Every step of the workflow that builds, whatever its job: the install
	// comes first, told the build's project type. Every step that installs
	// a tool keeps the download in the job's own temporary directory, since
	// the rows of a matrix on one host share /tmp.
	const ownTemp = `export CI_TEMP_DIR="${RUNNER_TEMP:?}"`

	builds, installs := 0, 0

	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			install := strings.Index(step.Run, "snapshot_build_sbom_tool ")
			evidence := strings.Index(step.Run, "snapshot_evidence_tools ")

			for _, tools := range []int{install, evidence} {
				if tools < 0 {
					continue
				}

				installs++

				if temp := strings.Index(step.Run, ownTemp); temp < 0 || temp > tools {
					t.Errorf("job %s installs a tool without first setting %s", name, ownTemp)
				}
			}

			build := max(strings.Index(step.Run, "snapshot_build_artifact "), strings.Index(step.Run, "snapshot_build_files "))
			if build < 0 {
				continue
			}

			builds++

			typed := false
			for key, value := range step.Env {
				typed = typed || strings.HasSuffix(key, "_PROJECT_TYPE") && strings.HasSuffix(value, ".project_type }}") && strings.Contains(step.Run, `"${`+key+`}"`)
			}

			if install < 0 || install > build || !typed {
				t.Errorf("job %s builds without first installing the Build SBOM generator for the build's project type", name)
			}
		}
	}

	if builds != 2 || installs != 3 {
		t.Fatalf("found %d build steps and %d tool installs, want the producer and files builds and their installs beside the publisher's", builds, installs)
	}
}

// The plan step hands the caller's publish-files and registry-user to the
// planner, and a plan with an image and no registry account is refused there,
// when the plan is frozen, not in the publisher after every build.
func TestSnapshotNativePlanForwardsFilesAndRefusesAnImageWithoutPublisher(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				ID  string            `yaml:"id"`
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/workflows/snapshot-images.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	wired := false

	for _, step := range workflow.Jobs["plan"].Steps {
		if step.ID != "plan" {
			continue
		}

		planned, refused := strings.LastIndex(step.Run, "snapshot_plan "), strings.Index(step.Run, "snapshot_require_publisher")
		wired = step.Env["SNAPSHOT_PUBLISH_FILES"] == "${{ inputs['publish-files'] }}" &&
			step.Env["SNAPSHOT_REGISTRY_USER"] == "${{ inputs['registry-user'] }}" &&
			step.Env["SECRET_SNAPSHOT_REGISTRY_PASSWORD"] == "${{ secrets.SNAPSHOT_REGISTRY_PASSWORD != '' }}" && planned >= 0 && refused > planned
	}

	if !wired {
		t.Fatal("the plan step must pass publish-files and registry-user and refuse an image without a publisher after planning")
	}

	script := filepath.Join(reporoot.Path(t), ".forgejo/bin/snapshot-images.sh")
	dir := testfs.NewReal(t).Root
	program := `set -euo pipefail
source "$1"
snapshot_engine() {
  case "$1 $2" in
    "validate event-context") ;;
    "platform checkout")
      git init --quiet snapshot-source
      git -C snapshot-source -c user.name=Fixture -c user.email=fixture@example.invalid commit --quiet --allow-empty -m fixture
      ;;
    "container snapshot-images") printf '%s\n' "$@" > "$TEST_LOG/plan-args" ;;
    *) return 99 ;;
  esac
}
snapshot_plan
`
	cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", script)
	cmd.Dir = dir
	cmd.Env = snapshotTestEnvironment(t, "TEST_LOG="+dir, "SNAPSHOT_BRANCH=feature/chosen", "SNAPSHOT_REPOSITORY=org/app", "SNAPSHOT_SERVER=https://forge.example", "SNAPSHOT_PUBLISH_FILES=true")

	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("plan: %v\n%s", err, output)
	}

	args, err := os.ReadFile(filepath.Join(dir, "plan-args"))
	if err != nil || !strings.Contains(string(args), "\n--image-repository\n\n--publish=true\n--publish-files=true\n") {
		t.Fatalf("the plan did not receive publish-files and an empty image repository: %v\n%s", err, args)
	}

	for name, test := range map[string]struct {
		images, user, password, namespace string
		refused                           string
	}{
		"image without account":  {"web", "", "true", "snapshot", "registry-user is empty"},
		"image without password": {"web", "ci-bot", "false", "snapshot", "SNAPSHOT_REGISTRY_PASSWORD is not set"},
		"image with both":        {"web", "ci-bot", "true", "snapshot", ""},
		"files alone":            {"", "", "false", "snapshot", ""},
		"files, long namespace":  {"", "", "false", strings.Repeat("n", 49), "artifact-name is longer than 48"},
	} {
		t.Run(name, func(t *testing.T) {
			program := `set -euo pipefail
source "$1"
snapshot_engine() {
  test "$*" = "container snapshot-images read-plan --plan snapshot-plan.json --field image-names"
  printf '%s' "$TEST_IMAGES"
}
snapshot_require_publisher
`
			cmd := exec.CommandContext(t.Context(), testBash(t), "--noprofile", "--norc", "-c", program, "snapshot", script)
			cmd.Env = snapshotTestEnvironment(t, "TEST_IMAGES="+test.images, "SNAPSHOT_REGISTRY_USER="+test.user,
				"SECRET_SNAPSHOT_REGISTRY_PASSWORD="+test.password, "SNAPSHOT_PUBLISH_FILES=true", "SNAPSHOT_NAMESPACE="+test.namespace)

			output, err := cmd.CombinedOutput()
			if (err != nil) != (test.refused != "") || !strings.Contains(string(output), test.refused) {
				t.Fatalf("refusal %q, want %q: %v\n%s", output, test.refused, err, output)
			}
		})
	}
}

func TestSnapshotForgejoAutomaticProducerAndGatherOrdering(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Needs    any    `yaml:"needs"`
			If       string `yaml:"if"`
			Strategy struct {
				Matrix map[string]string `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
				If  string            `yaml:"if"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/workflows/snapshot-images.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	if workflow.Jobs["produce"].Strategy.Matrix["artifact"] != "${{ fromJSON(needs.plan.outputs.has-artifacts == 'true' && needs.plan.outputs.artifacts || '[{}]') }}" {
		t.Fatal("producers must use the unique artifact projection, with a no-op placeholder when empty so Forgejo can settle downstream dependencies")
	}

	if !strings.Contains(workflow.Jobs["produce"].If, "needs.plan.outputs.enabled == 'true'") {
		t.Fatal("enabled container-first plans must complete the no-op producer dependency")
	}

	for _, step := range workflow.Jobs["produce"].Steps {
		artifactStep := strings.Contains(step.Run, "snapshot_build_artifact") ||
			strings.Contains(step.Run, "snapshot_checkout") ||
			strings.Contains(step.Run, "fetch_forge_ci") ||
			strings.Contains(step.Run, "artifact upload")
		if artifactStep && step.If != "${{ matrix.artifact.name != '' }}" {
			t.Fatal("the empty-plan placeholder must not fetch source, build or upload an artifact")
		}
	}

	if !strings.Contains(fmt.Sprint(workflow.Jobs["context"].Needs), "produce") || !strings.Contains(workflow.Jobs["context"].If, "needs.produce.result == 'success'") {
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

// A plan without an image (files alone, or publish-containers false) has
// "[]" for builds and images. Forgejo expands an empty matrix to no job at
// all and never settles the jobs that need it (forgejo/forgejo#12191), so the
// run stays blocked. Every matrix of the workflow therefore carries one no-op
// row for the empty plan, the image rows are skipped by their job's
// condition, and the summary still runs after them.
func TestSnapshotForgejoPlanWithoutImageSettlesEveryJob(t *testing.T) {
	var workflow struct {
		Jobs map[string]struct {
			Needs    any    `yaml:"needs"`
			If       string `yaml:"if"`
			RunsOn   string `yaml:"runs-on"`
			Strategy struct {
				Matrix map[string]string `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(reporoot.ReadFile(t, ".forgejo/workflows/snapshot-images.yml"), &workflow); err != nil {
		t.Fatal(err)
	}

	matrices := 0

	for name, job := range workflow.Jobs {
		for key, matrix := range job.Strategy.Matrix {
			matrices++

			if !strings.HasSuffix(matrix, " || '[{}]') }}") {
				t.Errorf("job %s matrix %s can be empty, which blocks the jobs that need it: %s", name, key, matrix)
			}
		}
	}

	if matrices != 4 {
		t.Fatalf("the workflow has %d matrices, want 4", matrices)
	}

	for name, want := range map[string]string{
		"build":   "${{ fromJSON(needs.plan.outputs.enabled == 'true' && needs.plan.outputs.builds || '[{}]') }}",
		"publish": "${{ fromJSON(needs.plan.outputs.enabled == 'true' && needs.plan.outputs.images || '[{}]') }}",
	} {
		job := workflow.Jobs[name]
		if matrix := job.Strategy.Matrix[map[string]string{"build": "build", "publish": "image"}[name]]; matrix != want {
			t.Errorf("the %s matrix is %q, want %q", name, matrix, want)
		}

		if !strings.Contains(job.If, "needs.plan.outputs.enabled == 'true'") {
			t.Errorf("the no-op row of %s must be skipped, not run: if %q", name, job.If)
		}
	}

	// The no-op row has no architecture; its labels must still evaluate.
	if want := "${{ fromJSON(inputs[matrix.build.arch && format('build-runs-on-linux-{0}', matrix.build.arch) || 'runs-on']) }}"; workflow.Jobs["build"].RunsOn != want {
		t.Errorf("build runs on %q, want %q", workflow.Jobs["build"].RunsOn, want)
	}

	collect := workflow.Jobs["collect"]
	if needs := fmt.Sprint(collect.Needs); !strings.Contains(needs, "build") || !strings.Contains(needs, "publish") {
		t.Errorf("the summary must wait for every image job: %s", needs)
	}

	// Image jobs skipped by a plan without images do not stop the summary; a
	// failed, cancelled or otherwise skipped one does.
	if want := "${{ !cancelled() && needs.coordinates.result == 'success' && needs.plan.result == 'success' && " +
		"((needs.build.result == 'success' && needs.publish.result == 'success') || " +
		"(needs.plan.outputs.enabled != 'true' && needs.build.result == 'skipped' && needs.publish.result == 'skipped')) }}"; collect.If != want {
		t.Errorf("the summary runs if %q, want %q", collect.If, want)
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
				Inputs map[string]struct {
					Default any `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Needs any `yaml:"needs"`
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
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
