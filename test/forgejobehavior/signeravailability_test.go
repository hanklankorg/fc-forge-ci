// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"slices"
	"strings"
	"testing"
)

func TestPrepareSignerImageAvailability(t *testing.T) {
	t.Parallel()

	const image = "registry.example/owner/signer@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	probe := workflowStep(t, "prepare-release.yml", "render", "Check signer image availability")
	coordinates := workflowStep(t, "prepare-release.yml", "coordinates", "Validate trust coordinate")

	if probe.Env["SIGNER_IMAGE"] != "${{ needs.coordinates.outputs.signer_image }}" {
		t.Fatal("availability probe must consume the validated image coordinate")
	}

	for _, tc := range []struct {
		name, image, skip, engineStatus string
		wantStatus                      int
		wantLookup                      bool
	}{
		{"public image", image, "false", "0", 0, true},
		{"missing or unreadable image", image, "false", "66", 66, true},
		{"explicit private image", image, "true", "66", 0, false},
		{"invalid skip", image, "yes", "0", 1, false},
		{"missing image", "", "false", "0", 1, false},
		{"mutable private image", "registry.example/owner/signer:latest", "true", "0", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			box := newSandbox(t, "prepare-release")
			box.write(".forge-ci-helpers/.forgejo/bin/lib-forge-ci.sh", fakeResolver, 0o600)
			box.write(".forge-ci-helpers/.forgejo/bin/forge-ci", fakeEngine, 0o700)

			admitted := box.run(coordinates.Run, "HELPERS_REPOSITORY_INPUT=owner/engine",
				"SERVER_URL=https://forge.example", "SIGNER_IMAGE_INPUT="+tc.image)
			if admitted.status != 0 {
				if tc.wantStatus != admitted.status || tc.wantLookup {
					t.Fatalf("coordinate admission failed unexpectedly: %s", admitted.stderr)
				}

				return
			}

			got := box.run(probe.Run, "SIGNER_IMAGE="+tc.image,
				"SKIP_SIGNER_IMAGE_CHECK="+tc.skip, "FAKE_ENGINE_STATUS="+tc.engineStatus)
			if got.status != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", got.status, tc.wantStatus, got.stderr)
			}

			if !tc.wantLookup {
				if argv := box.argv(); argv != nil {
					t.Fatalf("a refused or explicitly skipped probe reached the registry: %v", argv)
				}

				return
			}

			want := []string{"container", "manifest", "digest", "--ref", image, "--anonymous", "--retry-attempts", "1"}
			if argv := box.argv(); !slices.Equal(argv, want) {
				t.Fatalf("argv = %v, want %v", argv, want)
			}
		})
	}

	var workflow prepareReleaseWorkflow
	loadYAML(t, ".forgejo/workflows/prepare-release.yml", &workflow)

	index := map[string]int{}
	for idx, step := range workflow.Jobs["render"].Steps {
		index[step.Name] = idx
	}

	if index["Check required secrets"] >= index[probe.Name] ||
		index["Fetch engine binary"] >= index[probe.Name] ||
		index[probe.Name] >= index["Checkout consumer repository at release request tag"] {
		t.Fatal("availability must follow presence/bootstrap checks and precede consumer checkout")
	}

	for _, value := range probe.Env {
		if strings.Contains(value, "secrets.") {
			t.Fatal("the anonymous availability probe receives a secret")
		}
	}
}
