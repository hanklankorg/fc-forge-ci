// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gitlabbehavior

import (
	"bytes"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	appcontainer "github.com/diggsweden/forge-ci/internal/app/container"
	gitlabpipeline "github.com/diggsweden/forge-ci/internal/app/gitlabpipeline"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
	"github.com/diggsweden/forge-ci/internal/testutil/gitlabrender"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The image stage's settings are the policy's inputs, forwarded unchanged.
// Buildah stays the default; the builder's job image is an input of its own,
// empty by default, and no image job exists unless release-images is set.
func TestReleaseImageStageTakesItsSettingsFromThePolicy(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"templates/release.yml", "templates/release-jobs.yml"} {
		header, _ := releaseRouting(t, path)
		inputs := header["spec"].(map[string]any)["inputs"].(map[string]any)

		builder := inputs["image-builder"].(map[string]any)
		if builder["default"] != "buildah" || !reflect.DeepEqual(builder["options"], []any{"buildah", "kaniko"}) {
			t.Fatalf("%s image-builder must offer buildah, the default, and kaniko: %+v", path, builder)
		}

		image := inputs["builder-job-image"].(map[string]any)
		if image["default"] != "" || image["regex"] != `^$|^[^[:space:]]+@sha256:[0-9a-f]{64}$` {
			t.Fatalf("%s builder-job-image must be empty or pinned by digest: %+v", path, image)
		}

		images := inputs["release-images"].(map[string]any)
		if images["type"] != "boolean" || images["default"] != false {
			t.Fatalf("%s release-images must be an opt-in boolean: %+v", path, images)
		}

		// Bound to a platform, the build inputs have no default: the policy
		// names its runners, [] stating it has no arm64 one.
		for _, name := range []string{"build-runner-tags-linux-amd64", "build-runner-tags-linux-arm64"} {
			input := inputs[name].(map[string]any)
			if _, defaulted := input["default"]; input["type"] != "array" || defaulted {
				t.Fatalf("%s %s must be a required array: %+v", path, name, input)
			}
		}
	}

	root := releaseDocumentBody(t, "templates/release.yml")
	forwarded := root["release"].(map[string]any)["trigger"].(map[string]any)["include"].([]any)[0].(map[string]any)["inputs"].(map[string]any)

	for _, name := range []string{"job-user", "release-images", "artifacts-config", "release-platforms", "image-builder", "builder-job-image", "build-runner-tags-linux-arm64"} {
		if forwarded[name] != "$[[ inputs."+name+" ]]" {
			t.Fatalf("root component does not forward %s", name)
		}
	}

	body := releaseDocumentBody(t, "templates/release-jobs.yml")

	// The builder reaches the image jobs as literal script arguments, which a
	// project variable cannot replace, and no job names it as a variable.
	for name, job := range body {
		fields, isJob := job.(map[string]any)
		if !isJob {
			continue
		}

		if variables, ok := fields["variables"].(map[string]any); ok && (variables["CI_IMAGE_BUILDER"] != nil || variables["CI_IMAGE_BUILDER_JOB_IMAGE"] != nil) {
			t.Errorf("%s names the image builder as a variable", name)
		}
	}

	build := body["Build: release files"].(map[string]any)
	if !reflect.DeepEqual(build["script"], []any{"rc_release_checkout", "rc_gather_bases", "rc_gather_exports '$[[ inputs.export-files ]]'", "rc_gather_families", "$[[ inputs.build ]]", "rc_build_handoff"}) {
		t.Fatalf("the consumer build gained image work: %+v", build["script"])
	}

	// The export jobs take the builder, its job image and the architecture
	// from the policy's literals, as the image jobs do, and run in
	// builder-job-image on that architecture's runner. The body is rendered
	// with kaniko and export-files on.
	withExports := releaseJobsBodyFor(t, "kaniko")

	for arch, rule := range map[string]string{
		"amd64": `"true" == "true"`,
		"arm64": `'true' == 'true' && '$[[ inputs.build-runner-tags-linux-arm64 ]]' != '[]'`,
	} {
		job := gitlabrender.ResolveExtends(t, withExports, withExports["Build: export "+arch].(map[string]any))

		want := []any{
			"selected=$(rc_export_selected " + arch + " '$[[ inputs.release-platforms ]]') || exit; if [ \"$selected\" = false ]; then exit 0; fi",
			"rc_export_prepare " + arch + " 'kaniko' '$[[ inputs.builder-job-image ]]'",
			"$[[ inputs.export ]]",
			"rc_export_handoff " + arch,
		}
		if !reflect.DeepEqual(job["script"], want) || job["rules"].([]any)[0].(map[string]any)["if"] != rule {
			t.Fatalf("the %s export job does not take its builder and architecture from the policy: %+v", arch, job)
		}
	}

	exportJob := gitlabrender.ResolveExtends(t, withExports, withExports["Build: export amd64"].(map[string]any))
	if exportJob["image"].(map[string]any)["name"] != "$[[ inputs.builder-job-image ]]" || exportJob["variables"].(map[string]any)["RC_JOB_USER"] != "" {
		t.Fatalf("exports must run in builder-job-image as its own user: %+v", exportJob)
	}

	if body[".forge-ci-release-export-job-false"].(map[string]any)["image"].(map[string]any)["name"] != "$[[ inputs.job-image ]]" {
		t.Fatal("without export-files the export jobs must name job-image")
	}

	for arch, script := range map[string]string{
		"amd64": "rc_images_build amd64 '$[[ inputs.image-builder ]]' '$[[ inputs.builder-job-image ]]'",
		"arm64": "rc_images_build arm64 '$[[ inputs.image-builder ]]' '$[[ inputs.builder-job-image ]]'",
	} {
		job := body["Images: build "+arch].(map[string]any)
		if !reflect.DeepEqual(job["script"], []any{script}) ||
			!reflect.DeepEqual(job["extends"], []any{".forge-ci-release-images", ".forge-ci-release-image-builder-$[[ inputs.image-builder ]]"}) {
			t.Fatalf("the %s image job does not take its builder from the policy: %+v", arch, job)
		}
	}

	// Either builder runs in builder-job-image, never in the consumer's
	// build-image, and as that image's own user. Without release images,
	// when they never run, they name job-image, so that an empty
	// builder-job-image leaves GitLab a job image name to validate.
	withImages := releaseJobsBodyFor(t, "buildah")
	builderJob := gitlabrender.ResolveExtends(t, withImages, withImages[".forge-ci-release-image-builder"].(map[string]any))

	if builderJob["image"].(map[string]any)["name"] != "$[[ inputs.builder-job-image ]]" ||
		builderJob["variables"].(map[string]any)["RC_JOB_USER"] != "" {
		t.Fatalf("image builds must run in builder-job-image as its own user: %+v", builderJob)
	}

	if body[".forge-ci-release-image-job-false"].(map[string]any)["image"].(map[string]any)["name"] != "$[[ inputs.job-image ]]" {
		t.Fatal("without release images the image jobs must name job-image")
	}

	for _, builder := range []string{"buildah", "kaniko"} {
		template := body[".forge-ci-release-image-builder-"+builder].(map[string]any)
		if template["extends"] != ".forge-ci-release-image-builder" || template["image"] != nil {
			t.Fatalf("%s image builds must run in the builder's job image: %+v", builder, template)
		}
	}

	if _, tagged := body[".forge-ci-release-image-builder-kaniko"].(map[string]any)["tags"]; tagged {
		t.Fatal("kaniko builds need no tagged runner")
	}

	arm64Rule := body["Images: build arm64"].(map[string]any)["rules"].([]any)[0].(map[string]any)["if"]
	if arm64Rule != `'$[[ inputs.release-images ]]' == 'true' && '$[[ inputs.build-runner-tags-linux-arm64 ]]' != '[]'` {
		t.Fatalf("the arm64 image job is not bound to its runner tags: %v", arm64Rule)
	}

	sign := body["Sign: files and images"].(map[string]any)
	if !reflect.DeepEqual(sign["script"], []any{"rc_sign_release '$[[ inputs.release-images ]]'"}) {
		t.Fatalf("signing does not read the policy's hand-off: %+v", sign["script"])
	}
}

// The release image builder is admitted before any Containerfile is built:
// Buildah is set up from what its job image carries, installing nothing,
// kaniko admitted only as the kaniko job image of this engine project, read
// with the job token. Either needs its job image pinned by digest.
func TestReleaseImageBuilderAdmitsOnlyTheKanikoJobImage(t *testing.T) {
	t.Parallel()

	kanikoImage := "registry.invalid/team/kaniko-job@sha256:" + strings.Repeat("a", 64)
	kaniko := []string{"CI_IMAGE_BUILDER=kaniko", "CI_IMAGE_BUILDER_JOB_IMAGE=" + kanikoImage}
	buildahImage := "CI_IMAGE_BUILDER_JOB_IMAGE=registry.invalid/team/buildah-job@sha256:" + strings.Repeat("b", 64)
	setupBuildah := []string{"container setup-image-builder --image-builder buildah", "--require-job-image --install-packages=false"}

	for name, tc := range map[string]struct {
		env       []string
		usable    string
		admitted  bool
		calls     []string
		wantError string
	}{
		"buildah by default":         {env: []string{buildahImage}, admitted: true, calls: setupBuildah},
		"buildah named":              {env: []string{"CI_IMAGE_BUILDER=buildah", buildahImage}, admitted: true, calls: setupBuildah},
		"kaniko job image admitted":  {env: kaniko, usable: "0", admitted: true, calls: []string{"container login --registry registry.invalid", "job-token-canary", "container setup-image-builder --image-builder kaniko --job-image " + kanikoImage, "--require-job-image --install-packages=false"}},
		"kaniko job image refused":   {env: kaniko, usable: "1", calls: []string{"container setup-image-builder --image-builder kaniko"}},
		"kaniko admission cancelled": {env: kaniko, usable: "cancel", calls: []string{"container setup-image-builder --image-builder kaniko"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := commandContext(t)

			work := t.TempDir()
			script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
			// The fake engine records its arguments and stdin, and fails the
			// admission when told to.
			command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-euo", "pipefail", "-c",
				`source "$1"; rc_engine() { printf '%s\n' "$*" >>"${RC_WORK}/engine"; if [ "$2" = login ]; then cat >>"${RC_WORK}/engine"; echo >>"${RC_WORK}/engine"; : >"${10}"; fi; if [ "$2" = setup-image-builder ]; then if [ "${USABLE_STATUS:-}" = cancel ]; then kill -TERM "$BASHPID"; fi; return "${USABLE_STATUS:-0}"; fi; }; rc_image_builder`,
				"image-builder", script)

			command.Env = append([]string{"PATH=/usr/bin:/bin", "RC_WORK=" + work, "HELPERS_ENGINE_ARCH=amd64", "USABLE_STATUS=" + tc.usable,
				"FORGE_CI_REPOSITORY_URL=https://gitlab.invalid/team/engine.git", "RC_REGISTRY=registry.invalid", "CI_JOB_TOKEN=job-token-canary"}, tc.env...)

			output, err := command.CombinedOutput()
			if (err == nil) != tc.admitted || !strings.Contains(string(output), tc.wantError) {
				t.Fatalf("admitted=%t, want %t:\n%s", err == nil, tc.admitted, output)
			}

			calls, readErr := os.ReadFile(filepath.Join(work, "engine"))
			if (readErr == nil) != (len(tc.calls) != 0) {
				t.Fatalf("engine called=%t, want %t", readErr == nil, len(tc.calls) != 0)
			}

			for _, want := range tc.calls {
				if !strings.Contains(string(calls), want) {
					t.Fatalf("the admission did not run %q:\n%s", want, calls)
				}
			}

			leftovers, globErr := filepath.Glob(filepath.Join(work, "builder-auth.*"))
			if globErr != nil || len(leftovers) != 0 {
				t.Fatalf("builder auth not cleaned: %v %v", leftovers, globErr)
			}
		})
	}
}

// releaseImagesCheck is the release request check's refusal of an image stage
// that would stop after Prepare pushed the release commit and tag.
// exportsCheck hands the export commands to the request check as data on
// stdin; it runs none of them.
const exportsCheck = "rc_check_exports '$[[ inputs.export-files ]]' '$[[ inputs.builder-job-image ]]' '$[[ inputs.release-platforms ]]' '$[[ inputs.build-runner-tags-linux-arm64 ]]' <<'RC_EXPORT_COMMANDS'\n" +
	"$[[ inputs.export ]]\nRC_EXPORT_COMMANDS\n"

const releaseImagesCheck = "rc_check_release_images '$[[ inputs.release-images ]]' '$[[ inputs.image-builder ]]' " +
	"'$[[ inputs.builder-job-image ]]' '$[[ inputs.artifacts-config ]]' '$[[ inputs.release-platforms ]]'"

// The release request check, which runs before Prepare pushes anything,
// refuses an image stage that would stop later: either builder without the
// job image it runs in, a publication project that is not separate, or a plan
// no image job carries out. Without release images it checks only that no
// labels are set, which no image would carry.
func TestReleaseRequestCheckRefusesAnImageStageThatWouldStop(t *testing.T) {
	t.Parallel()

	kanikoImage := "registry.invalid/team/kaniko-job@sha256:" + strings.Repeat("a", 64)
	buildahImage := "registry.invalid/team/buildah-job@sha256:" + strings.Repeat("b", 64)
	separate := []string{"RC_PUBLICATION_PROJECT=team/publication", "RC_PUBLICATION_IMAGE_REPOSITORY=registry.invalid/team/publication"}

	for name, tc := range map[string]struct {
		args      string
		env       []string
		labels    string
		wantError string
		planned   bool
	}{
		"no release images":         {args: "false buildah ''"},
		"labels without images":     {args: "false buildah ''", labels: "org.opencontainers.image.licenses=MIT", wantError: "sets labels but release-images is false"},
		"labels with images":        {args: "true buildah " + buildahImage, env: separate, labels: "org.opencontainers.image.licenses=MIT\norg.opencontainers.image.vendor=Acme, Inc.\n", planned: true},
		"kaniko without its image":  {args: "true kaniko ''", env: separate, wantError: "set it to the kaniko job image"},
		"kaniko with its image":     {args: "true kaniko " + kanikoImage, env: separate, planned: true},
		"buildah without its image": {args: "true buildah ''", env: separate, wantError: "set it to the buildah job image"},
		"buildah with its image":    {args: "true buildah " + buildahImage, env: separate, planned: true},
		"publication not separate":  {args: "true buildah " + buildahImage, env: []string{"RC_PUBLICATION_PROJECT=team/app", "RC_PUBLICATION_IMAGE_REPOSITORY=registry.invalid/team/app"}, wantError: "publication-project must be a separate project"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			work := t.TempDir()
			env := append([]string{"RC_WORK=" + work, "RELEASE_TAG=v1.2.3", "RC_REGISTRY=registry.invalid", "RC_REGISTRY_IMAGE=registry.invalid/team/app",
				"RC_SERVER_URL=https://gitlab.invalid", "CI_PROJECT_PATH=team/app", "RC_IMAGE_LABELS=" + tc.labels,
				"RC_RUNNER_TAGS=[]", "RC_SIGNER_RUNNER_TAGS=[]", `RC_BUILD_RUNNER_TAGS_LINUX_AMD64=["amd64"]`, "RC_BUILD_RUNNER_TAGS_LINUX_ARM64=[]"}, tc.env...)

			output, err := runReleaseFunction(t, work, "rc_check_release_images "+tc.args+" .forge-ci/artifacts.yml ''", env...)
			if (err == nil) != (tc.wantError == "") || !strings.Contains(output, tc.wantError) {
				t.Fatalf("err = %v:\n%s", err, output)
			}

			labelArgs := ""
			if tc.labels != "" {
				labelArgs = "--label org.opencontainers.image.licenses=MIT --label org.opencontainers.image.vendor=Acme, Inc. "
			}

			plan := "engine: container release-images plan --file .forge-ci/artifacts.yml --image-plan-json  --release-tag v1.2.3 --release-platforms  " + labelArgs +
				"--image-repository registry.invalid/team/app --server-url https://gitlab.invalid --repository team/app " +
				`--registry-username gitlab-ci-token --runner-input runner-tags=[] --runner-input signer-runner-tags=[] --runner-input build-runner-tags-linux-amd64=["amd64"] --runner-input build-runner-tags-linux-arm64=[] --output ` + work + "/release-images-plan.json"
			if strings.Contains(output, plan) != tc.planned {
				t.Fatalf("planned=%t, want %t:\n%s", strings.Contains(output, plan), tc.planned, output)
			}
		})
	}
}

// A consumer build no longer pushes or records release images: a ledger in
// its dist stops the hand-off before any engine call, and image-context is
// handed to the image jobs beside dist.
func TestReleaseBuildHandoffRefusesAConsumerLedger(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ ledger, planted, baseLedger bool }{
		"consumer ledger":         {ledger: true},
		"consumer base ledger":    {baseLedger: true},
		"no ledger":               {},
		"hand-off already exists": {planted: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			work := t.TempDir()
			for _, dir := range []string{"dist", "image-context/target", "project"} {
				if err := os.MkdirAll(filepath.Join(work, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			if err := os.WriteFile(filepath.Join(work, "image-context/target/app.jar"), []byte("jar"), 0o600); err != nil {
				t.Fatal(err)
			}

			if tc.ledger {
				if err := os.WriteFile(filepath.Join(work, "dist/release-images.json"), []byte("[]"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if tc.baseLedger {
				if err := os.WriteFile(filepath.Join(work, "dist/base-images.json"), []byte("[]"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			// The consumer's build runs in this job and could leave the
			// hand-off with its own files in it, to be merged into.
			if tc.planted {
				if err := os.MkdirAll(filepath.Join(work, "project/_release-build/dist"), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			output, err := runReleaseFunction(t, work, `rc_planned_packages() { : >"$1"; }; rc_build_handoff`, "RC_WORK="+work, "CI_PROJECT_DIR="+filepath.Join(work, "project"))
			if tc.planted {
				if err == nil || !strings.Contains(output, "reserved for this job") {
					t.Fatalf("a planted hand-off was merged into: %v\n%s", err, output)
				}

				return
			}

			// Base images are the base stage's: a build that left a base
			// ledger is refused before anything is signed.
			if tc.baseLedger {
				if err == nil || !strings.Contains(output, "name the base images in the release policy as base-stages") || strings.Contains(output, "engine:") {
					t.Fatalf("a consumer base ledger was handed off: %v\n%s", err, output)
				}

				return
			}

			if tc.ledger {
				if err == nil || !strings.Contains(output, "written by the image jobs") || strings.Contains(output, "engine:") {
					t.Fatalf("a consumer ledger was handed off: %v\n%s", err, output)
				}

				return
			}

			if err != nil {
				t.Fatalf("hand-off refused: %v\n%s", err, output)
			}

			if _, err := os.Stat(filepath.Join(work, "project/_release-build/image-context/target/app.jar")); err != nil {
				t.Fatalf("image-context was not handed off: %v", err)
			}
		})
	}
}

// An image build job reads the release commit's time before the build's files
// are laid over the checkout, gives the build none of the job's credentials,
// and hands off its layouts readable without after_script, which a kaniko job
// may have no shell left for.
func TestReleaseImageBuildHandsOffReadableLayouts(t *testing.T) {
	t.Parallel()

	work := t.TempDir()
	project := filepath.Join(work, "project")

	if err := os.MkdirAll(filepath.Join(project, "_release-build/image-context"), 0o700); err != nil {
		t.Fatal(err)
	}

	body := `rc_image_builder() { :; }; rc_release_checkout() { mkdir -p "${RC_WORK}/consumer"; cd "${RC_WORK}/consumer"; }; ` +
		`git() { echo "git: $1" >&2; }; cp() { echo "cp: $*"; command cp "$@"; }; ` +
		`rc_engine() { echo "engine: $3 token=${CI_JOB_TOKEN-unset} proxy=${CI_DEPENDENCY_PROXY_PASSWORD-unset}"; ` +
		`mkdir -p staged/app-arch-amd64/layout; touch staged/app-arch-amd64/app-amd64.json; }; ` +
		`umask 077; rc_images_build amd64 buildah ''`

	output, err := runReleaseFunction(t, work, body, "RC_WORK="+work, "CI_PROJECT_DIR="+project,
		"CI_JOB_TOKEN=job-token", "CI_DEPENDENCY_PROXY_PASSWORD=proxy-password", "RC_REGISTRY_IMAGE=registry.example.com/group/app",
		"RELEASE_TAG=v1.2.3", "RELEASE_SHA="+strings.Repeat("a", 40), "RC_SERVER_URL=https://gitlab.example.com", "CI_PROJECT_PATH=group/app")
	if err != nil {
		t.Fatalf("image build failed: %v\n%s", err, output)
	}

	if show, overlay := strings.Index(output, "git: show"), strings.Index(output, "cp: -R -- "+project+"/_release-build/image-context/."); show < 0 || overlay < 0 || show > overlay {
		t.Fatalf("the created label must be read from the release commit before the overlay:\n%s", output)
	}

	if !strings.Contains(output, "engine: build token=unset proxy=unset") {
		t.Fatalf("the build was given a job credential:\n%s", output)
	}

	handoff := filepath.Join(project, "_release-images-amd64")
	requireMode(t, handoff, 0o755)
	requireMode(t, filepath.Join(handoff, "app-arch-amd64/layout"), 0o755)
	requireMode(t, filepath.Join(handoff, "app-arch-amd64/app-amd64.json"), 0o644)
}

// Signing reads the hand-off the policy names and holds it to it: with
// release images the publishing job's, which must carry the ledger; without,
// the build's, which must not.
func TestReleaseSigningReadsTheHandoffThePolicyNames(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		images, handoff string
		ledger, signed  bool
	}{
		"images with their ledger": {images: "true", handoff: "_release-images", ledger: true, signed: true},
		"images without a ledger":  {images: "true", handoff: "_release-images"},
		"no images":                {images: "false", handoff: "_release-build", signed: true},
		"no images but a ledger":   {images: "false", handoff: "_release-build", ledger: true},
		"an unknown setting":       {images: "maybe"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			work := t.TempDir()
			ledger := ""

			if tc.ledger {
				ledger = "touch dist/release-images.json; "
			}

			// The checks the signer runs before reading are stubbed; the first
			// engine call after the read ends the run successfully.
			body := `rc_signing_preflight() { :; }; rc_release_checkout() { :; }; rc_release_file_signing() { rc_file_signing=cosign; }; ` +
				`rc_read_handoff() { echo "read: $1"; mkdir dist; ` + ledger + `export DIST_DIR=dist RELEASE_FILES_MANIFEST=dist/release-files.json; }; ` +
				`rc_engine() { echo "engine: $*"; exit 0; }; rc_sign_release ` + tc.images

			output, err := runReleaseFunction(t, work, body, "CI_PROJECT_DIR=/project", "RC_SIGN_DISTRO_PACKAGES=false")
			if (err == nil) != tc.signed {
				t.Fatalf("signed=%t, want %t:\n%s", err == nil, tc.signed, output)
			}

			if tc.handoff != "" && !strings.Contains(output, "read: /project/"+tc.handoff+"\n") {
				t.Fatalf("signing read another hand-off than %s:\n%s", tc.handoff, output)
			}
		})
	}
}

// runReleaseFunction runs body after sourcing release.sh in dir, with an
// engine that records its calls and succeeds unless body replaces it.
func runReleaseFunction(t *testing.T, dir, body string, env ...string) (string, error) {
	t.Helper()

	ctx := commandContext(t)

	script := filepath.Join(reporoot.Path(t), ".gitlab", "bin", "release.sh")
	command := exec.CommandContext(ctx, componentBash(t), "--noprofile", "--norc", "-euo", "pipefail", "-c",
		`source "$1"; rc_engine() { echo "engine: $*"; }; `+body, "release-function", script)
	command.Dir = dir
	command.Env = append([]string{"PATH=" + componentTools(t), "HOME=" + dir}, env...)

	output, err := command.CombinedOutput()

	return string(output), err
}

// releaseDocumentBody decodes the jobs document after a component's spec.
func releaseDocumentBody(t *testing.T, path string) map[string]any {
	t.Helper()

	decoder := yaml.NewDecoder(bytes.NewReader(reporoot.ReadFile(t, path)))

	var header, body map[string]any
	if err := decoder.Decode(&header); err != nil {
		t.Fatal(err)
	}

	if err := decoder.Decode(&body); err != nil {
		t.Fatal(err)
	}

	return body
}

// Each architecture's hand-off contributes only its own <name>-arch-<arch>
// layout directories: an entry of another architecture, which could replace
// that job's layout, or anything but such a directory stops publishing.
func TestReleaseImagesPublishAdmitsEachArchitecturesOwnLayouts(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		plant     func(t *testing.T, handoff string)
		wantError string
	}{
		"own layouts":            {},
		"another arch's layout":  {plant: mkdirLayout("app-arch-arm64"), wantError: "layout entry"},
		"a file":                 {plant: writeFile(".ledger"), wantError: "layout entry"},
		"a linked layout":        {plant: linkLayout("worker-arch-amd64"), wantError: "layout entry"},
		"an unnamed arch layout": {plant: mkdirLayout("-arch-amd64"), wantError: "layout entry"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			work := t.TempDir()
			handoff := filepath.Join(work, "_release-images-amd64")

			for _, dir := range []string{filepath.Join(handoff, "app-arch-amd64", "layout")} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			if err := os.WriteFile(filepath.Join(handoff, "app-arch-amd64", "app-amd64.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}

			if tc.plant != nil {
				tc.plant(t, handoff)
			}

			err := appcontainer.AdmitReleaseImageLayoutSources(map[string]string{"amd64": handoff}, []pipeline.ReleaseImage{{Name: "app", Archs: "amd64"}})

			output := ""
			if err != nil {
				output = err.Error()
			}

			if (err == nil) != (tc.wantError == "") || !strings.Contains(output, tc.wantError) {
				t.Fatalf("err = %v:\n%s", err, output)
			}

			if tc.wantError == "" {
				if _, statErr := os.Stat(filepath.Join(handoff, "app-arch-amd64", "layout")); statErr != nil {
					t.Fatalf("the admitted source was changed: %v", statErr)
				}
			}
		})
	}
}

func mkdirLayout(name string) func(t *testing.T, handoff string) {
	return func(t *testing.T, handoff string) {
		t.Helper()

		if err := os.MkdirAll(filepath.Join(handoff, name, "layout"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(name string) func(t *testing.T, handoff string) {
	return func(t *testing.T, handoff string) {
		t.Helper()

		if err := os.WriteFile(filepath.Join(handoff, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func linkLayout(name string) func(t *testing.T, handoff string) {
	return func(t *testing.T, handoff string) {
		t.Helper()

		if err := os.Symlink(t.TempDir(), filepath.Join(handoff, name)); err != nil {
			t.Fatal(err)
		}
	}
}

// job-user reaches the jobs of job-image alone, as the check component's
// does. The build and image-builder jobs run their own images as those
// images' users, root for Buildah and kaniko, and the signer jobs name their
// image as a string, which replaces the image options entirely; all of them
// clear RC_JOB_USER.
func TestReleaseJobUserRunsOnlyTheJobImageJobs(t *testing.T) {
	t.Parallel()

	requireImageUserVariable(t, releaseDocumentBody(t, "templates/release-jobs.yml")[".forge-ci-release"].(map[string]any),
		"RC_JOB_USER", "$[[ inputs.job-user ]]")

	for _, builder := range []string{"buildah", "kaniko"} {
		body := releaseJobsBodyFor(t, builder)
		for name, raw := range body {
			job, isJob := raw.(map[string]any)
			if !isJob || strings.HasPrefix(name, ".") {
				continue
			}

			fields := gitlabrender.ResolveExtends(t, body, job)
			variables, _ := fields["variables"].(map[string]any)
			user := variables["RC_JOB_USER"]

			image, isMap := fields["image"].(map[string]any)
			if !isMap {
				if user != "" {
					t.Errorf("%s names its image alone with RC_JOB_USER %q: it takes no user", name, user)
				}

				continue
			}

			if inJobImage := image["name"] == "$[[ inputs.job-image ]]"; inJobImage != (user == "$[[ inputs.job-user ]]") || !inJobImage && user != "" {
				t.Errorf("%s with %s runs %v with RC_JOB_USER %q: only job-image jobs take job-user", name, builder, image["name"], user)
			}
		}
	}
}

// releaseJobsBodyFor decodes the release jobs document with the builder
// chosen and release images and exports on, so the extends names resolve as GitLab
// interpolates them.
func releaseJobsBodyFor(t *testing.T, builder string) map[string]any {
	t.Helper()

	text := strings.NewReplacer("$[[ inputs.image-builder ]]", builder, "$[[ inputs.release-images ]]", "true", "$[[ inputs.export-files ]]", "true").
		Replace(string(reporoot.ReadFile(t, "templates/release-jobs.yml")))

	var body map[string]any
	if err := yaml.Unmarshal([]byte(text[strings.Index(text, "\n---\n")+5:]), &body); err != nil {
		t.Fatal(err)
	}

	// The child pipeline as GitLab composes it from both includes, with
	// every family job "Release: plan" can name.
	maps.Copy(body, everyReleaseFamily.Jobs())

	return body
}

// everyReleaseFamily names every family job of the release child.
var everyReleaseFamily = gitlabpipeline.ReleaseFamilies{
	Version:  []string{"java", "node", "rust"},
	Build:    []string{"java", "node", "go", "rust", "android"},
	Packages: []string{"frontend"},
	Bases:    []gitlabpipeline.BaseStage{{Flavors: []string{"base"}}, {Flavors: []string{"tools", "debug"}, Parent: "base"}},
}

// The release's labels input admits key=value lines only, each value a
// single line without the quote that ends the literal it is assigned to,
// and both release components admit the same.
func TestReleaseLabelsInputAdmitsKeyValueLines(t *testing.T) {
	t.Parallel()

	outer, _ := gitlabrender.InputSpec(t, "templates/release.yml")["labels"]["regex"].(string)
	inner, _ := gitlabrender.InputSpec(t, "templates/release-jobs.yml")["labels"]["regex"].(string)

	if outer == "" || outer != inner {
		t.Fatalf("release.yml labels regex %q, release-jobs.yml %q; want one and the same", outer, inner)
	}

	admit := regexp.MustCompile(outer)
	for _, value := range []string{"", "org.opencontainers.image.licenses=MIT", "a.b=Acme, Inc.\nc-d=EUPL-1.2 OR MIT\n"} {
		if !admit.MatchString(value) {
			t.Errorf("labels %q refused", value)
		}
	}

	for _, value := range []string{"a.b=it's", "a.b=", "=x", "A.b=x", "a.b=x\n\nc.d=y", "a.b=x\r", "a.b=x' ; echo '", "a.b=x\tz", "\na.b=x"} {
		if admit.MatchString(value) {
			t.Errorf("labels %q admitted", value)
		}
	}
}
