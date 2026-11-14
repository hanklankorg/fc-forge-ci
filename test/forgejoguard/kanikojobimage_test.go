// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const (
	kanikoJobImageExample  = "examples/forgejo/kaniko-job-image.yml"
	kanikoJobContainerfile = "containers/kaniko-job/Containerfile"
	kanikoBuildJob         = "build"
	kanikoMergeJob         = "merge"

	// signerBuildArchPoolRunner is the pool runner native to the matrix
	// architecture: kaniko builds there, no container runner is needed.
	signerBuildArchPoolRunner = "${{ fromJSON(vars[matrix.arch == 'arm64' && 'CI_RUNNER_LABEL_ARM64' || 'CI_RUNNER_LABEL']) }}"
)

// The kaniko job image is the one image kaniko jobs cannot build, so its
// bootstrap builds each architecture in kaniko's own debug image, natively, on
// the pool, from the pinned engine commit as a git context, and a job with the
// engine merges the two into an index after checking each one's architecture
// and revision. No action anywhere: the debug image has no Node. One secret.
func TestKanikoJobImageExampleBootstrapsNativelyPerArchitectureThenMerges(t *testing.T) {
	t.Parallel()

	document := loadDocument(t, kanikoJobImageExample)
	parsed := loadWorkflow(t, kanikoJobImageExample)

	if len(parsed.jobs) != 2 {
		t.Fatalf("the bootstrap has %d jobs, want the per-architecture build and the merge", len(parsed.jobs))
	}

	// The merge job runs the engine binary the signer workflow publishes, so a
	// push that moves both pins must not start this one beside it.
	triggers := mappingEntries(mappingValue(document, "on"))
	if len(triggers) != 1 || scalarString(triggers[0].Key) != "workflow_dispatch" {
		t.Error("the bootstrap must run by dispatch only, after the pin's engine binary is published")
	}

	for name, target := range parsed.jobs {
		for _, step := range target.steps() {
			if uses := scalarString(mappingValue(step, "uses")); uses != "" {
				t.Errorf("job %s uses %q; the bootstrap runs no action", name, uses)
			}
		}
	}

	build := parsed.jobs[kanikoBuildJob]
	pinnedKaniko := regexp.MustCompile(`^ghcr\.io/osscontainertools/kaniko:v[0-9.]+-debug@sha256:[0-9a-f]{64}$`)

	if got := build.runsOn(); got != signerBuildArchPoolRunner {
		t.Errorf("build runs on %q, want the pool runner of its architecture %q", got, signerBuildArchPoolRunner)
	}

	// The image contains /kaniko, so the executor must work from another
	// directory: kaniko refuses to save its own.
	if got, ok := build.env("KANIKO_DIR"); !ok || got == "" || got == "/kaniko" {
		t.Errorf("build job must move kaniko's own directory away from /kaniko, got KANIKO_DIR=%q", got)
	}

	if image := build.containerImage(); !pinnedKaniko.MatchString(image) {
		t.Errorf("build runs in %q, want kaniko's debug image pinned by digest", image)
	}

	if got := scalarString(mappingValue(parsed.env, "FORGE_CI_COMMIT_SHA")); got != enginePinPlaceholder {
		t.Errorf("FORGE_CI_COMMIT_SHA is %q, want the placeholder %s: the git context and the labels name that commit", got, enginePinPlaceholder)
	}

	for _, want := range []string{`git_context="git://`, `#${FORGE_CI_COMMIT_SHA:?}"`, `--custom-platform "linux/${ARCH:?}"`, "--reproducible", "--destination", `rm -rf "${DOCKER_CONFIG}"`} {
		if !jobRunsStep(build, want) {
			t.Errorf("build job lacks %q: the image is built from the pinned commit, natively, pushed, and the credentials file removed", want)
		}
	}

	merge := parsed.jobs[kanikoMergeJob]
	if got := merge.runsOn(); got != repositoryRunner {
		t.Errorf("merge runs on %q, want the pool %q", got, repositoryRunner)
	}

	if needs := jobNeeds(merge); len(needs) != 1 || needs[0] != kanikoBuildJob {
		t.Errorf("merge needs %v, want the complete build matrix", needs)
	}

	for _, want := range []string{"image usable-digest", `--require-label "org.opencontainers.image.revision=`, "write-digest-marker", "manifest merge",
		// manifest merge takes tags as references under the image repository.
		`--tags "${KANIKO_JOB_IMAGE}:kaniko-job-${FORGE_CI_COMMIT_SHA}"`} {
		if !jobRunsStep(merge, want) {
			t.Errorf("merge job lacks %q: each architecture is admitted by its labels before the index is assembled", want)
		}
	}

	// Step outputs do not reach the log, so the reference to pin is printed.
	if !anyString(merge.node, func(v string) bool { return v == "${{ steps.index.outputs.image-digest-ref }}" }) || !jobRunsStep(merge, `echo "CI_IMAGE_BUILDER_JOB_IMAGE=${IMAGE_DIGEST_REF:?}"`) {
		t.Error("merge job must print the index reference to pin as CI_IMAGE_BUILDER_JOB_IMAGE")
	}

	secret := regexp.MustCompile(`secrets\.[A-Za-z0-9_]+`)

	anyString(document, func(value string) bool {
		for _, reference := range secret.FindAllString(value, -1) {
			if reference != "secrets.SIGNER_REGISTRY_TOKEN" {
				t.Errorf("the bootstrap reads %s; the registry token is its one secret", reference)
			}
		}

		return false
	})

	// The Containerfile the bootstrap builds pins the same kaniko release.
	body, err := os.ReadFile(filepath.Join(reporoot.Path(t), kanikoJobContainerfile))
	if err != nil {
		t.Fatal(err)
	}

	version := regexp.MustCompile(`kaniko:(v[0-9.]+)-debug@`).FindStringSubmatch(build.containerImage())
	if version == nil || !strings.Contains(string(body), "ghcr.io/osscontainertools/kaniko:"+version[1]+"@sha256:") {
		t.Errorf("%s and %s must pin the same kaniko release", kanikoJobImageExample, kanikoJobContainerfile)
	}

	for _, want := range []string{`org.opencontainers.image.title="forge-ci kaniko job image"`, "bash-static", "busybox-static", "COPY --from=kaniko /kaniko /kaniko", "SSL_CERT_FILE=/kaniko/ssl/certs/ca-certificates.crt", ":/kaniko/tools:", "install -d -m 1777 /kaniko/tmp", "TMPDIR=/kaniko/tmp"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("%s lacks %q: the steps after a kaniko build depend on it", kanikoJobContainerfile, want)
		}
	}
}

// The builder is a type, buildah by default: the signer build job runs in the
// builder's job image when one is named and leaves the rest to
// setup-image-builder, so the workflow names no builder.
func TestSignerImageExampleSelectsTheBuilderByVariable(t *testing.T) {
	t.Parallel()

	parsed := loadWorkflow(t, signerImageExample)
	build := parsed.jobs[signerBuildArchJob]

	if got := build.containerImage(); got != "${{ vars.CI_IMAGE_BUILDER_JOB_IMAGE }}" {
		t.Errorf("build job container is %q, want the CI_IMAGE_BUILDER_JOB_IMAGE variable", got)
	}

	if got, ok := build.env("CI_IMAGE_BUILDER"); !ok || got != "${{ vars.CI_IMAGE_BUILDER || 'buildah' }}" {
		t.Errorf("build job must take CI_IMAGE_BUILDER from the variable, buildah by default, got %q", got)
	}

	if got, ok := build.env("CI_IMAGE_BUILDER_JOB_IMAGE"); !ok || got != "${{ vars.CI_IMAGE_BUILDER_JOB_IMAGE }}" {
		t.Errorf("build job must pass CI_IMAGE_BUILDER_JOB_IMAGE to the builder setup, got %q", got)
	}

	// The workflow names no builder: one step prepares whichever is selected,
	// and no step is conditional on it.
	prepared := 0

	for _, step := range build.steps() {
		uses := scalarString(mappingValue(step, "uses"))
		if strings.Contains(uses, "/setup-buildah") {
			t.Error("build job must prepare the builder with setup-image-builder, not setup-buildah")
		}

		if uses == "./.forgejo/actions/setup-image-builder" {
			prepared++
		}

		if strings.Contains(scalarString(mappingValue(step, "if")), "CI_IMAGE_BUILDER") {
			t.Errorf("step %q branches on the builder; that is setup-image-builder's job", scalarString(mappingValue(step, "name")))
		}
	}

	if prepared != 1 {
		t.Errorf("build job prepares the image builder %d times, want once", prepared)
	}
}
