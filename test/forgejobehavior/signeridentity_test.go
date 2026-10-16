// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Execute both image steps with distinct engine and recipe identities. The
// recipe must influence tags without replacing the revision of the source
// checkout; both SHA-1 and SHA-256 repositories must fit OCI's tag limit.
func TestSignerExampleSeparatesEngineAndRecipeCommits(t *testing.T) {
	t.Parallel()

	var document struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	loadYAML(t, "examples/forgejo/signer-image.yml", &document)

	if document.Env["SIGNER_RECIPE_COMMIT_SHA"] != "${{ forgejo.sha }}" || document.Env["FORGE_CI_COMMIT_SHA"] != "PIN_FORGE_CI_SHA" {
		t.Fatal("recipe must come from the running workflow and engine from the checkout pin")
	}

	for job, command := range map[string]string{"build-signer-arch": "build-arch", "assemble-signer-image": "assemble"} {
		var script string

		for _, candidate := range document.Jobs[job].Steps {
			if strings.Contains(candidate.Run, "container multiarch-image "+command) {
				script = candidate.Run
			}
		}

		if script == "" {
			t.Fatalf("job %s has no image command", job)
		}

		for _, engineSize := range []int{40, 64} {
			for _, recipeSize := range []int{40, 64} {
				t.Run(command+"/"+strconv.Itoa(engineSize)+"/"+strconv.Itoa(recipeSize), func(t *testing.T) {
					t.Parallel()

					engine := strings.Repeat("a", engineSize)
					recipe := strings.Repeat("b", recipeSize)
					box := newSandbox(t, "signer-image")

					got := box.run(script, "FORGE_CI_COMMIT_SHA="+engine, "SIGNER_RECIPE_COMMIT_SHA="+recipe,
						"REGISTRY_AUTH_FILE=fixture-auth", "SIGNER_ARCH=amd64", "FORGEJO_SERVER=https://forgejo.invalid",
						"FORGEJO_REPO=policy-owner/ci-policy", "SOURCE_SHA="+recipe,
						"SIGNER_IMAGE_REPOSITORY=registry.example/policy/signer")
					if got.status != 0 {
						t.Fatalf("image step failed: %s", got.stderr)
					}

					args := box.argv()
					requireFlagValue(t, args, "--source-sha", engine)
					requireFlagValue(t, args, "--repository", "OWNER/forge-ci")
					requireFlagValue(t, args, "--server-url", "https://forgejo.invalid")
					requireFlagValue(t, args, "--image-repository", "registry.example/policy/signer")
					requireFlagValue(t, args, "--tag-prefix", "signer-"+strings.Repeat("b", 40)+"-")

					prefix := args[slices.Index(args, "--tag-prefix")+1]

					source := args[slices.Index(args, "--source-sha")+1]
					if tag := prefix + source + "-amd64"; len(tag) > 128 {
						t.Fatalf("architecture tag exceeds OCI's 128-byte limit: %s", tag)
					}
				})
			}
		}
	}
}
