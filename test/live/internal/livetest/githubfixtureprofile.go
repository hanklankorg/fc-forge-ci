// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diggsweden/forge-ci/internal/domain/errs"
)

// ValidateGitHubGPGFixtureProfile admits the declared Go GPG fixture before the
// ordinary consumer entry reads release credentials, opens a session or stages
// keys. It reads committed blobs, just like the native staging owner below.
// Other fixtures retain their existing multi-workflow installation contract.
func ValidateGitHubGPGFixtureProfile(ctx context.Context, root, fixtures string, overrides map[string]string) error {
	git := githubStageGit()

	commit, err := git.run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	source := Consumer{git: git, stage: root, Commit: strings.TrimSpace(commit)}

	workflows, err := githubFixtureWorkflows(ctx, source, filepath.ToSlash(filepath.Join(fixtures, "workflows"))+"/")
	if err != nil {
		return err
	}

	return validateGitHubGPGFixtureSource(ctx, source, fixtures, workflows, overrides)
}

func githubFixtureWorkflows(ctx context.Context, source Consumer, prefix string) (map[string]string, error) {
	listed, err := source.git.output(ctx, source.stage, nil, "ls-tree", "-rz", "--name-only", source.Commit, "--", prefix)
	if err != nil {
		return nil, err
	}

	workflows := map[string]string{}

	for path := range strings.SplitSeq(string(listed), "\x00") {
		name := strings.TrimPrefix(path, prefix)
		if path == "" || strings.Contains(name, "/") || filepath.Ext(name) != ".yml" {
			continue
		}

		body, err := githubWorkflow(ctx, source, path)
		if err != nil {
			return nil, err
		}

		workflows[name] = body
	}

	return workflows, nil
}

const (
	githubGPGFixtures = "test/live/github-gpg"
	githubGPGConfig   = ".forge-ci/artifacts-gpg.yml"
	githubGPGWorkflow = "release-gpg.yml"
)

// githubGPGProfiles are the fixture roots admitted to the GPG config, each
// with the one workflow it installs: the GPG release and its variants, which
// sign with the trial's disposable key and write to no public log.
var githubGPGProfiles = map[string]string{
	githubGPGFixtures:               githubGPGWorkflow,
	"test/live/github-no-sboms":     "release-no-sboms.yml",
	"test/live/github-no-changelog": "release-no-changelog.yml",
}

// Profile inputs are frozen paths, not GitHub programs. This admission-local
// allowlist excludes all interpolation delimiters, shell syntax and whitespace,
// not just one expression spelling. Like validate's literalInput.refusal, quoted
// or constant ${{ ... }} is still an expression, regardless of its result. That
// helper is private to app/validate; do not change the CLI's production contract
// or try to evaluate GitHub expressions here.
var githubLiteralFixturePath = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

// Check both declared and effective inputs; an override cannot conceal a GPG
// declaration or select GPG in an unrelated installed workflow set.
func validateGitHubGPGFixtureSource(ctx context.Context, source Consumer, fixtures string, workflows map[string]string, overrides map[string]string) error {
	if _, requested := overrides["artifacts-config"]; requested {
		applied := false

		for _, body := range workflows {
			_, matched := githubInputOverrides(body, overrides)
			applied = applied || slices.Contains(matched, "artifacts-config")
		}

		if !applied {
			return fmt.Errorf("fixture config override matches no installed input: %w", errs.ErrValidation)
		}
	}

	for _, inputs := range []map[string]string{nil, overrides} {
		configs, err := validateGitHubGPGFixtureWorkflows(fixtures, workflows, inputs)
		if err != nil {
			return err
		}

		for _, config := range configs {
			// Native filesystem reads follow symlinks. Admission must not let a
			// different config name resolve through one to the fixed GPG profile.
			// Inspect committed entries, not worktree symlinks or config contents.
			parts := strings.Split(config, "/")
			for index := range parts {
				prefix := strings.Join(parts[:index+1], "/")

				entry, err := source.git.output(ctx, source.stage, nil, "ls-tree", "-z", source.Commit, "--", prefix)
				if err != nil {
					return err
				}

				if strings.HasPrefix(string(entry), "120000 ") {
					return fmt.Errorf("fixture config must not use a symlink: %s: %w", config, errs.ErrValidation)
				}
			}

			if config == githubGPGConfig {
				if _, err := githubWorkflow(ctx, source, config); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// The fixed GPG profile is bound to its own root, workflow basename and config
// path, never to arbitrary config contents. Canonical Linux repo-relative input
// paths are required: reject, rather than silently clean, native-reader aliases.
func validateGitHubGPGFixtureWorkflows(fixtures string, workflows map[string]string, overrides map[string]string) ([]string, error) {
	if !githubCanonicalConfigPath(fixtures) {
		return nil, fmt.Errorf("fixture directory must be canonical repo-relative: %w", errs.ErrValidation)
	}

	gpgWorkflow, gpg := githubGPGProfiles[fixtures]
	if gpg && (len(workflows) != 1 || workflows[gpgWorkflow] == "") {
		return nil, fmt.Errorf("native Go GPG fixture must install exactly %s: %w", gpgWorkflow, errs.ErrValidation)
	}

	var configs []string

	for name, body := range workflows {
		body, _ = githubInputOverrides(body, overrides)

		selected, err := githubFixtureConfigs(name, body)
		if err != nil {
			return nil, err
		}

		selectedGPG := false

		for _, config := range selected {
			if (config == githubGPGConfig && !gpg) || (gpg && config != githubGPGConfig) {
				return nil, fmt.Errorf("fixture %s config does not match its admitted profile root: %w", name, errs.ErrValidation)
			}

			selectedGPG = config == githubGPGConfig
			configs = append(configs, config)
		}

		if (gpg || name == githubGPGWorkflow) && !selectedGPG {
			return nil, fmt.Errorf("fixture %s must select the declared GPG config in its own root: %w", name, errs.ErrValidation)
		}
	}

	return configs, nil
}

func githubFixtureConfigs(name, body string) ([]string, error) {
	var workflow struct {
		Jobs map[string]struct {
			With  map[string]any `yaml:"with"`
			Steps []struct {
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}

	if err := yaml.Unmarshal([]byte(body), &workflow); err != nil {
		return nil, fmt.Errorf("fixture %s: %w", name, err)
	}

	var configs []string

	for _, job := range workflow.Jobs {
		inputs := []map[string]any{job.With}
		for _, step := range job.Steps {
			inputs = append(inputs, step.With)
		}

		for _, with := range inputs {
			value, exists := with["artifacts-config"]
			if !exists {
				continue
			}

			config, ok := value.(string)
			if !ok || !githubCanonicalConfigPath(config) {
				return nil, fmt.Errorf("fixture %s config must be a literal canonical repo-relative path: %w", name, errs.ErrValidation)
			}

			configs = append(configs, config)
		}
	}

	return configs, nil
}

func githubCanonicalConfigPath(value string) bool {
	// Backslash is a literal Linux filename character, not a separator. Do not
	// accept Windows-shaped paths as an alternate spelling of this fixed profile.
	return githubLiteralFixturePath.MatchString(value) && value != "." && fs.ValidPath(value) && path.Clean(value) == value
}
