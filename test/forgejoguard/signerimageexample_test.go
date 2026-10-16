// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

const (
	signerImageExample   = "examples/forgejo/signer-image.yml"
	signerContainerfile  = "containers/signer/Containerfile"
	signerContextIgnores = signerContainerfile + ".containerignore"
	enginePinPlaceholder = "PIN_FORGE_CI_SHA"

	// publishBinaryJob is the job of the example that publishes the engine
	// binary, and binaryScriptPath the script it builds and uploads with.
	publishBinaryJob = "publish-engine-binary"
	binaryScriptPath = "scripts/ci/forgejo-binary.sh"
)

func TestSignerImageExampleDefersRegistryWritesUntilAllBuildsSucceed(t *testing.T) {
	t.Parallel()
	parsed := loadWorkflow(t, signerImageExample)
	build := parsed.jobs["build-signer-arch"]

	assembly := parsed.jobs["assemble-signer-image"]
	if got := scalarString(mappingValue(assembly.node, "needs")); got != "build-signer-arch" {
		t.Fatalf("assembly must depend on the complete build matrix, got %q", got)
	}
	// Cleanup may always run within the admitted job, but its job-level
	// condition must not bypass failed architecture builds.
	if strings.Contains(scalarString(mappingValue(assembly.node, "if")), "always()") {
		t.Fatal("assembly bypasses the matrix success barrier")
	}

	foundBuild, foundAssembly := false, false

	for _, step := range build.steps() {
		run := scalarString(mappingValue(step, "run"))
		if strings.Contains(run, "multiarch-image build-arch") {
			foundBuild = strings.Contains(run, "--stage")
		}
	}

	for _, step := range assembly.steps() {
		run := scalarString(mappingValue(step, "run"))
		if strings.Contains(run, "multiarch-image assemble") {
			foundAssembly = strings.Contains(run, "--from-layouts")
		}
	}

	if !foundBuild || !foundAssembly {
		t.Fatal("signer jobs must transfer staged layouts and publish only in assembly")
	}
}

// The signer-image example is what an organization copies to publish the
// engine binary and build its signer image from its copy of this repository.
// Every job runs on the repository's runner, checks the engine out at one
// placeholder commit under a placeholder owner, verifies that checkout
// against the same placeholder before anything else runs, and then uses only
// actions of that checkout; the binary job also sets Go up. The one secret is
// the registry token, and the workflow starts by hand or by a change to
// itself, never by a call.
func TestSignerImageExampleBuildsFromOnePinnedEngineCheckout(t *testing.T) {
	t.Parallel()

	document := loadDocument(t, signerImageExample)
	parsed := loadWorkflow(t, signerImageExample)

	if got := scalarString(mappingValue(mappingValue(document, "env"), "FORGE_CI_COMMIT_SHA")); got != enginePinPlaceholder {
		t.Errorf("FORGE_CI_COMMIT_SHA is %q, want the placeholder %s", got, enginePinPlaceholder)
	}

	events := make([]string, 0, 2)
	for _, event := range mappingEntries(mappingValue(document, "on")) {
		events = append(events, scalarString(event.Key))
	}

	if slices.Sort(events); !slices.Equal(events, []string{"push", "workflow_dispatch"}) {
		t.Errorf("the example starts on %q, want a manual run and a push", events)
	}

	paths := sequenceValues(mappingValue(mappingValue(mappingValue(document, "on"), "push"), "paths"))
	if len(paths) != 1 || scalarString(paths[0]) != ".forgejo/workflows/signer-image.yml" {
		t.Error("a push must rebuild the image only when the workflow file, which holds the pin, changes")
	}

	if len(parsed.jobs) != 3 {
		t.Fatalf("the example has %d jobs, want the binary, the per-architecture build and the manifest assembly", len(parsed.jobs))
	}

	if got := scalarString(mappingValue(mappingValue(document, "env"), "FORGE_CI_REPOSITORY_URL")); got != "${{ forgejo.server_url }}/OWNER/forge-ci" {
		t.Errorf("FORGE_CI_REPOSITORY_URL is %q, want the repository the checkouts clone: the resolver finds the binary under its owner", got)
	}

	for name, target := range parsed.jobs {
		assertSignerImageJob(t, name, target)
	}

	requireWalkReaches(t, document, "secrets.RELEASE_REGISTRY_TOKEN", signerImageExample)
	requireWalkReaches(t, document, "--containerfile "+signerContainerfile, signerImageExample)
}

// The engine binary is published before any job runs it. The first job of the
// example builds it with the one script that defines the build, from modules
// it downloaded itself, compares the build with the checksum of the pinned
// commit before the step that holds the token, and runs no engine: there is
// none to run yet. Every other job waits for it.
func TestSignerImageExamplePublishesTheEngineBinaryFirst(t *testing.T) {
	t.Parallel()

	parsed := loadWorkflow(t, signerImageExample)

	publish, found := parsed.jobs[publishBinaryJob]
	if !found {
		t.Fatalf("the example has no job %s", publishBinaryJob)
	}

	if mentions(publish.node, "run_forge_ci") || mentions(publish.node, "needs") {
		t.Errorf("job %s must run first and without the engine it publishes", publishBinaryJob)
	}

	var build, upload ast.Node

	for _, step := range publish.steps() {
		switch run := scalarString(mappingValue(step, "run")); {
		case strings.Contains(run, binaryScriptPath+" build "):
			build = step
		case strings.Contains(run, binaryScriptPath+" publish "):
			upload = step
		}
	}

	if build == nil || upload == nil {
		t.Fatalf("job %s must build and publish with %s, in two steps", publishBinaryJob, binaryScriptPath)
	}

	assertOrderedFragments(t, "the build step", scalarString(mappingValue(build, "run")), []string{
		`export GOMODCACHE="${RUNNER_TEMP:?}/engine-modules"`,
		"go list -deps ./cmd/forge-ci",
		binaryScriptPath + " build .forgejo/bin/forge-ci",
		"sha256sum --check --quiet bin/forge-ci.sha256",
		"exit 1",
	})

	if anyString(build, secretReference.MatchString) {
		t.Error("the build step holds a secret; the token belongs to the upload step alone")
	}

	if got := scalarString(mappingValue(mappingValue(upload, "env"), "FORGE_CI_PACKAGE_TOKEN")); got != "${{ secrets.RELEASE_REGISTRY_TOKEN }}" {
		t.Errorf("the upload step takes its token from %q, want the registry token", got)
	}

	for name, target := range parsed.jobs {
		if name == publishBinaryJob {
			continue
		}

		if !waitsFor(parsed, name, publishBinaryJob) {
			t.Errorf("job %s runs the engine and does not wait for %s", name, publishBinaryJob)
		}

		if mentions(target.node, binaryScriptPath) {
			t.Errorf("job %s builds the engine itself; it runs the published one", name)
		}
	}
}

// waitsFor reports whether job name needs the wanted job, directly or through
// the jobs it needs.
func waitsFor(parsed workflow, name, wanted string) bool {
	needs := mappingValue(parsed.jobs[name].node, "needs")

	listed := sequenceValues(needs)

	direct := make([]string, 0, 1+len(listed))
	direct = append(direct, scalarString(needs))

	for _, value := range listed {
		direct = append(direct, scalarString(value))
	}

	for _, needed := range direct {
		if needed == wanted || (needed != "" && needed != name && waitsFor(parsed, needed, wanted)) {
			return true
		}
	}

	return false
}

// assertSignerImageJob holds one job of the signer-image example to its
// shape: the repository's runner, the pinned engine checkout, its
// verification, then only actions of that checkout, and no secret but the
// registry token. The binary job also sets Go up, with a pinned action.
func assertSignerImageJob(t *testing.T, name string, target job) {
	t.Helper()

	localAction := regexp.MustCompile(`^\./\.forgejo/actions/([a-z0-9-]+)$`)
	pinnedSetupGo := regexp.MustCompile(`^https://code\.forgejo\.org/actions/setup-go@[0-9a-f]{40}$`)
	pinnedForgejoAction := regexp.MustCompile(`^https://code\.forgejo\.org/actions/checkout@[0-9a-f]{40}$`)

	if got := target.runsOn(); got != repositoryRunner {
		t.Errorf("job %s runs on %q, want %q", name, got, repositoryRunner)
	}

	steps := target.steps()
	if len(steps) < 4 {
		t.Fatalf("job %s has %d steps", name, len(steps))
	}

	guard := steps[0]
	if scalarString(mappingValue(mappingValue(guard, "env"), "PUBLICATION_REF")) != "${{ forgejo.ref }}" ||
		!strings.Contains(scalarString(mappingValue(guard, "run")), `if [ "${PUBLICATION_REF:?}" != refs/heads/main ]; then`) ||
		!strings.Contains(scalarString(mappingValue(guard, "run")), "exit 1") ||
		mappingValue(guard, "if") != nil || mappingValue(guard, "continue-on-error") != nil {
		t.Errorf("job %s must first refuse a mistaken side-branch dispatch, with the ref passed through env", name)
	}

	checkout := steps[1]
	with := mappingValue(checkout, "with")

	if !pinnedForgejoAction.MatchString(scalarString(mappingValue(checkout, "uses"))) ||
		scalarString(mappingValue(with, "repository")) != "OWNER/forge-ci" ||
		scalarString(mappingValue(with, "ref")) != enginePinPlaceholder ||
		scalarString(mappingValue(with, "persist-credentials")) != "false" {
		t.Errorf("job %s does not check the engine out after the branch guard at the placeholder pin, without persisted credentials", name)
	}

	if verify := scalarString(mappingValue(steps[2], "run")); !strings.Contains(verify, `git rev-parse HEAD`) ||
		!strings.Contains(verify, `"${actual_sha}" != "${FORGE_CI_COMMIT_SHA:?}"`) || !strings.Contains(verify, "exit 1") {
		t.Errorf("job %s does not verify the engine checkout against FORGE_CI_COMMIT_SHA immediately after checkout", name)
	}

	for _, step := range steps[3:] {
		uses := scalarString(mappingValue(step, "uses"))
		if uses == "" {
			continue
		}

		if name == publishBinaryJob && pinnedSetupGo.MatchString(uses) {
			if with := mappingValue(step, "with"); scalarString(mappingValue(with, "go-version-file")) != "go.mod" ||
				scalarString(mappingValue(with, "cache")) != "false" {
				t.Errorf("job %s must set up exactly the Go release of go.mod, without a cache", name)
			}

			continue
		}

		match := localAction.FindStringSubmatch(uses)
		if match == nil {
			t.Errorf("job %s uses %q after the checkout, want an action of the verified engine checkout", name, uses)

			continue
		}

		if info, err := os.Stat(filepath.Join(reporoot.Path(t), ".forgejo", "actions", match[1], "action.yml")); err != nil || !info.Mode().IsRegular() {
			t.Errorf("job %s uses %s, which this checkout lacks", name, uses)
		}
	}

	allowedReference := "secrets.SIGNER_REGISTRY_TOKEN"
	if name == publishBinaryJob {
		allowedReference = "secrets.RELEASE_REGISTRY_TOKEN"
	}

	secret := regexp.MustCompile(`secrets\.[A-Za-z0-9_]+`)

	anyString(target.node, func(value string) bool {
		for _, found := range secret.FindAllString(value, -1) {
			if found != allowedReference {
				t.Errorf("job %s reads %s; only %s belongs to its publication destination", name, found, allowedReference)
			}
		}

		return false
	})
}

// The signer build's context is the repository root, cut down by the ignore
// file named after the Containerfile to exactly what the image copies: a
// build must not receive the Git history or the layer's binary, and a COPY
// the ignore file does not admit would fail the build. The file belongs to
// this Containerfile alone; a root .containerignore would cut the context of
// the runtime images too.
func TestSignerBuildContextIsWhatTheImageCopies(t *testing.T) {
	t.Parallel()

	var copied []string

	for line := range strings.SplitSeq(string(reporoot.ReadFile(t, signerContainerfile)), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "COPY" && !strings.HasPrefix(fields[1], "--") {
			copied = append(copied, fields[1:len(fields)-1]...)
		}
	}

	if len(copied) == 0 {
		t.Fatal("the signer Containerfile copies nothing from its context; the check proves nothing")
	}

	var patterns []string

	for line := range strings.SplitSeq(string(reporoot.ReadFile(t, signerContextIgnores)), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, line)
		}
	}

	want := make([]string, 0, 1+len(copied))
	want = append(want, "*")

	for _, source := range copied {
		want = append(want, "!"+source)
	}

	if !slices.Equal(patterns, want) {
		t.Errorf("%s holds %q, want everything ignored but the copied files: %q", signerContextIgnores, patterns, want)
	}

	if _, err := os.Stat(filepath.Join(reporoot.Path(t), ".containerignore")); err == nil {
		t.Error("a root .containerignore applies to every image built from the repository root, the runtime images included")
	}
}
