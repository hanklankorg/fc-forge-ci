// SPDX-FileCopyrightText: The Forge CI Authors
//
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build linux

package forgejobehavior

import (
	"slices"
	"testing"

	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The builder is the repository's choice, made by CI_IMAGE_BUILDER: Buildah is
// set up as setup-buildah does it, and the kaniko job image is admitted only by
// digest and by its labels, so no workflow has to know which builder runs.
func TestSetupImageBuilder(t *testing.T) {
	t.Parallel()
	action := loadAction(t, "setup-image-builder")
	action.requireSteps(t, 1)
	script := action.Runs.Steps[0].Run

	t.Run("binds the package list to its input", func(t *testing.T) {
		t.Parallel()

		action.requireStepEnv(t, 0, map[string]string{
			"SETUP_BUILDAH_EXTRA_PACKAGES": "${{ inputs.extra-packages }}",
		})
	})

	const jobImage = "ghcr.io/acme/forge-ci-kaniko-job@sha256:4b6e012f34c3c416062a48779f6eee49c993157e6efcb7577e96aafdbe09f82e"

	for _, test := range []struct {
		name string
		env  []string
		argv []string
	}{
		{
			"buildah by default, as setup-buildah",
			[]string{"SETUP_BUILDAH_EXTRA_PACKAGES=jq skopeo"},
			[]string{
				"container", "setup-image-builder", "--image-builder", "buildah", "--job-image", "", "--engine-repository", "", "--install-packages=true", "--probe-build=true", "--print-store=false",
				"--summary=false", "--extra-packages", "jq skopeo",
			},
		},
		{
			"kaniko admits the job image by digest and labels, for an action fetched by URL",
			[]string{
				"CI_IMAGE_BUILDER=kaniko", "CI_IMAGE_BUILDER_JOB_IMAGE=" + jobImage,
				"GITHUB_SERVER_URL=https://forge.example/", "GITHUB_ACTION_REPOSITORY=acme/forge-ci",
			},
			[]string{
				"container", "setup-image-builder", "--image-builder", "kaniko", "--job-image", jobImage, "--engine-repository", "https://forge.example/acme/forge-ci",
				"--install-packages=true", "--probe-build=true", "--print-store=false", "--summary=false", "--extra-packages", "",
			},
		},
		{
			"kaniko names the engine by FORGE_CI_REPOSITORY_URL for a checked-out layer",
			[]string{
				"CI_IMAGE_BUILDER=kaniko", "CI_IMAGE_BUILDER_JOB_IMAGE=" + jobImage,
				"FORGE_CI_REPOSITORY_URL=https://forge.example/acme/forge-ci",
			},
			[]string{
				"container", "setup-image-builder", "--image-builder", "kaniko", "--job-image", jobImage, "--engine-repository", "https://forge.example/acme/forge-ci",
				"--install-packages=true", "--probe-build=true", "--print-store=false", "--summary=false", "--extra-packages", "",
			},
		},
		{
			"kaniko names the engine without the .git of a validated clone URL",
			[]string{
				"CI_IMAGE_BUILDER=kaniko", "CI_IMAGE_BUILDER_JOB_IMAGE=" + jobImage,
				"FORGE_CI_REPOSITORY_URL=https://forge.example/acme/forge-ci.git",
			},
			[]string{
				"container", "setup-image-builder", "--image-builder", "kaniko", "--job-image", jobImage, "--engine-repository", "https://forge.example/acme/forge-ci.git",
				"--install-packages=true", "--probe-build=true", "--print-store=false", "--summary=false", "--extra-packages", "",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			s := newSandbox(t, "setup-image-builder")
			s.write(".forgejo/bin/setup-image-builder.sh", string(reporoot.ReadFile(t, ".forgejo/bin/setup-image-builder.sh")), 0o600)

			got := s.run(script, test.env...)
			if got.status != 0 || !slices.Equal(s.argv(), test.argv) {
				t.Fatalf("status %d argv %q; stderr %s", got.status, s.argv(), got.stderr)
			}
		})
	}

	t.Run("preserves the Go owner's refusal status", func(t *testing.T) {
		t.Parallel()

		s := newSandbox(t, "setup-image-builder")
		s.write(".forgejo/bin/setup-image-builder.sh", string(reporoot.ReadFile(t, ".forgejo/bin/setup-image-builder.sh")), 0o600)

		got := s.run(script, "CI_IMAGE_BUILDER=unknown", "FAKE_ENGINE_STATUS=17")
		if got.status != 17 {
			t.Fatalf("Go refusal status was lost: %d; %s", got.status, got.stderr)
		}
	})
}
