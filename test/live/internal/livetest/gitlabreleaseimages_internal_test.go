// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	urfavecli "github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/cli"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/testutil/reporoot"
)

// The harness's default declaration is the one a consumer gets from the GitLab
// release component with artifacts-config left empty: the file the input's
// description names, and the file the engine's plan reads for an empty --file.
// A harness default of its own would test a release no consumer runs.
func TestGitLabReleaseImagesDefaultIsTheComponents(t *testing.T) {
	t.Parallel()

	var spec struct {
		Spec struct {
			Inputs map[string]struct {
				Description string    `yaml:"description"`
				Default     yaml.Node `yaml:"default"`
			} `yaml:"inputs"`
		} `yaml:"spec"`
	}

	if err := yaml.NewDecoder(bytes.NewReader(reporoot.ReadFile(t, "templates/release.yml"))).Decode(&spec); err != nil {
		t.Fatal(err)
	}

	input, ok := spec.Spec.Inputs["artifacts-config"]
	if !ok || input.Default.Kind != yaml.ScalarNode || input.Default.Value != "" {
		t.Fatalf("templates/release.yml artifacts-config: want an input defaulting to empty, got %q", input.Default.Value)
	}

	documented := ""
	if match := regexp.MustCompile(`Empty reads (\S+)\.`).FindStringSubmatch(input.Description); match != nil {
		documented = match[1]
	}

	if documented != DefaultArtifactsConfig {
		t.Errorf("templates/release.yml documents %q as the empty artifacts-config's file, the harness defaults to %q",
			documented, DefaultArtifactsConfig)
	}

	if engine := releaseImagesPlanFileDefault(t); engine != DefaultArtifactsConfig {
		t.Errorf("container release-images plan --file defaults to %q, the harness to %q", engine, DefaultArtifactsConfig)
	}
}

func releaseImagesPlanFileDefault(t *testing.T) string {
	t.Helper()

	cmd := cli.New(cli.BuildInfo{Version: "dev"})
	for _, name := range []string{"container", "release-images", "plan"} {
		cmd = cmd.Command(name)
		if cmd == nil {
			t.Fatalf("no %q command on the way to container release-images plan", name)
		}
	}

	for _, flag := range cmd.Flags {
		if file, ok := flag.(*urfavecli.StringFlag); ok && file.Name == "file" {
			return file.Value
		}
	}

	t.Fatal("container release-images plan has no --file flag")

	return ""
}

func TestGitLabReleaseImagesPicksOneDeclaration(t *testing.T) {
	t.Parallel()

	const (
		plain     = "containers:\n  - name: app\n"
		extracted = "artifacts:\n  - name: app\n    project-type: cargo\ncontainers:\n  - name: app\n    from: [app]\n" +
			"    extract:\n      binary:\n        target: export\n        names: [app]\n"
		noImages = "artifacts:\n  - name: app\n    project-type: npm\n"
	)

	for name, tc := range map[string]struct {
		def, dedicated string
		images         bool
		config         string
		refused        bool
	}{
		"neither":                     {},
		"default without containers":  {def: noImages},
		"default with containers":     {def: plain, images: true},
		"dedicated beside extract":    {def: extracted, dedicated: plain, images: true, config: GitLabDedicatedReleaseImagesConfig},
		"dedicated beside a default":  {def: plain, dedicated: plain, refused: true},
		"dedicated beside no images":  {def: noImages, dedicated: plain, refused: true},
		"dedicated without a default": {dedicated: plain, refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			write := func(path, body string) {
				if body == "" {
					return
				}

				if err := os.MkdirAll(filepath.Join(root, ".forge-ci"), 0o750); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			write(DefaultArtifactsConfig, tc.def)
			write(GitLabDedicatedReleaseImagesConfig, tc.dedicated)

			images, config, err := GitLabReleaseImages(root)
			if tc.refused {
				if !errors.Is(err, errs.ErrInvalidConfig) {
					t.Fatalf("want a refusal of two declarations, got %v", err)
				}

				return
			}

			if err != nil || images != tc.images || config != tc.config {
				t.Fatalf("got images=%t config=%q err=%v, want images=%t config=%q", images, config, err, tc.images, tc.config)
			}
		})
	}
}

// Every GitLab release demo declares its release images once: in
// artifacts.yml, or in release-images.yml only where artifacts.yml carries a
// shape release images refuse. Its Forgejo release reads the same file.
func TestGitLabReleaseDemosDeclareTheirImagesOnce(t *testing.T) {
	t.Parallel()

	root := filepath.Join(reporoot.Path(t), "test", "live", "fixtures", "consumers")

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	released := 0

	for _, entry := range entries {
		consumer := filepath.Join(root, entry.Name(), "consumer")
		if _, err := os.Stat(filepath.Join(consumer, GitLabConsumerBuildScript)); err != nil {
			continue
		}

		released++

		images, config, err := GitLabReleaseImages(consumer)
		if err != nil {
			t.Errorf("%s: %v", entry.Name(), err)

			continue
		}

		forgejo := filepath.Join(consumer, ".forgejo", "workflows", "release.yml")
		if _, err := os.Stat(forgejo); !images || err != nil {
			continue
		}

		if forgejoArtifactsConfig(t, forgejo, "release-images.yml") != config {
			t.Errorf("%s: the GitLab release reads %q (empty is the default), its Forgejo release another declaration",
				entry.Name(), config)
		}
	}

	if released == 0 {
		t.Fatal("no GitLab release demo found; the walk, not the demos, was measured")
	}
}

// forgejoArtifactsConfig is the artifacts-config input a Forgejo release
// workflow passes the layer's called workflow, "" for the default.
func forgejoArtifactsConfig(t *testing.T, path, called string) string {
	t.Helper()

	artifactsConfig, err := forgejoCalledArtifactsConfig(path, called)
	if err != nil {
		t.Fatal(err)
	}

	return artifactsConfig
}
