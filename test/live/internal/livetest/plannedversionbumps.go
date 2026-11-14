// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	appconfig "github.com/diggsweden/forge-ci/internal/app/config"
	"github.com/diggsweden/forge-ci/internal/domain/config"
	"github.com/diggsweden/forge-ci/internal/domain/errs"
	"github.com/diggsweden/forge-ci/internal/domain/output"
	"github.com/diggsweden/forge-ci/internal/domain/pipeline"
)

// PlannedVersionBumps are the projects a consumer's GitLab or Forgejo release
// bumps: the release plan of the declaration its artifacts-config names
// (artifactsConfig, empty for the default, the same file on both forges), as
// the release's own plan job reads it. Without a declaration nothing is
// bumped.
func PlannedVersionBumps(consumerRoot, artifactsConfig string) ([]pipeline.PlannedArtifact, error) {
	if consumerRoot == "" {
		return nil, nil
	}

	plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{
		Path: filepath.Join(consumerRoot, cmp.Or(artifactsConfig, DefaultArtifactsConfig)),
	}, output.Annotator{}, artifactsConfig == "")
	if err != nil {
		return nil, err
	}

	if !plans.Prepare.Targets.VersionBump.Runs {
		return nil, nil
	}

	return plans.Prepare.Targets.VersionBump.Items, nil
}

// ForgejoPlannedVersionBumps are the projects a consumer's Forgejo release
// bumps: PlannedVersionBumps of the declaration its release workflow (a file
// name under .forgejo/workflows) passes the layer's prepare-release.yml.
func ForgejoPlannedVersionBumps(consumerRoot, workflow string) ([]pipeline.PlannedArtifact, error) {
	artifactsConfig, err := forgejoCalledArtifactsConfig(filepath.Join(consumerRoot, ".forgejo", "workflows", workflow), "prepare-release.yml")
	if err != nil {
		return nil, err
	}

	return PlannedVersionBumps(consumerRoot, artifactsConfig)
}

// PlannedNPMPackages are the npm packages a consumer's release publishes to
// the forge's own package registry, by the name of each package.json: the
// npm projects that list forge-packages under publish-to in the declaration
// its artifacts-config names (artifactsConfig, empty for the default).
func PlannedNPMPackages(consumerRoot, artifactsConfig string) ([]string, error) {
	plans, err := appconfig.LoadReleaseStagePlans(appconfig.EmitConfigPlanInput{
		Path: filepath.Join(consumerRoot, cmp.Or(artifactsConfig, DefaultArtifactsConfig)),
	}, output.Annotator{}, artifactsConfig == "")
	if err != nil {
		return nil, err
	}

	var names []string

	for _, item := range plans.Build.Targets.NPM.Items {
		if !slices.Contains(item.PublishTo, config.PublishForgePackages) {
			continue
		}

		name, err := npmPackageName(filepath.Join(consumerRoot, item.WorkingDirectory))
		if err != nil {
			return nil, err
		}

		names = append(names, name)
	}

	return names, nil
}

// npmPackageName is the name the package.json of the npm project at dir
// carries.
func npmPackageName(dir string) (string, error) {
	file := filepath.Join(dir, "package.json")

	body, err := os.ReadFile(file) //nolint:gosec // G304: a declared project of the consumer checkout the caller names
	if err != nil {
		return "", err
	}

	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil || manifest.Name == "" {
		return "", fmt.Errorf("%s names no package: %w", file, errs.ErrMalformedInput)
	}

	return manifest.Name, nil
}

// ForgejoPlannedNPMPackages are the npm packages a consumer's Forgejo release
// publishes to the owner's package registry. The layer has no package job,
// so the consumer's release workflow (a file name under .forgejo/workflows)
// is the plan: PlannedNPMPackages of the declaration it passes
// prepare-release.yml when it runs `forge-ci publish forge-packages deploy`,
// and none when it does not.
func ForgejoPlannedNPMPackages(consumerRoot, workflow string) ([]string, error) {
	path := filepath.Join(consumerRoot, ".forgejo", "workflows", workflow)

	body, err := os.ReadFile(path) //nolint:gosec // G304: the workflow of the consumer checkout the caller names
	if err != nil {
		return nil, fmt.Errorf("read the release workflow: %w", err)
	}

	if !strings.Contains(string(body), "forge-ci publish forge-packages deploy") {
		return nil, nil
	}

	artifactsConfig, err := forgejoCalledArtifactsConfig(path, "prepare-release.yml")
	if err != nil {
		return nil, err
	}

	return PlannedNPMPackages(consumerRoot, artifactsConfig)
}

// forgejoCalledArtifactsConfig is the artifacts-config input a Forgejo
// workflow passes the layer's called workflow, "" for the default.
func forgejoCalledArtifactsConfig(path, called string) (string, error) {
	body, err := os.ReadFile(path) //nolint:gosec // G304: the workflow of the consumer checkout the caller names
	if err != nil {
		return "", fmt.Errorf("read the release workflow: %w", err)
	}

	var workflow struct {
		Jobs map[string]struct {
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}

	for _, job := range workflow.Jobs {
		if strings.Contains(job.Uses, "/.forgejo/workflows/"+called+"@") {
			return job.With["artifacts-config"], nil
		}
	}

	return "", fmt.Errorf("%s calls no %s: %w", path, called, os.ErrNotExist)
}
