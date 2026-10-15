// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package forgejoguard

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/ast"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// errRule is the sentinel every guard rule in this package wraps, so each
// finding is a static error carrying the rule's own detail.
var errRule = errors.New("workflow rule")

const signPublishWorkflow = ".forgejo/workflows/sign-and-publish-release.yml"

// secretChannels is the privilege separation inside the signer. Each step
// receives exactly the credentials its own job needs. Signing keys must not
// reach promotion or publication, and the release token must not reach
// signing.
func secretChannels() []struct {
	step  string
	allow []string
	deny  []string
} {
	return []struct {
		step  string
		allow []string
		deny  []string
	}{
		{
			step:  "Sign and attest container images",
			allow: []string{"COSIGN_KEY", "REGISTRY_TOKEN"},
			deny:  []string{"FORGEJO_TOKEN", "GPG_SIGNING_KEY", "MISE_GITHUB_TOKEN"},
		},
		{
			step:  "Promote signed images",
			allow: []string{"REGISTRY_TOKEN"},
			deny:  []string{"COSIGN_KEY", "FORGEJO_TOKEN", "GPG_SIGNING_KEY"},
		},
		{
			step:  "Publish release assets and signatures",
			allow: []string{"PUBLISH_RELEASE_TOKEN_INPUT"},
			deny:  []string{"COSIGN_KEY", "GPG_SIGNING_KEY", "REGISTRY_TOKEN"},
		},
	}
}

// stepChannelKeys follows findStep's first-match convention. It includes
// workflow/job/step env names and, for actions, explicit input channel names.
// Input names are checked as declared, without implicit environment aliases.
// A future run-to-action change must review its channel contract explicitly.
func (w workflow) stepChannelKeys(stepName string) (map[string]bool, bool) {
	for _, job := range w.jobs {
		for _, step := range job.steps() {
			if scalarString(mappingValue(step, "name")) != stepName {
				continue
			}

			keys := make(map[string]bool)

			for _, env := range []ast.Node{w.env, mappingValue(job.node, "env"), mappingValue(step, "env")} {
				for _, entry := range mappingEntries(env) {
					keys[scalarString(entry.Key)] = true
				}
			}

			if scalarString(mappingValue(step, "uses")) != "" {
				for _, entry := range mappingEntries(mappingValue(step, "with")) {
					keys[scalarString(entry.Key)] = true
				}
			}

			return keys, true
		}
	}

	return nil, false
}

func checkSecretChannels(data []byte) error {
	parsed, err := parseWorkflow(data)
	if err != nil {
		return err
	}

	for _, channel := range secretChannels() {
		keys, found := parsed.stepChannelKeys(channel.step)
		if !found {
			return fmt.Errorf("%w: step %q is missing", errRule, channel.step)
		}

		for _, name := range channel.allow {
			if !keys[name] {
				return fmt.Errorf("%w: step %q must receive %s", errRule, channel.step, name)
			}
		}

		for _, name := range channel.deny {
			if keys[name] {
				return fmt.Errorf("%w: step %q must not receive %s", errRule, channel.step, name)
			}
		}
	}

	return nil
}

// Each signer step receives only its own credentials.
func TestSignerStepsReceiveOnlyTheirOwnSecrets(t *testing.T) {
	t.Parallel()

	if err := checkSecretChannels(reporoot.ReadFile(t, signPublishWorkflow)); err != nil {
		t.Errorf("%s: %v", signPublishWorkflow, err)
	}
}

// The publish step's token input carries the release token.
func TestPublishStepUsesTheReleaseToken(t *testing.T) {
	t.Parallel()

	parsed := loadWorkflow(t, signPublishWorkflow)
	for _, job := range parsed.jobs {
		for _, step := range job.steps() {
			if scalarString(mappingValue(step, "name")) != "Publish release assets and signatures" {
				continue
			}

			value := scalarString(mappingValue(mappingValue(step, "env"), "PUBLISH_RELEASE_TOKEN_INPUT"))
			if !strings.Contains(value, "RELEASE_TOKEN") {
				t.Errorf("PUBLISH_RELEASE_TOKEN_INPUT is %q, want it to carry RELEASE_TOKEN", value)
			}

			return
		}
	}

	t.Error("publish step is missing")
}

// A leaked credential in the wrong step is rejected.
func TestSecretChannelRejectsALeakedCredential(t *testing.T) {
	t.Parallel()

	data := string(reporoot.ReadFile(t, signPublishWorkflow))

	leaked := strings.Replace(data,
		"        - name: Promote signed images",
		"        - name: Promote signed images\n          env:\n            COSIGN_KEY: leaked",
		1)
	if leaked == data {
		leaked = strings.Replace(data,
			"      - name: Promote signed images",
			"      - name: Promote signed images\n        env:\n          COSIGN_KEY: leaked",
			1)
	}

	if leaked == data {
		t.Fatal("could not build the leak fixture")
	}

	if err := checkSecretChannels([]byte(leaked)); err == nil {
		t.Error("accepted a signing key in the promotion step")
	}
}

func TestSecretChannelsIncludeInheritedEnvironment(t *testing.T) {
	t.Parallel()

	const baseline = `
env:
  PUBLIC_WORKFLOW_VALUE: fixture
jobs:
  sign:
    env:
      PUBLIC_JOB_VALUE: fixture
    steps:
      - name: Sign and attest container images
        env:
          COSIGN_KEY: fixture
          REGISTRY_TOKEN: fixture
        run: 'true'
  promote:
    env:
      PUBLIC_JOB_VALUE: fixture
    steps:
      - name: Promote signed images
        env:
          REGISTRY_TOKEN: fixture
        run: 'true'
  publish:
    steps:
      - name: Publish release assets and signatures
        env:
          PUBLISH_RELEASE_TOKEN_INPUT: fixture
        run: 'true'
`
	if err := checkSecretChannels([]byte(baseline)); err != nil {
		t.Fatalf("valid baseline: %v", err)
	}

	for _, test := range []struct {
		name, from, to, wantErr string
	}{
		{
			"denied job key without secret expression",
			"  sign:\n    env:\n",
			"  sign:\n    env:\n      FORGEJO_TOKEN: ${{ needs.coordinates.outputs.helpers_repository }}\n",
			`step "Sign and attest container images" must not receive FORGEJO_TOKEN`,
		},
		{
			"denied workflow key",
			"env:\n  PUBLIC_WORKFLOW_VALUE: fixture",
			"env:\n  COSIGN_KEY: fixture",
			`step "Promote signed images" must not receive COSIGN_KEY`,
		},
		{
			"allowed job key",
			"      PUBLIC_JOB_VALUE: fixture\n    steps:\n      - name: Promote signed images\n        env:\n          REGISTRY_TOKEN: fixture",
			"      REGISTRY_TOKEN: fixture\n    steps:\n      - name: Promote signed images",
			"",
		},
		{
			"allowed workflow key",
			"          PUBLISH_RELEASE_TOKEN_INPUT: fixture",
			"          PUBLIC_STEP_VALUE: fixture",
			`step "Publish release assets and signatures" must receive PUBLISH_RELEASE_TOKEN_INPUT`,
		},
		{
			"unrelated job does not supply required key",
			"          COSIGN_KEY: fixture\n          REGISTRY_TOKEN: fixture",
			"          COSIGN_KEY: fixture",
			`step "Sign and attest container images" must receive REGISTRY_TOKEN`,
		},
		{
			"missing step",
			"      - name: Promote signed images",
			"      - name: Other promotion step",
			`step "Promote signed images" is missing`,
		},
		{
			"allowed explicit action input channel",
			"        env:\n          REGISTRY_TOKEN: fixture\n        run: 'true'",
			"        uses: ./.forgejo/actions/fixture\n        with:\n          REGISTRY_TOKEN: fixture",
			"",
		},
		{
			"denied explicit action input channel",
			"        env:\n          REGISTRY_TOKEN: fixture\n        run: 'true'",
			"        uses: ./.forgejo/actions/fixture\n        env:\n          REGISTRY_TOKEN: fixture\n        with:\n          COSIGN_KEY: fixture",
			`step "Promote signed images" must not receive COSIGN_KEY`,
		},
		{
			"action input is not an implicit env alias",
			"        env:\n          PUBLISH_RELEASE_TOKEN_INPUT: fixture\n        run: 'true'",
			"        uses: ./.forgejo/actions/fixture\n        with:\n          token: fixture",
			`step "Publish release assets and signatures" must receive PUBLISH_RELEASE_TOKEN_INPUT`,
		},
		{
			"first same-name step still wins",
			"        run: 'true'\n  publish:",
			"        run: 'true'\n      - name: Promote signed images\n        env:\n          COSIGN_KEY: fixture\n        run: 'true'\n  publish:",
			"",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if strings.Count(baseline, test.from) != 1 {
				t.Fatal("fixture replacement must match exactly once")
			}

			data := strings.Replace(baseline, test.from, test.to, 1)
			if test.name == "allowed workflow key" {
				// First prove that no other scope supplies the required key.
				if err := checkSecretChannels([]byte(data)); err == nil || err.Error() != "workflow rule: "+test.wantErr {
					t.Fatalf("missing key: %v, want %s", err, test.wantErr)
				}

				data = strings.Replace(data, "  PUBLIC_WORKFLOW_VALUE: fixture", "  PUBLISH_RELEASE_TOKEN_INPUT: fixture", 1)
				test.wantErr = ""
			}

			err := checkSecretChannels([]byte(data))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("valid channel: %v", err)
				}
			} else if err == nil || err.Error() != "workflow rule: "+test.wantErr {
				t.Fatalf("channel error = %v, want %s", err, test.wantErr)
			}
		})
	}
}

// Distro-package signing is an optional release feature, so its step must stay
// explicitly conditional. Without the guard the signer would try to sign
// packages for a release that has none.
func TestDistroPackageSigningIsConditional(t *testing.T) {
	t.Parallel()

	const stepName = "GPG-sign distro packages"

	step, found := loadWorkflow(t, signPublishWorkflow).findStep(stepName)
	if !found {
		t.Fatalf("%s is missing the %q step", signPublishWorkflow, stepName)
	}

	condition := scalarString(mappingValue(step, "if"))
	if !strings.Contains(condition, "sign-distro-packages") {
		t.Errorf("step %q has condition %q, want it gated on sign-distro-packages", stepName, condition)
	}
}
