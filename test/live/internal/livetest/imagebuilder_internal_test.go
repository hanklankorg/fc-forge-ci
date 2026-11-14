// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/provider"
)

// The trial labels a kaniko job image with the layer it runs from, and only an
// image titled as containers/kaniko-job titles it, or one built from it.
func TestLabelKanikoJobImageNamesTheLayerOfAKanikoJobImageOnly(t *testing.T) {
	t.Parallel()

	base, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}

	file, err := base.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}

	config := file.Config
	config.Labels = map[string]string{
		"org.opencontainers.image.title":  kanikoJobImageTitle,
		"org.opencontainers.image.source": "https://forgejo.invalid/forgeci/fci-consumer-trial-layer",
	}

	kaniko, err := mutate.Config(base, config)
	if err != nil {
		t.Fatal(err)
	}

	const source = "https://gitlab.invalid/forgeci/fci-release-engine"

	labelled, err := labelKanikoJobImage(kaniko, source)
	if err != nil {
		t.Fatal(err)
	}

	got, err := labelled.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}

	if got.Config.Labels["org.opencontainers.image.source"] != source || got.Config.Labels["org.opencontainers.image.title"] != kanikoJobImageTitle {
		t.Fatalf("labels = %v", got.Config.Labels)
	}

	if original, _ := kaniko.ConfigFile(); original.Config.Labels["org.opencontainers.image.source"] == source {
		t.Fatal("labelling changed the archive's own image")
	}

	if _, err := labelKanikoJobImage(base, source); !errors.Is(err, errs.ErrValidation) {
		t.Fatalf("an image that is not a kaniko job image was labelled: %v", err)
	}
}

func TestGitLabKanikoReadsTheTrialsSetting(t *testing.T) {
	for value, want := range map[string]bool{"": false, "buildah": false, "kaniko": true} {
		t.Setenv(GitLabImageBuilderEnv, value)

		if got := GitLabKaniko(t); got != want {
			t.Errorf("%s=%q: GitLabKaniko() = %v, want %v", GitLabImageBuilderEnv, value, got, want)
		}
	}
}

func TestSelectedImageBuilderReadsEachForgesSetting(t *testing.T) {
	t.Setenv(GitLabImageBuilderEnv, ImageBuilderKaniko)
	t.Setenv(ForgejoImageBuilderEnv, "")

	if got := SelectedImageBuilder(t, provider.ForgeGitLab); got != ImageBuilderKaniko {
		t.Errorf("GitLab builder = %q, want kaniko", got)
	}

	if got := SelectedImageBuilder(t, provider.ForgeForgejo); got != ImageBuilderBuildah {
		t.Errorf("Forgejo builder = %q, want buildah: the GitLab setting must not select it", got)
	}

	t.Setenv(ForgejoImageBuilderEnv, ImageBuilderKaniko)

	if got := SelectedImageBuilder(t, provider.ForgeForgejo); got != ImageBuilderKaniko {
		t.Errorf("Forgejo builder = %q, want kaniko", got)
	}

	if env := ImageBuilderEnv(provider.ForgeGitHub); env != "" {
		t.Errorf("GitHub has no live builder setting, got %q", env)
	}
}

func TestKanikoDevelopmentAdmissionRefusesBuildahOnlyRecipes(t *testing.T) {
	t.Parallel()

	const workflow = `env:
  CI_IMAGE_BUILDER: ${{ vars.CI_IMAGE_BUILDER }}
  CI_IMAGE_BUILDER_JOB_IMAGE: ${{ vars.CI_IMAGE_BUILDER_JOB_IMAGE }}
jobs:
  development:
    name: "Check: development container"
    container:
      image: ${{ vars.CI_IMAGE_BUILDER_JOB_IMAGE }}
    steps:
      - uses: https://forge.invalid/owner/engine/.forgejo/actions/setup-image-builder@pin
`

	for _, tc := range []struct {
		name, old, replacement string
		valid                  bool
	}{
		{name: "selected job container", valid: true},
		{name: "scalar container", old: "container:\n      image:", replacement: "container:", valid: true},
		{name: "compact expression", old: "CI_IMAGE_BUILDER: ${{ vars.CI_IMAGE_BUILDER }}", replacement: "CI_IMAGE_BUILDER: ${{vars.CI_IMAGE_BUILDER}}", valid: true},
		{name: "Buildah setup", old: "actions/setup-image-builder@", replacement: "actions/setup-buildah@"},
		{name: "missing setup", old: "actions/setup-image-builder@", replacement: "actions/other@"},
		{name: "fixed builder", old: "CI_IMAGE_BUILDER: ${{ vars.CI_IMAGE_BUILDER }}", replacement: "CI_IMAGE_BUILDER: buildah"},
		{name: "missing image environment", old: "  CI_IMAGE_BUILDER_JOB_IMAGE: ${{ vars.CI_IMAGE_BUILDER_JOB_IMAGE }}\n"},
		{name: "fixed container", old: "image: ${{ vars.CI_IMAGE_BUILDER_JOB_IMAGE }}", replacement: "image: registry.invalid/buildah@sha256:abc"},
		{name: "different job", old: "Check: development container", replacement: "other job"},
		{name: "job overrides selection", old: "    steps:", replacement: "    env:\n      CI_IMAGE_BUILDER: buildah\n    steps:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			input := workflow
			if tc.old != "" {
				input = strings.Replace(input, tc.old, tc.replacement, 1)
			}

			err := ValidateKanikoDevelopmentJob(input, "Check: development container", "CI_IMAGE_BUILDER_JOB_IMAGE")
			if tc.valid && err != nil {
				t.Fatal(err)
			}

			if !tc.valid && !errors.Is(err, errs.ErrValidation) {
				t.Fatalf("incompatible recipe was not refused before a live trial: %v", err)
			}
		})
	}

	// A combined Rust/kaniko job image is supplied under the recipe's existing
	// toolchain variable as well as CI_IMAGE_BUILDER_JOB_IMAGE.
	rust := strings.Replace(workflow, "image: ${{ vars.CI_IMAGE_BUILDER_JOB_IMAGE }}", "image: ${{ vars.CI_RUST_BUILD_IMAGE }}", 1)
	if err := ValidateKanikoDevelopmentJob(rust, "Check: development container", "CI_RUST_BUILD_IMAGE"); err != nil {
		t.Fatal(err)
	}
}

// A Forgejo trial builds with the builder its settings name, buildah when
// they name none. A builder that is neither, and a run selector that names
// the other one, are refused before any resource exists.
func TestForgejoConsumerImageBuilder(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ variable, selector, want string }{
		{"", "", ImageBuilderBuildah},
		{ImageBuilderKaniko, "", ImageBuilderKaniko},
		{ImageBuilderKaniko, ImageBuilderKaniko, ImageBuilderKaniko},
		{"", ImageBuilderBuildah, ImageBuilderBuildah},
		{"docker", "", ""},
		{ImageBuilderKaniko, ImageBuilderBuildah, ""},
		{"", ImageBuilderKaniko, ""},
	} {
		variables := map[string]string{}
		if tc.variable != "" {
			variables["CI_IMAGE_BUILDER"] = tc.variable
		}

		got, err := ForgejoConsumerImageBuilder(variables, tc.selector)
		if got != tc.want || (tc.want == "") != errors.Is(err, errs.ErrValidation) {
			t.Errorf("CI_IMAGE_BUILDER=%q with selector %q: %q, %v; want %q", tc.variable, tc.selector, got, err, tc.want)
		}
	}
}

// A role whose archive is pushed already takes that image, and makes no
// project of its own: the target is never accepted, so a push would fail.
func TestJobImagesPushTakesTheImageOfAnArchivePushedAlready(t *testing.T) {
	images := JobImages{"/prepared/java.tar": "registry.example/fci-job@sha256:1"}

	got := images.Push(t, Target{}, "/prepared/java.tar", func() string {
		t.Fatal("a pushed archive made a project of its own")

		return ""
	})
	if got != "registry.example/fci-job@sha256:1" {
		t.Fatalf("Push = %q, want the image pushed already", got)
	}
}
