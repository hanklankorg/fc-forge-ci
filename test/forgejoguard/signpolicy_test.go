// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The signing policies: the per-job secret sets, serial retry-forward
// publication, the trust-anchor binding, engine delegation inside the signer,
// and tokens off argv. Flag spellings are not pinned here; the behaviour tests
// in test/forgejobehavior and the command tests observe those directly.

const (
	signPublishWorkflowPath   = ".forgejo/workflows/sign-and-publish-release.yml"
	signContainerWorkflowPath = ".forgejo/workflows/sign-container-images.yml"
)

// allStrings returns every mapping key and scalar value beneath the node.
func allStrings(node ast.Node) []string {
	var values []string

	anyString(node, func(value string) bool {
		values = append(values, value)

		return false
	})

	return values
}

// secretName matches both the dot and the quoted-bracket spelling.
var secretName = regexp.MustCompile(`secrets(?:\.([A-Za-z0-9_]+)|\[\s*['"]([A-Za-z0-9_]+)['"]\s*\])`)

// jobSecrets returns the sorted, de-duplicated secret names a job references.
func jobSecrets(node ast.Node) []string {
	var names []string

	for _, value := range allStrings(node) {
		for _, match := range secretName.FindAllStringSubmatch(value, -1) {
			name := match[1] + match[2]
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}

	slices.Sort(names)

	return names
}

// Each sign-and-publish job references exactly the secrets its role needs.
// Step-level channels are pinned by secretchannel_test.go; this pins the job
// boundary, so a new secret reference anywhere in a job needs review.
func TestSignPublishJobSecretSets(t *testing.T) {
	t.Parallel()

	want := map[string][]string{
		"coordinates": nil,
		"sign": {
			"RELEASE_COSIGN_PASSPHRASE", "RELEASE_COSIGN_PRIVATE_KEY", "RELEASE_GPG_FINGERPRINT",
			"RELEASE_GPG_PASSPHRASE", "RELEASE_GPG_PRIVATE_KEY", "RELEASE_REGISTRY_TOKEN",
		},
		"promote": {"RELEASE_REGISTRY_TOKEN"},
		"publish": {"RELEASE_TOKEN"},
		"cleanup": {"RELEASE_REGISTRY_TOKEN"},
	}

	parsed := loadWorkflow(t, signPublishWorkflowPath)
	if len(parsed.jobs) != len(want) {
		t.Errorf("sign-and-publish declares %d jobs, want %d", len(parsed.jobs), len(want))
	}

	for name, secrets := range want {
		target, ok := parsed.jobs[name]
		if !ok {
			t.Errorf("sign-and-publish has no %s job", name)

			continue
		}

		if got := jobSecrets(target.node); !slices.Equal(got, secrets) {
			t.Errorf("job %s references secrets %v, want %v", name, got, secrets)
		}
	}
}

// Publication runs one release at a time per repository and never cancels a
// running attempt, and a failure retries forward instead of rolling back.
func TestSignPublishIsSerialAndRetriesForward(t *testing.T) {
	t.Parallel()

	root, err := parseDocument(reporoot.ReadFile(t, signPublishWorkflowPath))
	if err != nil {
		t.Fatalf("%s: %v", signPublishWorkflowPath, err)
	}

	concurrency := mappingValue(root, "concurrency")
	if got := scalarString(mappingValue(concurrency, "group")); got != "sign-and-publish-${{ forgejo.repository }}" {
		t.Errorf("concurrency group is %q", got)
	}

	if got := scalarString(mappingValue(concurrency, "cancel-in-progress")); got != "false" {
		t.Errorf("cancel-in-progress is %q, want false", got)
	}

	parsed := loadWorkflow(t, signPublishWorkflowPath)
	for name, job := range parsed.jobs {
		steps := job.steps()
		conditions := make([]string, 0, 1+len(steps))
		conditions = append(conditions, scalarString(mappingValue(job.node, "if")))

		for _, step := range steps {
			conditions = append(conditions, scalarString(mappingValue(step, "if")))
		}

		for _, condition := range conditions {
			if strings.Contains(condition, "failure()") {
				t.Errorf("job %s has a failure() branch; release recovery retries forward", name)
			}
		}
	}

	requireWalkReaches(t, root, "container release-images promote", signPublishWorkflowPath)

	if mentions(root, "release-images rollback") {
		t.Error("sign-and-publish rolls back image promotions; release recovery retries forward")
	}
}

// Both Cosign signing workflows bind the signing key to the caller's trust
// anchor and its digest, so a swapped anchor cannot become the comparison
// target. The anchor is always the caller's two inputs: the layer ships no key
// and the step names no file of the helper checkout. What the step writes is
// tested in test/forgejobehavior; what the binding does, in the command tests.
func TestCosignSigningBindsKeyToTrustAnchor(t *testing.T) {
	t.Parallel()

	const stepName = "Bind Cosign signing key to trusted public key"

	for _, path := range []string{signPublishWorkflowPath, signContainerWorkflowPath} {
		parsed := loadWorkflow(t, path)

		var runs []string

		for _, job := range parsed.jobs {
			for _, step := range job.steps() {
				if scalarString(mappingValue(step, "name")) == stepName {
					runs = append(runs, scalarString(mappingValue(step, "run")))
				}
			}
		}

		if len(runs) != 1 {
			t.Errorf("%s has %d %q steps, want 1", path, len(runs), stepName)

			continue
		}

		for _, fragment := range []string{
			"validate signing-key",
			"--key env://COSIGN_KEY",
			`trusted_key="${SIGNER_WORKSPACE:?}/cosign.pub"`,
			`trusted_public_key=$(printf '%s' "${TRUSTED_KEY_INPUT}")`,
			`printf '%s\n' "${trusted_public_key}" >"${trusted_key}"`,
			`printf '%s\n' "${TRUSTED_KEY_SHA256_INPUT}" >"${trusted_key}.sha256"`,
			`--trusted-public-key "${trusted_key}"`,
			`--trusted-public-key-digest "${trusted_key}.sha256"`,
		} {
			if !strings.Contains(runs[0], fragment) {
				t.Errorf("%s: %q no longer passes %s", path, stepName, fragment)
			}
		}

		if strings.Contains(runs[0], "HELPERS_DIR:?}/keys") || strings.Contains(runs[0], "if [") {
			t.Errorf("%s: %q selects a trust anchor; the caller's inputs are the only one", path, stepName)
		}
	}
}

// sign-container-images.yml hands the whole base-image signing rule set to one
// engine process: ledger admission, the provenance statement, the registry
// login and the signatures. The step keeps the orchestration: it fetches the
// SBOM artifact, writes the ledger input to a file, supplies the timestamp and
// pipes the registry token on stdin. The token is cleared from the environment
// before the child-capable resolver is sourced, and the step leaves the
// runner's own source variables, which the signed statement reads, untouched.
func TestBaseImageSigningIsOneEngineCall(t *testing.T) {
	t.Parallel()

	const stepName = "Sign and attest images"

	parsed := loadWorkflow(t, signContainerWorkflowPath)

	step, found := parsed.findStep(stepName)
	if !found {
		t.Fatalf("step %q is missing", stepName)
	}

	run := scalarString(mappingValue(step, "run"))
	assertOrderedFragments(t, stepName, run, []string{
		`registry_token="${REGISTRY_TOKEN:?}"`,
		"unset COSIGN_KEY COSIGN_PASSWORD REGISTRY_TOKEN REGISTRY_USER",
		`source "${HELPERS_DIR:?}/.forgejo/bin/lib-forge-ci.sh"`,
		"run_forge_ci artifact download",
		"--dir premade-sboms",
		`printf '%s' "${LEDGER_JSON:?}" > base-images.ledger.json`,
		`printf '%s' "${registry_token}" | COSIGN_KEY="${cosign_key}" COSIGN_PASSWORD="${cosign_password}"`,
		"run_forge_ci container base-images sign",
		"--ledger base-images.ledger.json",
		`--server-url "${FORGEJO_SERVER:?}"`,
		`--repository "${FORGEJO_REPO:?}"`,
		`--repository-suffix "${REPOSITORY_SUFFIX}"`,
		`--expected-repository "${BASE_IMAGE_REPOSITORY}"`,
		`--workflow "${CALLER_WORKFLOW:?}"`,
		`--source-sha "${SOURCE_SHA}"`,
		`--started-on "$(date -u +%Y-%m-%dT%H:%M:%SZ)"`,
		`--registry-username "${registry_user}"`,
		"--registry-password-file -",
		"--sbom-path-pattern '^premade-sboms/",
		"--method kms",
		"--key env://COSIGN_KEY",
	})

	if calls := strings.Count(run, "run_forge_ci "); calls != 2 {
		t.Errorf("step %q makes %d engine calls, want the artifact download and the one signing call", stepName, calls)
	}

	job := mappingValue(parsed.jobs["sign-container-images"].node, "env")
	for _, name := range runnerSourceOverrides(run, parsed.env, job, mappingValue(step, "env")) {
		t.Errorf("step %q runs with the runner variable %s set by the workflow, which the provenance statement reads", stepName, name)
	}
}

// The engine signs and promotes base images only on the registry it logs in
// to. On the forge's own registry, the host for as long as the step names no
// other, a base-repository input chooses a package name under the calling
// repository's owner. A repository on another registry is the caller's
// declared external one, which the layer's helper marks from its host, so
// neither workflow passes --registry or sets CONTAINER_REGISTRY, and each
// input description states the rules its caller meets.
func TestBaseImageSigningAndPromotionNameTheirRegistry(t *testing.T) {
	t.Parallel()

	namesRegistry := regexp.MustCompile(`--registry([ =]|$)|CONTAINER_REGISTRY`)

	for path, stepName := range map[string]string{
		signContainerWorkflowPath:                              "Sign and attest images",
		".forgejo/workflows/promote-base-container-images.yml": "Verify signatures and promote base tags",
	} {
		root, err := parseDocument(reporoot.ReadFile(t, path))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		requireWalkReaches(t, root, `--expected-repository "${BASE_IMAGE_REPOSITORY}"`, path)
		requireWalkReaches(t, root, `forge_ci_mark_external_registry "${BASE_IMAGE_REPOSITORY:-}" "${FORGEJO_SERVER:-}"`, path)

		if anyString(root, namesRegistry.MatchString) {
			t.Errorf("%s names a registry host; the base-image registry must stay the forge's own", path)
		}

		parsed := loadWorkflow(t, path)
		if _, found := parsed.findStep(stepName); !found {
			t.Errorf("%s: step %q is missing", path, stepName)
		}

		inputs := mappingValue(mappingValue(mappingValue(root, "on"), "workflow_call"), "inputs")

		description := strings.Join(strings.Fields(scalarString(mappingValue(mappingValue(inputs, "base-repository"), "description"))), " ")
		for _, rule := range []string{"On this forge's registry, with its host and port", "under the calling repository's owner", "On another registry", "taken as declared"} {
			if !strings.Contains(description, rule) {
				t.Errorf("%s: the base-repository description does not state %q", path, rule)
			}
		}
	}
}

var miseTokenKey = regexp.MustCompile(`^MISE_(GITHUB|FORGEJO)_TOKEN$`)

// signerViolations reports what the locked signer must never do: run a Node
// action, install or fetch tools, carry a rate-limit token, run as root,
// derive trust from the caller's workflow_ref, or shell out to the signing and
// registry tools the engine owns.
func signerViolations(root ast.Node) []string {
	var found []string

	forbiddenUses := []string{"actions/checkout", "upload-artifact", "download-artifact", "actions/sign-and-publish@"}
	forbiddenText := regexp.MustCompile(
		`_RATE_LIMIT|apt-get|mise --no-config install|install-tool\.sh mise|` +
			`(github|forgejo)\.workflow_ref|FORGEJO_SHA=%s|` +
			`\bcosign (login|sign|sign-blob|attest)\b|\bskopeo (login|inspect|copy|delete)\b|\bsyft |\bcurl |Authorization: token`)

	var walk func(node ast.Node)

	walk = func(node ast.Node) {
		switch typed := node.(type) {
		case *ast.MappingNode, *ast.MappingValueNode:
			for _, entry := range mappingEntries(typed) {
				key := scalarString(entry.Key)

				value := scalarString(entry.Value)
				switch {
				case key == "uses" && slices.ContainsFunc(forbiddenUses, func(u string) bool { return strings.Contains(value, u) }):
					found = append(found, "uses "+value)
				case key == "options" && strings.Contains(value, "--user 0"):
					found = append(found, "container runs as root")
				case miseTokenKey.MatchString(key):
					found = append(found, "assigns "+key)
				}

				walk(entry.Value)
			}
		case *ast.SequenceNode:
			for _, item := range typed.Values {
				walk(item)
			}
		case nil:
		default:
			if match := forbiddenText.FindString(scalarString(node)); match != "" {
				found = append(found, match)
			}
		}
	}
	walk(root)

	return found
}

func TestSignerStaysLockedAndDelegatesToTheEngine(t *testing.T) {
	t.Parallel()

	root, err := parseDocument(reporoot.ReadFile(t, signPublishWorkflowPath))
	if err != nil {
		t.Fatal(err)
	}

	requireWalkReaches(t, root, "run_forge_ci", signPublishWorkflowPath)

	for _, violation := range signerViolations(root) {
		t.Error(violation)
	}
}

func TestSignerViolationsAreDetected(t *testing.T) {
	t.Parallel()

	for _, document := range []string{
		"jobs: {sign: {steps: [{uses: 'https://code.forgejo.org/actions/checkout@abc'}]}}",
		"jobs: {sign: {container: {options: '--user 0'}}}",
		"jobs: {sign: {env: {MISE_GITHUB_TOKEN: x}}}",
		"jobs: {sign: {steps: [{run: 'apt-get install -y jq'}]}}",
		"jobs: {sign: {steps: [{run: 'cosign sign-blob --key k f'}]}}",
		"jobs: {sign: {steps: [{run: 'curl -H \"Authorization: token $T\" u'}]}}",
		"jobs: {sign: {steps: [{env: {REF: '${{ forgejo.workflow_ref }}'}}]}}",
	} {
		root, err := parseDocument([]byte(document))
		if err != nil {
			t.Fatalf("%s: %v", document, err)
		}

		if len(signerViolations(root)) == 0 {
			t.Errorf("signer policy accepted %s", document)
		}
	}
}

// Release and registry tokens never reach argv or a token-bearing curl
// config. The container signing workflow and the token verifier are held to
// the same rule; the verifier must also never enable shell tracing.
func TestTokensStayOffArgv(t *testing.T) {
	t.Parallel()

	tokenInArgv := regexp.MustCompile(`\bcurl |Authorization: token|\s-K\s|write_token_curl_config`)

	for _, path := range []string{
		signPublishWorkflowPath,
		signContainerWorkflowPath,
		".forgejo/actions/verify-forgejo-token/action.yml",
		".forgejo/actions/verify-forgejo-token/verify-forgejo-token.sh",
	} {
		text := string(reporoot.ReadFile(t, path))
		if match := tokenInArgv.FindString(text); match != "" {
			t.Errorf("%s contains a token-exposing shell path: %q", path, match)
		}

		if strings.HasPrefix(path, ".forgejo/actions/verify-forgejo-token/") && strings.Contains(text, "set -x") {
			t.Errorf("%s enables shell tracing", path)
		}
	}
}

// registryLoginOrderViolation checks that the token is captured, unexported
// and its input variable cleared on consecutive lines, all before the
// child-capable resolver is sourced, and that it reaches the engine on stdin.
func registryLoginOrderViolation(run string) string {
	lines := strings.Split(run, "\n")
	index := func(prefix string) int {
		return slices.IndexFunc(lines, func(line string) bool {
			return strings.HasPrefix(strings.TrimSpace(line), prefix)
		})
	}
	capture := index("registry_login_token=${REGISTRY_LOGIN_TOKEN}")
	unexport := index("export -n registry_login_token")
	unsetToken := index("unset REGISTRY_LOGIN_TOKEN")

	source := index(`source "${GITHUB_ACTION_PATH}/../../bin/lib-forge-ci.sh"`)
	switch {
	case capture < 0 || unexport != capture+1 || unsetToken != unexport+1:
		return "the token is not captured, unexported and cleared on consecutive lines"
	case source <= unsetToken:
		return "the resolver is sourced before the exported token is cleared"
	case index(`printf '%s' "${registry_login_token}" |`) < 0:
		return "the token does not reach the engine on stdin"
	}

	return ""
}

func TestRegistryLoginClearsTheTokenBeforeTheResolver(t *testing.T) {
	t.Parallel()

	root, err := parseDocument(reporoot.ReadFile(t, ".forgejo/actions/registry-login/action.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var runs []string

	for _, step := range sequenceValues(mappingValue(mappingValue(root, "runs"), "steps")) {
		if run := scalarString(mappingValue(step, "run")); strings.Contains(run, "REGISTRY_LOGIN_TOKEN") {
			runs = append(runs, run)
		}
	}

	if len(runs) != 1 {
		t.Fatalf("registry-login has %d token-handling run steps, want 1", len(runs))
	}

	if violation := registryLoginOrderViolation(runs[0]); violation != "" {
		t.Errorf("registry-login: %s", violation)
	}

	reordered := strings.Replace(runs[0], "unset REGISTRY_LOGIN_TOKEN\n", "", 1)

	reordered = strings.Replace(reordered, "printf '%s'", "unset REGISTRY_LOGIN_TOKEN\nprintf '%s'", 1)
	if registryLoginOrderViolation(reordered) == "" {
		t.Error("the order check accepted a token cleared only after the resolver was sourced")
	}
}
