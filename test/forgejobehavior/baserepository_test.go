// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Run the real signer/promoter shell against the fake engine: the override must
// reach repository confinement as one argument, and the empty input must retain
// each engine call's existing default. The base signer and promoter pass the
// input as it is, empty included, since the engine derives the default; what
// it derives from the signer's flags is shown against the real engine by
// TestBaseImageSigningThroughTheEngine. Nothing signs or contacts a registry.
func TestBaseRepositoryWorkflowOverrides(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"sign-container-images", "promote-base-container-images", "sign-and-publish-release"} {
		for _, repository := range []string{"", "forgejo.invalid/owner/persistent-bases"} {
			job, stepName := name, "Verify signatures and promote base tags"
			flag, expected := "--expected-repository", repository

			switch name {
			case "sign-container-images":
				stepName = "Sign and attest images"
			case "sign-and-publish-release":
				job, stepName, flag = "sign", "Sign and attest container images", "--expected-base-repository"
			}

			t.Run(name+"/"+repository, func(t *testing.T) {
				t.Parallel()

				args := baseRepositoryEngineArgs(t, name, job, stepName, repository)
				requireFlagValue(t, args, flag, expected)

				if name != "sign-container-images" {
					return
				}

				// The inputs the engine derives the default repository from.
				for derivation, value := range map[string]string{
					"--server-url": "https://forgejo.invalid", "--repository": "Owner/Project", "--repository-suffix": "-base",
				} {
					requireFlagValue(t, args, derivation, value)
				}
			})
		}
	}
}

// baseRepositoryEngineArgs checks that the step's base repository is the
// workflow's optional input, runs the step with repository as that input, and
// returns what the fake engine was called with.
func baseRepositoryEngineArgs(t *testing.T, name, job, stepName, repository string) []string {
	t.Helper()

	selected := workflowStep(t, name+".yml", job, stepName)
	if selected.Env["BASE_IMAGE_REPOSITORY"] != "${{ inputs.base-repository }}" {
		t.Fatal("base repository does not come from the declared input")
	}

	var document struct {
		On struct {
			Call struct {
				Inputs map[string]actionInput `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
	}
	loadYAML(t, ".forgejo/workflows/"+name+".yml", &document)

	input, exists := document.On.Call.Inputs["base-repository"]
	if !exists || input.Required || input.Default != "" {
		t.Fatal("base repository must be optional and default to empty")
	}

	box := newSandbox(t, name)

	private := filepath.Join(box.root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}

	got := box.run(selected.Run,
		"PATH="+filepath.Join(box.root, ".forgejo", "bin")+":/usr/bin:/bin",
		"HELPERS_DIR="+box.root, "SIGNER_WORK_DIR="+box.root, "SIGNER_WORKSPACE="+private,
		"CONSUMER_WORKSPACE="+box.root, "FORGEJO_SERVER=https://forgejo.invalid",
		"FORGEJO_REPO=Owner/Project", "REPOSITORY_SUFFIX=-base", "BASE_IMAGE_REPOSITORY="+repository,
		"COSIGN_KEY=fixture-key", "COSIGN_PASSWORD=fixture-password", "REGISTRY_TOKEN=fixture-token",
		"REGISTRY_USER=owner", "LEDGER_JSON=[]", "SOURCE_SHA=fixture-sha",
		"CALLER_WORKFLOW=container-bases.yml", "SBOM_ARTIFACT=base-sboms",
		"TRUSTED_KEY_INPUT=public fixture", "TRUSTED_KEY_SHA256_INPUT=fixture-digest")
	if got.status != 0 {
		t.Fatalf("workflow shell failed: %s", got.stderr)
	}

	return box.argv()
}

// requireFlagValue fails unless args passes want as the value of flag.
func requireFlagValue(t *testing.T, args []string, flag, want string) {
	t.Helper()

	position := slices.Index(args, flag)
	if position < 0 || position+1 >= len(args) || args[position+1] != want {
		t.Fatalf("%s is not %q; engine arguments: %q", flag, want, args)
	}
}
