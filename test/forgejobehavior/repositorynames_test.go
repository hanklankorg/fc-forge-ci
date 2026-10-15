// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"strings"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/repositorycases"
)

func TestBootstrapRepositoryNamesMatchAdapterAddressing(t *testing.T) {
	t.Parallel()

	for _, workflow := range []string{"prepare-release.yml", "sign-and-publish-release.yml", "sign-container-images.yml", "verify-base-container-images.yml", "promote-base-container-images.yml"} {
		stepName := "Validate trust coordinates"
		if workflow == "prepare-release.yml" {
			stepName = "Validate trust coordinate"
		}

		selected := workflowStep(t, workflow, "coordinates", stepName)

		for _, tc := range repositorycases.OwnerName() {
			for _, fullURL := range []bool{false, true} {
				raw := tc.Path
				if fullURL {
					raw = "https://forge.example/" + raw + ".git"
				}

				t.Run(workflow+"/"+raw, func(t *testing.T) {
					t.Parallel()
					box := newSandbox(t, "coordinates")

					got := box.run(selected.Run, "HELPERS_REPOSITORY_INPUT="+raw,
						"SERVER_URL=https://forge.example", "TRANSPARENCY_LOG_INPUT=none",
						"SIGNER_IMAGE_INPUT=forge.example/org/app__tools--signer@sha256:"+strings.Repeat("a", 64))
					if (got.status == 0) != tc.Valid {
						t.Fatalf("status=%d, want valid=%v: %s", got.status, tc.Valid, got.stderr)
					}

					if tc.Valid && !strings.Contains(box.output(), "helpers_repository=https://forge.example/"+tc.Path+".git\n") {
						t.Fatalf("source spelling changed: %q", box.output())
					}
				})
			}
		}
	}
}

func TestPublishContainerNamingUsesWorkflowEnvironment(t *testing.T) {
	t.Parallel()
	engine := forgeCIEngine(t)
	resolve := jobStep(t, ".github/workflows/publish-container.yml", "prep", "Determine Image Name")

	validate := jobStep(t, ".github/workflows/publish-container.yml", "prep", "Validate Image Namespace")
	for _, tc := range []struct{ repository, name, image, expected, want string }{
		{"MixedOrg/My.Repo", "Worker", "", "", "ghcr.io/mixedorg/my.repo/worker"},
		{"MixedOrg/My.Repo", "my.repo", "", "", "ghcr.io/mixedorg/my.repo"},
		{"MixedOrg/.github", "", "ghcr.io/mixedorg/github-config", "ghcr.io/mixedorg/github-config", "ghcr.io/mixedorg/github-config"},
	} {
		t.Run(tc.repository+"/"+tc.name, func(t *testing.T) {
			t.Parallel()
			box := newSandbox(t, "publish-container")
			values := map[string]string{
				"${{ inputs.registry }}": "ghcr.io", "${{ inputs['image-name'] }}": tc.image,
				"${{ github.repository }}": tc.repository, "${{ github.repository_owner }}": "MixedOrg",
				"${{ inputs.name }}": tc.name, "${{ inputs['enforce-namespace'] || github.repository_owner }}": "MixedOrg",
				"${{ steps.image-name.outputs.name }}": tc.want, "${{ inputs['expected-image-repository'] }}": tc.expected,
			}

			for _, selected := range []step{resolve, validate} {
				env := make([]string, 0, 4+len(selected.Env))
				env = append(env, "FORGE_CI_BIN="+engine, "FORGE_CI_PROVIDER=github", "FORGE_CI_RUNNER=github", "GITHUB_OUTPUT="+box.outputs)

				for key, expression := range selected.Env {
					value, ok := values[expression]
					if !ok {
						t.Fatalf("unhandled workflow expression %q", expression)
					}

					env = append(env, key+"="+value)
				}

				got := box.run("forge-ci() { \"${FORGE_CI_BIN}\" \"$@\"; }\n"+selected.Run, env...)
				if got.status != 0 {
					t.Fatalf("%s: status=%d: %s", selected.Name, got.status, got.stderr)
				}

				if selected.Name == resolve.Name && box.output() != "name="+tc.want+"\n" {
					t.Fatalf("output=%q, want %s", box.output(), tc.want)
				}
			}
		})
	}
}
