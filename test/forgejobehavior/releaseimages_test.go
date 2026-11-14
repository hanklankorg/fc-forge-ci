// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// directoryRecordingEngine records its argv and the directory it started
// in, since the engine resolves every relative path against that directory.
const directoryRecordingEngine = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\0' "$@" >"${FAKE_ARGV_LOG:?}"
pwd >"${FAKE_ARGV_LOG}.pwd"
`

func TestReleaseImageLabelsReachThePlanBeforeAnyBuild(t *testing.T) {
	t.Parallel()

	box := newSandbox(t, "release-image-labels")
	box.write(".engine/bin/forge-ci", directoryRecordingEngine, 0o700)
	box.write("workspace/source/Containerfile", "FROM scratch\n", 0o600)

	step := workflowStep(t, "release-images.yml", "plan", "Plan release images")
	labels := []string{"org.example.features=one,two", "org.opencontainers.image.licenses=MIT"}

	got := box.run(step.Run,
		"HELPERS_DIR="+box.root, "GITHUB_WORKSPACE="+filepath.Join(box.root, "workspace"),
		"DECLARATION_FILE=", "IMAGE_REPOSITORY=registry.invalid/org/app", "REGISTRY_USER=org",
		"RELEASE_TAG=v1.2.3", "RELEASE_PLATFORMS=linux/amd64", "IMAGE_PLAN_JSON=",
		"FORGEJO_SERVER=https://forgejo.invalid", "FORGEJO_REPO=org/app", "IMAGE_LABELS="+strings.Join(labels, "\n"),
		`RUNS_ON=["pool"]`, `BUILD_RUNS_ON_LINUX_AMD64=["builder"]`, "BUILD_RUNS_ON_LINUX_ARM64=")

	if got.status != 0 {
		t.Fatalf("plan step: status %d: %s", got.status, got.stderr)
	}

	argv := box.argv()
	for _, label := range labels {
		if at := slices.Index(argv, label); at < 1 || argv[at-1] != "--label" {
			t.Errorf("label %q did not reach the engine's plan-time admission: %q", label, argv)
		}
	}

	if step.Env["IMAGE_LABELS"] != "${{ inputs.labels }}" {
		t.Fatal("workflow labels must enter the plan step")
	}

	if _, late := workflowStep(t, "release-images.yml", "build", "Build ${{ matrix.build.arch }} layout").Env["IMAGE_LABELS"]; late {
		t.Fatal("build step must consume admitted plan labels rather than the raw workflow input")
	}
}

// The engine takes only relative metadata directories and resolves the
// Containerfile and context against its working directory. The build stages
// beside the checkout, never in a build context, and uploads what it staged;
// publish keeps its index metadata apart from the layouts it downloaded. The
// build labels each image as the retired build-release-image action did:
// url and documentation from the source, ref.name the final tag, created the
// release commit's time.
func TestReleaseImages_StagedPathsAreRelativeAndMeet(t *testing.T) {
	t.Parallel()

	box := newSandbox(t, "release-images")
	box.write(".engine/bin/forge-ci", directoryRecordingEngine, 0o700)

	workspace := filepath.Join(box.root, "workspace")
	for _, directory := range []string{filepath.Join(workspace, "source"), filepath.Join(box.root, "runner-temp")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// The release commit the build labels its created time from.
	source := filepath.Join(workspace, "source")
	for _, argv := range [][]string{
		{"init", "--quiet"},
		{"-c", "user.name=fixture", "-c", "user.email=fixture@invalid", "commit", "--quiet", "--allow-empty", "-m", "release"},
	} {
		git := exec.CommandContext(t.Context(), "git", append([]string{"-C", source}, argv...)...)

		git.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2026-10-24T08:09:10+02:00", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", argv, err, out)
		}
	}

	env := []string{
		"HELPERS_DIR=" + box.root, "GITHUB_WORKSPACE=" + workspace, "RUNNER_TEMP=" + filepath.Join(box.root, "runner-temp"),
		"PUBLISH_DIR=" + filepath.Join(workspace, "source"),
		"IMAGE_NAME=app", "IMAGE_ARCH=amd64", "IMAGE_ARCHS=amd64", "IMAGE_REPOSITORY=registry.invalid/owner/app",
		"CANDIDATE_TAG=staging-v1.2.3", "FINAL_TAG_NAME=v1.2.3-slim", "RELEASE_SHA=" + strings.Repeat("a", 40), "RELEASE_TAG=v1.2.3",
		"FORGEJO_SERVER=https://forgejo.invalid", "FORGEJO_REPO=owner/app", "CONTAINER_FILE=Containerfile",
		"BUILD_CONTEXT=.", "BUILD_TARGET=", "BUILD_ARGS=", "IMAGE_LABELS=", "IMAGE_SCAN=true",
		"REGISTRY_TOKEN=fixture-token", "REGISTRY_USER=owner",
		"IMAGE_ARTIFACT_NAMESPACE=base-images", "PLANNED_IMAGE_LABELS=org.example.base-id=expected",
		"REQUIRED_IMAGE_LABELS=org.example.base-id=expected",
		"IMAGE_KIND=base", "IMAGE_FLAVOR=core", "BASE_INPUT_ID=" + strings.Repeat("b", 64),
		"IMAGE_ROLE=base", `IMAGE_PROVENANCE_JSON={"build_inputs":"approved"}`,
		"IMAGE_DIGEST=sha256:" + strings.Repeat("c", 64), "RELEASE_IMAGES_ADMIN_VULNERABILITIES=false",
	}
	box.write("workspace/image-artifacts/base-images-ledger-app/inventory-app-amd64.cyclonedx.json", `{"bomFormat":"CycloneDX"}`, 0o600)

	engine := func(file, job, name string) (string, []string) {
		t.Helper()

		got := box.run(workflowStep(t, file, job, name).Run, env...)
		if got.status != 0 {
			t.Fatalf("%s: status %d: %s", name, got.status, got.stderr)
		}

		cwd, err := os.ReadFile(box.argvLog + ".pwd")
		if err != nil {
			t.Fatal(err)
		}

		return strings.TrimSpace(string(cwd)), box.argv()
	}

	flag := func(argv []string, name string) string {
		t.Helper()

		at := slices.Index(argv, name)
		if at < 0 || at+1 == len(argv) {
			t.Fatalf("argv %q has no %s", argv, name)
		}

		return argv[at+1]
	}

	cwd, build := engine("release-images.yml", "build", "Build ${{ matrix.build.arch }} layout")
	staged := flag(build, "--metadata-dir")

	if !filepath.IsLocal(staged) || strings.HasPrefix(staged, "source/") {
		t.Errorf("build --metadata-dir %q: want a relative directory outside the checkout", staged)
	}

	for _, name := range []string{"--containerfile", "--context"} {
		if value := flag(build, name); !strings.HasPrefix(value, "source/") {
			t.Errorf("build %s %q is not in the checkout, source/", name, value)
		}
	}

	for _, label := range []string{
		"org.example.base-id=expected",
		"org.opencontainers.image.url=https://forgejo.invalid/owner/app",
		"org.opencontainers.image.documentation=https://forgejo.invalid/owner/app#readme",
		"org.opencontainers.image.ref.name=v1.2.3-slim",
		"org.opencontainers.image.created=2026-10-24T06:09:10Z",
	} {
		if !slices.Contains(build, label) {
			t.Errorf("build argv %q has no --label %s", build, label)
		}
	}

	_, upload := engine("release-images.yml", "build", "Upload ${{ matrix.build.arch }} layout")
	if name := flag(upload, "--name"); name != "base-images-layout-app-amd64" {
		t.Errorf("layout artifact %q escaped its invocation namespace", name)
	}

	if uploaded := filepath.Clean(flag(upload, "--dir")); uploaded != filepath.Join(cwd, staged) {
		t.Errorf("upload --dir %q, want the staged %q", uploaded, filepath.Join(cwd, staged))
	}

	_, assemble := engine("release-images.yml", "publish", "Scan and publish ${{ matrix.image.name }}")
	if !slices.Contains(assemble, "--enforce-vulnerabilities=false") {
		t.Errorf("publication dropped the administrator's vulnerability setting: %q", assemble)
	}

	if !slices.Contains(assemble, "org.example.base-id=expected") {
		t.Errorf("publication lost the consumer's required image identity: %q", assemble)
	}

	if index := flag(assemble, "--metadata-dir"); !filepath.IsLocal(index) || strings.Contains(index, "-arch-") {
		t.Errorf("publish --metadata-dir %q: want a relative directory apart from the downloaded layouts", index)
	}

	_, record := engine("release-images.yml", "publish", "Record ${{ matrix.image.name }} in the ledger")
	if flag(record, "--image-kind") != "base" || flag(record, "--role") != "base" || flag(record, "--flavor") != "core" || flag(record, "--base-input-id") != strings.Repeat("b", 64) || flag(record, "--provenance-json") != `{"build_inputs":"approved"}` {
		t.Errorf("publication lost the consumer's base lineage: %q", record)
	}

	if len(flag(record, "--sbom-sha256")) != 64 {
		t.Errorf("publication omitted the premade inventory pin: %q", record)
	}
}

func TestReleaseImages_RefusesUnsafeArtifactNamespacesBeforePlanning(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "../other", "bases/*", "bases\nother"} {
		box := newSandbox(t, "release-images-namespace")

		got := box.run(workflowStep(t, "release-images.yml", "coordinates", "Validate trust coordinates").Run,
			"ARTIFACT_NAME_INPUT="+name)
		if got.status == 0 || !strings.Contains(got.stderr, "artifact-name") {
			t.Errorf("namespace %q: status %d stderr %s", name, got.status, got.stderr)
		}
	}
}

func TestReleaseImages_RefusesBuilderDriftBeforeBuilding(t *testing.T) {
	t.Parallel()

	box := newSandbox(t, "release-images-builder-identity")
	box.write(".engine/bin/forge-ci", `#!/usr/bin/env bash
set -eu
if [ "$*" != 'container builder-identity' ]; then
  touch "${FORBIDDEN_BUILD_MARKER:?}"
  exit 91
fi
printf '%s\n' '`+strings.Repeat("a", 64)+`'
`, 0o700)
	marker := filepath.Join(box.root, "must-not-build")

	got := box.run(workflowStep(t, "release-images.yml", "build", "Build ${{ matrix.build.arch }} layout").Run,
		"HELPERS_DIR="+box.root, "EXPECTED_BUILDER_MATERIAL_ID="+strings.Repeat("b", 64),
		"FORBIDDEN_BUILD_MARKER="+marker)
	if got.status == 0 || !strings.Contains(got.stderr, "planned native identity") {
		t.Fatalf("builder drift: status %d stderr %s", got.status, got.stderr)
	}

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("builder drift reached a build operation: %v", err)
	}
}

func TestReleaseImages_CollectsExactPlannedNamesOutsideTheHelperTree(t *testing.T) {
	t.Parallel()
	box := newSandbox(t, "release-images-collection")
	box.write(".engine/bin/forge-ci", `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_ARGV_LOG:?}"
case "$1 $2" in
  'artifact download')
    mkdir -p "$6"
    image=${4##*-}
    for name in "sbom-${image}-amd64.json" "image-sbom-${image}.cyclonedx.json" "index-${image}.json"; do
      printf '%s' "$name" >"$6/$name"
    done
    ;;
  'container ledger') printf '[]\n' >"$5" ;;
esac
`, 0o700)

	got := box.run(workflowStep(t, "release-images.yml", "collect", "Merge and upload the release-images artifact").Run,
		"HELPERS_DIR="+box.root, "GITHUB_WORKSPACE="+box.root,
		"IMAGE_ARTIFACT_NAMESPACE=forge-ci-layer", "IMAGE_NAMES=core\ngo")
	if got.status != 0 {
		t.Fatalf("collection: status %d stderr %s", got.status, got.stderr)
	}

	log, err := os.ReadFile(box.argvLog)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"core", "go"} {
		if !strings.Contains(string(log), "artifact download --name forge-ci-layer-ledger-"+name+" --dir ") {
			t.Fatalf("missing exact planned ledger %s: %s", name, log)
		}

		for _, evidence := range []string{"sbom-" + name + "-amd64.json", "image-sbom-" + name + ".cyclonedx.json", "index-" + name + ".json"} {
			body, err := os.ReadFile(filepath.Join(box.root, "image-artifacts", "forge-ci-layer", evidence))
			if err != nil || string(body) != evidence {
				t.Fatalf("collected evidence %s missing or changed: %s, %v", evidence, body, err)
			}
		}
	}

	if strings.Contains(string(log), "--pattern") || !strings.Contains(string(log), "--dir "+box.root+"/image-artifacts/forge-ci-layer/") {
		t.Fatalf("collection used a broad pattern or helper output directory: %s", log)
	}
}

// A published release image is always scanned: the publish step does not
// read the plan's scan flag, so even a stale scan=false matrix value still
// scans, retains the index BOM and pins it in the ledger. Whether findings
// block is the engine's administrator policy.
func TestReleaseImages_AlwaysScansWhatItPublishes(t *testing.T) {
	t.Parallel()
	box := newSandbox(t, "release-evidence-always-scanned")
	box.write(".engine/bin/forge-ci", directoryRecordingEngine, 0o700)

	env := []string{
		"HELPERS_DIR=" + box.root, "GITHUB_WORKSPACE=" + box.root, "PUBLISH_DIR=" + box.root, "RUNNER_TEMP=" + box.root,
		"IMAGE_ARTIFACT_NAMESPACE=release-images", "IMAGE_NAME=app", "IMAGE_ARCHS=amd64", "IMAGE_SCAN=false", "IMAGE_KIND=",
		"IMAGE_REPOSITORY=registry.invalid/owner/app", "CANDIDATE_TAG=staging-v1.2.3", "FINAL_TAG_NAME=v1.2.3",
		"RELEASE_SHA=" + strings.Repeat("a", 40), "RELEASE_TAG=v1.2.3", "FORGEJO_SERVER=https://forgejo.invalid", "FORGEJO_REPO=owner/app",
		"REGISTRY_TOKEN=fixture-token", "REGISTRY_USER=owner", "IMAGE_DIGEST=sha256:" + strings.Repeat("b", 64),
		"IMAGE_SBOM_SHA256=" + strings.Repeat("c", 64), "RELEASE_IMAGES_ADMIN_VULNERABILITIES=true",
	}
	for step, required := range map[string][]string{
		"Scan and publish ${{ matrix.image.name }}":     {"--scan-output-template", "--sbom-output-template", "--index-sbom-output"},
		"Record ${{ matrix.image.name }} in the ledger": {"--sbom-sha256"},
	} {
		got := box.run(workflowStep(t, "release-images.yml", "publish", step).Run, env...)
		if got.status != 0 {
			t.Fatalf("%s: %s", step, got.stderr)
		}

		for _, flag := range required {
			if !slices.Contains(box.argv(), flag) {
				t.Fatalf("%s: a scan=false matrix value dropped %s: %q", step, flag, box.argv())
			}
		}
	}

	for _, name := range []string{"Install Trivy", "Install Syft for platform inventories"} {
		if step := workflowStep(t, "release-images.yml", "publish", name); step.If != "" {
			t.Errorf("%s runs only if %s; the scanner must always be installed", name, step.If)
		}
	}
}

// Exercise the actual workflow shell for standard images and arbitrary flavors.
// Image names are deliberately different from role/flavor so evidence filenames
// must agree with the existing ledger SBOM policy rather than a naming accident.
func TestReleaseImages_RetainsPinnedIndexEvidenceForEveryReleaseVariant(t *testing.T) {
	t.Parallel()

	for _, variant := range []string{"distroless", "alpine", "core", "javascript-typescript", "java", "kotlin", "swift", "go", "rust"} {
		t.Run(variant, func(t *testing.T) {
			t.Parallel()
			box := newSandbox(t, "release-evidence")
			box.write(".engine/bin/forge-ci", directoryRecordingEngine, 0o700)

			role, flavor := "flavor", variant
			if variant == "distroless" || variant == "alpine" {
				role, flavor = variant, ""
			}

			env := []string{
				"HELPERS_DIR=" + box.root, "GITHUB_WORKSPACE=" + box.root, "PUBLISH_DIR=" + box.root, "RUNNER_TEMP=" + box.root,
				"IMAGE_ARTIFACT_NAMESPACE=release-images", "IMAGE_NAME=selected-variant", "IMAGE_ARCHS=amd64\narm64",
				"IMAGE_REPOSITORY=registry.invalid/owner/app", "CANDIDATE_TAG=staging-v1.2.3", "FINAL_TAG_NAME=v1.2.3",
				"IMAGE_SCAN=true", "IMAGE_KIND=", "IMAGE_ROLE=" + role, "IMAGE_FLAVOR=" + flavor,
				"RELEASE_SHA=" + strings.Repeat("a", 40), "RELEASE_TAG=v1.2.3", "FORGEJO_SERVER=https://forgejo.invalid", "FORGEJO_REPO=owner/app",
				"REGISTRY_TOKEN=fixture-token", "REGISTRY_USER=owner", "IMAGE_DIGEST=sha256:" + strings.Repeat("b", 64),
				"IMAGE_SBOM_SHA256=" + strings.Repeat("c", 64), "RELEASE_IMAGES_ADMIN_VULNERABILITIES=true",
			}

			got := box.run(workflowStep(t, "release-images.yml", "publish", "Scan and publish ${{ matrix.image.name }}").Run, env...)
			if got.status != 0 {
				t.Fatalf("assemble: %s", got.stderr)
			}

			argv := box.argv()
			joined := strings.Join(argv, " ")

			for _, expected := range []string{
				"--sbom-output-template " + box.root + "/image-artifacts/release-images-ledger-selected-variant/sbom-" + variant + "-{arch}.json",
				"--index-sbom-output " + box.root + "/image-artifacts/release-images-ledger-selected-variant/image-sbom-" + variant + ".cyclonedx.json",
				"--sbom-url-prefix https://forgejo.invalid/owner/app/releases/download/v1.2.3",
			} {
				if !strings.Contains(joined, expected) {
					t.Fatalf("missing %q: %q", expected, argv)
				}
			}

			got = box.run(workflowStep(t, "release-images.yml", "publish", "Record ${{ matrix.image.name }} in the ledger").Run, env...)
			if got.status != 0 || !slices.Contains(box.argv(), strings.Repeat("c", 64)) {
				t.Fatalf("record lost the exact index BOM pin: status %d: %s", got.status, got.stderr)
			}

			env[len(env)-1] = "IMAGE_SBOM_SHA256="

			got = box.run(workflowStep(t, "release-images.yml", "publish", "Record ${{ matrix.image.name }} in the ledger").Run, env...)
			if got.status == 0 {
				t.Fatal("scanned release image accepted without its index BOM pin")
			}
		})
	}
}
