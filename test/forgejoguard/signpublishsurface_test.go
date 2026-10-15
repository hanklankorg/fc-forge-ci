// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// These invariants were previously asserted by grepping the workflow text for
// input names, step names, and occurrence counts. A count proves only that a
// string appears somewhere in the file, not that the job which needs it has it,
// so the checks below name the job or step that must carry each property.

type inputContract struct {
	required bool
	value    string
}

// signPublishInputs is the reusable workflow's complete public surface. A new
// input is a consumer-visible contract change and must be added here.
func signPublishInputs() map[string]inputContract {
	return map[string]inputContract{
		"runner-label":                 {required: true},
		"release-sha":                  {required: true},
		"release-request-sha":          {required: true},
		"release-tag":                  {required: true},
		"dist-digest":                  {required: true},
		"caller-workflow":              {required: true},
		"forge-ci-sha":                 {required: true},
		"signer-image":                 {required: true},
		"cosign-public-key":            {required: true},
		"cosign-public-key-sha256":     {required: true},
		"forge-ci-repository":          {value: ""},
		"artifact-name":                {value: "build-dist"},
		"artifact-path":                {value: "dist/"},
		"release-images-path":          {value: ""},
		"base-repository":              {value: ""},
		"publication-image-repository": {value: ""},
		"registry-user":                {value: ""},
		"release-draft":                {value: "false"},
		"sign-distro-packages":         {value: "true"},
		"release-asset-retention":      {value: ""},
		"transparency-log":             {value: "none"},
	}
}

// signerJobs run inside the signer image. coordinates stays outside it.
func signerJobs() []string {
	return []string{"sign", "promote", "publish", "cleanup"}
}

// tagVerifyingJobs must re-verify that the release tag still points at the
// release SHA before they act on it. cleanup is excluded: it deletes candidate
// tags and verifies the dist digest instead.
func tagVerifyingJobs() []string {
	return []string{"sign", "promote", "publish"}
}

// The reusable workflow's inputs, their required flags, and their defaults are
// the contract consumers pin. Defaults are asserted by value: a changed default
// silently changes every consumer that omits the input.
func TestSignPublishWorkflowCallSurface(t *testing.T) {
	t.Parallel()

	root, err := parseDocument(reporoot.ReadFile(t, signPublishWorkflow))
	if err != nil {
		t.Fatalf("%s: %v", signPublishWorkflow, err)
	}

	inputs := mappingValue(mappingValue(mappingValue(root, "on"), "workflow_call"), "inputs")
	if inputs == nil {
		t.Fatalf("%s declares no workflow_call inputs", signPublishWorkflow)
	}

	seen := make(map[string]bool)

	for _, entry := range mappingEntries(inputs) {
		name := scalarString(entry.Key)
		seen[name] = true

		want, known := signPublishInputs()[name]
		if !known {
			t.Errorf("input %q is not in the declared public surface", name)

			continue
		}

		required := scalarString(mappingValue(entry.Value, "required")) == "true"
		if required != want.required {
			t.Errorf("input %q required is %t, want %t", name, required, want.required)
		}

		if want.required {
			// A default on a required input would stand in for a value only
			// the caller can give.
			if mappingValue(entry.Value, "default") != nil {
				t.Errorf("required input %q has a default", name)
			}

			continue
		}

		if got := scalarString(mappingValue(entry.Value, "default")); got != want.value {
			t.Errorf("input %q defaults to %q, want %q", name, got, want.value)
		}
	}

	for name := range signPublishInputs() {
		if !seen[name] {
			t.Errorf("input %q is missing", name)
		}
	}
}

// The secret-free coordinates job gates the whole ladder, and the jobs after it
// run unconditionally: an `if:` on one of them would let a release publish
// while an earlier stage was skipped.
func TestSignPublishJobLadder(t *testing.T) {
	t.Parallel()

	wantNeeds := map[string][]string{
		"coordinates": nil,
		"sign":        {"coordinates"},
		"promote":     {"coordinates", "sign"},
		"publish":     {"coordinates", "sign", "promote"},
		"cleanup":     {"coordinates", "publish"},
	}

	parsed := loadWorkflow(t, signPublishWorkflow)
	if len(parsed.jobs) != len(wantNeeds) {
		t.Errorf("workflow declares %d jobs, want %d", len(parsed.jobs), len(wantNeeds))
	}

	for name, want := range wantNeeds {
		target, ok := parsed.jobs[name]
		if !ok {
			t.Errorf("job %s is missing", name)

			continue
		}

		if got := jobNeeds(target); !equalStrings(got, want) {
			t.Errorf("job %s needs %v, want %v", name, got, want)
		}

		if name == "coordinates" {
			continue
		}

		if condition := scalarString(mappingValue(target.node, "if")); condition != "" {
			t.Errorf("job %s is conditional on %q; every stage after coordinates runs unconditionally",
				name, condition)
		}
	}
}

// actingCommand matches the engine calls that sign, move or publish release
// material.
var actingCommand = regexp.MustCompile(
	`run_forge_ci (?:release (?:gpg sign-packages|sign|provenance|publish)|container release-images [a-z]+)(?:\s|$)`)

// Each job that acts on the release re-verifies the tag itself, in an
// unconditional step ahead of its first acting step. Verifying once in an
// earlier job would leave a window in which the tag is moved.
func TestReleaseTagIsVerifiedInEveryActingJob(t *testing.T) {
	t.Parallel()

	const verify = "run_forge_ci release validate-tag"

	parsed := loadWorkflow(t, signPublishWorkflow)
	for _, name := range tagVerifyingJobs() {
		target, ok := parsed.jobs[name]
		if !ok {
			t.Errorf("job %s is missing", name)

			continue
		}

		verified, acted := -1, -1

		for index, step := range target.steps() {
			run := shellCommands(scalarString(mappingValue(step, "run")))

			if verified < 0 && strings.Contains(run, verify) {
				verified = index

				if mappingValue(step, "if") != nil || mappingValue(step, "continue-on-error") != nil {
					t.Errorf("job %s verifies the release tag in a step that can be skipped or can fail open", name)
				}
			}

			if acted < 0 && actingCommand.MatchString(run) {
				acted = index
			}
		}

		switch {
		case acted < 0:
			t.Errorf("job %s has no acting step, so the order check proves nothing", name)
		case verified < 0:
			t.Errorf("job %s does not run `release validate-tag`", name)
		case verified > acted:
			t.Errorf("job %s acts on the release in step %d, before it verifies the tag in step %d", name, acted+1, verified+1)
		}
	}
}

// shellCommands returns a run script without its comment lines, so a command
// that is only mentioned in a comment does not count as run.
func shellCommands(run string) string {
	lines := strings.Split(run, "\n")

	return strings.Join(slices.DeleteFunc(lines, func(line string) bool {
		return strings.HasPrefix(strings.TrimSpace(line), "#")
	}), "\n")
}

// Every signer job re-establishes the trusted helper checkout for itself: a
// private workspace, a detached checkout of the expected SHA, and a rev-parse
// that proves the checkout landed on it. Jobs do not inherit this from
// each other, so each one must carry all three.
func TestEverySignerJobPinsItsHelperCheckout(t *testing.T) {
	t.Parallel()

	pins := []string{
		`mktemp -d /tmp/forge-ci-signer.XXXXXX`,
		`git -C "${helpers_dir}" checkout --quiet --detach "${EXPECTED_HELPERS_SHA}"`,
		`actual_sha=$(git -C "${HELPERS_DIR:?}" rev-parse HEAD)`,
	}

	parsed := loadWorkflow(t, signPublishWorkflow)
	for _, name := range signerJobs() {
		target, ok := parsed.jobs[name]
		if !ok {
			t.Errorf("job %s is missing", name)

			continue
		}

		for _, pin := range pins {
			if !jobRunsCommand(target, pin) {
				t.Errorf("job %s does not pin its helper checkout with %q", name, pin)
			}
		}
	}
}

// credentialWiring is which secret feeds each credential env name. The channel
// test asserts which step may hold a credential; this asserts that the value it
// holds is the intended secret, so a step cannot keep its approved env name
// while being fed a different credential.
func credentialWiring() map[string]string {
	return map[string]string{ //nolint:gosec // G101: names of environment variables and secrets, no values.
		"COSIGN_KEY":                  "secrets.RELEASE_COSIGN_PRIVATE_KEY",
		"COSIGN_PASSWORD":             "secrets.RELEASE_COSIGN_PASSPHRASE",
		"GPG_SIGNING_KEY":             "secrets.RELEASE_GPG_PRIVATE_KEY",
		"GPG_SIGNING_PASSWORD":        "secrets.RELEASE_GPG_PASSPHRASE",
		"GPG_SIGNING_FINGERPRINT":     "secrets.RELEASE_GPG_FINGERPRINT",
		"REGISTRY_TOKEN":              "secrets.RELEASE_REGISTRY_TOKEN",
		"REGISTRY_USER":               "forgejo.repository_owner",
		"FORGE_CI_PACKAGE_API_TOKEN":  "secrets.RELEASE_REGISTRY_TOKEN",
		"PUBLISH_RELEASE_TOKEN_INPUT": "secrets.RELEASE_TOKEN",
	}
}

// Every credential env name in the workflow is fed by its intended secret.
func TestCredentialEnvNamesCarryTheirIntendedSecret(t *testing.T) {
	t.Parallel()

	checked := 0

	for jobName, target := range loadWorkflow(t, signPublishWorkflow).jobs {
		for _, step := range target.steps() {
			stepName := scalarString(mappingValue(step, "name"))
			for _, entry := range mappingEntries(mappingValue(step, "env")) {
				want, known := credentialWiring()[scalarString(entry.Key)]
				if !known {
					continue
				}

				checked++

				if got := scalarString(entry.Value); !strings.Contains(got, want) {
					t.Errorf("job %s step %q sets %s to %q, want it fed by %s",
						jobName, stepName, scalarString(entry.Key), got, want)
				}
			}
		}
	}

	if checked < len(credentialWiring()) {
		t.Errorf("only %d credential env assignments were found, want at least %d; the walk missed some",
			checked, len(credentialWiring()))
	}
}

// signingCommand matches the engine calls that write a signature, a bundle or
// the provenance statement next to the release files.
var signingCommand = regexp.MustCompile(
	`run_forge_ci (?:release (?:gpg sign-packages|sign|provenance)|container release-images sign)(?:\s|$)`)

// The sign job checks the producer's hand-off before it signs anything: the
// engine's `release files validate --unsigned` refuses a signature, a bundle
// or a provenance statement that is already declared or already in the dist,
// which finalization would otherwise publish as the signer's. The step is
// unconditional, reads the dist only after its digest was verified, and comes
// before every signing step. The engine-tagged
// TestUnsignedHandOffStepThroughTheEngine runs it through the built binary.
func TestUnsignedHandOffIsCheckedBeforeAnySigning(t *testing.T) {
	t.Parallel()

	const (
		stepName   = "Verify unsigned release file hand-off"
		distDigest = "run_forge_ci release validate-dist"
	)

	checked, verified, signed := -1, -1, -1

	for index, step := range loadWorkflow(t, signPublishWorkflow).jobs["sign"].steps() {
		run := shellCommands(scalarString(mappingValue(step, "run")))

		switch {
		case scalarString(mappingValue(step, "name")) == stepName:
			checked = index

			if mappingValue(step, "if") != nil || mappingValue(step, "continue-on-error") != nil {
				t.Errorf("step %q can be skipped or can fail open", stepName)
			}

			assertOrderedFragments(t, stepName, run, []string{
				"run_forge_ci release files validate", "--unsigned",
				`--manifest "${RELEASE_FILES_MANIFEST:?}"`, `--dist-dir "${DIST_DIR:?}"`,
			})
		case verified < 0 && strings.Contains(run, distDigest):
			verified = index
		case signed < 0 && signingCommand.MatchString(run):
			signed = index
		}
	}

	switch {
	case checked < 0:
		t.Fatalf("the sign job has no step %q", stepName)
	case signed < 0:
		t.Fatal("the sign job has no signing step, so the order check proves nothing")
	case verified < 0 || verified > checked:
		t.Errorf("step %q reads the dist before `release validate-dist` verified its digest", stepName)
	case signed < checked:
		t.Errorf("the sign job signs in step %d, before it checks the hand-off in step %d", signed+1, checked+1)
	}
}

// optionalLedgerSteps act on the container image ledger, which is optional,
// each through the engine verb named here. The engine owns the ledger
// question: with --allow-missing-ledger it ends a release without images before it
// reads a credential, and it still refuses a ledger the caller named that is
// absent. The engine-tagged TestOptionalLedgerStepsThroughTheEngine shows
// both through the built binary.
func optionalLedgerSteps() map[string]string {
	return map[string]string{
		"Sign and attest container images": "sign",
		"Promote signed images":            "promote",
		"Cleanup release candidate tags":   "cleanup",
	}
}

func TestOptionalLedgerStepsLeaveTheLedgerQuestionToTheEngine(t *testing.T) {
	t.Parallel()

	const ledgerInput = "${{ inputs['release-images-path'] }}"

	parsed := loadWorkflow(t, signPublishWorkflow)
	for name, verb := range optionalLedgerSteps() {
		step, found := parsed.findStep(name)
		if !found {
			t.Errorf("step %q is missing", name)

			continue
		}
		// A step-level `if:` or continue-on-error would decide the ledger
		// question outside the engine, where the explicit case cannot fail.
		if condition := scalarString(mappingValue(step, "if")); condition != "" {
			t.Errorf("step %q is gated by %q instead of the engine's ledger admission", name, condition)
		}

		if scalarString(mappingValue(step, "continue-on-error")) != "" {
			t.Errorf("step %q continues on error, so a missing explicit ledger cannot fail it", name)
		}
		// The engine tells a named ledger from the default by this variable
		// being non-empty, so the input reaches it unchanged.
		if got := scalarString(mappingValue(mappingValue(step, "env"), "RELEASE_IMAGES_PATH")); got != ledgerInput {
			t.Errorf("step %q sets RELEASE_IMAGES_PATH to %q, want %s", name, got, ledgerInput)
		}

		run := scalarString(mappingValue(step, "run"))
		assertOrderedFragments(t, name, run, []string{"container release-images " + verb, "--allow-missing-ledger"})
	}

	// Only those three steps may treat their input as optional.
	for jobName, target := range parsed.jobs {
		for _, step := range target.steps() {
			name := scalarString(mappingValue(step, "name"))
			if _, optional := optionalLedgerSteps()[name]; !optional && mentions(step, "--allow-missing-ledger") {
				t.Errorf("job %s step %q passes --allow-missing-ledger", jobName, name)
			}
		}
	}
}

// The image steps pass a token that may be empty, and the engine reads other
// variables when the one it was given is empty: REGISTRY_PASSWORD after
// REGISTRY_TOKEN, and the Forgejo token names (FORGEJO_TOKEN, GITEA_TOKEN) after
// FORGE_CI_PACKAGE_API_TOKEN; it reads no other forge's token. Each
// step clears every such name before it sources the resolver and gives back
// only the secret it captured, so no ambient variable can stand in for the
// named secret. The engine-tagged TestOptionalLedgerStepsThroughTheEngine runs
// the same steps with an ambient fallback set.
func TestImageStepsLeaveTheEngineNoFallbackCredential(t *testing.T) {
	t.Parallel()

	const resolver = `source "${HELPERS_DIR:?}/.forgejo/bin/lib-forge-ci.sh"`

	//nolint:gosec // G101: variable names and shell text, no values.
	registry := map[string]string{"REGISTRY_TOKEN": `"${registry_token}"`, "REGISTRY_PASSWORD": ""}
	//nolint:gosec // G101: variable names and shell text, no values.
	provider := map[string]string{
		"FORGE_CI_PACKAGE_API_TOKEN": `"${provider_token}"`, "FORGEJO_TOKEN": "", "GITEA_TOKEN": "",
	}

	parsed := loadWorkflow(t, signPublishWorkflow)
	for name, verb := range optionalLedgerSteps() {
		step, found := parsed.findStep(name)
		if !found {
			t.Errorf("step %q is missing", name)

			continue
		}

		restored := maps.Clone(registry)
		if verb == "cleanup" {
			maps.Copy(restored, provider)
		}

		// One logical line per command, so a continued unset reads whole.
		lines := strings.Split(strings.ReplaceAll(scalarString(mappingValue(step, "run")), "\\\n", " "), "\n")

		unset := slices.IndexFunc(lines, func(line string) bool {
			return strings.HasPrefix(strings.TrimSpace(line), "unset ")
		})
		if unset < 0 {
			t.Errorf("step %q clears no credential variable", name)

			continue
		}

		before, after := strings.Join(lines[:unset], "\n"), strings.Join(lines[unset+1:], "\n")
		if strings.Contains(before, resolver) || !strings.Contains(after, resolver) {
			t.Errorf("step %q does not clear its credential variables before it sources the resolver", name)
		}

		cleared := strings.Fields(lines[unset])

		for variable, value := range restored {
			if !slices.Contains(cleared, variable) {
				t.Errorf("step %q does not unset %s, which the engine reads as a credential", name, variable)
			}

			// What comes back is the captured secret alone.
			if got := strings.Count(after, variable+"="); got != strings.Count(after, variable+"="+value) || (value == "") != (got == 0) {
				t.Errorf("step %q assigns %s after the unset to something other than its captured secret", name, variable)
			}
		}
	}
}

// Checksums and SBOMs exist for every release, so their signing must not be
// skipped along with an absent image ledger.
func TestBlobSigningDoesNotDependOnTheImageLedger(t *testing.T) {
	t.Parallel()

	const stepName = "Cosign sign-blob checksums and SBOMs"

	step, found := loadWorkflow(t, signPublishWorkflow).findStep(stepName)
	if !found {
		t.Fatalf("step %q is missing", stepName)
	}

	requireWalkReaches(t, step, "release sign", stepName)

	for _, ledger := range []string{"--allow-missing-ledger", "release-images", "RELEASE_IMAGES_PATH"} {
		if mentions(step, ledger) {
			t.Errorf("step %q depends on the image ledger: %q", stepName, ledger)
		}
	}
}

// The engine owns the rules on provenance inputs: `release provenance` takes
// the checksums file from the release manifest and validates it in process,
// and refuses an unsafe --workflow path itself. The step names the release tag
// and commit as flags, and the repository, builder and invocation stay the
// runner's own variables. The base-image signer has its own policy,
// TestBaseImageSigningIsOneEngineCall.
func TestProvenanceStepsLeaveReleaseDataRulesToTheEngine(t *testing.T) {
	t.Parallel()

	const stepName = "Generate and sign SLSA provenance"

	parsed := loadWorkflow(t, signPublishWorkflow)

	step, found := parsed.findStep(stepName)
	if !found {
		t.Fatalf("step %q is missing", stepName)
	}

	run := scalarString(mappingValue(step, "run"))
	assertOrderedFragments(t, stepName, run, []string{
		"run_forge_ci release provenance",
		"--checksums-from-manifest",
		`--manifest "${RELEASE_FILES_MANIFEST:?}"`,
		`--dist-dir "${DIST_DIR:?}"`,
		`--workflow "${CALLER_WORKFLOW:?}"`,
		`--ref-name "${RELEASE_TAG:?}"`,
		`--source-sha "${RELEASE_SHA:?}"`,
	})

	if calls := strings.Count(run, "run_forge_ci"); calls != 1 {
		t.Errorf("step %q makes %d engine calls, want the one provenance call", stepName, calls)
	}

	// A security rule, not a style one: the statement's repository, builder
	// and invocation are what the runner attests. A workflow, job or step that
	// sets one of the runner's source variables would put its own value into
	// signed provenance.
	for _, name := range runnerSourceOverrides(run, parsed.env, mappingValue(parsed.jobs["sign"].node, "env"), mappingValue(step, "env")) {
		t.Errorf("step %q runs with the runner variable %s set by the workflow, which the provenance statement reads", stepName, name)
	}

	// CHECKSUMS_FILE counts as an explicit --checksums-file to the engine.
	for subject, node := range map[string]ast.Node{"workflow env": parsed.env, "sign job": parsed.jobs["sign"].node} {
		if mentions(node, "CHECKSUMS_FILE") {
			t.Errorf("%s names CHECKSUMS_FILE, which conflicts with --checksums-from-manifest", subject)
		}
	}
}

// runnerSourceOverrides returns the runner's own source variables that a run
// script assigns or one of the env mappings sets.
func runnerSourceOverrides(run string, envs ...ast.Node) []string {
	var found []string

	for _, forge := range []string{"FORGEJO_", "GITHUB_"} {
		for _, suffix := range []string{"REF_NAME", "REF_TYPE", "SHA", "SERVER_URL", "REPOSITORY"} {
			name := forge + suffix
			if strings.Contains(run, name+"=") || slices.ContainsFunc(envs, func(env ast.Node) bool { return mappingValue(env, name) != nil }) {
				found = append(found, name)
			}
		}
	}

	return found
}

// assertOrderedFragments reports the first fragment that is missing or out of
// order, so a failure names the broken link rather than the whole step.
func assertOrderedFragments(t *testing.T, subject, text string, fragments []string) {
	t.Helper()

	cursor := 0
	for _, fragment := range fragments {
		index := strings.Index(text[cursor:], fragment)
		if index < 0 {
			if strings.Contains(text, fragment) {
				t.Errorf("%s: %q appears before the step reaches it", subject, fragment)
			} else {
				t.Errorf("%s: missing %q", subject, fragment)
			}

			return
		}

		cursor += index + len(fragment)
	}
}

// jobNeeds normalizes the two shapes needs can take: one scalar, or a sequence.
func jobNeeds(target job) []string {
	node := mappingValue(target.node, "needs")
	if node == nil {
		return nil
	}

	if values := sequenceValues(node); values != nil {
		needs := make([]string, 0, len(values))
		for _, value := range values {
			needs = append(needs, scalarString(value))
		}

		return needs
	}

	return []string{scalarString(node)}
}

// jobRunsCommand reports whether any of the job's steps run the given text.
// Only run: scripts are searched, so a mention in a step name or comment does
// not satisfy the check.
func jobRunsCommand(target job, command string) bool {
	for _, step := range target.steps() {
		if strings.Contains(scalarString(mappingValue(step, "run")), command) {
			return true
		}
	}

	return false
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}

	return true
}
