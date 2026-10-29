// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The input surface of sign-and-publish is pinned by
// TestSignPublishWorkflowCallSurface and the signer job counts by
// signerWorkflows; what remains is the coordinate preflight every helper
// workflow shares.

const (
	// The coordinates of an organization that is not the upstream one, as a
	// fork's consumers pass them.
	exampleServer       = "https://forge.example"
	exampleRepository   = exampleServer + "/owner/delivery.git"
	exampleSigner       = "forge.example/owner/signer@sha256:9a51d9f761cd269cadb2066710c9d2dd293f4e1020e229ff4f47ee1e6bce6234"
	validatedRepository = "${{ needs.coordinates.outputs.helpers_repository }}"
	versionBumpImage    = "${{ needs.coordinates.outputs.version_bump_image }}"
	builderJobImage     = "${{ needs.coordinates.outputs.builder_job_image }}"
	helperFallback      = `origin '+refs/heads/*:refs/remotes/origin/*' '+refs/tags/*:refs/tags/*'`
	helperRemote        = `remote add origin "${FORGE_CI_REPOSITORY_URL:?}"`
	// builderActions is where a builder workflow's container job finds the
	// actions it may run: its own verified helper checkout.
	builderActions = "./forge-ci-layer/.forgejo/actions/"
)

// coordinateWorkflows take the helper repository as a public coordinate,
// separate from the exact commit input, and validate it in a secret-free
// coordinates job. checkouts is how many helper checkouts the workflow runs.
// A builder workflow takes no signer image: its container is the image
// builder's job image, which the coordinates job validates from the caller's
// variables instead.
func coordinateWorkflows() map[string]coordinateContract {
	return map[string]coordinateContract{
		".forgejo/workflows/prepare-release.yml":               {"forge-ci-sha", 3, false},
		".forgejo/workflows/sign-and-publish-release.yml":      {"forge-ci-sha", 4, false},
		".forgejo/workflows/sign-container-images.yml":         {"forge-ci-sha", 1, false},
		".forgejo/workflows/promote-base-container-images.yml": {"forge-ci-sha", 1, false},
		".forgejo/workflows/verify-base-container-images.yml":  {"forge-ci-sha", 1, false},
		".forgejo/workflows/release-images.yml":                {"forge-ci-sha", 4, true},
		".forgejo/workflows/snapshot-images.yml":               {"forge-ci-sha", 6, true},
	}
}

type coordinateContract struct {
	shaInput  string
	checkouts int
	builder   bool
}

var (
	// rawCoordinateInput is an unvalidated coordinate, in any spelling.
	rawCoordinateInput = regexp.MustCompile(`inputs\s*(\.|\[\s*['"])(forge-ci-repository|signer-image)\b`)
	secretsWord        = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])secrets([^A-Za-z0-9_]|$)`)
	fetchMain          = regexp.MustCompile(`fetch .* origin main(\s|$)`)
	signerPin          = regexp.MustCompile(`@sha256:[0-9a-f]{64}`)
)

func workflowInput(root ast.Node, name string) ast.Node {
	return mappingValue(mappingValue(mappingValue(mappingValue(root, "on"), "workflow_call"), "inputs"), name)
}

// keyValues returns the value of every mapping entry named key beneath node.
func keyValues(node ast.Node, key string) []string {
	var values []string

	for _, entry := range mappingEntries(node) {
		if scalarString(entry.Key) == key {
			values = append(values, scalarString(entry.Value))
		}

		values = append(values, keyValues(entry.Value, key)...)
	}

	for _, item := range sequenceValues(node) {
		values = append(values, keyValues(item, key)...)
	}

	return values
}

func isSignerWorkflow(relative string) bool {
	_, ok := signerWorkflows()[relative]

	return ok
}

// validateStep returns the coordinates job's validation step.
func validateStep(t *testing.T, parsed workflow, relative string) ast.Node {
	t.Helper()

	step, ok := parsed.jobs["coordinates"].stepByID("validate")
	if !ok {
		t.Fatalf("%s: the coordinates job has no validate step", relative)
	}

	return step
}

// The helper repository is an optional input without a literal default, so
// the caller's variable can supply it. The signer image and the Cosign trust
// root are the calling organization's: required, with no default this
// repository could only fill with someone's coordinates. The exact commit
// stays a separate required input.
func TestCoordinateInputsAreSeparateFromTheExactSHA(t *testing.T) {
	t.Parallel()

	for relative, contract := range coordinateWorkflows() {
		root, err := parseDocument(reporoot.ReadFile(t, relative))
		if err != nil {
			t.Fatalf("%s: %v", relative, err)
		}

		repository := workflowInput(root, "forge-ci-repository")
		if scalarString(mappingValue(repository, "required")) != "false" || scalarString(mappingValue(repository, "default")) != "" {
			t.Errorf("%s: input forge-ci-repository must be optional with an empty default", relative)
		}

		required := []string{contract.shaInput}
		if !contract.builder {
			required = append(required, "signer-image")
		}

		if isSignerWorkflow(relative) {
			required = append(required, "cosign-public-key", "cosign-public-key-sha256")
		}

		for _, name := range required {
			input := workflowInput(root, name)
			if scalarString(mappingValue(input, "required")) != "true" {
				t.Errorf("%s: input %s must be required", relative, name)
			}

			if mappingValue(input, "default") != nil {
				t.Errorf("%s: input %s must have no default", relative, name)
			}
		}
	}
}

// Only the listed workflows take a coordinate as an input. A new one that
// did would face none of the preflight rules above, exactly as an unlisted
// signer workflow would escape the bootstrap contract, which is why its
// sibling guard exists next door.
func TestNoUnlistedWorkflowTakesACoordinateInput(t *testing.T) {
	t.Parallel()

	for _, relative := range allWorkflows(t) {
		if _, known := coordinateWorkflows()[relative]; known {
			continue
		}

		root, err := parseDocument(reporoot.ReadFile(t, relative))
		if err != nil {
			t.Errorf("%s: %v", relative, err)

			continue
		}

		for _, name := range []string{"forge-ci-repository", "signer-image"} {
			if workflowInput(root, name) != nil {
				t.Errorf("%s takes the %s input but is not listed in coordinateWorkflows", relative, name)
			}
		}
	}
}

// A secret-free, container-free coordinates job validates the raw inputs, and
// every other job reads only its outputs, after it: no job may consume a raw
// coordinate, run a signer container from anything but the validated image, or
// start before the preflight finished.
func TestCoordinatesArePreflightedBeforeUse(t *testing.T) {
	t.Parallel()

	for relative, contract := range coordinateWorkflows() {
		parsed := loadWorkflow(t, relative)

		coordinates, ok := parsed.jobs["coordinates"]
		if !ok {
			t.Errorf("%s: coordinates job is missing", relative)

			continue
		}

		assertCoordinatesJob(t, relative, contract, parsed, coordinates)

		consumers, containers := 0, 0

		for name, target := range parsed.jobs {
			if name == "coordinates" {
				continue
			}

			consumer, container := assertCoordinateConsumerJob(t, relative, contract, name, target)
			if consumer {
				consumers++
			}

			if container {
				containers++
			}
		}

		if consumers == 0 {
			t.Errorf("%s: no job consumes the coordinates outputs", relative)
		}

		if isSignerWorkflow(relative) && containers == 0 {
			t.Errorf("%s: no job runs the signer container", relative)
		}
	}
}

// assertCoordinatesJob checks that the coordinates job validates the raw
// inputs on the host, without secrets, and exposes them as outputs.
func assertCoordinatesJob(t *testing.T, relative string, contract coordinateContract, parsed workflow, coordinates job) {
	t.Helper()

	if mappingValue(coordinates.node, "container") != nil {
		t.Errorf("%s: the coordinates job must not run in a container", relative)
	}

	requireWalkReaches(t, coordinates.node, "HELPERS_REPOSITORY_INPUT", relative+" coordinates")

	if anyString(coordinates.node, secretsWord.MatchString) {
		t.Errorf("%s: the coordinates job references secrets", relative)
	}

	// HELPERS_REPOSITORY_INPUT is held by
	// TestHelperRepositoryIsTheCallersInputOrVariable.
	wiring := map[string]string{
		"SERVER_URL":         "${{ forgejo.server_url }}",
		"SIGNER_IMAGE_INPUT": "${{ inputs['signer-image'] }}",
	}
	outputs := []string{"helpers_repository", "signer_image"}

	if contract.builder {
		wiring = map[string]string{
			"SERVER_URL":              "${{ forgejo.server_url }}",
			"BUILDER_INPUT":           "${{ vars.CI_IMAGE_BUILDER }}",
			"BUILDER_JOB_IMAGE_INPUT": "${{ vars.CI_IMAGE_BUILDER_JOB_IMAGE }}",
		}
		outputs = []string{"helpers_repository", "builder", "builder_job_image"}
	}

	if isSignerWorkflow(relative) {
		wiring["TRANSPARENCY_LOG_INPUT"] = "${{ inputs['transparency-log'] }}"

		outputs = append(outputs, "transparency_log")
	}

	env := mappingValue(validateStep(t, parsed, relative), "env")
	for key, want := range wiring {
		if got := scalarString(mappingValue(env, key)); got != want {
			t.Errorf("%s: validate step sets %s to %q, want %q", relative, key, got, want)
		}
	}

	for _, output := range outputs {
		got := scalarString(mappingValue(mappingValue(coordinates.node, "outputs"), output))
		if want := "${{ steps.validate.outputs." + output + " }}"; got != want {
			t.Errorf("%s: coordinates output %s is %q, want %q", relative, output, got, want)
		}
	}
}

// assertCoordinateConsumerJob checks that a job reads only validated
// coordinates and, when it runs a container, runs the validated signer image
// with no action from another pin. It reports whether the job consumes the
// coordinates outputs and whether it runs a container.
func assertCoordinateConsumerJob(t *testing.T, relative string, contract coordinateContract, name string, target job) (bool, bool) {
	t.Helper()

	requireWalkReaches(t, target.node, "runs-on", relative+" job "+name)

	if anyString(target.node, rawCoordinateInput.MatchString) {
		t.Errorf("%s: job %s reads an unvalidated coordinate input", relative, name)
	}

	consumer := mentions(target.node, "needs.coordinates.")
	if consumer && !slices.Contains(jobNeeds(target), "coordinates") {
		t.Errorf("%s: job %s reads coordinates outputs without needing the coordinates job", relative, name)
	}

	for _, value := range keyValues(target.node, "FORGE_CI_REPOSITORY_URL") {
		if value != validatedRepository {
			t.Errorf("%s: job %s sets FORGE_CI_REPOSITORY_URL to %q, want %q", relative, name, value, validatedRepository)
		}
	}

	if mappingValue(target.node, "container") == nil {
		return consumer, false
	}

	// Container jobs run only the verified helper checkout, never an action
	// resolved from another pin. A builder job runs the layer's own actions
	// from that checkout.
	for _, step := range target.steps() {
		uses := scalarString(mappingValue(step, "uses"))
		ownAction := contract.builder && strings.HasPrefix(uses, builderActions) && !strings.Contains(uses, "..")

		if uses != "" && !ownAction {
			t.Errorf("%s: container job %s uses action %s", relative, name, uses)
		}
	}

	// A builder job runs the job image the coordinates job validated, and
	// holds no secret: it runs the caller's Containerfile.
	if contract.builder {
		want := builderJobImage
		if relative == ".forgejo/workflows/snapshot-images.yml" && name == "produce" {
			want = "${{ (matrix.artifact.project_type == 'go' && needs.coordinates.outputs.artifact_job_image_go) || ((matrix.artifact.project_type == 'maven' || matrix.artifact.project_type == 'gradle') && needs.coordinates.outputs.artifact_job_image_java) || (matrix.artifact.project_type == 'npm' && needs.coordinates.outputs.artifact_job_image_node) || (matrix.artifact.project_type == 'cargo' && needs.coordinates.outputs.artifact_job_image_rust) || '' }}"
		}
		if image := target.containerImage(); image != want {
			t.Errorf("%s: job %s runs container %q, want the validated %q", relative, name, image, want)
		}

		if anyString(target.node, secretsWord.MatchString) {
			t.Errorf("%s: builder job %s mentions secrets", relative, name)
		}

		return consumer, false
	}

	// The one container job that is not a signer: prepare-release's version
	// bump, in the project's own validated build image, which therefore may
	// not even name a secret.
	if image := target.containerImage(); image == versionBumpImage {
		if relative != ".forgejo/workflows/prepare-release.yml" || name != "version-bump" {
			t.Errorf("%s: job %s runs the version-bump image", relative, name)
		}

		if anyString(target.node, secretsWord.MatchString) {
			t.Errorf("%s: the version-bump job mentions secrets", relative)
		}

		return consumer, false
	}

	if image := target.containerImage(); image != signerImage {
		t.Errorf("%s: job %s runs container %q, want the validated %q", relative, name, image, signerImage)
	}

	return consumer, true
}

// Every helper checkout fetches the validated full repository URL, and falls
// back to advertised branches and tags without assuming a default branch.
func TestHelperCheckoutsFetchTheValidatedRepository(t *testing.T) {
	t.Parallel()

	for relative, contract := range coordinateWorkflows() {
		checkouts := 0

		for name, target := range loadWorkflow(t, relative).jobs {
			for _, step := range target.steps() {
				run := scalarString(mappingValue(step, "run"))
				if !strings.Contains(run, "remote add origin") {
					continue
				}

				checkouts++

				for line := range strings.SplitSeq(run, "\n") {
					if strings.Contains(line, "remote add origin") && !strings.HasSuffix(line, helperRemote) {
						t.Errorf("%s: job %s adds a remote other than the validated coordinate: %s",
							relative, name, strings.TrimSpace(line))
					}
				}

				if !strings.Contains(run, helperFallback) {
					t.Errorf("%s: job %s lacks the branch/tag fetch fallback", relative, name)
				}

				if fetchMain.MatchString(run) {
					t.Errorf("%s: job %s assumes a main branch", relative, name)
				}
			}
		}

		if checkouts != contract.checkouts {
			t.Errorf("%s: %d helper checkout(s), want %d", relative, checkouts, contract.checkouts)
		}
	}
}

// runPreflight runs the validate step's script as the runner would.
func runPreflight(t *testing.T, script, repository, signer string) (bool, string) {
	t.Helper()

	accepted, written, _ := runPreflightWithStderr(t, script, repository, signer)

	return accepted, written
}

// runPreflightWithStderr is runPreflight with what the script said.
//
// The transparency log is the input's default unless env names another value.
func runPreflightWithStderr(t *testing.T, script, repository, signer string, env ...string) (bool, string, string) {
	t.Helper()

	output := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(output, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "bash", "-c", script)
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HELPERS_REPOSITORY_INPUT=" + repository,
		"SERVER_URL=" + exampleServer,
		"SIGNER_IMAGE_INPUT=" + signer,
		"FORGEJO_OUTPUT=" + output,
		"TRANSPARENCY_LOG_INPUT=none",
	}
	command.Env = append(command.Env, env...)

	var stderr strings.Builder

	command.Stderr = &stderr
	err := command.Run()

	written, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}

	return err == nil, string(written), stderr.String()
}

// preflightScript returns the validate step's script: the block scalar's own
// text, not its YAML rendering.
func preflightScript(t *testing.T, relative string) string {
	t.Helper()

	literal, ok := mappingValue(validateStep(t, loadWorkflow(t, relative), relative), "run").(*ast.LiteralNode)
	if !ok {
		t.Fatalf("%s: the validate step's run is not a block scalar", relative)
	}

	return literal.Value.Value
}

// Each preflight accepts a full repository URL and an owner/repository on the
// instance, and emits the URL the helper checkout will fetch.
func TestCoordinatePreflightResolvesTheHelperRepository(t *testing.T) {
	t.Parallel()

	accepted := map[string]string{
		exampleRepository:                    exampleRepository,
		"owner/delivery":                     exampleRepository,
		"Owner-1/delivery.v2_x":              exampleServer + "/Owner-1/delivery.v2_x.git",
		"https://forge.example:8443/o/r":     "https://forge.example:8443/o/r",
		"https://[2001:db8::1]:3000/o/r.git": "https://[2001:db8::1]:3000/o/r.git",
	}

	for relative, contract := range coordinateWorkflows() {
		t.Run(filepath.Base(relative), func(t *testing.T) {
			t.Parallel()

			script := preflightScript(t, relative)

			for repository, fetched := range accepted {
				wantOutput := "helpers_repository=" + fetched + "\nsigner_image=" + exampleSigner + "\n"
				if contract.builder {
					wantOutput = "helpers_repository=" + fetched + "\nbuilder=buildah\nbuilder_job_image=\n"
				}

				if isSignerWorkflow(relative) {
					wantOutput += "transparency_log=none\n"
				}

				if ok, output := runPreflight(t, script, repository, exampleSigner); !ok || output != wantOutput {
					t.Errorf("repository %q: accepted=%t output %q, want %q", repository, ok, output, wantOutput)
				}
			}

			if contract.builder {
				want := "helpers_repository=" + exampleRepository + "\nbuilder=kaniko\nbuilder_job_image=" + exampleSigner + "\n"
				if ok, output, _ := runPreflightWithStderr(t, script, exampleRepository, "", "BUILDER_INPUT=kaniko", "BUILDER_JOB_IMAGE_INPUT="+exampleSigner); !ok || output != want {
					t.Errorf("kaniko: accepted=%t output %q, want %q", ok, output, want)
				}
			}
		})
	}
}

// Each preflight rejects credential-bearing, ambiguous, or mutable coordinates
// and a missing signer image, without emitting anything.
func TestCoordinatePreflightRejectsUnsafeCoordinates(t *testing.T) {
	t.Parallel()

	repositories := []string{
		"",
		"https://user@example.invalid/delivery.git",
		"http://forge.example/owner/delivery.git",
		"https://forge.example/owner/delivery.git?ref=main",
		"https://forge.example/owner/delivery.git#main",
		"https://forge.example",
		"https://forge.example:bad/owner/delivery.git",
		"https://forge.example:0/owner/delivery.git",
		"https://forge.example:65536/owner/delivery.git",
		`https://forge.example/owner\delivery.git`,
		"https://forge.example/owner/delivery.git\nother",
		"delivery",
		"owner/delivery/extra",
		"owner/delivery?ref=main",
		"../owner/delivery",
		"-owner/delivery",
		"owner/ delivery",
		"git@forge.example:owner/delivery.git",
		"ssh://git@forge.example/owner/delivery.git",
	}
	signerRepository, digest, _ := strings.Cut(exampleSigner, "@sha256:")
	signers := []string{
		"",
		signerRepository + ":latest",
		signerRepository + "@sha256:" + strings.ToUpper(digest),
		signerRepository + "@sha256:" + digest[:63],
		"signer@sha256:" + digest,
	}

	for relative, contract := range coordinateWorkflows() {
		t.Run(filepath.Base(relative), func(t *testing.T) {
			t.Parallel()

			script := preflightScript(t, relative)

			for _, repository := range repositories {
				if ok, output := runPreflight(t, script, repository, exampleSigner); ok || output != "" {
					t.Errorf("repository %q: accepted=%t output %q", repository, ok, output)
				}
			}

			// A builder workflow's image is the kaniko job image, held to the
			// signer image's digest rule, and its builder is one of two.
			if contract.builder {
				if ok, output, _ := runPreflightWithStderr(t, script, exampleRepository, "", "BUILDER_INPUT=docker"); ok || output != "" {
					t.Errorf("builder docker: accepted=%t output %q", ok, output)
				}

				for _, image := range signers {
					if ok, output, _ := runPreflightWithStderr(t, script, exampleRepository, "", "BUILDER_INPUT=kaniko", "BUILDER_JOB_IMAGE_INPUT="+image); ok || output != "" {
						t.Errorf("kaniko job image %q: accepted=%t output %q", image, ok, output)
					}
				}

				return
			}

			for _, signer := range signers {
				if ok, output := runPreflight(t, script, exampleRepository, signer); ok || output != "" {
					t.Errorf("signer %q: accepted=%t output %q", signer, ok, output)
				}
			}
		})
	}
}

// The layer and its examples pin no signer image: the engine publishes none,
// so a digest here could only be some organization's.
func TestLayerPinsNoSignerImage(t *testing.T) {
	t.Parallel()

	root := reporoot.Path(t)
	files := 0

	for _, dir := range append([]string{".forgejo/workflows", ".forgejo/actions"}, forgejoExampleTrees()...) {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || !entry.Type().IsRegular() {
				return err
			}

			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			// The kaniko bootstrap pins kaniko's own debug image, the one image
			// no kaniko job can build; it is not a signer image.
			if isSelfCIWorkflow(filepath.ToSlash(relative)) || filepath.ToSlash(relative) == kanikoJobImageExample {
				return nil
			}

			files++

			if pin := signerPin.FindString(string(reporoot.ReadFile(t, relative))); pin != "" {
				t.Errorf("%s pins an image by digest (%s); the signer image is the caller's input", relative, pin)
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if files == 0 {
		t.Error("no layer file found; the scan proves nothing")
	}
}
